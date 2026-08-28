package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

func TestMigrationBarrierAssignsEveryGiftToExactlyOneVersion(t *testing.T) {
	repository := newBarrierMemoryRepository()
	manager, target, targetProcessor := newBarrierManagerHarness(t, repository, 7)
	_, peer, peerProcessor := addBarrierAccount(t, manager, 8)
	oldGift := roomsource.Event{ID: "old-gift", RoomID: "42", Type: "SEND_GIFT"}
	newGift := roomsource.Event{ID: "new-gift", RoomID: "42", Type: "SEND_GIFT"}
	peerGift := roomsource.Event{ID: "peer-gift", RoomID: "42", Type: "SEND_GIFT"}

	sessionSink{active: target}.OnEvent(oldGift)
	applyDone := make(chan barrierApplyResult, 1)
	go func() {
		boundary, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19))
		applyDone <- barrierApplyResult{boundary: boundary, err: err}
	}()
	<-repository.activationStarted
	targetProcessor.waitForGift(t, oldGift.ID)

	newAdmitted := make(chan struct{})
	go func() {
		sessionSink{active: target}.OnEvent(newGift)
		close(newAdmitted)
	}()
	select {
	case <-newAdmitted:
		t.Fatal("target admission reopened before activation and full snapshot")
	case <-time.After(20 * time.Millisecond):
	}
	sessionSink{active: peer}.OnEvent(peerGift)
	peerProcessor.waitForGift(t, peerGift.ID)

	close(repository.allowActivation)
	result := <-applyDone
	if result.err != nil {
		t.Fatalf("ApplyConfigurationBarrier() error = %v", result.err)
	}
	select {
	case <-newAdmitted:
	case <-time.After(time.Second):
		t.Fatal("target admission did not reopen")
	}
	targetProcessor.waitForGift(t, newGift.ID)

	if got := targetProcessor.versionFor(oldGift.ID); got != 10 {
		t.Fatalf("old gift version = %d, want 10", got)
	}
	if got := targetProcessor.versionFor(newGift.ID); got != result.boundary.NewConfigVersionID {
		t.Fatalf("new gift version = %d, want %d", got, result.boundary.NewConfigVersionID)
	}
	if got := targetProcessor.committedGiftCount(); got != 2 {
		t.Fatalf("target committed gift count = %d, want 2", got)
	}
	if got := peerProcessor.committedGiftCount(); got != 1 {
		t.Fatalf("peer committed gift count = %d, want 1", got)
	}
	if result.boundary.BroadcastSessionID != 91 || result.boundary.LiveSessionID != 81 || result.boundary.LastOldRevision != 4 || result.boundary.FirstNewRevision != 5 {
		t.Fatalf("boundary = %#v", result.boundary)
	}
	if target.session.BroadcastSessionID != 91 || target.session.ID != 81 || target.subscription.(*barrierSubscription).cancelCount() != 0 {
		t.Fatalf("barrier changed Bstation/session lifecycle: session=%#v cancels=%d", target.session, target.subscription.(*barrierSubscription).cancelCount())
	}
	if snapshots := targetProcessor.publishedSnapshots(); len(snapshots) != 1 || snapshots[0].Revision != 5 || snapshots[0].LiveSessionID != 81 {
		t.Fatalf("full snapshots = %#v", snapshots)
	}
}

func TestMigrationBarrierOwnershipConflictBeforeMarkerReturnsWithoutDeadlockingCleanup(t *testing.T) {
	repository := newBarrierMemoryRepository()
	manager, active, processor := newBarrierManagerHarness(t, repository, 7)
	processor.blockEvent("ownership-conflict", ErrOwnershipConflict)
	sessionSink{active: active}.OnEvent(roomsource.Event{ID: "ownership-conflict", RoomID: "42", Type: "SEND_GIFT"})
	processor.waitForBlockedEvent(t)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := manager.ApplyConfigurationBarrier(ctx, 7, barrierCandidateFixture(19))
		result <- err
	}()
	waitForBarrierMarker(t, active)
	processor.releaseBlockedEvent()

	select {
	case err := <-result:
		if !errors.Is(err, migration.ErrOwnershipConflict) {
			t.Fatalf("ApplyConfigurationBarrier() error = %v, want migration.ErrOwnershipConflict", err)
		}
	case <-time.After(time.Second):
		t.Fatal("barrier remained blocked behind ownership cleanup")
	}
	waitForStaleCleanup(t, active.account, active)
	lockReturned := make(chan struct{})
	go func() {
		active.admissionMu.Lock()
		active.admissionMu.Unlock()
		close(lockReturned)
	}()
	select {
	case <-lockReturned:
	case <-time.After(time.Second):
		t.Fatal("ownership cleanup left admission permanently locked")
	}
}

