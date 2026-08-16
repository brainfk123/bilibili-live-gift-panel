package roomsource

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
)

func TestManagerSharesCanonicalRoomAndClosesOnLastCancel(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"7": "42", "42": "42"})
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)

	sinks := []*recordingSink{newRecordingSink(), newRecordingSink(), newRecordingSink()}
	roomIDs := []string{"7", "42", "7"}
	subscriptions := make([]Subscription, len(sinks))
	for index := range sinks {
		subscription, err := manager.Subscribe(context.Background(), roomIDs[index], int64(index+1), sinks[index])
		if err != nil {
			t.Fatalf("Subscribe(%q): %v", roomIDs[index], err)
		}
		subscriptions[index] = subscription
	}

	connection := gateway.onlyConnection(t)
	if got := gateway.openedRooms(); len(got) != 1 || got[0] != "42" {
		t.Fatalf("OpenRoom calls = %v, want [42]", got)
	}

	connection.emit(biligateway.Event{Type: "gift", Data: []byte("payload")})
	for index, sink := range sinks {
		event := sink.nextEvent(t)
		if string(event.Data) != "payload" {
			t.Fatalf("sink %d data = %q", index, event.Data)
		}
		if !event.Viewer.Ephemeral {
			t.Fatalf("sink %d viewer was not marked ephemeral", index)
		}
		event.Data[0] = byte('0' + index)
	}

	subscriptions[0].Cancel()
	if got := connection.closeCount(); got != 0 {
		t.Fatalf("close count after first cancel = %d, want 0", got)
	}
	subscriptions[1].Cancel()
	if got := connection.closeCount(); got != 0 {
		t.Fatalf("close count after second cancel = %d, want 0", got)
	}
	subscriptions[2].Cancel()
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("last cancel did not finish closing the shared source")
	}
	if err := subscriptions[2].Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := connection.closeCount(); got != 1 {
		t.Fatalf("close count after last cancel = %d, want 1", got)
	}
	subscriptions[2].Cancel()
	if got := connection.closeCount(); got != 1 {
		t.Fatalf("close count after repeated cancel = %d, want 1", got)
	}
}

