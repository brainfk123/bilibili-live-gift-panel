package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	"bilibili-live-gift-panel/internal/hosted/roomwatcher"
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

// roomEventSessionStore resolves only the business broadcast selected by a
// durable state event. Account membership comes exclusively from the ordered
// RoomReferencesChanged payload, never from a second database read path.
type roomEventSessionStore interface {
	OpenBroadcastSession(context.Context, string) (int64, error)
}

type RoomEventSource interface {
	LoadBootstrap(context.Context) (roomwatcher.Bootstrap, error)
	ReplayEvents(context.Context, uint64, int) ([]roomwatcher.Event, error)
}

// legacyRoomTransitionSessionStore exists only for the pre-Event unit-level
// compatibility entry point below. Production composition must use the
// commit-ordered reference payloads through ApplyRoomEvent.
type legacyRoomTransitionSessionStore interface {
	EnabledAccountsForRoom(context.Context, string) ([]int64, error)
}

// lostOwnershipSessionStore is the narrow administrative cleanup port used
// only after the repository has rejected the captured owner fence.
type lostOwnershipSessionStore interface {
	ReconcileSession(context.Context, ReconcileSessionCommand) error
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

type sessionPublisherFinalizer interface{ FinalizeSession() }

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

	beforeForgetLostOwner func()
	afterForgetLostOwner  func()
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
	beforeForgetLostOwner  func()
	afterForgetLostOwner   func()
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
	shutdownMu        sync.Mutex

	transitionMu    sync.Mutex
	lastSequence    uint64
	roomTransitions map[string]roomTransitionState
}

type roomTransitionState struct {
	leaseEpoch         uint64
	state              roomwatcher.State
	broadcastSessionID int64
	accounts           []int64
	activeAccounts     []int64
	pendingOwners      map[int64]OwnerFence
}

type accountRuntime struct {
	manager           *Manager
	accountID         int64
	opMu              sync.Mutex
	mu                sync.Mutex
	leases            map[uint64]LeaseKind
	disabled          bool
	shutting          bool
	degraded          bool
	sourceDegraded    bool
	current           *activeSession
	idleTimer         Timer
	idleCancel        chan struct{}
	idleDone          chan struct{}
	closeDone         chan struct{}
	stale             bool
	staleDone         chan struct{}
	staleRelease      OwnerFence
	owner             OwnerFence
	reconcile         bool
	operation         bool
	transitionPending *activeSession
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
	cleanupPhase    durableCleanupPhase
	cleanupFence    OwnerFence
	needsReconcile  bool
}

type durableCleanupPhase uint8

const (
	cleanupPhaseStarted durableCleanupPhase = iota + 1
	cleanupPhaseEnded
	cleanupPhaseFinalized
	cleanupPhaseReleased
)

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
	manager := &Manager{dependencies: dependencies, now: options.Now, newTimer: options.NewTimer, processorFactory: options.ProcessorFactory, ownerToken: options.OwnerToken, ownerTTL: options.OwnerTTL, heartbeat: options.HeartbeatInterval, ownerOperationTimeout: options.OwnerOperationTimeout, newHeartbeatTimer: options.NewHeartbeatTimer, newShutdownTimer: options.NewShutdownTimer, shutdownRetryBackoff: options.ShutdownRetryBackoff, beforeSessionPublish: options.BeforeSessionPublish, beforeForgetLostOwner: options.beforeForgetLostOwner, afterForgetLostOwner: options.afterForgetLostOwner, heartbeatDone: make(chan struct{}), ownershipControl: ownershipControl, cancelOwnershipControl: cancelOwnershipControl, ownershipStop: make(chan struct{}), accounts: make(map[int64]*accountRuntime), closing: make(chan struct{}), done: make(chan struct{}), lifecycle: lifecycle, cancel: cancel, processing: processing, cancelProcessing: cancelProcessing, roomTransitions: make(map[string]roomTransitionState)}
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
		err := account.manager.closeCurrentTerminal(ctx, account, OwnerFence{})
		cancel()
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