func TestMigrationBarrierFailsClosedWhenCandidateNeedsUnavailableTimerScheduler(t *testing.T) {
	repository := newBarrierMemoryRepository()
	manager, target, _ := newBarrierManagerHarness(t, repository, 7)
	candidate := barrierCandidateFixture(19)
	candidate.Definition.TimerRules = append(candidate.Definition.TimerRules, timerRuleFixture())

	_, err := manager.ApplyConfigurationBarrier(context.Background(), 7, candidate)
	if !errors.Is(err, ErrTimerSchedulerUnavailable) {
		t.Fatalf("ApplyConfigurationBarrier() error = %v, want ErrTimerSchedulerUnavailable", err)
	}
	if repository.activationCount() != 0 {
		t.Fatalf("activation count = %d, want 0", repository.activationCount())
	}
	admitted := make(chan struct{})
	go func() {
		sessionSink{active: target}.OnEvent(roomsource.Event{ID: "after-timer-rejection", RoomID: "42", Type: "SEND_GIFT"})
		close(admitted)
	}()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("timer rejection left admission closed")
	}
}

func TestMigrationBarrierSerializesConcurrentApplyWithoutRepublishingOrRewinding(t *testing.T) {
	repository := newBarrierMemoryRepository()
	manager, _, processor := newBarrierManagerHarness(t, repository, 7)
	results := make(chan barrierApplyResult, 2)
	apply := func() {
		boundary, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19))
		results <- barrierApplyResult{boundary: boundary, err: err}
	}
	go apply()
	<-repository.activationStarted
	go apply()
	close(repository.allowActivation)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent apply errors = %v, %v", first.err, second.err)
	}
	if first.boundary != second.boundary {
		t.Fatalf("concurrent boundaries differ: %#v %#v", first.boundary, second.boundary)
	}
	if repository.activationCount() != 1 {
		t.Fatalf("durable activation count = %d, want 1", repository.activationCount())
	}
	if snapshots := processor.publishedSnapshots(); len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
	processor.mu.Lock()
	revision := processor.revision
	processor.mu.Unlock()
	if revision != first.boundary.FirstNewRevision {
		t.Fatalf("processor revision = %d, want %d", revision, first.boundary.FirstNewRevision)
	}
}

func TestMigrationBarrierCompletedBoundaryIsIdempotentAcrossLaterSessionsWithoutRepublish(t *testing.T) {
	for _, test := range []struct {
		name                      string
		persistedLiveSessionID    int64
		persistedBroadcastID      int64
		persistedOldConfigVersion int64
		lastOldRevision           uint64
		firstNewRevision          uint64
		currentLiveSessionID      int64
		currentBroadcastID        int64
		currentRevision           uint64
	}{
		{name: "offline apply retried while later live", persistedOldConfigVersion: 0, lastOldRevision: 0, firstNewRevision: 1, currentLiveSessionID: 81, currentBroadcastID: 91, currentRevision: 3},
		{name: "session A apply retried in session B", persistedLiveSessionID: 81, persistedBroadcastID: 91, persistedOldConfigVersion: 10, lastOldRevision: 4, firstNewRevision: 5, currentLiveSessionID: 82, currentBroadcastID: 92, currentRevision: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newBarrierMemoryRepository()
			close(repository.allowActivation)
			persisted := configuration.Boundary{
				AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply,
				OldConfigVersionID: test.persistedOldConfigVersion, NewConfigVersionID: 11,
				LiveSessionID: test.persistedLiveSessionID, BroadcastSessionID: test.persistedBroadcastID,
				LastOldRevision: test.lastOldRevision, FirstNewRevision: test.firstNewRevision,
				AppliedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
			}
			repository.setBoundary(persisted)
			manager, active, processor := newBarrierManagerHarness(t, repository, 7)
			active.session.ID = test.currentLiveSessionID
			active.session.BroadcastSessionID = test.currentBroadcastID
			active.session.ConfigVersionID = 11
			processor.mu.Lock()
			processor.version = 11
			processor.revision = test.currentRevision
			processor.mu.Unlock()

			got, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19))
			if err != nil {
				t.Fatalf("cross-session retry error = %v", err)
			}
			if got != persisted {
				t.Fatalf("cross-session retry boundary = %#v, want persisted %#v", got, persisted)
			}
			if snapshots := processor.publishedSnapshots(); len(snapshots) != 0 {
				t.Fatalf("cross-session idempotent retry republished snapshots: %#v", snapshots)
			}
			if active.session.ID != test.currentLiveSessionID || active.session.BroadcastSessionID != test.currentBroadcastID || active.subscription.(*barrierSubscription).cancelCount() != 0 {
				t.Fatalf("cross-session retry changed current session: %#v", active.session)
			}
		})
	}
}

