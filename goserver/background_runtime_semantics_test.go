package main

import (
	"fmt"
	"testing"
	"time"
)

func float64Pointer(value float64) *float64 { return &value }
func intPointer(value int) *int             { return &value }

const (
	expectedLogEntryLimit = 200
	generatedLogEntries   = expectedLogEntryLimit + 5
)

func semanticState(rule giftRule, initial float64) appState {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: initial}}
	state.Rules = []giftRule{rule}
	return state
}

func TestApplyGiftEventHonorsMinimumPrice(t *testing.T) {
	state := semanticState(giftRule{
		ID: "priced", GiftID: 1, AttributeName: "积分",
		Formula: "积分+1", MinPrice: float64Pointer(100),
	}, 0)
	applyGiftEvent(&state, giftEvent{GiftID: 1, Price: 99, Num: 1, Rnd: "low"})
	applyGiftEvent(&state, giftEvent{GiftID: 1, Price: 100, Num: 1, Rnd: "equal"})
	if got := state.Attributes[0].Value; got != 1 {
		t.Fatalf("value = %v, want 1", got)
	}
}

func TestApplyGiftEventRepeatsQuantityWithoutExceedingDailyLimit(t *testing.T) {
	state := semanticState(giftRule{
		ID: "limited", GiftID: 1, AttributeName: "积分",
		Formula: "积分+1", DailyLimit: intPointer(2),
	}, 0)
	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 3, Rnd: "batch"})
	today := time.Now().Format("2006-01-02")
	if got := state.Attributes[0].Value; got != 2 {
		t.Fatalf("value = %v, want 2", got)
	}
	if got := state.Stats[today].GiftTotals["1"]; got != 3 {
		t.Fatalf("gift total = %d, want 3", got)
	}
	if got := state.Stats[today].RuleTriggers["limited"]; got != 2 {
		t.Fatalf("rule triggers = %d, want 2", got)
	}
}

func TestApplyGiftEventCapsGrowthButAllowsDecrease(t *testing.T) {
	state := semanticState(giftRule{
		ID: "capped", GiftID: 1, AttributeName: "积分",
		Formula: "积分+5", Cap: float64Pointer(10),
	}, 9)
	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Rnd: "growth"})
	if got := state.Attributes[0].Value; got != 10 {
		t.Fatalf("capped growth = %v, want 10", got)
	}
	state.Rules[0].Formula = "积分-3"
	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Rnd: "decrease"})
	if got := state.Attributes[0].Value; got != 7 {
		t.Fatalf("capped decrease = %v, want 7", got)
	}
}

func TestApplyGiftEventSkipsInvalidFormulaAndContinues(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 1}, {Name: "耐力", Value: 2}}
	state.Rules = []giftRule{
		{ID: "invalid", GiftID: 1, AttributeName: "积分", Formula: "积分+"},
		{ID: "valid", GiftID: 1, AttributeName: "耐力", Formula: "耐力+3"},
	}

	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Rnd: "mixed"})

	if got := state.Attributes[0].Value; got != 1 {
		t.Fatalf("invalid-rule attribute = %v, want 1", got)
	}
	if got := state.Attributes[1].Value; got != 5 {
		t.Fatalf("valid-rule attribute = %v, want 5", got)
	}
	if len(state.Log) != 1 || state.Log[0].RuleID != "valid" {
		t.Fatalf("logs = %#v, want only valid rule log", state.Log)
	}
}

func TestApplyGiftEventKeepsNewestTwoHundredGiftLogs(t *testing.T) {
	state := semanticState(giftRule{ID: "gift-log", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}, 0)
	for timestamp := int64(1); timestamp <= generatedLogEntries; timestamp++ {
		applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Rnd: fmt.Sprintf("gift-%d", timestamp), Timestamp: timestamp})
	}
	if got := len(state.Log); got != expectedLogEntryLimit {
		t.Fatalf("log length = %d, want %d", got, expectedLogEntryLimit)
	}
	if got := state.Log[0].Time; got != int64(generatedLogEntries) {
		t.Fatalf("newest log time = %d, want %d", got, generatedLogEntries)
	}
	if got := state.Log[expectedLogEntryLimit-1].Time; got != 6 {
		t.Fatalf("oldest retained log time = %d, want 6", got)
	}
}

func TestApplyTimerRulesKeepsNewestTwoHundredTimerLogs(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	for timestamp := 1; timestamp <= generatedLogEntries; timestamp++ {
		state.TimerRules = append(state.TimerRules, timerRule{
			ID: fmt.Sprintf("timer-%d", timestamp), AttributeName: "积分", Formula: "积分+1", Enabled: true,
		})
	}
	for timestamp := 1; timestamp <= generatedLogEntries; timestamp++ {
		ruleID := fmt.Sprintf("timer-%d", timestamp)
		applyTimerRules(&state, []string{ruleID}, time.Unix(int64(timestamp), 0))
	}
	if got := len(state.Log); got != expectedLogEntryLimit {
		t.Fatalf("log length = %d, want %d", got, expectedLogEntryLimit)
	}
	if got := state.Log[0].Time; got != int64(generatedLogEntries) {
		t.Fatalf("newest log time = %d, want %d", got, generatedLogEntries)
	}
	if got := state.Log[expectedLogEntryLimit-1].Time; got != 6 {
		t.Fatalf("oldest retained log time = %d, want 6", got)
	}
}

func TestApplyGiftEventCreatesTodayBucketWithoutMutatingHistoricalBucket(t *testing.T) {
	state := semanticState(giftRule{ID: "today", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}, 0)
	state.Stats["2000-01-01"] = dayStats{
		Date: "2000-01-01", GiftTotals: map[string]int{"9": 4}, RuleTriggers: map[string]int{"historic": 2},
	}

	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Rnd: "today"})

	historical := state.Stats["2000-01-01"]
	if got := historical.GiftTotals["9"]; got != 4 {
		t.Fatalf("historical gift total = %d, want 4", got)
	}
	if got := historical.RuleTriggers["historic"]; got != 2 {
		t.Fatalf("historical rule triggers = %d, want 2", got)
	}
	today := time.Now().Format("2006-01-02")
	if _, exists := state.Stats[today]; !exists {
		t.Fatal("today stats bucket was not created")
	}
}
