package roomsource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
)

func TestManagerReconnectsWithCappedExponentialDelays(t *testing.T) {
	clock := newManualClock(time.Unix(1_000, 0))
	timers := newManualTimerFactory(clock)
	gateway := newReconnectGateway()
	manager := NewManager(gateway, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	t.Cleanup(manager.Close)
	subscription, err := manager.Subscribe(context.Background(), "42", 7, newRecordingSink())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second, 60 * time.Second}
	for index, delay := range want {
		gateway.connection(t, index).fail(errors.New("disconnected"))
		timer := timers.next(t)
		if timer.delay != delay {
			t.Fatalf("delay %d = %v, want %v", index, timer.delay, delay)
		}
		timer.fire()
		gateway.waitForOpens(t, index+2)
	}
}

func TestManagerReconnectRespectsGatewayRetryAfter(t *testing.T) {
	retryErr := gatewayRetryError(t, 17*time.Second)
	clock := newManualClock(time.Unix(2_000, 0))
	timers := newManualTimerFactory(clock)
	gateway := newReconnectGateway()
	manager := NewManager(gateway, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	t.Cleanup(manager.Close)
	if _, err := manager.Subscribe(context.Background(), "42", 7, newRecordingSink()); err != nil {
		t.Fatal(err)
	}

	gateway.connection(t, 0).fail(retryErr)
	if got := timers.next(t).delay; got != 17*time.Second {
		t.Fatalf("reconnect delay = %v, want gateway Retry-After 17s", got)
	}
}

func TestManagerDoesNotRetryWhileGatewayBreakerIsOpen(t *testing.T) {
	clock := newManualClock(time.Unix(3_000, 0))
	timers := newManualTimerFactory(clock)
	gateway := newReconnectGateway()
	manager := NewManager(gateway, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	t.Cleanup(manager.Close)
	if _, err := manager.Subscribe(context.Background(), "42", 7, newRecordingSink()); err != nil {
		t.Fatal(err)
	}

	gateway.connection(t, 0).fail(errors.New("disconnected"))
	gateway.setBreakerOpen(true)
	timers.next(t).fire()
	breakerTimer := timers.next(t)
	if got := gateway.openCount(); got != 1 {
		t.Fatalf("OpenRoom count while breaker open = %d, want 1", got)
	}
	gateway.setBreakerOpen(false)
	breakerTimer.fire()
	gateway.waitForOpens(t, 2)
}

func TestManagerResetsReconnectAttemptsAfterTwoMinutesOfHealthyFrames(t *testing.T) {
	clock := newManualClock(time.Unix(4_000, 0))
	timers := newManualTimerFactory(clock)
	gateway := newReconnectGateway()
	manager := NewManager(gateway, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	t.Cleanup(manager.Close)
	if _, err := manager.Subscribe(context.Background(), "42", 7, newRecordingSink()); err != nil {
		t.Fatal(err)
	}

	gateway.connection(t, 0).fail(errors.New("first"))
	timers.next(t).fire()
	gateway.waitForOpens(t, 2)
	gateway.connection(t, 1).emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"healthy-1"}}`)})
	clock.Advance(2 * time.Minute)
	gateway.connection(t, 1).emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"healthy-2"}}`)})
	gateway.connection(t, 1).fail(errors.New("after_healthy_frames"))
	if got := timers.next(t).delay; got != time.Second {
		t.Fatalf("delay after healthy frames = %v, want reset 1s", got)
	}
}

