package main

import (
	"encoding/json"
	"fmt"
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