func TestManagerDetachesBackpressuredSubscriberWithoutBlockingHealthySubscriber(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"42": "42"})
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)

	slow := newBlockingSink()
	healthy := newRecordingSink()
	slowSubscription, err := manager.Subscribe(context.Background(), "42", 1, slow)
	if err != nil {
		t.Fatal(err)
	}
	defer slowSubscription.Cancel()
	healthySubscription, err := manager.Subscribe(context.Background(), "42", 2, healthy)
	if err != nil {
		t.Fatal(err)
	}
	defer healthySubscription.Cancel()

	connection := gateway.onlyConnection(t)
	connection.emit(biligateway.Event{Type: "gift", Data: []byte{0}})
	slow.waitUntilBlocked(t)
	_ = healthy.nextEvent(t)
	for index := 1; index <= 257; index++ {
		connection.emit(biligateway.Event{Type: "gift", Data: []byte{byte(index)}})
		_ = healthy.nextEvent(t)
	}

	select {
	case got := <-slow.errors:
		if !errors.Is(got, ErrSubscriberBackpressure) || got.Error() != "subscriber_backpressure" {
			t.Fatalf("slow subscriber error = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("slow subscriber did not receive subscriber_backpressure")
	}
	slow.release()
	connection.emit(biligateway.Event{Type: "gift", Data: []byte("healthy")})
	if got := healthy.nextEvent(t); string(got.Data) != "healthy" {
		t.Fatalf("healthy subscriber received %q", got.Data)
	}
	if got := connection.closeCount(); got != 0 {
		t.Fatalf("shared source closed while healthy subscriber remained: %d", got)
	}
}

func TestManagerParsesSemanticApplicationEventBeforeFanout(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"7": "42"})
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	sink := newRecordingSink()
	if _, err := manager.Subscribe(context.Background(), "7", 1, sink); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"gift-rnd","uid":123,"uname":"viewer","face":"https://example.test/avatar.png"}}`)
	gateway.onlyConnection(t).emit(biligateway.Event{Type: "application", Data: payload})
	event := sink.nextEvent(t)
	if event.RoomID != "42" || event.Type != "SEND_GIFT" || event.ID != "send-gift:gift-rnd" {
		t.Fatalf("semantic envelope = room:%q type:%q id:%q", event.RoomID, event.Type, event.ID)
	}
	if event.Viewer.UID != 123 || event.Viewer.Uname != "viewer" || event.Viewer.Avatar != "https://example.test/avatar.png" || !event.Viewer.Ephemeral {
		t.Fatalf("semantic viewer = %+v", event.Viewer)
	}
	if string(event.Data) != string(payload) {
		t.Fatalf("payload changed: %q", event.Data)
	}
}

func TestEventFromGatewayUsesStableIDsForPaidEventVariants(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantID  string
	}{
		{name: "super chat", payload: `{"cmd":"SUPER_CHAT_MESSAGE","data":{"id":987}}`, wantID: "super-chat:987"},
		{name: "Japanese super chat duplicate", payload: `{"cmd":"SUPER_CHAT_MESSAGE_JPN:1","data":{"id":"987"}}`, wantID: "super-chat:987"},
		{name: "guard string order", payload: `{"cmd":"GUARD_BUY","data":{"order_id":"guard-order"}}`, wantID: "guard:guard-order"},
		{name: "guard numeric order", payload: `{"cmd":"GUARD_BUY","data":{"order_id":123}}`, wantID: "guard:123"},
		{name: "guard id fallback", payload: `{"cmd":"GUARD_BUY","data":{"id":456}}`, wantID: "guard:456"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := eventFromGateway("42", biligateway.Event{Type: "application", Data: []byte(test.payload)})
			if event.ID != test.wantID {
				t.Fatalf("stable ID = %q, want %q", event.ID, test.wantID)
			}
		})
	}
}

func TestManagerWaitsForSharedInitialOpenFailure(t *testing.T) {
	wantErr := errors.New("open_failed")
	gateway := newBlockingOpenGateway(wantErr)
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)

	type result struct {
		subscription Subscription
		err          error
	}
	results := make(chan result, 2)
	subscribe := func(accountID int64) {
		subscription, err := manager.Subscribe(context.Background(), "42", accountID, newRecordingSink())
		results <- result{subscription: subscription, err: err}
	}
	go subscribe(1)
	gateway.waitUntilOpening(t)
	go subscribe(2)

	select {
	case got := <-results:
		t.Fatalf("Subscribe returned before shared OpenRoom resolved: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	gateway.release()
	for range 2 {
		got := <-results
		if got.subscription != nil || !errors.Is(got.err, wantErr) {
			t.Fatalf("Subscribe result = (%v, %v), want (nil, %v)", got.subscription, got.err, wantErr)
		}
	}
	if got := gateway.openCount(); got != 1 {
		t.Fatalf("OpenRoom count = %d, want 1", got)
	}
}

func TestManagerCreatorContextCanCancelWhileSharedInitialOpenContinues(t *testing.T) {
	gateway := newBlockingOpenGateway(nil)
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	creatorContext, cancelCreator := context.WithCancel(context.Background())
	type result struct {
		subscription Subscription
		err          error
	}
	creatorResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	go func() {
		subscription, err := manager.Subscribe(creatorContext, "42", 1, newRecordingSink())
		creatorResult <- result{subscription: subscription, err: err}
	}()
	gateway.waitUntilOpening(t)
	go func() {
		subscription, err := manager.Subscribe(context.Background(), "42", 2, newRecordingSink())
		secondResult <- result{subscription: subscription, err: err}
	}()
	gateway.waitForRoomInfoCalls(t, 2)
	cancelCreator()
	select {
	case got := <-creatorResult:
		if got.subscription != nil || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("creator result = (%v, %v)", got.subscription, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("creator Subscribe remained blocked in shared OpenRoom after its context was canceled")
	}
	gateway.release()
	got := <-secondResult
	if got.err != nil || got.subscription == nil {
		t.Fatalf("second result = (%v, %v)", got.subscription, got.err)
	}
	got.subscription.Cancel()
}

func TestManagerWaitIncludesInitialWorkerBeforeItBeginsOpen(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"42": "42"})
	manager := NewManager(gateway, Options{})
	launchStarted := make(chan struct{})
	releaseLaunch := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLaunch) }) }
	defer release()
	manager.launchInitial = func(worker func()) {
		close(launchStarted)
		<-releaseLaunch
		go worker()
	}

	subscribeResult := make(chan error, 1)
	go func() {
		_, err := manager.Subscribe(context.Background(), "42", 1, newRecordingSink())
		subscribeResult <- err
	}()
	select {
	case <-launchStarted:
	case <-time.After(time.Second):
		t.Fatal("initial worker launch did not reach gate")
	}
	manager.Close()

	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := manager.Wait(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Manager.Wait before initial worker began = %v, want deadline exceeded", err)
	}

	release()
	finishedContext, cancelFinished := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinished()
	if err := manager.Wait(finishedContext); err != nil {
		t.Fatalf("Manager.Wait after initial worker exited: %v", err)
	}
	if err := <-subscribeResult; !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("Subscribe after manager close = %v", err)
	}
}

func TestManagerConcurrentSubscribeCancelKeepsOneCanonicalSource(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"7": "42", "42": "42"})
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)

	const subscriberCount = 64
	subscriptions := make([]Subscription, subscriberCount)
	errorsBySubscriber := make([]error, subscriberCount)
	var subscribeGroup sync.WaitGroup
	for index := range subscriberCount {
		subscribeGroup.Add(1)
		go func(index int) {
			defer subscribeGroup.Done()
			roomID := "42"
			if index%2 == 0 {
				roomID = "7"
			}
			subscriptions[index], errorsBySubscriber[index] = manager.Subscribe(context.Background(), roomID, int64(index+1), newRecordingSink())
		}(index)
	}
	subscribeGroup.Wait()
	for index, err := range errorsBySubscriber {
		if err != nil {
			t.Fatalf("subscriber %d: %v", index, err)
		}
	}
	connection := gateway.onlyConnection(t)

	var cancelGroup sync.WaitGroup
	for _, subscription := range subscriptions {
		cancelGroup.Add(1)
		go func(subscription Subscription) {
			defer cancelGroup.Done()
			subscription.Cancel()
			subscription.Cancel()
		}(subscription)
	}
	cancelGroup.Wait()
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("concurrent last cancel did not finish closing the source")
	}
	if got := gateway.openedRooms(); len(got) != 1 || got[0] != "42" {
		t.Fatalf("OpenRoom calls = %v, want [42]", got)
	}
	if got := connection.closeCount(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
}

func TestManagerUsesTrustedAccountScopeForResolutionAndOpen(t *testing.T) {
	upstream := &controlledUpstream{}
	controlled := biligateway.NewControlledGateway(upstream, controlledCredentialLoader{}, biligateway.GatewayOptions{})
	manager := NewManager(controlled, Options{})
	t.Cleanup(manager.Close)

	subscription, err := manager.Subscribe(context.Background(), "0007", 91, newRecordingSink())
	if err != nil {
		t.Fatalf("Subscribe through account-scoped ControlledGateway: %v", err)
	}
	subscription.Cancel()
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if upstream.roomInfoCalls != 1 || upstream.openCalls != 1 || upstream.openRoom != "42" {
		t.Fatalf("controlled upstream calls = roomInfo:%d open:%d room:%q", upstream.roomInfoCalls, upstream.openCalls, upstream.openRoom)
	}
}

func TestCloneEventDeepCopiesAllMutableFieldsAndMarksViewerEphemeral(t *testing.T) {
	original := Event{
		ID: "event-1", Type: "gift", Data: []byte("abc"),
		Viewer:   Viewer{UID: 123, Uname: "viewer", Avatar: "avatar", Metadata: map[string]string{"rank": "1"}},
		Metadata: map[string]string{"gift": "rose"},
	}
	clone := cloneEvent(original)
	clone.Data[0] = 'z'
	clone.Metadata["gift"] = "star"
	clone.Viewer.Metadata["rank"] = "2"
	if string(original.Data) != "abc" || original.Metadata["gift"] != "rose" || original.Viewer.Metadata["rank"] != "1" {
		t.Fatalf("clone mutation changed original: %+v", original)
	}
	if !clone.Viewer.Ephemeral {
		t.Fatal("clone viewer was not marked ephemeral")
	}
}

func TestManagerContextCancellationClosesLastSubscription(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"42": "42"})
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := manager.Subscribe(ctx, "42", 1, newRecordingSink()); err != nil {
		t.Fatal(err)
	}
	connection := gateway.onlyConnection(t)
	cancel()
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not close the last source")
	}
	if got := connection.closeCount(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
}

func TestSubscriptionWaitIsQuiescentBarrierForQueuedEvents(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"42": "42"})
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	sink := newCallbackBarrierSink()
	subscription, err := manager.Subscribe(context.Background(), "42", 1, sink)
	if err != nil {
		t.Fatal(err)
	}
	connection := gateway.onlyConnection(t)
	connection.emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"first"}}`)})
	sink.waitForStarted(t)
	connection.emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"queued"}}`)})

	subscription.Cancel()
	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := subscription.Wait(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait while callback blocked = %v, want deadline exceeded", err)
	}
	sink.release()
	if err := subscription.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after callback release: %v", err)
	}
	if got := sink.startedCount(); got != 1 {
		t.Fatalf("OnEvent callbacks begun = %d, want only in-flight callback and no queued callback", got)
	}
	select {
	case <-subscription.Done():
	default:
		t.Fatal("Done was not closed after Wait returned")
	}
}

func TestLastCancelIsPromptAndTombstonePreventsOverlappingUpstream(t *testing.T) {
	gateway := newBlockingCloseGateway()
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	firstSubscription, err := manager.Subscribe(context.Background(), "42", 1, newRecordingSink())
	if err != nil {
		t.Fatal(err)
	}
	firstConnection := gateway.connection(t, 0)
	defer firstConnection.releaseClose()
	cancelReturned := make(chan struct{})
	go func() {
		firstSubscription.Cancel()
		close(cancelReturned)
	}()
	firstConnection.waitForClose(t)
	select {
	case <-cancelReturned:
	case <-time.After(20 * time.Millisecond):
		t.Fatal("Cancel blocked on upstream Connection.Close")
	}

	type subscribeResult struct {
		subscription Subscription
		err          error
	}
	secondResult := make(chan subscribeResult, 1)
	go func() {
		subscription, subscribeErr := manager.Subscribe(context.Background(), "42", 2, newRecordingSink())
		secondResult <- subscribeResult{subscription: subscription, err: subscribeErr}
	}()
	select {
	case result := <-secondResult:
		t.Fatalf("same-room Subscribe crossed closing tombstone: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	if got := gateway.openCount(); got != 1 {
		t.Fatalf("OpenRoom count before old close completed = %d, want 1", got)
	}

	firstConnection.releaseClose()
	result := <-secondResult
	if result.err != nil || result.subscription == nil {
		t.Fatalf("replacement Subscribe = (%v, %v)", result.subscription, result.err)
	}
	result.subscription.Cancel()
	if err := firstSubscription.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := firstConnection.closeCount(); got != 1 {
		t.Fatalf("old close count = %d, want 1", got)
	}
}

func TestBackpressureIsPromptAndTombstoneWaitsForBlockingClose(t *testing.T) {
	gateway := newBlockingCloseGateway()
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	slow := newBlockingSink()
	defer slow.release()
	if _, err := manager.Subscribe(context.Background(), "42", 1, slow); err != nil {
		t.Fatal(err)
	}
	connection := gateway.connection(t, 0)
	defer connection.releaseClose()
	connection.emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"first"}}`)})
	slow.waitUntilBlocked(t)
	emitReturned := make(chan struct{})
	go func() {
		defer close(emitReturned)
		for index := 0; index < 257; index++ {
			connection.emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"queued"}}`)})
		}
	}()
	connection.waitForClose(t)
	select {
	case <-emitReturned:
	case <-time.After(20 * time.Millisecond):
		t.Fatal("room reader blocked on upstream Connection.Close after backpressure")
	}
	select {
	case err := <-slow.errors:
		if !errors.Is(err, ErrSubscriberBackpressure) {
			t.Fatalf("backpressure error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backpressure error was not delivered promptly")
	}

	secondResult := make(chan error, 1)
	go func() {
		subscription, err := manager.Subscribe(context.Background(), "42", 2, newRecordingSink())
		if subscription != nil {
			defer subscription.Cancel()
		}
		secondResult <- err
	}()
	select {
	case err := <-secondResult:
		t.Fatalf("same-room Subscribe returned before backpressured source closed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if got := gateway.openCount(); got != 1 {
		t.Fatalf("OpenRoom count before blocked close release = %d, want 1", got)
	}
	connection.releaseClose()
	if err := <-secondResult; err != nil {
		t.Fatalf("replacement Subscribe: %v", err)
	}
}

func TestManagerWaitIsQuiescentBarrierWithContext(t *testing.T) {
	gateway := newBlockingCloseGateway()
	manager := NewManager(gateway, Options{})
	sink := newCallbackBarrierSink()
	if _, err := manager.Subscribe(context.Background(), "42", 1, sink); err != nil {
		t.Fatal(err)
	}
	connection := gateway.connection(t, 0)
	defer connection.releaseClose()
	defer sink.release()
	connection.emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"in-flight"}}`)})
	sink.waitForStarted(t)

	closeReturned := make(chan struct{})
	go func() {
		manager.Close()
		close(closeReturned)
	}()
	connection.waitForClose(t)
	select {
	case <-closeReturned:
	case <-time.After(20 * time.Millisecond):
		t.Fatal("Manager.Close blocked instead of initiating shutdown promptly")
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := manager.Wait(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Manager.Wait while resources blocked = %v, want deadline exceeded", err)
	}

	sink.release()
	connection.releaseClose()
	if err := manager.Wait(context.Background()); err != nil {
		t.Fatalf("Manager.Wait after release: %v", err)
	}
	select {
	case <-manager.Done():
	default:
		t.Fatal("Manager.Done was not closed after Wait returned")
	}
}

