package migration

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
)

const gameplayDependencyAlgorithmVersion = 1

type GameplayUnit struct {
	ID                        string   `json:"id"`
	Kind                      string   `json:"kind"`
	Name                      string   `json:"name"`
	AttributeIDs              []string `json:"attributeIds"`
	RuleIDs                   []string `json:"ruleIds"`
	TimerRuleIDs              []string `json:"timerRuleIds"`
	FormulaPresetIDs          []string `json:"formulaPresetIds"`
	ActivityIDs               []string `json:"activityIds"`
	DisplaySceneIDs           []string `json:"displaySceneIds"`
	GiftTargetPanelIDs        []string `json:"giftTargetPanelIds"`
	GiftIDs                   []int    `json:"giftIds"`
	CropPresetIDs             []string `json:"cropPresetIds"`
	SimplePlayTemplateID      string   `json:"-"`
	SimplePlayTemplateVersion int      `json:"-"`
}

type GameplayGroupReason struct {
	Kind        string `json:"kind"`
	ReferenceID string `json:"referenceId"`
}

type GameplayGroup struct {
	ID      string                `json:"id"`
	UnitIDs []string              `json:"unitIds"`
	Reasons []GameplayGroupReason `json:"reasons"`
}

type GameplayDependencyDeclaration struct {
	AlgorithmVersion int             `json:"algorithmVersion"`
	Units            []GameplayUnit  `json:"units"`
	Groups           []GameplayGroup `json:"groups"`
}

type unitDraft struct {
	id           string
	kind         string
	name         string
	attributeIDs []string
	activityIDs  []string
	panelIDs     []string
	simplePlay   bool
}

type groupReasonCandidate struct {
	kind  string
	id    string
	units []string
}

var formulaIdentifier = regexp.MustCompile(`[\p{L}_][\p{L}\p{N}_]*`)

// DeriveUnits independently rebuilds gameplay selection boundaries from the
// normalized allowlist. It deliberately accepts no desktop declaration.
func DeriveUnits(definition configuration.Definition, runtime configuration.RuntimeState) []GameplayUnit {
	return deriveUnits(definition, runtime, nil, nil)
}

func deriveUnits(definition configuration.Definition, _ configuration.RuntimeState, crops []CropPreset, effectIDs map[int]int) []GameplayUnit {
	attributesByID := make(map[string]configuration.AttributeDefinition, len(definition.Attributes))
	attributesByName := make(map[string]string, len(definition.Attributes))
	for _, attribute := range definition.Attributes {
		attributesByID[attribute.ID] = attribute
		if _, exists := attributesByName[attribute.Name]; !exists {
			attributesByName[attribute.Name] = attribute.ID
		}
	}
	claimed := make(map[string]struct{}, len(definition.Attributes))
	drafts := make([]unitDraft, 0, len(definition.Attributes)+len(definition.Activities)+len(definition.GiftTargetPanels)+1)
	if simple := definition.SimplePlay; simple != nil {
		if _, exists := attributesByID[simple.AttributeID]; exists {
			claimed[simple.AttributeID] = struct{}{}
			name := strings.TrimSpace(stringParameter(simple.Parameters, "name"))
			if name == "" {
				name = simple.TemplateID
			}
			drafts = append(drafts, unitDraft{id: "simple-play:" + simple.AttributeID, kind: "simple-play", name: name, attributeIDs: []string{simple.AttributeID}, simplePlay: true})
		}
	}
	for _, activity := range definition.Activities {
		ids := sortedUniqueStrings(filterKnownIDs(activity.AttributeIDs, attributesByID))
		for _, id := range ids {
			claimed[id] = struct{}{}
		}
		drafts = append(drafts, unitDraft{id: "activity:" + activity.ID, kind: "activity", name: activity.Name, attributeIDs: ids, activityIDs: []string{activity.ID}})
	}
	for _, attribute := range definition.Attributes {
		if _, exists := claimed[attribute.ID]; exists {
			continue
		}
		drafts = append(drafts, unitDraft{id: "attribute:" + attribute.ID, kind: "attribute", name: attribute.Name, attributeIDs: []string{attribute.ID}})
	}
	for _, panel := range definition.GiftTargetPanels {
		drafts = append(drafts, unitDraft{id: "gift-target:" + panel.ID, kind: "gift-target", name: panel.Name, panelIDs: []string{panel.ID}})
	}

	units := make([]GameplayUnit, 0, len(drafts))
	for _, draft := range drafts {
		units = append(units, materializeUnit(draft, definition, attributesByName, crops, effectIDs))
	}
	sort.Slice(units, func(left, right int) bool { return compareCodeUnits(units[left].ID, units[right].ID) < 0 })
	return units
}

