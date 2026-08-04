package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func transitionActivity(state *appState, activityID, action string, now time.Time) (*activitySessionState, error) {
	activity := state.findActivity(strings.TrimSpace(activityID))
	if activity == nil {
		return nil, fmt.Errorf("找不到活动会话")
	}
	nowMillis := now.UnixMilli()
	switch strings.TrimSpace(action) {
	case "start":
		if activity.Status != "not_started" {
			return nil, fmt.Errorf("只有未开始的活动才能开始")
		}
		restoreActivityInitialValues(state, activity)
		activity.Status = "active"
		activity.StartedAt = nowMillis
		activity.LockedAt = 0
		activity.SettledAt = 0
		activity.Result = nil
		clearActivityMilestones(activity)
		clearActivityGiftTimeout(activity)
	case "lock":
		if activity.Status != "active" {
			return nil, fmt.Errorf("只有进行中的活动才能锁定")
		}
		activity.Status = "locked"
		activity.LockedAt = nowMillis
		clearActivityGiftTimeout(activity)
	case "settle":
		if activity.Status != "active" && activity.Status != "locked" {
			return nil, fmt.Errorf("只有进行中或已锁定的活动才能结算")
		}
		if activity.LockedAt == 0 {
			activity.LockedAt = nowMillis
		}
		activity.Status = "settled"
		activity.SettledAt = nowMillis
		activity.Result = settleActivity(state, activity)
		clearActivityGiftTimeout(activity)
	case "reset":
		restoreActivityInitialValues(state, activity)
		activity.Status = "not_started"
		activity.StartedAt = 0
		activity.LockedAt = 0
		activity.SettledAt = 0
		activity.Result = nil
		clearActivityMilestones(activity)
		clearActivityGiftTimeout(activity)
	default:
		return nil, fmt.Errorf("不支持的活动操作")
	}
	return activity, nil
}

func clearActivityMilestones(activity *activitySessionState) {
	for index := range activity.Milestones {
		activity.Milestones[index].TriggeredAt = 0
		activity.Milestones[index].TriggerValue = nil
	}
}

func clearActivityGiftTimeout(activity *activitySessionState) {
	if activity.GiftTimeout == nil {
		return
	}
	activity.GiftTimeout.LastGiftAt = 0
	activity.GiftTimeout.DeadlineAt = 0
}

