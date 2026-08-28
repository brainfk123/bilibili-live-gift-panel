package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

var ErrTimerSchedulerUnavailable = errors.New("runtime: gameplay timer scheduler unavailable")

type Boundary = configuration.Boundary

// TimerRebuilder is the explicit future scheduler seam. All fallible work must
// happen in PrepareConfigurationTimers; Activate runs only after the durable
// boundary transaction commits and therefore cannot fail.
type TimerRebuilder interface {
	PrepareConfigurationTimers(context.Context, TimerRebuildCommand) (PreparedTimers, error)
}

type TimerRebuildCommand struct {
	AccountID     int64
	LiveSessionID int64
	Candidate     migration.BarrierCandidate
}

type PreparedTimers interface {
	Activate()
	Abort()
}

type preparedBarrierTimers struct{}

func (preparedBarrierTimers) Activate() {}
func (preparedBarrierTimers) Abort()    {}

type configurationBarrierProcessorState struct {
	ConfigVersionID int64
	ConfigVersion   uint64
	Revision        uint64
}

type preparedBarrierSnapshot interface {
	Publish()
	Abort()
}

type preparedBarrierSnapshotFuncs struct {
	publish func()
	abort   func()
}

func (prepared preparedBarrierSnapshotFuncs) Publish() {
	if prepared.publish != nil {
		prepared.publish()
	}
}
func (prepared preparedBarrierSnapshotFuncs) Abort() {
	if prepared.abort != nil {
		prepared.abort()
	}
}

// fullSnapshotPreparer lets tests and future durable publishers fail before
// activation. Publisher itself remains the current infallible in-memory OBS
// fanout.
type fullSnapshotPreparer interface {
	PrepareFullSnapshot(DisplaySnapshot) (preparedBarrierSnapshot, error)
}

type configurationBarrierDelegate interface {
	SessionProcessor
	configurationBarrierState(context.Context) (configurationBarrierProcessorState, error)
	prepareConfigurationSnapshot(migration.BarrierCandidate, Session, uint64) (preparedBarrierSnapshot, error)
	applyConfigurationBoundary(migration.BarrierCandidate, configuration.Boundary)
}

const configurationBarrierMarkerType = "__HOSTED_CONFIGURATION_BARRIER__"

// ConfigurationBarrierProcessorFactory keeps the session worker's processor
// pointer immutable while adding an internal FIFO marker used for exact drain.
type ConfigurationBarrierProcessorFactory struct{ inner ProcessorFactory }

func NewConfigurationBarrierProcessorFactory(inner ProcessorFactory) (*ConfigurationBarrierProcessorFactory, error) {
	if inner == nil {
		return nil, ErrInvalidInput
	}
	return &ConfigurationBarrierProcessorFactory{inner: inner}, nil
}

func (factory *ConfigurationBarrierProcessorFactory) New(ctx context.Context, owner OwnerFence, session Session) (SessionProcessor, error) {
	if factory == nil || factory.inner == nil {
		return nil, ErrInvalidInput
	}
	processor, err := factory.inner.New(ctx, owner, session)
	if err != nil {
		return nil, err
	}
	delegate, ok := processor.(configurationBarrierDelegate)
	if !ok {
		return nil, ErrUnavailable
	}
	return newManagedConfigurationBarrierProcessor(delegate), nil
}

type managedConfigurationBarrierProcessor struct {
	delegate configurationBarrierDelegate
	mu       sync.Mutex
	next     uint64
	markers  map[string]chan struct{}
}

func newManagedConfigurationBarrierProcessor(delegate configurationBarrierDelegate) *managedConfigurationBarrierProcessor {
	return &managedConfigurationBarrierProcessor{delegate: delegate, markers: make(map[string]chan struct{})}
}

func (processor *managedConfigurationBarrierProcessor) Accept(event roomsource.Event) error {
	if event.Type == configurationBarrierMarkerType {
		processor.mu.Lock()
		done := processor.markers[event.ID]
		delete(processor.markers, event.ID)
		processor.mu.Unlock()
		if done != nil {
			close(done)
		}
		return nil
	}
	return processor.delegate.Accept(event)
}

