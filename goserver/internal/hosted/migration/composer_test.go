package migration

import (
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
)

func TestComposeSelectionDefaultsSameIDToReplacementAndRetainsUnselectedHostedUnits(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "health", Name: "Health"}, {ID: "shield", Name: "Shield"}},
		map[string]float64{"health": 1, "shield": 2},
	)
	importDefinition, importRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "health", Name: "Health"}},
		map[string]float64{"health": 9},
	)
	imported := migrationEnvelope(importDefinition, importRuntime)

	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), SelectionCommand{UnitIDs: []string{"attribute:health"}})
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.Ready || len(candidate.Conflicts) != 0 || candidate.Hash == ([32]byte{}) {
		t.Fatalf("candidate readiness = %#v", candidate)
	}
	if got, want := candidate.Definition.Attributes, []configuration.AttributeDefinition{{ID: "health", Name: "Health"}, {ID: "shield", Name: "Shield"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attributes = %#v, want %#v", got, want)
	}
	if got, want := candidate.Runtime.AttributeValues, map[string]float64{"health": 9, "shield": 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
}

func TestConflictSelectionBlocksUntilExplicitAndKeepBothSuggestsImportedRename(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "online-health", Name: "Health"}},
		map[string]float64{"online-health": 3},
	)
	importDefinition, importRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "exe-health", Name: "Health"}},
		map[string]float64{"exe-health": 8},
	)
	imported := migrationEnvelope(importDefinition, importRuntime)
	selection := SelectionCommand{UnitIDs: []string{"attribute:exe-health"}}

	blocked, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection)
	if err != nil {
		t.Fatal(err)
	}
	wantConflict := SelectionConflict{
		ID: "attribute:exe-health", ImportedUnitIDs: []string{"attribute:exe-health"}, HostedUnitIDs: []string{"attribute:online-health"},
		SuggestedNames: map[string]string{"attribute:exe-health": "Health（从 EXE 导入）"},
	}
	if blocked.Ready || !reflect.DeepEqual(blocked.Conflicts, []SelectionConflict{wantConflict}) {
		t.Fatalf("blocked candidate = %#v, want conflict %#v", blocked, wantConflict)
	}

	selection.ConflictChoices = map[string]ConflictChoice{"attribute:exe-health": ConflictKeepBoth}
	kept, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := kept.Definition.Attributes, []configuration.AttributeDefinition{{ID: "exe-health", Name: "Health（从 EXE 导入）"}, {ID: "online-health", Name: "Health"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keep-both attributes = %#v, want %#v", got, want)
	}

	selection.ConflictChoices["attribute:exe-health"] = ConflictReplace
	replaced, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := replaced.Runtime.AttributeValues, map[string]float64{"exe-health": 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replace values = %#v, want %#v", got, want)
	}

	selection.ConflictChoices["attribute:exe-health"] = ConflictSkip
	skipped, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := skipped.Runtime.AttributeValues, map[string]float64{"online-health": 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skip values = %#v, want %#v", got, want)
	}
}

func TestConflictSelectionRequiresWholeGroupAndOneGroupWideChoice(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "online-a", Name: "A"}, {ID: "online-b", Name: "B"}},
		map[string]float64{"online-a": 1, "online-b": 2},
	)
	importDefinition, importRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "exe-a", Name: "A"}, {ID: "exe-b", Name: "B"}},
		map[string]float64{"exe-a": 10, "exe-b": 20},
	)
	imported := migrationEnvelope(importDefinition, importRuntime)
	imported.Groups = []GameplayGroup{{ID: "group:linked", UnitIDs: []string{"attribute:exe-a", "attribute:exe-b"}}}

	if _, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), SelectionCommand{UnitIDs: []string{"attribute:exe-a"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("partial group error = %v, want invalid input", err)
	}
	selection := SelectionCommand{
		UnitIDs:         []string{"attribute:exe-a", "attribute:exe-b"},
		ConflictChoices: map[string]ConflictChoice{"attribute:exe-a": ConflictReplace, "attribute:exe-b": ConflictKeepBoth},
	}
	if _, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("per-unit group choices error = %v, want invalid input", err)
	}
	selection.ConflictChoices = map[string]ConflictChoice{"group:linked": ConflictReplace}
	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.Ready || !reflect.DeepEqual(candidate.Runtime.AttributeValues, map[string]float64{"exe-a": 10, "exe-b": 20}) {
		t.Fatalf("group candidate = %#v", candidate)
	}
}

