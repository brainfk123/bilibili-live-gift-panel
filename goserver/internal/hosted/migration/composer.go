package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"
	"strconv"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
)

type ConflictChoice string

const (
	ConflictReplace  ConflictChoice = "replace"
	ConflictKeepBoth ConflictChoice = "keep_both"
	ConflictSkip     ConflictChoice = "skip"
)

// SelectionCommand contains only user choices. Unit boundaries, groups,
// compatibility and conflicts are always recalculated from server-owned data.
type SelectionCommand struct {
	UnitIDs                []string                  `json:"unitIds"`
	ConflictChoices        map[string]ConflictChoice `json:"conflictChoices,omitempty"`
	IncludeGeneralSettings bool                      `json:"includeGeneralSettings"`
	IncludeRoomSuggestion  bool                      `json:"includeRoomSuggestion"`
}

type SelectionConflict struct {
	ID              string            `json:"id"`
	ImportedUnitIDs []string          `json:"importedUnitIds"`
	HostedUnitIDs   []string          `json:"hostedUnitIds"`
	SuggestedNames  map[string]string `json:"suggestedNames,omitempty"`
}

type SelectionUnit struct {
	GameplayUnit
	Compatibility Compatibility `json:"compatibility"`
	Selected      bool          `json:"selected"`
}

// ComposeCandidate is a full, normalized account configuration assembled from
// retained Hosted gameplay and selected EXE gameplay. It is never serialized
// by the preview HTTP API.
type ComposeCandidate struct {
	Definition      configuration.Definition   `json:"-"`
	Runtime         configuration.RuntimeState `json:"-"`
	GeneralSettings *GeneralSettings           `json:"-"`
	CropPresets     []CropPreset               `json:"-"`
	RoomSuggestion  string                     `json:"-"`
	Conflicts       []SelectionConflict        `json:"conflicts,omitempty"`
	Ready           bool                       `json:"ready"`
	Hash            [sha256.Size]byte          `json:"-"`
}

