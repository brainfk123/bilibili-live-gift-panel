package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/roomwatcher"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestWatcherCompositionBootstrapsThenReplaysOnlyAfterCursor(t *testing.T) {
	trace := &lockedTrace{}
	watcher := newFakeRoomWatcher(trace)
	watcher.bootstrap = roomwatcher.Bootstrap{Cursor: 4, Rooms: []roomwatcher.BootstrapRoom{{RoomID: "7", State: roomwatcher.StateOffline, LeaseEpoch: 3, AccountIDs: []int64{1, 2}}}}
	watcher.durable = []roomwatcher.Event{
		referencesEvent(3, "7", 1),
		referencesEvent(5, "7", 1, 2),
		stateEvent(6, "7", roomwatcher.StateOffline, roomwatcher.StateLive, time.Unix(90, 0).UTC()),
	}
	runtime := &fakeRoomRuntime{trace: trace}
	loader := &fakeReferenceLoader{trace: trace, snapshots: [][]roomwatcher.Reference{{{AccountID: 1, RoomID: "7"}, {AccountID: 2, RoomID: "7"}}}}
	composition, err := StartRoomRuntime(context.Background(), watcher, runtime, loader, RoomRuntimeOptions{
		ProbeInterval: time.Minute,
		Now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("StartRoomRuntime() error = %v", err)
	}
	defer shutdownRoomRuntime(t, composition)

	want := []string{"load-bootstrap", "bootstrap:4", "restore-watcher:4", "replay:4", "apply:5", "apply:6", "poll", "load-references", "set-references"}
	if got := trace.snapshot(); !slices.Equal(got[:min(len(got), len(want))], want) || len(got) < len(want) {
		t.Fatalf("startup trace = %#v, want prefix %#v", got, want)
	}
	if got := runtime.eventSequences(); !slices.Equal(got, []uint64{5, 6}) {
		t.Fatalf("applied sequences = %#v, want only cursor-after events", got)
	}
	if status := composition.Status(); status.WatchedRooms != 1 {
		t.Fatalf("startup status = %#v", status)
	}
}

func TestWatcherCompositionRetriesFailedEventWithoutAdvancingCursor(t *testing.T) {
	watcher := newFakeRoomWatcher(nil)
	watcher.bootstrap = roomwatcher.Bootstrap{Cursor: 10}
	runtime := &fakeRoomRuntime{failSequence: 11, failRemaining: 1}
	retry := make(chan time.Time, 1)
	composition, err := StartRoomRuntime(context.Background(), watcher, runtime, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{
		ProbeInterval: time.Minute,
		retryAfter:    func(time.Duration) <-chan time.Time { return retry },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRoomRuntime(t, composition)
	watcher.clearReplayCalls()
	watcher.appendEvent(referencesEvent(11, "7", 1))
	eventually(t, func() bool { return runtime.attemptsFor(11) == 1 })
	if status := composition.Status(); status.TransitionFailures != 1 {
		t.Fatalf("status after failed apply = %#v, want one failure", status)
	}
	retry <- time.Now()
	deadline := time.Now().Add(time.Second)
	for !slices.Equal(runtime.eventSequences(), []uint64{11}) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !slices.Equal(runtime.eventSequences(), []uint64{11}) {
		t.Fatalf("retry status=%#v replay calls=%#v attempts=%d", composition.Status(), watcher.replayCallsSnapshot(), runtime.attemptsFor(11))
	}
	if got := watcher.replayCallsSnapshot(); len(got) != 2 || got[0] != 10 || got[1] != 10 {
		t.Fatalf("retry replay cursors = %#v, want the failed cursor 10 twice", got)
	}
}

func TestWatcherCompositionPollsThenReloadsLiveReferencesOnCadence(t *testing.T) {
	trace := &lockedTrace{}
	watcher := newFakeRoomWatcher(trace)
	watcher.bootstrap = roomwatcher.Bootstrap{Rooms: []roomwatcher.BootstrapRoom{{RoomID: "7", State: roomwatcher.StateOffline, LeaseEpoch: 1, AccountIDs: []int64{1}}}}
	ticks := make(chan time.Time, 1)
	loader := &fakeReferenceLoader{trace: trace, snapshots: [][]roomwatcher.Reference{
		{{AccountID: 1, RoomID: "7"}, {AccountID: 2, RoomID: "7"}},
		{{AccountID: 2, RoomID: "7"}, {AccountID: 3, RoomID: "8"}},
	}}
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, loader, RoomRuntimeOptions{
		ProbeInterval: time.Minute,
		newTicker: func(time.Duration) roomRuntimeTicker {
			return &fakeRoomRuntimeTicker{ticks: ticks}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRoomRuntime(t, composition)
	trace.clear()
	ticks <- time.Now()
	eventually(t, func() bool {
		return slices.Equal(watcher.referencesSnapshot(), []roomwatcher.Reference{{AccountID: 2, RoomID: "7"}, {AccountID: 3, RoomID: "8"}})
	})
	if got := trace.snapshot(); len(got) < 3 || !slices.Equal(got[:3], []string{"poll", "load-references", "set-references"}) {
		t.Fatalf("cadence trace = %#v", got)
	}
	if got := watcher.referencesSnapshot(); !slices.Equal(got, []roomwatcher.Reference{{AccountID: 2, RoomID: "7"}, {AccountID: 3, RoomID: "8"}}) {
		t.Fatalf("reloaded references = %#v", got)
	}
	if status := composition.Status(); status.WatchedRooms != 2 {
		t.Fatalf("watched room aggregate = %#v", status)
	}
}

// This test fails if administrator room changes must wait for the cadence, or
// if a failed durable reference sync is hidden instead of remaining retryable.
func TestWatcherCompositionRefreshesReferencesImmediatelyAndPropagatesRetry(t *testing.T) {
	watcher := newFakeRoomWatcher(nil)
	loader := &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{
		{{AccountID: 7, RoomID: "42"}},
		{{AccountID: 7, RoomID: "84"}},
	}}
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, loader, RoomRuntimeOptions{ProbeInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRoomRuntime(t, composition)

	watcher.failNextReferenceSet()
	if err := composition.RefreshReferences(context.Background()); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("first RefreshReferences error = %v, want unavailable", err)
	}
	if got := watcher.referencesSnapshot(); !slices.Equal(got, []roomwatcher.Reference{{AccountID: 7, RoomID: "42"}}) {
		t.Fatalf("failed refresh changed watcher references: %#v", got)
	}
	if err := composition.RefreshReferences(context.Background()); err != nil {
		t.Fatalf("retry RefreshReferences: %v", err)
	}
	if got := watcher.referencesSnapshot(); !slices.Equal(got, []roomwatcher.Reference{{AccountID: 7, RoomID: "84"}}) {
		t.Fatalf("immediate refreshed references = %#v", got)
	}
}

func TestWatcherCompositionStartsDegradedAndRetriesInitialProbeFailure(t *testing.T) {
	watcher := newFakeRoomWatcher(nil)
	watcher.setReferenceFailures = 1
	ticks := make(chan time.Time, 1)
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{{AccountID: 1, RoomID: "7"}}}}, RoomRuntimeOptions{
		ProbeInterval: time.Minute,
		newTicker: func(time.Duration) roomRuntimeTicker {
			return &fakeRoomRuntimeTicker{ticks: ticks}
		},
	})
	if err != nil {
		t.Fatalf("transient initial probe stopped Web/API startup: %v", err)
	}
	defer shutdownRoomRuntime(t, composition)
	if status := composition.Status(); status.TransitionFailures != 1 || status.WatchedRooms != 0 {
		t.Fatalf("degraded startup status = %#v", status)
	}
	ticks <- time.Now()
	eventually(t, func() bool { return composition.Status().WatchedRooms == 1 })
}

