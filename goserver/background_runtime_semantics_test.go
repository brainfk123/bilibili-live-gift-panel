package main

import (
	"fmt"
	"strings"
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

type fakeAttributeFreezeChecker map[string]bool

func (checker fakeAttributeFreezeChecker) IsFrozen(attributeID string) bool {
	return checker[attributeID]
}

func TestApplyGiftEventSkipsOnlyFrozenAttribute(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{
		{ID: "attribute-a", Name: "A", Value: 0},
		{ID: "attribute-b", Name: "B", Value: 0},
	}
	state.Rules = []giftRule{
		{ID: "gift-a", GiftID: 1, AttributeName: "A", Formula: "A+1"},
		{ID: "gift-b", GiftID: 1, AttributeName: "B", Formula: "B+1"},
	}

	applyGiftEventWithFreeze(&state, giftEvent{
		GiftID: 1, GiftName: "test gift", Num: 1, Price: 100, CoinType: "gold",
		UID: 1, Uname: "viewer", Timestamp: 1700000000, Rnd: "frozen-gift",
	}, fakeAttributeFreezeChecker{"attribute-a": true})

	if got := state.findAttribute("A").Value; got != 0 {
		t.Fatalf("frozen A = %v", got)
	}
	if got := state.findAttribute("B").Value; got != 1 {
		t.Fatalf("live B = %v", got)
	}
	if len(state.GiftReceipts) != 1 {
		t.Fatalf("receipts = %d", len(state.GiftReceipts))
	}
	if state.todayStats().GiftTotals[giftKey(1)] != 1 {
		t.Fatal("gift total was dropped")
	}
	if len(state.Contributions.Viewers) != 1 {
		t.Fatal("contribution was dropped")
	}
	if effects := state.GiftReceipts[0].Effects; len(effects) != 1 || effects[0].AttributeName != "B" {
		t.Fatalf("receipt effects = %#v", effects)
	}
	if got := state.todayStats().RuleTriggers["gift-a"]; got != 0 {
		t.Fatalf("frozen rule triggers = %d", got)
	}
	if got := state.todayStats().RuleTriggers["gift-b"]; got != 1 {
		t.Fatalf("live rule triggers = %d", got)
	}
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

func TestApplyGiftEventUsesNamedUserIdentityCondition(t *testing.T) {
	state := semanticState(giftRule{
		ID: "guard", GiftID: 1, AttributeName: "积分",
		Condition: "用户身份>=舰长", Formula: "积分+1",
	}, 0)
	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Membership: "fan", Rnd: "fan"})
	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Membership: "captain", Rnd: "captain"})
	if got := state.Attributes[0].Value; got != 1 {
		t.Fatalf("value = %v, want 1", got)
	}
}

func TestApplyGiftEventSupportsIdentityConditionComparisons(t *testing.T) {
	tests := []struct {
		name       string
		condition  string
		membership string
		want       float64
	}{
		{name: "exact fan", condition: "用户身份=粉丝团", membership: "fan", want: 1},
		{name: "governor satisfies captain", condition: "用户身份>=舰长", membership: "governor", want: 1},
		{name: "unknown is ordinary", condition: "用户身份>=粉丝团", membership: "unknown", want: 0},
		{name: "identity in formula", condition: "1", membership: "admiral", want: 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			formula := "积分+1"
			if test.name == "identity in formula" {
				formula = "IF(用户身份>=提督,积分+10,积分+1)"
			}
			state := semanticState(giftRule{ID: "identity", GiftID: 1, AttributeName: "积分", Condition: test.condition, Formula: formula}, 0)
			applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Membership: test.membership, Rnd: test.name})
			if got := state.Attributes[0].Value; got != test.want {
				t.Fatalf("value = %v, want %v", got, test.want)
			}
		})
	}
}

func TestApplyGiftEventConditionCombinesIdentityPriceAndAttributes(t *testing.T) {
	state := semanticState(giftRule{
		ID: "combined", GiftID: 1, AttributeName: "积分",
		Condition: "(用户身份>=舰长)*(price>=1000)*(积分<2)", Formula: "积分+1",
	}, 0)
	applyGiftEvent(&state, giftEvent{GiftID: 1, Price: 1000, Membership: "fan", Rnd: "fan"})
	applyGiftEvent(&state, giftEvent{GiftID: 1, Price: 999, Membership: "captain", Rnd: "cheap"})
	applyGiftEvent(&state, giftEvent{GiftID: 1, Price: 1000, Membership: "captain", Rnd: "captain"})
	if got := state.Attributes[0].Value; got != 1 {
		t.Fatalf("value = %v, want 1", got)
	}
}

func TestApplyGiftEventReevaluatesConditionForEachOccurrence(t *testing.T) {
	state := semanticState(giftRule{
		ID: "repeated", GiftID: 1, AttributeName: "积分",
		Condition: "积分<2", Formula: "积分+1",
	}, 0)
	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 4, Rnd: "batch"})
	today := time.Now().Format("2006-01-02")
	if got := state.Attributes[0].Value; got != 2 {
		t.Fatalf("value = %v, want 2", got)
	}
	if got := state.Stats[today].RuleTriggers["repeated"]; got != 2 {
		t.Fatalf("rule triggers = %d, want 2", got)
	}
	if len(state.Log) != 1 || state.Log[0].Delta != 2 {
		t.Fatalf("logs = %#v, want one aggregated change of 2", state.Log)
	}
	if len(state.GiftReceipts) != 1 || len(state.GiftReceipts[0].Effects) != 1 || state.GiftReceipts[0].Effects[0].Delta != 2 {
		t.Fatalf("receipts = %#v, want one effect of 2", state.GiftReceipts)
	}
	if len(state.Contributions.Viewers) != 1 || state.Contributions.Viewers[0].RuleTriggers != 2 {
		t.Fatalf("contributions = %#v, want two applied triggers", state.Contributions.Viewers)
	}
}

