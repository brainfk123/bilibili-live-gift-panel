package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type outputConfigContractFixture struct {
	SchemaVersion       int      `json:"schemaVersion"`
	DisplaySceneLayouts []string `json:"displaySceneLayouts"`
	GiftTargetLayouts   []string `json:"giftTargetLayouts"`
	DisplayThemeIDs     []string `json:"displayThemeIds"`
	Appearance          struct {
		FontSize struct {
			Min     int `json:"min"`
			Max     int `json:"max"`
			Default int `json:"default"`
		} `json:"fontSize"`
		PanelOpacity struct {
			Min     int `json:"min"`
			Max     int `json:"max"`
			Default int `json:"default"`
		} `json:"panelOpacity"`
	} `json:"appearance"`
	BlindBoxViewerSlots struct {
		Min     int `json:"min"`
		Max     int `json:"max"`
		Default int `json:"default"`
	} `json:"blindBoxViewerSlots"`
}

func readOutputConfigContractFixture(t *testing.T) outputConfigContractFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "output-config-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture outputConfigContractFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestOutputConfigMatchesSharedContract(t *testing.T) {
	fixture := readOutputConfigContractFixture(t)
	if fixture.SchemaVersion != outputConfigSchemaVersion {
		t.Fatalf("schema version = %d, want %d", outputConfigSchemaVersion, fixture.SchemaVersion)
	}
	for _, layout := range fixture.DisplaySceneLayouts {
		if !isDisplaySceneLayout(layout) {
			t.Fatalf("display scene layout %q is missing", layout)
		}
	}
	if len(displaySceneLayoutIDs) != len(fixture.DisplaySceneLayouts) {
		t.Fatalf("display scene layout count = %d, want %d", len(displaySceneLayoutIDs), len(fixture.DisplaySceneLayouts))
	}
	for _, layout := range fixture.GiftTargetLayouts {
		if !isGiftTargetLayout(layout) {
			t.Fatalf("gift target layout %q is missing", layout)
		}
	}
	if len(giftTargetLayoutIDs) != len(fixture.GiftTargetLayouts) {
		t.Fatalf("gift target layout count = %d, want %d", len(giftTargetLayoutIDs), len(fixture.GiftTargetLayouts))
	}
	for _, themeID := range fixture.DisplayThemeIDs {
		if !isDisplayThemeID(themeID) {
			t.Fatalf("display theme %q is missing", themeID)
		}
	}
	if fixture.Appearance.FontSize.Min != displayFontSizeMin || fixture.Appearance.FontSize.Max != displayFontSizeMax || fixture.Appearance.FontSize.Default != displayFontSizeDefault {
		t.Fatalf("font size contract does not match")
	}
	if fixture.Appearance.PanelOpacity.Min != displayPanelOpacityMin || fixture.Appearance.PanelOpacity.Max != displayPanelOpacityMax || fixture.Appearance.PanelOpacity.Default != displayPanelOpacityDefault {
		t.Fatalf("panel opacity contract does not match")
	}
	if fixture.BlindBoxViewerSlots.Min != blindBoxViewerSlotsMin || fixture.BlindBoxViewerSlots.Max != blindBoxViewerSlotsMax || fixture.BlindBoxViewerSlots.Default != blindBoxViewerSlotsDefault {
		t.Fatalf("viewer slots contract does not match")
	}
}

func TestConfigStorePersistsEveryOutputLayoutAndViewerSlots(t *testing.T) {
	fixture := readOutputConfigContractFixture(t)
	displayScenes := make([]map[string]any, 0, len(fixture.DisplaySceneLayouts))
	for _, layout := range fixture.DisplaySceneLayouts {
		displayScenes = append(displayScenes, map[string]any{
			"id": "scene-" + layout, "name": "Scene " + layout,
			"attributeNames": []string{"Score"}, "layout": layout, "themeId": "glass",
		})
	}
	payload, err := json.Marshal(map[string]any{
		"attributes":    []map[string]any{{"name": "Score", "value": 0, "unit": "none", "format": "number", "decimals": 0, "suffix": ""}},
		"displayScenes": displayScenes,
		"blindBoxDisplay": map[string]any{
			"themeId": "glass", "fontSize": 48, "accentColor": "#fb7299", "showConnection": true,
			"align": "center", "panelOpacity": 55, "viewerSlots": 8,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(payload))))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	gotLayouts := make([]string, 0, len(state.DisplayScenes))
	for _, scene := range state.DisplayScenes {
		gotLayouts = append(gotLayouts, scene.Layout)
	}
	if !reflect.DeepEqual(gotLayouts, fixture.DisplaySceneLayouts) {
		t.Fatalf("display scene layouts = %#v, want %#v", gotLayouts, fixture.DisplaySceneLayouts)
	}
	if state.BlindBoxDisplay.ViewerSlots != 8 {
		t.Fatalf("viewer slots = %d, want 8", state.BlindBoxDisplay.ViewerSlots)
	}
	encoded, err := json.Marshal(state.BlindBoxDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"viewerSlots":8`) || !strings.Contains(string(encoded), `"themeId":"glass"`) {
		t.Fatalf("blind box display JSON = %s", encoded)
	}
}