// ApplyRoomEvent serializes the one durable room-monitor outbox into account
// executions. Full reference snapshots define current membership; state
// payloads apply lifecycle behavior to that latest roster.
func (manager *Manager) ApplyRoomEvent(ctx context.Context, event roomwatcher.Event) error {
	if manager == nil || ctx == nil || !validRoomEvent(event) {
		return ErrInvalidInput
	}
	manager.transitionMu.Lock()
	defer manager.transitionMu.Unlock()
	manager.mu.Lock()
	closed := manager.closed
	manager.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if event.Sequence <= manager.lastSequence {
		return nil
	}
	if event.RoomReferencesChanged != nil {
		if err := manager.applyRoomReferencesChanged(ctx, *event.RoomReferencesChanged); err != nil {
			return err
		}
	} else if err := manager.applyRoomStateChanged(ctx, *event.RoomStateChanged); err != nil {
		return err
	}
	manager.lastSequence = event.Sequence
	return nil
}

// BootstrapRoomProjection installs one transactionally consistent current
// projection before incremental replay. Its cursor fences every historical
// outbox event at or before the snapshot boundary.
func (manager *Manager) BootstrapRoomProjection(ctx context.Context, bootstrap roomwatcher.Bootstrap) error {
	if manager == nil || ctx == nil || !validBootstrapProjection(bootstrap) {
		return ErrInvalidInput
	}
	manager.transitionMu.Lock()
	defer manager.transitionMu.Unlock()
	manager.mu.Lock()
	closed := manager.closed
	manager.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if manager.lastSequence != 0 {
		if bootstrap.Cursor <= manager.lastSequence {
			return nil
		}
		return ErrUnavailable
	}
	for _, projected := range bootstrap.Rooms {
		room := manager.roomTransitions[projected.RoomID]
		if room.leaseEpoch != 0 && room.leaseEpoch != projected.LeaseEpoch {
			return ErrUnavailable
		}
		room.state = projected.State
		room.leaseEpoch = projected.LeaseEpoch
		room.broadcastSessionID = projected.BroadcastSessionID
		room.accounts = append([]int64(nil), projected.AccountIDs...)
		manager.roomTransitions[projected.RoomID] = room
		if projected.State != roomwatcher.StateLive {
			continue
		}
		for _, accountID := range transitionDifference(projected.AccountIDs, room.activeAccounts) {
			if err := manager.stopTransitionAccountInOtherRooms(ctx, accountID, projected.RoomID); err != nil {
				return err
			}
			if err := manager.startTransitionAccount(ctx, accountID, projected.RoomID, projected.BroadcastSessionID); err != nil {
				return err
			}
			room = manager.roomTransitions[projected.RoomID]
			if !containsTransitionAccount(room.activeAccounts, accountID) {
				room.activeAccounts = append(room.activeAccounts, accountID)
			}
			manager.roomTransitions[projected.RoomID] = room
		}
	}
	manager.lastSequence = bootstrap.Cursor
	return nil
}

// BootstrapAndReplayRoomEvents is the restart catch-up boundary: it installs
// the current projection first, then asks durable replay only for events after
// that transaction's cursor.
func (manager *Manager) BootstrapAndReplayRoomEvents(ctx context.Context, source RoomEventSource, limit int) (uint64, error) {
	if manager == nil || ctx == nil || source == nil || limit <= 0 || limit > roomwatcher.MaxReplayLimit {
		return 0, ErrInvalidInput
	}
	bootstrap, err := source.LoadBootstrap(ctx)
	if err != nil {
		return 0, ErrUnavailable
	}
	if err := manager.BootstrapRoomProjection(ctx, bootstrap); err != nil {
		return 0, err
	}
	cursor := bootstrap.Cursor
	for {
		events, err := source.ReplayEvents(ctx, cursor, limit)
		if err != nil {
			return cursor, ErrUnavailable
		}
		for _, event := range events {
			if event.Sequence <= cursor {
				return cursor, ErrUnavailable
			}
			if err := manager.ApplyRoomEvent(ctx, event); err != nil {
				return cursor, err
			}
			cursor = event.Sequence
		}
		if len(events) < limit {
			return cursor, nil
		}
	}
}

