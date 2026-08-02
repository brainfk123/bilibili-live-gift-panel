package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigStoreLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel", "config.json")
	store := &configStore{path: path}

	empty := httptest.NewRecorder()
	store.handle(empty, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if empty.Code != http.StatusNoContent {
		t.Fatalf("empty GET status = %d, want 204", empty.Code)
	}

	payload := `{"roomId":"31567150","attributes":[],"rules":[]}`
	put := httptest.NewRecorder()
	store.handle(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("saved config is invalid JSON: %v", err)
	}
	if saved["roomId"] != "31567150" {
		t.Fatalf("saved roomId = %#v", saved["roomId"])
	}

	get := httptest.NewRecorder()
	store.handle(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "31567150") {
		t.Fatalf("GET status = %d, body = %s", get.Code, get.Body.String())
	}

	replace := httptest.NewRecorder()
	store.handle(replace, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"roomId":"2"}`)))
	if replace.Code != http.StatusOK {
		t.Fatalf("replacement PUT status = %d, body = %s", replace.Code, replace.Body.String())
	}

	deleted := httptest.NewRecorder()
	store.handle(deleted, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", deleted.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config still exists after DELETE: %v", err)
	}
}

func TestConfigStoreRejectsInvalidJSON(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`[]`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestConfigStoreReconnectsOnlyWhenRoomChanges(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	changes := 0
	store.setOnChange(func() { changes++ })

	put := func(payload string) {
		response := httptest.NewRecorder()
		store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
		if response.Code != http.StatusOK {
			t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	put(`{"roomId":"31567150","attributes":[],"rules":[]}`)
	put(`{"roomId":"31567150","attributes":[{"name":"积分","value":0,"unit":"number","format":"number"}],"rules":[]}`)
	if changes != 1 {
		t.Fatalf("same-room property edit triggered %d reconnects, want 1 initial room change", changes)
	}
	put(`{"roomId":"32025114","attributes":[],"rules":[]}`)
	if changes != 2 {
		t.Fatalf("room change callbacks = %d, want 2", changes)
	}
}

func TestConfigStoreRejectsFormulaThatUsesFrontendOnlyVariable(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[{"name":"积分","value":0,"unit":"none","format":"number","decimals":0,"suffix":""}],
        "rules":[{"id":"r1","giftId":1,"attributeName":"积分","formulaName":"旧公式","formula":"积分+count"}]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "count") {
		t.Fatalf("error does not explain the removed variable: %s", response.Body.String())
	}
}

func TestConfigStoreRejectsGiftOnlyPriceVariableInTimer(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[{"name":"加班时间","value":120,"unit":"seconds","format":"hhmmss","decimals":0,"suffix":""}],
        "rules":[],
        "timerRules":[{"id":"timer-1","attributeName":"加班时间","formulaName":"错误定时器","intervalSeconds":60,"condition":"price>0","formula":"加班时间-60","enabled":true}]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "price") {
		t.Fatalf("error does not explain the unavailable variable: %s", response.Body.String())
	}
}

func TestLegacyCompletedConfigDefaultsTutorialToHidden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
        "roomId":"31567150",
        "attributes":[{"name":"积分","value":0,"unit":"none","format":"number","decimals":0,"suffix":""}],
        "rules":[{"id":"r1","giftId":1,"attributeName":"积分","formula":"积分+1"}],
        "settings":{}
    }`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: path}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.ShowTutorial == nil || *state.Settings.ShowTutorial {
		t.Fatal("legacy completed setup should not reopen the tutorial")
	}
}
