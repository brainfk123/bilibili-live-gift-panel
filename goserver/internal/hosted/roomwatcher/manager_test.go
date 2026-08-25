package roomwatcher

import (
	"context"
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
		if transition.RoomID != "7" || transition.From != StateOffline || transition.To != StateLive || !transition.NewBroadcast {
			t.Fatalf("published transition = %#v, want opening live transition for room 7", transition)
		}
	default:
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

type fakeProbe struct {
	state ObservedState
	err   error
}

func (probe fakeProbe) Probe(context.Context, string) (ObservedState, error) {
	return probe.state, probe.err
}

type fakeRepository struct {
	mu          sync.Mutex
	references  []Reference
	transitions []Transition
}

func (repository *fakeRepository) SyncReferences(_ context.Context, references []Reference) error {
	repository.mu.Lock()
	repository.references = append([]Reference(nil), references...)
	repository.mu.Unlock()
	return nil
}

func (repository *fakeRepository) RecordTransition(_ context.Context, transition Transition) error {
	repository.mu.Lock()
	repository.transitions = append(repository.transitions, transition)
	repository.mu.Unlock()
	return nil
}

func (repository *fakeRepository) referencesSnapshot() []Reference {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]Reference(nil), repository.references...)
}

func (repository *fakeRepository) transitionsSnapshot() []Transition {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]Transition(nil), repository.transitions...)
}