// ApplyRoomTransition remains a compatibility wrapper for in-package callers
// while production composition consumes ApplyRoomEvent.
func (manager *Manager) ApplyRoomTransition(ctx context.Context, transition roomwatcher.Transition) error {
	if manager == nil || ctx == nil {
		return ErrInvalidInput
	}
	if transition.To == roomwatcher.StateLive {
		store, ok := manager.dependencies.Sessions.(legacyRoomTransitionSessionStore)
		if !ok {
			return ErrUnavailable
		}
		accounts, err := store.EnabledAccountsForRoom(ctx, transition.RoomID)
		if err != nil {
			return ErrUnavailable
		}
		accounts, err = normalizeTransitionAccounts(accounts)
		if err != nil {
			return ErrUnavailable
		}
		manager.transitionMu.Lock()
		room := manager.roomTransitions[transition.RoomID]
		if room.state == "" && len(room.accounts) == 0 {
			room.accounts = accounts
			manager.roomTransitions[transition.RoomID] = room
		}
		manager.transitionMu.Unlock()
	}
	sequence := transition.Sequence
	transition.Sequence = 0
	return manager.ApplyRoomEvent(ctx, roomwatcher.Event{Sequence: sequence, RoomStateChanged: &transition})
}

func (manager *Manager) applyRoomReferencesChanged(ctx context.Context, changed roomwatcher.RoomReferencesChanged) error {
	roomID := changed.RoomID
	room := manager.roomTransitions[roomID]
	for _, accountID := range transitionDifference(room.activeAccounts, changed.AccountIDs) {
		if err := manager.stopTransitionAccount(ctx, accountID, roomID, room.pendingOwners[accountID]); err != nil {
			return err
		}
		room = manager.roomTransitions[roomID]
		delete(room.pendingOwners, accountID)
		room.activeAccounts = removeTransitionAccount(room.activeAccounts, accountID)
		manager.roomTransitions[roomID] = room
	}
	if room.state == roomwatcher.StateLive {
		additions := transitionDifference(changed.AccountIDs, room.activeAccounts)
		if len(additions) != 0 {
			broadcastSessionID := room.broadcastSessionID
			if broadcastSessionID <= 0 {
				return ErrUnavailable
			}
			for _, accountID := range additions {
				if err := manager.stopTransitionAccountInOtherRooms(ctx, accountID, roomID); err != nil {
					return err
				}
				if err := manager.startTransitionAccount(ctx, accountID, roomID, broadcastSessionID); err != nil {
					return err
				}
				room = manager.roomTransitions[roomID]
				if !containsTransitionAccount(room.activeAccounts, accountID) {
					room.activeAccounts = append(room.activeAccounts, accountID)
				}
				manager.roomTransitions[roomID] = room
			}
		}
	}
	room = manager.roomTransitions[roomID]
	room.accounts = append([]int64(nil), changed.AccountIDs...)
	manager.roomTransitions[roomID] = room
	return nil
}

func (manager *Manager) applyRoomStateChanged(ctx context.Context, transition roomwatcher.Transition) error {
	roomID := transition.RoomID
	room := manager.roomTransitions[roomID]
	if transition.LeaseEpoch <= room.leaseEpoch {
		return ErrUnavailable
	}
	switch transition.To {
	case roomwatcher.StateLive:
		broadcastSessionID := room.broadcastSessionID
		if broadcastSessionID <= 0 || transition.From == roomwatcher.StateOffline {
			store, ok := manager.dependencies.Sessions.(roomEventSessionStore)
			if !ok {
				return ErrUnavailable
			}
			var err error
			broadcastSessionID, err = store.OpenBroadcastSession(ctx, roomID)
			if err != nil || broadcastSessionID <= 0 {
				return ErrUnavailable
			}
			room.broadcastSessionID = broadcastSessionID
			manager.roomTransitions[roomID] = room
		}
		for _, accountID := range transitionDifference(room.accounts, room.activeAccounts) {
			if err := manager.stopTransitionAccountInOtherRooms(ctx, accountID, roomID); err != nil {
				return err
			}
			if err := manager.startTransitionAccount(ctx, accountID, roomID, broadcastSessionID); err != nil {
				return err
			}
			room = manager.roomTransitions[roomID]
			if !containsTransitionAccount(room.activeAccounts, accountID) {
				room.activeAccounts = append(room.activeAccounts, accountID)
			}
			manager.roomTransitions[roomID] = room
		}
		if err := manager.setTransitionAdmission(room.activeAccounts, roomID, true); err != nil {
			return err
		}
	case roomwatcher.StateGrace:
		if err := manager.setTransitionAdmission(room.activeAccounts, roomID, false); err != nil {
			return err
		}
	case roomwatcher.StateOffline:
		for _, accountID := range append([]int64(nil), room.activeAccounts...) {
			if err := manager.stopTransitionAccount(ctx, accountID, roomID, room.pendingOwners[accountID]); err != nil {
				return err
			}
			room = manager.roomTransitions[roomID]
			delete(room.pendingOwners, accountID)
			room.activeAccounts = removeTransitionAccount(room.activeAccounts, accountID)
			manager.roomTransitions[roomID] = room
		}
		for _, accountID := range pendingTransitionAccounts(room.pendingOwners) {
			if err := manager.stopTransitionAccount(ctx, accountID, roomID, room.pendingOwners[accountID]); err != nil {
				return err
			}
			room = manager.roomTransitions[roomID]
			delete(room.pendingOwners, accountID)
			manager.roomTransitions[roomID] = room
		}
	}
	room = manager.roomTransitions[roomID]
	room.state = transition.To
	room.leaseEpoch = transition.LeaseEpoch
	if transition.To == roomwatcher.StateOffline {
		room.broadcastSessionID = 0
	}
	manager.roomTransitions[roomID] = room
	return nil
}