func TestComposeSelectionKeepsGeneralSettingsAndRoomSuggestionIndependent(t *testing.T) {
	imported := migrationEnvelope(attributeConfiguration([]configuration.AttributeDefinition{{ID: "health", Name: "Health"}}, map[string]float64{"health": 9}))
	imported.GeneralSettings = GeneralSettings{ConfigurationMode: "advanced"}
	imported.RoomSuggestion = "12345"

	settingsOnly, err := composeCandidate(imported, emptyDefinition(), emptyRuntime(), completeCapabilities(), SelectionCommand{IncludeGeneralSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	if settingsOnly.GeneralSettings == nil || settingsOnly.GeneralSettings.ConfigurationMode != "advanced" || settingsOnly.RoomSuggestion != "" {
		t.Fatalf("settings-only candidate = %#v", settingsOnly)
	}
	roomOnly, err := composeCandidate(imported, emptyDefinition(), emptyRuntime(), completeCapabilities(), SelectionCommand{IncludeRoomSuggestion: true})
	if err != nil {
		t.Fatal(err)
	}
	if roomOnly.GeneralSettings != nil || roomOnly.RoomSuggestion != "12345" {
		t.Fatalf("room-only candidate = %#v", roomOnly)
	}
}

func TestComposeSelectionEmbedsSelectedSettingsAndStableCropsInCompleteDefinition(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "hosted", Name: "Hosted"}}, map[string]float64{"hosted": 2})
	hostedDefinition.GeneralSettings = &configuration.GeneralSettings{ConfigurationMode: "advanced"}
	hostedDefinition.CropPresets = []configuration.CropPreset{{ID: "gift:2", Crop: configuration.Crop{Width: 1, Height: 1}}}
	importDefinition, importRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	importDefinition.GeneralSettings = &configuration.GeneralSettings{ConfigurationMode: "simple"}
	importDefinition.CropPresets = []configuration.CropPreset{{ID: "gift:1", Crop: configuration.Crop{X: 0.1, Y: 0.2, Width: 0.3, Height: 0.4}}}
	imported := migrationEnvelope(importDefinition, importRuntime)
	imported.GeneralSettings = *importDefinition.GeneralSettings
	imported.CropPresets = append([]CropPreset(nil), importDefinition.CropPresets...)
	imported.Units[0].CropPresetIDs = []string{"gift:1"}

	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), SelectionCommand{UnitIDs: []string{"attribute:exe"}, IncludeGeneralSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	wantCrops := []configuration.CropPreset{
		{ID: "gift:1", Crop: configuration.Crop{X: 0.1, Y: 0.2, Width: 0.3, Height: 0.4}},
		{ID: "gift:2", Crop: configuration.Crop{Width: 1, Height: 1}},
	}
	if candidate.Definition.GeneralSettings == nil || candidate.Definition.GeneralSettings.ConfigurationMode != "simple" || !reflect.DeepEqual(candidate.Definition.CropPresets, wantCrops) {
		t.Fatalf("candidate definition metadata=%#v %#v", candidate.Definition.GeneralSettings, candidate.Definition.CropPresets)
	}
	if candidate.GeneralSettings == nil || !reflect.DeepEqual(candidate.CropPresets, wantCrops) {
		t.Fatalf("candidate projection metadata=%#v %#v", candidate.GeneralSettings, candidate.CropPresets)
	}
	if candidate.Definition.MigrationHash != hex.EncodeToString(candidate.Hash[:]) {
		t.Fatalf("persisted migration hash=%q candidate=%x", candidate.Definition.MigrationHash, candidate.Hash)
	}

	retained, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), SelectionCommand{UnitIDs: []string{"attribute:exe"}})
	if err != nil {
		t.Fatal(err)
	}
	if retained.Definition.GeneralSettings == nil || retained.Definition.GeneralSettings.ConfigurationMode != "advanced" {
		t.Fatalf("unselected hosted settings were not retained: %#v", retained.Definition.GeneralSettings)
	}
}

