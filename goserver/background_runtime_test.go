package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGiftSource struct {
	started chan string
	events  chan giftEvent
}

func TestBackgroundRuntimeReplaysDurableInboxOnce(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	firstInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := newBackgroundRuntime(store, nil)
	firstRuntime.installInbox(firstInbox, firstInbox.SnapshotHealth())
	firstRuntime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
		GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "restart-rnd",
	})
	if health := firstInbox.Health(); health.PendingCount != 1 {
		t.Fatalf("pending before restart = %d, want 1", health.PendingCount)
	}
	canceledContext, cancelFirst := context.WithCancel(context.Background())
	cancelFirst()
	firstRuntime.Run(canceledContext)
	if health := firstInbox.Health(); health.PendingCount != 1 {
		t.Fatalf("pending after cancellation before consumption = %d, want 1", health.PendingCount)
	}

	secondInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime := newBackgroundRuntime(store, nil)
	secondRuntime.installInbox(secondInbox, secondInbox.SnapshotHealth())
	cancelSecond, secondDone := startBackgroundRuntimeForTest(secondRuntime)
	waitForInboxPendingCount(t, secondInbox, 0)
	if status := secondRuntime.Status(); status.Inbox == nil || status.Inbox.PendingCount != 0 {
		t.Fatalf("published inbox status after acknowledgement = %#v, want zero pending", status.Inbox)
	}
	cancelSecond()
	<-secondDone
	assertRuntimeAttributeValue(t, store, "积分", 1)

	thirdInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	thirdRuntime := newBackgroundRuntime(store, nil)
	thirdRuntime.installInbox(thirdInbox, thirdInbox.SnapshotHealth())
	cancelThird, thirdDone := startBackgroundRuntimeForTest(thirdRuntime)
	waitForInboxPendingCount(t, thirdInbox, 0)
	cancelThird()
	<-thirdDone
	assertRuntimeAttributeValue(t, store, "积分", 1)

	duplicateInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	duplicateRuntime := newBackgroundRuntime(store, nil)
	duplicateRuntime.installInbox(duplicateInbox, duplicateInbox.SnapshotHealth())
	duplicateRuntime.inboxRetryDelay = time.Hour
	for range 2 {
		duplicateRuntime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
			GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "durable-duplicate-rnd",
		})
	}
	writes := 0
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "events.log" {
			writes++
			if writes == 2 {
				return errors.New("injected duplicate settlement failure")
			}
		}
		return writeFileAtomically(path, data)
	}
	cancelDuplicate, duplicateDone := startBackgroundRuntimeForTest(duplicateRuntime)
	waitForIngestionError(t, duplicateRuntime, "injected duplicate settlement failure")
	cancelDuplicate()
	<-duplicateDone
	if health := duplicateInbox.Health(); health.PendingCount != 1 {
		t.Fatalf("pending after prepared duplicate failure = %d, want 1", health.PendingCount)
	}

	recoveredStore := &configStore{path: filepath.Join(root, "config.json")}
	recoveredInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	recoveredRuntime := newBackgroundRuntime(recoveredStore, nil)
	recoveredRuntime.installInbox(recoveredInbox, recoveredInbox.SnapshotHealth())
	cancelRecovered, recoveredDone := startBackgroundRuntimeForTest(recoveredRuntime)
	waitForInboxPendingCount(t, recoveredInbox, 0)
	cancelRecovered()
	<-recoveredDone
	assertRuntimeAttributeValue(t, recoveredStore, "积分", 2)
	recoveredState, err := recoveredStore.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(recoveredState.AppliedIngressIDs) != 3 {
		t.Fatalf("applied ingestion IDs = %d, want 3", len(recoveredState.AppliedIngressIDs))
	}
	if _, exists := recoveredState.RecentSourceGiftKeys["durable-duplicate-rnd"]; !exists {
		t.Fatalf("recent source keys = %#v", recoveredState.RecentSourceGiftKeys)
	}
}

type blockingUserProfileResolver struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func (resolver *blockingUserProfileResolver) Resolve(ctx context.Context, uid int64) (userProfile, error) {
	resolver.startedOnce.Do(func() { close(resolver.started) })
	select {
	case <-ctx.Done():
		return userProfile{}, ctx.Err()
	case <-resolver.release:
		return userProfile{UID: uid, Name: "完整昵称"}, nil
	}
}

func (resolver *blockingUserProfileResolver) unblock() {
	resolver.releaseOnce.Do(func() { close(resolver.release) })
}

func TestBackgroundRuntimeIngressDoesNotWaitForProfile(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, inbox.SnapshotHealth())
	resolver := &blockingUserProfileResolver{started: make(chan struct{}), release: make(chan struct{})}
	runtime.profileResolver = resolver
	runtime.profileTimeout = time.Hour
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		resolver.unblock()
		cancel()
		<-done
	}()

	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
		GiftID: 1, GiftName: "礼物", Num: 1, UID: 42, Uname: "字***", Timestamp: time.Now().Unix(), Rnd: "slow-000",
	})
	select {
	case <-resolver.started:
	case <-time.After(time.Second):
		t.Fatal("profile resolver did not block the inbox consumer")
	}

	const additional = 1
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for index := 1; index <= additional; index++ {
			runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
				GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "slow-" + leftPadThree(index),
			})
		}
	}()
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("durable gift acceptance waited for the blocked profile resolver")
	}
	if health := inbox.Health(); health.PendingCount != additional+1 {
		t.Fatalf("pending while profile is blocked = %d, want %d", health.PendingCount, additional+1)
	}

	resolver.unblock()
	waitForInboxPendingCount(t, inbox, 0)
	assertRuntimeAttributeValue(t, store, "积分", additional+1)
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Log) == 0 || updated.Log[0].EventID != "slow-001:积分" {
		t.Fatalf("newest ordered log entry = %#v", updated.Log)
	}
}

type resetBarrierInbox struct {
	mu            sync.Mutex
	acceptStarted chan struct{}
	allowAccept   chan struct{}
	resetCalled   chan struct{}
	acceptOnce    sync.Once
	resetOnce     sync.Once
	pending       int
	resetErr      error
}

func (inbox *resetBarrierInbox) Accept(roomID, command string, gift giftEvent) (giftInboxRecord, error) {
	inbox.acceptOnce.Do(func() { close(inbox.acceptStarted) })
	if inbox.allowAccept != nil {
		<-inbox.allowAccept
	}
	inbox.mu.Lock()
	inbox.pending++
	inbox.mu.Unlock()
	return giftInboxRecord{SchemaVersion: 1, LocalSequence: 1, IngestionID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RoomID: roomID, Command: command, Gift: gift}, nil
}

func (*resetBarrierInbox) Next() (giftInboxRecord, bool, error) { return giftInboxRecord{}, false, nil }
func (*resetBarrierInbox) Acknowledge(string) error             { return nil }
func (*resetBarrierInbox) Release(string) error                 { return nil }
func (*resetBarrierInbox) Close() error                         { return nil }
func (inbox *resetBarrierInbox) Health() giftInboxHealth {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return giftInboxHealth{PendingCount: inbox.pending}
}
func (inbox *resetBarrierInbox) Reset() error {
	inbox.resetOnce.Do(func() { close(inbox.resetCalled) })
	if inbox.resetErr != nil {
		return inbox.resetErr
	}
	inbox.mu.Lock()
	inbox.pending = 0
	inbox.mu.Unlock()
	return nil
}

func TestBackgroundRuntimeResetWaitsForConcurrentAcceptAndClearsAcceptedRecord(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox := &resetBarrierInbox{acceptStarted: make(chan struct{}), allowAccept: make(chan struct{}), resetCalled: make(chan struct{})}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, inbox.Health())
	accepted := make(chan struct{})
	go func() {
		runtime.acceptGift(context.Background(), "room-a", "SEND_GIFT", giftEvent{GiftID: 1})
		close(accepted)
	}()
	<-inbox.acceptStarted
	resetDone := make(chan error, 1)
	go func() { resetDone <- runtime.Reset() }()
	select {
	case <-inbox.resetCalled:
		t.Fatal("reset entered durable cleanup before concurrent Accept left the reset gate")
	case <-time.After(50 * time.Millisecond):
	}
	close(inbox.allowAccept)
	<-accepted
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	if health := inbox.Health(); health.PendingCount != 0 {
		t.Fatalf("accepted record survived reset: %#v", health)
	}
}

func TestBackgroundRuntimeResetWaitsForConsumerThenClearsAllOwnedArtifacts(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Accept("room-a", "SEND_GIFT", giftEvent{GiftID: 1, UID: 42, Uname: "字***", Rnd: "old-room-rnd"}); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, inbox.SnapshotHealth())
	if err := runtime.savePendingGiftAnimationFile(pendingGiftAnimationFile{SchemaVersion: pendingGiftAnimationsSchemaVersion, PreparedRoomID: "room-a", Records: []pendingGiftAnimation{}}); err != nil {
		t.Fatal(err)
	}
	resolver := &blockingUserProfileResolver{started: make(chan struct{}), release: make(chan struct{})}
	runtime.profileResolver = resolver
	runtime.profileTimeout = time.Hour
	consumed := make(chan error, 1)
	go func() {
		_, consumeErr := runtime.consumeAvailableInboxRecord(context.Background())
		consumed <- consumeErr
	}()
	<-resolver.started
	resetDone := make(chan error, 1)
	go func() { resetDone <- runtime.Reset() }()
	select {
	case err := <-resetDone:
		t.Fatalf("reset completed while consumer held the reset gate: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	resolver.unblock()
	if err := <-consumed; err != nil {
		t.Fatal(err)
	}
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	if health := inbox.Health(); health.PendingCount != 0 {
		t.Fatalf("inbox survived reset: %#v", health)
	}
	for _, path := range append(store.statePaths(), store.stateTransactionPath(), runtime.pendingGiftAnimationsPath()) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned artifact %s survived reset: %v", filepath.Base(path), err)
		}
	}
}

func TestBackgroundRuntimeResetRejectsStaleProducerAndProcessesOnlyNewRoom(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	oldState := defaultAppState()
	oldState.RoomID = "room-a"
	if err := store.replaceState(oldState); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, inbox.SnapshotHealth())
	oldGeneration := runtime.currentResetGeneration()
	if err := runtime.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.prepareRoomConnectionForGeneration(oldGeneration, "room-a"); !errors.Is(err, errResetGenerationChanged) {
		t.Fatalf("stale connection plan preparation error = %v, want reset generation change", err)
	}
	if _, err := os.Stat(runtime.pendingGiftAnimationsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale connection plan recreated pending animation state: %v", err)
	}
	newState := defaultAppState()
	newState.RoomID = "room-b"
	newState.Attributes = []attributeState{{Name: "积分", Value: 0}}
	newState.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(newState); err != nil {
		t.Fatal(err)
	}
	if err := runtime.prepareRoomConnection("room-b"); err != nil {
		t.Fatal(err)
	}
	runtime.acceptGiftForGeneration(context.Background(), oldGeneration, "room-a", "SEND_GIFT", giftEvent{GiftID: 1, Rnd: "stale-old-room"})
	if health := inbox.Health(); health.PendingCount != 0 {
		t.Fatalf("stale producer recreated inbox state after reset: %#v", health)
	}
	runtime.acceptGift(context.Background(), "room-b", "SEND_GIFT", giftEvent{GiftID: 1, Rnd: "new-room"})
	if _, err := runtime.consumeAvailableInboxRecord(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRuntimeAttributeValue(t, store, "积分", 1)
}

func TestBackgroundRuntimeDoesNotMatchQueuedRecordWhenCurrentRoomIsEmpty(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	record := giftInboxRecord{SchemaVersion: 1, LocalSequence: 1, IngestionID: "empty-room-ingestion", RoomID: "room-a", Gift: giftEvent{GiftID: 1, Rnd: "old-room"}}
	if err := runtime.processInboxRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attributes[0].Value != 0 || len(updated.GiftReceipts) != 0 {
		t.Fatalf("queued old-room record matched empty room: %#v", updated)
	}
	if len(updated.AppliedIngressIDs) != 1 || updated.AppliedIngressIDs[0] != record.IngestionID {
		t.Fatalf("empty-room no-op was not durably settled: %#v", updated.AppliedIngressIDs)
	}
}

type capacityFailureInbox struct{}

func (*capacityFailureInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, errGiftInboxCapacity
}

func (*capacityFailureInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}

