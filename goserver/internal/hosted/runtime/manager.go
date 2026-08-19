package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

const idleRuntimeTimeout = 10 * time.Minute
const runtimeCloseRetry = time.Minute
const defaultShutdownRetryBackoff = 500 * time.Millisecond
const defaultOwnerTTL = 30 * time.Second
const defaultOwnerHeartbeatInterval = 10 * time.Second
const defaultOwnerOperationTimeout = 5 * time.Second

var (
	ErrInvalidInput    = errors.New("runtime: invalid input")
	ErrAccountDisabled = errors.New("runtime: account disabled")
	ErrUnavailable     = errors.New("runtime: unavailable")
	ErrClosed          = errors.New("runtime: closed")
)

const (
	StateIdle         = "idle"
	StateActive       = "active"
	StateDegraded     = "degraded"
	StateDisabled     = "disabled"
	StateShuttingDown = "shutting_down"
)

type SessionStore interface {
	ClaimOwnership(context.Context, int64, OwnerToken, time.Duration) (OwnerClaim, error)
	RenewOwnership(context.Context, OwnerFence, time.Duration) error
	ReleaseOwnership(context.Context, OwnerFence) error
	TargetRoom(context.Context, int64) (string, error)
	PersistTargetRoom(context.Context, PersistTargetRoomCommand) error
	StartSession(context.Context, StartSessionCommand) (Session, error)
	EndSession(context.Context, EndSessionCommand) error
	PendingMigration(context.Context, int64) (int64, bool, error)
}

type ConfigurationRepository interface {
	LoadActive(context.Context, int64) (configuration.Version, configuration.State, error)
}

type PendingMigrationApplier interface {
	ApplyPendingAfterSession(context.Context, migration.OwnerFence, int64) (migration.Job, error)
}

type RoomSources interface {
	Resolve(context.Context, string, int64) (string, error)
	SubscribeCanonical(context.Context, string, int64, roomsource.Sink) (roomsource.Subscription, error)
	Close()
	Wait(context.Context) error
}

type ProcessEvent func(context.Context, OwnerFence, Session, roomsource.Event) error

type Dependencies struct {
	Sessions      SessionStore
	Configuration ConfigurationRepository
	Migration     PendingMigrationApplier
	RoomSources   RoomSources
}

type Options struct {
	Now                   func() time.Time
	NewTimer              func(time.Duration) Timer
	NewHeartbeatTimer     func(time.Duration) Timer
	NewShutdownTimer      func(time.Duration) Timer
	Process               ProcessEvent
	ProcessorFactory      ProcessorFactory
	OwnerToken            OwnerToken
	OwnerTTL              time.Duration
	HeartbeatInterval     time.Duration
	OwnerOperationTimeout time.Duration
	ShutdownRetryBackoff  time.Duration
	BeforeSessionPublish  func()
}

type Manager struct {
	dependencies           Dependencies
	now                    func() time.Time
	newTimer               func(time.Duration) Timer
	processorFactory       ProcessorFactory
	ownerToken             OwnerToken
	ownerTTL               time.Duration
	heartbeat              time.Duration
	ownerOperationTimeout  time.Duration
	newHeartbeatTimer      func(time.Duration) Timer
	newShutdownTimer       func(time.Duration) Timer
	shutdownRetryBackoff   time.Duration
	beforeSessionPublish   func()
	heartbeatDone          chan struct{}
	ownershipControl       context.Context
	cancelOwnershipControl context.CancelFunc
	ownershipStop          chan struct{}
	ownershipStopOnce      sync.Once

	mu                sync.Mutex
	accounts          map[int64]*accountRuntime
	nextLease         uint64
	closed            bool
	closing           chan struct{}
	done              chan struct{}
	lifecycle         context.Context
	cancel            context.CancelFunc
	processing        context.Context
	cancelProcessing  context.CancelFunc
	processCancelOnce sync.Once
	closeOnce         sync.Once
	shutdownErr       error
}

type accountRuntime struct {
	manager        *Manager
	accountID      int64
	opMu           sync.Mutex
	mu             sync.Mutex
	leases         map[uint64]LeaseKind
	disabled       bool
	shutting       bool
	degraded       bool
	sourceDegraded bool
	current        *activeSession
	idleTimer      Timer
	idleCancel     chan struct{}
	idleDone       chan struct{}
	closeDone      chan struct{}
	stale          bool
	staleDone      chan struct{}
	staleRelease   OwnerFence
	owner          OwnerFence
	reconcile      bool
	operation      bool
}

type activeSession struct {
	account         *accountRuntime
	owner           OwnerFence
	session         Session
	processor       SessionProcessor
	subscription    roomsource.Subscription
	events          chan roomsource.Event
	workerDone      chan struct{}
	admissionMu     sync.Mutex
	admitting       bool
	workerStarted   bool
	eventsClosed    bool
	drained         bool
	endedAt         time.Time
	processorClosed bool
	sourceHealthy   bool
}

