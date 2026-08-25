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
)

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
	SyncReferences(context.Context, []Reference) error
	RecordTransition(context.Context, Transition) error
}

// Reference is one enabled account's use of a canonical room.
type Reference struct {
	AccountID int64
	RoomID    string
}

type Options struct {
	Now         func() time.Time
	GracePeriod time.Duration
}

type Manager struct {
	probe      Probe
	repository Repository
	now        func() time.Time
	grace      time.Duration

	mu          sync.Mutex
	watchers    map[string]*watcher
	transitions chan Transition
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
	if options.GracePeriod == 0 {
		options.GracePeriod = 10 * time.Minute
	}
	if options.GracePeriod <= 0 {
		return nil, ErrInvalidInput
	}
	return &Manager{
		probe: probe, repository: repository, now: options.Now, grace: options.GracePeriod,
		watchers: make(map[string]*watcher), transitions: make(chan Transition, 128),
	}, nil
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
	if err := manager.repository.SyncReferences(ctx, normalized); err != nil {
		return err
	}
	now := manager.now()
	for roomID, current := range manager.watchers {
		if counts[roomID] != 0 {
			continue
		}
		if err := manager.persistLocked(ctx, roomID, current.machine.close(now)); err != nil {
			return err
		}
		delete(manager.watchers, roomID)
	}
	rooms := make([]string, 0, len(counts))
	for roomID := range counts {
		rooms = append(rooms, roomID)
	}
	sort.Strings(rooms)
	for _, roomID := range rooms {
		current := manager.watchers[roomID]
		if current == nil {
			current = &watcher{machine: NewStateMachine(manager.grace)}
			manager.watchers[roomID] = current
			if err := manager.probeLocked(ctx, roomID, current); err != nil {
				return err
			}
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

func (manager *Manager) probeLocked(ctx context.Context, roomID string, current *watcher) error {
	observed, err := manager.probe.Probe(ctx, roomID)
	if err != nil {
		return err
	}
	state, err := observedState(observed)
	if err != nil {
		return err
	}
	return manager.persistLocked(ctx, roomID, current.machine.Observe(state, manager.now()))
}

func (manager *Manager) persistLocked(ctx context.Context, roomID string, transition Transition) error {
	if transition.From == transition.To {
		return nil
	}
	transition.RoomID = roomID
	if err := manager.repository.RecordTransition(ctx, transition); err != nil {
		return err
	}
	manager.transitions <- transition
	return nil
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
