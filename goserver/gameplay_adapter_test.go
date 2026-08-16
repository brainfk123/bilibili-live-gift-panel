package main

import (
	"reflect"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
)

func TestGameplayAdapterMatchesGiftTransition(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	enabled, disabled := true, false

	tests := []struct {
		name  string
		state appState
		gift  giftEvent
	}{
		{
			name: "minimum price and cap",
			state: func() appState {
				state := semanticState(giftRule{
					ID: "priced", GiftID: 1, AttributeName: "积分", FormulaName: "加分",
					Formula: "积分+5", Enabled: &enabled, MinPrice: float64Pointer(100), Cap: float64Pointer(10),
				}, 8)
				return state
			}(),
			gift: giftEvent{GiftID: 1, GiftName: "小花花", Price: 100, Num: 1, Timestamp: now.Unix(), Rnd: "priced"},
		},
		{
			name: "daily limit rolls from historical date",
			state: func() appState {
				state := semanticState(giftRule{
					ID: "limited", GiftID: 1, AttributeName: "积分", Formula: "积分+1", DailyLimit: intPointer(2),
				}, 0)
				state.Stats["2000-01-01"] = dayStats{
					Date: "2000-01-01", GiftTotals: map[string]int{"9": 4}, RuleTriggers: map[string]int{"limited": 99},
				}
				return state
			}(),
			gift: giftEvent{GiftID: 1, GiftName: "小花花", Num: 3, Timestamp: now.Unix(), Rnd: "rollover"},
		},
		{
			name: "disabled rule",
			state: semanticState(giftRule{
				ID: "disabled", GiftID: 1, AttributeName: "积分", Formula: "积分+100", Enabled: &disabled,
			}, 3),
			gift: giftEvent{GiftID: 1, GiftName: "小花花", Num: 1, Timestamp: now.Unix(), Rnd: "disabled"},
		},
		{
			name: "invalid formula is isolated and zero count means one",
			state: func() appState {
				state := defaultAppState()
				state.Attributes = []attributeState{{Name: "积分", Value: 1}, {Name: "耐力", Value: 2}}
				state.Rules = []giftRule{
					{ID: "invalid", GiftID: 1, AttributeName: "积分", Formula: "积分+"},
					{ID: "valid", GiftID: 1, AttributeName: "耐力", Formula: "耐力+3"},
				}
				return state
			}(),
			gift: giftEvent{GiftID: 1, GiftName: "小花花", Num: 0, Timestamp: now.Unix(), Rnd: "zero"},
		},
		{
			name: "blind box parent advances rule and target",
			state: func() appState {
				state := semanticState(giftRule{ID: "parent", GiftID: 100, AttributeName: "积分", Formula: "积分+1"}, 0)
				state.GiftKPIPanels = []giftKPIPanelState{{
					ID: "targets", Items: []giftKPIItemState{{GiftID: 100, Target: 10, Received: 2}},
				}}
				return state
			}(),
			gift: giftEvent{GiftID: 101, BlindGiftID: 100, GiftName: "盲盒礼物", Num: 2, Timestamp: now.Unix(), Rnd: "blind"},
		},
		{
			name: "activity gate blocks inactive rule",
			state: func() appState {
				state := semanticState(giftRule{ID: "gated", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}, 0)
				state.Activities = []activitySessionState{{
					ID: "round", AttributeNames: []string{"积分"}, Status: "not_started", ResultMode: "none", GateRules: true,
					InitialValues: map[string]float64{"积分": 0},
				}}
				return state
			}(),
			gift: giftEvent{GiftID: 1, GiftName: "小花花", Num: 1, Timestamp: now.Unix(), Rnd: "gated"},
		},
		{
			name: "effective gift advances milestone and timeout",
			state: func() appState {
				state := semanticState(giftRule{ID: "combo", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}, 0)
				state.Activities = []activitySessionState{{
					ID: "round", AttributeNames: []string{"积分"}, Status: "active", ResultMode: "none", GateRules: true,
					InitialValues: map[string]float64{"积分": 0},
					Milestones: []activityMilestoneState{{
						ID: "first", Name: "第一分", AttributeName: "积分", Comparison: "gte", Threshold: 1, Action: "announce",
					}},
					GiftTimeout: &activityGiftTimeoutState{Seconds: 5, Action: "lock"},
				}}
				return state
			}(),
			gift: giftEvent{GiftID: 1, GiftName: "小花花", Num: 1, Timestamp: now.Unix(), Rnd: "activity"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := mustCloneGameplayAdapterState(t, test.state)
			applyGiftEvent(&want, test.gift)

			got := mustCloneGameplayAdapterState(t, test.state)
			transition, err := (gameplay.Engine{}).ApplyGift(snapshotFromAppState(got), gameplayGift(test.gift), now)
			if err != nil {
				t.Fatalf("ApplyGift() error = %v", err)
			}
			assertGameplayAttributeEffectsEqual(t, transition.Effects, want.Log)
			applyGameplayTransition(&got, transition)
			assertGameplayFieldsEqual(t, got, want)
		})
	}
}