type Status struct {
	State               string `json:"state"`
	RoomID              string `json:"roomId,omitempty"`
	SessionID           int64  `json:"sessionId,omitempty"`
	Leases              int    `json:"leases"`
	ConfigLease         bool   `json:"configLease"`
	OBSLease            bool   `json:"obsLease"`
	Degraded            bool   `json:"degraded"`
	PersistenceBuffered int    `json:"persistenceBuffered,omitempty"`
	ConnectionHealthy   bool   `json:"connectionHealthy"`
}

// Metrics is identity-free. It never includes account, room, session, or viewer labels.
type Metrics struct {
	ActiveAccounts    uint64
	QueueDepth        uint64
	QueueDepthMax     uint64
	DegradedAccounts  uint64
	RejectingAccounts uint64
}

func (manager *Manager) Metrics() Metrics {
	if manager == nil {
		return Metrics{}
	}
	manager.mu.Lock()
	accounts := make([]*accountRuntime, 0, len(manager.accounts))
	for _, account := range manager.accounts {
		accounts = append(accounts, account)
	}
	manager.mu.Unlock()
	var metrics Metrics
	for _, account := range accounts {
		account.mu.Lock()
		if account.disabled || account.current == nil {
			account.mu.Unlock()
			continue
		}
		metrics.ActiveAccounts++
		depth := uint64(len(account.current.events))
		degraded := account.degraded || account.sourceDegraded
		rejecting := false
		if account.current.processor != nil {
			status := account.current.processor.Status()
			depth += uint64(status.Buffered)
			degraded = degraded || status.Degraded
			rejecting = status.Rejecting
		}
		account.mu.Unlock()
		metrics.QueueDepth += depth
		if depth > metrics.QueueDepthMax {
			metrics.QueueDepthMax = depth
		}
		if degraded {
			metrics.DegradedAccounts++
		}
		if rejecting {
			metrics.RejectingAccounts++
		}
	}
	return metrics
}

type operationContext struct {
	values    context.Context
	lifecycle context.Context
}

func (ctx operationContext) Deadline() (time.Time, bool) { return ctx.lifecycle.Deadline() }
func (ctx operationContext) Done() <-chan struct{}       { return ctx.lifecycle.Done() }
func (ctx operationContext) Err() error                  { return ctx.lifecycle.Err() }
func (ctx operationContext) Value(key any) any           { return ctx.values.Value(key) }

