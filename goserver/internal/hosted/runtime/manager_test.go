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

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	"bilibili-live-gift-panel/internal/hosted/roomwatcher"
)

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

func TestManagerRejectsStaleRoomTransitionEpochAndIgnoresDuplicateSequence(t *testing.T) {
	log := &operationLog{}
	sessions := &transitionSessions{orderedSessions: &orderedSessions{enabled: true, log: log}, accounts: map[string][]int64{"42": {7}}, broadcasts: map[string]int64{"42": 99}}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(log, map[string]string{"42": "42"})}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	live := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateOffline, To: roomwatcher.StateLive, ConfirmedAt: time.Unix(100, 0), NewBroadcast: true, Sequence: 1, LeaseEpoch: 3}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyRoomTransition(context.Background(), live); err != nil {
		t.Fatalf("duplicate transition = %v", err)
	}
	graceUntil := time.Unix(800, 0)
	staleEpoch := roomwatcher.Transition{RoomID: "42", From: roomwatcher.StateLive, To: roomwatcher.StateGrace, ConfirmedAt: time.Unix(200, 0), GraceUntil: &graceUntil, Sequence: 2, LeaseEpoch: 3}
	if err := manager.ApplyRoomTransition(context.Background(), staleEpoch); !errors.Is(err, ErrUnavailable) {
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

func TestShutdownGraceExpiryForceCancelsProcessingThenWaitCanJoin(t *testing.T) {
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
	if err := manager.Wait(context.Background()); err != nil && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Wait error = %v, want nil or cleanup unavailable after grace deadline", err)
	}
	<-processingExited
	items := log.snapshot()
	forced := slices.Index(items, "forced-exit")
	ended := slices.Index(items, "end:1")
	released := slices.Index(items, "release")
	if forced < 0 || released < 0 || (ended >= 0 && released <= ended) {
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
}

type transitionSessions struct {
	*orderedSessions
	accounts      map[string][]int64
	broadcasts    map[string]int64
	startFailures map[int64]int
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
