package roomwatcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// This test fails if shared references create duplicate probes, existing
// watchers are never polled, or a grace recovery opens a second broadcast.
func TestAcceptanceSharedRoomPollingAndGraceRecovery(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	probe := &acceptanceProbe{state: ObservedOffline}
	repository := &fakeRepository{}
	manager, err := NewManager(probe, repository, Options{
		Now:         func() time.Time { return now },
		gracePeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	references := []Reference{{AccountID: 1, RoomID: "7"}, {AccountID: 2, RoomID: "007"}}
	if err := manager.SetReferences(context.Background(), references); err != nil {
		t.Fatal(err)
	}
	if got := probe.callCount(); got != 1 {
		t.Fatalf("initial shared-room probes = %d, want 1", got)
	}

	probe.setState(ObservedLive)
	now = now.Add(time.Minute)
	if err := manager.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe.setState(ObservedOffline)
	now = now.Add(time.Minute)
	if err := manager.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe.setState(ObservedLive)
	now = now.Add(5 * time.Minute)
	if err := manager.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}

	transitions := repository.transitionsSnapshot()
	if len(transitions) != 3 {
		t.Fatalf("transitions = %#v, want live, grace, recovery", transitions)
	}
	if !transitions[0].NewBroadcast || transitions[1].To != StateGrace || transitions[2].To != StateLive || transitions[2].NewBroadcast {
		t.Fatalf("grace recovery split broadcast: %#v", transitions)
	}
	if got := probe.callCount(); got != 4 {
		t.Fatalf("shared-room probes = %d, want one initial plus three polls", got)
	}
}

// This test fails if a failed durable transition mutates the in-memory state
// and makes a retry silently skip the transition.
func TestAcceptancePollRetriesPersistenceWithoutAdvancingState(t *testing.T) {
	probe := &acceptanceProbe{state: ObservedOffline}
	repository := &fakeRepository{}
	manager, err := NewManager(probe, repository, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "7"}}); err != nil {
		t.Fatal(err)
	}
	probe.setState(ObservedLive)
	repository.mu.Lock()
	repository.firstRecordErr = errors.New("durable write failed")
	repository.mu.Unlock()
	if err := manager.Poll(context.Background()); err == nil {
		t.Fatal("first Poll error = nil")
	}
	if err := manager.Poll(context.Background()); err != nil {
		t.Fatalf("retry Poll: %v", err)
	}
	transitions := repository.transitionsSnapshot()
	if len(transitions) != 1 || transitions[0].From != StateOffline || transitions[0].To != StateLive {
		t.Fatalf("retry transitions = %#v, want one offline -> live", transitions)
	}
}

// This test fails if restart restores only runtime.Manager while the watcher
// silently falls back to offline and tries to open a duplicate broadcast.
func TestAcceptanceRestoreBootstrapPreservesWatcherStateBeforePolling(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	probe := &acceptanceProbe{state: ObservedLive}
	repository := &fakeRepository{}
	manager, err := NewManager(probe, repository, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := Bootstrap{Cursor: 9, Rooms: []BootstrapRoom{{
		RoomID: "7", State: StateLive, BroadcastSessionID: 41, LeaseEpoch: 3, AccountIDs: []int64{1, 2},
	}}}
	if err := manager.RestoreBootstrap(bootstrap); err != nil {
		t.Fatalf("RestoreBootstrap() error = %v", err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "7"}, {AccountID: 2, RoomID: "7"}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := repository.transitionsSnapshot(); len(got) != 0 {
		t.Fatalf("restart reopened an already-live broadcast: %#v", got)
	}
	if got := probe.callCount(); got != 1 {
		t.Fatalf("restart probes = %d, want one cadence probe", got)
	}
}

type acceptanceProbe struct {
	mu    sync.Mutex
	state ObservedState
	calls int
}

func (probe *acceptanceProbe) Probe(context.Context, string) (ObservedState, error) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.calls++
	return probe.state, nil
}

func (probe *acceptanceProbe) setState(state ObservedState) {
	probe.mu.Lock()
	probe.state = state
	probe.mu.Unlock()
}

func (probe *acceptanceProbe) callCount() int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.calls
}
