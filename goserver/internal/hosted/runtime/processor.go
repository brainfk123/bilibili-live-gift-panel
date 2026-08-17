package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

type ProcessorRepository interface {
	LoadActive(context.Context, int64) (configuration.Version, configuration.State, error)
	CommitRuntimeEvent(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error)
}

type SessionProcessor interface {
	Accept(roomsource.Event) error
	Close(context.Context) error
	Status() ProcessorStatus
	SetConnectionHealthy(bool)
}

type ownershipLossReporter interface {
	SetOwnershipLost(func())
}

type ProcessorFactory interface {
	New(context.Context, OwnerFence, Session) (SessionProcessor, error)
}

type RuntimeProcessorFactory struct {
	repository ProcessorRepository
	publisher  SnapshotPublisher
	engine     gameplay.Engine
	options    ProcessorOptions
}

func NewProcessorFactory(repository ProcessorRepository, publisher SnapshotPublisher, options ProcessorOptions) (*RuntimeProcessorFactory, error) {
	if repository == nil || publisher == nil {
		return nil, ErrInvalidInput
	}
	return &RuntimeProcessorFactory{repository: repository, publisher: publisher, engine: gameplay.Engine{}, options: options}, nil
}

func (factory *RuntimeProcessorFactory) New(ctx context.Context, owner OwnerFence, session Session) (SessionProcessor, error) {
	if factory == nil {
		return nil, ErrInvalidInput
	}
	return NewProcessor(ctx, factory.repository, factory.publisher, factory.engine, ProcessorBinding{Owner: owner, Session: session}, factory.options)
}

type processEventProcessorFactory struct{ process ProcessEvent }

func (factory processEventProcessorFactory) New(ctx context.Context, owner OwnerFence, session Session) (SessionProcessor, error) {
	return processEventSessionProcessor{ctx: ctx, owner: owner, session: session, process: factory.process}, nil
}

type processEventSessionProcessor struct {
	ctx     context.Context
	owner   OwnerFence
	session Session
	process ProcessEvent
}

func (processor processEventSessionProcessor) Accept(event roomsource.Event) error {
	return processor.process(processor.ctx, processor.owner, processor.session, event)
}
func (processEventSessionProcessor) Close(context.Context) error { return nil }
func (processEventSessionProcessor) Status() ProcessorStatus     { return ProcessorStatus{} }
func (processEventSessionProcessor) SetConnectionHealthy(bool)   {}

type ProcessorBinding struct {
	Owner   OwnerFence
	Session Session
}

type ProcessorOptions struct {
	Now           func() time.Time
	NewRetryTimer func(time.Duration) Timer
	Alert         func(ProcessorStatus)
}

type ProcessorStatus struct {
	AccountID         int64 `json:"accountId"`
	LiveSessionID     int64 `json:"liveSessionId"`
	Degraded          bool  `json:"degraded"`
	Buffered          int   `json:"buffered"`
	Rejecting         bool  `json:"rejecting"`
	ConnectionHealthy bool  `json:"connectionHealthy"`
}

type bufferedRoomEvent struct {
	event    roomsource.Event
	queuedAt time.Time
}

type Processor struct {
	mu                sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	cancelOnce        sync.Once
	clearOnce         sync.Once
	ownershipLostOnce sync.Once
	ownershipMu       sync.Mutex
	ownershipLost     func()
	repository        ProcessorRepository
	publisher         SnapshotPublisher
	engine            gameplay.Engine
	binding           ProcessorBinding
	definition        configuration.Definition
	state             configuration.State
	viewers           *ViewerLedger
	now               func() time.Time
	newRetryTimer     func(time.Duration) Timer
	alert             func(ProcessorStatus)
	buffer            []bufferedRoomEvent
	localSeen         map[[32]byte]struct{}
	localSeenOrder    [][32]byte
	retryAttempt      int
	retryTimer        Timer
	retrying          bool
	degraded          bool
	rejecting         bool
	connectionHealthy atomic.Bool
	statusDegraded    atomic.Bool
	statusRejecting   atomic.Bool
	statusBuffered    atomic.Int64
	terminal          bool
	closing           bool
	retryStop         chan struct{}
	retryStopOnce     sync.Once
	retryWorkers      sync.WaitGroup
	drained           chan struct{}
	drainedOnce       sync.Once
}

var ErrWrongRoom = errors.New("runtime: event room does not match session")
var ErrPersistenceUnavailable = errors.New("runtime: persistence unavailable")