func TestWatcherCompositionAggregatesTenThirtySecondReadinessWithoutIdentifiers(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	watcher := newFakeRoomWatcher(nil)
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{
		ProbeInterval: time.Minute,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRoomRuntime(t, composition)
	watcher.appendEvent(stateEvent(1, "7", roomwatcher.StateOffline, roomwatcher.StateLive, now.Add(-5*time.Second)))
	watcher.appendEvent(stateEvent(2, "8", roomwatcher.StateOffline, roomwatcher.StateLive, now.Add(-20*time.Second)))
	watcher.appendEvent(stateEvent(3, "9", roomwatcher.StateOffline, roomwatcher.StateLive, now.Add(-31*time.Second)))
	eventually(t, func() bool { return composition.Status().ReadinessSamples == 3 })
	status := composition.Status()
	if status.ReadinessWithin10 != 1 || status.ReadinessWithin30 != 2 || status.ReadinessOver30 != 1 || !status.ReadinessAlert || status.ReadinessMaximum != 31*time.Second {
		t.Fatalf("readiness status = %#v", status)
	}
	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "roomId") || strings.Contains(strings.ToLower(string(payload)), "viewer") || strings.Contains(string(payload), `"7"`) {
		t.Fatalf("aggregate status exposed an identifier: %s", payload)
	}
}

// This test fails if a 50-room deployment at the fixed 20/min probe budget is
// represented as meeting a 30-second discovery cadence, or if capacity is
// conflated with confirmed-live-to-runtime readiness.
func TestWatcherCompositionExposesProbeCapacityBacklogWithoutFakingReadinessSLO(t *testing.T) {
	watcher := newFakeRoomWatcher(nil)
	watcher.probeCapacity = roomwatcher.ProbeCapacityStatus{CapacityPerMinute: 20, Available: 0, Backlog: 30}
	references := make([]roomwatcher.Reference, 50)
	for index := range references {
		references[index] = roomwatcher.Reference{AccountID: int64(index + 1), RoomID: strconv.Itoa(1001 + index)}
	}
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{references}}, RoomRuntimeOptions{ProbeInterval: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRoomRuntime(t, composition)
	status := composition.Status()
	if status.ProbeCapacityPerMinute != 20 || status.ProbeBacklog != 30 || !status.ProbeCapacityAlert {
		t.Fatalf("probe capacity status = %#v", status)
	}
	if status.ReadinessSamples != 0 || status.ReadinessAlert {
		t.Fatalf("capacity pressure fabricated confirmed-live readiness: %#v", status)
	}
}