func TestManagerCloseUnblocksSubscribeWaitingOnClosingTombstone(t *testing.T) {
	gateway := newBlockingCloseGateway()
	manager := NewManager(gateway, Options{})
	first, err := manager.Subscribe(context.Background(), "42", 1, newRecordingSink())
	if err != nil {
		t.Fatal(err)
	}
	connection := gateway.connection(t, 0)
	defer connection.releaseClose()
	first.Cancel()
	connection.waitForClose(t)

	result := make(chan error, 1)
	go func() {
		_, subscribeErr := manager.Subscribe(context.Background(), "42", 2, newRecordingSink())
		result <- subscribeErr
	}()
	select {
	case err := <-result:
		t.Fatalf("Subscribe did not wait on tombstone: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	manager.Close()
	select {
	case err := <-result:
		if !errors.Is(err, ErrInvalidSubscription) {
			t.Fatalf("Subscribe after Manager.Close = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Manager.Close did not unblock Subscribe waiting on tombstone")
	}
	connection.releaseClose()
	if err := manager.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRejectsNilInitialConnection(t *testing.T) {
	gateway := nilConnectionGateway{}
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	subscription, err := manager.Subscribe(context.Background(), "42", 1, newRecordingSink())
	if subscription != nil || !errors.Is(err, biligateway.ErrEgressUnavailable) {
		t.Fatalf("Subscribe = (%v, %v), want (nil, egress_unavailable)", subscription, err)
	}
}

type recordingSink struct {
	events chan Event
	errors chan error
}

type blockingSink struct {
	started chan struct{}
	unblock chan struct{}
	errors  chan error
	once    sync.Once
}

type callbackBarrierSink struct {
	started chan struct{}
	unblock chan struct{}
	mu      sync.Mutex
	count   int
	once    sync.Once
}

func newCallbackBarrierSink() *callbackBarrierSink {
	return &callbackBarrierSink{started: make(chan struct{}, 2), unblock: make(chan struct{})}
}
func (sink *callbackBarrierSink) OnEvent(Event) {
	sink.mu.Lock()
	sink.count++
	sink.mu.Unlock()
	sink.started <- struct{}{}
	<-sink.unblock
}
func (*callbackBarrierSink) OnError(error) {}
func (sink *callbackBarrierSink) waitForStarted(t *testing.T) {
	t.Helper()
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
}
func (sink *callbackBarrierSink) release() { sink.once.Do(func() { close(sink.unblock) }) }
func (sink *callbackBarrierSink) startedCount() int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.count
}

func newBlockingSink() *blockingSink {
	return &blockingSink{started: make(chan struct{}), unblock: make(chan struct{}), errors: make(chan error, 1)}
}
func (sink *blockingSink) OnEvent(Event) {
	sink.once.Do(func() { close(sink.started) })
	<-sink.unblock
}
func (sink *blockingSink) OnError(err error) { sink.errors <- err }
func (sink *blockingSink) waitUntilBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("slow sink did not start")
	}
}
func (sink *blockingSink) release() { close(sink.unblock) }

func newRecordingSink() *recordingSink {
	return &recordingSink{events: make(chan Event, 512), errors: make(chan error, 1)}
}

func (sink *recordingSink) OnEvent(event Event) { sink.events <- event }
func (sink *recordingSink) OnError(err error)   { sink.errors <- err }
func (sink *recordingSink) nextEvent(t *testing.T) Event {
	t.Helper()
	select {
	case event := <-sink.events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

type fakeGateway struct {
	mu          sync.Mutex
	canonical   map[string]string
	openRooms   []string
	connections []*fakeConnection
}

func newFakeGateway(canonical map[string]string) *fakeGateway {
	return &fakeGateway{canonical: canonical}
}

func (gateway *fakeGateway) RoomInfo(_ context.Context, roomID string) (biligateway.RoomInfo, error) {
	return biligateway.RoomInfo{RoomID: roomID, CanonicalRoomID: gateway.canonical[roomID]}, nil
}
func (*fakeGateway) GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (gateway *fakeGateway) OpenRoom(_ context.Context, roomID string, sink biligateway.Sink) (biligateway.Connection, error) {
	connection := newFakeConnection(sink)
	gateway.mu.Lock()
	gateway.openRooms = append(gateway.openRooms, roomID)
	gateway.connections = append(gateway.connections, connection)
	gateway.mu.Unlock()
	return connection, nil
}
func (*fakeGateway) Status() biligateway.Status { return biligateway.Status{} }
func (gateway *fakeGateway) openedRooms() []string {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return append([]string(nil), gateway.openRooms...)
}
func (gateway *fakeGateway) onlyConnection(t *testing.T) *fakeConnection {
	t.Helper()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.connections) != 1 {
		t.Fatalf("connections = %d, want 1", len(gateway.connections))
	}
	return gateway.connections[0]
}

type fakeConnection struct {
	mu     sync.Mutex
	sink   biligateway.Sink
	done   chan struct{}
	err    error
	closes int
}

func newFakeConnection(sink biligateway.Sink) *fakeConnection {
	return &fakeConnection{sink: sink, done: make(chan struct{})}
}
func (connection *fakeConnection) emit(event biligateway.Event) { connection.sink(event) }
func (connection *fakeConnection) Close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.closes++
	if connection.closes == 1 {
		close(connection.done)
	}
	return nil
}
func (connection *fakeConnection) Done() <-chan struct{} { return connection.done }
func (connection *fakeConnection) Err() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.err
}
func (connection *fakeConnection) closeCount() int {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.closes
}

