package roomwatcher

import "time"

type State string

const (
	StateOffline State = "offline"
	StateLive    State = "live"
	StateGrace   State = "grace"

	GracePeriod = 10 * time.Minute
)

// Transition is the durable, room-scoped boundary emitted by a watcher. A
// recovered grace period deliberately does not open a second broadcast.
type Transition struct {
	RoomID       string
	From         State
	To           State
	ConfirmedAt  time.Time
	GraceUntil   *time.Time
	NewBroadcast bool
	// Sequence and LeaseEpoch are assigned transactionally by Repository.
	// Consumers use the returned pair to fence stale transition delivery.
	Sequence   uint64
	LeaseEpoch uint64
}

// StateMachine is deliberately independent of probes, repositories, and
// timers so its live-session boundaries remain deterministic.
type StateMachine struct {
	state       State
	gracePeriod time.Duration
	graceUntil  time.Time
}

// NewStateMachine always uses the product-mandated ten-minute grace period.
// Tests inside this package may use newStateMachine for a shorter clock seam.
func NewStateMachine() *StateMachine {
	return newStateMachine(GracePeriod)
}

func newStateMachine(gracePeriod time.Duration) *StateMachine {
	return &StateMachine{state: StateOffline, gracePeriod: gracePeriod}
}

// Observe returns every durable boundary caused by one observation. A delayed
// live result first closes an expired grace period and only then opens a new
// broadcast, so callers cannot accidentally merge distinct broadcasts.
func (machine *StateMachine) Observe(observed State, confirmedAt time.Time) []Transition {
	if machine == nil {
		return nil
	}
	transitions := make([]Transition, 0, 2)
	if machine.state == StateGrace && !confirmedAt.Before(machine.graceUntil) {
		transitions = append(transitions, machine.Advance(confirmedAt))
	}
	if transition, changed := machine.observeOne(observed, confirmedAt); changed {
		transitions = append(transitions, transition)
	}
	return transitions
}

func (machine *StateMachine) observeOne(observed State, confirmedAt time.Time) (Transition, bool) {
	from := machine.state
	transition := Transition{From: from, To: from, ConfirmedAt: confirmedAt}
	switch {
	case from == StateOffline && observed == StateLive:
		machine.state = StateLive
		transition.To = StateLive
		transition.NewBroadcast = true
		return transition, true
	case from == StateLive && observed == StateOffline:
		machine.state = StateGrace
		machine.graceUntil = confirmedAt.Add(machine.gracePeriod)
		transition.To = StateGrace
		transition.GraceUntil = timePointer(machine.graceUntil)
		return transition, true
	case from == StateGrace && observed == StateLive:
		machine.state = StateLive
		machine.graceUntil = time.Time{}
		transition.To = StateLive
		return transition, true
	}
	return Transition{}, false
}

func (machine *StateMachine) Advance(now time.Time) Transition {
	if machine == nil {
		return Transition{}
	}
	transition := Transition{From: machine.state, To: machine.state, ConfirmedAt: now}
	if machine.state != StateGrace {
		return transition
	}
	if now.Before(machine.graceUntil) {
		transition.GraceUntil = timePointer(machine.graceUntil)
		return transition
	}
	endedAt := machine.graceUntil
	machine.state = StateOffline
	machine.graceUntil = time.Time{}
	transition.To = StateOffline
	transition.ConfirmedAt = endedAt
	return transition
}

func (machine *StateMachine) close(now time.Time) Transition {
	if machine == nil {
		return Transition{}
	}
	transition := Transition{From: machine.state, To: StateOffline, ConfirmedAt: now}
	machine.state = StateOffline
	machine.graceUntil = time.Time{}
	return transition
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
