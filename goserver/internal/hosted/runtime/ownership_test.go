package runtime

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestTwoManagersRespectExpiryTakeoverAndFenceTheOldOwner(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	timersA := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	timersB := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	logA := &operationLog{}
	logB := &operationLog{}
	managerA := newTestManager(t, sessions, newOrderedRoomSources(logA, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xa1), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, NewHeartbeatTimer: timersA.New})
	managerB := newTestManager(t, sessions, newOrderedRoomSources(logB, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xb2), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, NewHeartbeatTimer: timersB.New})
	leaseA, err := managerA.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("foreign Acquire before expiry = %v, want ownership conflict", err)
	}
	if err := managerB.SetRoom(context.Background(), 7, "43"); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("foreign SetRoom before expiry = %v, want ownership conflict", err)
	}
	if sessions.endCount() != 0 {
		t.Fatalf("foreign manager ended owner session %d times", sessions.endCount())
	}
	if got := logB.snapshot(); len(got) != 0 {
		t.Fatalf("foreign manager performed room-source work before ownership: %v", got)
	}

	firstHeartbeat := <-timersA.created
	clock.Advance(20 * time.Second)
	firstHeartbeat.Fire()
	<-sessions.renewed
	clock.Advance(20 * time.Second)
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("foreign Acquire after owner heartbeat = %v, want ownership conflict", err)
	}

	staleHeartbeat := <-timersA.created
	clock.Advance(31 * time.Second)
	leaseB, err := managerB.Acquire(context.Background(), 7, LeaseOBS)
	if err != nil {
		t.Fatalf("Acquire after expiry = %v", err)
	}
	if epoch, reconciles := sessions.ownerEpochAndReconciles(); epoch != 2 || reconciles != 1 {
		t.Fatalf("takeover epoch/reconciles = %d/%d, want 2/1", epoch, reconciles)
	}

	staleHeartbeat.Fire()
	<-sessions.staleRenewed
	if err := managerA.SetRoom(context.Background(), 7, "44"); !errors.Is(err, ErrOwnershipConflict) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("old owner SetRoom after takeover = %v, want ownership conflict or fail-closed unavailable", err)
	}
	if err := leaseA.Renew(context.Background()); !errors.Is(err, ErrInvalidLease) && !errors.Is(err, ErrOwnershipConflict) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("old owner Renew after takeover = %v", err)
	}
	leaseA.Release()
	leaseB.Release()
}

func TestCommittedDisableRacingAcquireCannotIssueLease(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 10, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	manager := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xc3)})
	lease, err := manager.Acquire(context.Background(), 9, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	sessions.disable()
	manager.AccountDisabled(9)
	result := make(chan error, 1)
	go func() {
		_, acquireErr := manager.Acquire(context.Background(), 9, LeaseOBS)
		result <- acquireErr
	}()
	if err := <-result; !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("Acquire racing committed disable = %v, want account disabled", err)
	}
	manager.waitAccountIdle(t, 9)
	status, err := manager.Status(context.Background(), 9)
	if err != nil || status.State != StateDisabled || status.Leases != 0 {
		t.Fatalf("disabled status = %#v, %v", status, err)
	}
	if err := lease.Renew(context.Background()); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("pre-disable lease Renew = %v", err)
	}
}

func TestSetRoomWithoutPresenceClaimsPersistsAndReleasesOwnership(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 20, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	logA := &operationLog{}
	logB := &operationLog{}
	managerA := newTestManager(t, sessions, newOrderedRoomSources(logA, nil), Options{Now: clock.Now, OwnerToken: ownerToken(0xd4)})
	managerB := newTestManager(t, sessions, newOrderedRoomSources(logB, nil), Options{Now: clock.Now, OwnerToken: ownerToken(0xe5)})
	if err := managerA.SetRoom(context.Background(), 7, "42"); err != nil {
		t.Fatal(err)
	}
	if err := managerB.SetRoom(context.Background(), 7, "43"); err != nil {
		t.Fatalf("second manager could not claim immediately after persist-only SetRoom: %v", err)
	}
	if epoch, _ := sessions.ownerEpochAndReconciles(); epoch != 2 {
		t.Fatalf("persist-only ownership epoch = %d, want released takeover epoch 2", epoch)
	}
	for _, item := range append(logA.snapshot(), logB.snapshot()...) {
		if strings.HasPrefix(item, "subscribe:") || strings.HasPrefix(item, "start:") {
			t.Fatalf("persist-only SetRoom opened runtime: %v", item)
		}
	}
}

func TestTakenOverQueuedProcessCannotPersistWithCapturedFence(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 30, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	processingStarted := make(chan struct{})
	allowPersist := make(chan struct{})
	persistResult := make(chan error, 1)
	managerA := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{
		Now: clock.Now, OwnerToken: ownerToken(0xf1), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
		Process: func(_ context.Context, owner OwnerFence, _ Session, _ roomsource.Event) error {
			close(processingStarted)
			<-allowPersist
			err := sessions.persistAggregate(owner)
			persistResult <- err
			return err
		},
	})
	managerB := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xf2), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second})
	if _, err := managerA.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	managerA.dependencies.RoomSources.(*orderedRoomSources).subscription(t, 0).Emit(roomsource.Event{ID: "gift-1", RoomID: "42"})
	<-processingStarted
	clock.Advance(31 * time.Second)
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); err != nil {
		t.Fatal(err)
	}
	close(allowPersist)
	if err := <-persistResult; !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("stale queued persist error = %v, want ownership conflict", err)
	}
}

