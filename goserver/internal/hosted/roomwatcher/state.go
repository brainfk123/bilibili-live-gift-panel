package roomwatcher

import "time"

type State string

const (
	StateOffline State = "offline"
	StateLive    State = "live"
	StateGrace   State = "grace"
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
}

// StateMachine is deliberately independent of probes, repositories, and
// timers so its live-session boundaries remain deterministic.
type StateMachine struct {
	state       State
	gracePeriod time.Duration
	graceUntil  time.Time
}

func NewStateMachine(gracePeriod time.Duration) *StateMachine {
	return &StateMachine{state: StateOffline, gracePeriod: gracePeriod}
}

func (machine *StateMachine) Observe(observed State, confirmedAt time.Time) Transition {
	if machine == nil {
		return Transition{}
	}
	from := machine.state
	transition := Transition{From: from, To: from, ConfirmedAt: confirmedAt}
	switch {
	case from == StateOffline && observed == StateLive:
		machine.state = StateLive
		transition.To = StateLive
		transition.NewBroadcast = true
	case from == StateLive && observed == StateOffline:
		machine.state = StateGrace
		machine.graceUntil = confirmedAt.Add(machine.gracePeriod)
		transition.To = StateGrace
		transition.GraceUntil = timePointer(machine.graceUntil)
	case from == StateGrace && observed == StateLive:
		machine.state = StateLive
		machine.graceUntil = time.Time{}
		transition.To = StateLive
	case from == StateGrace:
		transition.GraceUntil = timePointer(machine.graceUntil)
	}
	return transition
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
	machine.state = StateOffline
	machine.graceUntil = time.Time{}
	transition.To = StateOffline
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