func (processor *managedConfigurationBarrierProcessor) Close(ctx context.Context) error {
	return processor.delegate.Close(ctx)
}
func (processor *managedConfigurationBarrierProcessor) Status() ProcessorStatus {
	return processor.delegate.Status()
}
func (processor *managedConfigurationBarrierProcessor) SetConnectionHealthy(healthy bool) {
	processor.delegate.SetConnectionHealthy(healthy)
}
func (processor *managedConfigurationBarrierProcessor) SetOwnershipLost(callback func()) {
	if reporter, ok := processor.delegate.(ownershipLossReporter); ok {
		reporter.SetOwnershipLost(callback)
	}
}
func (processor *managedConfigurationBarrierProcessor) FinalizeSession() {
	if finalizer, ok := processor.delegate.(sessionPublisherFinalizer); ok {
		finalizer.FinalizeSession()
	}
}

func (processor *managedConfigurationBarrierProcessor) drain(ctx context.Context, events chan<- roomsource.Event, roomID string) error {
	processor.mu.Lock()
	processor.next++
	if processor.next == 0 {
		processor.mu.Unlock()
		return ErrUnavailable
	}
	markerID := "configuration-barrier-" + uintString(processor.next)
	done := make(chan struct{})
	processor.markers[markerID] = done
	processor.mu.Unlock()
	marker := roomsource.Event{ID: markerID, RoomID: roomID, Type: configurationBarrierMarkerType}
	select {
	case events <- marker:
	case <-ctx.Done():
		processor.removeMarker(markerID)
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		processor.removeMarker(markerID)
		return ctx.Err()
	}
}

func (processor *managedConfigurationBarrierProcessor) removeMarker(markerID string) {
	processor.mu.Lock()
	delete(processor.markers, markerID)
	processor.mu.Unlock()
}

func (processor *managedConfigurationBarrierProcessor) prepareTimers(ctx context.Context, command TimerRebuildCommand) (PreparedTimers, error) {
	if len(command.Candidate.Definition.TimerRules) == 0 {
		return preparedBarrierTimers{}, nil
	}
	rebuilder, ok := processor.delegate.(TimerRebuilder)
	if !ok {
		return nil, ErrTimerSchedulerUnavailable
	}
	prepared, err := rebuilder.PrepareConfigurationTimers(ctx, command)
	if err != nil || prepared == nil {
		if err == nil {
			err = ErrTimerSchedulerUnavailable
		}
		return nil, err
	}
	return prepared, nil
}

