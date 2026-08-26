package roomwatcher

import (
	"context"
	"errors"
	"sort"
	"time"
)

// RestoreBootstrap installs the watcher-side state projection from the same
// transactionally consistent snapshot runtime.Manager consumed. It performs
// no I/O and must run before SetReferences or Poll.
func (manager *Manager) RestoreBootstrap(bootstrap Bootstrap) error {
	if manager == nil || !validWatcherBootstrap(bootstrap) {
		return ErrInvalidInput
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || len(manager.watchers) != 0 {
		return ErrClosed
	}
	for _, room := range bootstrap.Rooms {
		machine := newStateMachine(manager.grace)
		machine.state = room.State
		if room.GraceUntil != nil {
			machine.graceUntil = *room.GraceUntil
		}
		manager.watchers[room.RoomID] = &watcher{machine: machine, refs: len(room.AccountIDs)}
	}
	return nil
}

func validWatcherBootstrap(bootstrap Bootstrap) bool {
	previousRoom := ""
	for _, room := range bootstrap.Rooms {
		canonical, err := canonicalRoomID(room.RoomID)
		if err != nil || canonical != room.RoomID || (previousRoom != "" && previousRoom >= room.RoomID) || room.LeaseEpoch == 0 || len(room.AccountIDs) == 0 {
			return false
		}
		previousRoom = room.RoomID
		switch room.State {
		case StateOffline:
			if room.GraceUntil != nil || room.BroadcastSessionID != 0 {
				return false
			}
		case StateLive:
			if room.GraceUntil != nil || room.BroadcastSessionID <= 0 {
				return false
			}
		case StateGrace:
			if room.GraceUntil == nil || room.GraceUntil.IsZero() || room.BroadcastSessionID <= 0 {
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
			previousAccount = accountID
		}
	}
	return true
}

// Poll probes every currently referenced canonical room exactly once. It is
// the narrow scheduling seam owned by production composition; cadence and
// retry policy remain outside the state-machine module.
func (manager *Manager) Poll(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidInput
	}
	return manager.poll(ctx, nil)
}

func (manager *Manager) poll(ctx context.Context, selected map[string]*watcher) error {
	manager.pollMu.Lock()
	defer manager.pollMu.Unlock()
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return ErrClosed
	}
	rooms := make([]string, 0, len(manager.watchers))
	for roomID, current := range manager.watchers {
		if selected != nil && selected[roomID] != current {
			continue
		}
		rooms = append(rooms, roomID)
	}
	sort.Strings(rooms)
	if len(rooms) == 0 {
		manager.updateProbeStatusLocked(0)
		manager.mu.Unlock()
		return nil
	}
	if manager.pollRemaining <= 0 || manager.pollRemaining > len(manager.watchers) {
		manager.pollRemaining = len(manager.watchers)
	}
	rooms = rotateRooms(rooms, manager.nextRoom)
	budget := len(rooms)
	if probeBudget, ok := manager.probe.(ProbeBudget); ok {
		budget = min(budget, max(0, probeBudget.AvailableProbeBudget()))
		manager.probeStatus.CapacityPerMinute = probeBudget.ProbeCapacityPerMinute()
		manager.probeStatus.Available = probeBudget.AvailableProbeBudget()
	}
	budget = min(budget, manager.pollRemaining)
	if budget == 0 {
		manager.updateProbeStatusLocked(manager.pollRemaining)
		manager.mu.Unlock()
		return nil
	}
	manager.mu.Unlock()
	var result error
	for index := 0; index < budget; index++ {
		roomID := rooms[index]
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		manager.mu.Lock()
		current := manager.watchers[roomID]
		manager.mu.Unlock()
		if current == nil || selected != nil && selected[roomID] != current {
			manager.advanceProbeCursor(rooms, index)
			continue
		}
		observed, err := manager.probe.Probe(ctx, roomID)
		if err != nil {
			if errors.Is(err, ErrProbeBudgetExhausted) {
				manager.mu.Lock()
				manager.nextRoom = roomID
				manager.updateProbeStatusLocked(manager.pollRemaining)
				manager.mu.Unlock()
				return errors.Join(result, err)
			}
			result = errors.Join(result, err)
			manager.advanceProbeCursor(rooms, index)
			continue
		}
		state, err := observedState(observed)
		if err != nil {
			result = errors.Join(result, err)
			manager.advanceProbeCursor(rooms, index)
			continue
		}
		manager.mu.Lock()
		if manager.watchers[roomID] != current {
			manager.mu.Unlock()
			manager.advanceProbeCursor(rooms, index)
			continue
		}
		candidate := *current.machine
		transitions := candidate.Observe(state, manager.now())
		for _, transition := range transitions {
			durable, changed, persistErr := manager.persistLocked(ctx, roomID, transition)
			if persistErr != nil {
				result = errors.Join(result, persistErr)
				break
			}
			if !changed {
				continue
			}
			applyDurableTransition(current.machine, transition)
			manager.notifyLocked(durable)
		}
		manager.mu.Unlock()
		manager.advanceProbeCursor(rooms, index)
	}
	return result
}

func rotateRooms(rooms []string, next string) []string {
	if len(rooms) == 0 || next == "" {
		return rooms
	}
	index := sort.SearchStrings(rooms, next)
	if index == len(rooms) {
		index = 0
	}
	rotated := append([]string(nil), rooms[index:]...)
	return append(rotated, rooms[:index]...)
}

func (manager *Manager) advanceProbeCursor(rooms []string, index int) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.pollRemaining > 0 {
		manager.pollRemaining--
	}
	manager.nextRoom = rooms[(index+1)%len(rooms)]
	manager.updateProbeStatusLocked(manager.pollRemaining)
}

func (manager *Manager) updateProbeStatusLocked(backlog int) {
	manager.probeStatus.Backlog = max(0, backlog)
	if budget, ok := manager.probe.(ProbeBudget); ok {
		manager.probeStatus.CapacityPerMinute = budget.ProbeCapacityPerMinute()
		manager.probeStatus.Available = budget.AvailableProbeBudget()
	}
}

func (manager *Manager) ProbeCapacity() ProbeCapacityStatus {
	if manager == nil {
		return ProbeCapacityStatus{}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.updateProbeStatusLocked(manager.pollRemaining)
	return manager.probeStatus
}

func applyDurableTransition(machine *StateMachine, transition Transition) {
	if machine == nil {
		return
	}
	machine.state = transition.To
	switch transition.To {
	case StateGrace:
		if transition.GraceUntil != nil {
			machine.graceUntil = *transition.GraceUntil
		}
	case StateLive, StateOffline:
		machine.graceUntil = time.Time{}
	}
}
