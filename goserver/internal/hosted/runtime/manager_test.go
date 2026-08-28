package runtime

import (
	"context"
	"errors"
	"reflect"
	goruntime "runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	"bilibili-live-gift-panel/internal/hosted/roomwatcher"
)

// This test fails if a live room keeps the membership captured at the state
// boundary instead of reconciling later full reference snapshots.
func TestManagerReconcilesReferencesWhileRoomIsAlreadyLive(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(1, "42", 7, 8)); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1})); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(3, "42", 8, 9)); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(4, "42", 8, 9)); err != nil {
		t.Fatal(err)
	}
	if sessions.startedCount() != 3 {
		t.Fatalf("started sessions = %d, want accounts 7, 8, then 9", sessions.startedCount())
	}
	ends := sessions.endCommands()
	if len(ends) != 1 || ends[0].AccountID != 7 {
		t.Fatalf("ended sessions = %#v, want only removed account 7", ends)
	}
	for _, accountID := range []int64{8, 9} {
		status, statusErr := manager.Status(context.Background(), accountID)
		if statusErr != nil || status.State != StateActive || status.RoomID != "42" {
			t.Fatalf("account %d status = %#v, %v", accountID, status, statusErr)
		}
	}
}

// This test fails if disable cleanup leaves historical room membership that a
// later grace/offline state event attempts to clean a second time.
func TestDisabledAccountIsNotRevisitedByLaterGraceOrOfflineEvents(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log, logOwnership: true}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(1, "42", 7)); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1})); err != nil {
		t.Fatal(err)
	}
	manager.AccountDisabled(7)
	account, accountErr := manager.accountExisting(7)
	if accountErr != nil {
		t.Fatal(accountErr)
	}
	var disabledDone <-chan struct{}
	for disabledDone == nil {
		account.mu.Lock()
		disabledDone = account.closeDone
		account.mu.Unlock()
		goruntime.Gosched()
	}
	<-disabledDone
	graceUntil := time.Unix(800, 0)
	if err := manager.ApplyRoomEvent(context.Background(), stateEvent(3, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateGrace, ConfirmedAt: time.Unix(200, 0), GraceUntil: &graceUntil, LeaseEpoch: 2})); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(4, "42", 8)); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), stateEvent(5, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateGrace, To: roomwatcher.StateOffline, ConfirmedAt: graceUntil, LeaseEpoch: 3})); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(6, "42", 8, 9)); err != nil {
		t.Fatal(err)
	}
	if sessions.startedCount() != 1 {
		t.Fatalf("grace/offline reference additions started %d sessions, want the original one only", sessions.startedCount())
	}
	if got := len(sessions.releaseFences()); got != 1 {
		t.Fatalf("ownership release calls = %d, want only disable cleanup", got)
	}
}

func TestManagerSwitchesAlreadyLiveRoomsAfterOldCleanup(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, broadcasts: map[string]int64{"42": 99, "84": 100}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42", "84": "84"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	for _, event := range []roomwatcher.Event{
		referencesEvent(1, "42", 7),
		stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1}),
		stateEvent(3, roomwatcher.Transition{RoomID: "84", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(101, 0), NewBroadcast: true, LeaseEpoch: 1}),
		referencesEvent(4, "84", 7),
		referencesEvent(5, "42"),
	} {
		if err := manager.ApplyRoomEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if got := log.snapshot(); !containsOrderedOperations(got, []string{"start:42", "end:1", "start:84"}) {
		t.Fatalf("live-room switch order = %v", got)
	}
	status, statusErr := manager.Status(context.Background(), 7)
	if statusErr != nil || status.RoomID != "84" || status.SessionID != 2 {
		t.Fatalf("switched status = %#v, %v", status, statusErr)
	}
}

func TestManagerRejectsRoomEventWithoutExactlyOnePayload(t *testing.T) {
	manager, err := NewManager(Dependencies{Sessions: &orderedSessions{enabled: true, log: &operationLog{}}, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(&operationLog{}, nil)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	transition := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1}
	references := roomwatcher.RoomReferencesChanged{RoomID: "42", AccountIDs: []int64{7}}
	for _, event := range []roomwatcher.Event{
		{Sequence: 1},
		{Sequence: 1, RoomStateChanged: &transition, RoomReferencesChanged: &references},
		{Sequence: 1, RoomReferencesChanged: &roomwatcher.RoomReferencesChanged{RoomID: "42", AccountIDs: []int64{7, 7}}},
		{Sequence: 1, RoomReferencesChanged: &roomwatcher.RoomReferencesChanged{RoomID: "42", AccountIDs: []int64{8, 7}}},
	} {
		if err := manager.ApplyRoomEvent(context.Background(), event); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ApplyRoomEvent(%#v) = %v, want invalid input", event, err)
		}
	}
}

// This test fails if ApplyRoomEvent waits on an uninterruptible transition
// lock after its caller's final-drain budget has expired.
func TestApplyRoomEventDeadlineInterruptsTransitionGateWait(t *testing.T) {
	log := &operationLog{}
	base := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, broadcasts: map[string]int64{"42": 99}}
	sessions := &blockingBroadcastSessions{transitionSessions: base, started: make(chan struct{}), release: make(chan struct{})}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(1, "42", 7)); err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- manager.ApplyRoomEvent(context.Background(), stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1}))
	}()
	select {
	case <-sessions.started:
	case <-time.After(time.Second):
		t.Fatal("first room event did not hold the transition gate")
	}

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 20*time.Millisecond)
	secondResult := make(chan error, 1)
	go func() { secondResult <- manager.ApplyRoomEvent(deadline, referencesEvent(3, "42", 7, 8)) }()
	select {
	case err := <-secondResult:
		cancelDeadline()
		if !errors.Is(err, context.DeadlineExceeded) {
			close(sessions.release)
			t.Fatalf("second ApplyRoomEvent error = %v, want deadline", err)
		}
	case <-time.After(250 * time.Millisecond):
		cancelDeadline()
		close(sessions.release)
		<-firstResult
		t.Fatal("ApplyRoomEvent ignored deadline while waiting for transition gate")
	}
	close(sessions.release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := manager.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

// This test fails if a lagged consumer replays a historical live event after
// restart instead of accepting the current offline projection and cursor.
func TestManagerBootstrapOfflineSkipsHistoricalLiveReplay(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	graceUntil := time.Unix(800, 0)
	bootstrap := roomwatcher.Bootstrap{Cursor: 10, Rooms: []roomwatcher.BootstrapRoom{
		{RoomID: "42", State: roomwatcher.StateOffline, LeaseEpoch: 3, AccountIDs: []int64{7}},
		{RoomID: "84", State: roomwatcher.StateGrace, BroadcastSessionID: 100, GraceUntil: &graceUntil, LeaseEpoch: 4, AccountIDs: []int64{8}},
	}}
	if err := manager.BootstrapRoomProjection(context.Background(), bootstrap); err != nil {
		t.Fatal(err)
	}
	historicalLive := stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1})
	if err := manager.ApplyRoomEvent(context.Background(), historicalLive); err != nil {
		t.Fatal(err)
	}
	if sessions.startedCount() != 0 {
		t.Fatalf("historical live replay started %d sessions", sessions.startedCount())
	}
}

// This test fails if restart binds executions to an old historical broadcast
// or if post-bootstrap reference events ignore the current projection.
func TestManagerBootstrapLiveUsesCurrentBroadcastThenAppliesIncrementalReplay(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, broadcasts: map[string]int64{"42": 999}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	source := &bootstrapReplaySource{
		bootstrap: roomwatcher.Bootstrap{Cursor: 20, Rooms: []roomwatcher.BootstrapRoom{{RoomID: "42", State: roomwatcher.StateLive, BroadcastSessionID: 200, LeaseEpoch: 9, AccountIDs: []int64{7}}}},
		events:    []roomwatcher.Event{referencesEvent(21, "42", 7, 8)},
	}
	cursor, replayErr := manager.BootstrapAndReplayRoomEvents(context.Background(), source, 10)
	if replayErr != nil {
		t.Fatal(replayErr)
	}
	if cursor != 21 || !slices.Equal(source.replayAfter, []uint64{20}) {
		t.Fatalf("bootstrap replay cursor/after = %d/%v, want 21/[20]", cursor, source.replayAfter)
	}
	if manager.lastSequence != 21 {
		t.Fatalf("manager sequence = %d, want 21", manager.lastSequence)
	}
	for _, accountID := range []int64{7, 8} {
		account, accountErr := manager.accountExisting(accountID)
		if accountErr != nil {
			t.Fatal(accountErr)
		}
		account.mu.Lock()
		active := account.current
		account.mu.Unlock()
		if active == nil || active.session.BroadcastSessionID != 200 {
			t.Fatalf("account %d bootstrap session = %#v, want broadcast 200", accountID, active)
		}
	}
	if sessions.startedCount() != 2 {
		t.Fatalf("bootstrap plus incremental starts = %d, want 2", sessions.startedCount())
	}
}

type bootstrapReplaySource struct {
	bootstrap   roomwatcher.Bootstrap
	events      []roomwatcher.Event
	replayAfter []uint64
}

func (source *bootstrapReplaySource) LoadBootstrap(context.Context) (roomwatcher.Bootstrap, error) {
	return source.bootstrap, nil
}