func (manager *Manager) stopTransitionAccountInOtherRooms(ctx context.Context, accountID int64, targetRoomID string) error {
	roomIDs := make([]string, 0)
	for roomID, room := range manager.roomTransitions {
		if roomID != targetRoomID && (containsTransitionAccount(room.activeAccounts, accountID) || validOwnerFence(room.pendingOwners[accountID])) {
			roomIDs = append(roomIDs, roomID)
		}
	}
	sort.Strings(roomIDs)
	for _, roomID := range roomIDs {
		room := manager.roomTransitions[roomID]
		if err := manager.stopTransitionAccount(ctx, accountID, roomID, room.pendingOwners[accountID]); err != nil {
			return err
		}
		room = manager.roomTransitions[roomID]
		delete(room.pendingOwners, accountID)
		room.activeAccounts = removeTransitionAccount(room.activeAccounts, accountID)
		room.accounts = removeTransitionAccount(room.accounts, accountID)
		manager.roomTransitions[roomID] = room
	}
	return nil
}

func validRoomEvent(event roomwatcher.Event) bool {
	if event.Sequence == 0 || (event.RoomStateChanged == nil) == (event.RoomReferencesChanged == nil) {
		return false
	}
	if event.RoomStateChanged != nil {
		return validRoomTransition(*event.RoomStateChanged)
	}
	changed := event.RoomReferencesChanged
	if changed == nil || !validRoomID(changed.RoomID) {
		return false
	}
	var previous int64
	for _, accountID := range changed.AccountIDs {
		if accountID <= previous {
			return false
		}
		previous = accountID
	}
	return true
}

func validBootstrapProjection(bootstrap roomwatcher.Bootstrap) bool {
	var previousRoom string
	accounts := make(map[int64]struct{})
	for _, room := range bootstrap.Rooms {
		if !validRoomID(room.RoomID) || (previousRoom != "" && previousRoom >= room.RoomID) || room.LeaseEpoch == 0 {
			return false
		}
		previousRoom = room.RoomID
		switch room.State {
		case roomwatcher.StateOffline:
			if room.BroadcastSessionID != 0 || room.GraceUntil != nil {
				return false
			}
		case roomwatcher.StateLive:
			if room.BroadcastSessionID <= 0 || room.GraceUntil != nil {
				return false
			}
		case roomwatcher.StateGrace:
			if room.BroadcastSessionID <= 0 || room.GraceUntil == nil || room.GraceUntil.IsZero() {
				return false
			}
		default:
			return false
		}
		var previousAccount int64
		for _, accountID := range room.AccountIDs {
			if accountID <= previousAccount {
				return false
			}
			if _, duplicate := accounts[accountID]; duplicate {
				return false
			}
			accounts[accountID] = struct{}{}
			previousAccount = accountID
		}
	}
	return true
}

func validRoomTransition(transition roomwatcher.Transition) bool {
	if !validRoomID(transition.RoomID) || transition.Sequence != 0 || transition.LeaseEpoch == 0 || transition.ConfirmedAt.IsZero() {
		return false
	}
	switch transition.To {
	case roomwatcher.StateLive:
		return transition.GraceUntil == nil && ((transition.From == roomwatcher.StateOffline && transition.NewBroadcast) || (transition.From == roomwatcher.StateGrace && !transition.NewBroadcast))
	case roomwatcher.StateGrace:
		return transition.From == roomwatcher.StateLive && !transition.NewBroadcast && transition.GraceUntil != nil && transition.GraceUntil.After(transition.ConfirmedAt)
	case roomwatcher.StateOffline:
		return transition.GraceUntil == nil && !transition.NewBroadcast && (transition.From == roomwatcher.StateLive || transition.From == roomwatcher.StateGrace)
	default:
		return false
	}
}

