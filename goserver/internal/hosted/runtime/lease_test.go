package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

func TestLeaseKindsShareOneExactIdleTimerAndReconnectCancelsIt(t *testing.T) {
	timers := &manualTimerFactory{}
	repository := newMemorySessionRepository()
	manager := newTestManager(t, repository, &fakeRoomSources{}, Options{NewTimer: timers.New})

	configLease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	obsLease, err := manager.Acquire(context.Background(), 7, LeaseOBS)
	if err != nil {
		t.Fatal(err)
	}
	configLease.Release()
	if got := timers.Count(); got != 0 {
		t.Fatalf("timer count after one of two leases released = %d, want 0", got)
	}
	obsLease.Release()
	timer := timers.Only(t)
	if timer.delay != 10*time.Minute {
		t.Fatalf("idle delay = %v, want exactly 10m", timer.delay)
	}
	if repository.endCalls() != 0 {
		t.Fatalf("session ended before timer fired")
	}

	reconnected, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !timer.Stopped() {
		t.Fatal("reconnect did not cancel idle timer")
	}
	timer.Fire()
	manager.waitAccountIdle(t, 7)
	if repository.endCalls() != 0 {
		t.Fatal("cancelled idle timer ended session")
	}

	reconnected.Release()
	second := timers.At(t, 1)
	second.Fire()
	manager.waitAccountIdle(t, 7)
	if repository.endCalls() != 0 {
		t.Fatalf("roomless runtime ended nonexistent session %d times", repository.endCalls())
	}
}

func TestAcquireAndRenewRejectDisabledAccountsBeforeLeaseMutation(t *testing.T) {
	repository := newMemorySessionRepository()
	repository.setEnabled(false)
	manager := newTestManager(t, repository, &fakeRoomSources{}, Options{})

	if _, err := manager.Acquire(context.Background(), 7, LeaseConfig); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("Acquire disabled error = %v", err)
	}
	if status, _ := manager.Status(context.Background(), 7); status.Leases != 0 {
		t.Fatalf("disabled Acquire created %d leases", status.Leases)
	}

	repository.setEnabled(true)
	lease, err := manager.Acquire(context.Background(), 8, LeaseOBS)
	if err != nil {
		t.Fatal(err)
	}
	repository.setEnabled(false)
	if err := lease.Renew(context.Background()); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("Renew disabled error = %v", err)
	}
	if status, _ := manager.Status(context.Background(), 8); status.Leases != 0 || status.State != StateDisabled {
		t.Fatalf("disabled renewal status = %#v", status)
	}
}

func TestAccountDisabledHookRejectsLeasesUntilAdministratorReenablesAccount(t *testing.T) {
	repository := newMemorySessionRepository()
	manager := newTestManager(t, repository, &fakeRoomSources{}, Options{})
	lease, err := manager.Acquire(context.Background(), 9, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}

	repository.setEnabled(false)
	manager.AccountDisabled(9)
	manager.waitAccountIdle(t, 9)
	if err := lease.Renew(context.Background()); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("old lease Renew error = %v", err)
	}
	if _, err := manager.Acquire(context.Background(), 9, LeaseOBS); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("Acquire after disable error = %v", err)
	}
	repository.setEnabled(true)
	reenabled, err := manager.Acquire(context.Background(), 9, LeaseConfig)
	if err != nil {
		t.Fatalf("Acquire after administrator enable error = %v", err)
	}
	reenabled.Release()
}

type manualTimerFactory struct {
	mu      sync.Mutex
	timers  []*manualTimer
	created chan *manualTimer
}

func (factory *manualTimerFactory) New(delay time.Duration) Timer {
	timer := &manualTimer{delay: delay, ch: make(chan time.Time, 1)}
	factory.mu.Lock()
	factory.timers = append(factory.timers, timer)
	factory.mu.Unlock()
	if factory.created != nil {
		factory.created <- timer
	}
	return timer
}

func (factory *manualTimerFactory) Count() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return len(factory.timers)
}

func (factory *manualTimerFactory) Only(t *testing.T) *manualTimer { return factory.At(t, 0) }
func (factory *manualTimerFactory) At(t *testing.T, index int) *manualTimer {
	t.Helper()
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.timers) <= index {
		t.Fatalf("timer %d missing; have %d", index, len(factory.timers))
	}
	return factory.timers[index]
}

type manualTimer struct {
	mu      sync.Mutex
	delay   time.Duration
	ch      chan time.Time
	stopped bool
}

func (timer *manualTimer) C() <-chan time.Time { return timer.ch }
func (timer *manualTimer) Stop() bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	wasActive := !timer.stopped
	timer.stopped = true
	return wasActive
}
func (timer *manualTimer) Stopped() bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	return timer.stopped
}
func (timer *manualTimer) Fire() {
	timer.mu.Lock()
	stopped := timer.stopped
	timer.mu.Unlock()
	if !stopped {
		timer.ch <- time.Unix(1, 0)
	}
}