func composeCandidate(imported Envelope, hostedDefinition configuration.Definition, hostedRuntime configuration.RuntimeState, capabilities CapabilitySet, selection SelectionCommand) (ComposeCandidate, error) {
	selected, err := validateSelection(imported.Units, imported.Groups, selection)
	if err != nil {
		return ComposeCandidate{}, err
	}
	for _, unit := range imported.Units {
		if _, ok := selected[unit.ID]; ok && AssessCompatibility(unit, capabilities).Status != CompatibilityComplete {
			return ComposeCandidate{}, ErrConflict
		}
	}
	hostedUnits := DeriveUnits(hostedDefinition, hostedRuntime)
	conflicts, conflictImports, conflictHosted := findSelectionConflicts(imported.Units, imported.Groups, hostedUnits, selected)
	allowedChoices := make(map[string]struct{}, len(conflicts))
	for _, conflict := range conflicts {
		allowedChoices[conflict.ID] = struct{}{}
	}
	for id, choice := range selection.ConflictChoices {
		if _, ok := allowedChoices[id]; !ok || !validConflictChoice(choice) {
			return ComposeCandidate{}, ErrInvalidInput
		}
	}

	result := ComposeCandidate{Conflicts: conflicts}
	for _, conflict := range conflicts {
		if _, explicit := selection.ConflictChoices[conflict.ID]; !explicit {
			return result, nil
		}
	}
	result.Ready = true

	removeHosted := map[string]struct{}{}
	for _, importedUnit := range imported.Units {
		if _, ok := selected[importedUnit.ID]; !ok {
			continue
		}
		for _, hostedUnit := range hostedUnits {
			if importedUnit.ID == hostedUnit.ID {
				removeHosted[hostedUnit.ID] = struct{}{}
			}
		}
	}
	skipImported := map[string]struct{}{}
	renameImported := map[string]string{}
	for _, conflict := range conflicts {
		choice := selection.ConflictChoices[conflict.ID]
		switch choice {
		case ConflictReplace:
			for _, id := range conflictHosted[conflict.ID] {
				removeHosted[id] = struct{}{}
			}
		case ConflictKeepBoth:
			for id, name := range conflict.SuggestedNames {
				renameImported[id] = name
			}
		case ConflictSkip:
			for _, id := range conflictImports[conflict.ID] {
				skipImported[id] = struct{}{}
			}
		}
	}
	retainedHostedUnits := make([]GameplayUnit, 0, len(hostedUnits))
	for _, unit := range hostedUnits {
		if _, remove := removeHosted[unit.ID]; !remove {
			retainedHostedUnits = append(retainedHostedUnits, unit)
		}
	}
	selectedImportedUnits := make([]GameplayUnit, 0, len(imported.Units))
	for _, unit := range imported.Units {
		if _, ok := selected[unit.ID]; !ok {
			continue
		}
		if _, skip := skipImported[unit.ID]; !skip {
			selectedImportedUnits = append(selectedImportedUnits, unit)
		}
	}
	if resourceConflicts := findCrossSourceResourceConflicts(hostedDefinition, hostedRuntime, retainedHostedUnits, imported.Definition, imported.Runtime, selectedImportedUnits); len(resourceConflicts) != 0 {
		result.Conflicts = append(result.Conflicts, resourceConflicts...)
		result.Ready = false
		return result, nil
	}

	assembled := newAssembly()
	for _, unit := range retainedHostedUnits {
		assembled.add(hostedDefinition, hostedRuntime, unit, "")
	}
	selectedCropIDs := map[string]struct{}{}
	for _, unit := range imported.Units {
		if _, ok := selected[unit.ID]; !ok {
			continue
		}
		if _, skip := skipImported[unit.ID]; skip {
			continue
		}
		assembled.add(imported.Definition, imported.Runtime, unit, renameImported[unit.ID])
		for _, id := range unit.CropPresetIDs {
			selectedCropIDs[id] = struct{}{}
		}
	}
	definition, runtime := assembled.values()
	definition.Appearance = cloneGlobalAppearance(hostedDefinition.Appearance)
	definition.BlindBoxDisplay = cloneDisplayAppearance(hostedDefinition.BlindBoxDisplay)
	if hostedDefinition.GeneralSettings != nil {
		settings := *hostedDefinition.GeneralSettings
		definition.GeneralSettings = &settings
	}
	if selection.IncludeGeneralSettings {
		settings := imported.GeneralSettings
		if imported.Definition.GeneralSettings != nil {
			settings = *imported.Definition.GeneralSettings
		}
		definition.GeneralSettings = &settings
		definition.Appearance = cloneGlobalAppearance(imported.Definition.Appearance)
		if capabilities.BlindBoxDisplaySupported {
			definition.BlindBoxDisplay = cloneDisplayAppearance(imported.Definition.BlindBoxDisplay)
		}
		result.GeneralSettings = &settings
	}
	importedCrops := selectedCrops(imported.CropPresets, selectedCropIDs)
	completeCrops, cropConflict := mergeCandidateCrops(hostedDefinition.CropPresets, importedCrops, selectedImportedUnits)
	if cropConflict != nil {
		result.Conflicts = append(result.Conflicts, *cropConflict)
		result.Ready = false
		return result, nil
	}
	definition.CropPresets = completeCrops
	definition, runtime, _, hash, err := freshCanonical(definition, runtime)
	if err != nil {
		return ComposeCandidate{}, ErrConflict
	}
	result.Definition, result.Runtime, result.Hash = definition, runtime, hash
	result.Definition.MigrationHash = hex.EncodeToString(hash[:])
	result.CropPresets = append([]CropPreset(nil), definition.CropPresets...)
	if selection.IncludeRoomSuggestion {
		result.RoomSuggestion = imported.RoomSuggestion
	}
	return result, nil
}

func cloneGlobalAppearance(appearance *configuration.GlobalAppearance) *configuration.GlobalAppearance {
	if appearance == nil {
		return nil
	}
	copy := *appearance
	return &copy
}

func cloneDisplayAppearance(appearance *configuration.DisplayAppearance) *configuration.DisplayAppearance {
	if appearance == nil {
		return nil
	}
	copy := *appearance
	return &copy
}

