package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

type fakeStateDirectory struct {
	syncErr  error
	closeErr error
}

func (d *fakeStateDirectory) Sync() error  { return d.syncErr }
func (d *fakeStateDirectory) Close() error { return d.closeErr }

func TestSyncStateDirectoryPropagatesOpenAndStorageErrors(t *testing.T) {
	storageErr := errors.New("storage failure")
	for name, open := range map[string]func(string) (stateDirectory, error){
		"open": func(string) (stateDirectory, error) { return nil, storageErr },
		"sync": func(string) (stateDirectory, error) { return &fakeStateDirectory{syncErr: storageErr}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			if err := syncStateDirectoryWith("unused", open, true); !errors.Is(err, storageErr) {
				t.Fatalf("error = %v, want storage failure", err)
			}
		})
	}
}

func TestSyncStateDirectorySuppressesOnlyUnsupportedWindowsSync(t *testing.T) {
	for _, unsupported := range []error{syscall.Errno(1), syscall.Errno(5), syscall.Errno(6)} {
		open := func(string) (stateDirectory, error) {
			return &fakeStateDirectory{syncErr: unsupported}, nil
		}
		if err := syncStateDirectoryWith("unused", open, true); err != nil {
			t.Fatalf("unsupported Windows directory sync error = %v", err)
		}
	}
	open := func(string) (stateDirectory, error) { return &fakeStateDirectory{syncErr: syscall.Errno(1)}, nil }
	if err := syncStateDirectoryWith("unused", open, false); !errors.Is(err, syscall.Errno(1)) {
		t.Fatalf("non-Windows error = %v", err)
	}
}

func TestAtomicWriteOutcomeMarksPostRenameDirectorySyncFailureCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	injected := errors.New("injected post-rename directory sync failure")
	outcome := writeFileAtomicallyOutcomeWith(path, []byte("committed\n"), func(string) error { return injected })
	if !outcome.Committed || !errors.Is(outcome.Err, injected) {
		t.Fatalf("outcome = %+v, want committed injected warning", outcome)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "committed\n" {
		t.Fatalf("final path data = %q", data)
	}
}

func TestConfigStoreMigratesLegacyFileIntoShards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{
        "roomId":"31567150",
        "attributes":[{"name":"积分","value":7,"unit":"none","format":"number","decimals":0,"suffix":""}],
        "giftCatalog":[{"id":1,"name":"测试礼物","price":100,"coinType":"gold","imgBasic":"gift.png"}],
        "recentGifts":[{"id":1,"name":"测试礼物","price":100,"coinType":"gold","imgBasic":"gift.png","lastReceived":10,"count":1}],
        "log":[{"time":10,"giftId":1,"giftName":"测试礼物","num":1,"uname":"观众","attributeName":"积分","delta":1,"valueAfter":7,"ruleId":"r1"}],
        "contributions":{"viewers":[{"key":"uid:1","uid":1,"uname":"观众","giftCount":1,"goldValue":100,"attributeDeltas":{"积分":1}}]}
    }`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: path}
	if err := store.migrateLegacy(); err != nil {
		t.Fatal(err)
	}

	configData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), `"log"`) || strings.Contains(string(configData), `"giftCatalog"`) {
		t.Fatalf("migrated config still contains cache/history fields: %s", configData)
	}
	if _, err := os.Stat(filepath.Join(dir, "cache.json")); err != nil {
		t.Fatalf("cache shard was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "history.json")); err != nil {
		t.Fatalf("history shard was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.log")); err != nil {
		t.Fatalf("event log shard was not created: %v", err)
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Attributes[0].Value != 7 || len(state.GiftCatalog) != 1 || len(state.Log) != 1 || len(state.Contributions.Viewers) != 1 {
		t.Fatalf("migrated state lost data: %#v", state)
	}
}

func TestConfigStorePatchCommitsAllTransactionShards(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := `{
        "roomId":"31567150",
        "giftCatalog":[{"id":1,"name":"测试礼物","price":100,"coinType":"gold","imgBasic":"gift.png"}],
        "log":[{"time":10,"giftId":1,"giftName":"测试礼物","num":1,"uname":"观众","attributeName":"积分","delta":1,"valueAfter":1,"ruleId":"r1"}]
    }`
	put := httptest.NewRecorder()
	store.handle(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(initial)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}
	oldTime := time.Unix(1_700_000_000, 0)
	cachePath := filepath.Join(dir, "cache.json")
	historyPath := filepath.Join(dir, "history.json")
	eventLogPath := filepath.Join(dir, "events.log")
	if err := os.Chtimes(cachePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(historyPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(eventLogPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	patch := httptest.NewRecorder()
	store.handle(patch, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"settings":{"theme":"light"}}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patch.Code, patch.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.Theme != "light" || len(state.Log) != 1 || len(state.GiftCatalog) != 1 {
		t.Fatalf("partial update did not preserve merged state: %#v", state)
	}
	for _, sidecar := range []string{cachePath, historyPath, eventLogPath} {
		info, err := os.Stat(sidecar)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Equal(oldTime) {
			t.Fatalf("transaction shard %s was not rewritten", sidecar)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "state-transaction.json")); !os.IsNotExist(err) {
		t.Fatalf("completed transaction evidence was not removed: %v", err)
	}
}

func TestConfigStorePersistsGiftClipCrops(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	legacyPlacementSettingsKey := "giftClip" + "Placements"
	patch := httptest.NewRecorder()
	payload := fmt.Sprintf(`{
		"settings":{
			"giftClipCrops":{
				"effect:99":{"x":0.1,"y":0.2,"width":0.6,"height":0.7},
				"media:clamped":{"x":0.9,"y":-1,"width":0.5,"height":2},
				"media:invalid":{"x":0,"y":0,"width":0,"height":1}
			},
			%q:{"effect:legacy":{"x":12,"y":-8}}
		}
	}`, legacyPlacementSettingsKey)
	store.handle(patch, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(payload)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patch.Code, patch.Body.String())
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"`+legacyPlacementSettingsKey+`"`) {
		t.Fatalf("legacy %s survived read/write cycle: %s", legacyPlacementSettingsKey, data)
	}
	if got := state.Settings.GiftClipCrops["effect:99"]; got != (giftClipCropState{X: .1, Y: .2, Width: .6, Height: .7}) {
		t.Fatalf("saved crop = %#v", got)
	}
	if got := state.Settings.GiftClipCrops["media:clamped"]; got != (giftClipCropState{X: .5, Y: 0, Width: .5, Height: 1}) {
		t.Fatalf("clamped crop = %#v", got)
	}
	if got := state.Settings.GiftClipCrops["media:invalid"]; got != (giftClipCropState{X: 0, Y: 0, Width: 1, Height: 1}) {
		t.Fatalf("repaired crop = %#v", got)
	}
	clone, err := cloneAppState(state)
	if err != nil {
		t.Fatal(err)
	}
	if got := clone.Settings.GiftClipCrops["effect:99"]; got != state.Settings.GiftClipCrops["effect:99"] {
		t.Fatalf("cloned crop = %#v, want %#v", got, state.Settings.GiftClipCrops["effect:99"])
	}
}

