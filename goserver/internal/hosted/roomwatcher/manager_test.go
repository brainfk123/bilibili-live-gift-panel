package roomwatcher

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

// This test fails if SetReferences cannot publish complete membership
// snapshots, publishes an unchanged room, or starts an added room before the
// removed room's snapshot has been durably ordered.
func TestManagerPublishesOnlyChangedReferenceSnapshotsInRemovalFirstOrder(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedOffline}, repository, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "7"}, {AccountID: 2, RoomID: "7"}, {AccountID: 3, RoomID: "9"}}); err != nil {
		t.Fatal(err)
	}
	initial, err := manager.ReplayEvents(context.Background(), 0, 2)
	if err != nil || len(initial) != 2 {
		t.Fatalf("initial ReplayEvents = %#v, %v", initial, err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 2, RoomID: "7"}, {AccountID: 1, RoomID: "8"}, {AccountID: 3, RoomID: "9"}}); err != nil {
		t.Fatal(err)
	}
	changed, err := manager.ReplayEvents(context.Background(), 2, 2)
	if err != nil || len(changed) != 2 {
		t.Fatalf("changed ReplayEvents = %#v, %v", changed, err)
	}
	removed, added := changed[0], changed[1]
	if removed.Sequence != 3 || removed.RoomReferencesChanged == nil || removed.RoomReferencesChanged.RoomID != "7" || !slices.Equal(removed.RoomReferencesChanged.AccountIDs, []int64{2}) {
		t.Fatalf("removed-room event = %#v, want sequence 3 and room 7 snapshot [2]", removed)
	}
	if added.Sequence != 4 || added.RoomReferencesChanged == nil || added.RoomReferencesChanged.RoomID != "8" || !slices.Equal(added.RoomReferencesChanged.AccountIDs, []int64{1}) {
		t.Fatalf("added-room event = %#v, want sequence 4 and room 8 snapshot [1]", added)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "8"}, {AccountID: 2, RoomID: "7"}, {AccountID: 3, RoomID: "9"}}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := manager.ReplayEvents(context.Background(), 4, 1)
	if err != nil || len(unchanged) != 0 {
		t.Fatalf("duplicate snapshot replay = %#v, %v; want no new event", unchanged, err)
	}
}

func TestManagerDeduplicatesCanonicalRoomReferences(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedOffline}, repository, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: " 007 "}, {AccountID: 2, RoomID: "7"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(manager.watchers); got != 1 {
		t.Fatalf("watched rooms = %d, want 1", got)
	}
	references := repository.referencesSnapshot()
	if len(references) != 2 || references[0].RoomID != "7" || references[1].RoomID != "7" {
		t.Fatalf("persisted references = %#v, want two canonical references for room 7", references)
	}
}

// This test fails to compile until the public repository port exposes startup
// recovery without consumers depending on the unexported SQL implementation.
func TestRepositoryInterfaceExposesRecoverableLoader(t *testing.T) {
	want := []RecoverableRoom{{RoomID: "7", State: StateLive, LeaseEpoch: 8, References: []Reference{{AccountID: 1, RoomID: "7"}}}}
	var repository Repository = &fakeRepository{recoverable: want}
	got, err := repository.LoadRecoverable(context.Background())
	if err != nil {
		t.Fatalf("LoadRecoverable() error = %v", err)
	}
	if len(got) != 1 || got[0].RoomID != "7" || got[0].State != StateLive || got[0].LeaseEpoch != 8 || len(got[0].References) != 1 {
		t.Fatalf("LoadRecoverable() = %#v, want %#v", got, want)
	}
}

func TestManagerRemovesLastReferenceAfterPersistingTerminalState(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedLive}, repository, Options{Now: func() time.Time { return time.Unix(100, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "7"}}); err != nil {
		t.Fatal(err)
	}
	events, replayErr := manager.ReplayEvents(context.Background(), 0, 2)
	if replayErr != nil || len(events) != 2 {
		t.Fatalf("ReplayEvents = %#v, %v", events, replayErr)
	}
	references, state := events[0], events[1]
	if references.Sequence != 1 || references.RoomReferencesChanged == nil || !slices.Equal(references.RoomReferencesChanged.AccountIDs, []int64{1}) {
		t.Fatalf("published references = %#v, want room 7 snapshot [1]", references)
	}
	if transition := state.RoomStateChanged; state.Sequence != 2 || transition == nil || transition.RoomID != "7" || transition.From != StateOffline || transition.To != StateLive || !transition.NewBroadcast || transition.LeaseEpoch != 7 {
		t.Fatalf("published state event = %#v, want opening live transition for room 7", state)
	}
	if err := manager.SetReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := len(manager.watchers); got != 0 {
		t.Fatalf("watched rooms = %d, want 0", got)
	}
	transitions := repository.transitionsSnapshot()
	if len(transitions) != 2 || transitions[0].To != StateLive || transitions[1].From != StateLive || transitions[1].To != StateOffline {
		t.Fatalf("persisted transitions = %#v, want live then terminal offline", transitions)
	}
}

