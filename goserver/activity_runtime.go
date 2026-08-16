package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
)

func transitionActivity(state *appState, activityID, action string, now time.Time) (*activitySessionState, error) {
	transition, err := (gameplay.Engine{}).TransitionActivity(gameplaySnapshotForActivities(*state), activityID, action, now)
	if err != nil {
		return nil, err
	}
	applyGameplayTransition(state, transition)
	return state.findActivity(strings.TrimSpace(activityID)), nil
}

func resetActivityGiftTimeouts(state *appState, changedAttributeNames map[string]struct{}, now time.Time) int {
	snapshot := gameplaySnapshotForActivities(*state)
	changedAttributeIDs := make([]string, 0, len(changedAttributeNames))
	for _, attribute := range snapshot.Attributes {
		if _, exists := changedAttributeNames[attribute.Name]; exists {
			changedAttributeIDs = append(changedAttributeIDs, attribute.ID)
		}
	}
	transition, reset, err := gameplay.ResetActivityGiftTimeouts(snapshot, changedAttributeIDs, now)
	if err != nil {
		return 0
	}
	applyGameplayTransition(state, transition)
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
	transition, triggered, err := gameplay.EvaluateActivityMilestones(gameplaySnapshotForActivities(*state), now)
	if err != nil {
		return 0
	}
	applyGameplayTransition(state, transition)
	return triggered
}

func activityAllowsRulesForAttribute(state appState, attributeName string) bool {
	snapshot := gameplaySnapshotForActivities(state)
	for _, attribute := range snapshot.Attributes {
		if attribute.Name == attributeName {
			return gameplay.AllowsRulesForAttribute(snapshot, attribute.ID)
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