type memorySessionRepository struct {
	mu      sync.Mutex
	enabled bool
	room    string
	ends    int
	owner   OwnerFence
	epoch   uint64
}

func newMemorySessionRepository() *memorySessionRepository {
	return &memorySessionRepository{enabled: true}
}
func (repository *memorySessionRepository) setEnabled(enabled bool) {
	repository.mu.Lock()
	repository.enabled = enabled
	repository.mu.Unlock()
}
func (repository *memorySessionRepository) AccountEnabled(context.Context, int64) (bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.enabled, nil
}
func (repository *memorySessionRepository) ClaimOwnership(_ context.Context, accountID int64, token OwnerToken, _ time.Duration) (OwnerClaim, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.enabled {
		return OwnerClaim{}, ErrAccountDisabled
	}
	if validOwnerFence(repository.owner) && repository.owner.Token != token {
		return OwnerClaim{}, ErrOwnershipConflict
	}
	reconcile := !validOwnerFence(repository.owner)
	if reconcile {
		repository.epoch++
		repository.owner = OwnerFence{AccountID: accountID, Token: token, Epoch: repository.epoch}
	}
	return OwnerClaim{Fence: repository.owner, Reconcile: false}, nil
}
func (repository *memorySessionRepository) RenewOwnership(_ context.Context, fence OwnerFence, _ time.Duration) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.enabled {
		return ErrAccountDisabled
	}
	if repository.owner != fence {
		return ErrOwnershipConflict
	}
	return nil
}
func (repository *memorySessionRepository) ReleaseOwnership(_ context.Context, fence OwnerFence) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.owner != fence {
		return ErrOwnershipConflict
	}
	repository.owner = OwnerFence{}
	return nil
}
func (repository *memorySessionRepository) TargetRoom(context.Context, int64) (string, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.room == "" {
		return "", ErrNoTargetRoom
	}
	return repository.room, nil
}
func (repository *memorySessionRepository) PersistTargetRoom(_ context.Context, command PersistTargetRoomCommand) error {
	repository.mu.Lock()
	if repository.owner != command.Owner {
		repository.mu.Unlock()
		return ErrOwnershipConflict
	}
	repository.room = command.RoomID
	repository.mu.Unlock()
	return nil
}
func (repository *memorySessionRepository) StartSession(_ context.Context, command StartSessionCommand) (Session, error) {
	return Session{ID: 1, AccountID: command.AccountID, RoomID: command.RoomID, ConfigVersionID: command.ConfigVersionID, StartedAt: command.StartedAt}, nil
}
func (repository *memorySessionRepository) EndSession(context.Context, EndSessionCommand) error {
	repository.mu.Lock()
	repository.ends++
	repository.mu.Unlock()
	return nil
}
func (repository *memorySessionRepository) PendingMigration(context.Context, int64) (int64, bool, error) {
	return 0, false, nil
}
func (repository *memorySessionRepository) endCalls() int {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.ends
}

type fakeConfiguration struct{}

func (fakeConfiguration) LoadActive(context.Context, int64) (configuration.Version, configuration.State, error) {
	return configuration.Version{ID: 1}, configuration.State{}, nil
}

type fakeMigration struct{}

func (fakeMigration) ApplyPendingAfterSession(context.Context, migration.OwnerFence, int64) (migration.Job, error) {
	return migration.Job{}, nil
}

type fakeRoomSources struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

func (sources *fakeRoomSources) Subscribe(context.Context, string, int64, roomsource.Sink) (roomsource.Subscription, error) {
	return nil, errors.New("unexpected subscribe")
}
func (sources *fakeRoomSources) Resolve(context.Context, string, int64) (string, error) {
	return "", errors.New("unexpected resolve")
}
func (sources *fakeRoomSources) SubscribeCanonical(context.Context, string, int64, roomsource.Sink) (roomsource.Subscription, error) {
	return nil, errors.New("unexpected subscribe")
}
func (sources *fakeRoomSources) Close() {
	sources.mu.Lock()
	defer sources.mu.Unlock()
	if !sources.closed {
		sources.closed = true
		if sources.done != nil {
			close(sources.done)
		}
	}
}
func (sources *fakeRoomSources) Wait(context.Context) error { return nil }

func newTestManager(t *testing.T, sessions SessionStore, sources RoomSources, options Options) *Manager {
	t.Helper()
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: fakeMigration{}, RoomSources: sources}, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager
}

func (manager *Manager) waitAccountIdle(t *testing.T, accountID int64) {
	t.Helper()
	account, err := manager.accountExisting(accountID)
	if err != nil {
		t.Fatal(err)
	}
	account.mu.Lock()
	done := account.idleDone
	if account.closeDone != nil {
		done = account.closeDone
	}
	account.mu.Unlock()
	if done != nil {
		<-done
	}
}