func TestConfigStorePatchPreservesExplicitlyDisabledRules(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := `{
		"attributes":[{"name":"积分","value":0,"unit":"none","format":"number"}],
		"rules":[{"id":"gift-rule","giftId":1,"attributeName":"积分","formula":"积分+1","enabled":true}],
		"timerRules":[{"id":"timer-rule","attributeName":"积分","formulaName":"每秒减少","intervalSeconds":1,"formula":"积分-1","enabled":true}]
	}`
	put := httptest.NewRecorder()
	store.handle(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(initial)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}

	patch := httptest.NewRecorder()
	store.handle(patch, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{
		"rules":[{"id":"gift-rule","giftId":1,"attributeName":"积分","formula":"积分+1","enabled":false}],
		"timerRules":[{"id":"timer-rule","attributeName":"积分","formulaName":"每秒减少","intervalSeconds":1,"formula":"积分-1","enabled":false}]
	}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patch.Code, patch.Body.String())
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Rules) != 1 || state.Rules[0].Enabled == nil || *state.Rules[0].Enabled {
		t.Fatalf("explicit enabled=false was not preserved: %#v", state.Rules)
	}
	if len(state.TimerRules) != 1 || state.TimerRules[0].Enabled {
		t.Fatalf("explicit timer enabled=false was not preserved: %#v", state.TimerRules)
	}
}

func TestConfigStorePatchClearsOmittedOptionalFieldsInsideReplacementArrays(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := `{
		"attributes":[{"name":"积分","value":0,"unit":"none","format":"number"}],
		"displayScenes":[{"id":"scene-score","name":"积分面板","attributeNames":["积分"],"layout":"stack","themeId":"glass"}],
		"activities":[{
			"id":"activity-score","name":"积分活动","attributeNames":["积分"],"sceneId":"scene-score",
			"status":"not_started","resultMode":"highest","gateRules":false,"initialValues":{"积分":0}
		}]
	}`
	put := httptest.NewRecorder()
	store.handle(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(initial)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}

	patch := httptest.NewRecorder()
	store.handle(patch, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{
		"displayScenes":[],
		"activities":[{
			"id":"activity-score","name":"积分活动","attributeNames":["积分"],
			"status":"not_started","resultMode":"highest","gateRules":false,"initialValues":{"积分":0}
		}]
	}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patch.Code, patch.Body.String())
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.DisplayScenes) != 0 {
		t.Fatalf("display scenes = %#v, want none", state.DisplayScenes)
	}
	if len(state.Activities) != 1 || state.Activities[0].SceneID != "" {
		t.Fatalf("activity scene reference was not cleared: %#v", state.Activities)
	}
}

