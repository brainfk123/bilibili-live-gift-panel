package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type fakeGiftSource struct {
	started chan string
	events  chan giftEvent
}

func (f *fakeGiftSource) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	callbacks.onState("connected")
	f.started <- roomID
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case gift := <-f.events:
			callbacks.onGift(gift)
		}
	}
}

func TestBackgroundRuntimeProcessesGiftWithoutDisplayPage(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "31567150"
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 33300, AttributeName: "加班时间", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	source := &fakeGiftSource{started: make(chan string, 1), events: make(chan giftEvent, 1)}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return source })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)

	select {
	case roomID := <-source.started:
		if roomID != "31567150" {
			t.Fatalf("connected room = %s", roomID)
		}
	case <-time.After(time.Second):
		t.Fatal("background runtime did not connect")
	}

	source.events <- giftEvent{GiftID: 33012, GiftName: "666", Num: 1, Price: 1000, CoinType: "gold", Timestamp: 1700000000, Rnd: "gift-1"}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.readState()
		if err == nil && len(updated.Attributes) == 1 && updated.Attributes[0].Value == 60 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("gift did not update the attribute in the disk configuration")
}

func TestBackgroundRuntimeProcessesTimerWithoutRoomOrDisplayPage(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 120, Unit: "seconds", Format: "hhmmss"}}
	state.TimerRules = []timerRule{{
		ID: "timer-1", AttributeName: "加班时间", FormulaName: "每分钟减少",
		IntervalSeconds: 60, Formula: "MAX(加班时间-60,0)", Enabled: true,
	}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	ticks := make(chan time.Time, 2)
	runtime := newBackgroundRuntime(store, nil)
	runtime.timerTicks = ticks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)

	startedAt := time.Unix(1700000000, 0)
	ticks <- startedAt
	ticks <- startedAt.Add(60 * time.Second)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.readState()
		if err == nil && len(updated.Attributes) == 1 && updated.Attributes[0].Value == 60 {
			if len(updated.Log) == 0 || updated.Log[0].Source != "timer" || updated.Log[0].RuleID != "timer-1" {
				t.Fatalf("timer log = %#v", updated.Log)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timer did not update the attribute while room and display were absent")
}

func TestTimerConditionSkipsOnlyTheCurrentOccurrence(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.TimerRules = []timerRule{{
		ID: "timer-1", AttributeName: "加班时间", FormulaName: "大于零时减少",
		IntervalSeconds: 60, Condition: "加班时间>0", Formula: "MAX(加班时间-60,0)", Enabled: true,
	}}

	if applied := applyTimerRules(&state, []string{"timer-1"}, time.Unix(1700000000, 0)); applied != 0 {
		t.Fatalf("condition-false timer applied %d times", applied)
	}
	if state.Attributes[0].Value != 0 || len(state.Log) != 0 {
		t.Fatalf("condition-false timer changed state: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}

	state.Attributes[0].Value = 120
	if applied := applyTimerRules(&state, []string{"timer-1"}, time.Unix(1700000060, 0)); applied != 1 {
		t.Fatalf("condition-true timer applied %d times", applied)
	}
	if state.Attributes[0].Value != 60 || len(state.Log) != 1 || state.Log[0].Source != "timer" {
		t.Fatalf("condition-true timer state: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}
}

func TestTimerScheduleWaitsForAFullFirstInterval(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	state := defaultAppState()
	state.TimerRules = []timerRule{{ID: "timer-1", IntervalSeconds: 60, Enabled: true}}
	startedAt := time.Unix(1700000000, 0)

	if due := runtime.dueTimerRuleIDs(state, startedAt); len(due) != 0 {
		t.Fatalf("new timer was due immediately: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(59*time.Second)); len(due) != 0 {
		t.Fatalf("new timer was due before one full interval: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(60*time.Second)); len(due) != 1 || due[0] != "timer-1" {
		t.Fatalf("timer due after one interval = %v", due)
	}
}