func TestManagerRemovesLastReferenceOnlyAfterAtomicRepositoryReceipt(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedLive}, repository, Options{Now: func() time.Time { return time.Unix(100, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "7"}}); err != nil {
		t.Fatal(err)
	}
	_ = awaitEvent(t, manager.Events())
	if err := manager.SetReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := repository.atomicTerminalsSnapshot(); len(got) != 1 || got[0].RoomID != "7" || got[0].From != StateLive || got[0].To != StateOffline {
		t.Fatalf("atomic terminal candidates = %#v, want room 7 live -> offline", got)
	}
	if references := awaitEvent(t, manager.Events()); references.Sequence != 3 || references.RoomReferencesChanged == nil || len(references.RoomReferencesChanged.AccountIDs) != 0 {
		t.Fatalf("terminal reference notification = %#v, want empty sequence 3 snapshot", references)
	}
	events, replayErr := manager.ReplayEvents(context.Background(), 3, 1)
	if replayErr != nil || len(events) != 1 {
		t.Fatalf("terminal ReplayEvents = %#v, %v", events, replayErr)
	}
	if event := events[0]; event.Sequence != 4 || event.RoomStateChanged == nil || event.RoomStateChanged.To != StateOffline {
		t.Fatalf("terminal state notification = %#v, want durable receipt sequence 4", event)
	}
}

func TestManagerRetriesInitialProbeAndPersistenceFailuresWithoutLeakingWatcher(t *testing.T) {
	for _, test := range []struct {
		name       string
		probeErr   error
		recordErr  error
		wantProbes int
	}{
		{name: "probe", probeErr: errors.New("probe failed"), wantProbes: 2},
		{name: "persistence", recordErr: errors.New("record failed"), wantProbes: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := &retryProbe{state: ObservedLive, firstErr: test.probeErr}
			repository := &fakeRepository{firstRecordErr: test.recordErr}
			manager, err := NewManager(probe, repository, Options{})
			if err != nil {
				t.Fatal(err)
			}
			references := []Reference{{AccountID: 1, RoomID: "7"}}
			if err := manager.SetReferences(context.Background(), references); err == nil {
				t.Fatal("first SetReferences error = nil, want probe or persistence failure")
			}
			if got := len(manager.watchers); got != 0 {
				t.Fatalf("watchers after failed initial admission = %d, want 0", got)
			}
			if err := manager.SetReferences(context.Background(), references); err != nil {
				t.Fatalf("retry SetReferences: %v", err)
			}
			if probe.calls != test.wantProbes || len(manager.watchers) != 1 {
				t.Fatalf("probe calls/watchers = %d/%d, want %d/1", probe.calls, len(manager.watchers), test.wantProbes)
			}
		})
	}
}

func TestManagerNotifiesAndReplaysEveryDurableEventWithPausedConsumer(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedOffline}, repository, Options{})
	if err != nil {
		t.Fatal(err)
	}
	references := make([]Reference, 129)
	for index := range references {
		references[index] = Reference{AccountID: int64(index + 1), RoomID: integer(index + 1)}
	}
	done := make(chan error, 1)
	go func() { done <- manager.SetReferences(context.Background(), references) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetReferences: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SetReferences blocked while its transition consumer was paused")
	}
	select {
	case event := <-manager.Events():
		if event.Sequence != 1 || event.RoomReferencesChanged == nil {
			t.Fatalf("first notification = %#v, want durable reference sequence 1", event)
		}
	case <-time.After(time.Second):
		t.Fatal("paused consumer did not receive a bounded replay notification")
	}
	replayed, err := manager.ReplayEvents(context.Background(), 0, 129)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 129 {
		t.Fatalf("replayed events = %d, want 129", len(replayed))
	}
	for index, event := range replayed {
		if want := uint64(index + 1); event.Sequence != want || event.RoomReferencesChanged == nil {
			t.Fatalf("replayed event %d = %#v, want durable reference sequence %d", index, event, want)
		}
	}
}

func TestManagerKeepsNotificationOrderDuringForcedInterleavedReferenceUpdates(t *testing.T) {
	notifyStarted := make(chan struct{})
	releaseNotify := make(chan struct{})
	secondNotifyStarted := make(chan struct{})
	releaseSecondNotify := make(chan struct{})
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedOffline}, repository, Options{beforeNotify: func(event Event) {
		if event.Sequence == 1 {
			close(notifyStarted)
			<-releaseNotify
		}
		if event.Sequence == 2 {
			close(secondNotifyStarted)
			<-releaseSecondNotify
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "1"}})
	}()
	<-notifyStarted
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "1"}, {AccountID: 2, RoomID: "2"}})
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second SetReferences completed before sequence 1 notification: %v", err)
	default:
	}
	close(releaseNotify)
	if err := <-firstDone; err != nil {
		t.Fatalf("first SetReferences: %v", err)
	}
	if event := awaitEvent(t, manager.Events()); event.Sequence != 1 {
		t.Fatalf("first notification sequence = %d, want 1", event.Sequence)
	}
	<-secondNotifyStarted
	close(releaseSecondNotify)
	if err := <-secondDone; err != nil {
		t.Fatalf("second SetReferences: %v", err)
	}
	if event := awaitEvent(t, manager.Events()); event.Sequence != 2 {
		t.Fatalf("second notification sequence = %d, want 2", event.Sequence)
	}
}