func NewManager(dependencies Dependencies, options Options) (*Manager, error) {
	if dependencies.Sessions == nil || dependencies.Configuration == nil || dependencies.Migration == nil || dependencies.RoomSources == nil {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewTimer == nil {
		options.NewTimer = newSystemTimer
	}
	if options.NewHeartbeatTimer == nil {
		options.NewHeartbeatTimer = newSystemTimer
	}
	if options.NewShutdownTimer == nil {
		options.NewShutdownTimer = newSystemTimer
	}
	if options.OwnerTTL == 0 {
		options.OwnerTTL = defaultOwnerTTL
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = defaultOwnerHeartbeatInterval
	}
	if options.OwnerOperationTimeout == 0 {
		options.OwnerOperationTimeout = defaultOwnerOperationTimeout
	}
	if options.ShutdownRetryBackoff == 0 {
		options.ShutdownRetryBackoff = defaultShutdownRetryBackoff
	}
	if options.OwnerTTL <= 0 || options.HeartbeatInterval <= 0 || options.HeartbeatInterval >= options.OwnerTTL || options.OwnerOperationTimeout <= 0 || options.ShutdownRetryBackoff <= 0 {
		return nil, ErrInvalidInput
	}
	remaining := options.OwnerTTL - options.HeartbeatInterval
	if options.HeartbeatInterval >= remaining || options.OwnerOperationTimeout >= remaining-options.HeartbeatInterval {
		return nil, ErrInvalidInput
	}
	if options.OwnerToken == (OwnerToken{}) {
		var err error
		options.OwnerToken, err = NewOwnerToken(rand.Reader)
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	if options.Process != nil && options.ProcessorFactory != nil {
		return nil, ErrInvalidInput
	}
	if options.Process == nil && options.ProcessorFactory == nil {
		options.Process = func(context.Context, OwnerFence, Session, roomsource.Event) error { return nil }
	}
	if options.ProcessorFactory == nil {
		options.ProcessorFactory = processEventProcessorFactory{process: options.Process}
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	processing, cancelProcessing := context.WithCancel(context.Background())
	ownershipControl, cancelOwnershipControl := context.WithCancel(context.Background())
	manager := &Manager{dependencies: dependencies, now: options.Now, newTimer: options.NewTimer, processorFactory: options.ProcessorFactory, ownerToken: options.OwnerToken, ownerTTL: options.OwnerTTL, heartbeat: options.HeartbeatInterval, ownerOperationTimeout: options.OwnerOperationTimeout, newHeartbeatTimer: options.NewHeartbeatTimer, newShutdownTimer: options.NewShutdownTimer, shutdownRetryBackoff: options.ShutdownRetryBackoff, beforeSessionPublish: options.BeforeSessionPublish, heartbeatDone: make(chan struct{}), ownershipControl: ownershipControl, cancelOwnershipControl: cancelOwnershipControl, ownershipStop: make(chan struct{}), accounts: make(map[int64]*accountRuntime), closing: make(chan struct{}), done: make(chan struct{}), lifecycle: lifecycle, cancel: cancel, processing: processing, cancelProcessing: cancelProcessing}
	go manager.runHeartbeat()
	return manager, nil
}

func (manager *Manager) Acquire(ctx context.Context, accountID int64, kind LeaseKind) (ConnectionLease, error) {
	if manager == nil || ctx == nil || accountID <= 0 || !validLeaseKind(kind) {
		return nil, ErrInvalidLease
	}
	account, err := manager.account(accountID)
	if err != nil {
		return nil, err
	}
	return manager.acquireKnownAccount(ctx, accountID, kind, account)
}

func (manager *Manager) acquireKnownAccount(ctx context.Context, accountID int64, kind LeaseKind, account *accountRuntime) (ConnectionLease, error) {
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return nil, ErrUnavailable
	}
	if account.shutting {
		account.mu.Unlock()
		return nil, ErrClosed
	}
	if account.disabled && account.current != nil {
		account.mu.Unlock()
		return nil, ErrAccountDisabled
	}
	account.mu.Unlock()
	account.opMu.Lock()
	defer account.opMu.Unlock()
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return nil, ErrUnavailable
	}
	if account.shutting {
		account.mu.Unlock()
		return nil, ErrClosed
	}
	if account.disabled && account.current != nil {
		account.mu.Unlock()
		return nil, ErrAccountDisabled
	}
	account.mu.Unlock()
	claim, err := manager.dependencies.Sessions.ClaimOwnership(ctx, accountID, manager.ownerToken, manager.ownerTTL)
	if err != nil {
		if errors.Is(err, ErrAccountDisabled) {
			manager.markDisabledLocked(account)
		} else if errors.Is(err, ErrOwnershipConflict) {
			manager.beginStaleOnConflictLocked(account)
		}
		return nil, err
	}
	account.mu.Lock()
	active := account.current
	stale := account.stale
	account.owner = claim.Fence
	account.reconcile = claim.Reconcile
	account.mu.Unlock()
	if stale {
		manager.beginStaleCleanupLocked(account, active, claim.Fence)
		return nil, ErrUnavailable
	}
	if active != nil && active.owner != claim.Fence {
		manager.beginStaleCleanupLocked(account, active, claim.Fence)
		return nil, ErrUnavailable
	}
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return nil, ErrUnavailable
	}
	if account.disabled {
		if account.current != nil {
			account.mu.Unlock()
			return nil, ErrAccountDisabled
		}
		account.disabled = false
		account.degraded = false
		account.closeDone = nil
	}
	if account.shutting {
		account.mu.Unlock()
		return nil, ErrClosed
	}
	account.cancelIdleLocked()
	needsCloseRetry := account.current != nil && account.current.drained
	account.mu.Unlock()
	if needsCloseRetry {
		if err := manager.closeCurrent(ctx, account); err != nil {
			if errors.Is(err, ErrOwnershipConflict) {
				return nil, ErrUnavailable
			}
			account.mu.Lock()
			account.degraded = true
			account.scheduleCloseLocked(runtimeCloseRetry)
			account.mu.Unlock()
			return nil, err
		}
	}
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return nil, ErrUnavailable
	}
	if account.disabled {
		account.mu.Unlock()
		return nil, ErrAccountDisabled
	}
	if account.shutting {
		account.mu.Unlock()
		return nil, ErrClosed
	}
	manager.mu.Lock()
	manager.nextLease++
	leaseID := manager.nextLease
	manager.mu.Unlock()
	account.leases[leaseID] = kind
	start := account.current == nil
	account.mu.Unlock()
	if start {
		if err := manager.startPersisted(ctx, account); err != nil {
			if errors.Is(err, ErrOwnershipConflict) {
				account.mu.Lock()
				active := account.current
				account.mu.Unlock()
				manager.beginStaleCleanupLocked(account, active, OwnerFence{})
			}
			account.mu.Lock()
			delete(account.leases, leaseID)
			noPresence := len(account.leases) == 0 && account.current == nil
			fence := account.owner
			account.mu.Unlock()
			if noPresence {
				if releaseErr := manager.releaseOwner(ctx, account, fence); releaseErr != nil {
					account.markDegraded()
				}
			}
			return nil, err
		}
	}
	return &Lease{manager: manager, accountID: accountID, id: leaseID, kind: kind}, nil
}