func materializeUnit(draft unitDraft, definition configuration.Definition, attributesByName map[string]string, crops []CropPreset, effectIDs map[int]int) GameplayUnit {
	rules := filterRules(definition.Rules, draft.attributeIDs)
	timers := filterTimerRules(definition.TimerRules, draft.attributeIDs)
	presets := filterFormulaPresets(definition.FormulaPresets, draft.attributeIDs)
	dependencies := make(map[string]struct{}, len(draft.attributeIDs))
	for _, id := range draft.attributeIDs {
		dependencies[id] = struct{}{}
	}
	for _, formula := range append(ruleFormulas(rules), append(timerFormulas(timers), presetFormulas(presets)...)...) {
		for _, token := range formulaIdentifier.FindAllString(formula, -1) {
			if id, exists := attributesByName[token]; exists {
				dependencies[id] = struct{}{}
			}
		}
	}
	attributeIDs := sortedMapKeys(dependencies)
	activityScenes := make(map[string]struct{}, len(draft.activityIDs))
	for _, activity := range definition.Activities {
		if containsString(draft.activityIDs, activity.ID) && activity.SceneID != "" {
			activityScenes[activity.SceneID] = struct{}{}
		}
	}
	sceneIDs := make([]string, 0)
	for _, scene := range definition.DisplayScenes {
		if _, referenced := activityScenes[scene.ID]; referenced || hasSharedString(scene.AttributeIDs, attributeIDs) {
			sceneIDs = append(sceneIDs, scene.ID)
		}
	}
	panelIDs := sortedUniqueStrings(draft.panelIDs)
	giftSet := make(map[int]struct{})
	for _, rule := range rules {
		giftSet[rule.GiftID] = struct{}{}
		for _, giftID := range rule.MatchGiftIDs {
			giftSet[giftID] = struct{}{}
		}
	}
	if draft.simplePlay && definition.SimplePlay != nil {
		for _, ids := range definition.SimplePlay.Gifts {
			for _, giftID := range ids {
				giftSet[giftID] = struct{}{}
			}
		}
		for _, action := range definition.SimplePlay.OvertimeGiftActions {
			giftSet[action.GiftID] = struct{}{}
		}
	}
	for _, panel := range definition.GiftTargetPanels {
		if !containsString(panelIDs, panel.ID) {
			continue
		}
		for _, item := range panel.Items {
			giftSet[item.GiftID] = struct{}{}
		}
	}
	giftIDs := sortedIntSet(giftSet)
	unit := GameplayUnit{
		ID: draft.id, Kind: draft.kind, Name: draft.name, AttributeIDs: attributeIDs,
		RuleIDs: sortedRuleIDs(rules), TimerRuleIDs: sortedTimerRuleIDs(timers), FormulaPresetIDs: sortedPresetIDs(presets),
		ActivityIDs: sortedUniqueStrings(draft.activityIDs), DisplaySceneIDs: sortedUniqueStrings(sceneIDs), GiftTargetPanelIDs: panelIDs, GiftIDs: giftIDs,
	}
	if draft.simplePlay && definition.SimplePlay != nil {
		unit.SimplePlayTemplateID = definition.SimplePlay.TemplateID
		unit.SimplePlayTemplateVersion = definition.SimplePlay.TemplateVersion
	}
	for _, crop := range crops {
		if cropBelongsToUnit(crop.ID, giftSet, effectIDs) {
			unit.CropPresetIDs = append(unit.CropPresetIDs, crop.ID)
		}
	}
	unit.CropPresetIDs = sortedUniqueStrings(unit.CropPresetIDs)
	return unit
}