func (source *bootstrapReplaySource) ReplayEvents(_ context.Context, after uint64, limit int) ([]roomwatcher.Event, error) {
	source.replayAfter = append(source.replayAfter, after)
	result := make([]roomwatcher.Event, 0, min(limit, len(source.events)))
	for _, event := range source.events {
		if event.Sequence > after {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

// This test fails if an old F1 stale-cleanup forget runs after F2 started and
// removes F2 from the room projection without matching its exact fence.
func TestStaleCleanupForgetDoesNotRemoveNewFenceFromActiveRoom(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, broadcasts: map[string]int64{"42": 99, "84": 100}}
	forgetStarted := make(chan struct{})
	allowForget := make(chan struct{})
	forgetDone := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42", "84": "84"})}, Options{
		beforeForgetLostOwner: func() { close(forgetStarted); <-allowForget },
		afterForgetLostOwner:  func() { close(forgetDone) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	for _, event := range []roomwatcher.Event{
		referencesEvent(1, "42", 7),
		stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1}),
		stateEvent(3, roomwatcher.Transition{RoomID: "84", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(101, 0), NewBroadcast: true, LeaseEpoch: 1}),
	} {
		if err := manager.ApplyRoomEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	account, accountErr := manager.accountExisting(7)
	if accountErr != nil {
		t.Fatal(accountErr)
	}
	account.mu.Lock()
	first := account.current
	account.mu.Unlock()
	manager.beginStaleCleanup(account, first, first.owner, OwnerFence{})
	<-forgetStarted
	sessions.mu.Lock()
	sessions.owner = OwnerFence{}
	sessions.mu.Unlock()
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(4, "84", 7)); err != nil {
		t.Fatal(err)
	}
	account.mu.Lock()
	second := account.current
	account.mu.Unlock()
	if second == nil || second.owner == first.owner {
		t.Fatalf("replacement session = %#v, want new F2", second)
	}
	close(allowForget)
	<-forgetDone
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(5, "84")); err != nil {
		t.Fatal(err)
	}
	ends := sessions.endCommands()
	if len(ends) != 1 || ends[0].SessionID != second.session.ID || ends[0].Owner != second.owner {
		t.Fatalf("reference removal ends = %#v, want exact F2 session/fence", ends)
	}
}

func referencesEvent(sequence uint64, roomID string, accountIDs ...int64) roomwatcher.Event {
	return roomwatcher.Event{Sequence: sequence, RoomReferencesChanged: &roomwatcher.RoomReferencesChanged{RoomID: roomID, AccountIDs: accountIDs}}
}

func stateEvent(sequence uint64, transition roomwatcher.Transition) roomwatcher.Event {
	return roomwatcher.Event{Sequence: sequence, RoomStateChanged: &transition}
}

func TestManagerAppliesRoomTransitionsAcrossGraceAndOffline(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0).UTC(), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatalf("ApplyRoomTransition(live) = %v", err)
	}
	if sessions.startedCount() != 1 || sources.maximumActive() != 1 {
		t.Fatalf("live starts/sources = %d/%d, want 1/1", sessions.startedCount(), sources.maximumActive())
	}

	graceUntil := live.ConfirmedAt.Add(10 * time.Minute)
	grace := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateGrace, ConfirmedAt: live.ConfirmedAt.Add(time.Minute), GraceUntil: &graceUntil, Sequence: 2, LeaseEpoch: 2}
	if err := manager.ApplyRoomTransition(context.Background(), grace); err != nil {
		t.Fatalf("ApplyRoomTransition(grace) = %v", err)
	}
	if sessions.startedCount() != 1 || sources.maximumActive() != 1 {
		t.Fatalf("grace starts/sources = %d/%d, want 1/1", sessions.startedCount(), sources.maximumActive())
	}

	offline := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateGrace, To: roomwatcher.StateOffline, ConfirmedAt: graceUntil, Sequence: 3, LeaseEpoch: 3}
	if err := manager.ApplyRoomTransition(context.Background(), offline); err != nil {
		t.Fatalf("ApplyRoomTransition(offline) = %v", err)
	}
	if !containsOperation(log.snapshot(), "end:1") {
		t.Fatalf("offline did not end the execution session: %v", log.snapshot())
	}
}

func TestGraceDropsNewGiftsAndRecoveredLiveRestoresAdmission(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	processed := make(chan string, 2)
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{Process: func(_ context.Context, _ OwnerFence, _ Session, event roomsource.Event) error {
		processed <- event.ID
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	graceUntil := time.Unix(800, 0)
	grace := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateGrace, ConfirmedAt: time.Unix(200, 0), GraceUntil: &graceUntil, Sequence: 2, LeaseEpoch: 2}
	recovered := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateGrace, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(300, 0), Sequence: 3, LeaseEpoch: 3}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	subscription := sources.subscription(t, 0)
	subscription.Emit(roomsource.Event{ID: "live-before-grace"})
	if got := receiveRuntimeSignal(t, processed, "live gift"); got != "live-before-grace" {
		t.Fatalf("live processing = %q", got)
	}
	if err := manager.ApplyRoomTransition(context.Background(), grace); err != nil {
		t.Fatal(err)
	}
	subscription.Emit(roomsource.Event{ID: "grace-gift"})
	select {
	case got := <-processed:
		t.Fatalf("grace processed gift %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err := manager.ApplyRoomTransition(context.Background(), recovered); err != nil {
		t.Fatal(err)
	}
	subscription.Emit(roomsource.Event{ID: "recovered-live-gift"})
	if got := receiveRuntimeSignal(t, processed, "recovered live gift"); got != "recovered-live-gift" {
		t.Fatalf("recovered processing = %q", got)
	}
}

func TestManagerSetRoomPersistsSelectionWithoutReplacingLiveExecution(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42", "84": "84"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.ApplyRoomTransition(context.Background(), roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0).UTC(), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetRoom(context.Background(), 7, "84"); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.RoomID != "42" || status.SessionID == 0 {
		t.Fatalf("selection replaced live execution: status=%#v err=%v", status, err)
	}
}

// This test fails if SetRoom overwrites the execution's captured F1 fence with
// a newer F2 claim before detecting that the active session is stale.
func TestSetRoomFenceMismatchDoesNotOverwriteActiveOwner(t *testing.T) {
	log := &operationLog{}
	ends := &fenceMismatchSessions{
		transitionSessions: transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, broadcasts: map[string]int64{"42": 99}},
		endStarted:         make(chan struct{}),
		allowEnd:           make(chan struct{}),
	}
	defer close(ends.allowEnd)
	manager, err := NewManager(Dependencies{Sessions: ends, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42", "84": "84"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(1, "42", 7)); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 1})); err != nil {
		t.Fatal(err)
	}
	account, accountErr := manager.accountExisting(7)
	if accountErr != nil {
		t.Fatal(accountErr)
	}
	account.mu.Lock()
	fenceOne := account.owner
	account.mu.Unlock()
	ends.mu.Lock()
	ends.owner = OwnerFence{AccountID: 7, Token: fenceOne.Token, Epoch: fenceOne.Epoch + 1}
	ends.mu.Unlock()
	if err := manager.SetRoom(context.Background(), 7, "84"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SetRoom fence mismatch = %v, want unavailable", err)
	}
	select {
	case <-ends.endStarted:
	case <-time.After(time.Second):
		t.Fatal("stale active cleanup did not start")
	}
	account.mu.Lock()
	gotOwner := account.owner
	account.mu.Unlock()
	if gotOwner != fenceOne {
		t.Fatalf("account owner = %#v, want captured F1 %#v while F2 cleanup is pending", gotOwner, fenceOne)
	}
}

func TestManagerRoomEventRejectsStaleEpochAndIgnoresDuplicateSequence(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.ApplyRoomEvent(context.Background(), referencesEvent(1, "42", 7)); err != nil {
		t.Fatal(err)
	}
	live := stateEvent(2, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, LeaseEpoch: 3})
	if err := manager.ApplyRoomEvent(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomEvent(context.Background(), live); err != nil {
		t.Fatalf("duplicate event = %v", err)
	}
	graceUntil := time.Unix(800, 0)
	staleEpoch := stateEvent(3, roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateGrace, ConfirmedAt: time.Unix(200, 0), GraceUntil: &graceUntil, LeaseEpoch: 3})
	if err := manager.ApplyRoomEvent(context.Background(), staleEpoch); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale epoch = %v, want unavailable", err)
	}
	if sessions.startedCount() != 1 {
		t.Fatalf("stale/duplicate transition started %d sessions, want one", sessions.startedCount())
	}
}

func TestOfflineTransitionCleansAccountsStartedBeforeLaterLiveFailure(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7, 8}}, broadcasts: map[string]int64{"42": 99}, startFailures: map[int64]int{8: 1}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	offline := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateOffline, ConfirmedAt: time.Unix(101, 0), Sequence: 2, LeaseEpoch: 2}
	if err := manager.ApplyRoomTransition(context.Background(), live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("live error = %v, want unavailable", err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); err != nil {
		t.Fatalf("offline cleanup = %v", err)
	}
	if !containsOperation(log.snapshot(), "end:1") {
		t.Fatalf("offline did not end the successful account: %v", log.snapshot())
	}
}