func (manager *Manager) renew(ctx context.Context, lease *Lease) error {
	account, err := manager.account(lease.accountID)
	if err != nil {
		return err
	}
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return ErrUnavailable
	}
	account.mu.Unlock()
	account.opMu.Lock()
	defer account.opMu.Unlock()
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return ErrUnavailable
	}
	if account.disabled {
		account.mu.Unlock()
		return ErrAccountDisabled
	}
	if account.shutting {
		account.mu.Unlock()
		return ErrClosed
	}
	if kind, ok := account.leases[lease.id]; !ok || kind != lease.kind {
		account.mu.Unlock()
		return ErrInvalidLease
	}
	fence := account.owner
	account.mu.Unlock()
	if err := manager.dependencies.Sessions.RenewOwnership(ctx, fence, manager.ownerTTL); err != nil {
		if errors.Is(err, ErrAccountDisabled) {
			manager.markDisabledLocked(account)
		} else if errors.Is(err, ErrOwnershipConflict) {
			manager.beginStaleOnConflictLocked(account)
		}
		return err
	}
	return nil
}

func (manager *Manager) release(lease *Lease) {
	account, err := manager.accountExisting(lease.accountID)
	if err != nil {
		return
	}
	account.mu.Lock()
	if kind, ok := account.leases[lease.id]; ok && kind == lease.kind {
		delete(account.leases, lease.id)
		if len(account.leases) == 0 && !account.disabled && !account.shutting {
			account.scheduleIdleLocked()
		}
	}
	account.mu.Unlock()
}

func (account *accountRuntime) scheduleIdleLocked() {
	account.scheduleCloseLocked(idleRuntimeTimeout)
}

func (account *accountRuntime) scheduleCloseLocked(delay time.Duration) {
	if account.idleTimer != nil || account.disabled || account.shutting || len(account.leases) != 0 {
		return
	}
	timer := account.manager.newTimer(delay)
	cancel := make(chan struct{})
	done := make(chan struct{})
	account.idleTimer, account.idleCancel, account.idleDone = timer, cancel, done
	go func() {
		defer close(done)
		select {
		case <-timer.C():
		case <-cancel:
			timer.Stop()
			return
		case <-account.manager.closing:
			timer.Stop()
			return
		}
		account.opMu.Lock()
		defer account.opMu.Unlock()
		account.mu.Lock()
		if account.idleCancel != cancel || len(account.leases) != 0 || account.disabled || account.shutting {
			account.mu.Unlock()
			return
		}
		account.idleTimer, account.idleCancel = nil, nil
		account.mu.Unlock()
		ctx, cancel := account.manager.ownerOperationContext()
		err := account.manager.closeCurrent(ctx, account)
		cancel()
		if errors.Is(err, ErrOwnershipConflict) {
			err = nil
		}
		if err == nil {
			account.mu.Lock()
			fence := account.owner
			account.mu.Unlock()
			if validOwnerFence(fence) {
				ctx, cancel = account.manager.ownerOperationContext()
				err = account.manager.releaseOwner(ctx, account, fence)
				cancel()
			}
		}
		account.mu.Lock()
		if err != nil {
			account.degraded = true
			if len(account.leases) == 0 && !account.disabled && !account.shutting {
				account.scheduleCloseLocked(runtimeCloseRetry)
			}
		} else if account.current == nil {
			account.degraded = false
		}
		account.mu.Unlock()
	}()
}

func (account *accountRuntime) cancelIdleLocked() {
	if account.idleCancel != nil {
		close(account.idleCancel)
		account.idleCancel = nil
	}
	if account.idleTimer != nil {
		account.idleTimer.Stop()
		account.idleTimer = nil
	}
}

func (manager *Manager) Status(ctx context.Context, accountID int64) (Status, error) {
	if manager == nil || ctx == nil || accountID <= 0 {
		return Status{}, ErrInvalidInput
	}
	account, err := manager.accountExisting(accountID)
	if err != nil {
		return Status{State: StateIdle}, nil
	}
	account.mu.Lock()
	defer account.mu.Unlock()
	status := Status{State: StateIdle, Leases: len(account.leases), Degraded: account.degraded}
	if account.current != nil && account.current.processor != nil {
		processorStatus := account.current.processor.Status()
		status.Degraded = status.Degraded || account.sourceDegraded || processorStatus.Degraded
		status.PersistenceBuffered = processorStatus.Buffered
		status.ConnectionHealthy = processorStatus.ConnectionHealthy
	}
	for _, kind := range account.leases {
		status.ConfigLease = status.ConfigLease || kind == LeaseConfig
		status.OBSLease = status.OBSLease || kind == LeaseOBS
	}
	if account.disabled {
		status.State = StateDisabled
	} else if account.shutting {
		status.State = StateShuttingDown
	} else if status.Degraded {
		status.State = StateDegraded
	} else if account.current != nil {
		status.State = StateActive
	}
	if account.current != nil {
		status.RoomID = account.current.session.RoomID
		status.SessionID = account.current.session.ID
	}
	return status, nil
}

func (manager *Manager) Snapshot(ctx context.Context, accountID int64) (configuration.RuntimeState, error) {
	if manager == nil || ctx == nil || accountID <= 0 {
		return configuration.RuntimeState{}, ErrInvalidInput
	}
	_, state, err := manager.dependencies.Configuration.LoadActive(ctx, accountID)
	if err != nil {
		return configuration.RuntimeState{}, ErrUnavailable
	}
	return state.Runtime, nil
}