func mergeCandidateCrops(hosted, imported []CropPreset, importedUnits []GameplayUnit) ([]CropPreset, *SelectionConflict) {
	byID := make(map[string]CropPreset, len(hosted)+len(imported))
	for _, preset := range hosted {
		byID[preset.ID] = preset
	}
	for _, preset := range imported {
		if existing, ok := byID[preset.ID]; ok && !reflect.DeepEqual(existing, preset) {
			owners := make(map[string]struct{})
			for _, unit := range importedUnits {
				if containsString(unit.CropPresetIDs, preset.ID) {
					owners[unit.ID] = struct{}{}
				}
			}
			return nil, &SelectionConflict{ID: "resource:crop-preset:" + preset.ID, ImportedUnitIDs: sortedMapKeys(owners), HostedUnitIDs: []string{"hosted:definition"}}
		}
		byID[preset.ID] = preset
	}
	result := make([]CropPreset, 0, len(byID))
	for _, preset := range byID {
		result = append(result, preset)
	}
	sort.Slice(result, func(left, right int) bool { return compareCodeUnits(result[left].ID, result[right].ID) < 0 })
	return result, nil
}

type unitResource struct {
	kind, id string
	value    any
}

type ownedResource struct {
	value  any
	owners map[string]struct{}
}

func findCrossSourceResourceConflicts(hostedDefinition configuration.Definition, hostedRuntime configuration.RuntimeState, hostedUnits []GameplayUnit, importedDefinition configuration.Definition, importedRuntime configuration.RuntimeState, importedUnits []GameplayUnit) []SelectionConflict {
	hosted := map[string]*ownedResource{}
	for _, unit := range hostedUnits {
		for _, resource := range resourcesForUnit(hostedDefinition, hostedRuntime, unit) {
			key := resource.kind + ":" + resource.id
			if hosted[key] == nil {
				hosted[key] = &ownedResource{value: resource.value, owners: map[string]struct{}{}}
			}
			hosted[key].owners[unit.ID] = struct{}{}
		}
	}
	type conflictDraft struct {
		kind, id string
		imports  map[string]struct{}
		hosted   map[string]struct{}
	}
	drafts := map[string]*conflictDraft{}
	for _, unit := range importedUnits {
		for _, resource := range resourcesForUnit(importedDefinition, importedRuntime, unit) {
			key := resource.kind + ":" + resource.id
			existing := hosted[key]
			if existing == nil || reflect.DeepEqual(existing.value, resource.value) {
				continue
			}
			if drafts[key] == nil {
				drafts[key] = &conflictDraft{kind: resource.kind, id: resource.id, imports: map[string]struct{}{}, hosted: map[string]struct{}{}}
			}
			drafts[key].imports[unit.ID] = struct{}{}
			for owner := range existing.owners {
				drafts[key].hosted[owner] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(drafts))
	for key := range drafts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return compareCodeUnits(keys[i], keys[j]) < 0 })
	result := make([]SelectionConflict, 0, len(keys))
	for _, key := range keys {
		draft := drafts[key]
		result = append(result, SelectionConflict{ID: "resource:" + draft.kind + ":" + draft.id, ImportedUnitIDs: sortedMapKeys(draft.imports), HostedUnitIDs: sortedMapKeys(draft.hosted)})
	}
	return result
}