func TestConfigStoreMigratesMissingFieldsWithDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"schemaVersion":0,"roomId":"31567150","settings":{"theme":"light"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: path}
	if err := store.migrateLegacy(); err != nil {
		t.Fatal(err)
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.Theme != "light" || state.Settings.FontSize == 0 || state.Settings.AccentColor == "" {
		t.Fatalf("legacy fields were not merged with current defaults: %#v", state.Settings)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != stateShardSchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", metadata.SchemaVersion, stateShardSchemaVersion)
	}
}

func TestStateShardVersionElevenUpgradesToTwelve(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := os.WriteFile(store.path, []byte(`{"schemaVersion":11,"settings":{"theme":"light"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateLegacy(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != 12 {
		t.Fatalf("schemaVersion = %d, want 12", metadata.SchemaVersion)
	}
}

func TestNormalizeGiftClipCropsLimitsCount(t *testing.T) {
	input := make(map[string]giftClipCropState, 204)
	for index := 0; index < 204; index++ {
		input[fmt.Sprintf("effect:%d", index)] = giftClipCropState{X: 0, Y: 0, Width: 1, Height: 1}
	}
	got := normalizeGiftClipCrops(input)
	if len(got) != 200 {
		t.Fatalf("crop count = %d, want 200", len(got))
	}
}

func TestNormalizeGiftClipCropsRepairsNonFinite(t *testing.T) {
	got := normalizeGiftClipCrops(map[string]giftClipCropState{
		"invalid": {X: math.NaN(), Y: 0, Width: 1, Height: 1},
	})
	if crop := got["invalid"]; crop != fullGiftClipCrop() {
		t.Fatalf("non-finite crop = %#v", crop)
	}
}

func TestNormalizeGiftClipCropsAcceptsUnicodeKeysAtCharacterLimit(t *testing.T) {
	key := strings.Repeat("礼", 160)
	want := fullGiftClipCrop()
	got := normalizeGiftClipCrops(map[string]giftClipCropState{key: want})
	if crop, exists := got[key]; !exists || crop != want {
		t.Fatalf("unicode key crop = %#v, exists = %t", crop, exists)
	}
}

func TestNormalizeGiftClipCropsAppliesSharedKeyPolicy(t *testing.T) {
	want := fullGiftClipCrop()
	unicodeAtLimit := strings.Repeat("🎁", 160)
	unicodeOverLimit := strings.Repeat("🎁", 161)
	got := normalizeGiftClipCrops(map[string]giftClipCropState{
		"  effect:trimmed  ": want,
		unicodeAtLimit:       want,
		unicodeOverLimit:     want,
		" constructor ":      want,
		"prototype":          want,
		"__proto__":          want,
	})
	if len(got) != 2 {
		t.Fatalf("normalized crop keys = %#v, want two accepted keys", got)
	}
	if crop, exists := got["effect:trimmed"]; !exists || crop != want {
		t.Fatalf("trimmed crop = %#v, exists = %t", crop, exists)
	}
	if crop, exists := got[unicodeAtLimit]; !exists || crop != want {
		t.Fatalf("unicode crop = %#v, exists = %t", crop, exists)
	}
}

func TestConfigExperienceDefaultsAndLegacyMigration(t *testing.T) {
	if experience := defaultAppState().Settings.ConfigExperience; experience != "simple" {
		t.Fatalf("new config experience = %q, want simple", experience)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	legacy := fmt.Sprintf(`{"schemaVersion":%d,"settings":{"theme":"light"}}`, stateShardSchemaVersion)
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: path}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.ConfigExperience != "advanced" {
		t.Fatalf("legacy config experience = %q, want advanced", state.Settings.ConfigExperience)
	}
}

func TestConfigExperienceNormalizesInvalidValueToAdvanced(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{
		"settings":{"configExperience":"unexpected"}
	}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.ConfigExperience != "advanced" {
		t.Fatalf("normalized config experience = %q, want advanced", state.Settings.ConfigExperience)
	}
}

func TestConfigStorePersistsAndPatchesSimplePlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	store := &configStore{path: path}
	payload := `{
		"settings":{"configExperience":"simple"},
		"attributes":[
			{"id":"attribute-overtime","name":"加班时间","value":0,"unit":"seconds","format":"hhmmss","decimals":0,"suffix":""},
			{"id":"attribute-score","name":"积分","value":0,"unit":"none","format":"number","decimals":0,"suffix":""}
		],
		"simplePlay":{
			"version":1,
			"templateId":"overtime",
			"templateVersion":2,
			"attributeId":"attribute-overtime",
			"parameters":{"initialSeconds":300,"allowNegative":false},
			"gifts":{"add":[1,2],"reset":[3]},
			"overtimeGiftActions":[
				{"giftId":1,"operation":"add","seconds":60},
				{"giftId":3,"operation":"double","seconds":999}
			],
			"managedFingerprint":"managed-v1"
		}
	}`
	put := httptest.NewRecorder()
	store.handle(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", put.Code, put.Body.String())
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.SimplePlay == nil || state.SimplePlay.TemplateID != "overtime" || state.SimplePlay.AttributeID != "attribute-overtime" {
		t.Fatalf("simplePlay was not persisted: %#v", state.SimplePlay)
	}
	if got := state.SimplePlay.Parameters["initialSeconds"]; got != float64(300) {
		t.Fatalf("simplePlay parameter = %#v, want 300", got)
	}
	if len(state.SimplePlay.Gifts["add"]) != 2 || len(state.SimplePlay.OvertimeGiftActions) != 2 {
		t.Fatalf("simplePlay gifts/actions were not preserved: %#v", state.SimplePlay)
	}
	if state.SimplePlay.OvertimeGiftActions[0].Seconds == nil || *state.SimplePlay.OvertimeGiftActions[0].Seconds != 60 {
		t.Fatalf("add seconds were not preserved: %#v", state.SimplePlay.OvertimeGiftActions[0])
	}
	if state.SimplePlay.OvertimeGiftActions[1].Seconds != nil {
		t.Fatalf("double seconds were not normalized away: %#v", state.SimplePlay.OvertimeGiftActions[1])
	}
	configData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configData), `"simplePlay"`) {
		t.Fatalf("simplePlay was not written to the main config shard: %s", configData)
	}

	patch := httptest.NewRecorder()
	store.handle(patch, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{
		"simplePlay":{
			"version":1,"templateId":"counter","templateVersion":1,"attributeId":"attribute-score",
			"parameters":{"initialValue":0},"gifts":{"increment":[9]},"managedFingerprint":"managed-v2"
		}
	}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patch.Code, patch.Body.String())
	}
	state, err = store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.SimplePlay == nil || state.SimplePlay.TemplateID != "counter" || state.SimplePlay.ManagedFingerprint != "managed-v2" {
		t.Fatalf("simplePlay PATCH was not persisted: %#v", state.SimplePlay)
	}
	if state.Settings.ConfigExperience != "simple" {
		t.Fatalf("simplePlay PATCH changed config experience to %q", state.Settings.ConfigExperience)
	}
}

func TestConfigStoreRejectsSimplePlayWithMissingAttribute(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	payload := `{
		"settings":{"configExperience":"simple"},
		"simplePlay":{
			"version":1,"templateId":"counter","templateVersion":1,"attributeId":"missing",
			"parameters":{"name":"积分"},"gifts":{"count":[1]},"managedFingerprint":"managed-v1"
		}
	}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want %d, body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "引用的属性不存在") {
		t.Fatalf("unexpected error: %s", response.Body.String())
	}
}

func TestConfigStoreRefusesNewerSchemaWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := []byte(`{"schemaVersion":999,"roomId":"future","futureField":{"keep":true}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: path}
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"settings":{"theme":"light"}}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("PATCH status = %d, body = %s, want 409", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("newer config was overwritten:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(dir, "cache.json")); !os.IsNotExist(err) {
		t.Fatalf("sidecar was created despite incompatible schema: %v", err)
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

func TestConfigStorePreservesInternalIngestionLedgersAcrossClientReplacement(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.AppliedIngressIDs = []string{"ingress-1"}
	initial.RecentSourceGiftKeys = map[string]int64{"rnd-1": time.Now().UnixMilli()}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	replacement := defaultAppState()
	replacement.RoomID = "200"
	replaced, err := store.replaceClientState(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.State.AppliedIngressIDs) != 1 || replaced.State.AppliedIngressIDs[0] != "ingress-1" {
		t.Fatalf("applied ingress IDs = %#v", replaced.State.AppliedIngressIDs)
	}
	if _, exists := replaced.State.RecentSourceGiftKeys["rnd-1"]; !exists {
		t.Fatalf("recent source keys = %#v", replaced.State.RecentSourceGiftKeys)
	}

	encoded, err := json.Marshal(replaced.State)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "appliedIngressIds") || strings.Contains(string(encoded), "recentSourceGiftKeys") {
		t.Fatalf("internal ledgers leaked into client state: %s", encoded)
	}
	history, err := os.ReadFile(filepath.Join(dir, "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(history), "appliedIngressIds") || !strings.Contains(string(history), "recentSourceGiftKeys") {
		t.Fatalf("internal ledgers missing from history shard: %s", history)
	}
}

func TestUpdateStateForIngestionBoundsAppliedLedger(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	initial := defaultAppState()
	initial.AppliedIngressIDs = make([]string, maxAppliedIngressIDs)
	for index := range initial.AppliedIngressIDs {
		initial.AppliedIngressIDs[index] = fmt.Sprintf("ingress-%04d", index)
	}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	state, applied, err := store.updateStateForIngestion("ingress-new", func(*appState) error { return nil })
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if len(state.AppliedIngressIDs) != maxAppliedIngressIDs {
		t.Fatalf("applied ingress count = %d", len(state.AppliedIngressIDs))
	}
	if state.AppliedIngressIDs[0] != "ingress-0001" || state.AppliedIngressIDs[len(state.AppliedIngressIDs)-1] != "ingress-new" {
		t.Fatalf("bounded applied ingress IDs = %#v", state.AppliedIngressIDs)
	}
}

func TestConfigStorePrunesRecentSourceGiftKeysByAgeAndCount(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	initial := defaultAppState()
	now := time.Now()
	initial.RecentSourceGiftKeys = map[string]int64{"expired": now.Add(-2 * time.Minute).UnixMilli()}
	for index := 0; index < 501; index++ {
		initial.RecentSourceGiftKeys[fmt.Sprintf("recent-%03d", index)] = now.Add(-time.Duration(index) * time.Millisecond).UnixMilli()
	}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.RecentSourceGiftKeys) != 500 {
		t.Fatalf("recent source key count = %d, want 500", len(state.RecentSourceGiftKeys))
	}
	if _, exists := state.RecentSourceGiftKeys["expired"]; exists {
		t.Fatal("expired source key was retained")
	}
	if _, exists := state.RecentSourceGiftKeys["recent-500"]; exists {
		t.Fatal("oldest over-limit source key was retained")
	}
	if _, exists := state.RecentSourceGiftKeys["recent-000"]; !exists {
		t.Fatal("newest source key was pruned")
	}
}

func TestConfigStoreClearsRoomScopedRecordsWhenRoomChanges(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	initial := defaultAppState()
	initial.RoomID = "100"
	initial.Attributes = []attributeState{{Name: "积分", Value: 7, Unit: "none", Format: "number"}}
	initial.GiftCatalog = []giftInfo{{ID: 1, Name: "测试礼物", Price: 100, CoinType: "gold"}}
	initial.RecentGifts = []recentGift{{giftInfo: giftInfo{ID: 1, Name: "测试礼物", Price: 100, CoinType: "gold"}, Count: 2}}
	initial.Stats = map[string]dayStats{"2026-08-04": {Date: "2026-08-04", GiftTotals: map[string]int{"1": 2}}}
	initial.Log = []logEntry{{Time: 1, GiftID: 1, GiftName: "测试礼物", AttributeName: "积分", Delta: 1, ValueAfter: 7}}
	initial.Contributions = contributionLedgerState{
		UpdatedAt: 10,
		Viewers:   []viewerContribution{{Key: "uid:1", UID: 1, Uname: "观众", GiftCount: 2, AttributeDeltas: map[string]float64{"积分": 2}}},
	}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"roomId":"200"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if updated.RoomID != "200" {
		t.Fatalf("roomId = %q, want 200", updated.RoomID)
	}
	if len(updated.RecentGifts) != 0 || len(updated.Stats) != 0 || len(updated.Log) != 0 || len(updated.Contributions.Viewers) != 0 {
		t.Fatalf("room-scoped records were not cleared: recent=%d stats=%d log=%d viewers=%d", len(updated.RecentGifts), len(updated.Stats), len(updated.Log), len(updated.Contributions.Viewers))
	}
	if updated.Contributions.UpdatedAt <= initial.Contributions.UpdatedAt {
		t.Fatalf("cleared contribution ledger timestamp = %d, want newer than %d", updated.Contributions.UpdatedAt, initial.Contributions.UpdatedAt)
	}
	if len(updated.Attributes) != 1 || len(updated.GiftCatalog) != 1 {
		t.Fatalf("configuration was cleared with records: attributes=%d gifts=%d", len(updated.Attributes), len(updated.GiftCatalog))
	}
}

func TestConfigStorePreservesRoomScopedRecordsWhenReconnectingSameRoom(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	initial := defaultAppState()
	initial.RoomID = "100"
	initial.Log = []logEntry{{Time: 1, GiftID: 1, GiftName: "测试礼物", AttributeName: "积分", Delta: 1, ValueAfter: 1}}
	initial.Contributions = contributionLedgerState{UpdatedAt: 10, Viewers: []viewerContribution{{Key: "uid:1", UID: 1, Uname: "观众"}}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"roomId":"100"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Log) != 1 || len(updated.Contributions.Viewers) != 1 {
		t.Fatalf("same-room reconnect cleared records: log=%d viewers=%d", len(updated.Log), len(updated.Contributions.Viewers))
	}
}

func TestConfigStoreNotifiesTimerChangesWithoutReconnect(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	roomChanges := 0
	timerChanges := 0
	store.setOnChange(func() { roomChanges++ })
	store.setOnTimerChange(func() { timerChanges++ })

	put := func(payload string) {
		response := httptest.NewRecorder()
		store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
		if response.Code != http.StatusOK {
			t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	put(`{"roomId":"31567150","attributes":[{"name":"加班时间","value":60,"unit":"seconds","format":"hhmmss"}],"timerRules":[{"id":"timer-1","attributeName":"加班时间","formulaName":"每分钟减少","intervalSeconds":60,"formula":"加班时间-60","enabled":false}]}`)
	put(`{"roomId":"31567150","attributes":[{"name":"加班时间","value":60,"unit":"seconds","format":"hhmmss"}],"timerRules":[{"id":"timer-1","attributeName":"加班时间","formulaName":"每分钟减少","intervalSeconds":60,"formula":"加班时间-60","enabled":true}]}`)

	if roomChanges != 1 {
		t.Fatalf("timer-only config edit triggered %d reconnects, want 1 initial room change", roomChanges)
	}
	if timerChanges != 2 {
		t.Fatalf("timer config callbacks = %d, want 2 changes", timerChanges)
	}
}

func TestConfigStoreNotifiesAutomaticUpdateSettingChanges(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	changes := 0
	store.setOnUpdateChange(func() { changes++ })

	put := func(enabled bool) {
		response := httptest.NewRecorder()
		payload := fmt.Sprintf(`{"settings":{"autoUpdate":%t}}`, enabled)
		store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
		if response.Code != http.StatusOK {
			t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	put(true)
	put(false)
	put(false)
	put(true)
	if changes != 2 {
		t.Fatalf("automatic update callbacks = %d, want 2", changes)
	}
}

func TestConfigStoreRejectsFormulaThatUsesFrontendOnlyVariable(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[{"name":"积分","value":0,"unit":"none","format":"number","decimals":0,"suffix":""}],
        "rules":[{"id":"r1","giftId":1,"attributeName":"积分","formulaName":"旧规则","formula":"积分+count"}]
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

func TestConfigStoreRejectsReservedFormulaNameAttributes(t *testing.T) {
	for _, name := range []string{"用户身份", "普通用户", "粉丝团", "舰长", "提督", "总督"} {
		t.Run(name, func(t *testing.T) {
			store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
			payload := fmt.Sprintf(`{"attributes":[{"name":%q,"value":0}]}`, name)
			response := httptest.NewRecorder()
			store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "系统公式名称不能作为属性名") {
				t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestConfigStoreAllowsNearMatchToReservedFormulaName(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"attributes":[{"name":"用户身份等级","value":0}]}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
}

func TestConfigStoreRejectsReservedFormulaNamePresetSource(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{"formulaPresets":[{"id":"legacy","name":"历史预设","context":"gift","formula":"积分+1","sourceAttributeName":"用户身份"}]}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "系统公式名称不能作为预设来源属性") {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestConfigStorePersistsGiftRuleCondition(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
		"attributes":[{"name":"积分","value":0}],
		"rules":[{"id":"r1","giftId":1,"attributeName":"积分","formulaName":"舰长规则","condition":"用户身份>=舰长","formula":"积分+1"}]
	}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Rules) != 1 || state.Rules[0].Condition != "用户身份>=舰长" {
		t.Fatalf("rules = %#v, want persisted condition", state.Rules)
	}
}

func TestConfigStoreValidationDoesNotEvaluateRandomChoiceBranches(t *testing.T) {
	originalRandomIntn := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = originalRandomIntn })

	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
		"attributes":[{"name":"积分","value":0}],
		"rules":[{
			"id":"r1","giftId":1,"attributeName":"积分","formulaName":"惰性随机规则",
			"condition":"RANDOMCHOICE(1,1/0)","formula":"RANDOMCHOICE(10,1/0)"
		}]
	}`

	for _, selectedIndex := range []int{0, 1} {
		formulaRandomIntn = func(limit int) int {
			if limit != 2 {
				t.Fatalf("random limit = %d, want 2", limit)
			}
			return selectedIndex
		}
		response := httptest.NewRecorder()
		store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
		if response.Code != http.StatusOK {
			t.Fatalf("selected branch %d: status = %d, want 200; body = %s", selectedIndex, response.Code, response.Body.String())
		}
	}
}

func TestConfigStoreRejectsGuaranteedFormulaRuntimeErrors(t *testing.T) {
	overflow := "1" + strings.Repeat("0", 307) + "*100"
	tests := map[string]string{
		"1/0":                         "除数为零",
		overflow:                      "规则结果不是有效数字",
		"RANDBETWEEN(10,1)":           "最小值不能大于最大值",
		"积分*(" + overflow + ")":       "规则结果不是有效数字",
		"积分/ROUND(1,309)":             "规则结果不是有效数字",
		"MAX(积分," + overflow + ")":    "规则结果不是有效数字",
		"MIN(积分,-(" + overflow + "))": "规则结果不是有效数字",
		"ROUND(积分,309)":               "规则结果不是有效数字",
	}
	for formula, message := range tests {
		t.Run(message, func(t *testing.T) {
			store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
			payload := fmt.Sprintf(`{
				"attributes":[{"name":"积分","value":0}],
				"rules":[{"id":"r1","giftId":1,"attributeName":"积分","formulaName":"错误规则","formula":%q}]
			}`, formula)
			response := httptest.NewRecorder()
			store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), message) {
				t.Fatalf("formula %s: status = %d, body = %s", formula, response.Code, response.Body.String())
			}
		})
	}
}