func TestCommittedSessionCandidateIsNotPublishedAfterFenceConflict(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 32, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	heartbeats := &manualTimerFactory{created: make(chan *manualTimer, 3)}
	startCommitted := make(chan struct{})
	allowPublish := make(chan struct{})
	workerRan := make(chan struct{}, 1)
	sourcesA := newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})
	managerA, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sourcesA}, Options{
		Now: clock.Now, OwnerToken: ownerToken(0xec), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, NewHeartbeatTimer: heartbeats.New,
		BeforeSessionPublish: func() { close(startCommitted); <-allowPublish },
		Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
			workerRan <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	managerB := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xed), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second})
	acquireDone := make(chan error, 1)
	go func() {
		_, acquireErr := managerA.Acquire(context.Background(), 7, LeaseConfig)
		acquireDone <- acquireErr
	}()
	<-startCommitted
	candidate := sourcesA.subscription(t, 0)
	candidate.Emit(roomsource.Event{ID: "never-run", RoomID: "42"})
	clock.Advance(31 * time.Second)
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); err != nil {
		t.Fatalf("foreign takeover after candidate commit = %v", err)
	}
	(<-heartbeats.created).Fire()
	<-sessions.staleRenewed
	account, err := managerA.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		account.mu.Lock()
		stale := account.stale
		account.mu.Unlock()
		if stale {
			break
		}
		select {
		case <-deadline:
			t.Fatal("heartbeat ownership conflict did not mark candidate stale")
		default:
			goruntime.Gosched()
		}
	}
	close(allowPublish)
	if err := <-acquireDone; !errors.Is(err, ErrOwnershipConflict) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("candidate Acquire error = %v, want fail-closed ownership result", err)
	}
	select {
	case <-candidate.cancelled:
	case <-time.After(time.Second):
		t.Fatal("unpublished candidate subscription was not cancelled")
	}
	account.mu.Lock()
	staleDone := account.staleDone
	account.mu.Unlock()
	if staleDone != nil {
		<-staleDone
	}
	status, err := managerA.Status(context.Background(), 7)
	if err != nil || status.SessionID != 0 || status.Leases != 0 {
		t.Fatalf("unpublished candidate status = %#v, %v", status, err)
	}
	select {
	case <-workerRan:
		t.Fatal("unpublished candidate started its worker or processed queued work")
	default:
	}
	if sessions.endCount() != 0 {
		t.Fatalf("unpublished stale candidate attempted EndSession %d times", sessions.endCount())
	}
}

func TestCommittedSessionCandidateIsHandedToShutdownForFencedEnd(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 33, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	startCommitted := make(chan struct{})
	allowPublish := make(chan struct{})
	workerRan := make(chan struct{}, 1)
	sources := newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, Options{
		Now: clock.Now, OwnerToken: ownerToken(0xee),
		BeforeSessionPublish: func() { close(startCommitted); <-allowPublish },
		Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
			workerRan <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	acquireDone := make(chan error, 1)
	go func() {
		_, acquireErr := manager.Acquire(context.Background(), 7, LeaseConfig)
		acquireDone <- acquireErr
	}()
	<-startCommitted
	candidate := sources.subscription(t, 0)
	candidate.Emit(roomsource.Event{ID: "never-run", RoomID: "42"})
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	account, err := manager.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		account.mu.Lock()
		shutting := account.shutting
		account.mu.Unlock()
		if shutting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("shutdown did not mark account while publication was gated")
		default:
			goruntime.Gosched()
		}
	}
	close(allowPublish)
	if err := <-acquireDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("Acquire after committed shutdown handoff = %v, want closed", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerRan:
		t.Fatal("shutdown-only candidate started its worker")
	default:
	}
	sessions.mu.Lock()
	open := sessions.open
	ends := sessions.ends
	ownerActive := clock.Now().Before(sessions.owner.expiresAt)
	sessions.mu.Unlock()
	if open != nil || ends != 1 || ownerActive {
		t.Fatalf("shutdown handoff lifecycle open=%#v ends=%d ownerActive=%v, want nil/1/false", open, ends, ownerActive)
	}
}

func TestShutdownRetriesTransientEndBeforeRelease(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 34, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	sessions.endFailures = 1
	idleTimers := &manualTimerFactory{}
	retryTimers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	manager := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{
		Now: clock.Now, OwnerToken: ownerToken(0xe1), NewTimer: idleTimers.New, NewShutdownTimer: retryTimers.New,
	})
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	grace, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(grace) }()
	var retry *manualTimer
	select {
	case retry = <-retryTimers.created:
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before retrying transient EndSession: %v", err)
	case <-time.After(time.Second):
		t.Fatal("shutdown did not schedule transient EndSession retry")
	}
	if retry.delay != defaultShutdownRetryBackoff {
		t.Fatalf("shutdown retry delay = %v, want %v", retry.delay, defaultShutdownRetryBackoff)
	}
	if idleTimers.Count() != 0 {
		t.Fatalf("shutdown used normal idle-close timer %d times", idleTimers.Count())
	}
	sessions.mu.Lock()
	openBeforeRetry := sessions.open != nil
	ownerActiveBeforeRetry := clock.Now().Before(sessions.owner.expiresAt)
	sessions.mu.Unlock()
	if !openBeforeRetry || !ownerActiveBeforeRetry {
		t.Fatalf("transient EndSession released lifecycle early: open=%v ownerActive=%v", openBeforeRetry, ownerActiveBeforeRetry)
	}
	retry.Fire()
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	open := sessions.open
	ends := sessions.ends
	ownerActive := clock.Now().Before(sessions.owner.expiresAt)
	sessions.mu.Unlock()
	if open != nil || ends != 1 || ownerActive {
		t.Fatalf("retried shutdown lifecycle open=%#v ends=%d ownerActive=%v, want nil/1/false", open, ends, ownerActive)
	}
}

func TestShutdownRetryRequiresBackoffWindowBeforeDeadline(t *testing.T) {
	manager := &Manager{shutdownRetryBackoff: defaultShutdownRetryBackoff}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(defaultShutdownRetryBackoff/2))
	defer cancel()
	if delay, retry := manager.shutdownRetryDelay(ctx); retry || delay != 0 {
		t.Fatalf("shutdown retry at grace boundary = %v/%v, want 0/false", delay, retry)
	}
}

func TestShutdownEndRetryOwnershipConflictReconcilesOnlyCapturedSession(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 35, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	sessions.endFailures = 1
	retryTimers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	manager := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{
		Now: clock.Now, OwnerToken: ownerToken(0xe2), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, NewShutdownTimer: retryTimers.New,
	})
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	var retry *manualTimer
	select {
	case retry = <-retryTimers.created:
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before retry conflict: %v", err)
	case <-time.After(time.Second):
		t.Fatal("shutdown did not schedule EndSession retry")
	}
	clock.Advance(31 * time.Second)
	takeover, err := sessions.ClaimOwnership(context.Background(), 7, ownerToken(0xe3), 30*time.Second)
	if err != nil || !takeover.Reconcile {
		t.Fatalf("takeover claim = %#v, %v", takeover, err)
	}
	retry.Fire()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown ownership-conflict cleanup = %v", err)
	}
	sessions.mu.Lock()
	open := sessions.open
	ends := sessions.ends
	reconciles := sessions.reconciles
	owner := sessions.owner.fence
	sessions.mu.Unlock()
	if open != nil || ends != 0 || reconciles != 1 || owner != takeover.Fence {
		t.Fatalf("stale shutdown reconcile open=%#v ends=%d reconciles=%d owner=%#v, want nil/0/1/%#v", open, ends, reconciles, owner, takeover.Fence)
	}
}