func TestManagerCloseDrainsNotificationClosesStreamAndRejectsNewWrites(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedLive}, repository, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "7"}}); err != nil {
		t.Fatal(err)
	}
	manager.Close()
	if err := manager.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if event, ok := <-manager.Events(); !ok || event.Sequence != 1 {
		t.Fatalf("drained event/ok = %#v/%v, want sequence 1/true", event, ok)
	}
	if _, ok := <-manager.Events(); ok {
		t.Fatal("event stream remained open after buffered notification drained")
	}
	if err := manager.SetReferences(context.Background(), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetReferences after Close error = %v, want ErrClosed", err)
	}
	if repository.syncCalls() != 1 {
		t.Fatalf("SyncReferences calls after Close = %d, want 1", repository.syncCalls())
	}
}

func TestManagerRejectsReplayLimitBeforeRepositoryAllocation(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedOffline}, repository, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReplayEvents(context.Background(), 0, int(^uint(0)>>1)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ReplayEvents() error = %v, want ErrInvalidInput", err)
	}
	if repository.replayCalls() != 0 {
		t.Fatalf("ReplayEvents reached repository %d times, want 0", repository.replayCalls())
	}
}

type fakeProbe struct {
	state ObservedState
	err   error
}

type retryProbe struct {
	state    ObservedState
	firstErr error
	calls    int
}

func (probe *retryProbe) Probe(context.Context, string) (ObservedState, error) {
	probe.calls++
	if probe.calls == 1 && probe.firstErr != nil {
		return "", probe.firstErr
	}
	return probe.state, nil
}

func (probe fakeProbe) Probe(context.Context, string) (ObservedState, error) {
	return probe.state, probe.err
}

type fakeRepository struct {
	mu              sync.Mutex
	references      []Reference
	transitions     []Transition
	events          []Event
	firstRecordErr  error
	sequence        uint64
	syncCount       int
	atomicTerminals []Transition
	replayCount     int
	recoverable     []RecoverableRoom
}

func (repository *fakeRepository) SyncReferences(_ context.Context, references []Reference, terminal []Transition) ([]Event, error) {
	repository.mu.Lock()
	snapshots := changedReferenceSnapshots(repository.references, references)
	repository.references = append([]Reference(nil), references...)
	repository.syncCount++
	repository.atomicTerminals = append([]Transition(nil), terminal...)
	persisted := make([]Event, 0, len(snapshots)+len(terminal))
	for _, snapshot := range snapshots {
		repository.sequence++
		copy := snapshot
		copy.AccountIDs = append([]int64(nil), snapshot.AccountIDs...)
		event := Event{Sequence: repository.sequence, RoomReferencesChanged: &copy}
		repository.events = append(repository.events, event)
		persisted = append(persisted, event)
	}
	for _, transition := range terminal {
		repository.sequence++
		transition.LeaseEpoch = 7
		repository.transitions = append(repository.transitions, transition)
		event := Event{Sequence: repository.sequence, RoomStateChanged: &transition}
		repository.events = append(repository.events, event)
		persisted = append(persisted, event)
	}
	repository.mu.Unlock()
	return persisted, nil
}

func (repository *fakeRepository) ReplayEvents(_ context.Context, after uint64, limit int) ([]Event, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.replayCount++
	result := make([]Event, 0, min(limit, len(repository.events)))
	for _, event := range repository.events {
		if event.Sequence > after {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (repository *fakeRepository) LoadRecoverable(context.Context) ([]RecoverableRoom, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]RecoverableRoom(nil), repository.recoverable...), nil
}

func (repository *fakeRepository) RecordTransition(_ context.Context, transition Transition) (Event, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.firstRecordErr != nil {
		err := repository.firstRecordErr
		repository.firstRecordErr = nil
		return Event{}, err
	}
	repository.sequence++
	transition.LeaseEpoch = 7
	repository.transitions = append(repository.transitions, transition)
	event := Event{Sequence: repository.sequence, RoomStateChanged: &transition}
	repository.events = append(repository.events, event)
	return event, nil
}

func (repository *fakeRepository) referencesSnapshot() []Reference {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]Reference(nil), repository.references...)
}

func (repository *fakeRepository) syncCalls() int {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.syncCount
}

func (repository *fakeRepository) atomicTerminalsSnapshot() []Transition {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]Transition(nil), repository.atomicTerminals...)
}

func (repository *fakeRepository) replayCalls() int {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.replayCount
}

func integer(value int) string { return strconv.Itoa(value) }

func (repository *fakeRepository) transitionsSnapshot() []Transition {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]Transition(nil), repository.transitions...)
}

func awaitEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for room event notification")
		return Event{}
	}
}