func resetActivityGiftTimeouts(state *appState, changedAttributeNames map[string]struct{}, now time.Time) int {
	reset := 0
	for activityIndex := range state.Activities {
		activity := &state.Activities[activityIndex]
		if activity.Status != "active" || activity.GiftTimeout == nil || activity.GiftTimeout.Seconds < 1 {
			continue
		}
		matched := false
		for _, attributeName := range activity.AttributeNames {
			if _, exists := changedAttributeNames[attributeName]; exists {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		activity.GiftTimeout.LastGiftAt = now.UnixMilli()
		activity.GiftTimeout.DeadlineAt = now.Add(time.Duration(activity.GiftTimeout.Seconds) * time.Second).UnixMilli()
		reset++
	}
	return reset
}

func dueActivityGiftTimeoutIDs(state appState, now time.Time) []string {
	nowMillis := now.UnixMilli()
	ids := []string{}
	for _, activity := range state.Activities {
		if activity.Status == "active" && activity.GiftTimeout != nil && activity.GiftTimeout.DeadlineAt > 0 && activity.GiftTimeout.DeadlineAt <= nowMillis {
			ids = append(ids, activity.ID)
		}
	}
	return ids
}

func applyActivityGiftTimeouts(state *appState, activityIDs []string, now time.Time) int {
	due := make(map[string]struct{}, len(activityIDs))
	for _, activityID := range activityIDs {
		due[activityID] = struct{}{}
	}
	applied := 0
	for activityIndex := range state.Activities {
		activity := &state.Activities[activityIndex]
		if _, exists := due[activity.ID]; !exists || activity.Status != "active" || activity.GiftTimeout == nil {
			continue
		}
		if activity.GiftTimeout.DeadlineAt <= 0 || activity.GiftTimeout.DeadlineAt > now.UnixMilli() {
			continue
		}
		action := activity.GiftTimeout.Action
		if _, err := transitionActivity(state, activity.ID, action, now); err == nil {
			applied++
		}
	}
	return applied
}

func evaluateActivityMilestones(state *appState, now time.Time) int {
	triggered := 0
	for activityIndex := range state.Activities {
		activity := &state.Activities[activityIndex]
		if activity.Status != "active" {
			continue
		}
		for milestoneIndex := range activity.Milestones {
			if activity.Status != "active" {
				break
			}
			milestone := &activity.Milestones[milestoneIndex]
			if milestone.TriggeredAt > 0 {
				continue
			}
			attribute := state.findAttribute(milestone.AttributeName)
			if attribute == nil {
				continue
			}
			reached := milestone.Comparison == "lte" && attribute.Value <= milestone.Threshold ||
				milestone.Comparison == "gte" && attribute.Value >= milestone.Threshold
			if !reached {
				continue
			}
			value := attribute.Value
			milestone.TriggeredAt = now.UnixMilli()
			milestone.TriggerValue = &value
			triggered++
			switch milestone.Action {
			case "lock":
				activity.Status = "locked"
				activity.LockedAt = now.UnixMilli()
				clearActivityGiftTimeout(activity)
			case "settle":
				activity.Status = "settled"
				activity.LockedAt = now.UnixMilli()
				activity.SettledAt = now.UnixMilli()
				activity.Result = settleActivity(state, activity)
				clearActivityGiftTimeout(activity)
			}
		}
	}
	return triggered
}

func activityAllowsRulesForAttribute(state appState, attributeName string) bool {
	for _, activity := range state.Activities {
		if activity.GateRules && containsString(activity.AttributeNames, attributeName) {
			return activity.Status == "active"
		}
	}
	return true
}

func activityForScene(state appState, sceneID string) *activitySessionState {
	for index := range state.Activities {
		if state.Activities[index].SceneID == sceneID {
			return &state.Activities[index]
		}
	}
	return nil
}

func restoreActivityInitialValues(state *appState, activity *activitySessionState) {
	for _, attributeName := range activity.AttributeNames {
		attribute := state.findAttribute(attributeName)
		if attribute == nil {
			continue
		}
		if value, exists := activity.InitialValues[attributeName]; exists {
			attribute.Value = value
		}
	}
}

func settleActivity(state *appState, activity *activitySessionState) *activityResultState {
	result := &activityResultState{Values: map[string]float64{}}
	for _, attributeName := range activity.AttributeNames {
		if attribute := state.findAttribute(attributeName); attribute != nil {
			result.Values[attributeName] = attribute.Value
		}
	}
	if activity.ResultMode == "none" || len(result.Values) == 0 {
		return result
	}
	var winner string
	var winnerValue float64
	tied := false
	for _, attributeName := range activity.AttributeNames {
		value, exists := result.Values[attributeName]
		if !exists {
			continue
		}
		if winner == "" {
			winner = attributeName
			winnerValue = value
			tied = false
			continue
		}
		better := activity.ResultMode == "highest" && value > winnerValue || activity.ResultMode == "lowest" && value < winnerValue
		if better {
			winner = attributeName
			winnerValue = value
			tied = false
		} else if value == winnerValue {
			tied = true
		}
	}
	if !tied {
		result.WinnerAttributeName = winner
	}
	return result
}

func handleActivityTransition(store *configStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		var request struct {
			ActivityID string `json:"activityId"`
			Action     string `json:"action"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "请求格式不正确"})
			return
		}
		updated, err := store.updateState(func(state *appState) error {
			_, transitionErr := transitionActivity(state, request.ActivityID, request.Action, time.Now())
			return transitionErr
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		activity := updated.findActivity(strings.TrimSpace(request.ActivityID))
		attributeValues := map[string]float64{}
		if activity != nil {
			for _, attributeName := range activity.AttributeNames {
				if attribute := updated.findAttribute(attributeName); attribute != nil {
					attributeValues[attributeName] = attribute.Value
				}
			}
		}
		store.notifyTimerChanged()
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "activity": activity, "attributeValues": attributeValues})
	}
}