func resourcesForUnit(definition configuration.Definition, runtime configuration.RuntimeState, unit GameplayUnit) []unitResource {
	result := make([]unitResource, 0, len(unit.AttributeIDs)+len(unit.RuleIDs)+len(unit.TimerRuleIDs)+len(unit.FormulaPresetIDs)+len(unit.ActivityIDs)+len(unit.DisplaySceneIDs)+len(unit.GiftTargetPanelIDs)+len(unit.GiftIDs)+1)
	for _, value := range definition.Attributes {
		if containsString(unit.AttributeIDs, value.ID) {
			result = append(result, unitResource{kind: "attribute", id: value.ID, value: struct {
				Definition configuration.AttributeDefinition
				Value      float64
			}{value, runtime.AttributeValues[value.ID]}})
		}
	}
	for _, value := range definition.DisplayScenes {
		if containsString(unit.DisplaySceneIDs, value.ID) {
			result = append(result, unitResource{kind: "scene", id: value.ID, value: value})
		}
	}
	for _, value := range definition.GiftTargetPanels {
		if !containsString(unit.GiftTargetPanelIDs, value.ID) {
			continue
		}
		states := make([]configuration.GiftTargetRuntimeState, 0)
		for _, state := range runtime.GiftTargetReceived {
			if state.PanelID == value.ID {
				states = append(states, state)
			}
		}
		result = append(result, unitResource{kind: "panel", id: value.ID, value: struct {
			Definition configuration.GiftTargetPanelDefinition
			Runtime    []configuration.GiftTargetRuntimeState
		}{value, states}})
	}
	for _, value := range definition.Activities {
		if !containsString(unit.ActivityIDs, value.ID) {
			continue
		}
		var state configuration.ActivityRuntimeState
		for _, candidate := range runtime.Activities {
			if candidate.ID == value.ID {
				state = candidate
				break
			}
		}
		result = append(result, unitResource{kind: "activity", id: value.ID, value: struct {
			Definition configuration.ActivityDefinition
			Runtime    configuration.ActivityRuntimeState
		}{value, state}})
	}
	for _, value := range definition.Rules {
		if containsString(unit.RuleIDs, value.ID) {
			result = append(result, unitResource{kind: "rule", id: value.ID, value: value})
		}
	}
	for _, value := range definition.TimerRules {
		if containsString(unit.TimerRuleIDs, value.ID) {
			result = append(result, unitResource{kind: "timer-rule", id: value.ID, value: value})
		}
	}
	for _, id := range sortedUniqueStrings(append(append([]string(nil), unit.RuleIDs...), unit.TimerRuleIDs...)) {
		count, present := runtime.RuleLimits.AppliedCounts[id]
		result = append(result, unitResource{kind: "rule-limit-count", id: id, value: struct {
			Count   int
			Present bool
		}{count, present}})
	}
	if len(unit.RuleIDs) != 0 || len(unit.TimerRuleIDs) != 0 {
		result = append(result, unitResource{kind: "rule-limits-local-date", id: "singleton", value: runtime.RuleLimits.LocalDate})
	}
	for _, value := range definition.FormulaPresets {
		if containsString(unit.FormulaPresetIDs, value.ID) {
			result = append(result, unitResource{kind: "formula-preset", id: value.ID, value: value})
		}
	}
	for _, value := range definition.Gifts {
		if containsInt(unit.GiftIDs, value.ID) {
			result = append(result, unitResource{kind: "gift", id: strconv.Itoa(value.ID), value: value})
		}
	}
	if unit.Kind == "simple-play" && definition.SimplePlay != nil {
		result = append(result, unitResource{kind: "simple-play", id: "singleton", value: *definition.SimplePlay})
	}
	return result
}

func validateSelection(units []GameplayUnit, groups []GameplayGroup, selection SelectionCommand) (map[string]struct{}, error) {
	known := make(map[string]struct{}, len(units))
	for _, unit := range units {
		if unit.ID == "" {
			return nil, ErrInvalidInput
		}
		known[unit.ID] = struct{}{}
	}
	selected := make(map[string]struct{}, len(selection.UnitIDs))
	for _, id := range selection.UnitIDs {
		if _, ok := known[id]; !ok {
			return nil, ErrInvalidInput
		}
		if _, duplicate := selected[id]; duplicate {
			return nil, ErrInvalidInput
		}
		selected[id] = struct{}{}
	}
	for _, group := range groups {
		count := 0
		for _, id := range group.UnitIDs {
			if _, ok := selected[id]; ok {
				count++
			}
		}
		if count != 0 && count != len(group.UnitIDs) {
			return nil, ErrInvalidInput
		}
	}
	return selected, nil
}

