package roomwatcher

import (
	"testing"
	"time"
)

func TestStateMachineKeepsOneBroadcastAcrossGraceRecovery(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	machine := NewStateMachine()
	live := machine.Observe(StateLive, now)
	grace := machine.Observe(StateOffline, now.Add(time.Minute))
	recovered := machine.Observe(StateLive, now.Add(5*time.Minute))
	if len(live) != 1 || len(grace) != 1 || len(recovered) != 1 || live[0].To != StateLive || grace[0].To != StateGrace || recovered[0].To != StateLive {
		t.Fatalf("transitions = %#v %#v %#v", live, grace, recovered)
	}
	if recovered[0].NewBroadcast {
		t.Fatal("grace recovery split the broadcast")
	}
}

func TestStateMachineClosesOnlyWhenGraceExpires(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	machine := NewStateMachine()
	_ = machine.Observe(StateLive, now)
	grace := machine.Observe(StateOffline, now.Add(time.Minute))
	if len(grace) != 1 || grace[0].GraceUntil == nil || !grace[0].GraceUntil.Equal(now.Add(11*time.Minute)) {
		t.Fatalf("grace deadline = %#v, want %v", grace, now.Add(11*time.Minute))
	}
	if pending := machine.Advance(now.Add(10*time.Minute + 59*time.Second)); pending.To != StateGrace {
		t.Fatalf("transition before deadline = %#v, want grace", pending)
	}
	if closed := machine.Advance(now.Add(11 * time.Minute)); closed.To != StateOffline || closed.GraceUntil != nil {
		t.Fatalf("transition at deadline = %#v, want offline without grace", closed)
	}
}

func TestStateMachineDelayedLiveClosesExpiredBroadcastBeforeOpeningAnother(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	machine := NewStateMachine()
	_ = machine.Observe(StateLive, now)
	_ = machine.Observe(StateOffline, now.Add(time.Minute))

	transitions := machine.Observe(StateLive, now.Add(12*time.Minute))
	if len(transitions) != 2 {
		t.Fatalf("delayed live transitions = %#v, want close then open", transitions)
	}
	closed, opened := transitions[0], transitions[1]
	if closed.From != StateGrace || closed.To != StateOffline || !closed.ConfirmedAt.Equal(now.Add(11*time.Minute)) {
		t.Fatalf("expired broadcast close = %#v", closed)
	}
	if opened.From != StateOffline || opened.To != StateLive || !opened.NewBroadcast || !opened.ConfirmedAt.Equal(now.Add(12*time.Minute)) {
		t.Fatalf("replacement broadcast open = %#v", opened)
	}
}