func transitionDifference(left, right []int64) []int64 {
	rightSet := make(map[int64]struct{}, len(right))
	for _, accountID := range right {
		rightSet[accountID] = struct{}{}
	}
	result := make([]int64, 0)
	for _, accountID := range left {
		if _, exists := rightSet[accountID]; !exists {
			result = append(result, accountID)
		}
	}
	return result
}

func removeTransitionAccount(accounts []int64, accountID int64) []int64 {
	result := accounts[:0]
	for _, current := range accounts {
		if current != accountID {
			result = append(result, current)
		}
	}
	return result
}

func normalizeTransitionAccounts(accountIDs []int64) ([]int64, error) {
	unique := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return nil, ErrInvalidInput
		}
		unique[accountID] = struct{}{}
	}
	accounts := make([]int64, 0, len(unique))
	for accountID := range unique {
		accounts = append(accounts, accountID)
	}
	sort.Slice(accounts, func(left, right int) bool { return accounts[left] < accounts[right] })
	return accounts, nil
}

func containsTransitionAccount(accounts []int64, accountID int64) bool {
	for _, current := range accounts {
		if current == accountID {
			return true
		}
	}
	return false
}

func pendingTransitionAccounts(owners map[int64]OwnerFence) []int64 {
	accounts := make([]int64, 0, len(owners))
	for accountID := range owners {
		accounts = append(accounts, accountID)
	}
	sort.Slice(accounts, func(left, right int) bool { return accounts[left] < accounts[right] })
	return accounts
}

func (manager *Manager) setTransitionAdmission(accounts []int64, roomID string, admitting bool) error {
	for _, accountID := range accounts {
		account, err := manager.accountExisting(accountID)
		if err != nil {
			return ErrUnavailable
		}
		account.mu.Lock()
		active := account.current
		account.mu.Unlock()
		if active == nil || active.session.RoomID != roomID {
			return ErrUnavailable
		}
		active.admissionMu.Lock()
		if active.eventsClosed || active.drained {
			active.admissionMu.Unlock()
			return ErrUnavailable
		}
		active.admitting = admitting
		active.admissionMu.Unlock()
	}
	return nil
}

// registerTransitionOwner runs while ApplyRoomTransition holds transitionMu.
// It precedes every fallible admission operation after ClaimOwnership so an
// interrupted live transition cannot lose the exact fence that offline must
// release.
func (manager *Manager) registerTransitionOwner(roomID string, accountID int64, fence OwnerFence) {
	room := manager.roomTransitions[roomID]
	if room.pendingOwners == nil {
		room.pendingOwners = make(map[int64]OwnerFence)
	}
	room.pendingOwners[accountID] = fence
	manager.roomTransitions[roomID] = room
}

func (manager *Manager) clearTransitionOwner(roomID string, accountID int64, fence OwnerFence) {
	room := manager.roomTransitions[roomID]
	if room.pendingOwners[accountID] != fence {
		return
	}
	delete(room.pendingOwners, accountID)
	manager.roomTransitions[roomID] = room
}

