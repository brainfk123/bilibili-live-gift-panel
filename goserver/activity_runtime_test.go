package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestActivityLifecycleRestoresLocksAndSettles(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{
		{Name: "红队", Value: 99, Unit: "none", Format: "number"},
		{Name: "蓝队", Value: 88, Unit: "none", Format: "number"},
	}
	state.Activities = []activitySessionState{{
		ID: "activity-match", Name: "阵营对抗", AttributeNames: []string{"红队", "蓝队"},
		Status: "not_started", ResultMode: "highest", GateRules: true,
		InitialValues: map[string]float64{"红队": 0, "蓝队": 0},
	}}
	now := time.Unix(1_700_000_000, 0)
	activity, err := transitionActivity(&state, "activity-match", "start", now)
	if err != nil {
		t.Fatal(err)
	}
	if activity.Status != "active" || state.Attributes[0].Value != 0 || state.Attributes[1].Value != 0 {
		t.Fatalf("start did not activate and restore values: %#v, %#v", activity, state.Attributes)
	}
	state.Attributes[0].Value = 12
	state.Attributes[1].Value = 18
	if _, err := transitionActivity(&state, "activity-match", "lock", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	activity, err = transitionActivity(&state, "activity-match", "settle", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if activity.Status != "settled" || activity.Result == nil || activity.Result.WinnerAttributeName != "蓝队" {
		t.Fatalf("unexpected settlement: %#v", activity)
	}
	if activity.Result.Values["红队"] != 12 || activity.Result.Values["蓝队"] != 18 {
		t.Fatalf("settlement snapshot = %#v", activity.Result.Values)
	}
	if _, err := transitionActivity(&state, "activity-match", "reset", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if activity.Status != "not_started" || activity.Result != nil || state.Attributes[0].Value != 0 || state.Attributes[1].Value != 0 {
		t.Fatalf("reset did not clear the session: %#v, %#v", activity, state.Attributes)
	}
}

func TestActivitySettlementLeavesTieWithoutWinner(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "A", Value: 5}, {Name: "B", Value: 5}}
	state.Activities = []activitySessionState{{
		ID: "tie", Name: "平局", AttributeNames: []string{"A", "B"}, Status: "active", ResultMode: "highest",
		InitialValues: map[string]float64{"A": 0, "B": 0},
	}}
	activity, err := transitionActivity(&state, "tie", "settle", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if activity.Result == nil || activity.Result.WinnerAttributeName != "" {
		t.Fatalf("tie should not select a winner: %#v", activity.Result)
	}
}

func TestInactiveActivityGatesGiftAndTimerRules(t *testing.T) {
	enabled := true
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0, Unit: "none", Format: "number"}}
	state.Rules = []giftRule{{ID: "gift-rule", GiftID: 1, AttributeName: "积分", Formula: "积分+1", Enabled: &enabled}}
	state.TimerRules = []timerRule{{ID: "timer-rule", AttributeName: "积分", FormulaName: "自动增加", IntervalSeconds: 1, Formula: "积分+1", Enabled: true}}
	state.Activities = []activitySessionState{{
		ID: "gated", Name: "限时积分", AttributeNames: []string{"积分"}, Status: "not_started", ResultMode: "none", GateRules: true,
		InitialValues: map[string]float64{"积分": 0},
	}}
	applyGiftEvent(&state, giftEvent{GiftID: 1, GiftName: "测试礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "gift-1"})
	if state.Attributes[0].Value != 0 || len(state.Log) != 0 {
		t.Fatalf("inactive activity allowed gift rule: value=%v log=%d", state.Attributes[0].Value, len(state.Log))
	}
	if applied := applyTimerRules(&state, []string{"timer-rule"}, time.Now()); applied != 0 {
		t.Fatalf("inactive activity allowed timer rule: %d", applied)
	}
	state.Activities[0].Status = "active"
	applyGiftEvent(&state, giftEvent{GiftID: 1, GiftName: "测试礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "gift-2"})
	if state.Attributes[0].Value != 1 {
		t.Fatalf("active activity did not allow gift rule: %v", state.Attributes[0].Value)
	}
	if applied := applyTimerRules(&state, []string{"timer-rule"}, time.Now()); applied != 1 || state.Attributes[0].Value != 2 {
		t.Fatalf("active activity did not allow timer rule: applied=%d value=%v", applied, state.Attributes[0].Value)
	}
}

func TestActivityTransitionHandlerReturnsBackendValues(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 9, Unit: "none", Format: "number"}}
	state.Activities = []activitySessionState{{
		ID: "activity-1", Name: "积分赛", AttributeNames: []string{"积分"}, Status: "not_started", ResultMode: "none",
		InitialValues: map[string]float64{"积分": 0},
	}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/activities/transition", strings.NewReader(`{"activityId":"activity-1","action":"start"}`))
	handleActivityTransition(store)(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"status":"active"`) || !strings.Contains(response.Body.String(), `"积分":0`) {
		t.Fatalf("response does not include backend state: %s", response.Body.String())
	}
}

func TestActivityMilestoneTriggersOnceAndResetClearsIt(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 10, Unit: "none", Format: "number"}}
	state.Activities = []activitySessionState{{
		ID: "milestones", Name: "目标挑战", AttributeNames: []string{"积分"}, Status: "active", ResultMode: "none",
		InitialValues: map[string]float64{"积分": 0},
		Milestones: []activityMilestoneState{{
			ID: "target", Name: "达到十分", AttributeName: "积分", Comparison: "gte", Threshold: 10,
			Action: "announce", Message: "目标达成！",
		}},
	}}
	now := time.Unix(1_700_000_100, 0)
	if triggered := evaluateActivityMilestones(&state, now); triggered != 1 {
		t.Fatalf("triggered = %d, want 1", triggered)
	}
	milestone := &state.Activities[0].Milestones[0]
	if milestone.TriggeredAt != now.UnixMilli() || milestone.TriggerValue == nil || *milestone.TriggerValue != 10 {
		t.Fatalf("unexpected milestone state: %#v", milestone)
	}
	if triggered := evaluateActivityMilestones(&state, now.Add(time.Second)); triggered != 0 {
		t.Fatalf("milestone triggered twice: %d", triggered)
	}
	if _, err := transitionActivity(&state, "milestones", "reset", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if milestone.TriggeredAt != 0 || milestone.TriggerValue != nil {
		t.Fatalf("reset did not clear milestone: %#v", milestone)
	}
}

func TestActivityMilestoneCanAutomaticallySettle(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "红队", Value: 12}, {Name: "蓝队", Value: 8}}
	state.Activities = []activitySessionState{{
		ID: "auto-settle", Name: "抢先赛", AttributeNames: []string{"红队", "蓝队"}, Status: "active", ResultMode: "highest",
		InitialValues: map[string]float64{"红队": 0, "蓝队": 0},
		Milestones: []activityMilestoneState{{
			ID: "first-to-ten", Name: "先到十分", AttributeName: "红队", Comparison: "gte", Threshold: 10, Action: "settle",
		}},
	}}
	if triggered := evaluateActivityMilestones(&state, time.Now()); triggered != 1 {
		t.Fatalf("triggered = %d, want 1", triggered)
	}
	activity := state.Activities[0]
	if activity.Status != "settled" || activity.Result == nil || activity.Result.WinnerAttributeName != "红队" {
		t.Fatalf("milestone did not settle activity: %#v", activity)
	}
}

func TestGiftAndTimerRulesEvaluateActivityMilestones(t *testing.T) {
	enabled := true
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0, Unit: "none", Format: "number"}}
	state.Rules = []giftRule{{ID: "gift", GiftID: 1, AttributeName: "积分", Formula: "积分+1", Enabled: &enabled}}
	state.TimerRules = []timerRule{{ID: "timer", AttributeName: "积分", FormulaName: "自动增加", IntervalSeconds: 1, Formula: "积分+1", Enabled: true}}
	state.Activities = []activitySessionState{{
		ID: "challenge", Name: "积分挑战", AttributeNames: []string{"积分"}, Status: "active", ResultMode: "none",
		InitialValues: map[string]float64{"积分": 0},
		Milestones: []activityMilestoneState{{
			ID: "gift-target", Name: "礼物目标", AttributeName: "积分", Comparison: "gte", Threshold: 1, Action: "announce",
		}, {
			ID: "timer-target", Name: "定时目标", AttributeName: "积分", Comparison: "gte", Threshold: 2, Action: "lock",
		}},
	}}
	applyGiftEvent(&state, giftEvent{GiftID: 1, GiftName: "测试礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "milestone-gift"})
	if state.Activities[0].Milestones[0].TriggeredAt == 0 || state.Activities[0].Status != "active" {
		t.Fatalf("gift did not trigger announce milestone: %#v", state.Activities[0])
	}
	if applied := applyTimerRules(&state, []string{"timer"}, time.Now()); applied != 1 {
		t.Fatalf("timer applied = %d, want 1", applied)
	}
	if state.Activities[0].Milestones[1].TriggeredAt == 0 || state.Activities[0].Status != "locked" {
		t.Fatalf("timer did not trigger lock milestone: %#v", state.Activities[0])
	}
}

func TestActivityGiftTimeoutStartsOnEffectiveGiftAndLocksWhenDue(t *testing.T) {
	enabled := true
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "连击", Value: 0, Unit: "none", Format: "number"}}
	state.Rules = []giftRule{{ID: "combo", GiftID: 1, AttributeName: "连击", Formula: "连击+1", Enabled: &enabled}}
	state.Activities = []activitySessionState{{
		ID: "combo-session", Name: "连击挑战", AttributeNames: []string{"连击"}, Status: "active", ResultMode: "none",
		InitialValues: map[string]float64{"连击": 0}, GiftTimeout: &activityGiftTimeoutState{Seconds: 5, Action: "lock"},
	}}
	now := time.Unix(1_700_000_200, 0)
	applyGiftEvent(&state, giftEvent{GiftID: 1, GiftName: "测试礼物", Num: 1, Timestamp: now.Unix(), Rnd: "combo-1"})
	timeout := state.Activities[0].GiftTimeout
	if timeout.LastGiftAt != now.UnixMilli() || timeout.DeadlineAt != now.Add(5*time.Second).UnixMilli() {
		t.Fatalf("unexpected timeout schedule: %#v", timeout)
	}
	if ids := dueActivityGiftTimeoutIDs(state, now.Add(4*time.Second)); len(ids) != 0 {
		t.Fatalf("timeout became due too early: %#v", ids)
	}
	due := dueActivityGiftTimeoutIDs(state, now.Add(5*time.Second))
	if applied := applyActivityGiftTimeouts(&state, due, now.Add(5*time.Second)); applied != 1 {
		t.Fatalf("applied timeouts = %d, want 1", applied)
	}
	if state.Activities[0].Status != "locked" || timeout.DeadlineAt != 0 {
		t.Fatalf("timeout did not lock activity: %#v", state.Activities[0])
	}
}

func TestUnmatchedGiftDoesNotStartActivityGiftTimeout(t *testing.T) {
	enabled := true
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "连击", Value: 0}}
	state.Rules = []giftRule{{ID: "combo", GiftID: 1, AttributeName: "连击", Formula: "连击+1", Enabled: &enabled}}
	state.Activities = []activitySessionState{{
		ID: "combo-session", Name: "连击挑战", AttributeNames: []string{"连击"}, Status: "active", ResultMode: "none",
		InitialValues: map[string]float64{"连击": 0}, GiftTimeout: &activityGiftTimeoutState{Seconds: 5, Action: "reset"},
	}}
	applyGiftEvent(&state, giftEvent{GiftID: 2, GiftName: "其他礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "other"})
	if state.Activities[0].GiftTimeout.DeadlineAt != 0 {
		t.Fatalf("unmatched gift started timeout: %#v", state.Activities[0].GiftTimeout)
	}
}