type blockingOpenGateway struct {
	started        chan struct{}
	unblock        chan struct{}
	err            error
	mu             sync.Mutex
	opens          int
	roomInfos      int
	roomInfoSignal chan struct{}
}

type controlledCredentialLoader struct{}

func (controlledCredentialLoader) Load(context.Context) (biligateway.Credential, error) {
	return biligateway.Credential{Version: 1, Cookie: []byte("credential")}, nil
}

type controlledUpstream struct {
	mu            sync.Mutex
	roomInfoCalls int
	openCalls     int
	openRoom      string
}

func (upstream *controlledUpstream) RoomInfo(_ context.Context, roomID string, _ []byte) (biligateway.RoomInfo, error) {
	upstream.mu.Lock()
	upstream.roomInfoCalls++
	upstream.mu.Unlock()
	return biligateway.RoomInfo{RoomID: roomID, CanonicalRoomID: "42"}, nil
}
func (*controlledUpstream) GiftCatalog(context.Context, string, []byte) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (upstream *controlledUpstream) OpenRoom(_ context.Context, roomID string, _ []byte, sink biligateway.Sink) (biligateway.Connection, error) {
	upstream.mu.Lock()
	upstream.openCalls++
	upstream.openRoom = roomID
	upstream.mu.Unlock()
	return newFakeConnection(sink), nil
}