func TestWatcherShutdownCancelsAndJoinsPollBeforeClosingStream(t *testing.T) {
	trace := &lockedTrace{}
	watcher := newFakeRoomWatcher(trace)
	watcher.bootstrap = roomwatcher.Bootstrap{Rooms: []roomwatcher.BootstrapRoom{{RoomID: "7", State: roomwatcher.StateOffline, LeaseEpoch: 1, AccountIDs: []int64{1}}}}
	started := make(chan struct{})
	var pollMu sync.Mutex
	pollCalls := 0
	watcher.poll = func(ctx context.Context) error {
		pollMu.Lock()
		pollCalls++
		call := pollCalls
		pollMu.Unlock()
		if call == 1 {
			return nil
		}
		close(started)
		<-ctx.Done()
		trace.add("poll-exit")
		return ctx.Err()
	}
	ticks := make(chan time.Time, 1)
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{
		ProbeInterval: time.Minute,
		newTicker: func(time.Duration) roomRuntimeTicker {
			return &fakeRoomRuntimeTicker{ticks: ticks}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	trace.clear()
	ticks <- time.Now()
	<-started
	shutdownRoomRuntime(t, composition)
	got := trace.snapshot()
	pollExit := slices.Index(got, "poll-exit")
	watcherClose := slices.Index(got, "watcher-close")
	watcherWait := slices.Index(got, "watcher-wait")
	if pollExit < 0 || watcherClose <= pollExit || watcherWait <= watcherClose {
		t.Fatalf("shutdown trace = %#v, want poll exit before producer close before watcher join", got)
	}
}

func TestWatcherShutdownTimeoutDoesNotAbandonLifecycleJoin(t *testing.T) {
	watcher := newFakeRoomWatcher(nil)
	watcher.bootstrap = roomwatcher.Bootstrap{Rooms: []roomwatcher.BootstrapRoom{{RoomID: "7", State: roomwatcher.StateOffline, LeaseEpoch: 1, AccountIDs: []int64{1}}}}
	started := make(chan struct{})
	release := make(chan struct{})
	var pollMu sync.Mutex
	pollCalls := 0
	watcher.poll = func(context.Context) error {
		pollMu.Lock()
		pollCalls++
		call := pollCalls
		pollMu.Unlock()
		if call == 1 {
			return nil
		}
		close(started)
		<-release
		return nil
	}
	ticks := make(chan time.Time, 1)
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{
		ProbeInterval: time.Minute,
		newTicker: func(time.Duration) roomRuntimeTicker {
			return &fakeRoomRuntimeTicker{ticks: ticks}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	<-started
	short, cancelShort := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelShort()
	if err := composition.Shutdown(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short Shutdown error = %v", err)
	}
	close(release)
	joined, cancelJoined := context.WithTimeout(context.Background(), time.Second)
	defer cancelJoined()
	if err := composition.Wait(joined); err != nil {
		t.Fatalf("lifecycle join was abandoned after timeout: %v", err)
	}
}

// This test fails if shutdown cancels the normal consumer without giving a
// durable event written before producer Close a separate final replay budget.
func TestWatcherShutdownAppliesDurableEventAfterStoppingNormalConsumer(t *testing.T) {
	watcher := newBlockedNormalReplayWatcher(newFakeRoomWatcher(nil))
	applied := &fakeRoomRuntime{}
	composition, err := StartRoomRuntime(context.Background(), watcher, applied, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{ProbeInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	watcher.blockNextReplay()
	watcher.appendEvent(referencesEvent(1, "7", 1))
	watcher.waitUntilBlocked(t)
	shutdownRoomRuntime(t, composition)

	if got := applied.eventSequences(); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("events applied through shutdown = %#v, want final durable event 1", got)
	}
}

// This test fails if final replay advances its cursor only after a whole page,
// advances past a failed event, or hides that final ApplyRoomEvent failure.
func TestWatcherShutdownRetainsLastSuccessfulCursorWithinFailedBatch(t *testing.T) {
	watcher := newBlockedNormalReplayWatcher(newFakeRoomWatcher(nil))
	applied := &fakeRoomRuntime{failSequence: 2, failRemaining: 100}
	composition, err := StartRoomRuntime(context.Background(), watcher, applied, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{ProbeInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	watcher.blockNextReplay()
	watcher.appendEvent(referencesEvent(1, "7", 1))
	watcher.appendEvent(referencesEvent(2, "7", 1, 2))
	watcher.waitUntilBlocked(t)
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := composition.Shutdown(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Shutdown() error = %v, want final apply failure", err)
	}
	if err := composition.Wait(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Wait() error = %v, want final apply failure", err)
	}
	composition.mu.Lock()
	cursor := composition.cursor
	composition.mu.Unlock()
	if cursor != 1 {
		t.Fatalf("cursor after event 1 success/event 2 failure = %d, want 1", cursor)
	}
	if got := applied.eventSequences(); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("successfully applied events = %#v, want [1]", got)
	}
	if status := composition.Status(); status.TransitionFailures != 1 {
		t.Fatalf("final failure aggregate = %#v, want one transition failure", status)
	}
}

// This test fails if closing the producer leaves the consumer in its normal
// five-second retry loop forever when final durable replay is permanently
// unavailable, preventing RoomRuntime and the outer lifecycle from joining.
func TestWatcherShutdownJoinsWhenFinalReplayPermanentlyFails(t *testing.T) {
	watcher := &permanentReplayFailureWatcher{
		fakeRoomWatcher: newFakeRoomWatcher(nil),
		closed:          make(chan struct{}),
	}
	composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{ProbeInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelShutdown()
	if err := composition.Shutdown(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Shutdown() with permanent final replay failure = %v, want reported failure", err)
	}
	if err := composition.Wait(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Wait() with permanent final replay failure = %v, want reported failure", err)
	}
	if status := composition.Status(); status.TransitionFailures != 1 {
		t.Fatalf("permanent replay failure aggregate = %#v, want one transition failure", status)
	}
}

func TestWatcherShutdownJoinsWhenFinalApplyPermanentlyFails(t *testing.T) {
	watcher := newBlockedNormalReplayWatcher(newFakeRoomWatcher(nil))
	applied := &fakeRoomRuntime{failSequence: 1, failRemaining: 100}
	composition, err := StartRoomRuntime(context.Background(), watcher, applied, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{ProbeInterval: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	watcher.blockNextReplay()
	watcher.appendEvent(referencesEvent(1, "7", 1))
	watcher.waitUntilBlocked(t)

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelShutdown()
	if err := composition.Shutdown(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Shutdown() with permanent final apply failure = %v, want reported failure", err)
	}
	if err := composition.Wait(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Wait() with permanent final apply failure = %v, want reported failure", err)
	}
	if status := composition.Status(); status.TransitionFailures != 1 {
		t.Fatalf("permanent apply failure aggregate = %#v, want one transition failure", status)
	}
}

func TestWatcherShutdownBoundsBlockedFinalReplayAndApply(t *testing.T) {
	t.Run("ReplayEvents", func(t *testing.T) {
		watcher := newDeadlineReplayWatcher(newFakeRoomWatcher(nil))
		composition, err := StartRoomRuntime(context.Background(), watcher, &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{
			ProbeInterval:     time.Minute,
			FinalDrainTimeout: 20 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		watcher.blockNextReplay()
		watcher.appendEvent(referencesEvent(1, "7", 1))
		watcher.waitUntilBlocked(t)
		assertBoundedFinalDrain(t, composition, watcher.finalStarted)
	})

	t.Run("ApplyRoomEvent", func(t *testing.T) {
		watcher := newCloseSignalingReplayWatcher(newFakeRoomWatcher(nil))
		applied := &blockingFinalApplyRuntime{closed: watcher.closed, started: make(chan struct{})}
		composition, err := StartRoomRuntime(context.Background(), watcher, applied, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{
			ProbeInterval:     time.Minute,
			FinalDrainTimeout: 20 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		watcher.blockNextReplay()
		watcher.appendEvent(referencesEvent(1, "7", 1))
		watcher.waitUntilBlocked(t)
		assertBoundedFinalDrain(t, composition, applied.started)
	})
}

func TestWatcherCompositionRejectsNegativeFinalDrainTimeout(t *testing.T) {
	composition, err := StartRoomRuntime(context.Background(), newFakeRoomWatcher(nil), &fakeRoomRuntime{}, &fakeReferenceLoader{snapshots: [][]roomwatcher.Reference{{}}}, RoomRuntimeOptions{
		ProbeInterval:     time.Minute,
		FinalDrainTimeout: -time.Second,
	})
	if composition != nil {
		shutdownRoomRuntime(t, composition)
	}
	if !errors.Is(err, ErrRoomRuntimeInvalid) {
		t.Fatalf("negative FinalDrainTimeout error = %v, want invalid input", err)
	}
}

func assertBoundedFinalDrain(t *testing.T, composition *RoomRuntime, finalStarted <-chan struct{}) {
	t.Helper()
	startedAt := time.Now()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelShutdown()
	if err := composition.Shutdown(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Shutdown() error = %v, want bounded final drain failure", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 250*time.Millisecond {
		t.Fatalf("bounded final drain took %v, caller budget was 250ms", elapsed)
	}
	select {
	case <-finalStarted:
	default:
		t.Fatal("shutdown returned without executing the final drain")
	}
	if err := composition.Wait(shutdownContext); !errors.Is(err, ErrRoomRuntimeUnavailable) {
		t.Fatalf("RoomRuntime.Wait() error = %v, want persisted final drain failure", err)
	}
}

func TestRoomProbeCadenceEnvironmentIsConfigurableWithConservativeDefault(t *testing.T) {
	t.Setenv("HOSTED_ROOM_PROBE_INTERVAL", "")
	if got, err := RoomProbeIntervalFromEnvironment(); err != nil || got != 30*time.Second {
		t.Fatalf("default probe interval = %v, %v", got, err)
	}
	t.Setenv("HOSTED_ROOM_PROBE_INTERVAL", "45s")
	if got, err := RoomProbeIntervalFromEnvironment(); err != nil || got != 45*time.Second {
		t.Fatalf("configured probe interval = %v, %v", got, err)
	}
	t.Setenv("HOSTED_ROOM_PROBE_INTERVAL", "2s")
	if _, err := RoomProbeIntervalFromEnvironment(); err == nil {
		t.Fatal("risk-aggressive probe interval was accepted")
	}
}

func TestSQLRoomReferenceLoaderReadsOnlyEnabledAccountsWithValidTargets(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectQuery("SELECT a.id, r.room_id FROM streamer_accounts AS a JOIN account_runtime_rooms AS r ON r.account_id = a.id WHERE a.disabled_at IS NULL ORDER BY r.room_id, a.id").
		WillReturnRows(sqlmock.NewRows([]string{"id", "room_id"}).AddRow(2, "7").AddRow(3, "8"))
	loader := NewSQLRoomReferenceLoader(database)
	got, err := loader.LoadEnabledRoomReferences(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []roomwatcher.Reference{{AccountID: 2, RoomID: "7"}, {AccountID: 3, RoomID: "8"}}
	if !slices.Equal(got, want) {
		t.Fatalf("references = %#v, want %#v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type lockedTrace struct {
	mu     sync.Mutex
	values []string
}

func (trace *lockedTrace) add(value string) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	trace.values = append(trace.values, value)
	trace.mu.Unlock()
}
func (trace *lockedTrace) snapshot() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.values...)
}
func (trace *lockedTrace) clear() {
	trace.mu.Lock()
	trace.values = nil
	trace.mu.Unlock()
}

type fakeRoomWatcher struct {
	mu                   sync.Mutex
	trace                *lockedTrace
	bootstrap            roomwatcher.Bootstrap
	durable              []roomwatcher.Event
	replays              []uint64
	refs                 []roomwatcher.Reference
	setReferenceFailures int
	poll                 func(context.Context) error
	events               chan roomwatcher.Event
	done                 chan struct{}
	closeOnce            sync.Once
	probeCapacity        roomwatcher.ProbeCapacityStatus
}

type permanentReplayFailureWatcher struct {
	*fakeRoomWatcher
	closed    chan struct{}
	closeOnce sync.Once
}

type blockedNormalReplayWatcher struct {
	*fakeRoomWatcher
	blockMu sync.Mutex
	block   bool
	started chan struct{}
}

type closeSignalingReplayWatcher struct {
	*blockedNormalReplayWatcher
	closed     chan struct{}
	signalOnce sync.Once
}

type deadlineReplayWatcher struct {
	*closeSignalingReplayWatcher
	finalStarted chan struct{}
	finalOnce    sync.Once
}

type blockingFinalApplyRuntime struct {
	closed  <-chan struct{}
	started chan struct{}
	once    sync.Once
}

func newBlockedNormalReplayWatcher(watcher *fakeRoomWatcher) *blockedNormalReplayWatcher {
	return &blockedNormalReplayWatcher{fakeRoomWatcher: watcher}
}

func newCloseSignalingReplayWatcher(watcher *fakeRoomWatcher) *closeSignalingReplayWatcher {
	return &closeSignalingReplayWatcher{blockedNormalReplayWatcher: newBlockedNormalReplayWatcher(watcher), closed: make(chan struct{})}
}

func newDeadlineReplayWatcher(watcher *fakeRoomWatcher) *deadlineReplayWatcher {
	return &deadlineReplayWatcher{closeSignalingReplayWatcher: newCloseSignalingReplayWatcher(watcher), finalStarted: make(chan struct{})}
}

func (watcher *blockedNormalReplayWatcher) blockNextReplay() {
	watcher.blockMu.Lock()
	watcher.block = true
	watcher.started = make(chan struct{})
	watcher.blockMu.Unlock()
}

func (watcher *blockedNormalReplayWatcher) ReplayEvents(ctx context.Context, after uint64, limit int) ([]roomwatcher.Event, error) {
	watcher.blockMu.Lock()
	if watcher.block {
		watcher.block = false
		started := watcher.started
		watcher.blockMu.Unlock()
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	watcher.blockMu.Unlock()
	return watcher.fakeRoomWatcher.ReplayEvents(ctx, after, limit)
}

func (watcher *blockedNormalReplayWatcher) waitUntilBlocked(t *testing.T) {
	t.Helper()
	watcher.blockMu.Lock()
	started := watcher.started
	watcher.blockMu.Unlock()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("normal replay did not block before shutdown")
	}
}

func (watcher *closeSignalingReplayWatcher) Close() {
	watcher.signalOnce.Do(func() { close(watcher.closed) })
	watcher.fakeRoomWatcher.Close()
}

func (watcher *deadlineReplayWatcher) ReplayEvents(ctx context.Context, after uint64, limit int) ([]roomwatcher.Event, error) {
	select {
	case <-watcher.closed:
		watcher.finalOnce.Do(func() { close(watcher.finalStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	default:
		return watcher.blockedNormalReplayWatcher.ReplayEvents(ctx, after, limit)
	}
}

func (*blockingFinalApplyRuntime) BootstrapRoomProjection(context.Context, roomwatcher.Bootstrap) error {
	return nil
}

func (runtime *blockingFinalApplyRuntime) ApplyRoomEvent(ctx context.Context, _ roomwatcher.Event) error {
	select {
	case <-runtime.closed:
		runtime.once.Do(func() { close(runtime.started) })
		<-ctx.Done()
		return ctx.Err()
	default:
		return nil
	}
}

func (watcher *permanentReplayFailureWatcher) ReplayEvents(ctx context.Context, after uint64, limit int) ([]roomwatcher.Event, error) {
	select {
	case <-watcher.closed:
		return nil, errors.New("permanent final replay failure")
	default:
		return watcher.fakeRoomWatcher.ReplayEvents(ctx, after, limit)
	}
}

func (watcher *permanentReplayFailureWatcher) Close() {
	watcher.closeOnce.Do(func() { close(watcher.closed) })
	watcher.fakeRoomWatcher.Close()
}

func (watcher *fakeRoomWatcher) ProbeCapacity() roomwatcher.ProbeCapacityStatus {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return watcher.probeCapacity
}

func newFakeRoomWatcher(trace *lockedTrace) *fakeRoomWatcher {
	return &fakeRoomWatcher{trace: trace, events: make(chan roomwatcher.Event, 1), done: make(chan struct{})}
}
func (watcher *fakeRoomWatcher) LoadBootstrap(context.Context) (roomwatcher.Bootstrap, error) {
	watcher.trace.add("load-bootstrap")
	return watcher.bootstrap, nil
}
func (watcher *fakeRoomWatcher) RestoreBootstrap(bootstrap roomwatcher.Bootstrap) error {
	watcher.trace.add("restore-watcher:" + integerString(bootstrap.Cursor))
	return nil
}
func (watcher *fakeRoomWatcher) ReplayEvents(_ context.Context, after uint64, limit int) ([]roomwatcher.Event, error) {
	watcher.trace.add("replay:" + integerString(after))
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	watcher.replays = append(watcher.replays, after)
	result := make([]roomwatcher.Event, 0, limit)
	for _, event := range watcher.durable {
		if event.Sequence > after {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
func (watcher *fakeRoomWatcher) SetReferences(_ context.Context, refs []roomwatcher.Reference) error {
	watcher.trace.add("set-references")
	watcher.mu.Lock()
	if watcher.setReferenceFailures > 0 {
		watcher.setReferenceFailures--
		watcher.mu.Unlock()
		return errors.New("initial probe unavailable")
	}
	watcher.refs = append([]roomwatcher.Reference(nil), refs...)
	watcher.mu.Unlock()
	return nil
}
func (watcher *fakeRoomWatcher) Poll(ctx context.Context) error {
	watcher.trace.add("poll")
	if watcher.poll != nil {
		return watcher.poll(ctx)
	}
	return nil
}
func (watcher *fakeRoomWatcher) Events() <-chan roomwatcher.Event { return watcher.events }
func (watcher *fakeRoomWatcher) Close() {
	watcher.closeOnce.Do(func() {
		watcher.trace.add("watcher-close")
		close(watcher.events)
		close(watcher.done)
	})
}
func (watcher *fakeRoomWatcher) Wait(ctx context.Context) error {
	watcher.trace.add("watcher-wait")
	select {
	case <-watcher.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (watcher *fakeRoomWatcher) appendEvent(event roomwatcher.Event) {
	watcher.mu.Lock()
	watcher.durable = append(watcher.durable, event)
	watcher.mu.Unlock()
	select {
	case watcher.events <- event:
	default:
	}
}
func (watcher *fakeRoomWatcher) clearReplayCalls() {
	watcher.mu.Lock()
	watcher.replays = nil
	watcher.mu.Unlock()
}
func (watcher *fakeRoomWatcher) replayCallsSnapshot() []uint64 {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return append([]uint64(nil), watcher.replays...)
}
func (watcher *fakeRoomWatcher) referencesSnapshot() []roomwatcher.Reference {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return append([]roomwatcher.Reference(nil), watcher.refs...)
}

func (watcher *fakeRoomWatcher) failNextReferenceSet() {
	watcher.mu.Lock()
	watcher.setReferenceFailures++
	watcher.mu.Unlock()
}

type fakeRoomRuntime struct {
	mu            sync.Mutex
	trace         *lockedTrace
	events        []uint64
	attempts      map[uint64]int
	failSequence  uint64
	failRemaining int
}

func (runtime *fakeRoomRuntime) BootstrapRoomProjection(_ context.Context, bootstrap roomwatcher.Bootstrap) error {
	runtime.trace.add("bootstrap:" + integerString(bootstrap.Cursor))
	return nil
}
func (runtime *fakeRoomRuntime) ApplyRoomEvent(_ context.Context, event roomwatcher.Event) error {
	runtime.trace.add("apply:" + integerString(event.Sequence))
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.attempts == nil {
		runtime.attempts = make(map[uint64]int)
	}
	runtime.attempts[event.Sequence]++
	if event.Sequence == runtime.failSequence && runtime.failRemaining > 0 {
		runtime.failRemaining--
		return errors.New("runtime apply failed")
	}
	runtime.events = append(runtime.events, event.Sequence)
	return nil
}
func (runtime *fakeRoomRuntime) eventSequences() []uint64 {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return append([]uint64(nil), runtime.events...)
}
func (runtime *fakeRoomRuntime) attemptsFor(sequence uint64) int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.attempts[sequence]
}

type fakeReferenceLoader struct {
	mu        sync.Mutex
	trace     *lockedTrace
	snapshots [][]roomwatcher.Reference
	calls     int
}

func (loader *fakeReferenceLoader) LoadEnabledRoomReferences(context.Context) ([]roomwatcher.Reference, error) {
	loader.trace.add("load-references")
	loader.mu.Lock()
	defer loader.mu.Unlock()
	index := min(loader.calls, len(loader.snapshots)-1)
	loader.calls++
	if index < 0 {
		return nil, nil
	}
	return append([]roomwatcher.Reference(nil), loader.snapshots[index]...), nil
}

type fakeRoomRuntimeTicker struct{ ticks <-chan time.Time }

func (ticker *fakeRoomRuntimeTicker) C() <-chan time.Time { return ticker.ticks }
func (*fakeRoomRuntimeTicker) Stop()                      {}

func referencesEvent(sequence uint64, roomID string, accountIDs ...int64) roomwatcher.Event {
	return roomwatcher.Event{Sequence: sequence, RoomReferencesChanged: &roomwatcher.RoomReferencesChanged{RoomID: roomID, AccountIDs: accountIDs}}
}
func stateEvent(sequence uint64, roomID string, from, to roomwatcher.State, confirmed time.Time) roomwatcher.Event {
	return roomwatcher.Event{Sequence: sequence, RoomStateChanged: &roomwatcher.Transition{RoomID: roomID, From: from, To: to, ConfirmedAt: confirmed, NewBroadcast: from == roomwatcher.StateOffline && to == roomwatcher.StateLive, LeaseEpoch: sequence}}
}
func integerString(value uint64) string {
	return strconv.FormatUint(value, 10)
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
func shutdownRoomRuntime(t *testing.T, composition *RoomRuntime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := composition.Shutdown(ctx); err != nil {
		t.Fatalf("RoomRuntime.Shutdown() error = %v", err)
	}
	if err := composition.Wait(ctx); err != nil {
		t.Fatalf("RoomRuntime.Wait() error = %v", err)
	}
}

type fakeHealth struct {
	err error
}

func (health fakeHealth) Health(context.Context) error {
	return health.err
}

func TestHealthDoesNotExposeConfiguration(t *testing.T) {
	handler := New(Dependencies{DB: fakeHealth{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q, want minimal status", got)
	}
	for _, forbidden := range []string{"MYSQL", "DSN", "KEY", "configuration"} {
		if strings.Contains(strings.ToUpper(response.Body.String()), forbidden) {
			t.Fatalf("health response exposed %q: %q", forbidden, response.Body.String())
		}
	}
}

func TestHealthReturnsServiceUnavailableWhenDatabaseFails(t *testing.T) {
	handler := New(Dependencies{DB: fakeHealth{err: errors.New("database-secret-details")}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got := response.Body.String(); got != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("body = %q, want minimal unavailable status", got)
	}
	if strings.Contains(response.Body.String(), "database-secret-details") {
		t.Fatalf("health response exposed database error: %q", response.Body.String())
	}
}

func TestBootstrapReturnsOnlyRuntimeCSRFAndRejectsQueries(t *testing.T) {
	handler := New(Dependencies{DB: fakeHealth{}, CSRFToken: "runtime-csrf"})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Body.String(); got != "{\"csrfToken\":\"runtime-csrf\"}\n" {
		t.Fatalf("body = %q", got)
	}

	for _, target := range []string{"/api/bootstrap?extra=1", "/api/bootstrap?x;y"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400", target, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q", target, got)
		}
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(`{}`)))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("POST Cache-Control = %q", got)
	}
	if strings.Contains(response.Body.String(), "runtime-csrf") {
		t.Fatalf("POST exposed bootstrap value: %q", response.Body.String())
	}
}

func TestConfigurationMethodRoutesWinOverBroaderPrefixes(t *testing.T) {
	configuration := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	auth := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: auth, Configuration: configuration})

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/configuration"},
		{http.MethodPut, "/api/configuration/definition"},
		{http.MethodPut, "/api/configuration/state"},
		{http.MethodPut, "/api/configuration/room-suggestion"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want configuration handler", route.method, route.path, response.Code)
		}
	}
}

func TestMigrationMethodRoutesWinOverBroaderAuthenticationPrefix(t *testing.T) {
	migration := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	auth := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: auth, Migration: migration})
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/migrations/preview"},
		{http.MethodGet, "/api/migrations"},
		{http.MethodPut, "/api/migrations/9/selection"},
		{http.MethodPost, "/api/migrations/9/apply"},
		{http.MethodDelete, "/api/migrations/9"},
		{http.MethodPost, "/api/migrations/9/rollback"},
		{http.MethodPost, "/api/migrations/9/obs-links"},
		{http.MethodGet, "/api/migrations/9"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want migration handler", route.method, route.path, response.Code)
		}
	}
}

func TestBiliServiceRoutesWinOverBroaderAdministratorPrefix(t *testing.T) {
	biliService := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	administrator := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Admin: administrator, BiliService: biliService})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/bili-service/status"},
		{http.MethodPost, "/api/admin/bili-service/challenge"},
		{http.MethodGet, "/api/admin/bili-service/challenge/proof"},
		{http.MethodPost, "/api/admin/bili-service/replace"},
		{http.MethodPost, "/api/admin/bili-service/check"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want Bili service handler", route.method, route.path, response.Code)
		}
	}
}

func TestAdministratorConsoleQueriesWinOverBroadAccountHandler(t *testing.T) {
	console := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	broad := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: broad, Admin: broad, AdminConsole: console})
	for _, path := range []string{"/api/admin/overview", "/api/admin/accounts", "/api/admin/accounts/41"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("GET %s status=%d, want administrator console", path, response.Code)
		}
	}
	for _, route := range []struct{ method, path string }{{http.MethodPost, "/api/admin/accounts/batch"}, {http.MethodPut, "/api/admin/accounts/41/room"}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want administrator console", route.method, route.path, response.Code)
		}
	}
}

func TestSessionInventoryRoutesStayInsideAdministratorSettings(t *testing.T) {
	allowed := map[string]string{
		"/api/admin/sessions": http.MethodGet,
		"/api/admin/sessions/00112233445566778899aabbccddeeff": http.MethodDelete,
		"/api/admin/login-events":                              http.MethodGet,
	}
	settings := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != allowed[request.URL.Path] {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusTeapot)
	})
	broad := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: broad, Admin: broad, Invitation: broad, AdminSettings: settings})
	for path, method := range allowed {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want administrator settings", method, path, response.Code)
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/sessions"},
		{http.MethodPost, "/api/admin/sessions/00112233445566778899aabbccddeeff"},
		{http.MethodDelete, "/api/admin/login-events"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status=%d, want 405 from administrator settings", route.method, route.path, response.Code)
		}
	}
}

func TestEveryMethodForExactBiliServicePathsStaysOutOfBroadAdministratorHandler(t *testing.T) {
	allowed := map[string]string{
		"/api/admin/bili-service/status":          http.MethodGet,
		"/api/admin/bili-service/challenge":       http.MethodPost,
		"/api/admin/bili-service/challenge/proof": http.MethodGet,
		"/api/admin/bili-service/replace":         http.MethodPost,
		"/api/admin/bili-service/check":           http.MethodPost,
	}
	biliService := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != allowed[request.URL.Path] {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusTeapot)
	})
	administrator := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	})
	handler := New(Dependencies{DB: fakeHealth{}, Admin: administrator, BiliService: biliService})

	for path, allowedMethod := range allowed {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodHead, "BREW"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			want := http.StatusMethodNotAllowed
			if method == allowedMethod {
				want = http.StatusTeapot
			}
			if response.Code != want {
				t.Fatalf("%s %s status=%d, want %d from Bili service handler", method, path, response.Code, want)
			}
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/bili-service/challenge/proof/extra", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("deeper challenge path status=%d, want broader administrator handler", response.Code)
	}
}