func TestGameplayAdapterMatchesTimerTransition(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tests := []struct {
		name        string
		status      string
		wantApplied int
	}{
		{name: "active rules apply and milestone locks", status: "active", wantApplied: 1},
		{name: "inactive gate blocks rules", status: "not_started", wantApplied: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := defaultAppState()
			state.Attributes = []attributeState{{Name: "积分", Value: 0}}
			state.TimerRules = []timerRule{
				{ID: "valid", AttributeName: "积分", FormulaName: "自动增加", Formula: "积分+2", Enabled: true},
				{ID: "invalid", AttributeName: "积分", Formula: "积分+", Enabled: true},
				{ID: "disabled", AttributeName: "积分", Formula: "积分+100", Enabled: false},
			}
			state.Activities = []activitySessionState{{
				ID: "round", AttributeNames: []string{"积分"}, Status: test.status, ResultMode: "none", GateRules: true,
				InitialValues: map[string]float64{"积分": 0},
				Milestones:    []activityMilestoneState{{ID: "two", AttributeName: "积分", Comparison: "gte", Threshold: 2, Action: "lock"}},
			}}

			want := mustCloneGameplayAdapterState(t, state)
			if applied := applyTimerRules(&want, []string{"valid", "invalid", "disabled"}, now); applied != test.wantApplied {
				t.Fatalf("legacy applied = %d, want %d", applied, test.wantApplied)
			}

			got := mustCloneGameplayAdapterState(t, state)
			transition, err := (gameplay.Engine{}).ApplyTimers(snapshotFromAppState(got), []string{"valid", "invalid", "disabled"}, now)
			if err != nil {
				t.Fatalf("ApplyTimers() error = %v", err)
			}
			assertGameplayAttributeEffectsEqual(t, transition.Effects, want.Log)
			applyGameplayTransition(&got, transition)
			assertGameplayFieldsEqual(t, got, want)
		})
	}
}

func TestGameplayAdapterTimerKeepsProcessingDateCountsWithHistoricalEventTime(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.TimerRules = []timerRule{{ID: "timer", AttributeName: "积分", Formula: "积分+1", Enabled: true}}
	today := time.Now().Format("2006-01-02")
	state.Stats[today] = dayStats{
		Date: today, GiftTotals: map[string]int{}, RuleTriggers: map[string]int{"timer": 3},
	}

	if applied := applyTimerRules(&state, []string{"timer"}, time.Unix(1_700_000_000, 0)); applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if got := state.Stats[today].RuleTriggers["timer"]; got != 4 {
		t.Fatalf("today rule count = %d, want 4", got)
	}
}

func TestGameplayAdaptersIgnoreUnrelatedBoundedCollections(t *testing.T) {
	t.Run("gift ignores timer volume", func(t *testing.T) {
		state := semanticState(giftRule{ID: "gift", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}, 0)
		for index := 0; index < 101; index++ {
			state.TimerRules = append(state.TimerRules, timerRule{ID: "unused-" + string(rune(index+1)), AttributeName: "积分"})
		}
		applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Rnd: "gift"})
		if got := state.Attributes[0].Value; got != 1 {
			t.Fatalf("gift value = %v, want 1", got)
		}
	})

	t.Run("timer ignores gift rule volume", func(t *testing.T) {
		state := defaultAppState()
		state.Attributes = []attributeState{{Name: "积分", Value: 0}}
		state.TimerRules = []timerRule{{ID: "timer", AttributeName: "积分", Formula: "积分+1", Enabled: true}}
		for index := 0; index < 501; index++ {
			state.Rules = append(state.Rules, giftRule{ID: "unused-" + string(rune(index+1)), GiftID: 99, AttributeName: "积分"})
		}
		if applied := applyTimerRules(&state, []string{"timer"}, time.Now()); applied != 1 {
			t.Fatalf("timer applied = %d, want 1", applied)
		}
	})

	t.Run("activity ignores target volume", func(t *testing.T) {
		state := gameplayActivityState("not_started", 9, 8)
		for index := 0; index < 101; index++ {
			state.GiftKPIPanels = append(state.GiftKPIPanels, giftKPIPanelState{ID: "unused-" + string(rune(index+1))})
		}
		if _, err := transitionActivity(&state, "round", "start", time.Now()); err != nil {
			t.Fatalf("transitionActivity() error = %v", err)
		}
	})

	t.Run("target ignores timer volume", func(t *testing.T) {
		state := defaultAppState()
		state.GiftKPIPanels = []giftKPIPanelState{{ID: "target", Items: []giftKPIItemState{{GiftID: 1, Target: 10}}}}
		for index := 0; index < 101; index++ {
			state.TimerRules = append(state.TimerRules, timerRule{ID: "unused-" + string(rune(index+1))})
		}
		applyGiftTargetEvent(&state, giftEvent{GiftID: 1, Num: 1})
		if got := state.GiftKPIPanels[0].Items[0].Received; got != 1 {
			t.Fatalf("target received = %d, want 1", got)
		}
	})
}