func ConnectedGroups(units []GameplayUnit) []GameplayGroup {
	adjacency := make(map[string]map[string]struct{}, len(units))
	for _, unit := range units {
		adjacency[unit.ID] = map[string]struct{}{}
	}
	connectSharedUnits(units, adjacency, func(unit GameplayUnit) []string { return unit.AttributeIDs })
	connectSharedUnits(units, adjacency, func(unit GameplayUnit) []string { return unit.DisplaySceneIDs })
	connectSharedUnits(units, adjacency, func(unit GameplayUnit) []string { return unit.CropPresetIDs })
	byID := make(map[string]GameplayUnit, len(units))
	for _, unit := range units {
		byID[unit.ID] = unit
	}
	visited := make(map[string]struct{}, len(units))
	groups := make([]GameplayGroup, 0)
	for _, unit := range units {
		if _, seen := visited[unit.ID]; seen || len(adjacency[unit.ID]) == 0 {
			continue
		}
		pending, ids := []string{unit.ID}, []string{}
		for len(pending) > 0 {
			current := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if _, seen := visited[current]; seen {
				continue
			}
			visited[current] = struct{}{}
			ids = append(ids, current)
			for neighbor := range adjacency[current] {
				pending = append(pending, neighbor)
			}
		}
		ids = sortedUniqueStrings(ids)
		members := make([]GameplayUnit, 0, len(ids))
		for _, id := range ids {
			members = append(members, byID[id])
		}
		groups = append(groups, GameplayGroup{ID: "group:" + stableGroupHash(strings.Join(ids, "\n")), UnitIDs: ids, Reasons: groupReasons(members)})
	}
	sort.Slice(groups, func(left, right int) bool { return compareCodeUnits(groups[left].ID, groups[right].ID) < 0 })
	return groups
}

func connectSharedUnits(units []GameplayUnit, adjacency map[string]map[string]struct{}, refs func(GameplayUnit) []string) {
	owners := map[string][]string{}
	for _, unit := range units {
		for _, ref := range refs(unit) {
			owners[ref] = append(owners[ref], unit.ID)
		}
	}
	for _, ids := range owners {
		ids = sortedUniqueStrings(ids)
		for index := 1; index < len(ids); index++ {
			adjacency[ids[0]][ids[index]] = struct{}{}
			adjacency[ids[index]][ids[0]] = struct{}{}
		}
	}
}

