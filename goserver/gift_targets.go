package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"bilibili-live-gift-panel/internal/gameplay"
)

// Gift target definitions are configuration, while received counts are
// backend-owned runtime state. The public app state combines both as a read
// model, but all writes cross the functions in this file.
type giftTargetProgressState map[string]map[string]int

type giftTargetItemProgress struct {
	GiftID   int `json:"giftId"`
	Received int `json:"received"`
}

type giftTargetPanelProgress struct {
	PanelID string                   `json:"panelId"`
	Items   []giftTargetItemProgress `json:"items"`
}

var errGiftTargetPanelNotFound = errors.New("找不到礼物目标面板")

func giftTargetProgressFromPanels(panels []giftKPIPanelState) giftTargetProgressState {
	progress := make(giftTargetProgressState, len(panels))
	for _, panel := range panels {
		items := make(map[string]int, len(panel.Items))
		for _, item := range panel.Items {
			if item.Received > 0 {
				items[strconv.Itoa(item.GiftID)] = item.Received
			}
		}
		if len(items) > 0 {
			progress[panel.ID] = items
		}
	}
	return progress
}

func applyGiftTargetProgress(panels []giftKPIPanelState, progress giftTargetProgressState) {
	for panelIndex := range panels {
		items := progress[panels[panelIndex].ID]
		for itemIndex := range panels[panelIndex].Items {
			item := &panels[panelIndex].Items[itemIndex]
			item.Received = maxInt(0, items[strconv.Itoa(item.GiftID)])
		}
	}
}

func giftTargetConfigPanels(panels []giftKPIPanelState) []giftKPIPanelState {
	configured := make([]giftKPIPanelState, len(panels))
	for panelIndex, panel := range panels {
		configured[panelIndex] = panel
		configured[panelIndex].Items = make([]giftKPIItemState, len(panel.Items))
		copy(configured[panelIndex].Items, panel.Items)
		for itemIndex := range configured[panelIndex].Items {
			configured[panelIndex].Items[itemIndex].Received = 0
		}
	}
	return configured
}

func preserveGiftTargetProgress(previous []giftKPIPanelState, next []giftKPIPanelState) {
	applyGiftTargetProgress(next, giftTargetProgressFromPanels(previous))
}

func applyGiftTargetEvent(state *appState, gift giftEvent) {
	transition, err := gameplay.ApplyGiftTargets(gameplaySnapshotForTargets(*state), gameplayGift(gift))
	if err != nil {
		return
	}
	applyGameplayTransition(state, transition)
}

func resetGiftTargetPanelProgress(state *appState, panelID string) (giftTargetPanelProgress, error) {
	panelID = strings.TrimSpace(panelID)
	for panelIndex := range state.GiftKPIPanels {
		panel := &state.GiftKPIPanels[panelIndex]
		if panel.ID != panelID {
			continue
		}
		progress := giftTargetPanelProgress{PanelID: panel.ID, Items: make([]giftTargetItemProgress, len(panel.Items))}
		for itemIndex := range panel.Items {
			panel.Items[itemIndex].Received = 0
			progress.Items[itemIndex] = giftTargetItemProgress{GiftID: panel.Items[itemIndex].GiftID}
		}
		return progress, nil
	}
	return giftTargetPanelProgress{}, errGiftTargetPanelNotFound
}

func handleGiftTargetProgress(store *configStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", http.MethodDelete)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		panelID := strings.TrimSpace(r.URL.Query().Get("panelId"))
		if panelID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "缺少礼物目标面板 ID"})
			return
		}
		var progress giftTargetPanelProgress
		_, err := store.updateState(func(state *appState) error {
			var resetErr error
			progress, resetErr = resetGiftTargetPanelProgress(state, panelID)
			return resetErr
		})
		if errors.Is(err, errGiftTargetPanelNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "progress": progress})
	}
}