func TestGameplayAdapterMatchesActivityTransitions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name   string
		action string
		state  appState
	}{
		{name: "start", action: "start", state: gameplayActivityState("not_started", 9, 8)},
		{name: "lock", action: "lock", state: gameplayActivityState("active", 9, 8)},
		{name: "settle", action: "settle", state: gameplayActivityState("active", 9, 8)},
		{name: "reset", action: "reset", state: gameplayActivityState("settled", 9, 8)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := mustCloneGameplayAdapterState(t, test.state)
			if _, err := transitionActivity(&want, "round", test.action, now); err != nil {
				t.Fatalf("legacy transitionActivity() error = %v", err)
			}

			got := mustCloneGameplayAdapterState(t, test.state)
			transition, err := (gameplay.Engine{}).TransitionActivity(snapshotFromAppState(got), "round", test.action, now)
			if err != nil {
				t.Fatalf("TransitionActivity() error = %v", err)
			}
			applyGameplayTransition(&got, transition)
			assertGameplayFieldsEqual(t, got, want)
		})
	}
}

func TestGameplayAdapterMatchesActivityTimeout(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	state := gameplayActivityState("active", 9, 8)
	state.Activities[0].GiftTimeout = &activityGiftTimeoutState{
		Seconds: 5, Action: "lock", LastGiftAt: now.Add(-10 * time.Second).UnixMilli(), DeadlineAt: now.UnixMilli(),
	}

	want := mustCloneGameplayAdapterState(t, state)
	if applied := applyActivityGiftTimeouts(&want, []string{"round"}, now); applied != 1 {
		t.Fatalf("legacy applied timeouts = %d, want 1", applied)
	}

	got := mustCloneGameplayAdapterState(t, state)
	transition, err := (gameplay.Engine{}).TransitionActivity(snapshotFromAppState(got), "round", "lock", now)
	if err != nil {
		t.Fatalf("TransitionActivity() error = %v", err)
	}
	applyGameplayTransition(&got, transition)
	assertGameplayFieldsEqual(t, got, want)
}

func gameplayActivityState(status string, red, blue float64) appState {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "红队", Value: red}, {Name: "蓝队", Value: blue}}
	state.Activities = []activitySessionState{{
		ID: "round", AttributeNames: []string{"红队", "蓝队"}, Status: status, ResultMode: "highest", GateRules: true,
		InitialValues: map[string]float64{"红队": 0, "蓝队": 0},
		Milestones: []activityMilestoneState{{
			ID: "winner", AttributeName: "红队", Comparison: "gte", Threshold: 9, Action: "announce",
			TriggeredAt: 123, TriggerValue: float64Pointer(9),
		}},
		GiftTimeout: &activityGiftTimeoutState{Seconds: 5, Action: "lock", LastGiftAt: 100, DeadlineAt: 200},
	}}
	return state
}

func mustCloneGameplayAdapterState(t *testing.T, state appState) appState {
	t.Helper()
	clone, err := cloneAppState(state)
	if err != nil {
		t.Fatalf("cloneAppState() error = %v", err)
	}
	return clone
}

func assertGameplayFieldsEqual(t *testing.T, got, want appState) {
	t.Helper()
	if !reflect.DeepEqual(got.Attributes, want.Attributes) {
		t.Fatalf("attributes = %#v, want %#v", got.Attributes, want.Attributes)
	}
	if !reflect.DeepEqual(got.Activities, want.Activities) {
		t.Fatalf("activities = %#v, want %#v", got.Activities, want.Activities)
	}
	if !reflect.DeepEqual(got.GiftKPIPanels, want.GiftKPIPanels) {
		t.Fatalf("gift targets = %#v, want %#v", got.GiftKPIPanels, want.GiftKPIPanels)
	}
	if !reflect.DeepEqual(gameplayRuleCounts(got), gameplayRuleCounts(want)) {
		t.Fatalf("rule counts = %#v, want %#v", gameplayRuleCounts(got), gameplayRuleCounts(want))
	}
}

func gameplayRuleCounts(state appState) map[string]map[string]int {
	counts := make(map[string]map[string]int, len(state.Stats))
	for date, stats := range state.Stats {
		counts[date] = stats.RuleTriggers
	}
	return counts
}

func assertGameplayAttributeEffectsEqual(t *testing.T, got []gameplay.Effect, legacy []logEntry) {
	t.Helper()
	want := make([]gameplay.Effect, 0, len(legacy))
	for index := len(legacy) - 1; index >= 0; index-- {
		entry := legacy[index]
		want = append(want, gameplay.Effect{
			RuleID: entry.RuleID, AttributeName: entry.AttributeName, Delta: entry.Delta,
			ValueAfter: entry.ValueAfter, TriggerName: entry.TriggerName,
		})
	}
	attributeEffects := make([]gameplay.Effect, 0, len(got))
	for _, effect := range got {
		if effect.AttributeName == "" {
			continue
		}
		effect.Target = nil
		effect.Activity = nil
		attributeEffects = append(attributeEffects, effect)
	}
	if !reflect.DeepEqual(attributeEffects, want) {
		t.Fatalf("attribute effects = %#v, want %#v", attributeEffects, want)
	}
}
