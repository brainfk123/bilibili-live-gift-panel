package roomwatcher

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// This test fails if every cadence restarts at the first sorted room, if a
// fixed 20-token probe budget permanently starves room 21+, or if a budget
// rejection advances past the unobserved room.
func TestAcceptanceFiftyRoomsRoundRobinAcrossProbeBudgets(t *testing.T) {
	probe := newCadenceBudgetProbe(20)
	manager, err := NewManager(probe, &fakeRepository{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	references := make([]Reference, 50)
	for index := range references {
		references[index] = Reference{AccountID: int64(index + 1), RoomID: strconv.Itoa(1001 + index)}
	}
	if err := manager.SetReferences(context.Background(), references); err != nil {
		t.Fatalf("SetReferences() error = %v", err)
	}
	for cadence := 0; cadence < 3; cadence++ {
		probe.refill(10)
		if err := manager.Poll(context.Background()); err != nil {
			t.Fatalf("Poll cadence %d: %v", cadence+1, err)
		}
	}
	for room := 1001; room <= 1050; room++ {
		if got := probe.callsFor(strconv.Itoa(room)); got == 0 {
			t.Fatalf("room %d was permanently starved; calls=%v", room, probe.snapshot())
		}
	}
	status := manager.ProbeCapacity()
	if status.CapacityPerMinute != 20 || status.Backlog != 0 {
		t.Fatalf("probe capacity status = %#v, want 20/min and no remaining backlog", status)
	}
}

func TestPollRetainsRateLimitedRoomAsNextCursor(t *testing.T) {
	probe := newCadenceBudgetProbe(2)
	probe.rejectRoom = "1002"
	manager, err := NewManager(probe, &fakeRepository{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "1001"}, {AccountID: 2, RoomID: "1002"}, {AccountID: 3, RoomID: "1003"}}); !errors.Is(err, ErrProbeBudgetExhausted) {
		t.Fatalf("SetReferences error = %v, want probe budget exhausted", err)
	}
	probe.rejectRoom = ""
	probe.refill(1)
	if err := manager.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := probe.snapshot(); len(got) < 3 || got[2] != "1002" {
		t.Fatalf("probe order = %v, want rate-limited room 1002 retried first", got)
	}
}

// This test fails if Poll holds Manager.mu across a slow upstream probe and
// prevents a reference snapshot refresh from committing.
func TestReferenceRefreshCompletesWhileCadenceProbeIsBlocked(t *testing.T) {
	probe := newCadenceBudgetProbe(2)
	manager, err := NewManager(probe, &fakeRepository{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "1001"}}); err != nil {
		t.Fatal(err)
	}
	probe.refill(1)
	probe.blockRoom = "1001"
	probe.started = make(chan struct{})
	probe.release = make(chan struct{})
	pollDone := make(chan error, 1)
	go func() { pollDone <- manager.Poll(context.Background()) }()
	<-probe.started
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- manager.SetReferences(context.Background(), []Reference{{AccountID: 1, RoomID: "1001"}, {AccountID: 2, RoomID: "1001"}})
	}()
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SetReferences was starved behind blocked Poll probe")
	}
	close(probe.release)
	if err := <-pollDone; err != nil {
		t.Fatal(err)
	}
}

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

type cadenceBudgetProbe struct {
	mu         sync.Mutex
	capacity   int
	tokens     int
	calls      []string
	rejectRoom string
	blockRoom  string
	started    chan struct{}
	release    chan struct{}
}

func newCadenceBudgetProbe(tokens int) *cadenceBudgetProbe {
	return &cadenceBudgetProbe{capacity: 20, tokens: tokens}
}

func (probe *cadenceBudgetProbe) AvailableProbeBudget() int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.tokens
}

func (probe *cadenceBudgetProbe) ProbeCapacityPerMinute() int { return probe.capacity }

func (probe *cadenceBudgetProbe) Probe(_ context.Context, roomID string) (ObservedState, error) {
	probe.mu.Lock()
	probe.calls = append(probe.calls, roomID)
	if probe.tokens <= 0 || probe.rejectRoom == roomID {
		probe.mu.Unlock()
		return "", ErrProbeBudgetExhausted
	}
	probe.tokens--
	block := probe.blockRoom == roomID
	started, release := probe.started, probe.release
	probe.mu.Unlock()
	if block {
		close(started)
		<-release
	}
	return ObservedOffline, nil
}

func (probe *cadenceBudgetProbe) refill(tokens int) {
	probe.mu.Lock()
	probe.tokens += tokens
	probe.mu.Unlock()
}

func (probe *cadenceBudgetProbe) callsFor(roomID string) int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	count := 0
	for _, called := range probe.calls {
		if called == roomID {
			count++
		}
	}
	return count
}

func (probe *cadenceBudgetProbe) snapshot() []string {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return append([]string(nil), probe.calls...)
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