func TestManagerRejectsLateFramesFromSupersededConnection(t *testing.T) {
	clock := newManualClock(time.Unix(5_000, 0))
	timers := newManualTimerFactory(clock)
	gateway := newReconnectGateway()
	manager := NewManager(gateway, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	t.Cleanup(manager.Close)
	sink := newRecordingSink()
	if _, err := manager.Subscribe(context.Background(), "42", 7, sink); err != nil {
		t.Fatal(err)
	}
	oldConnection := gateway.connection(t, 0)
	oldConnection.fail(errors.New("disconnected"))
	timers.next(t).fire()
	gateway.waitForOpens(t, 2)
	oldConnection.emit(biligateway.Event{Type: "application", Data: []byte(`{"cmd":"SEND_GIFT","data":{"rnd":"stale"}}`)})
	select {
	case event := <-sink.events:
		t.Fatalf("received late frame from superseded connection: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestManagerWaitIncludesReconnectBlockedInGatewayStatus(t *testing.T) {
	clock := newManualClock(time.Unix(6_000, 0))
	timers := newManualTimerFactory(clock)
	gateway := newBlockingStatusGateway()
	defer gateway.releaseStatus()
	manager := NewManager(gateway, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	if _, err := manager.Subscribe(context.Background(), "42", 7, newRecordingSink()); err != nil {
		t.Fatal(err)
	}

	gateway.connection(t, 0).fail(errors.New("disconnected"))
	timers.next(t).fire()
	gateway.waitForStatus(t)
	manager.Close()

	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := manager.Wait(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Manager.Wait while Status is blocked = %v, want deadline exceeded", err)
	}

	gateway.releaseStatus()
	finishedContext, cancelFinished := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinished()
	if err := manager.Wait(finishedContext); err != nil {
		t.Fatalf("Manager.Wait after Status unblocked: %v", err)
	}
}

func TestManagerWaitIncludesSupervisorAfterSuccessfulReconnect(t *testing.T) {
	clock := newManualClock(time.Unix(7_000, 0))
	timers := newManualTimerFactory(clock)
	gateway := newReconnectGateway()
	manager := NewManager(gateway, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	if _, err := manager.Subscribe(context.Background(), "42", 7, newRecordingSink()); err != nil {
		t.Fatal(err)
	}

	doneBlock := gateway.blockNextConnectionDone()
	defer doneBlock.releaseDone()
	gateway.connection(t, 0).fail(errors.New("disconnected"))
	timers.next(t).fire()
	doneBlock.waitForDone(t)
	manager.Close()

	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if err := manager.Wait(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Manager.Wait while reconnected supervisor is blocked = %v, want deadline exceeded", err)
	}

	doneBlock.releaseDone()
	finishedContext, cancelFinished := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinished()
	if err := manager.Wait(finishedContext); err != nil {
		t.Fatalf("Manager.Wait after reconnected supervisor exited: %v", err)
	}
}

func gatewayRetryError(t *testing.T, delay time.Duration) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Retry-After", "17")
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	upstream, err := biligateway.NewHTTPUpstream(biligateway.HTTPUpstreamOptions{Client: server.Client(), RoomInfoEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upstream.RoomInfo(context.Background(), "42", []byte("credential"))
	if got, ok := biligateway.RetryAfter(err); !ok || got != delay {
		t.Fatalf("gateway RetryAfter fixture = %v, %v", got, ok)
	}
	return err
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock { return &manualClock{now: now} }
func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}
func (clock *manualClock) Advance(delay time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	clock.mu.Unlock()
}

type manualTimerFactory struct {
	clock   *manualClock
	created chan *manualTimer
}

func newManualTimerFactory(clock *manualClock) *manualTimerFactory {
	return &manualTimerFactory{clock: clock, created: make(chan *manualTimer, 32)}
}
func (factory *manualTimerFactory) NewTimer(delay time.Duration) Timer {
	timer := &manualTimer{factory: factory, delay: delay, channel: make(chan time.Time, 1)}
	factory.created <- timer
	return timer
}
func (factory *manualTimerFactory) next(t *testing.T) *manualTimer {
	t.Helper()
	select {
	case timer := <-factory.created:
		return timer
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect timer")
		return nil
	}
}

type manualTimer struct {
	factory *manualTimerFactory
	delay   time.Duration
	channel chan time.Time
	once    sync.Once
}

func (timer *manualTimer) C() <-chan time.Time { return timer.channel }
func (timer *manualTimer) Stop() bool {
	stopped := false
	timer.once.Do(func() { stopped = true })
	return stopped
}
func (timer *manualTimer) fire() {
	timer.once.Do(func() {
		timer.factory.clock.Advance(timer.delay)
		timer.channel <- timer.factory.clock.Now()
	})
}

type reconnectGateway struct {
	mu          sync.Mutex
	connections []*reconnectConnection
	opens       int
	openSignal  chan struct{}
	breakerOpen bool
	nextDone    *connectionDoneBlock
}

func newReconnectGateway() *reconnectGateway {
	return &reconnectGateway{openSignal: make(chan struct{}, 32)}
}
func (*reconnectGateway) RoomInfo(_ context.Context, roomID string) (biligateway.RoomInfo, error) {
	return biligateway.RoomInfo{RoomID: roomID, CanonicalRoomID: "42"}, nil
}
func (*reconnectGateway) GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (gateway *reconnectGateway) OpenRoom(_ context.Context, _ string, sink biligateway.Sink) (biligateway.Connection, error) {
	gateway.mu.Lock()
	connection := &reconnectConnection{sink: sink, done: make(chan struct{}), doneBlock: gateway.nextDone}
	gateway.nextDone = nil
	gateway.connections = append(gateway.connections, connection)
	gateway.opens++
	gateway.mu.Unlock()
	gateway.openSignal <- struct{}{}
	return connection, nil
}
func (gateway *reconnectGateway) Status() biligateway.Status {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return biligateway.Status{EgressOpen: gateway.breakerOpen}
}
func (gateway *reconnectGateway) setBreakerOpen(open bool) {
	gateway.mu.Lock()
	gateway.breakerOpen = open
	gateway.mu.Unlock()
}
func (gateway *reconnectGateway) connection(t *testing.T, index int) *reconnectConnection {
	t.Helper()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if index >= len(gateway.connections) {
		t.Fatalf("connection %d missing; have %d", index, len(gateway.connections))
	}
	return gateway.connections[index]
}
func (gateway *reconnectGateway) waitForOpens(t *testing.T, want int) {
	t.Helper()
	for {
		if gateway.openCount() >= want {
			return
		}
		select {
		case <-gateway.openSignal:
		case <-time.After(time.Second):
			t.Fatalf("OpenRoom count = %d, want at least %d", gateway.openCount(), want)
		}
	}
}
func (gateway *reconnectGateway) openCount() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.opens
}

func (gateway *reconnectGateway) blockNextConnectionDone() *connectionDoneBlock {
	block := &connectionDoneBlock{started: make(chan struct{}), release: make(chan struct{})}
	gateway.mu.Lock()
	gateway.nextDone = block
	gateway.mu.Unlock()
	return block
}

type blockingStatusGateway struct {
	*reconnectGateway
	statusStarted chan struct{}
	statusRelease chan struct{}
	statusOnce    sync.Once
	releaseOnce   sync.Once
}

func newBlockingStatusGateway() *blockingStatusGateway {
	return &blockingStatusGateway{
		reconnectGateway: newReconnectGateway(),
		statusStarted:    make(chan struct{}),
		statusRelease:    make(chan struct{}),
	}
}

func (gateway *blockingStatusGateway) Status() biligateway.Status {
	gateway.statusOnce.Do(func() { close(gateway.statusStarted) })
	<-gateway.statusRelease
	return gateway.reconnectGateway.Status()
}

func (gateway *blockingStatusGateway) waitForStatus(t *testing.T) {
	t.Helper()
	select {
	case <-gateway.statusStarted:
	case <-time.After(time.Second):
		t.Fatal("gateway Status did not start")
	}
}

func (gateway *blockingStatusGateway) releaseStatus() {
	gateway.releaseOnce.Do(func() { close(gateway.statusRelease) })
}

type reconnectConnection struct {
	mu        sync.Mutex
	sink      biligateway.Sink
	done      chan struct{}
	doneOnce  sync.Once
	err       error
	closes    int
	doneBlock *connectionDoneBlock
}

func (connection *reconnectConnection) emit(event biligateway.Event) { connection.sink(event) }
func (connection *reconnectConnection) fail(err error) {
	connection.mu.Lock()
	connection.err = err
	connection.mu.Unlock()
	connection.doneOnce.Do(func() { close(connection.done) })
}
func (connection *reconnectConnection) Close() error {
	connection.mu.Lock()
	connection.closes++
	connection.mu.Unlock()
	connection.doneOnce.Do(func() { close(connection.done) })
	return nil
}
func (connection *reconnectConnection) Done() <-chan struct{} {
	if connection.doneBlock != nil {
		connection.doneBlock.enter()
	}
	return connection.done
}
func (connection *reconnectConnection) Err() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.err
}

type connectionDoneBlock struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func (block *connectionDoneBlock) enter() {
	block.startedOnce.Do(func() { close(block.started) })
	<-block.release
}

func (block *connectionDoneBlock) waitForDone(t *testing.T) {
	t.Helper()
	select {
	case <-block.started:
	case <-time.After(time.Second):
		t.Fatal("reconnected supervisor did not call Connection.Done")
	}
}

func (block *connectionDoneBlock) releaseDone() {
	block.releaseOnce.Do(func() { close(block.release) })
}
