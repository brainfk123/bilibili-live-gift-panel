package roomwatcher

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidInput = errors.New("roomwatcher: invalid input")
	ErrClosed       = errors.New("roomwatcher: closed")
)

// MaxReplayLimit bounds one outbox page before either manager or repository
// allocates a result slice from a caller-provided limit.
const MaxReplayLimit = 256

type ObservedState string

const (
	ObservedOffline ObservedState = "offline"
	ObservedLive    ObservedState = "live"
)

// Probe determines only the current room live state. It must not infer live
// state from gifts because those may arrive while a room is offline.
type Probe interface {
	Probe(context.Context, string) (ObservedState, error)
}

// Repository persists shared room references and state transitions. Its MySQL
// implementation belongs outside this package's state-machine core.
type Repository interface {
	// SyncReferences atomically replaces the reference snapshot and persists
	// complete snapshots for changed rooms plus final state boundaries caused
	// by removed rooms. Returned events are the only notifications Manager may
	// publish.
	SyncReferences(context.Context, []Reference, []Transition) ([]Event, error)
	// LoadRecoverable exposes persisted live/grace watchers to startup
	// composition without requiring a concrete SQL repository type.
	LoadRecoverable(context.Context) ([]RecoverableRoom, error)
	// LoadBootstrap reads the current room projection and the outbox cursor from
	// one consistent database snapshot so restart never replays historical live
	// side effects to reconstruct current state.
	LoadBootstrap(context.Context) (Bootstrap, error)
	// RecordTransition atomically records the candidate and returns its durable
	// Event envelope with a monotonically increasing Sequence and LeaseEpoch.
	RecordTransition(context.Context, Transition) (Event, error)
	// ReplayEvents returns the durable outbox strictly after afterSequence,
	// in increasing Sequence order. Notifications are bounded wake-ups only;
	// consumers resume losslessly from this repository cursor.
	ReplayEvents(context.Context, uint64, int) ([]Event, error)
}

// Reference is one enabled account's use of a canonical room.
type Reference struct {
	AccountID int64
	RoomID    string
}

type Bootstrap struct {
	Cursor uint64
	Rooms  []BootstrapRoom
}

type BootstrapRoom struct {
	RoomID             string
	State              State
	GraceUntil         *time.Time
	BroadcastSessionID int64
	LeaseEpoch         uint64
	AccountIDs         []int64
}

// RoomReferencesChanged is the complete enabled-account snapshot for one
// canonical room. AccountIDs are strictly increasing and duplicate-free.
type RoomReferencesChanged struct {
	RoomID     string
	AccountIDs []int64
}

// Event is the only durable room-watcher delivery contract. Exactly one
// payload is present; both kinds share the same commit-ordered Sequence.
type Event struct {
	Sequence              uint64
	RoomStateChanged      *Transition
	RoomReferencesChanged *RoomReferencesChanged
}

type Options struct {
	Now func() time.Time

	gracePeriod  time.Duration
	beforeNotify func(Event)
}

type Manager struct {
	probe      Probe
	repository Repository
	now        func() time.Time
	grace      time.Duration

	mu           sync.Mutex
	watchers     map[string]*watcher
	events       chan Event
	done         chan struct{}
	closed       bool
	beforeNotify func(Event)
}

type watcher struct {
	machine *StateMachine
	refs    int
}