func TestComposeSelectionRejectsSelectedPartialOrIncompatibleUnit(t *testing.T) {
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "health", Name: "Health"}}, map[string]float64{"health": 9})
	imported := migrationEnvelope(definition, runtime)
	imported.Units[0].DisplaySceneIDs = []string{"scene"}
	partial := completeCapabilities()
	partial.DisplayScenesSupported = false
	selection := SelectionCommand{UnitIDs: []string{"attribute:health"}}
	if _, err := composeCandidate(imported, emptyDefinition(), emptyRuntime(), partial, selection); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial selection error = %v, want conflict", err)
	}
	incompatible := completeCapabilities()
	incompatible.UnitKinds["attribute"] = false
	if _, err := composeCandidate(imported, emptyDefinition(), emptyRuntime(), incompatible, selection); !errors.Is(err, ErrConflict) {
		t.Fatalf("incompatible selection error = %v, want conflict", err)
	}
}

func TestComposeSelectionBlocksDifferentCrossSourceSharedDependencyInsteadOfOverwritingHosted(t *testing.T) {
	hostedDefinition, hostedRuntime := activityConfiguration("host", "Hosted activity", "Hosted shared", 1)
	importDefinition, importRuntime := activityConfiguration("import", "Imported activity", "Imported shared", 9)
	imported := migrationEnvelope(importDefinition, importRuntime)

	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), SelectionCommand{UnitIDs: []string{"activity:import"}})
	if err != nil {
		t.Fatal(err)
	}
	want := SelectionConflict{ID: "resource:attribute:shared", ImportedUnitIDs: []string{"activity:import"}, HostedUnitIDs: []string{"activity:host"}}
	if candidate.Ready || !reflect.DeepEqual(candidate.Conflicts, []SelectionConflict{want}) {
		t.Fatalf("shared dependency candidate=%#v want blocking conflict=%#v", candidate, want)
	}
}

func TestComposeSelectionAllowsIdenticalCrossSourceSharedDependency(t *testing.T) {
	hostedDefinition, hostedRuntime := activityConfiguration("host", "Hosted activity", "Shared", 1)
	importDefinition, importRuntime := activityConfiguration("import", "Imported activity", "Shared", 1)
	imported := migrationEnvelope(importDefinition, importRuntime)

	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), SelectionCommand{UnitIDs: []string{"activity:import"}})
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.Ready || len(candidate.Definition.Attributes) != 1 || candidate.Runtime.AttributeValues["shared"] != 1 {
		t.Fatalf("identical shared dependency candidate=%#v", candidate)
	}
}

func TestConflictKeepBothUsesDeterministicUnoccupiedSuggestedName(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "online", Name: "Health"}, {ID: "occupied", Name: "Health（从 EXE 导入）"}},
		map[string]float64{"online": 1, "occupied": 2},
	)
	importDefinition, importRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "Health"}}, map[string]float64{"exe": 9})
	imported := migrationEnvelope(importDefinition, importRuntime)
	selection := SelectionCommand{UnitIDs: []string{"attribute:exe"}, ConflictChoices: map[string]ConflictChoice{"attribute:exe": ConflictKeepBoth}}

	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if got := candidate.Conflicts[0].SuggestedNames["attribute:exe"]; got != "Health（从 EXE 导入 2）" {
		t.Fatalf("suggested name=%q want deterministic free name", got)
	}
	if got := candidate.Definition.Attributes[0].Name; got != "Health（从 EXE 导入 2）" {
		t.Fatalf("kept imported name=%q", got)
	}
}