func findSelectionConflicts(importedUnits []GameplayUnit, groups []GameplayGroup, hostedUnits []GameplayUnit, selected map[string]struct{}) ([]SelectionConflict, map[string][]string, map[string][]string) {
	occupiedNames := make(map[string]struct{}, len(importedUnits)+len(hostedUnits))
	for _, unit := range hostedUnits {
		occupiedNames[unit.Name] = struct{}{}
	}
	for _, unit := range importedUnits {
		if _, ok := selected[unit.ID]; ok {
			occupiedNames[unit.Name] = struct{}{}
		}
	}
	groupByUnit := map[string]string{}
	groupMembers := map[string][]string{}
	for _, group := range groups {
		groupMembers[group.ID] = append([]string(nil), group.UnitIDs...)
		for _, id := range group.UnitIDs {
			groupByUnit[id] = group.ID
		}
	}
	type draft struct {
		imports, hosted map[string]struct{}
		suggestions     map[string]string
	}
	drafts := map[string]*draft{}
	for _, imported := range importedUnits {
		if _, ok := selected[imported.ID]; !ok {
			continue
		}
		for _, hosted := range hostedUnits {
			if imported.ID == hosted.ID || imported.Name != hosted.Name {
				continue
			}
			key := imported.ID
			if groupByUnit[imported.ID] != "" {
				key = groupByUnit[imported.ID]
			}
			if drafts[key] == nil {
				drafts[key] = &draft{imports: map[string]struct{}{}, hosted: map[string]struct{}{}, suggestions: map[string]string{}}
			}
			drafts[key].imports[imported.ID] = struct{}{}
			drafts[key].hosted[hosted.ID] = struct{}{}
			if _, exists := drafts[key].suggestions[imported.ID]; !exists {
				suggestion := availableImportedName(imported.Name, occupiedNames)
				drafts[key].suggestions[imported.ID] = suggestion
				occupiedNames[suggestion] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(drafts))
	for key := range drafts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return compareCodeUnits(keys[i], keys[j]) < 0 })
	conflicts := make([]SelectionConflict, 0, len(keys))
	conflictImports, conflictHosted := map[string][]string{}, map[string][]string{}
	for _, key := range keys {
		draft := drafts[key]
		imports := sortedMapKeys(draft.imports)
		if members := groupMembers[key]; len(members) != 0 {
			imports = sortedUniqueStrings(members)
		}
		hosted := sortedMapKeys(draft.hosted)
		conflictImports[key], conflictHosted[key] = imports, hosted
		conflicts = append(conflicts, SelectionConflict{ID: key, ImportedUnitIDs: imports, HostedUnitIDs: hosted, SuggestedNames: draft.suggestions})
	}
	return conflicts, conflictImports, conflictHosted
}

func availableImportedName(name string, occupied map[string]struct{}) string {
	base := name + "（从 EXE 导入）"
	if _, exists := occupied[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := name + "（从 EXE 导入 " + strconv.Itoa(suffix) + "）"
		if _, exists := occupied[candidate]; !exists {
			return candidate
		}
	}
}

func validConflictChoice(choice ConflictChoice) bool {
	return choice == ConflictReplace || choice == ConflictKeepBoth || choice == ConflictSkip
}

type configurationAssembly struct {
	attributes      map[string]configuration.AttributeDefinition
	scenes          map[string]gameplay.DisplayScene
	panels          map[string]configuration.GiftTargetPanelDefinition
	activities      map[string]configuration.ActivityDefinition
	rules           map[string]gameplay.Rule
	timers          map[string]gameplay.TimerRule
	presets         map[string]gameplay.FormulaPreset
	gifts           map[int]configuration.GiftDefinition
	attributeValues map[string]float64
	targets         map[string]configuration.GiftTargetRuntimeState
	states          map[string]configuration.ActivityRuntimeState
	limits          gameplay.RuleLimitState
	simple          *gameplay.SimplePlay
}

func newAssembly() *configurationAssembly {
	return &configurationAssembly{
		attributes: map[string]configuration.AttributeDefinition{}, scenes: map[string]gameplay.DisplayScene{}, panels: map[string]configuration.GiftTargetPanelDefinition{},
		activities: map[string]configuration.ActivityDefinition{}, rules: map[string]gameplay.Rule{}, timers: map[string]gameplay.TimerRule{}, presets: map[string]gameplay.FormulaPreset{},
		gifts: map[int]configuration.GiftDefinition{}, attributeValues: map[string]float64{}, targets: map[string]configuration.GiftTargetRuntimeState{}, states: map[string]configuration.ActivityRuntimeState{},
		limits: gameplay.RuleLimitState{AppliedCounts: map[string]int{}},
	}
}

