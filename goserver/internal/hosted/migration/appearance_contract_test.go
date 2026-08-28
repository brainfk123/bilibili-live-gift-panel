package migration

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"bilibili-live-gift-panel/internal/hosted/configuration"
)

// This fixture is byte-for-byte the output locked by the desktop exporter's
// createOnlineMigration contract test. Removing any appearance adapter in the
// Hosted decode or composition path must make this test fail.
func TestExporterV2AppearanceSurvivesDecodeAndComposition(t *testing.T) {
	raw, err := os.ReadFile("testdata/online-migration-v2-appearance.json")
	if err != nil {
		t.Fatal(err)
	}
	envelope, report, err := Decode(bytes.NewReader(raw), maximumEnvelopeBytes)
	if err != nil {
		t.Fatalf("Decode exporter V2 appearance fixture: %v", err)
	}
	for _, pointer := range []string{
		"/payload/definition/appearance",
		"/payload/definition/attributes/0/display/appearance",
		"/payload/definition/displayScenes/0/appearance",
		"/payload/definition/blindBoxDisplay",
		"/payload/definition/giftTargetPanels/0/appearance",
	} {
		if contains(report.Ignored, pointer) {
			t.Fatalf("safe exporter appearance was silently ignored at %s: %#v", pointer, report)
		}
	}

	wantGlobal := &configuration.GlobalAppearance{Theme: "light", FontSize: 36, AccentColor: "#3366ff", Align: "left", PanelOpacity: 72, ShowConnection: false}
	wantAttribute := &configuration.DisplayAppearance{ThemeID: "neon", FontSize: 40, AccentColor: "#ff3366", ShowConnection: true, Align: "center", PanelOpacity: 80}
	wantScene := &configuration.DisplayAppearance{ThemeID: "pixel", FontSize: 44, AccentColor: "#00cc88", ShowConnection: false, Align: "right", PanelOpacity: 66}
	wantBlindBox := &configuration.DisplayAppearance{ThemeID: "kawaii", FontSize: 32, AccentColor: "#cc55ff", ShowConnection: true, Align: "center", PanelOpacity: 75}
	wantGiftTarget := &configuration.DisplayAppearance{ThemeID: "minimal", FontSize: 30, AccentColor: "#ffaa00", ShowConnection: false, Align: "left", PanelOpacity: 70}
	assertAppearanceDefinition(t, envelope.Definition, wantGlobal, wantAttribute, wantScene, wantBlindBox, wantGiftTarget)

	candidate, err := composeCandidate(
		envelope,
		emptyDefinition(),
		emptyRuntime(),
		completeCapabilities(),
		SelectionCommand{UnitIDs: []string{"attribute:health", "gift-target:gift-goal"}, IncludeGeneralSettings: true},
	)
	if err != nil {
		t.Fatalf("compose exporter V2 appearance fixture: %v", err)
	}
	if !candidate.Ready {
		t.Fatalf("appearance-preserving candidate not ready: %#v", candidate.Conflicts)
	}
	assertAppearanceDefinition(t, candidate.Definition, wantGlobal, wantAttribute, wantScene, wantBlindBox, wantGiftTarget)
}

func TestExporterV2BlindBoxAppearanceIsSkippedWithoutBlockingSupportedGlobalAppearance(t *testing.T) {
	raw, err := os.ReadFile("testdata/online-migration-v2-appearance.json")
	if err != nil {
		t.Fatal(err)
	}
	envelope, _, err := Decode(bytes.NewReader(raw), maximumEnvelopeBytes)
	if err != nil {
		t.Fatal(err)
	}
	compatibility := AssessBlindBoxDisplayCompatibility(envelope.Definition, hostedMigrationCapabilities())
	if compatibility == nil || compatibility.Status != CompatibilityPartial || !sameStrings(compatibility.ReasonCodes, []string{"blind_box_display_unsupported"}) {
		t.Fatalf("production blind-box compatibility = %#v", compatibility)
	}
	selection := SelectionCommand{UnitIDs: []string{"attribute:health", "gift-target:gift-goal"}, IncludeGeneralSettings: true}
	candidate, err := composeCandidate(envelope, emptyDefinition(), emptyRuntime(), hostedMigrationCapabilities(), selection)
	if err != nil || !candidate.Ready {
		t.Fatalf("supported appearance composition = %#v, error = %v", candidate, err)
	}
	if !reflect.DeepEqual(candidate.Definition.Appearance, envelope.Definition.Appearance) {
		t.Fatalf("global appearance = %#v, want %#v", candidate.Definition.Appearance, envelope.Definition.Appearance)
	}
	if candidate.Definition.BlindBoxDisplay != nil {
		t.Fatalf("unsupported blind-box appearance leaked into candidate: %#v", candidate.Definition.BlindBoxDisplay)
	}
}

func assertAppearanceDefinition(t *testing.T, definition configuration.Definition, wantGlobal *configuration.GlobalAppearance, wantAttribute, wantScene, wantBlindBox, wantGiftTarget *configuration.DisplayAppearance) {
	t.Helper()
	if !reflect.DeepEqual(definition.Appearance, wantGlobal) {
		t.Fatalf("global appearance = %#v, want %#v", definition.Appearance, wantGlobal)
	}
	if len(definition.Attributes) != 1 || definition.Attributes[0].Display == nil || !reflect.DeepEqual(definition.Attributes[0].Display.Appearance, wantAttribute) {
		t.Fatalf("attribute appearance = %#v, want %#v", definition.Attributes, wantAttribute)
	}
	if len(definition.DisplayScenes) != 1 || !reflect.DeepEqual(definition.DisplayScenes[0].Appearance, wantScene) {
		t.Fatalf("scene appearance = %#v, want %#v", definition.DisplayScenes, wantScene)
	}
	if !reflect.DeepEqual(definition.BlindBoxDisplay, wantBlindBox) {
		t.Fatalf("blind-box appearance = %#v, want %#v", definition.BlindBoxDisplay, wantBlindBox)
	}
	if len(definition.GiftTargetPanels) != 1 || !reflect.DeepEqual(definition.GiftTargetPanels[0].Appearance, wantGiftTarget) {
		t.Fatalf("gift-target appearance = %#v, want %#v", definition.GiftTargetPanels, wantGiftTarget)
	}
}