func TestMigrationBarrierOldEdgeCannotReconcileBoundaryFromDifferentSession(t *testing.T) {
	repository := newBarrierMemoryRepository()
	close(repository.allowActivation)
	repository.setBoundary(configuration.Boundary{
		AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply,
		OldConfigVersionID: 10, NewConfigVersionID: 11,
		LiveSessionID: 81, BroadcastSessionID: 91,
		LastOldRevision: 3, FirstNewRevision: 4,
		AppliedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	})
	manager, active, processor := newBarrierManagerHarness(t, repository, 7)
	active.session.ID = 82
	active.session.BroadcastSessionID = 92

	if _, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("old-edge cross-session reconciliation error = %v, want ErrUnavailable", err)
	}
	if snapshots := processor.publishedSnapshots(); len(snapshots) != 0 {
		t.Fatalf("rejected cross-session reconciliation published snapshots: %#v", snapshots)
	}
	assertBarrierFailureRecovered(t, active, processor, "after-cross-session-rejection")
}

func TestMigrationBarrierCancellationAndFailuresAlwaysReopenAdmissionWithoutPartialSwitch(t *testing.T) {
	t.Run("context cancellation during activation", func(t *testing.T) {
		repository := newBarrierMemoryRepository()
		manager, active, processor := newBarrierManagerHarness(t, repository, 7)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := manager.ApplyConfigurationBarrier(ctx, 7, barrierCandidateFixture(19))
			done <- err
		}()
		<-repository.activationStarted
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled barrier error = %v", err)
		}
		assertBarrierFailureRecovered(t, active, processor, "after-cancel")
	})

	t.Run("activation failure", func(t *testing.T) {
		repository := newBarrierMemoryRepository()
		repository.activationErr = errors.New("activation failed")
		close(repository.allowActivation)
		manager, active, processor := newBarrierManagerHarness(t, repository, 7)
		if _, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("activation failure error = %v, want ErrUnavailable", err)
		}
		assertBarrierFailureRecovered(t, active, processor, "after-activation-failure")
	})

	t.Run("ownership failure remains distinct", func(t *testing.T) {
		repository := newBarrierMemoryRepository()
		repository.activationErr = configuration.ErrOwnership
		close(repository.allowActivation)
		manager, active, processor := newBarrierManagerHarness(t, repository, 7)
		if _, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19)); !errors.Is(err, migration.ErrOwnershipConflict) {
			t.Fatalf("ownership failure error = %v, want migration.ErrOwnershipConflict", err)
		}
		assertBarrierFailureRecovered(t, active, processor, "after-ownership-failure")
	})

	t.Run("OBS snapshot preparation failure", func(t *testing.T) {
		repository := newBarrierMemoryRepository()
		manager, active, processor := newBarrierManagerHarness(t, repository, 7)
		processor.prepareSnapshotErr = errors.New("publisher unavailable")
		if _, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("snapshot failure error = %v, want ErrUnavailable", err)
		}
		if repository.activationCount() != 0 {
			t.Fatalf("snapshot preflight failure activated configuration %d times", repository.activationCount())
		}
		assertBarrierFailureRecovered(t, active, processor, "after-snapshot-failure")
	})
}