func (manager *Manager) SetRoom(ctx context.Context, accountID int64, roomID string) (resultErr error) {
	roomID = strings.TrimSpace(roomID)
	if manager == nil || ctx == nil || accountID <= 0 || !validRoomID(roomID) {
		return ErrInvalidInput
	}
	account, err := manager.account(accountID)
	if err != nil {
		return err
	}
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return ErrUnavailable
	}
	if account.shutting {
		account.mu.Unlock()
		return ErrClosed
	}
	account.mu.Unlock()
	account.opMu.Lock()
	defer account.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	operationContext := operationContext{values: context.WithoutCancel(ctx), lifecycle: manager.lifecycle}
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return ErrUnavailable
	}
	if account.shutting {
		account.mu.Unlock()
		return ErrClosed
	}
	wasActive := account.current != nil
	hasPresence := len(account.leases) != 0
	active := account.current
	account.mu.Unlock()
	claim, err := manager.dependencies.Sessions.ClaimOwnership(operationContext, accountID, manager.ownerToken, manager.ownerTTL)
	if err != nil {
		if errors.Is(err, ErrAccountDisabled) {
			manager.markDisabledLocked(account)
		} else if errors.Is(err, ErrOwnershipConflict) {
			manager.beginStaleOnConflictLocked(account)
		}
		return err
	}
	account.mu.Lock()
	stale := account.stale
	account.owner = claim.Fence
	account.reconcile = claim.Reconcile
	if stale {
		account.mu.Unlock()
		manager.beginStaleCleanupLocked(account, active, claim.Fence)
		return ErrUnavailable
	}
	account.disabled = false
	account.degraded = false
	account.closeDone = nil
	account.operation = true
	account.mu.Unlock()
	defer func() {
		account.mu.Lock()
		account.operation = false
		account.mu.Unlock()
	}()
	temporaryClaim := !wasActive && !hasPresence
	if temporaryClaim {
		defer func() {
			if err := manager.releaseOwner(operationContext, account, claim.Fence); err != nil {
				account.markDegraded()
				if resultErr == nil {
					resultErr = ErrUnavailable
				}
			}
		}()
	}
	if active != nil && active.owner != claim.Fence {
		manager.beginStaleCleanupLocked(account, active, claim.Fence)
		return ErrUnavailable
	}
	canonical, err := manager.dependencies.RoomSources.Resolve(operationContext, roomID, accountID)
	if err != nil || !validRoomID(canonical) {
		return ErrUnavailable
	}
	account.mu.Lock()
	stale = account.stale
	account.mu.Unlock()
	if stale {
		return ErrUnavailable
	}
	if err := manager.closeCurrent(operationContext, account); err != nil {
		if errors.Is(err, ErrOwnershipConflict) {
			return ErrUnavailable
		}
		account.markDegraded()
		return err
	}
	jobID, pending, err := manager.dependencies.Sessions.PendingMigration(operationContext, accountID)
	if err != nil {
		account.markDegraded()
		return ErrUnavailable
	}
	if pending {
		migrationOwner := migration.OwnerFence{AccountID: claim.Fence.AccountID, Token: [32]byte(claim.Fence.Token), Epoch: claim.Fence.Epoch}
		if err := manager.applyPendingMigration(operationContext, migrationOwner, jobID); err != nil {
			if errors.Is(err, ErrOwnershipConflict) {
				manager.beginStaleCleanupLocked(account, nil, OwnerFence{})
				return ErrUnavailable
			}
			account.markDegraded()
			return ErrUnavailable
		}
	}
	account.mu.Lock()
	stale = account.stale
	account.mu.Unlock()
	if stale {
		return ErrUnavailable
	}
	if err := manager.startRoom(operationContext, account, canonical, true, wasActive || hasPresence); err != nil {
		if errors.Is(err, ErrOwnershipConflict) {
			account.mu.Lock()
			active := account.current
			account.mu.Unlock()
			manager.beginStaleCleanupLocked(account, active, OwnerFence{})
			return ErrUnavailable
		}
		account.markDegraded()
		return err
	}
	return nil
}

func (manager *Manager) applyPendingMigration(ctx context.Context, owner migration.OwnerFence, jobID int64) error {
	_, err := manager.dependencies.Migration.ApplyPendingAfterSession(ctx, owner, jobID)
	if errors.Is(err, migration.ErrOwnershipConflict) {
		return ErrOwnershipConflict
	}
	return err
}

func (account *accountRuntime) markDegraded() {
	account.mu.Lock()
	account.degraded = true
	account.mu.Unlock()
}

