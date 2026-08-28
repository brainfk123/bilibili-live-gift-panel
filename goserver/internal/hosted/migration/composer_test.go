package migration

import (
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

func migrationEnvelope(definition configuration.Definition, runtime configuration.RuntimeState) Envelope {
	return Envelope{Definition: definition, Runtime: runtime, Units: DeriveUnits(definition, runtime), Groups: ConnectedGroups(DeriveUnits(definition, runtime))}
}

func attributeConfiguration(definition []configuration.AttributeDefinition, values map[string]float64) (configuration.Definition, configuration.RuntimeState) {
	return configuration.Definition{Attributes: definition}, configuration.RuntimeState{
		AttributeValues: values, GiftTargetReceived: []configuration.GiftTargetRuntimeState{}, Activities: []configuration.ActivityRuntimeState{},
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