func newBlockingOpenGateway(err error) *blockingOpenGateway {
	return &blockingOpenGateway{started: make(chan struct{}), unblock: make(chan struct{}), err: err, roomInfoSignal: make(chan struct{}, 8)}
}
func (gateway *blockingOpenGateway) RoomInfo(_ context.Context, roomID string) (biligateway.RoomInfo, error) {
	gateway.mu.Lock()
	gateway.roomInfos++
	gateway.mu.Unlock()
	gateway.roomInfoSignal <- struct{}{}
	return biligateway.RoomInfo{RoomID: roomID, CanonicalRoomID: "42"}, nil
}
func (*blockingOpenGateway) GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (gateway *blockingOpenGateway) OpenRoom(_ context.Context, _ string, sink biligateway.Sink) (biligateway.Connection, error) {
	gateway.mu.Lock()
	gateway.opens++
	if gateway.opens == 1 {
		close(gateway.started)
	}
	gateway.mu.Unlock()
	<-gateway.unblock
	if gateway.err != nil {
		return nil, gateway.err
	}
	return newFakeConnection(sink), nil
}
func (*blockingOpenGateway) Status() biligateway.Status { return biligateway.Status{} }
func (gateway *blockingOpenGateway) waitUntilOpening(t *testing.T) {
	t.Helper()
	select {
	case <-gateway.started:
	case <-time.After(time.Second):
		t.Fatal("OpenRoom did not start")
	}
}
func (gateway *blockingOpenGateway) release() { close(gateway.unblock) }
func (gateway *blockingOpenGateway) openCount() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.opens
}