func TestShutdownHandoffHeartbeatConflictDrainsWorkerlessCandidate(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 36, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	var waitReturned atomic.Bool
	sessions.waitReturned = &waitReturned
	sessions.target = "42"
	sessions.endFailures = 1
	startCommitted := make(chan struct{})
	allowPublish := make(chan struct{})
	retryTimers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	heartbeatTimers := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	manager := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{
		Now: clock.Now, OwnerToken: ownerToken(0xe4), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
		NewShutdownTimer: retryTimers.New, NewHeartbeatTimer: heartbeatTimers.New,
		BeforeSessionPublish: func() { close(startCommitted); <-allowPublish },
	})
	acquireDone := make(chan error, 1)
	go func() {
		_, acquireErr := manager.Acquire(context.Background(), 7, LeaseConfig)
		acquireDone <- acquireErr
	}()
	<-startCommitted
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	account, err := manager.accountExisting(7)
	if err != nil {
		t.Fatal(err)
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
	if err := <-acquireDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("handoff Acquire = %v, want closed", err)
	}
	retry := <-retryTimers.created
	clock.Advance(31 * time.Second)
	takeover, err := sessions.ClaimOwnership(context.Background(), 7, ownerToken(0xe5), 30*time.Second)
	if err != nil || !takeover.Reconcile {
		t.Fatalf("takeover claim = %#v, %v", takeover, err)
	}
	firstHeartbeat := <-heartbeatTimers.created
	firstHeartbeat.Fire()
	<-sessions.staleRenewed
	deadline := time.After(time.Second)
	for {
		account.mu.Lock()
		stale := account.stale
		account.mu.Unlock()
		if stale {
			break
		}
		select {
		case <-deadline:
			t.Fatal("heartbeat conflict did not revoke shutdown handoff")
		default:
			goruntime.Gosched()
		}
	}
	retry.Fire()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown after heartbeat conflict = %v", err)
	}
	waitReturned.Store(true)
	account.mu.Lock()
	staleDone := account.staleDone
	current := account.current
	account.mu.Unlock()
	if staleDone != nil {
		t.Fatal("Shutdown returned before workerless stale cleanup was joined")
	}
	if current != nil {
		t.Fatalf("workerless stale cleanup retained current session %#v", current.session)
	}
	sessions.mu.Lock()
	open := sessions.open
	ends := sessions.ends
	reconciles := sessions.reconciles
	owner := sessions.owner.fence
	sessions.mu.Unlock()
	if open != nil || ends != 0 || reconciles != 1 || owner != takeover.Fence {
		t.Fatalf("heartbeat reconcile open=%#v ends=%d reconciles=%d owner=%#v", open, ends, reconciles, owner)
	}
	stoppedHeartbeat := <-heartbeatTimers.created
	if !stoppedHeartbeat.Stopped() {
		t.Fatal("Shutdown returned before the rearmed heartbeat timer was stopped")
	}
	stoppedHeartbeat.Fire()
	for range 100 {
		goruntime.Gosched()
	}
	if late := sessions.lateOperations.Load(); late != 0 {
		t.Fatalf("repository operations started after Shutdown/Wait returned: %d", late)
	}
}

func TestTakenOverPendingMigrationCannotApplyWithCapturedFence(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 35, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.pendingJob = 91
	applier := &fencedPendingMigration{sessions: sessions, started: make(chan struct{}), allow: make(chan struct{})}
	managerA, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: applier, RoomSources: newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})}, Options{
		Now: clock.Now, OwnerToken: ownerToken(0xf3), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	managerB := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, nil), Options{Now: clock.Now, OwnerToken: ownerToken(0xf4), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second})
	setRoomDone := make(chan error, 1)
	go func() { setRoomDone <- managerA.SetRoom(context.Background(), 7, "42") }()
	<-applier.started
	clock.Advance(31 * time.Second)
	leaseB, err := managerB.Acquire(context.Background(), 7, LeaseOBS)
	if err != nil {
		t.Fatalf("takeover Acquire = %v", err)
	}
	close(applier.allow)
	if err := <-setRoomDone; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale pending apply SetRoom error = %v, want unavailable", err)
	}
	if applier.wasApplied() {
		t.Fatal("stale pending migration mutated state after ownership takeover")
	}
	account, err := managerA.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	account.mu.Lock()
	staleDone := account.staleDone
	account.mu.Unlock()
	if staleDone != nil {
		<-staleDone
	}
	status, err := managerA.Status(context.Background(), 7)
	if err != nil || status.Leases != 0 || status.SessionID != 0 || status.Degraded {
		t.Fatalf("pending ownership conflict did not complete stale cleanup: %#v, %v", status, err)
	}
	leaseB.Release()
}

func TestDisableEndOwnershipConflictClearsCurrentAndAllowsLaterReenable(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 38, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	processingStarted := make(chan struct{})
	allowDrain := make(chan struct{})
	sources := newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})
	manager := newTestManager(t, sessions, sources, Options{
		Now: clock.Now, OwnerToken: ownerToken(0xee), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
		Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
			close(processingStarted)
			<-allowDrain
			return nil
		},
	})
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	sources.subscription(t, 0).Emit(roomsource.Event{ID: "disable-drain", RoomID: "42"})
	<-processingStarted
	sessions.disable()
	manager.AccountDisabled(7)
	<-sources.subscription(t, 0).cancelled
	clock.Advance(31 * time.Second)
	close(allowDrain)
	manager.waitAccountIdle(t, 7)
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.State != StateDisabled || status.SessionID != 0 || status.Leases != 0 {
		t.Fatalf("disabled stale-End status = %#v, %v", status, err)
	}
	if sessions.endCount() != 0 {
		t.Fatalf("expired disable fence ended session %d times", sessions.endCount())
	}
	sessions.enable()
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatalf("Acquire after re-enable and stale cleanup = %v", err)
	}
	defer lease.Release()
	status, err = manager.Status(context.Background(), 7)
	if err != nil || status.State != StateActive || status.SessionID <= 0 {
		t.Fatalf("re-enabled runtime status = %#v, %v", status, err)
	}
}