func TestMigrationBarrierUnresolvedCommitFailsClosedUntilRetryReconciles(t *testing.T) {
	repository := newBarrierMemoryRepository()
	repository.setActivationError(configuration.ErrCommitUncertain)
	close(repository.allowActivation)
	manager, active, processor := newBarrierManagerHarness(t, repository, 7)

	if _, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unresolved barrier error = %v, want ErrUnavailable", err)
	}
	status, err := manager.Status(context.Background(), 7)
	if err != nil || !status.Degraded {
		t.Fatalf("fail-closed status = %#v, %v", status, err)
	}
	sessionSink{active: active}.OnEvent(roomsource.Event{ID: "while-commit-uncertain", RoomID: "42", Type: "SEND_GIFT"})
	select {
	case id := <-processor.accepted:
		t.Fatalf("event %q entered the old processor while commit was uncertain", id)
	case <-time.After(20 * time.Millisecond):
	}

	repository.setActivationError(nil)
	boundary, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19))
	if err != nil {
		t.Fatalf("reconciliation retry error = %v", err)
	}
	sessionSink{active: active}.OnEvent(roomsource.Event{ID: "after-reconciliation", RoomID: "42", Type: "SEND_GIFT"})
	processor.waitForGift(t, "after-reconciliation")
	if got := processor.versionFor("after-reconciliation"); got != boundary.NewConfigVersionID {
		t.Fatalf("gift after reconciliation used version %d, want %d", got, boundary.NewConfigVersionID)
	}
	status, err = manager.Status(context.Background(), 7)
	if err != nil || status.Degraded {
		t.Fatalf("reconciled status = %#v, %v", status, err)
	}
}

func TestMigrationBarrierContextCancelsWhileWaitingForConcurrentAccountGate(t *testing.T) {
	repository := newBarrierMemoryRepository()
	manager, _, _ := newBarrierManagerHarness(t, repository, 7)
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.ApplyConfigurationBarrier(context.Background(), 7, barrierCandidateFixture(19))
		firstDone <- err
	}()
	<-repository.activationStarted
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.ApplyConfigurationBarrier(ctx, 7, barrierCandidateFixture(19))
		secondDone <- err
	}()
	cancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("gate waiter error = %v, want context.Canceled", err)
	}
	close(repository.allowActivation)
	if err := <-firstDone; err != nil {
		t.Fatalf("first apply error = %v", err)
	}
}

func TestConfigurationBarrierStateLetsRepositoryReconcileAnAmbiguousCommit(t *testing.T) {
	processor := &Processor{
		repository: advancedBarrierStateRepository{},
		binding: ProcessorBinding{
			Owner:   OwnerFence{AccountID: 7, Token: OwnerToken{1}, Epoch: 1},
			Session: Session{ID: 81, AccountID: 7, RoomID: "42", ConfigVersionID: 10},
		},
		state: configuration.State{AccountID: 7, ConfigVersionID: 10, Revision: 4},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	state, err := processor.configurationBarrierState(ctx)
	if err != nil {
		t.Fatalf("configurationBarrierState() error = %v", err)
	}
	if state.ConfigVersionID != 10 || state.Revision != 4 {
		t.Fatalf("configurationBarrierState() = %#v, want drained local 10/4", state)
	}
}

func TestPendingConfigurationBarrierUsesOwnedOfflinePathWithoutReacquiringTheAccountGate(t *testing.T) {
	repository := newBarrierMemoryRepository()
	close(repository.allowActivation)
	manager := &Manager{
		dependencies: Dependencies{Configuration: repository},
		accounts:     make(map[int64]*accountRuntime),
		now:          func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) },
	}
	owner := OwnerFence{AccountID: 7, Token: OwnerToken{1, 2, 3}, Epoch: 4}
	account := &accountRuntime{manager: manager, accountID: 7, owner: owner, operation: true, leases: make(map[uint64]LeaseKind)}
	manager.accounts[7] = account
	permit, err := account.opGate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer mustReleaseOperationPermit(permit)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	boundary, err := manager.ApplyPendingConfigurationBarrier(ctx, migration.OwnerFence{AccountID: 7, Token: [32]byte(owner.Token), Epoch: owner.Epoch}, barrierCandidateFixture(19))
	if err != nil {
		t.Fatalf("ApplyPendingConfigurationBarrier() error = %v", err)
	}
	command := repository.lastActivationCommand()
	if boundary.LiveSessionID != 0 || boundary.BroadcastSessionID != 0 || command.OwnerToken != [32]byte(owner.Token) || command.OwnerEpoch != owner.Epoch {
		t.Fatalf("owned offline boundary=%#v command=%#v", boundary, command)
	}
}