func (manager *Manager) startTransitionAccount(ctx context.Context, accountID int64, roomID string, broadcastSessionID int64) error {
	account, err := manager.account(accountID)
	if err != nil {
		return err
	}
	account.opMu.Lock()
	defer account.opMu.Unlock()
	account.mu.Lock()
	pendingSession := account.transitionPending
	account.mu.Unlock()
	if pendingSession != nil {
		if pendingSession.session.RoomID != roomID {
			return ErrUnavailable
		}
		if err := manager.stopTransitionAccountLocked(ctx, account, roomID, pendingSession.owner); err != nil {
			return err
		}
		manager.clearTransitionOwner(roomID, accountID, pendingSession.owner)
	}
	account.mu.Lock()
	if current := account.current; current != nil {
		if current.session.RoomID == roomID {
			account.mu.Unlock()
			return nil
		}
		account.mu.Unlock()
		return ErrUnavailable
	}
	blocked := account.stale || account.shutting || account.disabled
	account.mu.Unlock()
	room := manager.roomTransitions[roomID]
	if pendingOwner := room.pendingOwners[accountID]; validOwnerFence(pendingOwner) {
		if err := manager.releaseOwner(ctx, account, pendingOwner); err != nil {
			return ErrUnavailable
		}
		manager.clearTransitionOwner(roomID, accountID, pendingOwner)
	}
	if blocked {
		return ErrUnavailable
	}
	claim, err := manager.dependencies.Sessions.ClaimOwnership(ctx, accountID, manager.ownerToken, manager.ownerTTL)
	if err != nil {
		return err
	}
	account.mu.Lock()
	account.owner, account.reconcile = claim.Fence, claim.Reconcile
	account.mu.Unlock()
	manager.registerTransitionOwner(roomID, accountID, claim.Fence)
	jobID, pending, err := manager.dependencies.Sessions.PendingMigration(ctx, accountID)
	if err != nil {
		if manager.releaseOwner(ctx, account, claim.Fence) == nil {
			manager.clearTransitionOwner(roomID, accountID, claim.Fence)
		}
		return ErrUnavailable
	}
	if pending {
		migrationOwner := migration.OwnerFence{AccountID: claim.Fence.AccountID, Token: [32]byte(claim.Fence.Token), Epoch: claim.Fence.Epoch}
		if err := manager.applyPendingMigration(ctx, migrationOwner, jobID); err != nil {
			if manager.releaseOwner(ctx, account, claim.Fence) == nil {
				manager.clearTransitionOwner(roomID, accountID, claim.Fence)
			}
			if errors.Is(err, ErrOwnershipConflict) {
				return ErrUnavailable
			}
			return ErrUnavailable
		}
	}
	if err := manager.startRoom(ctx, account, roomID, false, true, broadcastSessionID); err != nil {
		if cleanupErr := manager.stopTransitionAccountLocked(ctx, account, roomID, claim.Fence); cleanupErr != nil {
			return cleanupErr
		}
		manager.clearTransitionOwner(roomID, accountID, claim.Fence)
		if errors.Is(err, ErrOwnershipConflict) {
			return ErrUnavailable
		}
		return err
	}
	return nil
}

func (manager *Manager) stopTransitionAccount(ctx context.Context, accountID int64, roomID string, transitionOwner OwnerFence) error {
	account, err := manager.accountExisting(accountID)
	if err != nil {
		return nil
	}
	account.opMu.Lock()
	defer account.opMu.Unlock()
	return manager.stopTransitionAccountLocked(ctx, account, roomID, transitionOwner)
}

// stopTransitionAccountLocked is the single post-StartSession cleanup owner.
// The caller holds account.opMu, including admission failure paths that have
// not yet returned from startTransitionAccount.
func (manager *Manager) stopTransitionAccountLocked(ctx context.Context, account *accountRuntime, roomID string, transitionOwner OwnerFence) error {
	account.mu.Lock()
	active := account.current
	if active == nil {
		active = account.transitionPending
	}
	account.mu.Unlock()
	pendingRelease := transitionOwner
	if active != nil {
		if active.session.RoomID != roomID {
			return nil
		}
		if err := manager.closeCurrentTerminal(ctx, account, transitionOwner); err != nil {
			return err
		}
		return nil
	}
	if !validOwnerFence(pendingRelease) {
		return nil
	}
	if err := manager.releaseOwner(ctx, account, pendingRelease); err != nil {
		return ErrUnavailable
	}
	return nil
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
	if active != nil && active.owner != claim.Fence {
		account.mu.Unlock()
		manager.beginStaleCleanupLocked(account, active, claim.Fence)
		return ErrUnavailable
	}
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
	temporaryClaim := active == nil
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
	// A session created before roomwatcher composition has no business-broadcast
	// link. Keep the old switch path only for that temporary compatibility
	// state; monitor-owned executions (which always carry the link) are changed
	// exclusively by durable room transitions.
	if active != nil && active.session.BroadcastSessionID == 0 {
		if err := manager.closeCurrent(operationContext, account); err != nil {
			if errors.Is(err, ErrOwnershipConflict) {
				return ErrUnavailable
			}
			account.markDegraded()
			return err
		}
		if err := manager.applyPendingMigrationForOwner(operationContext, claim.Fence, accountID); err != nil {
			return err
		}
		if err := manager.startRoom(operationContext, account, canonical, true, true, 0); err != nil {
			if errors.Is(err, ErrOwnershipConflict) {
				return ErrUnavailable
			}
			account.markDegraded()
			return err
		}
		return nil
	}
	if temporaryClaim {
		if err := manager.applyPendingMigrationForOwner(operationContext, claim.Fence, accountID); err != nil {
			return err
		}
	}
	if err := manager.dependencies.Sessions.PersistTargetRoom(operationContext, PersistTargetRoomCommand{Owner: claim.Fence, RoomID: canonical, UpdatedAt: manager.now()}); err != nil {
		if errors.Is(err, ErrOwnershipConflict) {
			manager.beginStaleCleanupLocked(account, nil, OwnerFence{})
			return ErrUnavailable
		}
		account.markDegraded()
		return err
	}
	return nil
}