func TestHeartbeatKeepsOwnershipThroughGracefulDrainUntilExactRelease(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 40, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	timersA := &manualTimerFactory{created: make(chan *manualTimer, 4)}
	processingStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	sourcesA := newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})
	managerA, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sourcesA}, Options{
		Now: clock.Now, OwnerToken: ownerToken(0xa3), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, NewHeartbeatTimer: timersA.New,
		Process: func(context.Context, OwnerFence, Session, roomsource.Event) error {
			close(processingStarted)
			<-allowCommit
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	managerB := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xb4), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second})
	if _, err := managerA.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	sourcesA.subscription(t, 0).Emit(roomsource.Event{ID: "gift-1", RoomID: "42"})
	<-processingStarted
	heartbeat := <-timersA.created
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- managerA.Shutdown(context.Background()) }()
	<-sourcesA.subscription(t, 0).cancelled
	clock.Advance(20 * time.Second)
	heartbeat.Fire()
	select {
	case <-sessions.renewed:
	case <-time.After(time.Second):
		t.Fatal("ownership heartbeat stopped before graceful drain released its fence")
	}
	clock.Advance(20 * time.Second)
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("takeover beyond original TTL during graceful drain = %v, want conflict", err)
	}
	close(allowCommit)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); err != nil {
		t.Fatalf("takeover after exact graceful release = %v", err)
	}
}

func TestHeartbeatRenewsAccountsConcurrentlyWithBoundedControlContexts(t *testing.T) {
	sessions := newConcurrentRenewSessions()
	timers := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: &fakeRoomSources{}}, Options{
		OwnerToken: ownerToken(0xc5), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, OwnerOperationTimeout: 50 * time.Millisecond, NewHeartbeatTimer: timers.New,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer close(sessions.forceUnblock)
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(context.Background(), 8, LeaseOBS); err != nil {
		t.Fatal(err)
	}
	(<-timers.created).Fire()
	select {
	case bounded := <-sessions.blockedContextBounded:
		if !bounded {
			t.Fatal("blocked ownership renew had no deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked account renew did not start")
	}
	select {
	case <-sessions.otherRenewed:
	case <-time.After(time.Second):
		t.Fatal("one blocked owner renewal starved another account")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if err := manager.Shutdown(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown expired error = %v", err)
	}
	select {
	case <-sessions.blockedExited:
	case <-time.After(time.Second):
		t.Fatal("bounded ownership control context did not cancel blocked renewal")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown error = %v", err)
	}
}

func TestNewManagerValidatesCompleteRenewalSafetyWindowWithoutOverflow(t *testing.T) {
	for _, test := range []struct {
		name      string
		ttl       time.Duration
		heartbeat time.Duration
		operation time.Duration
		valid     bool
	}{
		{name: "strictly inside", ttl: 30 * time.Second, heartbeat: 10 * time.Second, operation: 9 * time.Second, valid: true},
		{name: "equal boundary", ttl: 30 * time.Second, heartbeat: 10 * time.Second, operation: 10 * time.Second},
		{name: "beyond boundary", ttl: 30 * time.Second, heartbeat: 10 * time.Second, operation: 11 * time.Second},
		{name: "two heartbeats consume ttl", ttl: 20 * time.Second, heartbeat: 10 * time.Second, operation: time.Nanosecond},
		{name: "large duration allowed without addition", ttl: time.Duration(1<<63 - 1), heartbeat: time.Duration((1<<63 - 1) / 3), operation: time.Nanosecond, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(Dependencies{Sessions: newMemorySessionRepository(), Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: &fakeRoomSources{}}, Options{
				OwnerTTL: test.ttl, HeartbeatInterval: test.heartbeat, OwnerOperationTimeout: test.operation,
			})
			if test.valid {
				if err != nil {
					t.Fatalf("NewManager valid safety window error = %v", err)
				}
				if err := manager.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("NewManager unsafe owner operation window error = %v, want invalid input", err)
			}
		})
	}
}

func TestOwnershipConflictFailsClosedUntilBlockedStaleCurrentDrains(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 45, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	heartbeatsA := &manualTimerFactory{created: make(chan *manualTimer, 3)}
	processingStarted := make(chan struct{})
	allowPersist := make(chan struct{})
	persistResult := make(chan error, 1)
	sourcesA := newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})
	managerA := newTestManager(t, sessions, sourcesA, Options{
		Now: clock.Now, OwnerToken: ownerToken(0xc6), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, NewHeartbeatTimer: heartbeatsA.New,
		Process: func(_ context.Context, owner OwnerFence, _ Session, _ roomsource.Event) error {
			close(processingStarted)
			<-allowPersist
			err := sessions.persistAggregate(owner)
			persistResult <- err
			return err
		},
	})
	managerB := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xc7), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second})
	if _, err := managerA.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	oldSubscription := sourcesA.subscription(t, 0)
	oldSubscription.Emit(roomsource.Event{ID: "gift-stale", RoomID: "42"})
	<-processingStarted
	staleHeartbeat := <-heartbeatsA.created
	clock.Advance(31 * time.Second)
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); err != nil {
		t.Fatalf("foreign takeover = %v", err)
	}
	staleHeartbeat.Fire()
	<-sessions.staleRenewed
	<-oldSubscription.cancelled
	if _, err := managerA.Acquire(context.Background(), 7, LeaseConfig); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Acquire during stale drain = %v, want unavailable", err)
	}
	status, err := managerA.Status(context.Background(), 7)
	if err != nil || status.Leases != 0 || !status.Degraded {
		t.Fatalf("stale draining status = %#v, %v", status, err)
	}
	account, err := managerA.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	account.mu.Lock()
	staleDone := account.staleDone
	account.mu.Unlock()
	if staleDone == nil {
		t.Fatal("ownership conflict did not install stale-drain completion")
	}
	close(allowPersist)
	if err := <-persistResult; !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("old queued write = %v, want ownership conflict", err)
	}
	<-staleDone
	if err := managerB.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, err := managerA.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatalf("Acquire after stale cleanup and takeover release = %v", err)
	}
	defer lease.Release()
	status, err = managerA.Status(context.Background(), 7)
	if err != nil || status.State != StateActive || status.Leases != 1 || status.SessionID <= 0 {
		t.Fatalf("replacement runtime status = %#v, %v", status, err)
	}
}

