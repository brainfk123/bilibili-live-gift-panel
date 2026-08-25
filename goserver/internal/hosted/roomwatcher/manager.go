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
	// final watcher transitions caused by removed rooms. Returned transitions
	// are durable outbox receipts and are the only removal notifications Manager
	// may publish.
	SyncReferences(context.Context, []Reference, []Transition) ([]Transition, error)
	// RecordTransition atomically records the candidate and returns its durable
	// form with a monotonically increasing Sequence and fencing LeaseEpoch.
	RecordTransition(context.Context, Transition) (Transition, error)
	// ReplayTransitions returns the durable outbox strictly after afterSequence,
	// in increasing Sequence order. Notifications are bounded wake-ups only;
	// consumers resume losslessly from this repository cursor.
	ReplayTransitions(context.Context, uint64, int) ([]Transition, error)
}

// Reference is one enabled account's use of a canonical room.
type Reference struct {
	AccountID int64
	RoomID    string
}

type Options struct {
	Now func() time.Time

	gracePeriod  time.Duration
	beforeNotify func(Transition)
}

type Manager struct {
	probe      Probe
	repository Repository
	now        func() time.Time
	grace      time.Duration

	mu           sync.Mutex
	watchers     map[string]*watcher
	transitions  chan Transition
	done         chan struct{}
	closed       bool
	beforeNotify func(Transition)
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
		watchers: make(map[string]*watcher), transitions: make(chan Transition, 1), done: make(chan struct{}), beforeNotify: options.beforeNotify,
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
	persistedTerminal, err := manager.repository.SyncReferences(ctx, normalized, terminal)
	if err != nil {
		return err
	}
	for _, roomID := range removedRooms {
		delete(manager.watchers, roomID)
	}
	for _, transition := range persistedTerminal {
		manager.notifyLocked(transition)
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
			for _, transition := range persisted {
				manager.notifyLocked(transition)
			}
			manager.watchers[roomID] = current
		}
		current.refs = counts[roomID]
	}
	return nil
}

func (manager *Manager) Transitions() <-chan Transition {
	if manager == nil {
		return nil
	}
	return manager.transitions
}

// ReplayTransitions resumes the durable outbox after the caller's last
// applied Sequence. It remains available after Close so a final notification
// can be drained before the consumer releases its replay cursor.
func (manager *Manager) ReplayTransitions(ctx context.Context, afterSequence uint64, limit int) ([]Transition, error) {
	if manager == nil || ctx == nil || limit <= 0 || limit > MaxReplayLimit {
		return nil, ErrInvalidInput
	}
	return manager.repository.ReplayTransitions(ctx, afterSequence, limit)
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
		close(manager.transitions)
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

func (manager *Manager) probeLocked(ctx context.Context, roomID string, current *watcher) ([]Transition, error) {
	observed, err := manager.probe.Probe(ctx, roomID)
	if err != nil {
		return nil, err
	}
	state, err := observedState(observed)
	if err != nil {
		return nil, err
	}
	transitions := current.machine.Observe(state, manager.now())
	persisted := make([]Transition, 0, len(transitions))
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

func (manager *Manager) persistLocked(ctx context.Context, roomID string, transition Transition) (Transition, bool, error) {
	if transition.From == transition.To {
		return Transition{}, false, nil
	}
	transition.RoomID = roomID
	persisted, err := manager.repository.RecordTransition(ctx, transition)
	if err != nil {
		return Transition{}, false, err
	}
	return persisted, true, nil
}

// notifyLocked is intentionally bounded and non-blocking. The notification
// is sent while manager.mu serializes RecordTransition calls, so every
// delivered notification is strictly ordered by durable Sequence. A full
// buffer coalesces later notifications; consumers recover them through the
// repository outbox rather than an unbounded in-memory queue.
func (manager *Manager) notifyLocked(transition Transition) {
	if manager.beforeNotify != nil {
		manager.beforeNotify(transition)
	}
	select {
	case manager.transitions <- transition:
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