func (gateway *blockingOpenGateway) waitForRoomInfoCalls(t *testing.T, want int) {
	t.Helper()
	for {
		gateway.mu.Lock()
		got := gateway.roomInfos
		gateway.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-gateway.roomInfoSignal:
		case <-time.After(time.Second):
			t.Fatalf("RoomInfo calls = %d, want %d", got, want)
		}
	}
}

type nilConnectionGateway struct{}

func (nilConnectionGateway) RoomInfo(_ context.Context, roomID string) (biligateway.RoomInfo, error) {
	return biligateway.RoomInfo{RoomID: roomID, CanonicalRoomID: "42"}, nil
}
func (nilConnectionGateway) GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (nilConnectionGateway) OpenRoom(context.Context, string, biligateway.Sink) (biligateway.Connection, error) {
	return nil, nil
}
func (nilConnectionGateway) Status() biligateway.Status { return biligateway.Status{} }

type blockingCloseGateway struct {
	mu          sync.Mutex
	connections []*blockingCloseConnection
	opens       int
}

func newBlockingCloseGateway() *blockingCloseGateway { return &blockingCloseGateway{} }
func (*blockingCloseGateway) RoomInfo(_ context.Context, roomID string) (biligateway.RoomInfo, error) {
	return biligateway.RoomInfo{RoomID: roomID, CanonicalRoomID: "42"}, nil
}
func (*blockingCloseGateway) GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (gateway *blockingCloseGateway) OpenRoom(_ context.Context, _ string, sink biligateway.Sink) (biligateway.Connection, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.opens++
	connection := newBlockingCloseConnection(sink, gateway.opens == 1)
	gateway.connections = append(gateway.connections, connection)
	return connection, nil
}
func (*blockingCloseGateway) Status() biligateway.Status { return biligateway.Status{} }
func (gateway *blockingCloseGateway) openCount() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.opens
}
func (gateway *blockingCloseGateway) connection(t *testing.T, index int) *blockingCloseConnection {
	t.Helper()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if index >= len(gateway.connections) {
		t.Fatalf("connection %d missing; have %d", index, len(gateway.connections))
	}
	return gateway.connections[index]
}