func NewManager(probe Probe, repository Repository, options Options) (*Manager, error) {
	if probe == nil || repository == nil {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.gracePeriod == 0 {
		options.gracePeriod = GracePeriod
	}
	if options.gracePeriod <= 0 {
		return nil, ErrInvalidInput
	}
	manager := &Manager{
		probe: probe, repository: repository, now: options.Now, grace: options.gracePeriod,
		watchers: make(map[string]*watcher), events: make(chan Event, 1), done: make(chan struct{}), beforeNotify: options.beforeNotify,
	}
	return manager, nil
}

func (manager *Manager) SetReferences(ctx context.Context, references []Reference) error {
	if manager == nil || ctx == nil {
		return ErrInvalidInput
	}
	normalized, counts, err := normalizeReferences(references)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	now := manager.now()
	terminal := make([]Transition, 0)
	removedRooms := make([]string, 0)
	for roomID, current := range manager.watchers {
		if counts[roomID] != 0 {
			continue
		}
		candidate := *current.machine
		transition := candidate.close(now)
		if transition.From != transition.To {
			transition.RoomID = roomID
			terminal = append(terminal, transition)
		}
		removedRooms = append(removedRooms, roomID)
	}
	persistedEvents, err := manager.repository.SyncReferences(ctx, normalized, terminal)
	if err != nil {
		return err
	}
	for _, roomID := range removedRooms {
		delete(manager.watchers, roomID)
	}
	for _, event := range persistedEvents {
		manager.notifyLocked(event)
	}
	rooms := make([]string, 0, len(counts))
	for roomID := range counts {
		rooms = append(rooms, roomID)
	}
	sort.Strings(rooms)
	for _, roomID := range rooms {
		current := manager.watchers[roomID]
		if current == nil {
			current = &watcher{machine: newStateMachine(manager.grace)}
			persisted, err := manager.probeLocked(ctx, roomID, current)
			if err != nil {
				return err
			}
			for _, event := range persisted {
				manager.notifyLocked(event)
			}
			manager.watchers[roomID] = current
		}
		current.refs = counts[roomID]
	}
	return nil
}

func (manager *Manager) Events() <-chan Event {
	if manager == nil {
		return nil
	}
	return manager.events
}

// ReplayEvents resumes the durable outbox after the caller's last
// applied Sequence. It remains available after Close so a final notification
// can be drained before the consumer releases its replay cursor.
func (manager *Manager) ReplayEvents(ctx context.Context, afterSequence uint64, limit int) ([]Event, error) {
	if manager == nil || ctx == nil || limit <= 0 || limit > MaxReplayLimit {
		return nil, ErrInvalidInput
	}
	return manager.repository.ReplayEvents(ctx, afterSequence, limit)
}

func (manager *Manager) LoadBootstrap(ctx context.Context) (Bootstrap, error) {
	if manager == nil || ctx == nil {
		return Bootstrap{}, ErrInvalidInput
	}
	return manager.repository.LoadBootstrap(ctx)
}

// Close rejects future writes, retains one already-buffered wake-up for a
// final outbox replay, then closes Transitions after that wake-up is drained.
func (manager *Manager) Close() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	if !manager.closed {
		manager.closed = true
		close(manager.events)
		close(manager.done)
	}
	manager.mu.Unlock()
}

func (manager *Manager) Wait(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidInput
	}
	select {
	case <-manager.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) probeLocked(ctx context.Context, roomID string, current *watcher) ([]Event, error) {
	observed, err := manager.probe.Probe(ctx, roomID)
	if err != nil {
		return nil, err
	}
	state, err := observedState(observed)
	if err != nil {
		return nil, err
	}
	transitions := current.machine.Observe(state, manager.now())
	persisted := make([]Event, 0, len(transitions))
	for _, transition := range transitions {
		durable, changed, err := manager.persistLocked(ctx, roomID, transition)
		if err != nil {
			return nil, err
		}
		if changed {
			persisted = append(persisted, durable)
		}
	}
	return persisted, nil
}

func (manager *Manager) persistLocked(ctx context.Context, roomID string, transition Transition) (Event, bool, error) {
	if transition.From == transition.To {
		return Event{}, false, nil
	}
	transition.RoomID = roomID
	persisted, err := manager.repository.RecordTransition(ctx, transition)
	if err != nil {
		return Event{}, false, err
	}
	return persisted, true, nil
}

// notifyLocked is intentionally bounded and non-blocking. The notification
// is sent while manager.mu serializes RecordTransition calls, so every
// delivered notification is strictly ordered by durable Sequence. A full
// buffer coalesces later notifications; consumers recover them through the
// repository outbox rather than an unbounded in-memory queue.
func (manager *Manager) notifyLocked(event Event) {
	if manager.beforeNotify != nil {
		manager.beforeNotify(event)
	}
	select {
	case manager.events <- event:
	default:
	}
}

func normalizeReferences(references []Reference) ([]Reference, map[string]int, error) {
	unique := make(map[Reference]struct{}, len(references))
	counts := make(map[string]int)
	for _, reference := range references {
		if reference.AccountID <= 0 {
			return nil, nil, ErrInvalidInput
		}
		roomID, err := canonicalRoomID(reference.RoomID)
		if err != nil {
			return nil, nil, ErrInvalidInput
		}
		reference.RoomID = roomID
		if _, exists := unique[reference]; exists {
			continue
		}
		unique[reference] = struct{}{}
		counts[roomID]++
	}
	normalized := make([]Reference, 0, len(unique))
	for reference := range unique {
		normalized = append(normalized, reference)
	}
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].RoomID == normalized[right].RoomID {
			return normalized[left].AccountID < normalized[right].AccountID
		}
		return normalized[left].RoomID < normalized[right].RoomID
	})
	return normalized, counts, nil
}

func canonicalRoomID(input string) (string, error) {
	numeric, err := strconv.ParseUint(strings.TrimSpace(input), 10, 64)
	if err != nil || numeric == 0 {
		return "", ErrInvalidInput
	}
	return strconv.FormatUint(numeric, 10), nil
}

func observedState(observed ObservedState) (State, error) {
	switch observed {
	case ObservedOffline:
		return StateOffline, nil
	case ObservedLive:
		return StateLive, nil
	default:
		return "", ErrInvalidInput
	}
}