func TestOfflineTransitionRetriesReleaseAfterExecutionEnded(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, releaseFailures: 1, logOwnership: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	offline := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateOffline, ConfirmedAt: time.Unix(101, 0), Sequence: 2, LeaseEpoch: 2}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first offline = %v, want unavailable", err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); err != nil {
		t.Fatalf("offline release retry = %v", err)
	}
	if got := log.snapshot(); !containsOrderedOperations(got, []string{"end:1", "release"}) {
		t.Fatalf("end/release order = %v", got)
	}
}

func TestOfflineTransitionRetriesClaimedOwnerAfterStartFailure(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, releaseFailures: 2, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}, startFailures: map[int64]int{7: 1}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	offline := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateOffline, ConfirmedAt: time.Unix(101, 0), Sequence: 2, LeaseEpoch: 2}
	if err := manager.ApplyRoomTransition(context.Background(), live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("live start failure = %v, want unavailable", err)
	}
	wantFence := sessions.currentOwner()
	if !validOwnerFence(wantFence) {
		t.Fatal("failed start released ownership despite configured release failure")
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first offline release = %v, want unavailable", err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); err != nil {
		t.Fatalf("offline release retry = %v", err)
	}
	for _, fence := range sessions.releaseFences() {
		if fence != wantFence {
			t.Fatalf("release fence = %#v, want %#v", fence, wantFence)
		}
	}
	if len(sessions.releaseFences()) != 3 {
		t.Fatalf("release attempts = %d, want 3", len(sessions.releaseFences()))
	}
}