func assertBarrierFailureRecovered(t *testing.T, active *activeSession, processor *barrierRecordingProcessor, giftID string) {
	t.Helper()
	admitted := make(chan struct{})
	go func() {
		sessionSink{active: active}.OnEvent(roomsource.Event{ID: giftID, RoomID: "42", Type: "SEND_GIFT"})
		close(admitted)
	}()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("barrier failure left admission closed")
	}
	processor.waitForGift(t, giftID)
	if got := processor.versionFor(giftID); got != 10 {
		t.Fatalf("gift after failed barrier used version %d, want 10", got)
	}
	if snapshots := processor.publishedSnapshots(); len(snapshots) != 0 {
		t.Fatalf("failed barrier published snapshots: %#v", snapshots)
	}
}

type barrierApplyResult struct {
	boundary Boundary
	err      error
}

func barrierCandidateFixture(jobID int64) migration.BarrierCandidate {
	return migration.BarrierCandidate{
		JobID:         jobID,
		Definition:    configuration.Definition{},
		Runtime:       configuration.RuntimeState{AttributeValues: map[string]float64{}, GiftTargetReceived: []configuration.GiftTargetRuntimeState{}, Activities: []configuration.ActivityRuntimeState{}},
		Operation:     configuration.BarrierMigrationApply,
		IntegritySeal: [32]byte{1},
	}
}

func timerRuleFixture() gameplay.TimerRule {
	return gameplay.TimerRule{ID: "tick", AttributeID: "clock", Formula: "1", IntervalSeconds: 1, Enabled: true}
}

type barrierMemoryRepository struct {
	mu                sync.Mutex
	activations       int
	activationStarted chan struct{}
	allowActivation   chan struct{}
	startOnce         sync.Once
	boundary          *configuration.Boundary
	activationErr     error
	lastCommand       configuration.BarrierCommand
}

type advancedBarrierStateRepository struct{}

func (advancedBarrierStateRepository) LoadActive(context.Context, int64) (configuration.Version, configuration.State, error) {
	return configuration.Version{ID: 11, AccountID: 7, Number: 3}, configuration.State{AccountID: 7, ConfigVersionID: 11, Revision: 5}, nil
}

func (advancedBarrierStateRepository) CommitRuntimeEvent(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
	return configuration.RuntimeEventResult{}, errors.New("unexpected runtime event")
}

func newBarrierMemoryRepository() *barrierMemoryRepository {
	return &barrierMemoryRepository{activationStarted: make(chan struct{}), allowActivation: make(chan struct{})}
}

func (repository *barrierMemoryRepository) LoadActive(context.Context, int64) (configuration.Version, configuration.State, error) {
	return configuration.Version{ID: 10, AccountID: 7, Number: 2}, configuration.State{AccountID: 7, ConfigVersionID: 10, Revision: 4}, nil
}