type blockingCloseConnection struct {
	mu           sync.Mutex
	sink         biligateway.Sink
	done         chan struct{}
	doneOnce     sync.Once
	closeStarted chan struct{}
	release      chan struct{}
	releaseOnce  sync.Once
	blockClose   bool
	closes       int
}

func newBlockingCloseConnection(sink biligateway.Sink, block bool) *blockingCloseConnection {
	return &blockingCloseConnection{sink: sink, done: make(chan struct{}), closeStarted: make(chan struct{}), release: make(chan struct{}), blockClose: block}
}
func (connection *blockingCloseConnection) emit(event biligateway.Event) { connection.sink(event) }
func (connection *blockingCloseConnection) Close() error {
	connection.mu.Lock()
	connection.closes++
	first := connection.closes == 1
	connection.mu.Unlock()
	if first {
		close(connection.closeStarted)
	}
	if connection.blockClose {
		<-connection.release
	}
	connection.doneOnce.Do(func() { close(connection.done) })
	return nil
}
func (connection *blockingCloseConnection) Done() <-chan struct{} { return connection.done }
func (*blockingCloseConnection) Err() error                       { return nil }
func (connection *blockingCloseConnection) releaseClose() {
	connection.releaseOnce.Do(func() { close(connection.release) })
}
func (connection *blockingCloseConnection) waitForClose(t *testing.T) {
	t.Helper()
	select {
	case <-connection.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("Connection.Close did not start")
	}
}
func (connection *blockingCloseConnection) closeCount() int {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.closes
}