func TestTransitionProcessorCreationEndFailureRetainsExactSessionAndFenceForOfflineRetry(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, endFailures: 1, logOwnership: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{ProcessorFactory: processorFactoryFunc(func(context.Context, OwnerFence, Session) (SessionProcessor, error) {
		log.add("processor-create-failed")
		return nil, ErrUnavailable
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	offline := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateOffline, ConfirmedAt: time.Unix(101, 0), Sequence: 2, LeaseEpoch: 2}

	if err := manager.ApplyRoomTransition(context.Background(), live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("live processor failure = %v, want unavailable", err)
	}
	wantFence := sessions.currentOwner()
	if !validOwnerFence(wantFence) {
		t.Fatal("failed EndSession released the owner fence")
	}
	if got := log.snapshot(); containsOperation(got, "release") {
		t.Fatalf("failed EndSession released ownership: %v", got)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); err != nil {
		t.Fatalf("offline EndSession retry = %v", err)
	}
	afterCleanup := log.snapshot()
	if !containsOrderedOperations(afterCleanup, []string{"start:42", "processor-create-failed", "end:1", "end:1", "release"}) {
		t.Fatalf("cleanup order = %v", afterCleanup)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); err != nil {
		t.Fatalf("duplicate offline = %v", err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, afterCleanup) {
		t.Fatalf("duplicate offline repeated cleanup: before=%v after=%v", afterCleanup, got)
	}
	for _, command := range sessions.endCommands() {
		if command.SessionID != 1 || command.Owner != wantFence {
			t.Fatalf("EndSession command = %#v, want session 1 fence %#v", command, wantFence)
		}
	}
}

func TestTransitionLiveRetryFinishesPendingEndBeforeStartingReplacement(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, endFailures: 1, logOwnership: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	var factoryCalls int
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{ProcessorFactory: processorFactoryFunc(func(context.Context, OwnerFence, Session) (SessionProcessor, error) {
		factoryCalls++
		if factoryCalls == 1 {
			return nil, ErrUnavailable
		}
		return &finalizingSessionProcessor{log: log}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	if err := manager.ApplyRoomTransition(context.Background(), live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first live = %v, want unavailable", err)
	}
	firstFence := sessions.currentOwner()
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatalf("live retry = %v", err)
	}
	if sessions.startedCount() != 2 || factoryCalls != 2 {
		t.Fatalf("starts/factory calls = %d/%d, want 2/2", sessions.startedCount(), factoryCalls)
	}
	ends, releases := sessions.endCommands(), sessions.releaseFences()
	if len(ends) != 2 || ends[0].SessionID != 1 || ends[1].SessionID != 1 || ends[0].Owner != firstFence || ends[1].Owner != firstFence {
		t.Fatalf("pending EndSession retries = %#v, want session 1 fence %#v", ends, firstFence)
	}
	if len(releases) != 1 || releases[0] != firstFence {
		t.Fatalf("pending release = %#v, want %#v", releases, firstFence)
	}
	afterRetry := log.snapshot()
	if !containsOrderedOperations(afterRetry, []string{"start:42", "end:1", "end:1", "release", "start:42"}) {
		t.Fatalf("live retry order = %v", afterRetry)
	}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatalf("duplicate successful live = %v", err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, afterRetry) {
		t.Fatalf("duplicate live repeated admission: before=%v after=%v", afterRetry, got)
	}
}

func TestTransitionLiveRetryFinishesPendingReleaseBeforeStartingReplacement(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, releaseFailures: 1, logOwnership: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	var factoryCalls int
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{ProcessorFactory: processorFactoryFunc(func(context.Context, OwnerFence, Session) (SessionProcessor, error) {
		factoryCalls++
		if factoryCalls == 1 {
			return nil, ErrUnavailable
		}
		return &finalizingSessionProcessor{log: log}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	if err := manager.ApplyRoomTransition(context.Background(), live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first live = %v, want unavailable", err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatalf("live release retry = %v", err)
	}
	if got := log.snapshot(); !containsOrderedOperations(got, []string{"start:42", "end:1", "release", "start:42"}) {
		t.Fatalf("release retry/start order = %v", got)
	}
	if releases := sessions.releaseFences(); len(releases) != 2 || releases[0] != releases[1] {
		t.Fatalf("release retries = %#v, want same exact fence twice", releases)
	}
}

func TestTransitionPrePublishAbortEndsBeforeFinalizeAndExactOwnerRelease(t *testing.T) {
	for _, test := range []struct {
		name  string
		abort func(*accountRuntime)
	}{
		{name: "owner fence changed", abort: func(account *accountRuntime) {
			account.owner = OwnerFence{AccountID: account.accountID, Token: ownerToken(0x99), Epoch: 99}
		}},
		{name: "account disabled", abort: func(account *accountRuntime) { account.disabled = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := &operationLog{}
			sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, logOwnership: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
			var manager *Manager
			var err error
			manager, err = NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{
				ProcessorFactory: finalizingProcessorFactory{log: log},
				BeforeSessionPublish: func() {
					account, err := manager.accountExisting(7)
					if err != nil {
						t.Fatal(err)
					}
					account.mu.Lock()
					test.abort(account)
					account.mu.Unlock()
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
			live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
			if err := manager.ApplyRoomTransition(context.Background(), live); err == nil {
				t.Fatal("pre-publish abort unexpectedly succeeded")
			}
			commands, releases := sessions.endCommands(), sessions.releaseFences()
			if len(commands) != 1 || len(releases) != 1 || commands[0].SessionID != 1 || commands[0].Owner != releases[0] {
				t.Fatalf("end/release fences = %#v / %#v", commands, releases)
			}
			if got := log.snapshot(); !containsOrderedOperations(got, []string{"end:1", "processor-finalize", "release"}) {
				t.Fatalf("EndSession/finalize/release order = %v", got)
			}
		})
	}
}

func TestTransitionPrePublishShutdownEndsBeforeFinalizeAndOwnerRelease(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, logOwnership: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	startCommitted := make(chan struct{})
	allowPublish := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{
		ProcessorFactory:     finalizingProcessorFactory{log: log},
		BeforeSessionPublish: func() { close(startCommitted); <-allowPublish },
	})
	if err != nil {
		t.Fatal(err)
	}
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	applied := make(chan error, 1)
	go func() { applied <- manager.ApplyRoomTransition(context.Background(), live) }()
	<-startCommitted
	shutdown := make(chan error, 1)
	go func() { shutdown <- manager.Shutdown(context.Background()) }()
	account, accountErr := manager.accountExisting(7)
	if accountErr != nil {
		t.Fatal(accountErr)
	}
	for {
		account.mu.Lock()
		shutting := account.shutting
		account.mu.Unlock()
		if shutting {
			break
		}
		goruntime.Gosched()
	}
	close(allowPublish)
	if err := <-applied; !errors.Is(err, ErrClosed) {
		t.Fatalf("live during shutdown = %v, want closed", err)
	}
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	commands, releases := sessions.endCommands(), sessions.releaseFences()
	if len(commands) != 1 || len(releases) != 1 || commands[0].Owner != releases[0] || !validOwnerFence(commands[0].Owner) {
		t.Fatalf("shutdown end/release fences = %#v / %#v", commands, releases)
	}
	if got := log.snapshot(); !containsOrderedOperations(got, []string{"end:1", "processor-finalize", "release"}) {
		t.Fatalf("shutdown cleanup order = %v", got)
	}
}

func TestTransitionPrePublishShutdownDeadlineRetainsCleanupForSecondShutdown(t *testing.T) {
	log := &operationLog{}
	base := &transitionSessions{
		orderedSessions: &orderedSessions{enabled: true, logOwnership: true, log: log},
		accounts:        map[string][]int64{"42": {7}},
		broadcasts:      map[string]int64{"42": 99},
	}
	sessions := &sequencedShutdownEndSessions{
		transitionSessions: base,
		secondEndStarted:   make(chan struct{}),
		secondEndReturned:  make(chan struct{}),
		releaseCalled:      make(chan struct{}, 1),
	}
	startCommitted := make(chan struct{})
	allowPublish := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{
		ProcessorFactory:     finalizingProcessorFactory{log: log},
		BeforeSessionPublish: func() { close(startCommitted); <-allowPublish },
	})
	if err != nil {
		t.Fatal(err)
	}
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	applied := make(chan error, 1)
	go func() { applied <- manager.ApplyRoomTransition(context.Background(), live) }()
	<-startCommitted

	shutdownContext, cancelShutdown := context.WithCancel(context.Background())
	firstShutdown := make(chan error, 1)
	go func() { firstShutdown <- manager.Shutdown(shutdownContext) }()
	account, accountErr := manager.accountExisting(7)
	if accountErr != nil {
		t.Fatal(accountErr)
	}
	for {
		account.mu.Lock()
		shutting := account.shutting
		account.mu.Unlock()
		if shutting {
			break
		}
		goruntime.Gosched()
	}
	close(allowPublish)
	if err := <-applied; err == nil {
		t.Fatal("live during shutdown unexpectedly succeeded")
	}
	<-sessions.secondEndStarted
	cancelShutdown()
	<-sessions.secondEndReturned
	if err := <-firstShutdown; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Shutdown error = %v, want canceled", err)
	}
	select {
	case <-sessions.releaseCalled:
		t.Fatalf("expired shutdown released before EndSession succeeded: %v", log.snapshot())
	case <-time.After(100 * time.Millisecond):
	}
	if containsOperation(log.snapshot(), "processor-finalize") {
		t.Fatalf("expired shutdown finalized before EndSession succeeded: %v", log.snapshot())
	}
	account.mu.Lock()
	pending := account.transitionPending
	account.mu.Unlock()
	if pending == nil || pending.session.ID != 1 || pending.owner != sessions.firstFence() {
		t.Fatalf("expired shutdown lost pending session/fence: pending=%#v fence=%#v", pending, sessions.firstFence())
	}

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown error = %v", err)
	}
	if got := log.snapshot(); !containsOrderedOperations(got, []string{"end:1", "end:1", "end:1", "processor-finalize", "release", "sources-close", "sources-wait"}) {
		t.Fatalf("shutdown retry order = %v", got)
	}
	commands, releases := sessions.endCommands(), sessions.releaseFences()
	if len(commands) != 3 || len(releases) != 1 {
		t.Fatalf("end/release attempts = %#v / %#v", commands, releases)
	}
	for _, command := range commands {
		if command.SessionID != 1 || command.Owner != releases[0] {
			t.Fatalf("EndSession command = %#v, release fence = %#v", command, releases[0])
		}
	}
}

func TestHeartbeatOwnershipConflictReconcilesPrePublishStartedSession(t *testing.T) {
	log := &operationLog{}
	base := &transitionSessions{
		orderedSessions: &orderedSessions{enabled: true, logOwnership: true, log: log},
		accounts:        map[string][]int64{"42": {7}},
		broadcasts:      map[string]int64{"42": 99},
	}
	sessions := &heartbeatConflictSessions{transitionSessions: base, renewed: make(chan OwnerFence, 1), reconciled: make(chan ReconcileSessionCommand, 1)}
	heartbeats := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	startCommitted := make(chan struct{})
	allowPublish := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{
		ProcessorFactory:     finalizingProcessorFactory{log: log},
		NewHeartbeatTimer:    heartbeats.New,
		BeforeSessionPublish: func() { close(startCommitted); <-allowPublish },
	})
	if err != nil {
		t.Fatal(err)
	}
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	applied := make(chan error, 1)
	go func() { applied <- manager.ApplyRoomTransition(context.Background(), live) }()
	<-startCommitted
	account, accountErr := manager.accountExisting(7)
	if accountErr != nil {
		t.Fatal(accountErr)
	}
	account.mu.Lock()
	pending := account.transitionPending
	account.mu.Unlock()
	if pending == nil || pending.session.ID != 1 {
		t.Fatalf("pre-publish pending = %#v", pending)
	}
	(<-heartbeats.created).Fire()
	lostFence := <-sessions.renewed
	for {
		account.mu.Lock()
		stale := account.stale
		account.mu.Unlock()
		if stale {
			break
		}
		goruntime.Gosched()
	}
	close(allowPublish)
	if err := <-applied; err == nil {
		t.Fatal("ownership-conflicted live unexpectedly succeeded")
	}
	command := <-sessions.reconciled
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
	defer cancelCleanup()
	if err := manager.waitStaleCleanup(cleanupContext, account); err != nil {
		t.Fatalf("wait stale cleanup = %v", err)
	}

	if command.AccountID != 7 || command.SessionID != 1 || command.LostOwner != lostFence || command.EndedAt.IsZero() {
		t.Fatalf("reconcile command = %#v, lost fence %#v", command, lostFence)
	}
	if commands := sessions.endCommands(); len(commands) != 0 {
		t.Fatalf("lost fence used for EndSession: %#v", commands)
	}
	if releases := sessions.releaseFences(); len(releases) != 0 {
		t.Fatalf("lost owner was released after conflict: %#v", releases)
	}
	pending.admissionMu.Lock()
	phase := pending.cleanupPhase
	pending.admissionMu.Unlock()
	if phase != cleanupPhaseReleased {
		t.Fatalf("cleanup phase = %d, want released", phase)
	}
	account.mu.Lock()
	current, retained, stale := account.current, account.transitionPending, account.stale
	account.mu.Unlock()
	if current != nil || retained != nil || stale {
		t.Fatalf("reconciled account state = current %#v pending %#v stale %v", current, retained, stale)
	}
	if got := log.snapshot(); !containsOrderedOperations(got, []string{"start:42", "cancel:42", "processor-close", "reconcile:1", "processor-finalize"}) {
		t.Fatalf("reconcile cleanup order = %v", got)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownAndApplyRoomTransitionSerializeClosedState(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	applied := make(chan error, 1)
	go func() { applied <- manager.ApplyRoomTransition(context.Background(), live) }()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-applied; err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("concurrent ApplyRoomTransition = %v", err)
	}
}

// This test fails if a second Shutdown call blocks on the first caller's
// internal serialization lock after its own context deadline.
func TestConcurrentShutdownWaiterHonorsOwnDeadline(t *testing.T) {
	sources := &blockingShutdownRoomSources{waitStarted: make(chan struct{}), release: make(chan struct{})}
	manager, err := NewManager(Dependencies{Sessions: &orderedSessions{enabled: true, log: &operationLog{}}, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	firstResult := make(chan error, 1)
	go func() { firstResult <- manager.Shutdown(context.Background()) }()
	select {
	case <-sources.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("first Shutdown did not reach RoomSources.Wait")
	}

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 20*time.Millisecond)
	secondResult := make(chan error, 1)
	go func() { secondResult <- manager.Shutdown(deadline) }()
	select {
	case err := <-secondResult:
		cancelDeadline()
		if !errors.Is(err, context.DeadlineExceeded) {
			close(sources.release)
			t.Fatalf("second Shutdown error = %v, want deadline", err)
		}
	case <-time.After(250 * time.Millisecond):
		cancelDeadline()
		close(sources.release)
		<-firstResult
		t.Fatal("concurrent Shutdown ignored its deadline while waiting for serialization")
	}
	close(sources.release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSetRoomPersistsSelectionWithoutStartingRuntime(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"8": "84"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.SetRoom(context.Background(), 7, "8"); err != nil {
		t.Fatal(err)
	}
	if got, want := log.snapshot(), []string{"resolve:84", "persist:84"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selection operations = %v, want %v", got, want)
	}
	if sources.maximumActive() != 0 {
		t.Fatalf("selection started %d room sources", sources.maximumActive())
	}
}

func TestAdmittedProcessReceivesCapturedOwnerFence(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	token := ownerToken(0x71)
	received := make(chan OwnerFence, 1)
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{
		OwnerToken: token,
		Process: func(_ context.Context, owner OwnerFence, _ Session, _ roomsource.Event) error {
			received <- owner
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	sources.subscription(t, 0).Emit(roomsource.Event{ID: "gift-1", RoomID: "42"})
	if owner := <-received; owner.AccountID != 7 || owner.Token != token || owner.Epoch != 1 {
		t.Fatalf("Process owner = %#v", owner)
	}
}

func TestManagerCreatesBoundProcessorAndClosesItBeforeEndingSession(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	factory := &recordingProcessorFactory{log: log, accepted: make(chan roomsource.Event, 1)}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{ProcessorFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	event := roomsource.Event{ID: "gift-1", RoomID: "42"}
	sources.subscription(t, 0).Emit(event)
	if received := <-factory.accepted; received.ID != event.ID {
		t.Fatalf("accepted event = %#v", received)
	}
	lease.Release()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.snapshot(); !containsOrderedOperations(got, []string{"processor-new:7:1", "processor-accept:gift-1", "processor-close", "end:1"}) {
		t.Fatalf("processor lifecycle order = %v", got)
	}
}

func TestManagerStatusIncludesBoundProcessorDegradation(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	factory := &recordingProcessorFactory{log: log, accepted: make(chan roomsource.Event, 1), status: ProcessorStatus{Degraded: true, Buffered: 1, ConnectionHealthy: true}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{ProcessorFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.State != StateDegraded || !status.Degraded || status.PersistenceBuffered != 1 || !status.ConnectionHealthy {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
}

func TestManagerKeepsProcessorConnectionHealthVisibleAcrossSourceErrors(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	factory := &recordingProcessorFactory{log: log, accepted: make(chan roomsource.Event, 1), status: ProcessorStatus{ConnectionHealthy: true}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{ProcessorFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	subscription := sources.subscription(t, 0)
	subscription.Fail(errors.New("upstream lost"))
	if status, _ := manager.Status(context.Background(), 7); status.ConnectionHealthy {
		t.Fatalf("connection health remained true after source error: %#v", status)
	}
	subscription.Emit(roomsource.Event{ID: "healthy", RoomID: "42"})
	<-factory.accepted
	if status, _ := manager.Status(context.Background(), 7); !status.ConnectionHealthy || status.Degraded || status.State != StateActive {
		t.Fatalf("source health did not recover on frame: %#v", status)
	}
}

func TestManagerDoesNotOverwriteSourceFailureDuringProcessorStartup(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	processor := &startupHealthProcessor{initialHealthyStarted: make(chan struct{}), releaseInitialHealthy: make(chan struct{}), unhealthyApplied: make(chan struct{})}
	factory := processorFactoryFunc(func(context.Context, OwnerFence, Session) (SessionProcessor, error) { return processor, nil })
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{ProcessorFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	acquired := make(chan error, 1)
	go func() {
		_, err := manager.Acquire(context.Background(), 7, LeaseConfig)
		acquired <- err
	}()
	<-processor.initialHealthyStarted
	failureDelivered := make(chan struct{})
	go func() {
		sources.subscription(t, 0).Fail(errors.New("failed during startup"))
		close(failureDelivered)
	}()
	select {
	case <-processor.unhealthyApplied:
	case <-time.After(100 * time.Millisecond):
	}
	close(processor.releaseInitialHealthy)
	<-failureDelivered
	if err := <-acquired; err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.ConnectionHealthy || !status.Degraded || status.State != StateDegraded {
		t.Fatalf("startup failure status = %#v, %v", status, err)
	}
}

func TestManagerSerializesSourceErrorStateBeforeConcurrentHealthyFrame(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	processor := &callbackOrderProcessor{unhealthyApplied: make(chan struct{}), recoveredApplied: make(chan struct{})}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{ProcessorFactory: processorFactoryFunc(func(context.Context, OwnerFence, Session) (SessionProcessor, error) { return processor, nil })})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	account, err := manager.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	subscription := sources.subscription(t, 0)
	account.mu.Lock()
	errorDone := make(chan struct{})
	go func() {
		subscription.Fail(errors.New("overlapping source failure"))
		close(errorDone)
	}()
	<-processor.unhealthyApplied
	eventDone := make(chan struct{})
	go func() {
		subscription.Emit(roomsource.Event{ID: "healthy", RoomID: "42"})
		close(eventDone)
	}()
	recoveredBeforeErrorState := false
	select {
	case <-processor.recoveredApplied:
		recoveredBeforeErrorState = true
	case <-time.After(100 * time.Millisecond):
	}
	account.mu.Unlock()
	<-errorDone
	<-eventDone
	if recoveredBeforeErrorState {
		t.Fatal("healthy frame changed connection health before source error state was serialized")
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || !status.ConnectionHealthy || status.Degraded || status.State != StateActive {
		t.Fatalf("overlap recovery status = %#v, %v", status, err)
	}
}

func TestManagerRevokesAdmissionImmediatelyWhenBufferedRetryLosesOwnership(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	repository := processorRepositoryFixture()
	attempts := 0
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		attempts++
		if attempts == 1 {
			return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
		}
		return configuration.RuntimeEventResult{}, configuration.ErrOwnership
	}
	retryTimers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	publisher := NewPublisher()
	factory, err := NewProcessorFactory(repository, publisher, ProcessorOptions{Now: processorNow, NewRetryTimer: retryTimers.New})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: processorManagerConfiguration{version: repository.version, state: repository.state}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{ProcessorFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	subscription := sources.subscription(t, 0)
	subscription.Emit(giftEventFixture("event-1", 123, "secret", "avatar"))
	(<-retryTimers.created).Fire()
	select {
	case <-subscription.cancelled:
	case <-time.After(time.Second):
		t.Fatal("ownership loss during buffered retry did not revoke admission before heartbeat")
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.Leases != 0 || status.State == StateActive {
		t.Fatalf("status after retry ownership loss = %#v, %v", status, err)
	}
	subscription.Emit(giftEventFixture("event-after-takeover", 456, "other", "avatar-two"))
	if attempts != 2 {
		t.Fatalf("repository attempts after revoked admission = %d, want 2", attempts)
	}
	if err := lease.Renew(context.Background()); err == nil {
		t.Fatal("lease remained usable after retry ownership loss")
	}
}

type processorManagerConfiguration struct {
	version configuration.Version
	state   configuration.State
}

func TestManagerDisplayStateLoadsOneActiveAppearanceProjection(t *testing.T) {
	version := processorVersionFixture()
	version.Definition.Attributes[0].Display = &gameplay.Display{Variant: "number", Appearance: &configuration.DisplayAppearance{ThemeID: "neon", FontSize: 40, AccentColor: "#ff3366", ShowConnection: true, Align: "center", PanelOpacity: 80}}
	state := processorStateFixture()
	manager := &Manager{dependencies: Dependencies{Configuration: processorManagerConfiguration{version: version, state: state}}}

	runtimeState, presentation, err := manager.DisplayState(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	want := &configuration.DisplayPresentation{AttributeAppearances: map[string]configuration.DisplayAppearance{"score": *version.Definition.Attributes[0].Display.Appearance}}
	if !reflect.DeepEqual(runtimeState, state.Runtime) || !reflect.DeepEqual(presentation, want) {
		t.Fatalf("DisplayState() = %#v, %#v; want %#v, %#v", runtimeState, presentation, state.Runtime, want)
	}
}

func (repository processorManagerConfiguration) LoadActive(context.Context, int64) (configuration.Version, configuration.State, error) {
	return repository.version, repository.state, nil
}

type processorFactoryFunc func(context.Context, OwnerFence, Session) (SessionProcessor, error)

func (factory processorFactoryFunc) New(ctx context.Context, owner OwnerFence, session Session) (SessionProcessor, error) {
	return factory(ctx, owner, session)
}

type startupHealthProcessor struct {
	healthy               atomic.Bool
	initialHealthyStarted chan struct{}
	releaseInitialHealthy chan struct{}
	unhealthyApplied      chan struct{}
	initialOnce           sync.Once
	unhealthyOnce         sync.Once
}

type callbackOrderProcessor struct {
	healthy          atomic.Bool
	healthyCalls     atomic.Int64
	unhealthyApplied chan struct{}
	recoveredApplied chan struct{}
	unhealthyOnce    sync.Once
	recoveredOnce    sync.Once
}

func (processor *callbackOrderProcessor) Accept(roomsource.Event) error { return nil }
func (processor *callbackOrderProcessor) Close(context.Context) error   { return nil }
func (processor *callbackOrderProcessor) Status() ProcessorStatus {
	return ProcessorStatus{ConnectionHealthy: processor.healthy.Load()}
}
func (processor *callbackOrderProcessor) SetConnectionHealthy(healthy bool) {
	processor.healthy.Store(healthy)
	if !healthy {
		processor.unhealthyOnce.Do(func() { close(processor.unhealthyApplied) })
		return
	}
	if processor.healthyCalls.Add(1) > 1 {
		processor.recoveredOnce.Do(func() { close(processor.recoveredApplied) })
	}
}

func (processor *startupHealthProcessor) Accept(roomsource.Event) error { return nil }
func (processor *startupHealthProcessor) Close(context.Context) error   { return nil }
func (processor *startupHealthProcessor) Status() ProcessorStatus {
	return ProcessorStatus{ConnectionHealthy: processor.healthy.Load()}
}
func (processor *startupHealthProcessor) SetConnectionHealthy(healthy bool) {
	if healthy {
		processor.initialOnce.Do(func() {
			close(processor.initialHealthyStarted)
			<-processor.releaseInitialHealthy
		})
	}
	processor.healthy.Store(healthy)
	if !healthy {
		processor.unhealthyOnce.Do(func() { close(processor.unhealthyApplied) })
	}
}

type recordingProcessorFactory struct {
	log      *operationLog
	accepted chan roomsource.Event
	status   ProcessorStatus
}

func (factory *recordingProcessorFactory) New(_ context.Context, owner OwnerFence, session Session) (SessionProcessor, error) {
	factory.log.add("processor-new:" + strconv.FormatInt(owner.AccountID, 10) + ":" + strconv.FormatInt(session.ID, 10))
	return &recordingSessionProcessor{factory: factory}, nil
}

type recordingSessionProcessor struct{ factory *recordingProcessorFactory }

func (processor *recordingSessionProcessor) Accept(event roomsource.Event) error {
	processor.factory.log.add("processor-accept:" + event.ID)
	processor.factory.accepted <- event
	return nil
}

func (processor *recordingSessionProcessor) Close(context.Context) error {
	processor.factory.log.add("processor-close")
	return nil
}

func (processor *recordingSessionProcessor) Status() ProcessorStatus { return processor.factory.status }
func (processor *recordingSessionProcessor) SetConnectionHealthy(healthy bool) {
	processor.factory.status.ConnectionHealthy = healthy
}

type finalizingProcessorFactory struct{ log *operationLog }

func (factory finalizingProcessorFactory) New(context.Context, OwnerFence, Session) (SessionProcessor, error) {
	return &finalizingSessionProcessor{log: factory.log}, nil
}

type finalizingSessionProcessor struct{ log *operationLog }

func (*finalizingSessionProcessor) Accept(roomsource.Event) error { return nil }
func (processor *finalizingSessionProcessor) Close(context.Context) error {
	processor.log.add("processor-close")
	return nil
}
func (*finalizingSessionProcessor) Status() ProcessorStatus   { return ProcessorStatus{} }
func (*finalizingSessionProcessor) SetConnectionHealthy(bool) {}
func (processor *finalizingSessionProcessor) FinalizeSession() {
	processor.log.add("processor-finalize")
}

func containsOrderedOperations(got, want []string) bool {
	index := 0
	for _, operation := range got {
		if index < len(want) && operation == want[index] {
			index++
		}
	}
	return index == len(want)
}

func TestLastLeaseExpiryEndsSessionAndShutdownWaitsForRoomSourceQuiescence(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	timers := &manualTimerFactory{}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{NewTimer: timers.New})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Acquire(context.Background(), 7, LeaseOBS)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	timers.Only(t).Fire()
	manager.waitAccountIdle(t, 7)
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"subscribe:42", "start:42", "cancel:42", "end:1"}) {
		t.Fatalf("idle expiry operations = %v", got)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"subscribe:42", "start:42", "cancel:42", "end:1", "sources-close", "sources-wait"}) {
		t.Fatalf("shutdown operations = %v", got)
	}
}

func TestIdleEndFailureBecomesDegradedAndRetriesWithoutNewPresence(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", endFailures: 1, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	timers := &manualTimerFactory{created: make(chan *manualTimer, 4)}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{NewTimer: timers.New})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	first := <-timers.created
	if first.delay != 10*time.Minute {
		t.Fatalf("first close delay = %v", first.delay)
	}
	first.Fire()
	retry := <-timers.created
	if retry.delay != time.Minute {
		t.Fatalf("failed-end retry delay = %v, want 1m", retry.delay)
	}
	status, _ := manager.Status(context.Background(), 7)
	if status.State != StateDegraded || status.SessionID != 1 {
		t.Fatalf("failed idle end status = %#v", status)
	}
	retry.Fire()
	manager.waitAccountIdle(t, 7)
	status, _ = manager.Status(context.Background(), 7)
	if status.State != StateIdle || status.SessionID != 0 {
		t.Fatalf("retried idle end status = %#v", status)
	}
}

func TestReconnectDuringFailedEndRetryClosesOldGuardBeforeRestart(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", endFailures: 1, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	timers := &manualTimerFactory{created: make(chan *manualTimer, 4)}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{NewTimer: timers.New})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	(<-timers.created).Fire()
	retryTimer := <-timers.created
	reconnected, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Release()
	if !retryTimer.Stopped() {
		t.Fatal("reconnect did not cancel failed-end retry timer")
	}
	status, _ := manager.Status(context.Background(), 7)
	if status.State != StateActive || status.SessionID != 2 || status.Degraded {
		t.Fatalf("reconnected status = %#v", status)
	}
	if sources.maximumActive() != 1 {
		t.Fatalf("maximum simultaneous sources = %d", sources.maximumActive())
	}
}

func TestShutdownRetriesSessionEndFailureBeforeQuiescingRoomSources(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", endFailures: 2, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	retryTimers := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{NewShutdownTimer: retryTimers.New})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	(<-retryTimers.created).Fire()
	(<-retryTimers.created).Fire()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown error = %v", err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"subscribe:42", "start:42", "cancel:42", "end:1", "end:1", "end:1", "sources-close", "sources-wait"}) {
		t.Fatalf("failed shutdown operations = %v", got)
	}
}

func TestShutdownDrainsAdmittedCommitBeforeEndAndOwnershipRelease(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log, logOwnership: true}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	processingStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{
		Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
			close(processingStarted)
			<-allowCommit
			log.add("commit")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	sources.subscription(t, 0).Emit(roomsource.Event{ID: "gift-1", RoomID: "42"})
	<-processingStarted
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	<-sources.subscription(t, 0).cancelled
	if got := log.snapshot(); containsOperation(got, "end:1") || containsOperation(got, "release") {
		t.Fatalf("shutdown ended/released before admitted commit: %v", got)
	}
	close(allowCommit)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	want := []string{"subscribe:42", "start:42", "cancel:42", "commit", "end:1", "release", "sources-close", "sources-wait"}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown drain order = %v\nwant %v", got, want)
	}
}

func TestShutdownGraceExpiryForceCancelsProcessingThenSecondShutdownCanJoin(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log, logOwnership: true}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	processingStarted := make(chan struct{})
	processingExited := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{
		Process: func(ctx context.Context, _ OwnerFence, _ Session, _ roomsource.Event) error {
			close(processingStarted)
			<-ctx.Done()
			log.add("forced-exit")
			close(processingExited)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	sources.subscription(t, 0).Emit(roomsource.Event{ID: "gift-1", RoomID: "42"})
	<-processingStarted
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if err := manager.Shutdown(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown expired error = %v", err)
	}
	<-processingExited
	if items := log.snapshot(); containsOperation(items, "end:1") || containsOperation(items, "release") {
		t.Fatalf("expired shutdown ended or released before retry: %v", items)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown error = %v", err)
	}
	if err := manager.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after successful retry = %v", err)
	}
	items := log.snapshot()
	forced := slices.Index(items, "forced-exit")
	ended := slices.Index(items, "end:1")
	released := slices.Index(items, "release")
	if forced < 0 || ended <= forced || released <= ended {
		t.Fatalf("forced shutdown order = %v", items)
	}
}

func TestSetRoomRejectsBlankMalformedAndZeroWithoutSideEffects(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, log: log}
	sources := newOrderedRoomSources(log, nil)
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	for _, roomID := range []string{"", " ", "0", "01", "abc", "-1", "18446744073709551616"} {
		if err := manager.SetRoom(context.Background(), 7, roomID); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("SetRoom(%q) error = %v", roomID, err)
		}
	}
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("invalid rooms caused side effects: %v", got)
	}
}

func TestSetRoomWithoutPresencePersistsCanonicalTargetWithoutStartingSession(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.SetRoom(context.Background(), 7, "7"); err != nil {
		t.Fatal(err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"resolve:42", "persist:42"}) {
		t.Fatalf("room selection without presence operations = %v", got)
	}
	status, _ := manager.Status(context.Background(), 7)
	if status.State != StateIdle || status.SessionID != 0 {
		t.Fatalf("room selection without presence status = %#v", status)
	}
}

// This test fails if an inactive disabled account room edit claims runtime
// ownership, starts a source/session, skips canonicalization, or returns a
// result reconstructed outside the serialized mutation.
func TestMutateRoomPersistsDisabledCanonicalTargetWithoutClaimOrRuntimeAdmission(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: false, target: "111", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	result, err := manager.MutateRoom(context.Background(), 7, "7")
	if err != nil || result != (RoomMutationResult{OldCanonical: "111", NewCanonical: "42"}) {
		t.Fatalf("MutateRoom() = %#v, %v", result, err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"resolve:42", "persist-disabled:42"}) {
		t.Fatalf("disabled room mutation operations = %v", got)
	}
	sessions.mu.Lock()
	claims, starts := sessions.claims, sessions.nextID
	sessions.mu.Unlock()
	if claims != 0 || starts != 0 || sources.maximumActive() != 0 {
		t.Fatalf("disabled room mutation claims=%d starts=%d activeSources=%d", claims, starts, sources.maximumActive())
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.State != StateDisabled || status.SessionID != 0 {
		t.Fatalf("disabled room mutation status = %#v, %v", status, err)
	}
}

// This test fails if the disabled-account shortcut can overwrite the target
// while an admitted execution still requires fenced cleanup.
func TestMutateRoomDoesNotBypassFenceWhileDisabledExecutionNeedsCleanup(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "111", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"111": "111", "7": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	sessions.setEnabled(false)

	if _, err := manager.MutateRoom(context.Background(), 7, "7"); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("MutateRoom with disabled active execution = %v, want account disabled", err)
	}
	sessions.mu.Lock()
	target := sessions.target
	sessions.mu.Unlock()
	if target != "111" || containsOperation(log.snapshot(), "persist-disabled:42") {
		t.Fatalf("disabled cleanup bypassed fence: target=%q operations=%v", target, log.snapshot())
	}
}

func TestSetRoomPersistFailureDoesNotOpenUpstreamOrStartSession(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, persistFailures: 1, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.SetRoom(context.Background(), 7, "7"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SetRoom error = %v, want unavailable", err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"resolve:42", "persist:42"}) {
		t.Fatalf("persist failure operations = %v", got)
	}
	if sources.maximumActive() != 0 || len(sources.subs) != 0 || sessions.startedCount() != 0 {
		t.Fatalf("persist failure active/subscriptions/sessions = %d/%d/%d, want zero", sources.maximumActive(), len(sources.subs), sessions.startedCount())
	}
}

func TestSetRoomWithoutPresenceReportsTemporaryOwnershipReleaseFailure(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, releaseFailures: 1, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if err := manager.SetRoom(context.Background(), 7, "7"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SetRoom release error = %v, want unavailable", err)
	}
	if sources.maximumActive() != 0 || sessions.startedCount() != 0 {
		t.Fatal("temporary ownership release failure opened a runtime")
	}
}

func TestOfflineTransitionRetriesFailedExecutionEndBeforeLaterTransitions(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, endFailures: 1, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	offline := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateOffline, ConfirmedAt: time.Unix(101, 0), Sequence: 2, LeaseEpoch: 2}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first offline error = %v, want unavailable", err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), offline); err != nil {
		t.Fatalf("offline retry = %v", err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"subscribe:42", "start:42", "cancel:42", "end:1", "end:1"}) {
		t.Fatalf("offline retry operations = %v", got)
	}
}

func TestOfflineTransitionDrainsAcceptedEventBeforeEndingExecution(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	processingStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
		close(processingStarted)
		<-allowCommit
		log.add("commit")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 1}
	offline := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateOffline, ConfirmedAt: time.Unix(101, 0), Sequence: 2, LeaseEpoch: 2}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	sources.subscription(t, 0).Emit(roomsource.Event{ID: "gift"})
	<-processingStarted
	done := make(chan error, 1)
	go func() { done <- manager.ApplyRoomTransition(context.Background(), offline) }()
	<-sources.subscription(t, 0).cancelled
	if containsOperation(log.snapshot(), "end:1") {
		t.Fatalf("ended before accepted event committed: %v", log.snapshot())
	}
	close(allowCommit)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"subscribe:42", "start:42", "cancel:42", "commit", "end:1"}) {
		t.Fatalf("offline drain operations = %v", got)
	}
}

func TestAccountDisableDrainsActiveQueueBeforeEndingAndReleasesSubscription(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	processingStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
		close(processingStarted)
		<-allowCommit
		log.add("commit")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	sources.subscription(t, 0).Emit(roomsource.Event{ID: "gift"})
	<-processingStarted
	sessions.setEnabled(false)
	manager.AccountDisabled(7)
	<-sources.subscription(t, 0).cancelled
	if containsOperation(log.snapshot(), "end:1") {
		t.Fatal("disable ended session before committed work drained")
	}
	close(allowCommit)
	manager.waitAccountIdle(t, 7)
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"subscribe:42", "start:42", "cancel:42", "commit", "end:1"}) {
		t.Fatalf("disable operations = %v", got)
	}
	if status, _ := manager.Status(context.Background(), 7); status.State != StateDisabled || status.Leases != 0 || status.SessionID != 0 {
		t.Fatalf("disabled status = %#v", status)
	}
	if err := lease.Renew(context.Background()); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled lease Renew error = %v", err)
	}
}

func TestRoomSourceFailureMarksItsAccountDegraded(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	sources.subscription(t, 0).Fail(errors.New("upstream failed"))
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.State != StateDegraded || !status.Degraded {
		t.Fatalf("degraded Status() = %#v, %v", status, err)
	}
	if err := manager.SetRoom(context.Background(), 7, "8"); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(context.Background(), 7)
	if err != nil || status.State != StateActive || status.Degraded {
		t.Fatalf("replacement session Status() = %#v, %v", status, err)
	}
}

type operationLog struct {
	mu    sync.Mutex
	items []string
}

func (log *operationLog) add(value string) {
	log.mu.Lock()
	log.items = append(log.items, value)
	log.mu.Unlock()
}
func (log *operationLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.items...)
}
func containsOperation(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

type orderedSessions struct {
	mu              sync.Mutex
	enabled         bool
	target          string
	pendingJob      int64
	nextID          int64
	endFailures     int
	persistFailures int
	releaseFailures int
	logOwnership    bool
	log             *operationLog
	owner           OwnerFence
	epoch           uint64
	releases        []OwnerFence
	ends            []EndSessionCommand
	reconciles      []ReconcileSessionCommand
	claims          int
}

type transitionSessions struct {
	*orderedSessions
	accounts      map[string][]int64
	broadcasts    map[string]int64
	startFailures map[int64]int
}

type blockingBroadcastSessions struct {
	*transitionSessions
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (sessions *blockingBroadcastSessions) OpenBroadcastSession(ctx context.Context, roomID string) (int64, error) {
	sessions.once.Do(func() { close(sessions.started) })
	select {
	case <-sessions.release:
		return sessions.transitionSessions.OpenBroadcastSession(ctx, roomID)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

type blockingShutdownRoomSources struct {
	waitStarted chan struct{}
	release     chan struct{}
	startOnce   sync.Once
}

func (*blockingShutdownRoomSources) Resolve(_ context.Context, roomID string, _ int64) (string, error) {
	return roomID, nil
}
func (*blockingShutdownRoomSources) SubscribeCanonical(context.Context, string, int64, roomsource.Sink) (roomsource.Subscription, error) {
	return nil, errors.New("unexpected room subscription")
}
func (*blockingShutdownRoomSources) Close() {}
func (sources *blockingShutdownRoomSources) Wait(ctx context.Context) error {
	sources.startOnce.Do(func() { close(sources.waitStarted) })
	select {
	case <-sources.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type fenceMismatchSessions struct {
	transitionSessions
	endStarted chan struct{}
	allowEnd   chan struct{}
	once       sync.Once
}

func (sessions *fenceMismatchSessions) EndSession(ctx context.Context, command EndSessionCommand) error {
	sessions.once.Do(func() { close(sessions.endStarted) })
	select {
	case <-sessions.allowEnd:
	case <-ctx.Done():
		return ctx.Err()
	}
	return sessions.transitionSessions.EndSession(ctx, command)
}

type sequencedShutdownEndSessions struct {
	*transitionSessions
	mu                 sync.Mutex
	endCalls           int
	secondEndStarted   chan struct{}
	secondEndReturned  chan struct{}
	releaseCalled      chan struct{}
	firstObservedFence OwnerFence
}

type heartbeatConflictSessions struct {
	*transitionSessions
	renewed    chan OwnerFence
	reconciled chan ReconcileSessionCommand
	mu         sync.Mutex
	reconciles []ReconcileSessionCommand
}

func (sessions *heartbeatConflictSessions) RenewOwnership(_ context.Context, fence OwnerFence, _ time.Duration) error {
	sessions.renewed <- fence
	return ErrOwnershipConflict
}

func (sessions *heartbeatConflictSessions) ReconcileSession(_ context.Context, command ReconcileSessionCommand) error {
	sessions.mu.Lock()
	sessions.reconciles = append(sessions.reconciles, command)
	sessions.mu.Unlock()
	sessions.orderedSessions.log.add("reconcile:" + integer(command.SessionID))
	sessions.reconciled <- command
	return nil
}

func (sessions *sequencedShutdownEndSessions) EndSession(ctx context.Context, command EndSessionCommand) error {
	sessions.orderedSessions.log.add("end:" + integer(command.SessionID))
	sessions.orderedSessions.mu.Lock()
	sessions.orderedSessions.ends = append(sessions.orderedSessions.ends, command)
	sessions.orderedSessions.mu.Unlock()
	sessions.mu.Lock()
	sessions.endCalls++
	call := sessions.endCalls
	if call == 1 {
		sessions.firstObservedFence = command.Owner
	}
	sessions.mu.Unlock()
	switch call {
	case 1:
		return ErrUnavailable
	case 2:
		close(sessions.secondEndStarted)
		<-ctx.Done()
		close(sessions.secondEndReturned)
		return ctx.Err()
	default:
		return nil
	}
}

func (sessions *sequencedShutdownEndSessions) ReleaseOwnership(ctx context.Context, fence OwnerFence) error {
	select {
	case sessions.releaseCalled <- struct{}{}:
	default:
	}
	return sessions.orderedSessions.ReleaseOwnership(ctx, fence)
}

func (sessions *sequencedShutdownEndSessions) firstFence() OwnerFence {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.firstObservedFence
}

func (sessions *transitionSessions) EnabledAccountsForRoom(_ context.Context, roomID string) ([]int64, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return append([]int64(nil), sessions.accounts[roomID]...), nil
}

func (sessions *transitionSessions) OpenBroadcastSession(_ context.Context, roomID string) (int64, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.broadcasts[roomID], nil
}

func (sessions *transitionSessions) StartSession(ctx context.Context, command StartSessionCommand) (Session, error) {
	sessions.mu.Lock()
	if sessions.startFailures[command.AccountID] > 0 {
		sessions.startFailures[command.AccountID]--
		sessions.mu.Unlock()
		return Session{}, ErrUnavailable
	}
	sessions.mu.Unlock()
	session, err := sessions.orderedSessions.StartSession(ctx, command)
	session.BroadcastSessionID = command.BroadcastSessionID
	return session, err
}

type blockingPendingSessions struct {
	*orderedSessions
	mu         sync.Mutex
	pendingCtx context.Context
	started    chan struct{}
	release    chan struct{}
}

func (sessions *blockingPendingSessions) PendingMigration(ctx context.Context, _ int64) (int64, bool, error) {
	sessions.mu.Lock()
	sessions.pendingCtx = ctx
	sessions.mu.Unlock()
	close(sessions.started)
	select {
	case <-ctx.Done():
		return 0, false, ctx.Err()
	case <-sessions.release:
		return 0, false, nil
	}
}
func (sessions *blockingPendingSessions) contextError() error {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.pendingCtx.Err()
}

func (sessions *orderedSessions) AccountEnabled(context.Context, int64) (bool, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.enabled, nil
}
func (sessions *orderedSessions) ClaimOwnership(_ context.Context, accountID int64, token OwnerToken, _ time.Duration) (OwnerClaim, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.claims++
	if !sessions.enabled {
		return OwnerClaim{}, ErrAccountDisabled
	}
	if validOwnerFence(sessions.owner) && sessions.owner.Token != token {
		return OwnerClaim{}, ErrOwnershipConflict
	}
	reconcile := !validOwnerFence(sessions.owner)
	if reconcile {
		sessions.epoch++
		sessions.owner = OwnerFence{AccountID: accountID, Token: token, Epoch: sessions.epoch}
	}
	return OwnerClaim{Fence: sessions.owner, Reconcile: false}, nil
}

func (sessions *orderedSessions) PersistDisabledTargetRoom(_ context.Context, command PersistDisabledTargetRoomCommand) (RoomMutationResult, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.enabled {
		return RoomMutationResult{}, ErrUnavailable
	}
	old := sessions.target
	sessions.target = command.RoomID
	sessions.log.add("persist-disabled:" + command.RoomID)
	return RoomMutationResult{OldCanonical: old, NewCanonical: command.RoomID}, nil
}
func (sessions *orderedSessions) RenewOwnership(_ context.Context, fence OwnerFence, _ time.Duration) error {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if !sessions.enabled {
		return ErrAccountDisabled
	}
	if sessions.owner != fence {
		return ErrOwnershipConflict
	}
	return nil
}
func (sessions *orderedSessions) ReleaseOwnership(_ context.Context, fence OwnerFence) error {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.releases = append(sessions.releases, fence)
	if sessions.releaseFailures > 0 {
		sessions.releaseFailures--
		return ErrUnavailable
	}
	if sessions.owner != fence {
		return ErrOwnershipConflict
	}
	sessions.owner = OwnerFence{}
	if sessions.logOwnership {
		sessions.log.add("release")
	}
	return nil
}
func (sessions *orderedSessions) currentOwner() OwnerFence {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.owner
}
func (sessions *orderedSessions) releaseFences() []OwnerFence {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return append([]OwnerFence(nil), sessions.releases...)
}
func (sessions *orderedSessions) setEnabled(enabled bool) {
	sessions.mu.Lock()
	sessions.enabled = enabled
	sessions.mu.Unlock()
}
func (sessions *orderedSessions) TargetRoom(context.Context, int64) (string, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.target == "" {
		return "", ErrNoTargetRoom
	}
	return sessions.target, nil
}
func (sessions *orderedSessions) PersistTargetRoom(_ context.Context, command PersistTargetRoomCommand) error {
	sessions.mu.Lock()
	if sessions.persistFailures > 0 {
		sessions.persistFailures--
		sessions.mu.Unlock()
		sessions.log.add("persist:" + command.RoomID)
		return ErrUnavailable
	}
	if sessions.owner != command.Owner {
		sessions.mu.Unlock()
		return ErrOwnershipConflict
	}
	sessions.target = command.RoomID
	sessions.mu.Unlock()
	sessions.log.add("persist:" + command.RoomID)
	return nil
}
func (sessions *orderedSessions) startedCount() int {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return int(sessions.nextID)
}
func (sessions *orderedSessions) StartSession(_ context.Context, command StartSessionCommand) (Session, error) {
	sessions.mu.Lock()
	sessions.nextID++
	id := sessions.nextID
	sessions.mu.Unlock()
	sessions.log.add("start:" + command.RoomID)
	return Session{ID: id, AccountID: command.AccountID, RoomID: command.RoomID, ConfigVersionID: command.ConfigVersionID, StartedAt: command.StartedAt}, nil
}
func (sessions *orderedSessions) EndSession(_ context.Context, command EndSessionCommand) error {
	sessions.log.add("end:" + integer(command.SessionID))
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.ends = append(sessions.ends, command)
	if sessions.endFailures > 0 {
		sessions.endFailures--
		return ErrUnavailable
	}
	return nil
}
func (sessions *orderedSessions) ReconcileSession(_ context.Context, command ReconcileSessionCommand) error {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.reconciles = append(sessions.reconciles, command)
	return nil
}
func (sessions *orderedSessions) endCommands() []EndSessionCommand {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return append([]EndSessionCommand(nil), sessions.ends...)
}
func (sessions *orderedSessions) PendingMigration(context.Context, int64) (int64, bool, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.pendingJob == 0 {
		return 0, false, nil
	}
	sessions.log.add("pending:" + integer(sessions.pendingJob))
	return sessions.pendingJob, true, nil
}

type orderedMigration struct{ log *operationLog }

func (migrationService orderedMigration) ApplyPendingAfterSession(_ context.Context, _ migration.OwnerFence, jobID int64) (migration.Job, error) {
	migrationService.log.add("apply:" + integer(jobID))
	return migration.Job{ID: jobID, Status: "applied"}, nil
}

type orderedRoomSources struct {
	mu        sync.Mutex
	log       *operationLog
	canonical map[string]string
	active    int
	maxActive int
	subs      []*orderedSubscription
	closed    bool
}

func newOrderedRoomSources(log *operationLog, canonical map[string]string) *orderedRoomSources {
	return &orderedRoomSources{log: log, canonical: canonical}
}
func (sources *orderedRoomSources) Subscribe(_ context.Context, roomID string, _ int64, sink roomsource.Sink) (roomsource.Subscription, error) {
	canonical, err := sources.Resolve(context.Background(), roomID, 1)
	if err != nil {
		return nil, err
	}
	return sources.SubscribeCanonical(context.Background(), canonical, 1, sink)
}
func (sources *orderedRoomSources) Resolve(_ context.Context, roomID string, _ int64) (string, error) {
	sources.mu.Lock()
	canonical := sources.canonical[roomID]
	if canonical == "" {
		canonical = roomID
	}
	sources.mu.Unlock()
	sources.log.add("resolve:" + canonical)
	return canonical, nil
}
func (sources *orderedRoomSources) SubscribeCanonical(_ context.Context, canonical string, _ int64, sink roomsource.Sink) (roomsource.Subscription, error) {
	sources.mu.Lock()
	subscription := &orderedSubscription{sources: sources, roomID: canonical, sink: sink, done: make(chan struct{}), cancelled: make(chan struct{})}
	sources.subs = append(sources.subs, subscription)
	sources.active++
	if sources.active > sources.maxActive {
		sources.maxActive = sources.active
	}
	sources.mu.Unlock()
	sources.log.add("subscribe:" + canonical)
	return subscription, nil
}
func (sources *orderedRoomSources) Close() {
	sources.mu.Lock()
	sources.closed = true
	sources.mu.Unlock()
	sources.log.add("sources-close")
}
func (sources *orderedRoomSources) Wait(context.Context) error {
	sources.log.add("sources-wait")
	return nil
}
func (sources *orderedRoomSources) subscription(t *testing.T, index int) *orderedSubscription {
	t.Helper()
	sources.mu.Lock()
	defer sources.mu.Unlock()
	if len(sources.subs) <= index {
		t.Fatalf("subscription %d missing", index)
	}
	return sources.subs[index]
}
func (sources *orderedRoomSources) maximumActive() int {
	sources.mu.Lock()
	defer sources.mu.Unlock()
	return sources.maxActive
}

type orderedSubscription struct {
	once      sync.Once
	sources   *orderedRoomSources
	roomID    string
	sink      roomsource.Sink
	done      chan struct{}
	cancelled chan struct{}
}

func (subscription *orderedSubscription) RoomID() string { return subscription.roomID }
func (subscription *orderedSubscription) Cancel() {
	subscription.once.Do(func() {
		subscription.sources.mu.Lock()
		subscription.sources.active--
		subscription.sources.mu.Unlock()
		subscription.sources.log.add("cancel:" + subscription.roomID)
		close(subscription.cancelled)
		close(subscription.done)
	})
}
func (subscription *orderedSubscription) Done() <-chan struct{} { return subscription.done }
func (subscription *orderedSubscription) Wait(ctx context.Context) error {
	select {
	case <-subscription.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (subscription *orderedSubscription) Emit(event roomsource.Event) {
	subscription.sink.OnEvent(event)
}
func (subscription *orderedSubscription) Fail(err error) { subscription.sink.OnError(err) }

func integer(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		buffer[index] = '-'
	}
	return string(buffer[index:])
}

var _ ConfigurationRepository = fakeConfiguration{}