func (repository *barrierMemoryRepository) ActivateBarrier(ctx context.Context, command configuration.BarrierCommand) (configuration.Boundary, error) {
	repository.startOnce.Do(func() { close(repository.activationStarted) })
	select {
	case <-repository.allowActivation:
	case <-ctx.Done():
		return configuration.Boundary{}, ctx.Err()
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.boundary != nil {
		return *repository.boundary, nil
	}
	repository.activations++
	repository.lastCommand = command
	if repository.activationErr != nil {
		return configuration.Boundary{}, repository.activationErr
	}
	boundary := configuration.Boundary{
		AccountID: command.AccountID, MigrationJobID: command.MigrationJobID, Operation: command.Operation,
		OldConfigVersionID: command.ExpectedConfigVersionID, NewConfigVersionID: 11,
		BroadcastSessionID: command.BroadcastSessionID, LiveSessionID: command.LiveSessionID,
		LastOldRevision: command.ExpectedRevision, FirstNewRevision: command.ExpectedRevision + 1, AppliedAt: command.At,
	}
	repository.boundary = &boundary
	return boundary, nil
}

func (repository *barrierMemoryRepository) lastActivationCommand() configuration.BarrierCommand {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.lastCommand
}

func (repository *barrierMemoryRepository) activationCount() int {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.activations
}

func (repository *barrierMemoryRepository) setActivationError(err error) {
	repository.mu.Lock()
	repository.activationErr = err
	repository.mu.Unlock()
}

func (repository *barrierMemoryRepository) setBoundary(boundary configuration.Boundary) {
	repository.mu.Lock()
	repository.boundary = &boundary
	repository.mu.Unlock()
}

type barrierRecordingProcessor struct {
	mu                 sync.Mutex
	version            int64
	revision           uint64
	versions           map[string]int64
	accepted           chan string
	snapshots          []DisplaySnapshot
	prepareSnapshotErr error
	blockedEventID     string
	blockedEventErr    error
	blockedEventStart  chan struct{}
	blockedEventAllow  chan struct{}
	blockedStartOnce   sync.Once
	blockedAllowOnce   sync.Once
}

func newBarrierRecordingProcessor(version int64, revision uint64) *barrierRecordingProcessor {
	return &barrierRecordingProcessor{version: version, revision: revision, versions: make(map[string]int64), accepted: make(chan string, 16)}
}

func (processor *barrierRecordingProcessor) Accept(event roomsource.Event) error {
	processor.mu.Lock()
	blocked := event.ID == processor.blockedEventID && processor.blockedEventStart != nil && processor.blockedEventAllow != nil
	blockedStart, blockedAllow, blockedErr := processor.blockedEventStart, processor.blockedEventAllow, processor.blockedEventErr
	processor.mu.Unlock()
	if blocked {
		processor.blockedStartOnce.Do(func() { close(blockedStart) })
		<-blockedAllow
		return blockedErr
	}
	processor.mu.Lock()
	processor.versions[event.ID] = processor.version
	processor.revision++
	processor.mu.Unlock()
	processor.accepted <- event.ID
	return nil
}

func (processor *barrierRecordingProcessor) blockEvent(eventID string, err error) {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	processor.blockedEventID = eventID
	processor.blockedEventErr = err
	processor.blockedEventStart = make(chan struct{})
	processor.blockedEventAllow = make(chan struct{})
}

func (processor *barrierRecordingProcessor) waitForBlockedEvent(t *testing.T) {
	t.Helper()
	processor.mu.Lock()
	started := processor.blockedEventStart
	processor.mu.Unlock()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocked event did not reach processor")
	}
}

func (processor *barrierRecordingProcessor) releaseBlockedEvent() {
	processor.mu.Lock()
	allow := processor.blockedEventAllow
	processor.mu.Unlock()
	processor.blockedAllowOnce.Do(func() { close(allow) })
}

func (*barrierRecordingProcessor) Close(context.Context) error { return nil }
func (*barrierRecordingProcessor) Status() ProcessorStatus {
	return ProcessorStatus{ConnectionHealthy: true}
}
func (*barrierRecordingProcessor) SetConnectionHealthy(bool) {}

func (processor *barrierRecordingProcessor) configurationBarrierState(context.Context) (configurationBarrierProcessorState, error) {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return configurationBarrierProcessorState{ConfigVersionID: processor.version, ConfigVersion: 2, Revision: processor.revision}, nil
}

func (processor *barrierRecordingProcessor) prepareConfigurationSnapshot(candidate migration.BarrierCandidate, session Session, firstRevision uint64) (preparedBarrierSnapshot, error) {
	if processor.prepareSnapshotErr != nil {
		return nil, processor.prepareSnapshotErr
	}
	snapshot := DisplaySnapshot{AccountID: session.AccountID, LiveSessionID: session.ID, Revision: firstRevision, Runtime: candidate.Runtime}
	return preparedBarrierSnapshotFuncs{
		publish: func() {
			processor.mu.Lock()
			processor.snapshots = append(processor.snapshots, snapshot)
			processor.mu.Unlock()
		},
	}, nil
}

func (processor *barrierRecordingProcessor) applyConfigurationBoundary(candidate migration.BarrierCandidate, boundary configuration.Boundary) {
	processor.mu.Lock()
	processor.version = boundary.NewConfigVersionID
	processor.revision = boundary.FirstNewRevision
	processor.mu.Unlock()
}