func (manager *Manager) applyPendingMigrationForOwner(ctx context.Context, owner OwnerFence, accountID int64) error {
	jobID, pending, err := manager.dependencies.Sessions.PendingMigration(ctx, accountID)
	if err != nil {
		return ErrUnavailable
	}
	if !pending {
		return nil
	}
	migrationOwner := migration.OwnerFence{AccountID: owner.AccountID, Token: [32]byte(owner.Token), Epoch: owner.Epoch}
	if err := manager.applyPendingMigration(ctx, migrationOwner, jobID); err != nil {
		if errors.Is(err, ErrOwnershipConflict) {
			return ErrUnavailable
		}
		return ErrUnavailable
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
		account.mu.Lock()
		active := account.current
		if active == nil {
			active = account.transitionPending
		}
		fence := account.owner
		if active != nil {
			fence = active.owner
		}
		account.mu.Unlock()
		err := manager.closeCurrentTerminal(ctx, account, OwnerFence{})
		cancel()
		account.opMu.Unlock()
		if err == nil {
			manager.forgetTransitionAccount(account.accountID, fence)
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
		err := manager.closeCurrentTerminal(ctx, account, OwnerFence{})
		if err == nil {
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
		newRetryTimer := manager.newShutdownTimer
		account.mu.Lock()
		active := account.current
		if active == nil {
			active = account.transitionPending
		}
		account.mu.Unlock()
		if active != nil {
			active.admissionMu.Lock()
			phase := active.cleanupPhase
			active.admissionMu.Unlock()
			if phase == cleanupPhaseFinalized {
				delay = manager.heartbeat
				newRetryTimer = manager.newTimer
			}
		}
		timer := newRetryTimer(delay)
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
// repository has rejected. The caller holds account.opMu. A started session
// remains attached to the account until it has been reconciled, finalized and
// marked released; losing a fence must never erase durable cleanup work.
func (manager *Manager) beginStaleOnConflictLocked(account *accountRuntime) {
	account.mu.Lock()
	active := account.current
	if active == nil {
		active = account.transitionPending
	}
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
		if active == nil {
			active = account.transitionPending
		}
	}
	if active != nil && account.current != active && account.transitionPending != active {
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
		if validOwnerFence(releaseFence) {
			active.cleanupFence = releaseFence
			active.needsReconcile = false
		} else {
			active.cleanupFence = OwnerFence{}
			active.needsReconcile = true
		}
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
	for {
		account.mu.Lock()
		if account.staleDone != done {
			account.mu.Unlock()
			account.opMu.Unlock()
			return
		}
		releaseFence = account.staleRelease
		account.mu.Unlock()

		var err error
		if active != nil {
			ctx, cancel := manager.ownerOperationContext()
			err = manager.advanceSessionCleanup(ctx, account, active, cleanupPhaseReleased, releaseFence)
			cancel()
		} else if validOwnerFence(releaseFence) {
			ctx, cancel := manager.ownerOperationContext()
			err = manager.releaseOwner(ctx, account, releaseFence)
			cancel()
		}
		if err == nil {
			break
		}
		if manager.ownershipControl.Err() != nil {
			account.opMu.Unlock()
			return
		}
		timer := manager.newTimer(manager.heartbeat)
		select {
		case <-timer.C():
			timer.Stop()
		case <-manager.ownershipControl.Done():
			timer.Stop()
			account.opMu.Unlock()
			return
		}
	}

	needsReconcile := false
	if active != nil {
		active.admissionMu.Lock()
		needsReconcile = active.needsReconcile
		active.admissionMu.Unlock()
	}
	account.mu.Lock()
	if account.staleDone != done {
		account.mu.Unlock()
		account.opMu.Unlock()
		return
	}
	if account.current == active {
		account.current = nil
	}
	if account.transitionPending == active {
		account.transitionPending = nil
	}
	if active != nil && account.owner == active.owner {
		account.owner = OwnerFence{}
		account.reconcile = false
	} else if (!validOwnerFence(releaseFence) && active == nil) || needsReconcile {
		account.owner = OwnerFence{}
		account.reconcile = false
	}
	account.stale = false
	account.degraded = false
	account.staleRelease = OwnerFence{}
	close(done)
	account.staleDone = nil
	account.mu.Unlock()
	account.opMu.Unlock()
	if active != nil {
		if manager.beforeForgetLostOwner != nil {
			manager.beforeForgetLostOwner()
		}
		manager.forgetLostTransitionOwner(account.accountID, active.owner)
		if manager.afterForgetLostOwner != nil {
			manager.afterForgetLostOwner()
		}
	}
}

func (manager *Manager) forgetLostTransitionOwner(accountID int64, fence OwnerFence) {
	manager.forgetTransitionAccount(accountID, fence)
}

func (manager *Manager) forgetTransitionAccount(accountID int64, fence OwnerFence) {
	manager.transitionMu.Lock()
	defer manager.transitionMu.Unlock()
	for roomID, room := range manager.roomTransitions {
		if !validOwnerFence(fence) || room.pendingOwners[accountID] != fence {
			continue
		}
		delete(room.pendingOwners, accountID)
		room.activeAccounts = removeTransitionAccount(room.activeAccounts, accountID)
		manager.roomTransitions[roomID] = room
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
		active := validOwnerFence(fence) && (account.shutting || account.disabled || len(account.leases) != 0 || account.current != nil || account.transitionPending != nil || account.operation)
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
	})
	if err := ctx.Err(); err != nil {
		manager.processCancelOnce.Do(manager.cancelProcessing)
		return err
	}

	manager.shutdownMu.Lock()
	defer manager.shutdownMu.Unlock()
	select {
	case <-manager.done:
		manager.mu.Lock()
		err := manager.shutdownErr
		manager.mu.Unlock()
		return err
	default:
	}

	manager.mu.Lock()
	accounts := make([]*accountRuntime, 0, len(manager.accounts))
	for _, account := range manager.accounts {
		accounts = append(accounts, account)
	}
	manager.mu.Unlock()
	for _, account := range accounts {
		if err := manager.waitStaleCleanup(ctx, account); err != nil {
			if ctx.Err() != nil {
				manager.processCancelOnce.Do(manager.cancelProcessing)
				return ctx.Err()
			}
			return ErrUnavailable
		}
		account.opMu.Lock()
		if err := ctx.Err(); err != nil {
			account.opMu.Unlock()
			manager.processCancelOnce.Do(manager.cancelProcessing)
			return err
		}
		err := manager.closeCurrentDuringShutdown(ctx, account)
		if err != nil {
			account.opMu.Unlock()
			if ctx.Err() != nil {
				manager.processCancelOnce.Do(manager.cancelProcessing)
				return ctx.Err()
			}
			return ErrUnavailable
		}
		account.mu.Lock()
		fence := account.owner
		account.mu.Unlock()
		if validOwnerFence(fence) {
			err = manager.releaseOwnerDuringShutdown(ctx, account, fence)
		}
		account.opMu.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				manager.processCancelOnce.Do(manager.cancelProcessing)
				return ctx.Err()
			}
			return ErrUnavailable
		}
	}

	manager.stopOwnershipHeartbeat()
	select {
	case <-manager.heartbeatDone:
	case <-ctx.Done():
		manager.processCancelOnce.Do(manager.cancelProcessing)
		return ctx.Err()
	}
	for _, account := range accounts {
		if err := manager.waitStaleCleanup(ctx, account); err != nil {
			if ctx.Err() != nil {
				manager.processCancelOnce.Do(manager.cancelProcessing)
				return ctx.Err()
			}
			return ErrUnavailable
		}
	}
	manager.processCancelOnce.Do(manager.cancelProcessing)
	manager.stopOwnershipControl()
	manager.dependencies.RoomSources.Close()
	if err := manager.dependencies.RoomSources.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	close(manager.done)
	return nil
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