func uintString(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

// ApplyConfigurationBarrier pauses only the target account, drains its exact
// accepted prefix, commits the version/revision boundary, publishes a complete
// OBS snapshot, and then allows blocked callbacks to continue.
func (manager *Manager) ApplyConfigurationBarrier(ctx context.Context, accountID int64, candidate migration.BarrierCandidate) (Boundary, error) {
	if manager == nil || ctx == nil || accountID <= 0 || candidate.JobID <= 0 || candidate.Operation != configuration.BarrierMigrationApply && candidate.Operation != configuration.BarrierMigrationRollback || candidate.Definition.MigrationHash != "" {
		return Boundary{}, ErrInvalidInput
	}
	normalizedDefinition, normalizedRuntime, err := configuration.Normalize(candidate.Definition, candidate.Runtime)
	if err != nil {
		return Boundary{}, ErrInvalidInput
	}
	candidate.Definition, candidate.Runtime = normalizedDefinition, normalizedRuntime
	account, err := manager.account(accountID)
	if err != nil {
		return Boundary{}, err
	}
	permit, err := account.opGate.Acquire(ctx)
	if err != nil {
		return Boundary{}, err
	}
	defer mustReleaseOperationPermit(permit)
	if err := ctx.Err(); err != nil {
		return Boundary{}, err
	}
	account.mu.Lock()
	if account.stale || account.disabled {
		account.mu.Unlock()
		return Boundary{}, ErrUnavailable
	}
	if account.shutting {
		account.mu.Unlock()
		return Boundary{}, ErrClosed
	}
	active := account.current
	if active == nil {
		active = account.transitionPending
	}
	account.mu.Unlock()
	if active == nil {
		return manager.applyOfflineConfigurationBarrier(ctx, accountID, candidate, nil)
	}
	if err := lockAdmissionContext(ctx, &active.admissionMu); err != nil {
		return Boundary{}, err
	}
	defer active.admissionMu.Unlock()
	if !active.admitting || !active.workerStarted || active.eventsClosed || active.drained || active.processorClosed || active.processor == nil || active.session.AccountID != accountID || active.session.ID <= 0 || active.session.BroadcastSessionID <= 0 {
		return Boundary{}, ErrUnavailable
	}
	processor, ok := active.processor.(*managedConfigurationBarrierProcessor)
	if !ok || processor.delegate == nil {
		return Boundary{}, ErrUnavailable
	}
	if err := processor.drain(ctx, active.events, active.session.RoomID); err != nil {
		return Boundary{}, err
	}
	state, err := processor.delegate.configurationBarrierState(ctx)
	if err != nil {
		return Boundary{}, barrierRuntimeError(ctx, err)
	}
	if state.ConfigVersionID != active.session.ConfigVersionID || state.ConfigVersionID <= 0 || state.ConfigVersion == 0 || state.Revision == 0 {
		return Boundary{}, ErrUnavailable
	}
	preparedTimers, err := processor.prepareTimers(ctx, TimerRebuildCommand{AccountID: accountID, LiveSessionID: active.session.ID, Candidate: candidate})
	if err != nil {
		return Boundary{}, err
	}
	preparedSnapshot, err := processor.delegate.prepareConfigurationSnapshot(candidate, active.session, state.Revision+1)
	if err != nil || preparedSnapshot == nil {
		preparedTimers.Abort()
		return Boundary{}, barrierRuntimeError(ctx, err)
	}
	repository, ok := manager.dependencies.Configuration.(configuration.BarrierRepository)
	if !ok {
		preparedSnapshot.Abort()
		preparedTimers.Abort()
		return Boundary{}, ErrUnavailable
	}
	now := time.Now
	if manager.now != nil {
		now = manager.now
	}
	boundary, err := repository.ActivateBarrier(ctx, configuration.BarrierCommand{
		AccountID: accountID, ExpectedConfigVersionID: state.ConfigVersionID, ExpectedVersion: state.ConfigVersion, ExpectedRevision: state.Revision,
		Definition: candidate.Definition, Runtime: candidate.Runtime, Operation: candidate.Operation, MigrationJobID: candidate.JobID, IntegritySeal: candidate.IntegritySeal,
		KeepRoomSuggestion: candidate.KeepRoomSuggestion, RoomSuggestion: candidate.RoomSuggestion,
		OwnerToken: [32]byte(active.owner.Token), OwnerEpoch: active.owner.Epoch,
		LiveSessionID: active.session.ID, BroadcastSessionID: active.session.BroadcastSessionID, At: now().UTC(),
	})
	if err != nil {
		preparedSnapshot.Abort()
		preparedTimers.Abort()
		return Boundary{}, barrierRuntimeError(ctx, err)
	}
	if boundary.AccountID != accountID || boundary.MigrationJobID != candidate.JobID || boundary.Operation != candidate.Operation || boundary.LiveSessionID != active.session.ID || boundary.BroadcastSessionID != active.session.BroadcastSessionID || boundary.NewConfigVersionID <= 0 || boundary.FirstNewRevision == 0 || boundary.FirstNewRevision != boundary.LastOldRevision+1 {
		preparedSnapshot.Abort()
		preparedTimers.Abort()
		return Boundary{}, ErrUnavailable
	}
	if boundary.NewConfigVersionID == state.ConfigVersionID {
		if boundary.OldConfigVersionID == boundary.NewConfigVersionID || boundary.FirstNewRevision > state.Revision {
			preparedSnapshot.Abort()
			preparedTimers.Abort()
			return Boundary{}, ErrUnavailable
		}
		preparedSnapshot.Abort()
		preparedTimers.Abort()
		return boundary, nil
	}
	if boundary.OldConfigVersionID != state.ConfigVersionID || boundary.LastOldRevision != state.Revision {
		preparedSnapshot.Abort()
		preparedTimers.Abort()
		return Boundary{}, ErrUnavailable
	}
	processor.delegate.applyConfigurationBoundary(candidate, boundary)
	active.session.ConfigVersionID = boundary.NewConfigVersionID
	preparedTimers.Activate()
	preparedSnapshot.Publish()
	return boundary, nil
}

// ApplyPendingConfigurationBarrier is called only while the runtime room
// transition already owns the account operation gate and has ended the old
// session. It must not reacquire that gate.
func (manager *Manager) ApplyPendingConfigurationBarrier(ctx context.Context, owner migration.OwnerFence, candidate migration.BarrierCandidate) (Boundary, error) {
	runtimeOwner := OwnerFence{AccountID: owner.AccountID, Token: OwnerToken(owner.Token), Epoch: owner.Epoch}
	if manager == nil || ctx == nil || !validOwnerFence(runtimeOwner) || candidate.JobID <= 0 || candidate.Operation != configuration.BarrierMigrationApply || candidate.Definition.MigrationHash != "" {
		return Boundary{}, ErrInvalidInput
	}
	definition, runtimeState, err := configuration.Normalize(candidate.Definition, candidate.Runtime)
	if err != nil {
		return Boundary{}, ErrInvalidInput
	}
	candidate.Definition, candidate.Runtime = definition, runtimeState
	account, err := manager.account(owner.AccountID)
	if err != nil {
		return Boundary{}, err
	}
	account.mu.Lock()
	available := !account.stale && !account.disabled && !account.shutting && account.operation && account.owner == runtimeOwner && account.current == nil && account.transitionPending == nil
	account.mu.Unlock()
	if !available {
		return Boundary{}, ErrUnavailable
	}
	return manager.applyOfflineConfigurationBarrier(ctx, owner.AccountID, candidate, &runtimeOwner)
}

func (manager *Manager) applyOfflineConfigurationBarrier(ctx context.Context, accountID int64, candidate migration.BarrierCandidate, owner *OwnerFence) (Boundary, error) {
	if len(candidate.Definition.TimerRules) != 0 {
		return Boundary{}, ErrTimerSchedulerUnavailable
	}
	repository, ok := manager.dependencies.Configuration.(configuration.BarrierRepository)
	if !ok {
		return Boundary{}, ErrUnavailable
	}
	version, state, err := manager.dependencies.Configuration.LoadActive(ctx, accountID)
	if errors.Is(err, configuration.ErrNotFound) {
		version, state, err = configuration.Version{}, configuration.State{}, nil
	}
	if err != nil {
		return Boundary{}, barrierRuntimeError(ctx, err)
	}
	now := time.Now
	if manager.now != nil {
		now = manager.now
	}
	var ownerToken [32]byte
	var ownerEpoch uint64
	if owner != nil {
		ownerToken = [32]byte(owner.Token)
		ownerEpoch = owner.Epoch
	}
	boundary, err := repository.ActivateBarrier(ctx, configuration.BarrierCommand{
		AccountID: accountID, ExpectedConfigVersionID: version.ID, ExpectedVersion: version.Number, ExpectedRevision: state.Revision,
		Definition: candidate.Definition, Runtime: candidate.Runtime, Operation: candidate.Operation, MigrationJobID: candidate.JobID, IntegritySeal: candidate.IntegritySeal,
		KeepRoomSuggestion: candidate.KeepRoomSuggestion, RoomSuggestion: candidate.RoomSuggestion,
		OwnerToken: ownerToken, OwnerEpoch: ownerEpoch, At: now().UTC(),
	})
	if err != nil {
		return Boundary{}, barrierRuntimeError(ctx, err)
	}
	return boundary, nil
}

func lockAdmissionContext(ctx context.Context, mutex *sync.Mutex) error {
	acquired := make(chan struct{}, 1)
	go func() {
		mutex.Lock()
		acquired <- struct{}{}
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		go func() {
			<-acquired
			mutex.Unlock()
		}()
		return ctx.Err()
	}
}

func barrierRuntimeError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return ErrUnavailable
	}
	if errors.Is(err, ErrTimerSchedulerUnavailable) {
		return err
	}
	if errors.Is(err, configuration.ErrOwnership) {
		return migration.ErrOwnershipConflict
	}
	return ErrUnavailable
}