func TestConfigStoreRejectsInvalidGiftRuleCondition(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
		"attributes":[{"name":"积分","value":0}],
		"rules":[{"id":"r1","giftId":1,"attributeName":"积分","formulaName":"坏条件","condition":"用户身份>=不存在身份","formula":"积分+1"}]
	}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "运行条件无效") {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestConfigStoreRejectsGiftIdentityInTimer(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
		"attributes":[{"name":"积分","value":0}],
		"timerRules":[{"id":"timer","attributeName":"积分","formulaName":"错误定时器","intervalSeconds":60,"condition":"用户身份>=舰长","formula":"积分+1","enabled":true}]
	}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "用户身份") {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestConfigStorePersistsFormulaPresets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := &configStore{path: path}
	payload := `{
        "formulaPresets":[
            {"id":"gift-1","name":"按价格加时","context":"gift","formula":"加班时间+price/1000*60","sourceAttributeName":"加班时间"},
            {"id":"timer-1","name":"每分钟减少","context":"timer","formula":"MAX(加班时间-60,0)","sourceAttributeName":"加班时间"}
        ]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.FormulaPresets) != 2 {
		t.Fatalf("formula presets = %d, want 2", len(state.FormulaPresets))
	}
}

func TestConfigStorePersistsGameplayTemplateDisplayMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := &configStore{path: path}
	payload := `{
        "attributes":[{
            "name":"Boss 血量","value":720,"unit":"none","format":"suffix","decimals":0,"suffix":" HP",
            "display":{"variant":"health","themeId":"rpg","title":"深渊领主","min":0,"max":1000,"lowThreshold":20},
            "createdFromTemplateId":"boss","createdFromTemplateVersion":1
        }],
        "settings":{"defaultDisplayThemeId":"neon"}
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.DefaultDisplayThemeID != "neon" {
		t.Fatalf("default display theme = %q, want neon", state.Settings.DefaultDisplayThemeID)
	}
	attribute := state.findAttribute("Boss 血量")
	if attribute == nil || attribute.Display == nil {
		t.Fatal("template display metadata was not persisted")
	}
	if attribute.Display.Variant != "health" || attribute.Display.ThemeID != "rpg" || attribute.CreatedFromTemplateID != "boss" {
		t.Fatalf("unexpected template metadata: %#v", attribute)
	}
}

func TestConfigStorePersistsDisplayScenes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := &configStore{path: path}
	payload := `{
        "attributes":[
            {"name":"生命值","value":100,"unit":"none","format":"number","decimals":0,"suffix":""},
            {"name":"能量","value":50,"unit":"none","format":"number","decimals":0,"suffix":""}
        ],
        "displayScenes":[{
            "id":"scene-status","name":"战斗状态","attributeNames":["能量","生命值"],"layout":"grid","themeId":"neon"
        }]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.DisplayScenes) != 1 {
		t.Fatalf("display scenes = %d, want 1", len(state.DisplayScenes))
	}
	scene := state.DisplayScenes[0]
	if scene.Name != "战斗状态" || scene.Layout != "grid" || scene.ThemeID != "neon" {
		t.Fatalf("unexpected display scene: %#v", scene)
	}
	if len(scene.AttributeNames) != 2 || scene.AttributeNames[0] != "能量" || scene.AttributeNames[1] != "生命值" {
		t.Fatalf("scene attribute order = %#v", scene.AttributeNames)
	}
}

func TestConfigStoreRejectsDisplaySceneWithMissingAttribute(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[{"name":"生命值","value":100,"unit":"none","format":"number","decimals":0,"suffix":""}],
        "displayScenes":[{"id":"scene-bad","name":"错误面板","attributeNames":["不存在"],"layout":"stack","themeId":"glass"}]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "不存在的属性") {
		t.Fatalf("error does not explain the missing attribute: %s", response.Body.String())
	}
}

func TestConfigStorePersistsEnumValueMappings(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[{
            "name":"比赛结果","value":1,"unit":"none","format":"number","decimals":0,"suffix":"",
            "display":{"variant":"enum","themeId":"neon","valueMappings":[
                {"value":1,"label":"红队胜","color":"#ff3366","imageUrl":"https://example.com/red.png"}
            ]}
        }]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	mappings := state.Attributes[0].Display.ValueMappings
	if len(mappings) != 1 || mappings[0].Label != "红队胜" || mappings[0].Color != "#ff3366" {
		t.Fatalf("unexpected mappings: %#v", mappings)
	}
}

func TestConfigStoreRejectsDuplicateEnumValues(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[{
            "name":"比赛结果","value":1,"unit":"none","format":"number","decimals":0,"suffix":"",
            "display":{"variant":"enum","themeId":"glass","valueMappings":[
                {"value":1,"label":"红队胜"},{"value":1,"label":"蓝队胜"}
            ]}
        }]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "枚举数值不能重复") {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestConfigStorePersistsActivitySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := &configStore{path: path}
	payload := `{
        "attributes":[
            {"name":"红队","value":0,"unit":"none","format":"number","decimals":0,"suffix":""},
            {"name":"蓝队","value":0,"unit":"none","format":"number","decimals":0,"suffix":""}
        ],
        "displayScenes":[{"id":"scene-match","name":"对战面板","attributeNames":["红队","蓝队"],"layout":"grid","themeId":"neon"}],
        "activities":[{
            "id":"activity-match","name":"阵营对抗","attributeNames":["红队","蓝队"],"sceneId":"scene-match",
            "status":"not_started","resultMode":"highest","gateRules":true,"initialValues":{"红队":0,"蓝队":0},
            "milestones":[{"id":"target","name":"红队达标","attributeName":"红队","comparison":"gte","threshold":10,"action":"settle","message":"红队达标！"}],
            "giftTimeout":{"seconds":30,"action":"lock"}
        }]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Activities) != 1 || state.Activities[0].SceneID != "scene-match" || !state.Activities[0].GateRules || len(state.Activities[0].Milestones) != 1 || state.Activities[0].GiftTimeout == nil {
		t.Fatalf("unexpected activities: %#v", state.Activities)
	}
}