func TestApplyGiftEventSkipsInvalidConditionAndContinues(t *testing.T) {
	huge := strings.Repeat("9", 200)
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}, {Name: "耐力", Value: 0}}
	state.Rules = []giftRule{
		{ID: "invalid", GiftID: 1, AttributeName: "积分", Condition: "积分+", Formula: "积分+1"},
		{ID: "false", GiftID: 1, AttributeName: "积分", Condition: "0", Formula: "积分+1"},
		{ID: "non-finite", GiftID: 1, AttributeName: "积分", Condition: huge + "*" + huge, Formula: "积分+1"},
		{ID: "valid", GiftID: 1, AttributeName: "耐力", Condition: "1", Formula: "耐力+1"},
	}
	applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Rnd: "mixed"})
	today := time.Now().Format("2006-01-02")
	if got := state.Attributes[0].Value; got != 0 {
		t.Fatalf("skipped attribute = %v, want 0", got)
	}
	if got := state.Attributes[1].Value; got != 1 {
		t.Fatalf("valid attribute = %v, want 1", got)
	}
	if got := state.Stats[today].RuleTriggers["invalid"] + state.Stats[today].RuleTriggers["false"] + state.Stats[today].RuleTriggers["non-finite"]; got != 0 {
		t.Fatalf("skipped rule triggers = %d, want 0", got)
	}
	if len(state.Log) != 1 || state.Log[0].RuleID != "valid" {
		t.Fatalf("logs = %#v, want only valid rule log", state.Log)
	}
}

func TestApplyGiftEventCombinesIdentityConditionAndRandomChoice(t *testing.T) {
	original := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = original })
	draws := 0
	formulaRandomIntn = func(limit int) int {
		draws++
		if limit != 2 {
			t.Fatalf("random choice limit = %d, want 2", limit)
		}
		return 1
	}
	state := semanticState(giftRule{
		ID: "random", GiftID: 1, AttributeName: "积分",
		Condition: "用户身份>=舰长", Formula: "RANDOMCHOICE(积分+1,积分+10)",
	}, 0)
	applyGiftEvent(&state, giftEvent{GiftID: 1, Membership: "fan", Rnd: "fan"})
	applyGiftEvent(&state, giftEvent{GiftID: 1, Membership: "captain", Rnd: "captain"})
	today := time.Now().Format("2006-01-02")
	if got := state.Attributes[0].Value; got != 10 {
		t.Fatalf("value = %v, want 10", got)
	}
	if draws != 1 {
		t.Fatalf("random draws = %d, want 1", draws)
	}
	if got := state.Stats[today].RuleTriggers["random"]; got != 1 {
		t.Fatalf("rule triggers = %d, want 1", got)
	}
	if len(state.Log) != 1 || len(state.GiftReceipts) != 2 || len(state.GiftReceipts[0].Effects) != 1 {
		t.Fatalf("logs/receipts = %#v / %#v, want one effect", state.Log, state.GiftReceipts)
	}
	if len(state.Contributions.Viewers) != 1 || state.Contributions.Viewers[0].RuleTriggers != 1 {
		t.Fatalf("contributions = %#v, want one applied trigger", state.Contributions.Viewers)
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

func TestApplyGiftEventKeepsDailyBucketsOnProcessingDate(t *testing.T) {
	processingNow := time.Date(2000, time.January, 2, 12, 34, 56, 0, time.Local)
	processingDate := "2000-01-02"
	otherDate := "1999-12-31"
	state := semanticState(giftRule{
		ID: "limited", GiftID: 1, AttributeName: "积分",
		Formula: "积分+1", DailyLimit: intPointer(2),
	}, 0)
	state.Stats = map[string]dayStats{
		processingDate: {
			Date: processingDate, GiftTotals: map[string]int{"1": 4}, RuleTriggers: map[string]int{"limited": 1},
		},
		otherDate: {
			Date: otherDate, GiftTotals: map[string]int{"9": 7}, RuleTriggers: map[string]int{"historic": 3},
		},
	}

	applyGiftEventWithFreezeAt(&state, giftEvent{GiftID: 1, Num: 3, Rnd: "historical-processing-date"}, nil, processingNow)

	if got := state.Attributes[0].Value; got != 1 {
		t.Fatalf("value = %v, want 1 remaining daily-limit application", got)
	}
	if got := state.Stats[processingDate].GiftTotals["1"]; got != 7 {
		t.Fatalf("processing-date gift total = %d, want 7", got)
	}
	if got := state.Stats[processingDate].RuleTriggers["limited"]; got != 2 {
		t.Fatalf("processing-date rule triggers = %d, want 2", got)
	}
	if got := state.Stats[otherDate].GiftTotals["9"]; got != 7 {
		t.Fatalf("other-date gift total = %d, want 7", got)
	}
	if got := state.Stats[otherDate].RuleTriggers["historic"]; got != 3 {
		t.Fatalf("other-date rule triggers = %d, want 3", got)
	}
	if got := len(state.Stats); got != 2 {
		t.Fatalf("daily bucket count = %d, want 2", got)
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
