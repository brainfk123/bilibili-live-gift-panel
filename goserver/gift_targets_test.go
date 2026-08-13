package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const giftTargetTestConfig = `{
  "giftKpiPanels":[
    {"id":"target-1","name":"本场目标","layout":"grid","items":[{"giftId":1,"giftName":"小花花","target":10,"received":0,"barStyle":"progress"}],"appearance":{"themeId":"glass","fontSize":48,"accentColor":"#fb7299","showConnection":true,"align":"center","panelOpacity":55}},
    {"id":"target-2","name":"备用目标","layout":"stack","items":[{"giftId":2,"giftName":"情书","target":20,"received":0,"barStyle":"health"}],"appearance":{"themeId":"glass","fontSize":48,"accentColor":"#fb7299","showConnection":true,"align":"center","panelOpacity":55}}
  ]
}`

func newGiftTargetTestStore(t *testing.T) *configStore {
	t.Helper()
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(giftTargetTestConfig)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	return store
}

func TestGiftTargetConfigPatchPreservesBackendProgress(t *testing.T) {
	store := newGiftTargetTestStore(t)
	if _, err := store.updateState(func(state *appState) error {
		state.GiftKPIPanels[0].Items[0].Received = 7
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	patch := httptest.NewRecorder()
	store.handle(patch, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{
      "giftKpiPanels":[
        {"id":"target-1","name":"更新后的目标","layout":"grid","items":[{"giftId":1,"giftName":"小花花","target":30,"received":0,"barStyle":"resource"}],"appearance":{"themeId":"glass","fontSize":48,"accentColor":"#fb7299","showConnection":true,"align":"center","panelOpacity":55}}
      ]
    }`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patch.Code, patch.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.GiftKPIPanels) != 1 || state.GiftKPIPanels[0].Name != "更新后的目标" || state.GiftKPIPanels[0].Items[0].Target != 30 {
		t.Fatalf("configuration was not updated: %#v", state.GiftKPIPanels)
	}
	if state.GiftKPIPanels[0].Items[0].Received != 7 {
		t.Fatalf("received = %d, want backend-owned progress 7", state.GiftKPIPanels[0].Items[0].Received)
	}
}

func TestGiftTargetProgressCommitsAllTransactionShards(t *testing.T) {
	store := newGiftTargetTestStore(t)
	oldTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(store.path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.updateState(func(state *appState) error {
		applyGiftTargetEvent(state, giftEvent{GiftID: 1, Num: 3})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configInfo, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if configInfo.ModTime().Equal(oldTime) {
		t.Fatal("runtime progress did not commit the config transaction shard")
	}
	configData, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), `"received"`) {
		t.Fatalf("config shard contains runtime progress: %s", configData)
	}
	historyData, err := os.ReadFile(store.historyPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(historyData), `"giftTargetProgress"`) || !strings.Contains(string(historyData), `"1": 3`) {
		t.Fatalf("history shard does not contain gift target progress: %s", historyData)
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.GiftKPIPanels[0].Items[0].Received != 3 {
		t.Fatalf("reloaded received = %d, want 3", state.GiftKPIPanels[0].Items[0].Received)
	}
}

func TestGiftTargetProgressHandlerResetsOnePanel(t *testing.T) {
	store := newGiftTargetTestStore(t)
	if _, err := store.updateState(func(state *appState) error {
		state.GiftKPIPanels[0].Items[0].Received = 7
		state.GiftKPIPanels[1].Items[0].Received = 9
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handleGiftTargetProgress(store)(response, httptest.NewRequest(http.MethodDelete, "/api/gift-targets/progress?panelId=target-1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Code     int                     `json:"code"`
		Progress giftTargetPanelProgress `json:"progress"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != 0 || payload.Progress.PanelID != "target-1" || payload.Progress.Items[0].Received != 0 {
		t.Fatalf("unexpected response: %#v", payload)
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.GiftKPIPanels[0].Items[0].Received != 0 || state.GiftKPIPanels[1].Items[0].Received != 9 {
		t.Fatalf("reset affected wrong panels: %#v", state.GiftKPIPanels)
	}

	missing := httptest.NewRecorder()
	handleGiftTargetProgress(store)(missing, httptest.NewRequest(http.MethodDelete, "/api/gift-targets/progress?panelId=missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing panel status = %d, want 404", missing.Code)
	}
}