func TestConflictKeepBothIgnoresNamesFromUnselectedImportsOutsideCompleteCandidate(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "online", Name: "Health"}}, map[string]float64{"online": 1})
	importDefinition, importRuntime := attributeConfiguration(
		[]configuration.AttributeDefinition{{ID: "exe", Name: "Health"}, {ID: "unselected", Name: "Health（从 EXE 导入）"}},
		map[string]float64{"exe": 9, "unselected": 4},
	)
	imported := migrationEnvelope(importDefinition, importRuntime)
	selection := SelectionCommand{UnitIDs: []string{"attribute:exe"}, ConflictChoices: map[string]ConflictChoice{"attribute:exe": ConflictKeepBoth}}

	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, completeCapabilities(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if got := candidate.Conflicts[0].SuggestedNames["attribute:exe"]; got != "Health（从 EXE 导入）" {
		t.Fatalf("suggested name=%q included an unselected import", got)
	}
}

func TestConflictKeepBothInitializesNilSimplePlayParametersBeforeRename(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "online", Name: "resource"}}, map[string]float64{"online": 1})
	importDefinition, importRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	importDefinition.SimplePlay = &gameplay.SimplePlay{Version: 1, TemplateID: "resource", TemplateVersion: 1, AttributeID: "exe"}
	imported := migrationEnvelope(importDefinition, importRuntime)
	selection := SelectionCommand{UnitIDs: []string{"simple-play:exe"}, ConflictChoices: map[string]ConflictChoice{"simple-play:exe": ConflictKeepBoth}}
	capabilities := completeCapabilities()
	capabilities.SimplePlayTemplates["resource"] = 1

	candidate, err := composeCandidate(imported, hostedDefinition, hostedRuntime, capabilities, selection)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.Ready || candidate.Definition.SimplePlay == nil || candidate.Definition.SimplePlay.Parameters["name"] != "resource（从 EXE 导入）" {
		t.Fatalf("renamed simple play=%#v", candidate.Definition.SimplePlay)
	}
}

func migrationEnvelope(definition configuration.Definition, runtime configuration.RuntimeState) Envelope {
	return Envelope{Definition: definition, Runtime: runtime, Units: DeriveUnits(definition, runtime), Groups: ConnectedGroups(DeriveUnits(definition, runtime))}
}

func attributeConfiguration(definition []configuration.AttributeDefinition, values map[string]float64) (configuration.Definition, configuration.RuntimeState) {
	return configuration.Definition{Attributes: definition}, configuration.RuntimeState{
		AttributeValues: values, GiftTargetReceived: []configuration.GiftTargetRuntimeState{}, Activities: []configuration.ActivityRuntimeState{},
		RuleLimits: gameplay.RuleLimitState{AppliedCounts: map[string]int{}},
	}
}

func activityConfiguration(activityID, activityName, attributeName string, value float64) (configuration.Definition, configuration.RuntimeState) {
	return configuration.Definition{
			Attributes: []configuration.AttributeDefinition{{ID: "shared", Name: attributeName}},
			Activities: []configuration.ActivityDefinition{{ID: activityID, Name: activityName, AttributeIDs: []string{"shared"}}},
		}, configuration.RuntimeState{
			AttributeValues: map[string]float64{"shared": value}, GiftTargetReceived: []configuration.GiftTargetRuntimeState{},
			Activities: []configuration.ActivityRuntimeState{{ID: activityID, Status: "not_started", Milestones: []configuration.MilestoneRuntimeState{}}},
			RuleLimits: gameplay.RuleLimitState{AppliedCounts: map[string]int{}},
		}
}

func emptyDefinition() configuration.Definition { return configuration.Definition{} }
func emptyRuntime() configuration.RuntimeState {
	return configuration.RuntimeState{AttributeValues: map[string]float64{}, GiftTargetReceived: []configuration.GiftTargetRuntimeState{}, Activities: []configuration.ActivityRuntimeState{}, RuleLimits: gameplay.RuleLimitState{AppliedCounts: map[string]int{}}}
}

func completeCapabilities() CapabilitySet {
	return CapabilitySet{
		UnitKinds:           map[string]bool{"attribute": true, "activity": true, "gift-target": true, "simple-play": true},
		SimplePlayTemplates: map[string]int{}, RulesSupported: true, TimerRulesSupported: true, FormulaPresetsSupported: true,
		DisplayScenesSupported: true, CropPresetsSupported: true,
	}
}