func (processor *barrierRecordingProcessor) waitForGift(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-processor.accepted:
		if got != id {
			t.Fatalf("accepted gift = %q, want %q", got, id)
		}
	case <-time.After(time.Second):
		t.Fatalf("gift %q was not processed", id)
	}
}

func (processor *barrierRecordingProcessor) versionFor(id string) int64 {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return processor.versions[id]
}

func (processor *barrierRecordingProcessor) committedGiftCount() int {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return len(processor.versions)
}

func (processor *barrierRecordingProcessor) publishedSnapshots() []DisplaySnapshot {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return append([]DisplaySnapshot(nil), processor.snapshots...)
}

type barrierSubscription struct {
	mu         sync.Mutex
	cancels    int
	done       chan struct{}
	cancelOnce sync.Once
}

func newBarrierSubscription() *barrierSubscription {
	return &barrierSubscription{done: make(chan struct{})}
}
func (*barrierSubscription) RoomID() string { return "42" }
func (subscription *barrierSubscription) Cancel() {
	subscription.cancelOnce.Do(func() {
		subscription.mu.Lock()
		subscription.cancels++
		subscription.mu.Unlock()
		close(subscription.done)
	})
}
func (subscription *barrierSubscription) Done() <-chan struct{} { return subscription.done }
func (subscription *barrierSubscription) Wait(ctx context.Context) error {
	select {
	case <-subscription.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (subscription *barrierSubscription) cancelCount() int {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.cancels
}

func newBarrierManagerHarness(t *testing.T, repository *barrierMemoryRepository, accountID int64) (*Manager, *activeSession, *barrierRecordingProcessor) {
	t.Helper()
	manager := &Manager{
		dependencies:          Dependencies{Sessions: newMemorySessionRepository(), Configuration: repository},
		accounts:              make(map[int64]*accountRuntime),
		roomTransitions:       make(map[string]roomTransitionState),
		ownershipControl:      context.Background(),
		ownerOperationTimeout: time.Second,
		newTimer:              newSystemTimer,
		now:                   func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) },
	}
	_, active, processor := addBarrierAccount(t, manager, accountID)
	return manager, active, processor
}

func addBarrierAccount(t *testing.T, manager *Manager, accountID int64) (*accountRuntime, *activeSession, *barrierRecordingProcessor) {
	t.Helper()
	account := &accountRuntime{manager: manager, accountID: accountID, leases: make(map[uint64]LeaseKind)}
	processor := newBarrierRecordingProcessor(10, 3)
	managedProcessor := newManagedConfigurationBarrierProcessor(processor)
	active := &activeSession{
		account: account, owner: OwnerFence{AccountID: accountID, Token: OwnerToken{1}, Epoch: 1},
		session:   Session{ID: 81 + accountID - 7, BroadcastSessionID: 91, AccountID: accountID, RoomID: "42", ConfigVersionID: 10},
		processor: managedProcessor, subscription: newBarrierSubscription(), events: make(chan roomsource.Event, 16), workerDone: make(chan struct{}),
		admitting: true, workerStarted: true, sourceHealthy: true,
	}
	account.current = active
	manager.accounts[accountID] = account
	go active.run(manager, account)
	t.Cleanup(func() {
		active.admissionMu.Lock()
		if !active.eventsClosed {
			active.admitting = false
			close(active.events)
			active.eventsClosed = true
		}
		active.admissionMu.Unlock()
		<-active.workerDone
	})
	return account, active, processor
}

func waitForBarrierMarker(t *testing.T, active *activeSession) {
	t.Helper()
	managed, ok := active.processor.(*managedConfigurationBarrierProcessor)
	if !ok {
		t.Fatal("active processor is not barrier-managed")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		managed.mu.Lock()
		count := len(managed.markers)
		managed.mu.Unlock()
		if count != 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("barrier marker was not queued")
}

func waitForStaleCleanup(t *testing.T, account *accountRuntime, active *activeSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		account.mu.Lock()
		clean := !account.stale && account.current != active && account.transitionPending != active && account.staleDone == nil
		account.mu.Unlock()
		if clean {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stale ownership cleanup did not complete")
}
