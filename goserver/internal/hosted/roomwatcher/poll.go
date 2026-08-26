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
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	rooms := make([]string, 0, len(manager.watchers))
	for roomID := range manager.watchers {
		rooms = append(rooms, roomID)
	}
	sort.Strings(rooms)
	var result error
	for _, roomID := range rooms {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		current := manager.watchers[roomID]
		observed, err := manager.probe.Probe(ctx, roomID)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		state, err := observedState(observed)
		if err != nil {
			result = errors.Join(result, err)
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
	}
	return result
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