func TestRuntimeRoutesAreExactAndExposeNoStartOrStop(t *testing.T) {
	runtimeHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		allowed := map[string]string{
			"/api/runtime/room":   http.MethodPut,
			"/api/runtime/events": http.MethodGet,
			"/api/runtime/status": http.MethodGet,
		}
		if request.Method != allowed[request.URL.Path] {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusTeapot)
	})
	auth := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: auth, Runtime: runtimeHandler})
	for _, route := range []struct{ method, path string }{
		{http.MethodPut, "/api/runtime/room"},
		{http.MethodGet, "/api/runtime/events"},
		{http.MethodGet, "/api/runtime/status"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status = %d, want runtime handler", route.method, route.path, response.Code)
		}
	}
	for _, path := range []string{"/api/runtime/start", "/api/runtime/stop"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want no route", path, response.Code)
		}
	}
}

func TestOBSOwnsEveryMethodOnCredentialExchangeAndEventPaths(t *testing.T) {
	obsHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	broad := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: broad, Admin: broad, OBS: obsHandler})
	paths := []string{
		"/api/admin/accounts/41/obs-credential",
		"/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/exchange",
		"/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/events",
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusTeapot {
				t.Fatalf("%s %s status=%d, want OBS handler", method, path, response.Code)
			}
		}
	}
}

