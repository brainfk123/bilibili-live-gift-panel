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

func TestManagerPublishesEveryDurableTransitionWithoutBlockingReferenceUpdates(t *testing.T) {
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
	for sequence := uint64(1); sequence <= 129; sequence++ {
		select {
		case transition := <-manager.Transitions():
			if transition.Sequence != sequence || transition.LeaseEpoch != 7 {
				t.Fatalf("published durable transition = %#v, want sequence %d and lease epoch 7", transition, sequence)
			}
		case <-time.After(time.Second):
			t.Fatalf("durable transition %d was not published", sequence)
		}
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
	mu             sync.Mutex
	references     []Reference
	transitions    []Transition
	firstRecordErr error
	sequence       uint64
}

func (repository *fakeRepository) SyncReferences(_ context.Context, references []Reference) error {
	repository.mu.Lock()
	repository.references = append([]Reference(nil), references...)
	repository.mu.Unlock()
	return nil
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

func integer(value int) string { return strconv.Itoa(value) }

func (repository *fakeRepository) transitionsSnapshot() []Transition {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]Transition(nil), repository.transitions...)
}
