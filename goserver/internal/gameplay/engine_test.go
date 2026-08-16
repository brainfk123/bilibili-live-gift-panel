package gameplay

import (
	"reflect"
	"testing"
	"time"
)

func TestEngineApplyGiftRollsRuleLimitsAndCountsOnlySuccessfulRules(t *testing.T) {
	limit := 1
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.Local)
	current := Snapshot{
		Attributes: []Attribute{{ID: "score", Name: "score", Value: 0}},
		Rules: []Rule{
			{ID: "invalid", GiftID: 1, AttributeID: "score", Formula: "score+"},
			{ID: "limited", GiftID: 1, AttributeID: "score", Formula: "score+1", DailyLimit: &limit},
		},
		RuleLimits: RuleLimitState{LocalDate: "2000-01-01", AppliedCounts: map[string]int{"limited": 99}},
	}

	transition, err := (Engine{}).ApplyGift(current, Gift{GiftID: 1, Count: 2}, now)
	if err != nil {
		t.Fatalf("ApplyGift() error = %v", err)
	}
	if transition.Next.Attributes[0].Value != 1 {
		t.Fatalf("value = %v, want 1", transition.Next.Attributes[0].Value)
	}
	wantLimits := RuleLimitState{LocalDate: "2026-08-16", AppliedCounts: map[string]int{"limited": 1}}
	if !reflect.DeepEqual(transition.Next.RuleLimits, wantLimits) {
		t.Fatalf("rule limits = %#v, want %#v", transition.Next.RuleLimits, wantLimits)
	}
	if current.Attributes[0].Value != 0 || current.RuleLimits.LocalDate != "2000-01-01" || current.RuleLimits.AppliedCounts["limited"] != 99 {
		t.Fatalf("ApplyGift() mutated input: %#v", current)
	}
}

func TestEngineTransitionsReturnDetachedSnapshots(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.Local)
	current := Snapshot{
		Attributes: []Attribute{{ID: "score", Name: "score", Value: 0}},
		Rules:      []Rule{{ID: "score", GiftID: 1, AttributeID: "score", Formula: "score+1"}},
		Activities: []Activity{{
			ID: "round", AttributeIDs: []string{"score"}, Status: "active", ResultMode: "none",
			InitialValues: map[string]float64{"score": 0},
		}},
		RuleLimits: RuleLimitState{LocalDate: "2026-08-16", AppliedCounts: map[string]int{}},
	}

	transition, err := (Engine{}).ApplyGift(current, Gift{GiftID: 1, Count: 1}, now)
	if err != nil {
		t.Fatalf("ApplyGift() error = %v", err)
	}
	transition.Next.Attributes[0].Value = 99
	transition.Next.Activities[0].InitialValues["score"] = 99
	transition.Next.RuleLimits.AppliedCounts["score"] = 99
	if current.Attributes[0].Value != 0 || current.Activities[0].InitialValues["score"] != 0 || len(current.RuleLimits.AppliedCounts) != 0 {
		t.Fatalf("transition aliases input: %#v", current)
	}
}