func TestStaticHandlerFailsFastWhenManifestAssetsAreMissing(t *testing.T) {
	root := writeStaticFixture(t)
	if err := os.Remove(filepath.Join(root, "assets", "obs.css")); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStaticHandler(root); err == nil {
		t.Fatal("NewStaticHandler() accepted a manifest with a missing asset")
	}
}

func TestStaticRoutesServeOnlyHostedPagesAndManifestAssets(t *testing.T) {
	staticHandler, err := NewStaticHandler(writeStaticFixture(t))
	if err != nil {
		t.Fatalf("NewStaticHandler() error = %v", err)
	}
	obsHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	})
	handler := New(Dependencies{DB: fakeHealth{}, OBS: obsHandler, Static: staticHandler})
	publicID := strings.Repeat("A", 43)
	for _, target := range []string{
		"/obs/" + publicID + "/",
		"/obs/" + publicID + "/?theme=glass",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || response.Body.String() != "obs-page" {
			t.Fatalf("GET %s = (%d, %q), want trailing-slash OBS page", target, response.Code, response.Body.String())
		}
	}
	for _, theme := range []string{"minimal", "glass", "rpg", "pixel", "neon", "kawaii"} {
		response := httptest.NewRecorder()
		target := "/obs/" + publicID + "?theme=" + theme
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || response.Body.String() != "obs-page" {
			t.Fatalf("GET %s = (%d, %q), want themed OBS page", target, response.Code, response.Body.String())
		}
	}

	for _, test := range []struct {
		method, target string
		want           int
		body           string
	}{
		{http.MethodGet, "/", http.StatusOK, "hosted-page"},
		{http.MethodGet, "/hosted.html", http.StatusOK, "hosted-page"},
		{http.MethodHead, "/hosted.html", http.StatusOK, ""},
		{http.MethodGet, "/obs/" + publicID, http.StatusOK, "obs-page"},
		{http.MethodGet, "/assets/hosted.js", http.StatusOK, "hosted-script"},
		{http.MethodGet, "/assets/obs.css", http.StatusOK, "obs-style"},
		{http.MethodGet, "/obs/short", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "/extra", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "/events/", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "/exchange/", http.StatusNotFound, ""},
		{http.MethodGet, "/assets/unlisted.js", http.StatusNotFound, ""},
		{http.MethodGet, "/.vite/manifest.json", http.StatusNotFound, ""},
		{http.MethodGet, "/secret.txt", http.StatusNotFound, ""},
		{http.MethodGet, "/assets/", http.StatusNotFound, ""},
		{http.MethodGet, "/hosted.html?theme=glass", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=unknown", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=glass&theme=neon", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=glass&file=secret.txt", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=%67lass", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=glass%2F..%2Fsecret", http.StatusNotFound, ""},
		{http.MethodPost, "/", http.StatusMethodNotAllowed, ""},
		{http.MethodGet, "/obs/" + publicID + "/events", http.StatusTeapot, ""},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
		if response.Code != test.want || (test.body != "" && response.Body.String() != test.body) {
			t.Fatalf("%s %s = (%d, %q), want (%d, %q)", test.method, test.target, response.Code, response.Body.String(), test.want, test.body)
		}
	}
}

func writeStaticFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"hosted.html":         "hosted-page",
		"obs.html":            "obs-page",
		"assets/hosted.js":    "hosted-script",
		"assets/obs.css":      "obs-style",
		"assets/unlisted.js":  "must-not-serve",
		"secret.txt":          "must-not-serve",
		".vite/manifest.json": `{"hosted.html":{"file":"assets/hosted.js"},"obs.html":{"file":"assets/hosted.js","css":["assets/obs.css"]}}`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