func TestConfigStoreRejectsActivityMilestoneForUnlinkedAttribute(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[
            {"name":"积分","value":0,"unit":"none","format":"number","decimals":0,"suffix":""},
            {"name":"生命值","value":100,"unit":"none","format":"number","decimals":0,"suffix":""}
        ],
        "activities":[{
            "id":"challenge","name":"积分挑战","attributeNames":["积分"],"status":"not_started","resultMode":"none","gateRules":true,
            "initialValues":{"积分":0},
            "milestones":[{"id":"bad","name":"错误目标","attributeName":"生命值","comparison":"gte","threshold":10,"action":"announce","message":""}]
        }]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "未关联的属性") {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestConfigStoreRejectsOverlappingGatedActivities(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "attributes":[{"name":"积分","value":0,"unit":"none","format":"number","decimals":0,"suffix":""}],
        "activities":[
            {"id":"a","name":"活动 A","attributeNames":["积分"],"status":"not_started","resultMode":"none","gateRules":true,"initialValues":{"积分":0}},
            {"id":"b","name":"活动 B","attributeNames":["积分"],"status":"not_started","resultMode":"none","gateRules":true,"initialValues":{"积分":0}}
        ]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "不能同时由活动") {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestConfigStoreRejectsInvalidFormulaPresetContext(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
        "formulaPresets":[
            {"id":"bad-1","name":"错误预设","context":"other","formula":"积分+1","sourceAttributeName":"积分"}
        ]
    }`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
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

func TestConfigStorePreservesTrainingTopicProgress(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{"settings":{"showTutorial":false,"trainingCompletedTopics":["blind-box","obs-no-change"]}}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"blind-box", "obs-no-change"}
	if len(state.Settings.TrainingCompletedTopics) != len(want) {
		t.Fatalf("training topics = %#v, want %#v", state.Settings.TrainingCompletedTopics, want)
	}
	for index := range want {
		if state.Settings.TrainingCompletedTopics[index] != want[index] {
			t.Fatalf("training topics = %#v, want %#v", state.Settings.TrainingCompletedTopics, want)
		}
	}
}

func TestConfigStorePreservesTutorialReplayModeAndTargetAttribute(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{
		"attributes":[{"id":"attribute-overtime","name":"加班时间","value":0,"unit":"seconds","format":"hhmmss","decimals":0,"suffix":""}],
		"settings":{"showTutorial":true,"tutorialVersion":3,"tutorialCompletedLessons":[],"tutorialReplayMode":true,"tutorialTargetAttributeId":"attribute-overtime"}
	}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.TutorialReplayMode == nil || !*state.Settings.TutorialReplayMode {
		t.Fatal("tutorial replay mode should survive persistence")
	}
	if len(state.Attributes) != 1 || state.Attributes[0].ID != "attribute-overtime" {
		t.Fatalf("tutorial attribute id = %#v, want attribute-overtime", state.Attributes)
	}
	if state.Settings.TutorialTargetAttributeID != "attribute-overtime" {
		t.Fatalf("tutorial target attribute id = %q, want attribute-overtime", state.Settings.TutorialTargetAttributeID)
	}
}

func TestLegacyResetTutorialInfersReplayMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
        "roomId":"31567150",
        "attributes":[{"name":"积分","value":0,"unit":"none","format":"number","decimals":0,"suffix":""}],
        "rules":[{"id":"r1","giftId":1,"attributeName":"积分","formula":"积分+1"}],
        "settings":{"showTutorial":true,"tutorialVersion":3,"tutorialCompletedLessons":[]}
    }`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: path}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.TutorialReplayMode == nil || !*state.Settings.TutorialReplayMode {
		t.Fatal("legacy reset tutorial should resume in replay mode")
	}
}

func TestConfigStorePreservesLastSeenChangelogVersion(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	payload := `{"settings":{"showTutorial":false,"lastSeenChangelogVersion":"0.2.0"}}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.LastSeenChangelogVersion != "0.2.0" {
		t.Fatalf("last seen changelog = %q, want %q", state.Settings.LastSeenChangelogVersion, "0.2.0")
	}
}
