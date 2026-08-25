package roomwatcher

import (
	"testing"
	"time"
)

func TestStateMachineKeepsOneBroadcastAcrossGraceRecovery(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	machine := NewStateMachine(10 * time.Minute)
	live := machine.Observe(StateLive, now)
	grace := machine.Observe(StateOffline, now.Add(time.Minute))
	recovered := machine.Observe(StateLive, now.Add(5*time.Minute))
	if live.To != StateLive || grace.To != StateGrace || recovered.To != StateLive {
		t.Fatalf("transitions = %#v %#v %#v", live, grace, recovered)
	}
	if recovered.NewBroadcast {
		t.Fatal("grace recovery split the broadcast")
	}
}

func TestStateMachineClosesOnlyWhenGraceExpires(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	machine := NewStateMachine(10 * time.Minute)
	_ = machine.Observe(StateLive, now)
	grace := machine.Observe(StateOffline, now.Add(time.Minute))
	if grace.GraceUntil == nil || !grace.GraceUntil.Equal(now.Add(11*time.Minute)) {
		t.Fatalf("grace deadline = %v, want %v", grace.GraceUntil, now.Add(11*time.Minute))
	}
	if pending := machine.Advance(now.Add(10*time.Minute + 59*time.Second)); pending.To != StateGrace {
		t.Fatalf("transition before deadline = %#v, want grace", pending)
	}
	if closed := machine.Advance(now.Add(11 * time.Minute)); closed.To != StateOffline || closed.GraceUntil != nil {
		t.Fatalf("transition at deadline = %#v, want offline without grace", closed)
	}
}