func validRoomID(value string) bool {
	if len(value) == 0 || len(value) > 20 || value[0] == '0' {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

func (manager *Manager) AccountDisabled(accountID int64) {
	if manager == nil || accountID <= 0 {
		return
	}
	account, err := manager.account(accountID)
	if err != nil {
		return
	}
	go func() {
		account.opMu.Lock()
		manager.markDisabledLocked(account)
		account.opMu.Unlock()
	}()
}

// markDisabledLocked serializes the post-commit disable hook with ownership
// claims. The caller must hold account.opMu.
func (manager *Manager) markDisabledLocked(account *accountRuntime) {
	account.mu.Lock()
	if account.stale {
		account.disabled = true
		account.leases = make(map[uint64]LeaseKind)
		account.cancelIdleLocked()
		account.closeDone = account.staleDone
		account.mu.Unlock()
		return
	}
	if account.disabled {
		account.mu.Unlock()
		return
	}
	account.disabled = true
	account.leases = make(map[uint64]LeaseKind)
	account.cancelIdleLocked()
	done := make(chan struct{})
	account.closeDone = done
	account.mu.Unlock()
	go manager.drainDisabled(account, done)
}

func (manager *Manager) drainDisabled(account *accountRuntime, done chan struct{}) {
	defer close(done)
	for {
		account.opMu.Lock()
		account.mu.Lock()
		stillDisabled := account.disabled && account.closeDone == done
		account.mu.Unlock()
		if !stillDisabled {
			account.opMu.Unlock()
			return
		}
		ctx, cancel := manager.ownerOperationContext()
		err := manager.closeCurrent(ctx, account)
		cancel()
		if errors.Is(err, ErrOwnershipConflict) {
			account.opMu.Unlock()
			return
		}
		account.mu.Lock()
		fence := account.owner
		account.mu.Unlock()
		if err == nil && validOwnerFence(fence) {
			ctx, cancel = manager.ownerOperationContext()
			err = manager.releaseOwner(ctx, account, fence)
			cancel()
		}
		account.opMu.Unlock()
		if err == nil {
			return
		}
		account.mu.Lock()
		account.degraded = true
		account.mu.Unlock()
		timer := manager.newTimer(runtimeCloseRetry)
		select {
		case <-timer.C():
			timer.Stop()
		case <-manager.closing:
			timer.Stop()
			return
		}
	}
}

func (manager *Manager) releaseOwner(ctx context.Context, account *accountRuntime, fence OwnerFence) error {
	if !validOwnerFence(fence) {
		return nil
	}
	err := manager.dependencies.Sessions.ReleaseOwnership(ctx, fence)
	if err != nil && !errors.Is(err, ErrOwnershipConflict) {
		return err
	}
	account.mu.Lock()
	if account.owner == fence {
		account.owner = OwnerFence{}
		account.reconcile = false
	}
	account.mu.Unlock()
	return nil
}

func (manager *Manager) releaseOwnerDuringShutdown(ctx context.Context, account *accountRuntime, fence OwnerFence) error {
	for {
		if err := manager.releaseOwner(ctx, account, fence); err == nil {
			return nil
		}
		account.markDegraded()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := manager.newTimer(manager.heartbeat)
		select {
		case <-timer.C():
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func (manager *Manager) closeCurrentDuringShutdown(ctx context.Context, account *accountRuntime) error {
	for {
		err := manager.closeCurrent(ctx, account)
		if err == nil || errors.Is(err, ErrOwnershipConflict) {
			return err
		}
		account.markDegraded()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay, retry := manager.shutdownRetryDelay(ctx)
		if !retry {
			<-ctx.Done()
			return ctx.Err()
		}
		timer := manager.newShutdownTimer(delay)
		select {
		case <-timer.C():
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func (manager *Manager) shutdownRetryDelay(ctx context.Context) (time.Duration, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	delay := manager.shutdownRetryBackoff
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= delay {
			return 0, false
		}
	}
	return delay, true
}

// beginStaleCleanupLocked revokes process-local use of a fence that the
// repository has rejected. The caller holds account.opMu. Cleanup never ends
// the persisted session because its captured fence is already stale.
func (manager *Manager) beginStaleOnConflictLocked(account *accountRuntime) {
	account.mu.Lock()
	active := account.current
	hasLocalState := active != nil || len(account.leases) != 0 || account.operation
	expectedOwner := account.owner
	account.mu.Unlock()
	if hasLocalState {
		manager.beginStaleCleanup(account, active, expectedOwner, OwnerFence{})
	}
}

func (manager *Manager) beginStaleCleanupLocked(account *accountRuntime, active *activeSession, releaseFence OwnerFence) {
	manager.beginStaleCleanup(account, active, OwnerFence{}, releaseFence)
}

func (manager *Manager) beginStaleCleanup(account *accountRuntime, active *activeSession, expectedOwner, releaseFence OwnerFence) {
	account.mu.Lock()
	if validOwnerFence(expectedOwner) && account.owner != expectedOwner {
		account.mu.Unlock()
		return
	}
	if active == nil {
		active = account.current
	}
	if active != nil && account.current != active {
		account.mu.Unlock()
		return
	}
	if account.stale {
		if validOwnerFence(releaseFence) && !validOwnerFence(account.staleRelease) {
			account.staleRelease = releaseFence
		}
		account.mu.Unlock()
		return
	}
	account.stale = true
	account.degraded = true
	account.leases = make(map[uint64]LeaseKind)
	account.cancelIdleLocked()
	account.staleRelease = releaseFence
	done := make(chan struct{})
	account.staleDone = done
	account.mu.Unlock()

	if active != nil {
		active.admissionMu.Lock()
		if active.admitting {
			active.admitting = false
			active.subscription.Cancel()
		}
		active.admissionMu.Unlock()
	}
	go manager.finishStaleCleanup(account, active, releaseFence, done)
}

func (manager *Manager) finishStaleCleanup(account *accountRuntime, active *activeSession, releaseFence OwnerFence, done chan struct{}) {
	account.opMu.Lock()
	defer account.opMu.Unlock()
	account.mu.Lock()
	if account.staleDone != done {
		account.mu.Unlock()
		return
	}
	releaseFence = account.staleRelease
	account.mu.Unlock()
	released := make(chan bool, 1)
	go func() { released <- manager.releaseUnusableOwner(account, releaseFence) }()
	drained := manager.drainStaleActive(active)
	releaseSucceeded := <-released

	account.mu.Lock()
	defer account.mu.Unlock()
	if account.staleDone != done {
		return
	}
	if drained && account.current == active {
		account.current = nil
	}
	if drained && releaseSucceeded {
		if !validOwnerFence(releaseFence) {
			account.owner = OwnerFence{}
			account.reconcile = false
		}
		account.stale = false
		account.degraded = false
		account.staleRelease = OwnerFence{}
	}
	close(done)
	account.staleDone = nil
}

func (manager *Manager) drainStaleActive(active *activeSession) bool {
	if active == nil {
		return true
	}
	for {
		active.admissionMu.Lock()
		eventsClosed := active.eventsClosed
		active.admissionMu.Unlock()
		if eventsClosed {
			break
		}
		select {
		case <-active.subscription.Done():
			active.admissionMu.Lock()
			if !active.eventsClosed {
				close(active.events)
				active.eventsClosed = true
			}
			active.admissionMu.Unlock()
			break
		default:
			ctx, cancel := manager.ownerOperationContext()
			err := active.subscription.Wait(ctx)
			cancel()
			if err == nil {
				continue
			}
			if manager.ownershipControl.Err() != nil {
				return false
			}
			timer := manager.newTimer(manager.heartbeat)
			select {
			case <-timer.C():
				timer.Stop()
			case <-manager.ownershipControl.Done():
				timer.Stop()
				return false
			}
			continue
		}
		break
	}
	active.admissionMu.Lock()
	workerStarted := active.workerStarted
	if !workerStarted {
		active.drained = true
	}
	drained := active.drained
	active.admissionMu.Unlock()
	if drained {
		return true
	}
	select {
	case <-active.workerDone:
		active.admissionMu.Lock()
		active.drained = true
		active.admissionMu.Unlock()
		return true
	case <-manager.ownershipControl.Done():
		return false
	}
}

func (manager *Manager) releaseUnusableOwner(account *accountRuntime, fence OwnerFence) bool {
	if !validOwnerFence(fence) {
		return true
	}
	for {
		ctx, cancel := manager.ownerOperationContext()
		err := manager.releaseOwner(ctx, account, fence)
		cancel()
		if err == nil {
			return true
		}
		if manager.ownershipControl.Err() != nil {
			return false
		}
		timer := manager.newTimer(manager.heartbeat)
		select {
		case <-timer.C():
			timer.Stop()
		case <-manager.ownershipControl.Done():
			timer.Stop()
			return false
		}
	}
}

func (manager *Manager) waitStaleCleanup(ctx context.Context, account *accountRuntime) error {
	account.mu.Lock()
	if !account.stale {
		account.mu.Unlock()
		return nil
	}
	done := account.staleDone
	account.mu.Unlock()
	if done == nil {
		return ErrUnavailable
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	account.mu.Lock()
	stale := account.stale
	account.mu.Unlock()
	if stale {
		return ErrUnavailable
	}
	return nil
}

func (manager *Manager) ownerOperationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(manager.ownershipControl, manager.ownerOperationTimeout)
}

func (manager *Manager) stopOwnershipControl() {
	manager.stopOwnershipHeartbeat()
	manager.cancelOwnershipControl()
}

func (manager *Manager) stopOwnershipHeartbeat() {
	manager.ownershipStopOnce.Do(func() {
		close(manager.ownershipStop)
	})
}

func (manager *Manager) runHeartbeat() {
	defer close(manager.heartbeatDone)
	for {
		timer := manager.newHeartbeatTimer(manager.heartbeat)
		select {
		case <-timer.C():
			timer.Stop()
			manager.heartbeatOwners()
		case <-manager.ownershipStop:
			timer.Stop()
			return
		case <-manager.ownershipControl.Done():
			timer.Stop()
			return
		}
	}
}

func (manager *Manager) heartbeatOwners() {
	manager.mu.Lock()
	accounts := make([]*accountRuntime, 0, len(manager.accounts))
	for _, account := range manager.accounts {
		accounts = append(accounts, account)
	}
	manager.mu.Unlock()
	var renewals sync.WaitGroup
	for _, account := range accounts {
		account.mu.Lock()
		fence := account.owner
		active := validOwnerFence(fence) && (account.shutting || account.disabled || len(account.leases) != 0 || account.current != nil || account.operation)
		account.mu.Unlock()
		if !active {
			continue
		}
		renewals.Add(1)
		go func(account *accountRuntime, fence OwnerFence) {
			defer renewals.Done()
			ctx, cancel := manager.ownerOperationContext()
			err := manager.dependencies.Sessions.RenewOwnership(ctx, fence, manager.ownerTTL)
			cancel()
			if err == nil || manager.ownershipControl.Err() != nil {
				return
			}
			if errors.Is(err, ErrOwnershipConflict) {
				account.mu.Lock()
				active := account.current
				account.mu.Unlock()
				manager.beginStaleCleanup(account, active, fence, OwnerFence{})
				return
			}
			account.opMu.Lock()
			defer account.opMu.Unlock()
			account.mu.Lock()
			stillCurrent := account.owner == fence
			account.mu.Unlock()
			if !stillCurrent {
				return
			}
			switch {
			case errors.Is(err, ErrAccountDisabled):
				manager.markDisabledLocked(account)
			default:
				account.markDegraded()
			}
		}(account, fence)
	}
	renewals.Wait()
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidInput
	}
	entryErr := ctx.Err()
	manager.closeOnce.Do(func() {
		manager.cancel()
		manager.mu.Lock()
		manager.closed = true
		close(manager.closing)
		accounts := make([]*accountRuntime, 0, len(manager.accounts))
		for _, account := range manager.accounts {
			accounts = append(accounts, account)
		}
		manager.mu.Unlock()
		for _, account := range accounts {
			account.mu.Lock()
			account.shutting = true
			account.leases = make(map[uint64]LeaseKind)
			account.cancelIdleLocked()
			account.mu.Unlock()
		}
		go func() {
			select {
			case <-ctx.Done():
				manager.stopOwnershipControl()
				manager.processCancelOnce.Do(manager.cancelProcessing)
			case <-manager.done:
			}
		}()
		go func() {
			var workers sync.WaitGroup
			failures := make(chan struct{}, 2*len(accounts)+1)
			for _, account := range accounts {
				workers.Add(1)
				go func(account *accountRuntime) {
					defer workers.Done()
					for {
						if err := manager.waitStaleCleanup(ctx, account); err != nil {
							failures <- struct{}{}
							return
						}
						account.opMu.Lock()
						account.mu.Lock()
						stale := account.stale
						account.mu.Unlock()
						if !stale {
							break
						}
						account.opMu.Unlock()
					}
					if err := manager.closeCurrentDuringShutdown(ctx, account); err != nil && !errors.Is(err, ErrOwnershipConflict) {
						failures <- struct{}{}
					}
					account.mu.Lock()
					fence := account.owner
					account.mu.Unlock()
					if validOwnerFence(fence) {
						if err := manager.releaseOwnerDuringShutdown(ctx, account, fence); err != nil {
							failures <- struct{}{}
						}
					}
					account.opMu.Unlock()
				}(account)
			}
			workers.Wait()
			manager.stopOwnershipHeartbeat()
			<-manager.heartbeatDone
			for _, account := range accounts {
				if err := manager.waitStaleCleanup(ctx, account); err != nil {
					failures <- struct{}{}
				}
			}
			manager.processCancelOnce.Do(manager.cancelProcessing)
			manager.stopOwnershipControl()
			manager.dependencies.RoomSources.Close()
			if err := manager.dependencies.RoomSources.Wait(ctx); err != nil && ctx.Err() == nil {
				failures <- struct{}{}
			}
			close(failures)
			if _, failed := <-failures; failed {
				manager.mu.Lock()
				manager.shutdownErr = ErrUnavailable
				manager.mu.Unlock()
			}
			close(manager.done)
		}()
	})
	err := manager.Wait(ctx)
	if entryErr != nil {
		manager.stopOwnershipControl()
		manager.processCancelOnce.Do(manager.cancelProcessing)
		return entryErr
	}
	if err != nil && ctx.Err() != nil {
		manager.stopOwnershipControl()
		manager.processCancelOnce.Do(manager.cancelProcessing)
	}
	return err
}

func (manager *Manager) Wait(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidInput
	}
	select {
	case <-manager.done:
		manager.mu.Lock()
		err := manager.shutdownErr
		manager.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) account(accountID int64) (*accountRuntime, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil, ErrClosed
	}
	account := manager.accounts[accountID]
	if account == nil {
		account = &accountRuntime{manager: manager, accountID: accountID, leases: make(map[uint64]LeaseKind)}
		manager.accounts[accountID] = account
	}
	return account, nil
}

func (manager *Manager) accountExisting(accountID int64) (*accountRuntime, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	account := manager.accounts[accountID]
	if account == nil {
		return nil, ErrInvalidInput
	}
	return account, nil
}