func (processor *Processor) configurationBarrierState(ctx context.Context) (configurationBarrierProcessorState, error) {
	if processor == nil || ctx == nil {
		return configurationBarrierProcessorState{}, ErrInvalidInput
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		processor.mu.Lock()
		if processor.terminal || processor.closing {
			processor.mu.Unlock()
			return configurationBarrierProcessorState{}, ErrUnavailable
		}
		buffered := len(processor.buffer)
		accountID := processor.binding.Owner.AccountID
		configVersionID := processor.state.ConfigVersionID
		revision := processor.state.Revision
		processor.mu.Unlock()
		if buffered == 0 {
			version, state, err := processor.repository.LoadActive(ctx, accountID)
			if err != nil {
				return configurationBarrierProcessorState{}, err
			}
			processor.mu.Lock()
			stable := len(processor.buffer) == 0 && processor.state.ConfigVersionID == configVersionID && processor.state.Revision == revision
			processor.mu.Unlock()
			if stable {
				if version.ID <= 0 || version.Number == 0 || state.ConfigVersionID != version.ID || state.Revision == 0 {
					return configurationBarrierProcessorState{}, ErrUnavailable
				}
				// A successful commit can still have returned an ambiguous transport
				// error. Preserve the drained local edge so ActivateBarrier can
				// identify the already-completed job and return its durable boundary.
				if version.ID != configVersionID || state.Revision != revision {
					return configurationBarrierProcessorState{ConfigVersionID: configVersionID, ConfigVersion: version.Number, Revision: revision}, nil
				}
				return configurationBarrierProcessorState{ConfigVersionID: version.ID, ConfigVersion: version.Number, Revision: state.Revision}, nil
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return configurationBarrierProcessorState{}, ctx.Err()
		}
	}
}

func (processor *Processor) prepareConfigurationSnapshot(candidate migration.BarrierCandidate, session Session, firstRevision uint64) (preparedBarrierSnapshot, error) {
	if processor == nil || session.ID <= 0 || firstRevision == 0 {
		return nil, ErrInvalidInput
	}
	snapshot := DisplaySnapshot{AccountID: session.AccountID, LiveSessionID: session.ID, Revision: firstRevision, Runtime: candidate.Runtime, Viewers: processor.viewers.Rows()}
	if preparer, ok := processor.publisher.(fullSnapshotPreparer); ok {
		return preparer.PrepareFullSnapshot(snapshot)
	}
	return preparedBarrierSnapshotFuncs{publish: func() { processor.publisher.Publish(snapshot) }}, nil
}

func (processor *Processor) applyConfigurationBoundary(candidate migration.BarrierCandidate, boundary configuration.Boundary) {
	processor.mu.Lock()
	processor.definition = candidate.Definition
	processor.state = configuration.State{AccountID: boundary.AccountID, ConfigVersionID: boundary.NewConfigVersionID, Revision: boundary.FirstNewRevision, Runtime: candidate.Runtime, UpdatedAt: boundary.AppliedAt}
	processor.binding.Session.ConfigVersionID = boundary.NewConfigVersionID
	processor.mu.Unlock()
}
