package runtime

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

func TestSetRoomClosesAdmissionDrainsThenEndsMigratesPersistsAndStarts(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", pendingJob: 91, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42", "8": "84"})
	migrations := orderedMigration{log: log}
	processingStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: migrations, RoomSources: sources}, Options{
		Now: func() time.Time { return time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC) },
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
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	old := sources.subscription(t, 0)
	old.Emit(roomsource.Event{ID: "gift-1", RoomID: "42"})
	<-processingStarted

	switchDone := make(chan error, 1)
	go func() { switchDone <- manager.SetRoom(context.Background(), 7, "8") }()
	<-old.cancelled
	if got := log.snapshot(); containsOperation(got, "end:1") {
		t.Fatalf("old session ended before admitted event committed: %v", got)
	}
	close(allowCommit)
	if err := <-switchDone; err != nil {
		t.Fatal(err)
	}

	want := []string{"subscribe:42", "start:42", "resolve:84", "cancel:42", "commit", "end:1", "pending:91", "apply:91", "persist:84", "subscribe:84", "start:84"}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("room switch operations = %v\nwant %v", got, want)
	}
	if sources.maximumActive() != 1 {
		t.Fatalf("maximum simultaneous upstream subscriptions = %d, want 1", sources.maximumActive())
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.RoomID != "84" || status.State != StateActive {
		t.Fatalf("Status() = %#v, %v", status, err)
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

func TestFailedSessionEndIsRetriedBeforeAnyReplacementCanStart(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", endFailures: 1, log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42", "8": "84"})
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
	if err := manager.SetRoom(context.Background(), 7, "8"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first SetRoom error = %v", err)
	}
	if status, _ := manager.Status(context.Background(), 7); status.State != StateDegraded || !status.Degraded {
		t.Fatalf("failed switch status = %#v", status)
	}
	if sources.maximumActive() != 1 || len(sources.subs) != 1 {
		t.Fatalf("replacement subscribed after failed end: max=%d subscriptions=%d", sources.maximumActive(), len(sources.subs))
	}
	if err := manager.SetRoom(context.Background(), 7, "8"); err != nil {
		t.Fatalf("retry SetRoom error = %v", err)
	}
	want := []string{"subscribe:42", "start:42", "resolve:84", "cancel:42", "end:1", "resolve:84", "end:1", "persist:84", "subscribe:84", "start:84"}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("retry operations = %v\nwant %v", got, want)
	}
}

func TestRoomSwitchBarrierCompletesAfterCallerCancellation(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "42", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42", "8": "84"})
	processingStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
		close(processingStarted)
		<-allowCommit
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
	defer lease.Release()
	old := sources.subscription(t, 0)
	old.Emit(roomsource.Event{ID: "gift"})
	<-processingStarted
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- manager.SetRoom(ctx, 7, "8") }()
	<-old.cancelled
	cancel()
	select {
	case err := <-firstDone:
		t.Fatalf("SetRoom returned before admitted work drained: %v", err)
	default:
	}
	close(allowCommit)
	if err := <-firstDone; err != nil {
		t.Fatalf("SetRoom after caller cancellation error = %v", err)
	}
}

func TestRuntimeShutdownCancelsManagerOwnedRoomSwitchContext(t *testing.T) {
	log := &operationLog{}
	baseSessions := &orderedSessions{enabled: true, target: "42", log: log}
	sessions := &blockingPendingSessions{orderedSessions: baseSessions, started: make(chan struct{}), release: make(chan struct{})}
	sources := newOrderedRoomSources(log, map[string]string{"7": "42", "8": "84"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	switchDone := make(chan error, 1)
	go func() { switchDone <- manager.SetRoom(context.Background(), 7, "8") }()
	<-sessions.started
	shutdownContext, cancelShutdown := context.WithCancel(context.Background())
	cancelShutdown()
	if err := manager.Shutdown(shutdownContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error = %v", err)
	}
	if err := sessions.contextError(); !errors.Is(err, context.Canceled) {
		close(sessions.release)
		<-switchDone
		_ = manager.Wait(context.Background())
		t.Fatalf("room switch context after runtime shutdown = %v", err)
	}
	if err := <-switchDone; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("room switch error after shutdown = %v", err)
	}
	if err := manager.Wait(context.Background()); err != nil {
		t.Fatal(err)
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
	if sessions.endFailures > 0 {
		sessions.endFailures--
		return ErrUnavailable
	}
	return nil
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