func TestExpiredSameProcessClaimUsesNewFenceBeforeRestartingCapturedCurrent(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 47, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	processingStarted := make(chan struct{})
	allowPersist := make(chan struct{})
	persistResult := make(chan error, 1)
	sources := newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})
	manager := newTestManager(t, sessions, sources, Options{
		Now: clock.Now, OwnerToken: ownerToken(0xc8), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
		Process: func(_ context.Context, owner OwnerFence, _ Session, _ roomsource.Event) error {
			close(processingStarted)
			<-allowPersist
			err := sessions.persistAggregate(owner)
			persistResult <- err
			return err
		},
	})
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	oldSubscription := sources.subscription(t, 0)
	oldSubscription.Emit(roomsource.Event{ID: "gift-old-epoch", RoomID: "42"})
	<-processingStarted
	clock.Advance(31 * time.Second)
	if _, err := manager.Acquire(context.Background(), 7, LeaseOBS); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Acquire with a new fence over captured current = %v, want unavailable", err)
	}
	<-oldSubscription.cancelled
	account, err := manager.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	account.mu.Lock()
	staleDone := account.staleDone
	account.mu.Unlock()
	if staleDone == nil {
		t.Fatal("captured-fence mismatch did not start stale cleanup")
	}
	if _, err := manager.Acquire(context.Background(), 7, LeaseOBS); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Acquire before mismatched current cleanup = %v, want unavailable", err)
	}
	close(allowPersist)
	if err := <-persistResult; !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("old epoch queued write = %v, want ownership conflict", err)
	}
	<-staleDone
	if sessions.endCount() != 1 {
		t.Fatalf("new valid fence ended captured session %d times, want one", sessions.endCount())
	}
	lease, err := manager.Acquire(context.Background(), 7, LeaseOBS)
	if err != nil {
		t.Fatalf("Acquire after mismatched current cleanup = %v", err)
	}
	defer lease.Release()
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.State != StateActive || status.Leases != 1 || status.SessionID != 2 {
		t.Fatalf("replacement after captured-fence cleanup = %#v, %v", status, err)
	}
	if epoch, reconciles := sessions.ownerEpochAndReconciles(); epoch != 3 || reconciles != 0 {
		t.Fatalf("replacement epoch/reconciles = %d/%d, want 3/0", epoch, reconciles)
	}
}

func TestHeartbeatConflictRevokesAdmissionBeforeBlockedSetRoomReleasesOperationLock(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 48, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	heartbeats := &manualTimerFactory{created: make(chan *manualTimer, 3)}
	baseSources := newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42", "43": "43"})
	sources := &blockingResolveRoomSources{orderedRoomSources: baseSources, started: make(chan struct{}), allow: make(chan struct{})}
	managerA := newTestManager(t, sessions, sources, Options{Now: clock.Now, OwnerToken: ownerToken(0xc9), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second, NewHeartbeatTimer: heartbeats.New})
	managerB := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xca), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second})
	if _, err := managerA.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	oldSubscription := baseSources.subscription(t, 0)
	setRoomDone := make(chan error, 1)
	go func() { setRoomDone <- managerA.SetRoom(context.Background(), 7, "43") }()
	<-sources.started
	clock.Advance(31 * time.Second)
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); err != nil {
		t.Fatalf("foreign takeover = %v", err)
	}
	(<-heartbeats.created).Fire()
	<-sessions.staleRenewed
	select {
	case <-oldSubscription.cancelled:
	case <-time.After(time.Second):
		t.Fatal("known ownership conflict waited for blocked SetRoom before revoking admission")
	}
	status, err := managerA.Status(context.Background(), 7)
	if err != nil || status.Leases != 0 || !status.Degraded {
		t.Fatalf("immediately revoked status = %#v, %v", status, err)
	}
	select {
	case err := <-setRoomDone:
		t.Fatalf("SetRoom unexpectedly unblocked before resolver release: %v", err)
	default:
	}
	close(sources.allow)
	if err := <-setRoomDone; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SetRoom after stale resolution = %v, want unavailable", err)
	}
	if sessions.endCount() != 0 {
		t.Fatalf("known stale fence attempted EndSession %d times", sessions.endCount())
	}
}

func TestKnownAccountCannotClaimAfterShutdownWorkerHasPassed(t *testing.T) {
	sessions := newSharedOwnershipSessions(time.Now)
	sessions.target = "42"
	manager := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{OwnerToken: ownerToken(0xcb)})
	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	account, err := manager.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	before := sessions.claimCount()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.acquireKnownAccount(context.Background(), 7, LeaseOBS, account); !errors.Is(err, ErrClosed) {
		t.Fatalf("known-account Acquire after shutdown worker = %v, want closed", err)
	}
	if after := sessions.claimCount(); after != before {
		t.Fatalf("ownership claims after shutdown worker = %d, want unchanged %d", after, before)
	}
}

func TestAcquireCannotInsertLeaseAfterConflictRevokesDuringCloseRetry(t *testing.T) {
	log := &operationLog{}
	base := &orderedSessions{enabled: true, target: "42", endFailures: 1, log: log}
	sessions := &blockingRetryEndSessions{orderedSessions: base, retryStarted: make(chan struct{}), allowRetry: make(chan struct{}), conflictReturned: make(chan struct{})}
	closeTimers := &manualTimerFactory{created: make(chan *manualTimer, 3)}
	heartbeatTimers := &manualTimerFactory{created: make(chan *manualTimer, 3)}
	manager := newTestManager(t, sessions, newOrderedRoomSources(log, map[string]string{"42": "42"}), Options{NewTimer: closeTimers.New, NewHeartbeatTimer: heartbeatTimers.New})
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	(<-closeTimers.created).Fire()
	<-closeTimers.created // failed End installed its retry timer
	acquireDone := make(chan error, 1)
	go func() {
		_, acquireErr := manager.Acquire(context.Background(), 7, LeaseOBS)
		acquireDone <- acquireErr
	}()
	<-sessions.retryStarted
	sessions.setRenewConflict()
	(<-heartbeatTimers.created).Fire()
	<-sessions.conflictReturned
	account, err := manager.accountExisting(7)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		account.mu.Lock()
		stale := account.stale
		account.mu.Unlock()
		if stale {
			break
		}
		select {
		case <-deadline:
			t.Fatal("heartbeat conflict did not revoke runtime")
		default:
			goruntime.Gosched()
		}
	}
	close(sessions.allowRetry)
	if err := <-acquireDone; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Acquire after mid-operation revocation = %v, want unavailable", err)
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || status.Leases != 0 {
		t.Fatalf("lease survived mid-operation revocation = %#v, %v", status, err)
	}
}