const maxBufferedEvents = 500
const maxBufferedAge = 60 * time.Second
const maxProcessLocalReceipts = 4096

func NewProcessor(ctx context.Context, repository ProcessorRepository, publisher SnapshotPublisher, engine gameplay.Engine, binding ProcessorBinding, options ProcessorOptions) (*Processor, error) {
	if ctx == nil || repository == nil || publisher == nil || !validOwnerFence(binding.Owner) || binding.Session.ID <= 0 || binding.Session.AccountID != binding.Owner.AccountID || binding.Session.RoomID == "" {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewRetryTimer == nil {
		options.NewRetryTimer = newSystemTimer
	}
	version, state, err := repository.LoadActive(ctx, binding.Owner.AccountID)
	if err != nil || version.ID != binding.Session.ConfigVersionID || state.ConfigVersionID != version.ID || state.AccountID != binding.Owner.AccountID || state.Revision == 0 {
		return nil, ErrUnavailable
	}
	if _, err := configuration.Join(version.Definition, state.Runtime); err != nil {
		return nil, ErrUnavailable
	}
	processorContext, cancelProcessor := context.WithCancel(ctx)
	processor := &Processor{ctx: processorContext, cancel: cancelProcessor, repository: repository, publisher: publisher, engine: engine, binding: binding, definition: version.Definition, state: state, viewers: NewViewerLedger(), now: options.Now, newRetryTimer: options.NewRetryTimer, alert: options.Alert, localSeen: make(map[[32]byte]struct{}), retryStop: make(chan struct{}), drained: make(chan struct{})}
	processor.connectionHealthy.Store(true)
	return processor, nil
}

func (processor *Processor) Accept(event roomsource.Event) error {
	if processor == nil {
		return ErrInvalidInput
	}
	processor.mu.Lock()
	if processor.closing {
		processor.mu.Unlock()
		return ErrClosed
	}
	if event.RoomID != processor.binding.Session.RoomID {
		processor.mu.Unlock()
		return ErrWrongRoom
	}
	if _, mutation := gameplayGift(event); !mutation {
		processor.mu.Unlock()
		return nil
	}
	if processor.terminal {
		processor.mu.Unlock()
		return ErrPersistenceUnavailable
	}
	now := processor.now()
	if len(processor.buffer) > 0 {
		if len(processor.buffer) >= maxBufferedEvents || now.Sub(processor.buffer[0].queuedAt) >= maxBufferedAge {
			wasRejecting := processor.rejecting
			processor.rejecting = true
			status := processor.statusLocked()
			processor.mu.Unlock()
			if !wasRejecting {
				processor.sendAlert(status)
			}
			return ErrPersistenceUnavailable
		}
		processor.buffer = append(processor.buffer, bufferedRoomEvent{event: cloneProcessorEvent(event), queuedAt: now})
		processor.syncStatusLocked()
		processor.mu.Unlock()
		return nil
	}
	snapshot, err := processor.applyLocked(event, now)
	if isTransientPersistenceError(err) {
		processor.buffer = append(processor.buffer, bufferedRoomEvent{event: cloneProcessorEvent(event), queuedAt: now})
		wasDegraded := processor.degraded
		processor.degraded = true
		processor.scheduleRetryLocked()
		status := processor.statusLocked()
		processor.mu.Unlock()
		if !wasDegraded {
			processor.sendAlert(status)
		}
		return nil
	}
	if err != nil {
		processor.degraded = true
		processor.rejecting = true
		processor.terminal = true
		status := processor.statusLocked()
		processor.mu.Unlock()
		processor.sendAlert(status)
		if errors.Is(err, configuration.ErrOwnership) {
			return ErrOwnershipConflict
		}
		return err
	}
	if snapshot != nil {
		processor.publisher.Publish(*snapshot)
	}
	processor.mu.Unlock()
	return nil
}

func (processor *Processor) applyLocked(event roomsource.Event, now time.Time) (*DisplaySnapshot, error) {
	gift, ok := gameplayGift(event)
	if !ok {
		return nil, nil
	}
	localFingerprint := [32]byte{}
	if event.ID == "" {
		localFingerprint = fingerprintRoomEvent(event, gift)
		if _, duplicate := processor.localSeen[localFingerprint]; duplicate {
			return nil, nil
		}
	}
	current, err := configuration.Join(processor.definition, processor.state.Runtime)
	if err != nil {
		return nil, ErrUnavailable
	}
	transition, err := processor.engine.ApplyGift(current, gift, now)
	if err != nil {
		return nil, ErrInvalidInput
	}
	_, nextRuntime, err := configuration.Split(transition.Next)
	if err != nil {
		return nil, ErrUnavailable
	}
	aggregateDelta := configuration.RuntimeAggregate{EventCount: 1, GiftCount: gift.Count, GiftCoin: gift.Price * float64(gift.Count)}
	command := configuration.RuntimeEventCommand{
		AccountID: processor.binding.Owner.AccountID, LiveSessionID: processor.binding.Session.ID,
		ConfigVersionID: processor.binding.Session.ConfigVersionID,
		OwnerToken:      [32]byte(processor.binding.Owner.Token), OwnerEpoch: processor.binding.Owner.Epoch,
		ExpectedRevision: processor.state.Revision, Runtime: nextRuntime, AggregateDelta: aggregateDelta,
		UpdatedAt: now,
	}
	if event.ID != "" {
		hash := sha256.Sum256([]byte(event.ID))
		command.StableEventHash = &hash
	}
	result, err := processor.repository.CommitRuntimeEvent(processor.ctx, command)
	if err != nil {
		return nil, err
	}
	if result.Duplicate {
		if result.Revision > processor.state.Revision {
			if err := processor.reloadCanonicalStateLocked(result.Revision); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if result.Revision != processor.state.Revision+1 {
		return nil, ErrUnavailable
	}
	processor.state.Runtime = nextRuntime
	processor.state.Revision = result.Revision
	processor.state.UpdatedAt = now
	if event.ID == "" {
		processor.localSeen[localFingerprint] = struct{}{}
		processor.localSeenOrder = append(processor.localSeenOrder, localFingerprint)
		if len(processor.localSeenOrder) > maxProcessLocalReceipts {
			delete(processor.localSeen, processor.localSeenOrder[0])
			processor.localSeenOrder = processor.localSeenOrder[1:]
		}
	}
	processor.viewers.Record(event.Viewer, gift)
	snapshot := DisplaySnapshot{AccountID: processor.binding.Owner.AccountID, LiveSessionID: processor.binding.Session.ID, Revision: result.Revision, Runtime: nextRuntime, Effects: transition.Effects, Viewers: processor.viewers.Rows()}
	return &snapshot, nil
}

func (processor *Processor) reloadCanonicalStateLocked(minimumRevision uint64) error {
	version, state, err := processor.repository.LoadActive(processor.ctx, processor.binding.Owner.AccountID)
	if err != nil {
		return err
	}
	if version.ID != processor.binding.Session.ConfigVersionID ||
		version.AccountID != processor.binding.Owner.AccountID ||
		state.AccountID != processor.binding.Owner.AccountID ||
		state.ConfigVersionID != version.ID ||
		state.Revision < minimumRevision {
		return configuration.ErrRevisionConflict
	}
	if _, err := configuration.Join(version.Definition, state.Runtime); err != nil {
		return configuration.ErrRevisionConflict
	}
	processor.definition = version.Definition
	processor.state = state
	return nil
}

func (processor *Processor) Viewers() []ViewerRow {
	if processor == nil {
		return nil
	}
	return processor.viewers.Rows()
}

func (processor *Processor) RuntimeState() configuration.RuntimeState {
	if processor == nil {
		return configuration.RuntimeState{}
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()
	encoded, _ := json.Marshal(processor.state.Runtime)
	var detached configuration.RuntimeState
	_ = json.Unmarshal(encoded, &detached)
	return detached
}

func (processor *Processor) Status() ProcessorStatus {
	if processor == nil {
		return ProcessorStatus{}
	}
	return ProcessorStatus{AccountID: processor.binding.Owner.AccountID, LiveSessionID: processor.binding.Session.ID, Degraded: processor.statusDegraded.Load(), Buffered: int(processor.statusBuffered.Load()), Rejecting: processor.statusRejecting.Load(), ConnectionHealthy: processor.connectionHealthy.Load()}
}

func (processor *Processor) statusLocked() ProcessorStatus {
	processor.syncStatusLocked()
	return processor.Status()
}

func (processor *Processor) syncStatusLocked() {
	processor.statusDegraded.Store(processor.degraded)
	processor.statusRejecting.Store(processor.rejecting)
	processor.statusBuffered.Store(int64(len(processor.buffer)))
}

func (processor *Processor) SetConnectionHealthy(healthy bool) {
	if processor == nil {
		return
	}
	processor.connectionHealthy.Store(healthy)
}

func (processor *Processor) SetOwnershipLost(callback func()) {
	if processor == nil {
		return
	}
	processor.ownershipMu.Lock()
	processor.ownershipLost = callback
	processor.ownershipMu.Unlock()
}

func (processor *Processor) notifyOwnershipLost() {
	processor.ownershipMu.Lock()
	callback := processor.ownershipLost
	processor.ownershipMu.Unlock()
	if callback == nil {
		return
	}
	processor.ownershipLostOnce.Do(func() { go callback() })
}

func (processor *Processor) Close(ctx context.Context) error {
	if processor == nil || ctx == nil {
		return ErrInvalidInput
	}
	processor.mu.Lock()
	processor.closing = true
	if len(processor.buffer) == 0 {
		processor.signalDrainedLocked()
	}
	drained := processor.drained
	processor.mu.Unlock()
	select {
	case <-drained:
		processor.cancelOnce.Do(processor.cancel)
		processor.retryWorkers.Wait()
		processor.clearPublishedSession()
		return nil
	case <-ctx.Done():
		processor.cancelOnce.Do(processor.cancel)
		processor.retryStopOnce.Do(func() { close(processor.retryStop) })
		processor.mu.Lock()
		if processor.retryTimer != nil {
			processor.retryTimer.Stop()
		}
		processor.mu.Unlock()
		processor.retryWorkers.Wait()
		processor.mu.Lock()
		for index := range processor.buffer {
			processor.buffer[index] = bufferedRoomEvent{}
		}
		processor.buffer = nil
		processor.degraded = true
		processor.rejecting = true
		processor.terminal = true
		processor.syncStatusLocked()
		processor.signalDrainedLocked()
		processor.mu.Unlock()
		processor.clearPublishedSession()
		return ctx.Err()
	}
}

func (processor *Processor) clearPublishedSession() {
	processor.clearOnce.Do(func() {
		processor.viewers.Clear()
		if cleaner, ok := processor.publisher.(sessionSnapshotCleaner); ok {
			cleaner.Clear(processor.binding.Owner.AccountID, processor.binding.Session.ID)
		}
	})
}

func (processor *Processor) scheduleRetryLocked() {
	if processor.retrying || len(processor.buffer) == 0 {
		return
	}
	processor.retryAttempt++
	delay := time.Second << min(processor.retryAttempt-1, 5)
	if remaining := maxBufferedAge - processor.now().Sub(processor.buffer[0].queuedAt); delay > remaining {
		delay = remaining
	}
	if delay < 0 {
		delay = 0
	}
	processor.retryTimer = processor.newRetryTimer(delay)
	timer := processor.retryTimer
	processor.retrying = true
	processor.retryWorkers.Add(1)
	go func() {
		defer processor.retryWorkers.Done()
		select {
		case <-timer.C():
			processor.retryBuffered()
		case <-processor.retryStop:
		}
	}()
}

func (processor *Processor) retryBuffered() {
	for {
		processor.mu.Lock()
		processor.retrying = false
		if len(processor.buffer) == 0 {
			processor.degraded = false
			processor.rejecting = false
			processor.signalDrainedLocked()
			processor.syncStatusLocked()
			processor.mu.Unlock()
			return
		}
		buffered := processor.buffer[0]
		if processor.now().Sub(buffered.queuedAt) >= maxBufferedAge {
			for index := range processor.buffer {
				processor.buffer[index] = bufferedRoomEvent{}
			}
			processor.buffer = nil
			processor.degraded = true
			processor.rejecting = true
			processor.terminal = true
			processor.signalDrainedLocked()
			status := processor.statusLocked()
			processor.mu.Unlock()
			processor.sendAlert(status)
			return
		}
		snapshot, err := processor.applyLocked(buffered.event, processor.now())
		if isTransientPersistenceError(err) {
			processor.scheduleRetryLocked()
			processor.mu.Unlock()
			return
		}
		if err != nil {
			ownershipLost := errors.Is(err, configuration.ErrOwnership)
			for index := range processor.buffer {
				processor.buffer[index] = bufferedRoomEvent{}
			}
			processor.buffer = nil
			processor.degraded = true
			processor.rejecting = true
			processor.terminal = true
			processor.signalDrainedLocked()
			status := processor.statusLocked()
			processor.mu.Unlock()
			if ownershipLost {
				processor.notifyOwnershipLost()
			}
			processor.sendAlert(status)
			return
		}
		processor.buffer[0] = bufferedRoomEvent{}
		processor.buffer = processor.buffer[1:]
		if len(processor.buffer) == 0 {
			processor.buffer = nil
		}
		processor.retryAttempt = 0
		if len(processor.buffer) == 0 {
			processor.degraded = false
			processor.rejecting = false
			processor.signalDrainedLocked()
		}
		if snapshot != nil {
			processor.publisher.Publish(*snapshot)
		}
		processor.syncStatusLocked()
		processor.mu.Unlock()
	}
}

func (processor *Processor) signalDrainedLocked() {
	if processor.closing && len(processor.buffer) == 0 {
		processor.drainedOnce.Do(func() { close(processor.drained) })
	}
}

func (processor *Processor) sendAlert(status ProcessorStatus) {
	if processor.alert != nil {
		processor.alert(status)
	}
}

func isTransientPersistenceError(err error) bool {
	return errors.Is(err, configuration.ErrUnavailable)
}

func fingerprintRoomEvent(event roomsource.Event, gift gameplay.Gift) [32]byte {
	canonicalData := append([]byte(nil), event.Data...)
	decoder := json.NewDecoder(bytes.NewReader(event.Data))
	decoder.UseNumber()
	var payload any
	if decoder.Decode(&payload) == nil && decoder.Decode(&struct{}{}) == io.EOF {
		if normalized, err := json.Marshal(payload); err == nil {
			canonicalData = normalized
		}
	}
	encoded, _ := json.Marshal(struct {
		Type     string            `json:"type"`
		RoomID   string            `json:"roomId"`
		Data     json.RawMessage   `json:"data"`
		Gift     gameplay.Gift     `json:"gift"`
		UID      int64             `json:"uid"`
		Uname    string            `json:"uname"`
		Avatar   string            `json:"avatar"`
		Metadata map[string]string `json:"metadata"`
		Viewer   map[string]string `json:"viewerMetadata"`
	}{Type: event.Type, RoomID: event.RoomID, Data: canonicalData, Gift: gift, UID: event.Viewer.UID, Uname: event.Viewer.Uname, Avatar: event.Viewer.Avatar, Metadata: event.Metadata, Viewer: event.Viewer.Metadata})
	return sha256.Sum256(encoded)
}

func cloneProcessorEvent(event roomsource.Event) roomsource.Event {
	event.Data = append([]byte(nil), event.Data...)
	event.Metadata = cloneProcessorStrings(event.Metadata)
	event.Viewer.Metadata = cloneProcessorStrings(event.Viewer.Metadata)
	if event.Paid != nil {
		paid := *event.Paid
		event.Paid = &paid
	}
	return event
}

func cloneProcessorStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func gameplayGift(event roomsource.Event) (gameplay.Gift, bool) {
	paid := event.Paid
	if paid == nil || paid.Count <= 0 || paid.UnitPrice < 0 || paid.BlindGiftID < 0 {
		return gameplay.Gift{}, false
	}
	identityRank, ok := gameplayIdentityRank(paid.GuardLevel, paid.HasFanMedal)
	if !ok {
		return gameplay.Gift{}, false
	}
	gift := gameplay.Gift{Count: paid.Count, Price: paid.UnitPrice, IdentityRank: identityRank, OccurredAtMillis: paid.OccurredAtMillis}
	switch event.Type {
	case "SEND_GIFT":
		if paid.GiftID <= 0 {
			return gameplay.Gift{}, false
		}
		gift.GiftID = paid.GiftID
		gift.BlindGiftID = paid.BlindGiftID
		return gift, true
	case "GUARD_BUY":
		if paid.GuardLevel < 1 || paid.GuardLevel > 3 {
			return gameplay.Gift{}, false
		}
		gift.GiftID = 1_900_000_004 - paid.GuardLevel
		return gift, true
	case "SUPER_CHAT_MESSAGE", "SUPER_CHAT_MESSAGE_JPN":
		if paid.UnitPrice <= 0 {
			return gameplay.Gift{}, false
		}
		gift.GiftID = 1_900_000_004
		return gift, true
	default:
		return gameplay.Gift{}, false
	}
}

func gameplayIdentityRank(guardLevel int, hasFanMedal bool) (int, bool) {
	switch guardLevel {
	case 3:
		return 2, true
	case 2:
		return 3, true
	case 1:
		return 4, true
	case 0:
		if hasFanMedal {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}
