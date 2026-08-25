package roomwatcher

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

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

func TestManagerRemovesLastReferenceAfterPersistingTerminalState(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedLive}, repository, Options{Now: func() time.Time { return time.Unix(100, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "7"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case transition := <-manager.Transitions():
		if transition.RoomID != "7" || transition.From != StateOffline || transition.To != StateLive || !transition.NewBroadcast || transition.Sequence != 1 || transition.LeaseEpoch != 7 {
			t.Fatalf("published transition = %#v, want opening live transition for room 7", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("opening live transition was not published")
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
	<-manager.Transitions()
	if err := manager.SetReferences(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := repository.atomicTerminalsSnapshot(); len(got) != 1 || got[0].RoomID != "7" || got[0].From != StateLive || got[0].To != StateOffline {
		t.Fatalf("atomic terminal candidates = %#v, want room 7 live -> offline", got)
	}
	if transition := awaitTransition(t, manager.Transitions()); transition.To != StateOffline || transition.Sequence != 2 {
		t.Fatalf("terminal notification = %#v, want durable receipt sequence 2", transition)
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

func TestManagerNotifiesAndReplaysEveryDurableTransitionWithPausedConsumer(t *testing.T) {
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedLive}, repository, Options{})
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
	case transition := <-manager.Transitions():
		if transition.Sequence != 1 || transition.LeaseEpoch != 7 {
			t.Fatalf("first notification = %#v, want durable sequence 1", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("paused consumer did not receive a bounded replay notification")
	}
	replayed, err := manager.ReplayTransitions(context.Background(), 0, 129)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 129 {
		t.Fatalf("replayed transitions = %d, want 129", len(replayed))
	}
	for index, transition := range replayed {
		if want := uint64(index + 1); transition.Sequence != want || transition.LeaseEpoch != 7 {
			t.Fatalf("replayed transition %d = %#v, want durable sequence %d", index, transition, want)
		}
	}
}

func TestManagerKeepsNotificationOrderDuringForcedInterleavedReferenceUpdates(t *testing.T) {
	notifyStarted := make(chan struct{})
	releaseNotify := make(chan struct{})
	secondNotifyStarted := make(chan struct{})
	releaseSecondNotify := make(chan struct{})
	repository := &fakeRepository{}
	manager, err := NewManager(fakeProbe{state: ObservedLive}, repository, Options{beforeNotify: func(transition Transition) {
		if transition.Sequence == 1 {
			close(notifyStarted)
			<-releaseNotify
		}
		if transition.Sequence == 2 {
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
	if transition := awaitTransition(t, manager.Transitions()); transition.Sequence != 1 {
		t.Fatalf("first notification sequence = %d, want 1", transition.Sequence)
	}
	<-secondNotifyStarted
	close(releaseSecondNotify)
	if err := <-secondDone; err != nil {
		t.Fatalf("second SetReferences: %v", err)
	}
	if transition := awaitTransition(t, manager.Transitions()); transition.Sequence != 2 {
		t.Fatalf("second notification sequence = %d, want 2", transition.Sequence)
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
	if transition, ok := <-manager.Transitions(); !ok || transition.Sequence != 1 {
		t.Fatalf("drained transition/ok = %#v/%v, want sequence 1/true", transition, ok)
	}
	if _, ok := <-manager.Transitions(); ok {
		t.Fatal("transition stream remained open after buffered notification drained")
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
	if _, err := manager.ReplayTransitions(context.Background(), 0, int(^uint(0)>>1)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ReplayTransitions() error = %v, want ErrInvalidInput", err)
	}
	if repository.replayCalls() != 0 {
		t.Fatalf("ReplayTransitions reached repository %d times, want 0", repository.replayCalls())
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
	firstRecordErr  error
	sequence        uint64
	syncCount       int
	atomicTerminals []Transition
	replayCount     int
}

func (repository *fakeRepository) SyncReferences(_ context.Context, references []Reference, terminal []Transition) ([]Transition, error) {
	repository.mu.Lock()
	repository.references = append([]Reference(nil), references...)
	repository.syncCount++
	repository.atomicTerminals = append([]Transition(nil), terminal...)
	persisted := make([]Transition, 0, len(terminal))
	for _, transition := range terminal {
		repository.sequence++
		transition.Sequence = repository.sequence
		transition.LeaseEpoch = 7
		repository.transitions = append(repository.transitions, transition)
		persisted = append(persisted, transition)
	}
	repository.mu.Unlock()
	return persisted, nil
}

func (repository *fakeRepository) ReplayTransitions(_ context.Context, after uint64, limit int) ([]Transition, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.replayCount++
	result := make([]Transition, 0, min(limit, len(repository.transitions)))
	for _, transition := range repository.transitions {
		if transition.Sequence > after {
			result = append(result, transition)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (repository *fakeRepository) RecordTransition(_ context.Context, transition Transition) (Transition, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.firstRecordErr != nil {
		err := repository.firstRecordErr
		repository.firstRecordErr = nil
		return Transition{}, err
	}
	repository.sequence++
	transition.Sequence = repository.sequence
	transition.LeaseEpoch = 7
	repository.transitions = append(repository.transitions, transition)
	return transition, nil
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

func awaitTransition(t *testing.T, transitions <-chan Transition) Transition {
	t.Helper()
	select {
	case transition := <-transitions:
		return transition
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transition notification")
		return Transition{}
	}
}