func TestShutdownRetriesReleaseWhileHeartbeatKeepsFenceAlive(t *testing.T) {
	clock := &ownershipClock{now: time.Date(2026, 8, 17, 5, 50, 0, 0, time.UTC)}
	sessions := newSharedOwnershipSessions(clock.Now)
	sessions.target = "42"
	sessions.releaseFailures = 1
	retryTimers := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	heartbeatTimers := &manualTimerFactory{created: make(chan *manualTimer, 3)}
	managerA, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"})}, Options{
		Now: clock.Now, NewTimer: retryTimers.New, NewHeartbeatTimer: heartbeatTimers.New,
		OwnerToken: ownerToken(0xd5), OwnerTTL: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	managerB := newTestManager(t, sessions, newOrderedRoomSources(&operationLog{}, map[string]string{"42": "42"}), Options{Now: clock.Now, OwnerToken: ownerToken(0xe6)})
	if _, err := managerA.Acquire(context.Background(), 7, LeaseConfig); err != nil {
		t.Fatal(err)
	}
	heartbeat := <-heartbeatTimers.created
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- managerA.Shutdown(context.Background()) }()
	retry := <-retryTimers.created
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown stopped after transient release failure: %v", err)
	default:
	}
	clock.Advance(20 * time.Second)
	heartbeat.Fire()
	select {
	case <-sessions.renewed:
	case <-time.After(time.Second):
		t.Fatal("heartbeat stopped while exact release was pending retry")
	}
	clock.Advance(20 * time.Second)
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("takeover during release retry = %v, want conflict", err)
	}
	retry.Fire()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown after successful release retry = %v", err)
	}
	if _, err := managerB.Acquire(context.Background(), 7, LeaseOBS); err != nil {
		t.Fatalf("takeover after retried exact release = %v", err)
	}
}

func TestNewOwnerTokenUsesInjectedEntropyAndRejectsShortReads(t *testing.T) {
	wantBytes := bytes.Repeat([]byte{0x5a}, 32)
	token, err := NewOwnerToken(bytes.NewReader(wantBytes))
	if err != nil || !bytes.Equal(token[:], wantBytes) {
		t.Fatalf("NewOwnerToken() = %x, %v", token, err)
	}
	if _, err := NewOwnerToken(bytes.NewReader(wantBytes[:31])); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short NewOwnerToken error = %v, want invalid input", err)
	}
}