func (assembly *configurationAssembly) add(definition configuration.Definition, runtime configuration.RuntimeState, unit GameplayUnit, rename string) {
	for _, value := range definition.Attributes {
		if containsString(unit.AttributeIDs, value.ID) {
			if rename != "" && unit.Kind == "attribute" && len(unit.AttributeIDs) == 1 {
				value.Name = rename
			}
			assembly.attributes[value.ID] = value
			assembly.attributeValues[value.ID] = runtime.AttributeValues[value.ID]
		}
	}
	for _, value := range definition.DisplayScenes {
		if containsString(unit.DisplaySceneIDs, value.ID) {
			assembly.scenes[value.ID] = value
		}
	}
	for _, value := range definition.GiftTargetPanels {
		if containsString(unit.GiftTargetPanelIDs, value.ID) {
			if rename != "" && unit.Kind == "gift-target" {
				value.Name = rename
			}
			assembly.panels[value.ID] = value
		}
	}
	for _, state := range runtime.GiftTargetReceived {
		if containsString(unit.GiftTargetPanelIDs, state.PanelID) {
			assembly.targets[state.PanelID+":"+strconv.Itoa(state.GiftID)] = state
		}
	}
	for _, value := range definition.Activities {
		if containsString(unit.ActivityIDs, value.ID) {
			if rename != "" && unit.Kind == "activity" {
				value.Name = rename
			}
			assembly.activities[value.ID] = value
		}
	}
	for _, state := range runtime.Activities {
		if containsString(unit.ActivityIDs, state.ID) {
			assembly.states[state.ID] = state
		}
	}
	for _, value := range definition.Rules {
		if containsString(unit.RuleIDs, value.ID) {
			assembly.rules[value.ID] = value
		}
	}
	for _, value := range definition.TimerRules {
		if containsString(unit.TimerRuleIDs, value.ID) {
			assembly.timers[value.ID] = value
		}
	}
	for _, id := range sortedUniqueStrings(append(append([]string(nil), unit.RuleIDs...), unit.TimerRuleIDs...)) {
		if count, ok := runtime.RuleLimits.AppliedCounts[id]; ok {
			assembly.limits.AppliedCounts[id] = count
		}
	}
	for _, value := range definition.FormulaPresets {
		if containsString(unit.FormulaPresetIDs, value.ID) {
			assembly.presets[value.ID] = value
		}
	}
	for _, value := range definition.Gifts {
		if containsInt(unit.GiftIDs, value.ID) {
			assembly.gifts[value.ID] = value
		}
	}
	if (len(unit.RuleIDs) != 0 || len(unit.TimerRuleIDs) != 0) && runtime.RuleLimits.LocalDate != "" {
		assembly.limits.LocalDate = runtime.RuleLimits.LocalDate
	}
	if unit.Kind == "simple-play" && definition.SimplePlay != nil {
		copy := *definition.SimplePlay
		if definition.SimplePlay.Parameters != nil {
			copy.Parameters = make(map[string]any, len(definition.SimplePlay.Parameters))
			for key, value := range definition.SimplePlay.Parameters {
				copy.Parameters[key] = value
			}
		}
		if rename != "" {
			if copy.Parameters == nil {
				copy.Parameters = map[string]any{}
			}
			copy.Parameters["name"] = rename
		}
		assembly.simple = &copy
	}
}

func (assembly *configurationAssembly) values() (configuration.Definition, configuration.RuntimeState) {
	definition := configuration.Definition{
		Attributes: mapValues(assembly.attributes), DisplayScenes: mapValues(assembly.scenes), GiftTargetPanels: mapValues(assembly.panels), Activities: mapValues(assembly.activities),
		Rules: mapValues(assembly.rules), TimerRules: mapValues(assembly.timers), FormulaPresets: mapValues(assembly.presets), Gifts: intMapValues(assembly.gifts), SimplePlay: assembly.simple,
	}
	runtime := configuration.RuntimeState{
		AttributeValues: assembly.attributeValues, GiftTargetReceived: mapValues(assembly.targets), Activities: mapValues(assembly.states), RuleLimits: assembly.limits,
	}
	return definition, runtime
}

func mapValues[T any](values map[string]T) []T {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return compareCodeUnits(keys[i], keys[j]) < 0 })
	result := make([]T, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func intMapValues[T any](values map[int]T) []T {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	result := make([]T, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func selectedCrops(crops []CropPreset, selected map[string]struct{}) []CropPreset {
	result := make([]CropPreset, 0, len(crops))
	for _, crop := range crops {
		if _, ok := selected[crop.ID]; ok {
			result = append(result, crop)
		}
	}
	sort.Slice(result, func(i, j int) bool { return compareCodeUnits(result[i].ID, result[j].ID) < 0 })
	return result
}