func groupReasons(units []GameplayUnit) []GameplayGroupReason {
	candidates := make([]groupReasonCandidate, 0)
	for _, input := range []struct {
		kind string
		refs func(GameplayUnit) []string
	}{{"shared-attribute", func(unit GameplayUnit) []string { return unit.AttributeIDs }}, {"shared-scene", func(unit GameplayUnit) []string { return unit.DisplaySceneIDs }}, {"shared-crop-preset", func(unit GameplayUnit) []string { return unit.CropPresetIDs }}} {
		owners := map[string][]string{}
		for _, unit := range units {
			for _, ref := range input.refs(unit) {
				owners[ref] = append(owners[ref], unit.ID)
			}
		}
		for id, ids := range owners {
			ids = sortedUniqueStrings(ids)
			if len(ids) > 1 {
				candidates = append(candidates, groupReasonCandidate{input.kind, id, ids})
			}
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		leftRank, rightRank := groupReasonKindRank(candidates[left].kind), groupReasonKindRank(candidates[right].kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return compareCodeUnits(candidates[left].id, candidates[right].id) < 0
	})
	return groupSpanningReasons(units, candidates)
}

func groupReasonKindRank(kind string) int {
	switch kind {
	case "shared-attribute":
		return 0
	case "shared-scene":
		return 1
	case "shared-crop-preset":
		return 2
	default:
		return 3
	}
}

func groupSpanningReasons(units []GameplayUnit, candidates []groupReasonCandidate) []GameplayGroupReason {
	parent := make(map[string]string, len(units))
	for _, unit := range units {
		parent[unit.ID] = unit.ID
	}
	var find func(string) string
	find = func(id string) string {
		if parent[id] == id {
			return id
		}
		parent[id] = find(parent[id])
		return parent[id]
	}
	reasons := make([]GameplayGroupReason, 0, len(candidates))
	for _, candidate := range candidates {
		roots := sortedUniqueStrings(func() []string {
			values := make([]string, 0, len(candidate.units))
			for _, id := range candidate.units {
				values = append(values, find(id))
			}
			return values
		}())
		if len(roots) < 2 {
			continue
		}
		for _, root := range roots[1:] {
			parent[root] = roots[0]
		}
		reasons = append(reasons, GameplayGroupReason{Kind: candidate.kind, ReferenceID: candidate.id})
	}
	return reasons
}

func stableGroupHash(value string) string {
	hash := uint32(0x811c9dc5)
	for _, codeUnit := range utf16.Encode([]rune(value)) {
		hash ^= uint32(codeUnit)
		hash *= 0x01000193
	}
	return fmt.Sprintf("%08x", hash)
}

func stringParameter(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
func filterKnownIDs(values []string, known map[string]configuration.AttributeDefinition) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := known[value]; ok {
			result = append(result, value)
		}
	}
	return result
}
func filterRules(values []gameplay.Rule, attributes []string) []gameplay.Rule {
	result := []gameplay.Rule{}
	for _, value := range values {
		if containsString(attributes, value.AttributeID) {
			result = append(result, value)
		}
	}
	return result
}
func filterTimerRules(values []gameplay.TimerRule, attributes []string) []gameplay.TimerRule {
	result := []gameplay.TimerRule{}
	for _, value := range values {
		if containsString(attributes, value.AttributeID) {
			result = append(result, value)
		}
	}
	return result
}
func filterFormulaPresets(values []gameplay.FormulaPreset, attributes []string) []gameplay.FormulaPreset {
	result := []gameplay.FormulaPreset{}
	for _, value := range values {
		if containsString(attributes, value.AttributeID) {
			result = append(result, value)
		}
	}
	return result
}
func ruleFormulas(values []gameplay.Rule) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.Formula, value.Condition)
	}
	return result
}
func timerFormulas(values []gameplay.TimerRule) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.Formula, value.Condition)
	}
	return result
}
func presetFormulas(values []gameplay.FormulaPreset) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.Formula)
	}
	return result
}
func sortedRuleIDs(values []gameplay.Rule) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.ID)
	}
	return sortedUniqueStrings(result)
}
func sortedTimerRuleIDs(values []gameplay.TimerRule) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.ID)
	}
	return sortedUniqueStrings(result)
}
func sortedPresetIDs(values []gameplay.FormulaPreset) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.ID)
	}
	return sortedUniqueStrings(result)
}
func sortedIntSet(values map[int]struct{}) []int {
	result := make([]int, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}
func sortedMapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	return sortedUniqueStrings(result)
}
func sortedUniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return compareCodeUnits(result[left], result[right]) < 0 })
	return result
}
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func hasSharedString(left, right []string) bool {
	values := map[string]struct{}{}
	for _, value := range right {
		values[value] = struct{}{}
	}
	for _, value := range left {
		if _, ok := values[value]; ok {
			return true
		}
	}
	return false
}
func compareCodeUnits(left, right string) int {
	a, b := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
	for index := 0; index < len(a) && index < len(b); index++ {
		if a[index] < b[index] {
			return -1
		}
		if a[index] > b[index] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}