func TestSessionRepositoryClaimsAbsentSameAndExpiredOwnershipUnderAccountLock(t *testing.T) {
	token := ownerToken(0x11)
	foreign := ownerToken(0x22)
	ttl := 30 * time.Second
	for _, test := range []struct {
		name          string
		currentToken  OwnerToken
		currentEpoch  uint64
		expired       bool
		absent        bool
		wantEpoch     uint64
		wantReconcile bool
		update        bool
	}{
		{name: "absent", absent: true, wantEpoch: 1, wantReconcile: false},
		{name: "same unexpired", currentToken: token, currentEpoch: 4, wantEpoch: 4, update: true},
		{name: "expired foreign", currentToken: foreign, currentEpoch: 4, expired: true, wantEpoch: 5, wantReconcile: true, update: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			expectOwnershipAccountLock(mock, 7, true)
			ownerQuery := mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at <= UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7))
			if test.absent {
				ownerQuery.WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "expired"}))
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_account_owners (account_id, owner_token, fencing_epoch, expires_at) VALUES (?, ?, 1, DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND))")).
					WithArgs(int64(7), token[:], ttl.Microseconds()).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				ownerQuery.WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "expired"}).AddRow(test.currentToken[:], test.currentEpoch, test.expired))
				if test.currentToken == token && !test.expired {
					mock.ExpectExec("UPDATE runtime_account_owners SET expires_at = DATE_ADD").
						WithArgs(ttl.Microseconds(), int64(7), token[:], test.currentEpoch).WillReturnResult(sqlmock.NewResult(0, 1))
				} else {
					mock.ExpectExec("UPDATE runtime_account_owners SET owner_token = \\?, fencing_epoch = fencing_epoch \\+ 1, expires_at = DATE_ADD").
						WithArgs(token[:], ttl.Microseconds(), int64(7), test.currentToken[:], test.currentEpoch).WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			mock.ExpectCommit()

			claim, err := NewSessionRepository(database).ClaimOwnership(context.Background(), 7, token, ttl)
			if err != nil || claim.Fence.AccountID != 7 || claim.Fence.Token != token || claim.Fence.Epoch != test.wantEpoch || claim.Reconcile != test.wantReconcile {
				t.Fatalf("ClaimOwnership() = %#v, %v", claim, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionRepositoryRejectsDisabledAndForeignUnexpiredOwnership(t *testing.T) {
	token := ownerToken(0x11)
	foreign := ownerToken(0x22)
	for _, test := range []struct {
		name      string
		enabled   bool
		ownerRow  bool
		wantError error
	}{
		{name: "disabled", enabled: false, wantError: ErrAccountDisabled},
		{name: "foreign unexpired", enabled: true, ownerRow: true, wantError: ErrOwnershipConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			expectOwnershipAccountLock(mock, 7, test.enabled)
			if test.ownerRow {
				mock.ExpectQuery("SELECT owner_token, fencing_epoch").WithArgs(int64(7)).
					WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "expired"}).AddRow(foreign[:], uint64(9), false))
			}
			mock.ExpectRollback()
			_, err = NewSessionRepository(database).ClaimOwnership(context.Background(), 7, token, 30*time.Second)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ClaimOwnership error = %v, want %v", err, test.wantError)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionRepositoryRenewsAndReleasesOnlyExactUnexpiredFence(t *testing.T) {
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x33), Epoch: 12}
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewSessionRepository(database)
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	mock.ExpectExec("UPDATE runtime_account_owners SET expires_at = DATE_ADD").
		WithArgs((30 * time.Second).Microseconds(), int64(7), fence.Token[:], fence.Epoch).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repository.RenewOwnership(context.Background(), fence, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE runtime_account_owners SET expires_at = UTC_TIMESTAMP(6) WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ?")).
		WithArgs(int64(7), fence.Token[:], fence.Epoch).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repository.ReleaseOwnership(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryRenewAndReleaseRejectStaleFence(t *testing.T) {
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x34), Epoch: 13}
	for _, operation := range []string{"renew", "release"} {
		t.Run(operation, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			if operation == "renew" {
				expectOwnershipAccountLock(mock, 7, true)
				mock.ExpectExec("UPDATE runtime_account_owners SET expires_at = DATE_ADD").
					WithArgs((30 * time.Second).Microseconds(), int64(7), fence.Token[:], fence.Epoch).
					WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectQuery("SELECT id FROM streamer_accounts").WithArgs(int64(7)).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
				mock.ExpectExec("UPDATE runtime_account_owners SET expires_at = UTC_TIMESTAMP").
					WithArgs(int64(7), fence.Token[:], fence.Epoch).
					WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectRollback()
			repository := NewSessionRepository(database)
			if operation == "renew" {
				err = repository.RenewOwnership(context.Background(), fence, 30*time.Second)
			} else {
				err = repository.ReleaseOwnership(context.Background(), fence)
			}
			if !errors.Is(err, ErrOwnershipConflict) {
				t.Fatalf("%s error = %v, want ownership conflict", operation, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectOwnershipAccountLock(mock sqlmock.Sqlmock, accountID int64, enabled bool) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(accountID).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(enabled))
}

func ownerToken(value byte) OwnerToken {
	var token OwnerToken
	for index := range token {
		token[index] = value
	}
	return token
}

type ownershipClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *ownershipClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *ownershipClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

type sharedOwnerRecord struct {
	fence     OwnerFence
	expiresAt time.Time
}

type sharedOwnershipSessions struct {
	mu              sync.Mutex
	now             func() time.Time
	enabled         bool
	target          string
	owner           sharedOwnerRecord
	epoch           uint64
	open            *Session
	nextID          int64
	ends            int
	endFailures     int
	reconciles      int
	pendingJob      int64
	releaseFailures int
	claims          int
	waitReturned    *atomic.Bool
	lateOperations  atomic.Int32
	staleRenewed    chan struct{}
	staleOnce       sync.Once
	renewed         chan struct{}
}

func newSharedOwnershipSessions(now func() time.Time) *sharedOwnershipSessions {
	return &sharedOwnershipSessions{now: now, enabled: true, staleRenewed: make(chan struct{}), renewed: make(chan struct{}, 4)}
}

func (sessions *sharedOwnershipSessions) AccountEnabled(context.Context, int64) (bool, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.enabled, nil
}

func (sessions *sharedOwnershipSessions) ClaimOwnership(_ context.Context, accountID int64, token OwnerToken, ttl time.Duration) (OwnerClaim, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	sessions.claims++
	if !sessions.enabled {
		return OwnerClaim{}, ErrAccountDisabled
	}
	now := sessions.now()
	active := validOwnerFence(sessions.owner.fence) && now.Before(sessions.owner.expiresAt)
	if active && sessions.owner.fence.Token != token {
		return OwnerClaim{}, ErrOwnershipConflict
	}
	absent := !validOwnerFence(sessions.owner.fence)
	reconcile := !absent && !active
	if !active {
		sessions.epoch++
		sessions.owner.fence = OwnerFence{AccountID: accountID, Token: token, Epoch: sessions.epoch}
	}
	sessions.owner.expiresAt = now.Add(ttl)
	return OwnerClaim{Fence: sessions.owner.fence, Reconcile: reconcile}, nil
}

func (sessions *sharedOwnershipSessions) RenewOwnership(_ context.Context, fence OwnerFence, ttl time.Duration) error {
	sessions.recordOperationStart()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if !sessions.enabled {
		return ErrAccountDisabled
	}
	if sessions.owner.fence != fence || !sessions.now().Before(sessions.owner.expiresAt) {
		sessions.staleOnce.Do(func() { close(sessions.staleRenewed) })
		return ErrOwnershipConflict
	}
	sessions.owner.expiresAt = sessions.now().Add(ttl)
	sessions.renewed <- struct{}{}
	return nil
}

func (sessions *sharedOwnershipSessions) ReleaseOwnership(_ context.Context, fence OwnerFence) error {
	sessions.recordOperationStart()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.releaseFailures > 0 {
		sessions.releaseFailures--
		return ErrUnavailable
	}
	if sessions.owner.fence != fence {
		return ErrOwnershipConflict
	}
	sessions.owner.expiresAt = sessions.now()
	return nil
}

func (sessions *sharedOwnershipSessions) TargetRoom(context.Context, int64) (string, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.target == "" {
		return "", ErrNoTargetRoom
	}
	return sessions.target, nil
}

func (sessions *sharedOwnershipSessions) PersistTargetRoom(_ context.Context, command PersistTargetRoomCommand) error {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if !sessions.ownsLocked(command.Owner) {
		return ErrOwnershipConflict
	}
	sessions.target = command.RoomID
	return nil
}

func (sessions *sharedOwnershipSessions) StartSession(_ context.Context, command StartSessionCommand) (Session, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if !sessions.ownsLocked(command.Owner) {
		return Session{}, ErrOwnershipConflict
	}
	if sessions.open != nil {
		if !command.Reconcile {
			return Session{}, ErrUnavailable
		}
		sessions.reconciles++
		sessions.open = nil
	}
	sessions.nextID++
	started := Session{ID: sessions.nextID, AccountID: command.AccountID, RoomID: command.RoomID, ConfigVersionID: command.ConfigVersionID, StartedAt: command.StartedAt}
	sessions.open = &started
	return started, nil
}

func (sessions *sharedOwnershipSessions) EndSession(_ context.Context, command EndSessionCommand) error {
	sessions.recordOperationStart()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.endFailures > 0 {
		sessions.endFailures--
		return ErrUnavailable
	}
	if !sessions.ownsLocked(command.Owner) {
		return ErrOwnershipConflict
	}
	if sessions.open == nil || sessions.open.ID != command.SessionID || sessions.open.AccountID != command.AccountID {
		return ErrUnavailable
	}
	sessions.open = nil
	sessions.ends++
	return nil
}

func (sessions *sharedOwnershipSessions) ReconcileSession(_ context.Context, command ReconcileSessionCommand) error {
	sessions.recordOperationStart()
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.ownsLocked(command.LostOwner) {
		return ErrOwnershipConflict
	}
	if sessions.open == nil || sessions.open.ID != command.SessionID || sessions.open.AccountID != command.AccountID {
		return nil
	}
	sessions.open = nil
	sessions.reconciles++
	return nil
}

func (sessions *sharedOwnershipSessions) recordOperationStart() {
	if sessions.waitReturned != nil && sessions.waitReturned.Load() {
		sessions.lateOperations.Add(1)
	}
}

func (sessions *sharedOwnershipSessions) PendingMigration(context.Context, int64) (int64, bool, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.pendingJob, sessions.pendingJob != 0, nil
}

func (sessions *sharedOwnershipSessions) ownsLocked(fence OwnerFence) bool {
	return sessions.owner.fence == fence && sessions.now().Before(sessions.owner.expiresAt)
}

func (sessions *sharedOwnershipSessions) ownerEpochAndReconciles() (uint64, int) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.epoch, sessions.reconciles
}

func (sessions *sharedOwnershipSessions) endCount() int {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.ends
}

func (sessions *sharedOwnershipSessions) claimCount() int {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.claims
}

func (sessions *sharedOwnershipSessions) disable() {
	sessions.mu.Lock()
	sessions.enabled = false
	sessions.mu.Unlock()
}

func (sessions *sharedOwnershipSessions) enable() {
	sessions.mu.Lock()
	sessions.enabled = true
	sessions.mu.Unlock()
}

func (sessions *sharedOwnershipSessions) persistAggregate(owner OwnerFence) error {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if !sessions.ownsLocked(owner) {
		return ErrOwnershipConflict
	}
	return nil
}

type blockingResolveRoomSources struct {
	*orderedRoomSources
	started chan struct{}
	allow   chan struct{}
	once    sync.Once
}

type blockingRetryEndSessions struct {
	*orderedSessions
	mu               sync.Mutex
	endCalls         int
	retryStarted     chan struct{}
	allowRetry       chan struct{}
	renewConflict    bool
	conflictReturned chan struct{}
	conflictOnce     sync.Once
}

func (sessions *blockingRetryEndSessions) EndSession(ctx context.Context, command EndSessionCommand) error {
	sessions.mu.Lock()
	sessions.endCalls++
	call := sessions.endCalls
	sessions.mu.Unlock()
	if call == 2 {
		close(sessions.retryStarted)
		select {
		case <-sessions.allowRetry:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return sessions.orderedSessions.EndSession(ctx, command)
}

func (sessions *blockingRetryEndSessions) RenewOwnership(ctx context.Context, fence OwnerFence, ttl time.Duration) error {
	sessions.mu.Lock()
	conflict := sessions.renewConflict
	sessions.mu.Unlock()
	if conflict {
		sessions.conflictOnce.Do(func() { close(sessions.conflictReturned) })
		return ErrOwnershipConflict
	}
	return sessions.orderedSessions.RenewOwnership(ctx, fence, ttl)
}

func (sessions *blockingRetryEndSessions) setRenewConflict() {
	sessions.mu.Lock()
	sessions.renewConflict = true
	sessions.mu.Unlock()
}

func (sources *blockingResolveRoomSources) Resolve(ctx context.Context, roomID string, accountID int64) (string, error) {
	sources.once.Do(func() { close(sources.started) })
	select {
	case <-sources.allow:
		return sources.orderedRoomSources.Resolve(ctx, roomID, accountID)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type fencedPendingMigration struct {
	sessions *sharedOwnershipSessions
	started  chan struct{}
	allow    chan struct{}
	mu       sync.Mutex
	applied  bool
}

func (applier *fencedPendingMigration) ApplyPendingAfterSession(_ context.Context, owner migration.OwnerFence, jobID int64) (migration.Job, error) {
	close(applier.started)
	<-applier.allow
	fence := OwnerFence{AccountID: owner.AccountID, Token: OwnerToken(owner.Token), Epoch: owner.Epoch}
	applier.sessions.mu.Lock()
	owned := applier.sessions.ownsLocked(fence)
	applier.sessions.mu.Unlock()
	if !owned {
		return migration.Job{}, migration.ErrOwnershipConflict
	}
	applier.mu.Lock()
	applier.applied = true
	applier.mu.Unlock()
	return migration.Job{ID: jobID}, nil
}

func (applier *fencedPendingMigration) wasApplied() bool {
	applier.mu.Lock()
	defer applier.mu.Unlock()
	return applier.applied
}

type concurrentRenewSessions struct {
	repositories          map[int64]*memorySessionRepository
	blockedStarted        chan struct{}
	blockedContextBounded chan bool
	blockedExited         chan struct{}
	otherRenewed          chan struct{}
	forceUnblock          chan struct{}
	blockedOnce           sync.Once
	otherOnce             sync.Once
}

func newConcurrentRenewSessions() *concurrentRenewSessions {
	return &concurrentRenewSessions{
		repositories:   map[int64]*memorySessionRepository{7: newMemorySessionRepository(), 8: newMemorySessionRepository()},
		blockedStarted: make(chan struct{}), blockedContextBounded: make(chan bool, 1), blockedExited: make(chan struct{}), otherRenewed: make(chan struct{}), forceUnblock: make(chan struct{}),
	}
}

func (sessions *concurrentRenewSessions) repository(accountID int64) *memorySessionRepository {
	return sessions.repositories[accountID]
}

func (sessions *concurrentRenewSessions) ClaimOwnership(ctx context.Context, accountID int64, token OwnerToken, ttl time.Duration) (OwnerClaim, error) {
	return sessions.repository(accountID).ClaimOwnership(ctx, accountID, token, ttl)
}

func (sessions *concurrentRenewSessions) RenewOwnership(ctx context.Context, fence OwnerFence, ttl time.Duration) error {
	if fence.AccountID == 7 {
		sessions.blockedOnce.Do(func() {
			_, bounded := ctx.Deadline()
			sessions.blockedContextBounded <- bounded
			close(sessions.blockedStarted)
		})
		select {
		case <-ctx.Done():
			close(sessions.blockedExited)
			return ctx.Err()
		case <-sessions.forceUnblock:
			return ErrUnavailable
		}
	}
	select {
	case <-sessions.blockedStarted:
	case <-ctx.Done():
		return ctx.Err()
	case <-sessions.forceUnblock:
		return ErrUnavailable
	}
	err := sessions.repository(fence.AccountID).RenewOwnership(ctx, fence, ttl)
	if err == nil {
		sessions.otherOnce.Do(func() { close(sessions.otherRenewed) })
	}
	return err
}

func (sessions *concurrentRenewSessions) ReleaseOwnership(ctx context.Context, fence OwnerFence) error {
	return sessions.repository(fence.AccountID).ReleaseOwnership(ctx, fence)
}
func (*concurrentRenewSessions) TargetRoom(context.Context, int64) (string, error) {
	return "", ErrNoTargetRoom
}
func (*concurrentRenewSessions) PersistTargetRoom(context.Context, PersistTargetRoomCommand) error {
	return ErrUnavailable
}
func (*concurrentRenewSessions) StartSession(context.Context, StartSessionCommand) (Session, error) {
	return Session{}, ErrUnavailable
}
func (*concurrentRenewSessions) EndSession(context.Context, EndSessionCommand) error {
	return ErrUnavailable
}
func (sessions *concurrentRenewSessions) ReconcileSession(ctx context.Context, command ReconcileSessionCommand) error {
	return sessions.repository(command.AccountID).ReconcileSession(ctx, command)
}
func (*concurrentRenewSessions) PendingMigration(context.Context, int64) (int64, bool, error) {
	return 0, false, nil
}