func (*capacityFailureInbox) Acknowledge(string) error { return nil }
func (*capacityFailureInbox) Release(string) error     { return nil }
func (*capacityFailureInbox) Close() error             { return nil }
func (*capacityFailureInbox) Health() giftInboxHealth  { return giftInboxHealth{CapacityError: true} }

type runtimeStatusProbeInbox struct{ healthCalls int }

func (*runtimeStatusProbeInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (*runtimeStatusProbeInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (*runtimeStatusProbeInbox) Acknowledge(string) error { return nil }
func (*runtimeStatusProbeInbox) Release(string) error     { return nil }
func (*runtimeStatusProbeInbox) Close() error             { return nil }
func (inbox *runtimeStatusProbeInbox) Health() giftInboxHealth {
	inbox.healthCalls++
	return giftInboxHealth{PendingCount: 1}
}

type nonComparableRuntimeInbox struct{ values []int }

func (nonComparableRuntimeInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (nonComparableRuntimeInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (nonComparableRuntimeInbox) Acknowledge(string) error { return nil }
func (nonComparableRuntimeInbox) Release(string) error     { return nil }
func (nonComparableRuntimeInbox) Close() error             { return nil }
func (inbox nonComparableRuntimeInbox) Health() giftInboxHealth {
	return giftInboxHealth{PendingCount: len(inbox.values)}
}

type snapshotBarrierGiftInbox struct {
	*giftInbox
	mu               sync.Mutex
	blockNext        bool
	snapshotCaptured chan giftInboxHealth
	releaseSnapshot  chan struct{}
}

type startupResetBarrierInbox struct {
	mu             sync.Mutex
	health         giftInboxHealth
	healthCaptured chan giftInboxHealth
	releaseHealth  chan struct{}
	resetCalled    chan struct{}
	healthOnce     sync.Once
	resetOnce      sync.Once
}

type startupMarkerOrderingInbox struct {
	resetCalled chan struct{}
	nextCalled  chan bool
	resetDone   atomic.Bool
	resetOnce   sync.Once
	nextOnce    sync.Once
}

type startupResetRecoveryInbox struct {
	mu                  sync.Mutex
	resetCalls          int
	firstResetReturning chan struct{}
	firstResetOnce      sync.Once
	secondResetStarted  chan struct{}
	releaseSecondReset  chan struct{}
	secondResetOnce     sync.Once
	injected            error
}

func (*startupResetRecoveryInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (*startupResetRecoveryInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (*startupResetRecoveryInbox) Acknowledge(string) error { return nil }
func (*startupResetRecoveryInbox) Release(string) error     { return nil }
func (*startupResetRecoveryInbox) Close() error             { return nil }
func (*startupResetRecoveryInbox) Health() giftInboxHealth  { return giftInboxHealth{} }
func (inbox *startupResetRecoveryInbox) Reset() error {
	inbox.mu.Lock()
	inbox.resetCalls++
	call := inbox.resetCalls
	inbox.mu.Unlock()
	if call == 1 {
		inbox.firstResetOnce.Do(func() { close(inbox.firstResetReturning) })
		return inbox.injected
	}
	if call == 2 {
		inbox.secondResetOnce.Do(func() { close(inbox.secondResetStarted) })
		<-inbox.releaseSecondReset
	}
	return nil
}

func (inbox *startupResetRecoveryInbox) ResetCalls() int {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return inbox.resetCalls
}

func (*startupMarkerOrderingInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (inbox *startupMarkerOrderingInbox) Next() (giftInboxRecord, bool, error) {
	inbox.nextOnce.Do(func() { inbox.nextCalled <- inbox.resetDone.Load() })
	return giftInboxRecord{}, false, nil
}
func (*startupMarkerOrderingInbox) Acknowledge(string) error { return nil }
func (*startupMarkerOrderingInbox) Release(string) error     { return nil }
func (*startupMarkerOrderingInbox) Close() error             { return nil }
func (*startupMarkerOrderingInbox) Health() giftInboxHealth  { return giftInboxHealth{} }
func (inbox *startupMarkerOrderingInbox) Reset() error {
	inbox.resetDone.Store(true)
	inbox.resetOnce.Do(func() { close(inbox.resetCalled) })
	return nil
}

func TestBackgroundRuntimeStartupCompletesValidResetIntentBeforeInboxNext(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "reset-intent.json")
	if err := os.WriteFile(markerPath, canonicalResetIntentData, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := initializeConfigStore(&configStore{path: filepath.Join(dir, "config.json")})
	if err != nil {
		t.Fatal(err)
	}
	inbox := &startupMarkerOrderingInbox{resetCalled: make(chan struct{}), nextCalled: make(chan bool, 1)}
	runtime := newBackgroundRuntime(store, nil)
	runtime.inboxRetryDelay = time.Millisecond
	runtime.installInbox(inbox, inbox.Health())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(done)
	}()

	select {
	case <-inbox.resetCalled:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("startup did not complete the valid reset intent")
	}
	select {
	case afterReset := <-inbox.nextCalled:
		if !afterReset {
			t.Fatal("startup processed the inbox before reset completion")
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("gift worker did not start after reset completion")
	}
	cancel()
	<-done
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup recovery left reset marker: %v", err)
	}
}

func TestBackgroundRuntimeStartupResetFailureWaitsForSuccessfulRetryThenStartsWorkersOnce(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "reset-intent.json")
	if err := os.WriteFile(markerPath, canonicalResetIntentData, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := initializeConfigStore(&configStore{path: filepath.Join(dir, "config.json")})
	if err != nil {
		t.Fatal(err)
	}
	inbox := &startupResetRecoveryInbox{
		firstResetReturning: make(chan struct{}),
		secondResetStarted:  make(chan struct{}),
		releaseSecondReset:  make(chan struct{}),
		injected:            errors.New("injected transient startup reset failure"),
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, inbox.Health())
	store.setResetCoordinator(runtime.ResetWithOutcome)
	workerStarts := make(chan string, 6)
	runtime.onWorkerStart = func(name string) { workerStarts <- name }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(done)
	}()

	select {
	case <-inbox.firstResetReturning:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("startup reset did not reach the injected transient failure")
	}
	deleteResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		store.handle(response, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
		deleteResponse <- response
	}()
	select {
	case <-inbox.secondResetStarted:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("DELETE retry did not enter the second reset")
	}
	select {
	case name := <-workerStarts:
		t.Fatalf("worker %q started before reset recovery completed", name)
	default:
	}
	select {
	case <-done:
		t.Fatal("runtime returned while reset recovery remained incomplete")
	default:
	}
	close(inbox.releaseSecondReset)
	select {
	case response := <-deleteResponse:
		if response.Code != http.StatusNoContent {
			cancel()
			<-done
			t.Fatalf("DELETE retry status=%d body=%s, want 204", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("DELETE retry did not complete")
	}

	started := map[string]int{}
	for len(started) < 3 {
		select {
		case name := <-workerStarts:
			started[name]++
		case <-time.After(time.Second):
			cancel()
			<-done
			t.Fatalf("workers did not start after reset recovery: %v", started)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	for {
		select {
		case name := <-workerStarts:
			started[name]++
		default:
			goto drained
		}
	}

drained:
	for _, name := range []string{"connection", "gift", "timer"} {
		if started[name] != 1 {
			t.Fatalf("worker %q starts=%d, all starts=%v", name, started[name], started)
		}
	}
	if len(started) != 3 {
		t.Fatalf("unexpected worker starts: %v", started)
	}
	if calls := inbox.ResetCalls(); calls != 2 {
		t.Fatalf("inbox reset calls=%d, want failed startup plus one successful retry", calls)
	}
}

func TestBackgroundRuntimeStartupResetFailureWaitIsCancellationResponsive(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "reset-intent.json")
	if err := os.WriteFile(markerPath, canonicalResetIntentData, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := initializeConfigStore(&configStore{path: filepath.Join(dir, "config.json")})
	if err != nil {
		t.Fatal(err)
	}
	inbox := &startupResetRecoveryInbox{
		firstResetReturning: make(chan struct{}),
		secondResetStarted:  make(chan struct{}),
		releaseSecondReset:  make(chan struct{}),
		injected:            errors.New("injected transient startup reset failure"),
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, inbox.Health())
	workerStarts := make(chan string, 1)
	runtime.onWorkerStart = func(name string) { workerStarts <- name }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(done)
	}()

	select {
	case <-inbox.firstResetReturning:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("startup reset did not reach the injected failure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop while waiting for startup reset recovery")
	}
	select {
	case name := <-workerStarts:
		t.Fatalf("worker %q started before canceled startup recovery", name)
	default:
	}
	if calls := inbox.ResetCalls(); calls != 1 {
		t.Fatalf("startup reset calls=%d, want 1 before cancellation", calls)
	}
}

func TestBackgroundRuntimeResetRetiresLinkedStateWALAndAnimationLeavesWithoutFollowingTargets(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	runtime := newBackgroundRuntime(store, nil)
	inbox, err := openGiftInbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inbox.Close() })
	runtime.installInbox(inbox, inbox.Health())
	store.setResetCoordinator(runtime.ResetWithOutcome)
	outsideDir := t.TempDir()
	existingTarget := filepath.Join(outsideDir, "existing-target.json")
	if err := os.WriteFile(existingTarget, []byte("outside-must-survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	existingLink := store.historyPath()
	if err := os.Symlink(existingTarget, existingLink); err != nil {
		t.Skipf("filesystem symlinks are unavailable on this host: %v", err)
	}
	danglingLinks := map[string]string{
		store.path:                   filepath.Join(outsideDir, "missing-state-target.json"),
		store.stateTransactionPath(): filepath.Join(outsideDir, "missing-wal-target.json"),
		filepath.Join(inbox.pendingPath, inbox.recordFilename(1, strings.Repeat("c", 32))): filepath.Join(outsideDir, "missing-inbox-record-target.json"),
		filepath.Join(inbox.pendingPath, "config-linked-reset.tmp"):                        filepath.Join(outsideDir, "missing-inbox-temp-target.json"),
		runtime.pendingGiftAnimationsPath():                                                filepath.Join(outsideDir, "missing-animation-target.json"),
	}
	for path, target := range danglingLinks {
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("filesystem symlinks are unavailable on this host: %v", err)
		}
	}

	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE reset status=%d body=%s", response.Code, response.Body.String())
	}
	for _, path := range append([]string{existingLink}, mapKeysForResetLinkTest(danglingLinks)...) {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("linked reset artifact %s survived: %v", filepath.Base(path), err)
		}
	}
	data, err := os.ReadFile(existingTarget)
	if err != nil || string(data) != "outside-must-survive" {
		t.Fatalf("outside target changed: data=%q err=%v", data, err)
	}
	for ownedPath, target := range danglingLinks {
		if err := os.WriteFile(target, []byte("created-after-reset"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(ownedPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired dangling link %s resurrected after target creation: %v", filepath.Base(ownedPath), err)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "created-after-reset" {
			t.Fatalf("post-reset outside target %s changed: data=%q err=%v", filepath.Base(target), data, err)
		}
	}
}

func mapKeysForResetLinkTest(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestBackgroundRuntimeResetRejectsLinkedConfigRootBeforeMarkerOrInbox(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "owned-config-root")
	outside := t.TempDir()
	outsideSentinel := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideSentinel, []byte("outside-must-not-change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("directory symlink/reparse creation is unavailable on this host: %v", err)
	}
	store := &configStore{path: filepath.Join(root, "config.json")}
	runtime := newBackgroundRuntime(store, nil)
	inbox := &startupMarkerOrderingInbox{resetCalled: make(chan struct{}), nextCalled: make(chan bool, 1)}
	runtime.installInbox(inbox, inbox.Health())

	if err := runtime.Reset(); err == nil {
		t.Fatal("reset accepted a linked config root")
	}
	select {
	case <-inbox.resetCalled:
		t.Fatal("inbox reset ran before linked config root rejection")
	default:
	}
	if _, err := os.Lstat(filepath.Join(outside, "reset-intent.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reset marker was created through linked config root: %v", err)
	}
	data, err := os.ReadFile(outsideSentinel)
	if err != nil || string(data) != "outside-must-not-change" {
		t.Fatalf("outside sentinel changed: data=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "outside.txt" {
		t.Fatalf("outside entry set changed: %v", entries)
	}
}

func TestBackgroundRuntimeResetValidatesConfigRootBeforeMarkerOrInbox(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	injected := errors.New("injected unsafe config root")
	validationHits := 0
	store.validateResetRoot = func(path string) error {
		validationHits++
		if filepath.Clean(path) != filepath.Clean(filepath.Dir(store.path)) {
			t.Fatalf("validated root=%q, want %q", path, filepath.Dir(store.path))
		}
		return injected
	}
	markerWrites := 0
	store.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		markerWrites++
		return writeFileAtomicallyOutcome(path, data)
	}
	runtime := newBackgroundRuntime(store, nil)
	inbox := &startupMarkerOrderingInbox{resetCalled: make(chan struct{}), nextCalled: make(chan bool, 1)}
	runtime.installInbox(inbox, inbox.Health())

	if err := runtime.Reset(); !errors.Is(err, injected) {
		t.Fatalf("reset error=%v, want root validation failure", err)
	}
	if validationHits != 1 || markerWrites != 0 {
		t.Fatalf("root validations=%d marker writes=%d, want 1/0", validationHits, markerWrites)
	}
	select {
	case <-inbox.resetCalled:
		t.Fatal("inbox reset ran before config-root validation")
	default:
	}
}

func (*startupResetBarrierInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (*startupResetBarrierInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (*startupResetBarrierInbox) Acknowledge(string) error { return nil }
func (*startupResetBarrierInbox) Release(string) error     { return nil }
func (*startupResetBarrierInbox) Close() error             { return nil }
func (inbox *startupResetBarrierInbox) Health() giftInboxHealth {
	inbox.mu.Lock()
	health := inbox.health
	inbox.mu.Unlock()
	inbox.healthOnce.Do(func() {
		inbox.healthCaptured <- health
		<-inbox.releaseHealth
	})
	return health
}
func (inbox *startupResetBarrierInbox) Reset() error {
	inbox.resetOnce.Do(func() { close(inbox.resetCalled) })
	inbox.mu.Lock()
	inbox.health = giftInboxHealth{Revision: inbox.health.Revision + 1}
	inbox.mu.Unlock()
	return nil
}

func (inbox *snapshotBarrierGiftInbox) blockNextSnapshot() (<-chan giftInboxHealth, chan struct{}) {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	if inbox.blockNext {
		panic("snapshot barrier is already armed")
	}
	inbox.blockNext = true
	inbox.snapshotCaptured = make(chan giftInboxHealth, 1)
	inbox.releaseSnapshot = make(chan struct{})
	return inbox.snapshotCaptured, inbox.releaseSnapshot
}

func (inbox *snapshotBarrierGiftInbox) SnapshotHealth() giftInboxHealth {
	health := inbox.giftInbox.SnapshotHealth()
	inbox.mu.Lock()
	if !inbox.blockNext {
		inbox.mu.Unlock()
		return health
	}
	inbox.blockNext = false
	captured := inbox.snapshotCaptured
	release := inbox.releaseSnapshot
	inbox.mu.Unlock()
	captured <- health
	<-release
	return health
}

func TestRuntimeStatusUsesPublishedInboxSnapshotWithoutHealthReconciliation(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	inbox := &runtimeStatusProbeInbox{}
	runtime.installInbox(inbox, giftInboxHealth{PendingCount: 3, OldestPendingAt: 2000})
	for range 100 {
		if got := runtime.Status().Inbox; got == nil || got.PendingCount != 3 {
			t.Fatalf("status inbox = %#v, want published snapshot", got)
		}
	}
	if inbox.healthCalls != 0 {
		t.Fatalf("Status called Health %d times, want 0", inbox.healthCalls)
	}
}

func TestRuntimeInboxPublicationDoesNotRequireComparableImplementation(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	inbox := nonComparableRuntimeInbox{values: []int{1}}
	installation := runtime.installInbox(inbox, inbox.Health())
	runtime.publishInbox(installation, giftInboxHealth{Revision: 1, PendingCount: 2})
	if status := runtime.Status(); status.Inbox == nil || status.Inbox.PendingCount != 2 {
		t.Fatalf("published inbox = %#v, want non-comparable implementation snapshot", status.Inbox)
	}
}

func TestBackgroundRuntimeDelayedAckCannotOverwriteNewerRefillOrClearCapacityFailure(t *testing.T) {
	oldRecordLimit, oldByteLimit := giftInboxRecordLimit, giftInboxByteLimit
	giftInboxRecordLimit, giftInboxByteLimit = 1, 1<<20
	defer func() { giftInboxRecordLimit, giftInboxByteLimit = oldRecordLimit, oldByteLimit }()

	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	opened, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	inbox := &snapshotBarrierGiftInbox{giftInbox: opened}
	runtime := newBackgroundRuntime(store, nil)
	installation := runtime.installInbox(inbox, inbox.SnapshotHealth())
	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{GiftID: 1, GiftName: "first", Num: 1, Rnd: "first"})
	record, ok, err := inbox.Next()
	if err != nil || !ok {
		t.Fatalf("Next() = %#v, %t, %v", record, ok, err)
	}

	captured, release := inbox.blockNextSnapshot()
	ackDone := make(chan error, 1)
	go func() {
		ackDone <- runtime.consumeClaimedInboxRecord(context.Background(), installation, record)
	}()
	ackHealth := <-captured
	if ackHealth.Revision != 5 || ackHealth.PendingCount != 0 || ackHealth.CapacityError {
		t.Fatalf("captured Ack snapshot = %#v, want drained revision 5", ackHealth)
	}

	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{GiftID: 2, GiftName: "refill", Num: 1, Rnd: "refill"})
	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{GiftID: 3, GiftName: "rejected", Num: 1, Rnd: "rejected"})
	close(release)
	if err := <-ackDone; err != nil {
		t.Fatal(err)
	}
	status := runtime.Status()
	if status.Inbox == nil || status.Inbox.Revision != 7 || status.Inbox.PendingCount != 1 || !status.Inbox.CapacityError {
		t.Fatalf("published inbox after delayed Ack = %#v, want full revision 7 refill", status.Inbox)
	}
	if status.IngestionErrorKind != "inbox_capacity" {
		t.Fatalf("ingestion error kind = %q, want newer capacity rejection retained", status.IngestionErrorKind)
	}
}

func TestBackgroundRuntimeRetiredInboxCannotPublishAfterReplacement(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(root, "old")
	pending := filepath.Join(oldRoot, "gift-inbox", "pending")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	oldRecord := giftInboxRecord{
		SchemaVersion: 1, LocalSequence: 1, IngestionID: strings.Repeat("1", 32),
		RoomID: "room", Command: "SEND_GIFT", ReceivedAt: 1_800_000_000,
		Gift: giftEvent{GiftID: 1, GiftName: "old", Num: 1, Rnd: "old"},
	}
	writeGiftInboxFixture(t, filepath.Join(pending, giftInboxFilename(1, oldRecord.IngestionID)), oldRecord)
	openedOld, err := openGiftInbox(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer openedOld.Close()
	oldInbox := &snapshotBarrierGiftInbox{giftInbox: openedOld}
	var record giftInboxRecord
	var ok bool
	for range 4 {
		record, ok, err = oldInbox.Next()
		if err != nil || !ok {
			t.Fatalf("old Next() = %#v, %t, %v", record, ok, err)
		}
		if err := oldInbox.Release(record.IngestionID); err != nil {
			t.Fatal(err)
		}
	}
	if health := oldInbox.SnapshotHealth(); health.Revision != 9 {
		t.Fatalf("old installed health = %#v, want revision 9", health)
	}

	runtime := newBackgroundRuntime(store, nil)
	oldInstallation := runtime.installInbox(oldInbox, oldInbox.SnapshotHealth())
	captured, release := oldInbox.blockNextSnapshot()
	ackDone := make(chan error, 1)
	go func() {
		ackDone <- runtime.consumeClaimedInboxRecord(context.Background(), oldInstallation, record)
	}()
	health := <-captured
	if health.Revision != 10 || health.PendingCount != 0 {
		t.Fatalf("delayed old Ack snapshot = %#v, want empty revision 10", health)
	}

	newInbox, err := openGiftInbox(filepath.Join(root, "new"))
	if err != nil {
		t.Fatal(err)
	}
	defer newInbox.Close()
	if health := newInbox.SnapshotHealth(); health.Revision != 1 {
		t.Fatalf("new installed health = %#v, want revision 1", health)
	}
	runtime.installInbox(newInbox, newInbox.SnapshotHealth())
	if status := runtime.Status(); status.Inbox == nil || status.Inbox.Revision != 1 || status.Inbox.PendingCount != 0 {
		t.Fatalf("new installed runtime snapshot = %#v, want empty revision 1", status.Inbox)
	}
	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{GiftID: 2, GiftName: "new", Num: 1, Rnd: "new"})
	close(release)
	if err := <-ackDone; err != nil {
		t.Fatal(err)
	}
	if status := runtime.Status(); status.Inbox == nil || status.Inbox.Revision != 3 || status.Inbox.PendingCount != 1 {
		t.Fatalf("retired revision 10 publication replaced new inbox: %#v", status.Inbox)
	}
}

func TestBackgroundRuntimePublishesOpenedInboxBeforeConcurrentStatus(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if status := runtime.Status(); status.Inbox != nil {
			for range 100 {
				_ = runtime.Status()
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Run did not publish the opened inbox to concurrent status readers")
}

func TestBackgroundRuntimeStartupInboxInstallSerializesWithReset(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	inbox := &startupResetBarrierInbox{
		health: giftInboxHealth{
			Revision: 7, PendingCount: 3, PendingBytes: 42,
			OldestPendingAt: 1_700_000_000, CapacityError: true,
		},
		healthCaptured: make(chan giftInboxHealth, 1),
		releaseHealth:  make(chan struct{}),
		resetCalled:    make(chan struct{}),
	}
	runtime := newBackgroundRuntime(store, nil)
	// Exercise Run's supported epoch-zero preinstallation path, before startup
	// snapshots and publishes the supplied inbox.
	runtime.mu.Lock()
	runtime.inbox = inbox
	runtime.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(runDone)
	}()
	defer func() {
		select {
		case <-inbox.releaseHealth:
		default:
			close(inbox.releaseHealth)
		}
		cancel()
		<-runDone
	}()

	captured := <-inbox.healthCaptured
	if captured.Revision != 7 || captured.PendingCount != 3 || !captured.CapacityError {
		t.Fatalf("captured startup health = %#v", captured)
	}
	resetAttempted := make(chan struct{})
	resetDone := make(chan error, 1)
	go func() {
		close(resetAttempted)
		resetDone <- runtime.Reset()
	}()
	<-resetAttempted
	select {
	case <-inbox.resetCalled:
		t.Fatal("Reset entered cleanup before startup installed and published its captured health")
	case <-time.After(50 * time.Millisecond):
	}

	close(inbox.releaseHealth)
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	status := runtime.Status()
	if status.Inbox == nil || status.Inbox.Revision != 8 || status.Inbox.PendingCount != 0 || status.Inbox.PendingBytes != 0 || status.Inbox.OldestPendingAt != 0 || status.Inbox.CapacityError {
		t.Fatalf("published inbox after startup/reset = %#v, want reset revision 8", status.Inbox)
	}
}

func TestRuntimeLastFrameIsRoomScoped(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	runtime.setStatus("connected", "room-a", nil)
	runtime.recordLastFrame("room-a")
	if runtime.Status().LastFrameAt == 0 {
		t.Fatal("room-a frame was not recorded")
	}
	runtime.setStatus("connecting", "room-b", nil)
	if runtime.Status().LastFrameAt != 0 {
		t.Fatal("room change retained prior room frame timestamp")
	}
	runtime.recordLastFrame("room-a")
	if runtime.Status().LastFrameAt != 0 {
		t.Fatal("old room callback updated current room frame timestamp")
	}
}

func TestBackgroundRuntimeReportsInboxCapacityWithoutVolatileApply(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(&capacityFailureInbox{}, giftInboxHealth{CapacityError: true})

	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{GiftID: 1, GiftName: "礼物", Num: 1})

	if got := runtime.Status().IngestionError; got != errGiftInboxCapacity.Error() {
		t.Fatalf("runtime error = %q, want %q", got, errGiftInboxCapacity.Error())
	}
	assertRuntimeAttributeValue(t, store, "积分", 0)
}

func TestBackgroundRuntimeTransactionRecoveryFailurePreservesInboxOrder(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		if _, err := inbox.Accept("room", "SEND_GIFT", giftEvent{
			GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "blocked-" + leftPadThree(index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(store.stateTransactionPath(), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	runtime.inboxRetryDelay = time.Hour
	cancel, done := startBackgroundRuntimeForTest(runtime)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runtime.Status().IngestionErrorKind != "transaction_recovery" {
		time.Sleep(10 * time.Millisecond)
	}
	if status := runtime.Status(); status.IngestionErrorKind != "transaction_recovery" || !status.TransactionPending {
		t.Fatalf("degraded recovery status = %#v", status)
	}
	cancel()
	<-done

	if health := inbox.Health(); health.PendingCount != 2 {
		t.Fatalf("pending after transaction recovery failure = %d, want 2", health.PendingCount)
	}
	if err := os.Remove(store.stateTransactionPath()); err != nil {
		t.Fatal(err)
	}
	assertRuntimeAttributeValue(t, &configStore{path: filepath.Join(root, "config.json")}, "积分", 0)
}

func TestBackgroundRuntimePersistsAnimationFirstMergeAcrossCrashAndRestart(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	runtime.inboxRetryDelay = time.Hour
	if err := runtime.savePendingGiftAnimationFile(pendingGiftAnimationFile{
		SchemaVersion: pendingGiftAnimationsSchemaVersion, PreparedRoomID: "room-a", Records: []pendingGiftAnimation{},
	}); err != nil {
		t.Fatal(err)
	}
	runtime.acceptGift(context.Background(), "room-a", "GUARD_BUY", giftEvent{
		GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 1_700_000_001,
		Membership: "captain", EffectID: 9001, EffectMP4: "https://i0.hdslb.com/guard.mp4",
		EffectMP4JSON: "https://i0.hdslb.com/guard.json", AnimationOnly: true,
	})
	failed := false
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "events.log" && !failed {
			failed = true
			return errors.New("injected animation settlement failure")
		}
		return writeFileAtomically(path, data)
	}
	cancel, done := startBackgroundRuntimeForTest(runtime)
	waitForIngestionError(t, runtime, "injected animation settlement failure")
	cancel()
	<-done

	recoveredStore := &configStore{path: filepath.Join(root, "config.json")}
	recoveredInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered := newBackgroundRuntime(recoveredStore, nil)
	recovered.installInbox(recoveredInbox, recoveredInbox.SnapshotHealth())
	cancelRecovered, recoveredDone := startBackgroundRuntimeForTest(recovered)
	waitForInboxPendingCount(t, recoveredInbox, 0)
	cancelRecovered()
	<-recoveredDone

	purchaseInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	purchaseRuntime := newBackgroundRuntime(recoveredStore, nil)
	purchaseRuntime.installInbox(purchaseInbox, purchaseInbox.SnapshotHealth())
	purchaseRuntime.acceptGift(context.Background(), "room-a", "GUARD_BUY", giftEvent{
		GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 1,
		UID: 42, Uname: "舰长观众", Timestamp: 1_700_000_002, Rnd: "purchase-after-animation",
	})
	cancelPurchase, purchaseDone := startBackgroundRuntimeForTest(purchaseRuntime)
	waitForInboxPendingCount(t, purchaseInbox, 0)
	cancelPurchase()
	<-purchaseDone

	updated, err := recoveredStore.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.GiftReceipts) != 1 || updated.GiftReceipts[0].Animation == nil || updated.GiftReceipts[0].Animation.EffectID != 9001 {
		t.Fatalf("restart-safe animation receipt = %#v", updated.GiftReceipts)
	}
	if len(updated.AppliedIngressIDs) != 2 {
		t.Fatalf("applied ingestion IDs = %d, want 2", len(updated.AppliedIngressIDs))
	}
}

func TestBackgroundRuntimeProcessesCommittedInboxRecordAfterDurabilityWarningWithoutRetry(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	state.Attributes = []attributeState{{Name: "points", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "points", Formula: "points+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected post-rename directory sync failure")
	failNextCommittedGiftInboxRecordWrite(inbox, injected)
	runtime := newBackgroundRuntime(store, func() giftEventSource { return &stableConnectedSource{} })
	runtime.installInbox(inbox, inbox.SnapshotHealth())
	if err := runtime.savePendingGiftAnimationFile(pendingGiftAnimationFile{
		SchemaVersion: pendingGiftAnimationsSchemaVersion, PreparedRoomID: "room-a", Records: []pendingGiftAnimation{},
	}); err != nil {
		t.Fatal(err)
	}

	runtime.acceptGift(context.Background(), "room-a", "SEND_GIFT", giftEvent{
		GiftID: 1, GiftName: "committed", Num: 1, Timestamp: 1_700_000_001, Rnd: "committed-warning",
	})
	if status := runtime.Status(); status.IngestionErrorKind != "inbox_durability" {
		t.Fatalf("ingestion error kind = %q, want inbox_durability", status.IngestionErrorKind)
	}
	if health := inbox.SnapshotHealth(); health.PendingCount != 1 {
		t.Fatalf("pending immediately after warning = %d, want 1", health.PendingCount)
	}

	cancel, done := startBackgroundRuntimeForTest(runtime)
	waitForInboxPendingCount(t, inbox, 0)
	cancel()
	<-done
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	attribute := updated.findAttribute("points")
	if attribute == nil || attribute.Value != 1 {
		t.Fatalf("settled attribute = %#v, want exactly one application", attribute)
	}
	if len(updated.AppliedIngressIDs) != 1 {
		t.Fatalf("applied ingestion IDs = %d, want exactly 1", len(updated.AppliedIngressIDs))
	}
}

func TestBackgroundRuntimeRecoversPurchaseAfterPendingAnimationWasPrepared(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "guard", GiftID: specialGiftGuardCaptain, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	animation := giftInboxRecord{
		IngestionID: strings.Repeat("d", 32), RoomID: "room-a", Command: "GUARD_BUY",
		Gift: giftEvent{
			GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 1_700_000_001,
			EffectID: 9001, EffectMP4: "https://i0.hdslb.com/guard.mp4", EffectMP4JSON: "https://i0.hdslb.com/guard.json", AnimationOnly: true,
		},
	}
	if err := newBackgroundRuntime(store, nil).processInboxRecord(context.Background(), animation); err != nil {
		t.Fatal(err)
	}
	metadata, err := newBackgroundRuntime(store, nil).loadPendingGiftAnimationFile()
	if err != nil {
		t.Fatal(err)
	}
	metadata.PreparedRoomID = "room-a"
	if err := newBackgroundRuntime(store, nil).savePendingGiftAnimationFile(metadata); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	runtime.inboxRetryDelay = time.Hour
	runtime.acceptGift(context.Background(), "room-a", "GUARD_BUY", giftEvent{
		GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 1, UID: 42,
		Timestamp: 1_700_000_002, Rnd: "purchase-prepared-crash",
	})
	failed := false
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "events.log" && !failed {
			failed = true
			return errors.New("injected purchase settlement failure")
		}
		return writeFileAtomically(path, data)
	}
	cancel, done := startBackgroundRuntimeForTest(runtime)
	waitForIngestionError(t, runtime, "injected purchase settlement failure")
	cancel()
	<-done

	recoveredStore := &configStore{path: filepath.Join(root, "config.json")}
	recoveredInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered := newBackgroundRuntime(recoveredStore, nil)
	recovered.installInbox(recoveredInbox, recoveredInbox.SnapshotHealth())
	cancelRecovered, recoveredDone := startBackgroundRuntimeForTest(recovered)
	waitForInboxPendingCount(t, recoveredInbox, 0)
	cancelRecovered()
	<-recoveredDone
	updated, err := recoveredStore.readState()
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attributes[0].Value != 1 || len(updated.GiftReceipts) != 1 || updated.GiftReceipts[0].Animation == nil || updated.GiftReceipts[0].Animation.EffectID != 9001 {
		t.Fatalf("recovered purchase state = attribute=%v receipts=%#v", updated.Attributes[0].Value, updated.GiftReceipts)
	}
}

func TestBackgroundRuntimeDoesNotApplyBacklogFromAnotherRoom(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := inbox.Accept("room-a", "SEND_GIFT", giftEvent{GiftID: 1, GiftName: "旧房间", Num: 1, Rnd: "same-rnd"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := inbox.Accept("room-b", "SEND_GIFT", giftEvent{GiftID: 1, GiftName: "当前房间", Num: 1, Rnd: "same-rnd"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	cancel, done := startBackgroundRuntimeForTest(runtime)
	waitForInboxPendingCount(t, inbox, 0)
	cancel()
	<-done
	assertRuntimeAttributeValue(t, store, "积分", 1)
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !testContainsString(updated.AppliedIngressIDs, first.IngestionID) || !testContainsString(updated.AppliedIngressIDs, second.IngestionID) {
		t.Fatalf("room-scoped applied IDs = %#v", updated.AppliedIngressIDs)
	}
	if len(updated.Log) != 1 || updated.Log[0].GiftName != "当前房间" {
		t.Fatalf("room-scoped gift log = %#v", updated.Log)
	}
}

func TestBackgroundRuntimeRoomSwitchResetsRoomScopedIngestionMetadata(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	state.RecentSourceGiftKeys["same-rnd"] = time.Now().UnixMilli()
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	if err := runtime.savePendingGiftAnimations([]pendingGiftAnimation{
		{RoomID: "room-a", Gift: giftEvent{GiftID: 1, UID: 42, Timestamp: 1, EffectID: 99, AnimationOnly: true}},
		{RoomID: "room-b", Gift: giftEvent{GiftID: 2, UID: 43, Timestamp: 2, EffectID: 100, AnimationOnly: true}},
	}); err != nil {
		t.Fatal(err)
	}
	metadata, err := runtime.loadPendingGiftAnimationFile()
	if err != nil {
		t.Fatal(err)
	}
	metadata.PreparedRoomID = "room-a"
	if err := runtime.savePendingGiftAnimationFile(metadata); err != nil {
		t.Fatal(err)
	}
	runtime.setStatus("connected", "room-a", nil)
	if err := runtime.prepareRoomConnection("room-b"); err != nil {
		t.Fatal(err)
	}
	record := giftInboxRecord{
		IngestionID: strings.Repeat("b", 32), RoomID: "room-b", Command: "SEND_GIFT",
		Gift: giftEvent{GiftID: 1, GiftName: "新房间", Num: 1, Rnd: "same-rnd"},
	}
	if err := runtime.processInboxRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attributes[0].Value != 1 || len(updated.GiftReceipts) != 1 {
		t.Fatalf("room switch state = attribute=%v receipts=%#v", updated.Attributes[0].Value, updated.GiftReceipts)
	}
	pending, err := runtime.loadPendingGiftAnimations()
	if err != nil || len(pending) != 1 || pending[0].RoomID != "room-b" || pending[0].Gift.EffectID != 100 {
		t.Fatalf("pending animations after room switch = %#v, err=%v", pending, err)
	}
}

func TestBackgroundRuntimeRetriesFailedRoomPreparationBeforeStartingSource(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	state.RecentSourceGiftKeys["old-room-rnd"] = time.Now().UnixMilli()
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.setStatus("connected", "room-a", nil)
	if err := runtime.savePendingGiftAnimations([]pendingGiftAnimation{{RoomID: "room-a", Gift: giftEvent{GiftID: 1, UID: 42, AnimationOnly: true}}}); err != nil {
		t.Fatal(err)
	}
	failed := false
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "events.log" && !failed {
			failed = true
			return errors.New("injected room preparation failure")
		}
		return writeFileAtomically(path, data)
	}
	if err := runtime.prepareRoomConnection("room-b"); err == nil {
		t.Fatal("first room preparation unexpectedly succeeded")
	}
	runtime.setStatus("error", "room-b", errors.New("injected room preparation failure"))
	if err := runtime.prepareRoomConnection("room-b"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RecentSourceGiftKeys) != 0 {
		t.Fatalf("old-room dedupe survived retry: %#v", updated.RecentSourceGiftKeys)
	}
	pending, err := runtime.loadPendingGiftAnimations()
	if err != nil || len(pending) != 0 {
		t.Fatalf("old-room animations survived retry: %#v, err=%v", pending, err)
	}
}

func TestBackgroundRuntimeMigratesEmptyPreparedRoomBeforeStartingTargetSource(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	state.RecentSourceGiftKeys["old-room-rnd"] = time.Now().UnixMilli()
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	seed := newBackgroundRuntime(store, nil)
	if err := seed.savePendingGiftAnimationFile(pendingGiftAnimationFile{
		SchemaVersion:  pendingGiftAnimationsSchemaVersion,
		PreparedRoomID: "",
		Records: []pendingGiftAnimation{{
			RoomID: "room-a",
			Gift:   giftEvent{GiftID: 1, UID: 42, EffectID: 99, AnimationOnly: true},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	inspected := make(chan error, 1)
	source := &inspectionGiftSource{inspect: func() error {
		startedState, err := store.readState()
		if err != nil {
			return err
		}
		if len(startedState.RecentSourceGiftKeys) != 0 {
			return fmt.Errorf("source started with old-room dedupe: %#v", startedState.RecentSourceGiftKeys)
		}
		metadata, err := newBackgroundRuntime(store, nil).loadPendingGiftAnimationFile()
		if err != nil {
			return err
		}
		if metadata.PreparedRoomID != "room-b" || len(metadata.Records) != 0 {
			return fmt.Errorf("source started with unmigrated metadata: %#v", metadata)
		}
		return nil
	}, inspected: inspected}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return source })
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		cancel()
		<-done
	}()
	select {
	case err := <-inspected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("target source did not start after empty-marker migration")
	}
}

type preparationBarrierInbox struct {
	runtimeGiftInbox
	claimed      chan struct{}
	allowClaim   chan struct{}
	released     chan struct{}
	claimedOnce  sync.Once
	releasedOnce sync.Once
}

func (inbox *preparationBarrierInbox) Next() (giftInboxRecord, bool, error) {
	record, ok, err := inbox.runtimeGiftInbox.Next()
	if ok && err == nil {
		inbox.claimedOnce.Do(func() { close(inbox.claimed) })
		<-inbox.allowClaim
	}
	return record, ok, err
}

func (inbox *preparationBarrierInbox) Release(ingestionID string) error {
	err := inbox.runtimeGiftInbox.Release(ingestionID)
	if err == nil {
		inbox.releasedOnce.Do(func() { close(inbox.released) })
	}
	return err
}

func TestBackgroundRuntimeConsumerWaitsForPreparationBeforeSettlingAndAcknowledging(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	state.RecentSourceGiftKeys["old-room-rnd"] = time.Now().UnixMilli()
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	seed := newBackgroundRuntime(store, nil)
	if err := seed.savePendingGiftAnimationFile(pendingGiftAnimationFile{
		SchemaVersion:  pendingGiftAnimationsSchemaVersion,
		PreparedRoomID: "",
		Records:        []pendingGiftAnimation{{RoomID: "room-a", Gift: giftEvent{GiftID: 2, UID: 7, AnimationOnly: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	realInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := realInbox.Accept("room-b", "SEND_GIFT", giftEvent{
		GiftID: 1, GiftName: "first", Num: 1, UID: 42, Uname: "字***", Rnd: "target-rnd",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox := &preparationBarrierInbox{
		runtimeGiftInbox: realInbox,
		claimed:          make(chan struct{}),
		allowClaim:       make(chan struct{}),
		released:         make(chan struct{}),
	}
	preparationWriteStarted := make(chan struct{})
	allowPreparationWrite := make(chan struct{})
	var allowPreparationOnce sync.Once
	allowRoomPreparation := func() { allowPreparationOnce.Do(func() { close(allowPreparationWrite) }) }
	sourceStarted := make(chan struct{})
	sourceConstructed := make(chan struct{})
	runtime := newBackgroundRuntime(store, func() giftEventSource {
		close(sourceConstructed)
		return &signalingStableSource{started: sourceStarted}
	})
	runtime.animationWriteAtomically = func(path string, data []byte) error {
		close(preparationWriteStarted)
		<-allowPreparationWrite
		return writeFileAtomically(path, data)
	}
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	resolver := &blockingUserProfileResolver{started: make(chan struct{}), release: make(chan struct{})}
	runtime.profileResolver = resolver
	runtime.profileTimeout = time.Hour
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		resolver.unblock()
		allowRoomPreparation()
		cancel()
		<-done
	}()
	select {
	case <-inbox.claimed:
	case <-time.After(time.Second):
		t.Fatal("consumer did not claim the pending target-room record")
	}
	select {
	case <-preparationWriteStarted:
	case <-time.After(time.Second):
		t.Fatal("room preparation did not begin")
	}
	close(inbox.allowClaim)
	select {
	case <-inbox.released:
	case <-resolver.started:
		t.Fatal("consumer began target-room settlement before room preparation")
	case <-time.After(50 * time.Millisecond):
		// The durable preparation writer may hold animationMu while the consumer
		// waits to inspect the prepared-room marker. Either that wait or release
		// is valid; settlement is not.
	}
	if realInbox.Health().PendingCount != 1 {
		t.Fatalf("record acknowledged before preparation: pending=%d", realInbox.Health().PendingCount)
	}
	assertRuntimeAttributeValue(t, store, "积分", 0)
	select {
	case <-sourceConstructed:
		t.Fatal("source factory ran before durable room preparation")
	case <-sourceStarted:
		t.Fatal("source Run started before durable room preparation")
	default:
	}

	allowRoomPreparation()
	select {
	case <-sourceConstructed:
	case <-time.After(time.Second):
		t.Fatal("source factory did not run after preparation")
	}
	select {
	case <-sourceStarted:
	case <-time.After(time.Second):
		t.Fatal("source did not start after preparation")
	}
	select {
	case <-resolver.started:
	case <-time.After(time.Second):
		t.Fatal("consumer did not resume after preparation")
	}
	resolver.unblock()
	waitForInboxPendingCount(t, realInbox, 0)
	assertRuntimeAttributeValue(t, store, "积分", 1)

	second, err := realInbox.Accept("room-b", "SEND_GIFT", giftEvent{
		GiftID: 1, GiftName: "second", Num: 1, UID: 42, Rnd: "target-rnd",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case runtime.inboxWake <- struct{}{}:
	default:
	}
	waitForInboxPendingCount(t, realInbox, 0)
	assertRuntimeAttributeValue(t, store, "积分", 1)
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !testContainsString(updated.AppliedIngressIDs, first.IngestionID) || !testContainsString(updated.AppliedIngressIDs, second.IngestionID) {
		t.Fatalf("prepared duplicate ingestion IDs = %#v", updated.AppliedIngressIDs)
	}
}

func TestBackgroundRuntimeRoomPreparationSerializesConcurrentAnimationAdd(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.setStatus("connected", "room-a", nil)
	writeStarted := make(chan struct{})
	allowWrite := make(chan struct{})
	var once sync.Once
	runtime.animationWriteAtomically = func(path string, data []byte) error {
		once.Do(func() { close(writeStarted); <-allowWrite })
		return writeFileAtomically(path, data)
	}
	prepared := make(chan error, 1)
	go func() { prepared <- runtime.prepareRoomConnection("room-b") }()
	<-writeStarted
	added := make(chan error, 1)
	go func() {
		added <- runtime.addPendingGiftAnimation("room-b", giftEvent{GiftID: 1, UID: 42, EffectID: 7, AnimationGIF: "https://i0.hdslb.com/a.gif", AnimationOnly: true})
	}()
	select {
	case err := <-added:
		t.Fatalf("concurrent add bypassed preparation lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowWrite)
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	if err := <-added; err != nil {
		t.Fatal(err)
	}
	pending, err := runtime.loadPendingGiftAnimations()
	if err != nil || len(pending) != 1 || pending[0].RoomID != "room-b" {
		t.Fatalf("pending after serialized add = %#v err=%v", pending, err)
	}
}

func TestBackgroundRuntimeRoomPreparationSerializesConcurrentAnimationDelete(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.setStatus("connected", "room-a", nil)
	if err := runtime.savePendingGiftAnimations([]pendingGiftAnimation{{RoomID: "room-b", Gift: giftEvent{GiftID: 1, UID: 42, Timestamp: 10, EffectID: 7, AnimationGIF: "https://i0.hdslb.com/a.gif", AnimationOnly: true}}}); err != nil {
		t.Fatal(err)
	}
	writeStarted := make(chan struct{})
	allowWrite := make(chan struct{})
	var once sync.Once
	runtime.animationWriteAtomically = func(path string, data []byte) error {
		once.Do(func() { close(writeStarted); <-allowWrite })
		return writeFileAtomically(path, data)
	}
	prepared := make(chan error, 1)
	go func() { prepared <- runtime.prepareRoomConnection("room-b") }()
	<-writeStarted
	deleted := make(chan error, 1)
	go func() {
		deleted <- runtime.processInboxRecord(context.Background(), giftInboxRecord{IngestionID: strings.Repeat("9", 32), RoomID: "room-b", Gift: giftEvent{GiftID: 1, UID: 42, Timestamp: 11, Rnd: "serialized-delete"}})
	}()
	select {
	case err := <-deleted:
		t.Fatalf("concurrent delete bypassed preparation lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowWrite)
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	pending, err := runtime.loadPendingGiftAnimations()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending resurrected after serialized delete = %#v err=%v", pending, err)
	}
}

func TestBackgroundRuntimePendingAnimationIsPrivateAndSurvivesHistoryClear(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	for index := 0; index < maxGiftReceiptEntries; index++ {
		state.GiftReceipts = append(state.GiftReceipts, giftReceipt{ID: fmt.Sprintf("real-%03d", index), GiftID: index + 1, Effects: []giftReceiptEffect{}})
	}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	if err := runtime.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: strings.Repeat("e", 32), RoomID: "room-a", Command: "GUARD_BUY",
		Gift: giftEvent{GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 10, EffectID: 9001, EffectMP4: "https://i0.hdslb.com/a.mp4", EffectMP4JSON: "https://i0.hdslb.com/a.json", AnimationOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.GiftReceipts) != maxGiftReceiptEntries || updated.GiftReceipts[0].ID != "real-000" || updated.GiftReceipts[maxGiftReceiptEntries-1].ID != fmt.Sprintf("real-%03d", maxGiftReceiptEntries-1) {
		t.Fatalf("pending animation changed public receipt history: count=%d first=%q last=%q", len(updated.GiftReceipts), updated.GiftReceipts[0].ID, updated.GiftReceipts[len(updated.GiftReceipts)-1].ID)
	}
	if _, err := store.updateState(func(state *appState) error {
		state.GiftReceipts = []giftReceipt{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := runtime.loadPendingGiftAnimations()
	if err != nil || len(pending) != 1 || pending[0].Gift.EffectID != 9001 {
		t.Fatalf("pending animation after history clear = %#v, err=%v", pending, err)
	}
	restarted := newBackgroundRuntime(&configStore{path: filepath.Join(root, "config.json")}, nil)
	if err := restarted.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: strings.Repeat("f", 32), RoomID: "room-a", Command: "GUARD_BUY",
		Gift: giftEvent{GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 1, UID: 42, Timestamp: 11, Rnd: "private-animation-purchase"},
	}); err != nil {
		t.Fatal(err)
	}
	merged, err := restarted.store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.GiftReceipts) != 1 || merged.GiftReceipts[0].Animation == nil || merged.GiftReceipts[0].Animation.EffectID != 9001 {
		t.Fatalf("private pending animation did not merge once: %#v", merged.GiftReceipts)
	}
	pending, err = restarted.loadPendingGiftAnimations()
	if err != nil || len(pending) != 0 {
		t.Fatalf("private pending animation not consumed: %#v, err=%v", pending, err)
	}
}

func TestBackgroundRuntimeCompletesPrivateAnimationWriteAfterCommittedIngestion(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := inbox.Accept("room-a", "GUARD_BUY", giftEvent{
		GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 10, EffectID: 9001,
		EffectMP4: "https://i0.hdslb.com/a.mp4", EffectMP4JSON: "https://i0.hdslb.com/a.json", AnimationOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	runtime.inboxRetryDelay = time.Hour
	if err := runtime.savePendingGiftAnimationFile(pendingGiftAnimationFile{
		SchemaVersion: pendingGiftAnimationsSchemaVersion, PreparedRoomID: "room-a", Records: []pendingGiftAnimation{},
	}); err != nil {
		t.Fatal(err)
	}
	failed := false
	runtime.animationWriteAtomically = func(path string, data []byte) error {
		if !failed {
			failed = true
			return errors.New("injected private animation write failure")
		}
		return writeFileAtomically(path, data)
	}
	cancel, done := startBackgroundRuntimeForTest(runtime)
	waitForIngestionError(t, runtime, "injected private animation write failure")
	cancel()
	<-done
	committed, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !testContainsString(committed.AppliedIngressIDs, record.IngestionID) || inbox.Health().PendingCount != 1 {
		t.Fatalf("committed animation ingestion not retained for completion: applied=%#v pending=%d", committed.AppliedIngressIDs, inbox.Health().PendingCount)
	}

	recoveredInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered := newBackgroundRuntime(&configStore{path: filepath.Join(root, "config.json")}, nil)
	recovered.installInbox(recoveredInbox, recoveredInbox.SnapshotHealth())
	cancelRecovered, recoveredDone := startBackgroundRuntimeForTest(recovered)
	waitForInboxPendingCount(t, recoveredInbox, 0)
	cancelRecovered()
	<-recoveredDone
	pending, err := recovered.loadPendingGiftAnimations()
	if err != nil || len(pending) != 1 || pending[0].Gift.EffectID != 9001 {
		t.Fatalf("private animation completion = %#v, err=%v", pending, err)
	}
}

func TestBackgroundRuntimeCompletesPrivateAnimationDeleteAfterCommittedPurchase(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "guard", GiftID: specialGiftGuardCaptain, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	seed := newBackgroundRuntime(store, nil)
	if err := seed.addPendingGiftAnimation("room-a", giftEvent{
		GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 10, EffectID: 9001,
		EffectMP4: "https://i0.hdslb.com/a.mp4", EffectMP4JSON: "https://i0.hdslb.com/a.json", AnimationOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	metadata, err := seed.loadPendingGiftAnimationFile()
	if err != nil {
		t.Fatal(err)
	}
	metadata.PreparedRoomID = "room-a"
	if err := seed.savePendingGiftAnimationFile(metadata); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := inbox.Accept("room-a", "GUARD_BUY", giftEvent{
		GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 1, UID: 42, Timestamp: 11, Rnd: "delete-after-commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	runtime.inboxRetryDelay = time.Hour
	failed := false
	runtime.animationWriteAtomically = func(path string, data []byte) error {
		if !failed {
			failed = true
			return errors.New("injected private animation delete failure")
		}
		return writeFileAtomically(path, data)
	}
	cancel, done := startBackgroundRuntimeForTest(runtime)
	waitForIngestionError(t, runtime, "injected private animation delete failure")
	cancel()
	<-done
	committed, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !testContainsString(committed.AppliedIngressIDs, record.IngestionID) || committed.Attributes[0].Value != 1 || len(committed.GiftReceipts) != 1 || inbox.Health().PendingCount != 1 {
		t.Fatalf("committed purchase not retained for delete completion: state=%#v pending=%d", committed, inbox.Health().PendingCount)
	}

	recoveredInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered := newBackgroundRuntime(&configStore{path: filepath.Join(root, "config.json")}, nil)
	recovered.installInbox(recoveredInbox, recoveredInbox.SnapshotHealth())
	cancelRecovered, recoveredDone := startBackgroundRuntimeForTest(recovered)
	waitForInboxPendingCount(t, recoveredInbox, 0)
	cancelRecovered()
	<-recoveredDone
	finalState, err := recovered.store.readState()
	if err != nil {
		t.Fatal(err)
	}
	pending, pendingErr := recovered.loadPendingGiftAnimations()
	if finalState.Attributes[0].Value != 1 || len(finalState.GiftReceipts) != 1 || len(pending) != 0 || pendingErr != nil {
		t.Fatalf("private delete completion state=%#v pending=%#v err=%v", finalState, pending, pendingErr)
	}
}

func TestBackgroundRuntimeReportsIngestionFailureWithoutDisconnectAndClearsAfterRetry(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	center := newNotificationCenter()
	notifications := make(chan desktopNotification, 4)
	center.AttachSink(func(notification desktopNotification) { notifications <- notification })
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return &stableConnectedSource{} }, center)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	runtime.inboxRetryDelay = 10 * time.Millisecond
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		cancel()
		<-done
	}()
	waitForConnectionState(t, runtime, "connected")
	for len(notifications) > 0 {
		<-notifications
	}
	eventsLogWrites := 0
	retryStarted := make(chan struct{})
	allowRetry := make(chan struct{})
	defer func() {
		select {
		case <-allowRetry:
		default:
			close(allowRetry)
		}
	}()
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "events.log" {
			eventsLogWrites++
			switch eventsLogWrites {
			case 1:
				return errors.New("injected ingestion health failure")
			case 2:
				close(retryStarted)
				<-allowRetry
			}
		}
		return writeFileAtomically(path, data)
	}
	runtime.acceptGift(context.Background(), "room-a", "SEND_GIFT", giftEvent{GiftID: 1, GiftName: "礼物", Num: 1, Rnd: "health-retry"})
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("ingestion retry did not reach the second events.log write")
	}
	runtime.mu.RLock()
	status := runtime.status
	runtime.mu.RUnlock()
	if status.State != "connected" || status.LastError != "" {
		t.Fatalf("connection status changed by ingestion failure: %#v", status)
	}
	if status.IngestionError != "injected ingestion health failure" || status.IngestionErrorKind != "transaction" {
		t.Fatalf("ingestion failure status before retry completion = %#v", status)
	}
	select {
	case notification := <-notifications:
		t.Fatalf("ingestion failure published connection notification: %#v", notification)
	default:
	}
	if health := inbox.Health(); health.PendingCount != 1 {
		t.Fatalf("pending count before retry completion = %d, want 1", health.PendingCount)
	}
	close(allowRetry)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status = runtime.Status()
		if inbox.Health().PendingCount == 0 && status.IngestionError == "" && status.IngestionErrorKind == "" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime status after retry = %#v, inbox health = %#v", status, inbox.Health())
}

type interleavedHealthInbox struct {
	mu          sync.Mutex
	record      giftInboxRecord
	claimed     bool
	ackStarted  chan struct{}
	allowAck    chan struct{}
	acceptError error
}

func (inbox *interleavedHealthInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, inbox.acceptError
}
func (inbox *interleavedHealthInbox) Next() (giftInboxRecord, bool, error) {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	if inbox.claimed {
		return giftInboxRecord{}, false, nil
	}
	inbox.claimed = true
	return inbox.record, true, nil
}
func (inbox *interleavedHealthInbox) Acknowledge(string) error {
	close(inbox.ackStarted)
	<-inbox.allowAck
	return nil
}
func (*interleavedHealthInbox) Release(string) error    { return nil }
func (*interleavedHealthInbox) Close() error            { return nil }
func (*interleavedHealthInbox) Health() giftInboxHealth { return giftInboxHealth{} }

func TestBackgroundRuntimeSuccessfulClaimDoesNotClearNewerIngressFailure(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	inbox := &interleavedHealthInbox{
		record: giftInboxRecord{
			IngestionID: strings.Repeat("1", 32), Command: "SEND_GIFT",
			Gift: giftEvent{GiftID: 1, GiftName: "older success", Num: 1, Rnd: "older-success"},
		},
		ackStarted: make(chan struct{}), allowAck: make(chan struct{}),
		acceptError: errors.New("newer accept failure"),
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	runtime.recordIngestionFailure(errors.New("older processing failure"))
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-inbox.ackStarted:
	case <-time.After(time.Second):
		t.Fatal("older successful claim did not reach acknowledgement")
	}
	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{GiftID: 2})
	close(inbox.allowAck)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.Status().IngestionError == "" {
			t.Fatal("older claim success cleared the newer accept failure")
		}
		if runtime.Status().IngestionError == "newer accept failure" {
			time.Sleep(20 * time.Millisecond)
			if runtime.Status().IngestionError != "newer accept failure" {
				t.Fatal("newer accept failure was cleared after older claim completion")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime status = %#v, want newer accept failure", runtime.Status())
}

type shutdownOrderingInbox struct {
	mu                 sync.Mutex
	closed             bool
	acceptedAfterClose bool
}

func (inbox *shutdownOrderingInbox) Accept(roomID, command string, gift giftEvent) (giftInboxRecord, error) {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	if inbox.closed {
		inbox.acceptedAfterClose = true
		return giftInboxRecord{}, errGiftInboxClosed
	}
	return giftInboxRecord{IngestionID: strings.Repeat("a", 32), RoomID: roomID, Command: command, Gift: gift}, nil
}
func (*shutdownOrderingInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (*shutdownOrderingInbox) Acknowledge(string) error { return nil }
func (*shutdownOrderingInbox) Release(string) error     { return nil }
func (inbox *shutdownOrderingInbox) Close() error {
	inbox.mu.Lock()
	inbox.closed = true
	inbox.mu.Unlock()
	return nil
}
func (*shutdownOrderingInbox) Health() giftInboxHealth { return giftInboxHealth{} }

type callbackDuringShutdownSource struct {
	started chan struct{}
	done    chan struct{}
}

type stableConnectedSource struct{}

type reloadGapSource struct {
	attempts atomic.Int32
}

type roomChangeJoiningSource struct {
	started chan string
	exited  chan string
}

func (source *roomChangeJoiningSource) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	callbacks.onState("connected")
	source.started <- roomID
	<-ctx.Done()
	source.exited <- roomID
	return ctx.Err()
}

func (source *reloadGapSource) Run(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
	attempt := source.attempts.Add(1)
	callbacks.onState("connected")
	if attempt == 1 {
		return newConnectionFailure("read", errors.New("first connection lost"))
	}
	<-ctx.Done()
	return ctx.Err()
}

type signalingStableSource struct {
	started chan struct{}
}

func (source *signalingStableSource) Run(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
	close(source.started)
	callbacks.onState("connected")
	<-ctx.Done()
	return ctx.Err()
}

type inspectionGiftSource struct {
	inspect   func() error
	inspected chan error
}

func (source *inspectionGiftSource) Run(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
	source.inspected <- source.inspect()
	callbacks.onState("connected")
	<-ctx.Done()
	return ctx.Err()
}

func (*stableConnectedSource) Run(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
	callbacks.onState("connected")
	<-ctx.Done()
	return ctx.Err()
}

func TestBackgroundRuntimePreservesConnectionGapsAcrossSameRoomReload(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	source := &reloadGapSource{}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return source })
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		cancel()
		<-done
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		gaps := runtime.Status().ConnectionGaps
		if len(gaps) == 1 && gaps[0].EndedAt != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if gaps := runtime.Status().ConnectionGaps; len(gaps) != 1 || gaps[0].EndedAt == 0 {
		t.Fatalf("initial completed gap = %#v", gaps)
	}
	runtime.NotifyConfigChanged()
	for time.Now().Before(deadline) {
		if source.attempts.Load() >= 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if source.attempts.Load() < 3 {
		t.Fatal("same-room reload did not replace the old supervisor")
	}
	if gaps := runtime.Status().ConnectionGaps; len(gaps) != 1 || gaps[0].EndedAt == 0 {
		t.Fatalf("same-room reload erased gap history: %#v", gaps)
	}
}

func TestBackgroundRuntimePublishesSafeConnectionFailureOnly(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, func() giftEventSource {
		return giftEventSourceFunc(func(context.Context, string, runtimeCallbacks) error {
			return newConnectionFailure("read", errors.New("https://user:secret@example.test/path?token=private response body"))
		})
	})
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		cancel()
		<-done
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := runtime.Status()
		if status.State == "reconnecting" && status.LastError != "" {
			if strings.Contains(status.LastError, "secret") || strings.Contains(status.LastError, "https://") || strings.Contains(status.LastError, "private") {
				t.Fatalf("unsafe API-shaped runtime status: %#v", status)
			}
			if status.LastError != "connection read failure" {
				t.Fatalf("last error = %q, want safe read category", status.LastError)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime did not publish a reconnecting status: %#v", runtime.Status())
}

func TestBackgroundRuntimeRoomChangeCancelsAndJoinsOldSupervisor(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	source := &roomChangeJoiningSource{started: make(chan string, 2), exited: make(chan string, 2)}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return source })
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		cancel()
		<-done
	}()
	if room := <-source.started; room != "room-a" {
		t.Fatalf("initial room = %q, want room-a", room)
	}
	if _, err := store.updateState(func(state *appState) error {
		state.RoomID = "room-b"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime.NotifyConfigChanged()
	select {
	case room := <-source.exited:
		if room != "room-a" {
			t.Fatalf("cancelled room = %q, want room-a", room)
		}
	case <-time.After(time.Second):
		t.Fatal("room change did not join old source")
	}
	select {
	case room := <-source.started:
		if room != "room-b" {
			t.Fatalf("replacement room = %q, want room-b", room)
		}
	case <-time.After(time.Second):
		t.Fatal("room change did not start target source")
	}
}

func TestBackgroundRuntimeClearsOwnedGapsWhenPreparationFailsThenRetriesNewRoom(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-b"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.setConnectionGapRoom("room-a")
	runtime.setConnectionGaps([]connectionGap{{StartedAt: 1, ErrorKind: "read"}})
	runtime.setStatus("error", "room-b", errors.New("first preparation failed"))
	failed := false
	runtime.animationWriteAtomically = func(path string, data []byte) error {
		if !failed {
			failed = true
			return errors.New("injected preparation failure")
		}
		return writeFileAtomically(path, data)
	}
	runtime.resetConnectionGapsForRoom("room-b")
	if err := runtime.prepareRoomConnection("room-b"); err == nil {
		t.Fatal("first room preparation unexpectedly succeeded")
	}
	if gaps := runtime.Status().ConnectionGaps; len(gaps) != 0 {
		t.Fatalf("old-room gaps survived failed target preparation: %#v", gaps)
	}
	runtime.resetConnectionGapsForRoom("room-b")
	if err := runtime.prepareRoomConnection("room-b"); err != nil {
		t.Fatal(err)
	}
	runtime.setConnectionGapRoom("room-b")
	if owner := runtime.connectionGapRoom(); owner != "room-b" {
		t.Fatalf("gap owner after successful retry = %q, want room-b", owner)
	}
}

func (source *callbackDuringShutdownSource) Run(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
	close(source.started)
	<-ctx.Done()
	callbacks.onGift(giftEvent{GiftID: 1, GiftName: "shutdown gift", Num: 1})
	close(source.done)
	return ctx.Err()
}

func TestBackgroundRuntimeJoinsSourceBeforeClosingInbox(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox := &shutdownOrderingInbox{}
	source := &callbackDuringShutdownSource{started: make(chan struct{}), done: make(chan struct{})}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return source })
	runtime.installInbox(inbox, runtime.snapshotInboxHealth(inbox))
	cancel, done := startBackgroundRuntimeForTest(runtime)
	<-source.started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not join the source during shutdown")
	}
	inbox.mu.Lock()
	acceptedAfterClose := inbox.acceptedAfterClose
	closed := inbox.closed
	inbox.mu.Unlock()
	if !closed || acceptedAfterClose {
		t.Fatalf("shutdown inbox closed=%v acceptedAfterClose=%v", closed, acceptedAfterClose)
	}
	select {
	case <-source.done:
	default:
		t.Fatal("runtime returned before source callback completed")
	}
}

func testContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func waitForIngestionError(t *testing.T, runtime *backgroundRuntime, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(runtime.Status().IngestionError, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime status = %#v, want ingestion error containing %q", runtime.Status(), want)
}

func waitForConnectionState(t *testing.T, runtime *backgroundRuntime, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.Status().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime status = %#v, want state %q", runtime.Status(), want)
}

func leftPadThree(value int) string {
	return fmt.Sprintf("%03d", value)
}

func startBackgroundRuntimeForTest(runtime *backgroundRuntime) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.Run(ctx)
	}()
	return cancel, done
}

func waitForInboxPendingCount(t *testing.T, inbox runtimeGiftInbox, want int) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if inbox.Health().PendingCount == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending count = %d, want %d", inbox.Health().PendingCount, want)
}

func assertRuntimeAttributeValue(t *testing.T, store *configStore, name string, want float64) {
	t.Helper()
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	attribute := state.findAttribute(name)
	if attribute == nil || attribute.Value != want {
		t.Fatalf("attribute %q = %#v, want %v", name, attribute, want)
	}
}

func (f *fakeGiftSource) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	callbacks.onState("connected")
	f.started <- roomID
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case gift := <-f.events:
			callbacks.onGift(gift)
		}
	}
}

func TestBackgroundRuntimeProcessesGiftWithoutDisplayPage(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "31567150"
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 33300, AttributeName: "加班时间", FormulaName: "每个加一分钟", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	source := &fakeGiftSource{started: make(chan string, 1), events: make(chan giftEvent, 1)}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return source })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)

	select {
	case roomID := <-source.started:
		if roomID != "31567150" {
			t.Fatalf("connected room = %s", roomID)
		}
	case <-time.After(time.Second):
		t.Fatal("background runtime did not connect")
	}

	source.events <- giftEvent{GiftID: 33012, GiftName: "666", Num: 1, Price: 1000, CoinType: "gold", Timestamp: 1700000000, Rnd: "gift-1"}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.readState()
		if err == nil && len(updated.Attributes) == 1 && updated.Attributes[0].Value == 60 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("gift did not update the attribute in the disk configuration")
}

func TestBackgroundRuntimePersistsBlindBoxMappingsFromRoomCatalog(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.mergeBlindBoxGiftCatalog([]roomGiftInfo{
		{ID: 35800, Name: "小熊虫盲盒", Price: 9000, CoinType: "gold", BlindBoxParent: true},
		{ID: 35801, Name: "心事虫虫", Price: 12000, CoinType: "gold", BlindBoxParentID: 35800, BlindBoxParentName: "小熊虫盲盒", BlindBoxParentPrice: 9000},
		{ID: 31164, Name: "粉丝团灯牌", Price: 100, CoinType: "gold"},
	})

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.GiftCatalog) != 2 {
		t.Fatalf("blind box catalog = %#v", state.GiftCatalog)
	}
	child := state.findGift(35801)
	if child == nil || child.BlindBoxParentID != 35800 || child.BlindBoxParentPrice != 9000 {
		t.Fatalf("blind box child mapping = %#v", child)
	}
}

func TestBackgroundRuntimeAttachesGuardAnimationInEitherEventOrder(t *testing.T) {
	for _, animationFirst := range []bool{false, true} {
		name := "purchase-first"
		if animationFirst {
			name = "animation-first"
		}
		t.Run(name, func(t *testing.T) {
			store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
			state := defaultAppState()
			state.RoomID = "31567150"
			if err := store.replaceState(state); err != nil {
				t.Fatal(err)
			}
			runtime := newBackgroundRuntime(store, nil)
			sequence := 0
			process := func(gift giftEvent) {
				t.Helper()
				sequence++
				record := giftInboxRecord{
					IngestionID: fmt.Sprintf("%032x", sequence), RoomID: state.RoomID, Command: "GUARD_BUY", Gift: gift,
				}
				if err := runtime.processInboxRecord(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			purchase := giftEvent{
				GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 1,
				UID: 42, Uname: "舰长观众", Avatar: "https://example.test/avatar.png",
				Timestamp: 1700000000, Rnd: "guard-order",
			}
			animation := giftEvent{
				GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 1700000001,
				Membership: "captain", EffectID: 9001,
				EffectMP4: "https://i0.hdslb.com/guard.mp4", EffectMP4JSON: "https://i0.hdslb.com/guard.json",
				AnimationOnly: true,
			}
			if animationFirst {
				process(animation)
				process(purchase)
			} else {
				process(purchase)
				process(animation)
			}
			updated, err := store.readState()
			if err != nil {
				t.Fatal(err)
			}
			if len(updated.GiftReceipts) != 1 || updated.GiftReceipts[0].Animation == nil || updated.GiftReceipts[0].Animation.EffectID != 9001 {
				t.Fatalf("guard receipt = %#v", updated.GiftReceipts)
			}
		})
	}
}

func TestSameGiftIdentityKeepsMatchingAfterIconRevision(t *testing.T) {
	configured := giftInfo{ID: 970001, Name: "情书", Price: 5200, CoinType: "gold", ImgBasic: "old.png"}
	event := giftEvent{GiftID: 970002, GiftName: "情书", Price: 5200, CoinType: "gold", ImgBasic: "current.png"}
	if !sameGiftIdentity(configured, event) {
		t.Fatal("icon-only gift revision should remain a runtime alias")
	}
}

func TestApplyGiftEventAggregatesBatchForAttributeBroadcast(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 33300, AttributeName: "加班时间", FormulaName: "每个加一分钟", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 33300, GiftName: "666", Num: 3, Price: 1000, CoinType: "gold",
		Uname: "昵称很长的观众", Avatar: "https://example.test/avatar.png", UID: 123456789,
		Timestamp: 1700000000, Rnd: "gift-batch-1",
	})

	if state.Attributes[0].Value != 180 {
		t.Fatalf("attribute value = %v", state.Attributes[0].Value)
	}
	if len(state.Log) != 1 {
		t.Fatalf("log entries = %d, want 1: %#v", len(state.Log), state.Log)
	}
	entry := state.Log[0]
	if entry.Num != 3 || entry.Delta != 180 || entry.ValueAfter != 180 {
		t.Fatalf("aggregated log = %#v", entry)
	}
	if entry.TriggerName != "每个加一分钟" {
		t.Fatalf("trigger name = %q", entry.TriggerName)
	}
	if entry.Avatar != "https://example.test/avatar.png" || entry.SenderUID != 123456789 || entry.EventID == "" || entry.Source != "gift" {
		t.Fatalf("broadcast metadata = %#v", entry)
	}
}

func TestApplyGiftEventUpdatesGiftKPIWithoutRules(t *testing.T) {
	state := defaultAppState()
	state.GiftKPIPanels = []giftKPIPanelState{{
		ID: "kpi-1", Name: "本场礼物目标", Layout: "grid",
		Items:      []giftKPIItemState{{GiftID: 33300, GiftName: "666", Target: 10, BarStyle: "progress"}},
		Appearance: displayAppearanceState{ThemeID: "glass", FontSize: 48, AccentColor: "#fb7299", Align: "center", PanelOpacity: 55},
	}}

	applyGiftEvent(&state, giftEvent{GiftID: 33300, GiftName: "666", Num: 3, Timestamp: 1700000000})

	if got := state.GiftKPIPanels[0].Items[0].Received; got != 3 {
		t.Fatalf("received = %d, want 3", got)
	}
	if len(state.Rules) != 0 || len(state.Attributes) != 0 {
		t.Fatalf("gift KPI should not create rules or attributes: state=%#v", state)
	}
}

func TestApplyGiftEventUpdatesBlindBoxParentAndRewardKPI(t *testing.T) {
	state := defaultAppState()
	state.GiftKPIPanels = []giftKPIPanelState{{
		ID: "kpi-blind-box", Name: "盲盒礼物目标", Layout: "grid",
		Items: []giftKPIItemState{
			{GiftID: 35800, GiftName: "小熊虫盲盒", Target: 10, BarStyle: "progress"},
			{GiftID: 35801, GiftName: "心事虫虫", Target: 10, BarStyle: "progress"},
		},
		Appearance: displayAppearanceState{ThemeID: "glass", FontSize: 48, AccentColor: "#fb7299", Align: "center", PanelOpacity: 55},
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 35801, BlindGiftID: 35800, GiftName: "心事虫虫", BlindGiftName: "小熊虫盲盒",
		Num: 2, Price: 12000, BlindGiftPrice: 9000, CoinType: "gold", Timestamp: 1700000000,
	})

	parentReceived := state.GiftKPIPanels[0].Items[0].Received
	rewardReceived := state.GiftKPIPanels[0].Items[1].Received
	if parentReceived != 2 || rewardReceived != 2 {
		t.Fatalf("blind box KPI received parent=%d reward=%d, want parent=2 reward=2", parentReceived, rewardReceived)
	}
}

func TestApplyGiftEventUpdatesBlindBoxParentKPIFromCatalog(t *testing.T) {
	state := defaultAppState()
	state.GiftCatalog = []giftInfo{
		{ID: 35800, Name: "小熊虫盲盒", Price: 9000, CoinType: "gold"},
		{ID: 35801, Name: "心事虫虫", Price: 12000, CoinType: "gold", BlindBoxParentID: 35800, BlindBoxParentName: "小熊虫盲盒", BlindBoxParentPrice: 9000},
	}
	state.GiftKPIPanels = []giftKPIPanelState{{
		ID: "kpi-blind-box", Name: "盲盒礼物目标", Layout: "grid",
		Items:      []giftKPIItemState{{GiftID: 35800, GiftName: "小熊虫盲盒", Target: 10, BarStyle: "progress"}},
		Appearance: displayAppearanceState{ThemeID: "glass", FontSize: 48, AccentColor: "#fb7299", Align: "center", PanelOpacity: 55},
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 35801, GiftName: "心事虫虫", Num: 3, Price: 12000, CoinType: "gold", Timestamp: 1700000000,
	})

	if got := state.GiftKPIPanels[0].Items[0].Received; got != 3 {
		t.Fatalf("catalog-mapped blind box KPI received = %d, want 3", got)
	}
}

func TestApplyGiftEventUpdatesEveryMatchingAttribute(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{
		{Name: "早播", Value: 0, Unit: "none", Format: "number"},
		{Name: "积分", Value: 10, Unit: "none", Format: "number"},
	}
	state.Rules = []giftRule{
		{ID: "r-early", GiftID: 33300, AttributeName: "早播", Formula: "早播+1"},
		{ID: "r-score", GiftID: 33300, AttributeName: "积分", Formula: "积分+2"},
	}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 33300, GiftName: "666", Num: 1, Price: 1000, CoinType: "gold",
		Timestamp: 1700000000, Rnd: "multi-attribute-gift-1",
	})

	if state.Attributes[0].Value != 1 || state.Attributes[1].Value != 12 {
		t.Fatalf("matching attributes = %#v, want early=1 and score=12", state.Attributes)
	}
	if len(state.Log) != 2 {
		t.Fatalf("log entries = %d, want 2: %#v", len(state.Log), state.Log)
	}
}

func TestApplyGiftEventUsesPaidEventAmountForRulesAndContributions(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{
		ID: "super-chat-time", GiftID: specialGiftSuperChat, AttributeName: "加班时间",
		FormulaName: "每元增加一秒", Formula: "加班时间+price/1000",
	}}
	state.GiftCatalog = []giftInfo{{ID: specialGiftSuperChat, Name: "Super Chat", Price: 30000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: specialGiftSuperChat, GiftName: "Super Chat", Num: 1,
		Price: 50000, TotalCoin: 50000, CoinType: "gold",
		UID: 123, Uname: "醒目留言观众", Timestamp: 1700000400, Rnd: "super-chat:runtime-1",
	})

	if state.Attributes[0].Value != 50 || len(state.Log) != 1 {
		t.Fatalf("super chat rule result: attribute=%#v log=%#v", state.Attributes, state.Log)
	}
	if len(state.Contributions.Viewers) != 1 || state.Contributions.Viewers[0].GoldValue != 50000 {
		t.Fatalf("super chat contribution = %#v", state.Contributions)
	}
}

func TestApplyGiftEventRepeatsGuardRuleForPurchasedQuantity(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "舰长月数", Value: 0, Unit: "none", Format: "number"}}
	state.Rules = []giftRule{{
		ID: "captain-months", GiftID: specialGiftGuardCaptain, AttributeName: "舰长月数",
		Formula: "舰长月数+1",
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 3,
		Price: 198000, TotalCoin: 594000, CoinType: "gold",
		UID: 456, Uname: "大航海观众", Timestamp: 1700000500, Rnd: "guard:runtime-1",
	})

	if state.Attributes[0].Value != 3 || len(state.Log) != 1 || state.Log[0].Num != 3 {
		t.Fatalf("guard rule result: attribute=%#v log=%#v", state.Attributes, state.Log)
	}
}

func TestApplyGiftEventMatchesBlindBoxParentFromEvent(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "blind-rule", GiftID: 35800, AttributeName: "加班时间", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 35800, Name: "小熊虫盲盒", Price: 9000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 35801, BlindGiftID: 35800, GiftName: "心事虫虫", Num: 1, Price: 9000,
		Timestamp: 1700000000, Rnd: "blind-parent-event",
	})

	if state.Attributes[0].Value != 60 || len(state.Log) != 1 {
		t.Fatalf("blind box event did not trigger parent rule: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}
}

func TestApplyGiftEventSkipsDisabledGiftRule(t *testing.T) {
	disabled := false
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 120, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{
		ID: "r1", GiftID: 33300, AttributeName: "加班时间", Formula: "加班时间+60", Enabled: &disabled,
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 33300, GiftName: "666", Num: 1, Price: 1000, CoinType: "gold",
		Timestamp: 1700000000, Rnd: "disabled-gift-rule",
	})

	if state.Attributes[0].Value != 120 {
		t.Fatalf("disabled rule changed attribute value to %v", state.Attributes[0].Value)
	}
	if len(state.Log) != 0 {
		t.Fatalf("disabled rule created log entries: %#v", state.Log)
	}
}

type fakeUserProfileResolver struct {
	profile userProfile
	calls   int
}

func (resolver *fakeUserProfileResolver) Resolve(_ context.Context, _ int64) (userProfile, error) {
	resolver.calls++
	return resolver.profile, nil
}

func TestBackgroundRuntimeEnrichesMaskedSenderFromAnonymousProfile(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "room"
	state.Attributes = []attributeState{{Name: "早播", Value: 0, Unit: "none", Format: "number"}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "早播", Formula: "早播+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	profiles := &fakeUserProfileResolver{profile: userProfile{
		UID: 123456789, Name: "完整昵称", Avatar: "https://example.test/full-avatar.png",
	}}
	runtime := newBackgroundRuntime(store, nil)
	runtime.profileResolver = profiles

	if err := runtime.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: strings.Repeat("c", 32), RoomID: "room", Command: "SEND_GIFT",
		Gift: giftEvent{
			GiftID: 1, GiftName: "人气票", Num: 1, Price: 100, CoinType: "gold",
			UID: 123456789, Uname: "字***", Timestamp: 1700000000, Rnd: "masked-gift-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Log) != 1 {
		t.Fatalf("log = %#v", updated.Log)
	}
	entry := updated.Log[0]
	if entry.SenderUID != 123456789 || entry.Uname != "完整昵称" || entry.Avatar != "https://example.test/full-avatar.png" {
		t.Fatalf("enriched log = %#v", entry)
	}
	if profiles.calls != 1 {
		t.Fatalf("profile resolver calls = %d", profiles.calls)
	}
}

func TestBilibiliUserProfileResolverQueriesAnonymousCardAndCaches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("mid") != "123456789" {
			t.Errorf("mid = %q", r.URL.Query().Get("mid"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"OK","data":{"card":{"mid":"123456789","name":"完整昵称","face":"https://example.test/avatar.png"}}}`))
	}))
	defer server.Close()

	resolver := newBilibiliUserProfileResolver(server.Client(), server.URL)
	for range 2 {
		profile, err := resolver.Resolve(context.Background(), 123456789)
		if err != nil {
			t.Fatal(err)
		}
		if profile.Name != "完整昵称" || profile.Avatar != "https://example.test/avatar.png" {
			t.Fatalf("profile = %#v", profile)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("anonymous profile requests = %d, want 1", requests.Load())
	}
}

func TestBilibiliUserProfileResolverCachesFailures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	resolver := newBilibiliUserProfileResolver(server.Client(), server.URL)
	for range 2 {
		if _, err := resolver.Resolve(context.Background(), 123456789); err == nil {
			t.Fatal("rate-limited profile request unexpectedly succeeded")
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("failed profile requests = %d, want 1", requests.Load())
	}
}

func TestBackgroundRuntimeProcessesTimerWithoutRoomOrDisplayPage(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 120, Unit: "seconds", Format: "hhmmss"}}
	state.TimerRules = []timerRule{{
		ID: "timer-1", AttributeName: "加班时间", FormulaName: "每分钟减少",
		IntervalSeconds: 60, Formula: "MAX(加班时间-60,0)", Enabled: true,
	}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	ticks := make(chan time.Time, 2)
	runtime := newBackgroundRuntime(store, nil)
	runtime.timerTicks = ticks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)

	startedAt := time.Unix(1700000000, 0)
	ticks <- startedAt
	ticks <- startedAt.Add(60 * time.Second)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.readState()
		if err == nil && len(updated.Attributes) == 1 && updated.Attributes[0].Value == 60 {
			if len(updated.Log) == 0 || updated.Log[0].Source != "timer" || updated.Log[0].RuleID != "timer-1" {
				t.Fatalf("timer log = %#v", updated.Log)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timer did not update the attribute while room and display were absent")
}

func TestBackgroundRuntimeFrozenTimerDoesNotCatchUp(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{
		{ID: "attribute-a", Name: "A", Value: 0},
		{ID: "attribute-b", Name: "B", Value: 0},
	}
	state.TimerRules = []timerRule{
		{ID: "timer-a", AttributeName: "A", IntervalSeconds: 60, Formula: "A+1", Enabled: true},
		{ID: "timer-b", AttributeName: "B", IntervalSeconds: 60, Formula: "B+1", Enabled: true},
	}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	freezes := fakeAttributeFreezeChecker{"attribute-a": true}
	runtime := newBackgroundRuntime(store, nil)
	runtime.setAttributeFreezeChecker(freezes)
	startedAt := time.Unix(1700000000, 0)
	runtime.handleTimerTick(startedAt)
	runtime.handleTimerTick(startedAt.Add(60 * time.Second))

	frozen, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if got := frozen.findAttribute("A").Value; got != 0 {
		t.Fatalf("frozen A = %v", got)
	}
	if got := frozen.findAttribute("B").Value; got != 1 {
		t.Fatalf("live B = %v", got)
	}

	delete(freezes, "attribute-a")
	runtime.handleTimerTick(startedAt.Add(90 * time.Second))
	beforeNextDue, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if got := beforeNextDue.findAttribute("A").Value; got != 0 {
		t.Fatalf("thawed A caught up = %v", got)
	}

	runtime.handleTimerTick(startedAt.Add(120 * time.Second))
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.findAttribute("A").Value; got != 1 {
		t.Fatalf("next scheduled A = %v", got)
	}
	if got := updated.findAttribute("B").Value; got != 2 {
		t.Fatalf("live B after second due tick = %v", got)
	}
}

func TestTimerConditionSkipsOnlyTheCurrentOccurrence(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.TimerRules = []timerRule{{
		ID: "timer-1", AttributeName: "加班时间", FormulaName: "大于零时减少",
		IntervalSeconds: 60, Condition: "加班时间>0", Formula: "MAX(加班时间-60,0)", Enabled: true,
	}}

	if applied := applyTimerRules(&state, []string{"timer-1"}, time.Unix(1700000000, 0)); applied != 0 {
		t.Fatalf("condition-false timer applied %d times", applied)
	}
	if state.Attributes[0].Value != 0 || len(state.Log) != 0 {
		t.Fatalf("condition-false timer changed state: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}

	state.Attributes[0].Value = 120
	if applied := applyTimerRules(&state, []string{"timer-1"}, time.Unix(1700000060, 0)); applied != 1 {
		t.Fatalf("condition-true timer applied %d times", applied)
	}
	if state.Attributes[0].Value != 60 || len(state.Log) != 1 || state.Log[0].Source != "timer" {
		t.Fatalf("condition-true timer state: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}
}

func TestTimerScheduleWaitsForAFullFirstInterval(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	state := defaultAppState()
	state.TimerRules = []timerRule{{ID: "timer-1", IntervalSeconds: 60, Enabled: true}}
	startedAt := time.Unix(1700000000, 0)

	if due := runtime.dueTimerRuleIDs(state, startedAt); len(due) != 0 {
		t.Fatalf("new timer was due immediately: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(59*time.Second)); len(due) != 0 {
		t.Fatalf("new timer was due before one full interval: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(60*time.Second)); len(due) != 1 || due[0] != "timer-1" {
		t.Fatalf("timer due after one interval = %v", due)
	}
}

func TestTimerConfigChangeRestartsScheduleFromReenable(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	state := defaultAppState()
	state.TimerRules = []timerRule{{ID: "timer-1", IntervalSeconds: 60, Enabled: true}}
	startedAt := time.Unix(1700000000, 0)

	if due := runtime.dueTimerRuleIDs(state, startedAt); len(due) != 0 {
		t.Fatalf("timer was due immediately: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(30*time.Second)); len(due) != 0 {
		t.Fatalf("timer was due before its original interval: %v", due)
	}

	runtime.NotifyTimerConfigChanged()
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(30*time.Second)); len(due) != 0 {
		t.Fatalf("timer was due immediately after re-enable: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(60*time.Second)); len(due) != 0 {
		t.Fatalf("timer reused its old schedule after re-enable: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(90*time.Second)); len(due) != 1 || due[0] != "timer-1" {
		t.Fatalf("timer due after restarted interval = %v", due)
	}
}
