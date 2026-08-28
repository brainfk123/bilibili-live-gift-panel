package migration

import "testing"

func TestAssessCompatibilityReturnsExactStableContracts(t *testing.T) {
	baseUnit := GameplayUnit{ID: "attribute:private-score", Kind: "attribute", Name: "private score configuration", SimplePlayTemplateID: "counter", SimplePlayTemplateVersion: 2}
	tests := []struct {
		name         string
		unit         GameplayUnit
		capabilities CapabilitySet
		want         Compatibility
	}{
		{"complete when every required feature is available", baseUnit, compatibleCapabilities(), Compatibility{Status: CompatibilityComplete}},
		{"incompatible for unsupported unit kind", baseUnit, CapabilitySet{UnitKinds: map[string]bool{"activity": true}, RulesSupported: true, TimerRulesSupported: true, FormulaPresetsSupported: true, DisplayScenesSupported: true, CropPresetsSupported: true}, Compatibility{Status: CompatibilityIncompatible, ReasonCodes: []string{"unit_kind_unsupported"}}},
		{"incompatible for unsupported rules", withRuleIDs(baseUnit, "private-rule"), CapabilitySet{UnitKinds: map[string]bool{"attribute": true}, TimerRulesSupported: true, FormulaPresetsSupported: true, DisplayScenesSupported: true, CropPresetsSupported: true}, Compatibility{Status: CompatibilityIncompatible, ReasonCodes: []string{"rules_unsupported"}}},
		{"incompatible for unsupported timer rules", withTimerRuleIDs(baseUnit, "private-timer"), CapabilitySet{UnitKinds: map[string]bool{"attribute": true}, RulesSupported: true, FormulaPresetsSupported: true, DisplayScenesSupported: true, CropPresetsSupported: true}, Compatibility{Status: CompatibilityIncompatible, ReasonCodes: []string{"timer_rules_unsupported"}}},
		{"incompatible for unsupported formula presets", withFormulaPresetIDs(baseUnit, "private-preset"), CapabilitySet{UnitKinds: map[string]bool{"attribute": true}, RulesSupported: true, TimerRulesSupported: true, DisplayScenesSupported: true, CropPresetsSupported: true}, Compatibility{Status: CompatibilityIncompatible, ReasonCodes: []string{"formula_presets_unsupported"}}},
		{"incompatible for unsupported simple play template", GameplayUnit{ID: "simple-play:private-score", Kind: "simple-play", Name: "private template", SimplePlayTemplateID: "counter", SimplePlayTemplateVersion: 2}, CapabilitySet{UnitKinds: map[string]bool{"simple-play": true}, SimplePlayTemplates: map[string]int{"counter": 1}, RulesSupported: true, TimerRulesSupported: true, FormulaPresetsSupported: true, DisplayScenesSupported: true, CropPresetsSupported: true}, Compatibility{Status: CompatibilityIncompatible, ReasonCodes: []string{"simple_play_template_unsupported"}}},
		{"partial for unsupported display scenes", withDisplaySceneIDs(baseUnit, "private-scene"), CapabilitySet{UnitKinds: map[string]bool{"attribute": true}, RulesSupported: true, TimerRulesSupported: true, FormulaPresetsSupported: true, CropPresetsSupported: true}, Compatibility{Status: CompatibilityPartial, ReasonCodes: []string{"display_scenes_unsupported"}}},
		{"partial for unsupported crop presets", withCropPresetIDs(baseUnit, "gift:1"), CapabilitySet{UnitKinds: map[string]bool{"attribute": true}, RulesSupported: true, TimerRulesSupported: true, FormulaPresetsSupported: true, DisplayScenesSupported: true}, Compatibility{Status: CompatibilityPartial, ReasonCodes: []string{"crop_presets_unsupported"}}},
		{"incompatible reasons take priority over partial reasons in stable order", GameplayUnit{ID: "simple-play:private-score", Kind: "simple-play", Name: "private template", RuleIDs: []string{"private-rule"}, TimerRuleIDs: []string{"private-timer"}, FormulaPresetIDs: []string{"private-preset"}, DisplaySceneIDs: []string{"private-scene"}, CropPresetIDs: []string{"gift:1"}, SimplePlayTemplateID: "counter", SimplePlayTemplateVersion: 2}, CapabilitySet{UnitKinds: map[string]bool{"simple-play": true}, SimplePlayTemplates: map[string]int{"counter": 1}}, Compatibility{Status: CompatibilityIncompatible, ReasonCodes: []string{"formula_presets_unsupported", "rules_unsupported", "simple_play_template_unsupported", "timer_rules_unsupported"}}},
	}

	allowedReasons := map[string]struct{}{"unit_kind_unsupported": {}, "rules_unsupported": {}, "timer_rules_unsupported": {}, "formula_presets_unsupported": {}, "simple_play_template_unsupported": {}, "display_scenes_unsupported": {}, "crop_presets_unsupported": {}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := AssessCompatibility(test.unit, test.capabilities)
			if got.Status != test.want.Status || !sameStrings(got.ReasonCodes, test.want.ReasonCodes) {
				t.Fatalf("AssessCompatibility() = %#v, want %#v", got, test.want)
			}
			for _, reason := range got.ReasonCodes {
				if _, allowed := allowedReasons[reason]; !allowed {
					t.Fatalf("reason is not a stable code: %q", reason)
				}
				for _, raw := range []string{test.unit.ID, test.unit.Name, test.unit.SimplePlayTemplateID, "private-rule", "private-timer", "private-preset", "private-scene", "gift:1"} {
					if reason == raw {
						t.Fatalf("reason leaked unit/configuration data: %q", reason)
					}
				}
			}
		})
	}
}

func compatibleCapabilities() CapabilitySet {
	return CapabilitySet{UnitKinds: map[string]bool{"attribute": true}, SimplePlayTemplates: map[string]int{"counter": 2}, RulesSupported: true, TimerRulesSupported: true, FormulaPresetsSupported: true, DisplayScenesSupported: true, CropPresetsSupported: true}
}
func withRuleIDs(unit GameplayUnit, values ...string) GameplayUnit {
	unit.RuleIDs = values
	return unit
}
func withTimerRuleIDs(unit GameplayUnit, values ...string) GameplayUnit {
	unit.TimerRuleIDs = values
	return unit
}
func withFormulaPresetIDs(unit GameplayUnit, values ...string) GameplayUnit {
	unit.FormulaPresetIDs = values
	return unit
}
func withDisplaySceneIDs(unit GameplayUnit, values ...string) GameplayUnit {
	unit.DisplaySceneIDs = values
	return unit
}
func withCropPresetIDs(unit GameplayUnit, values ...string) GameplayUnit {
	unit.CropPresetIDs = values
	return unit
}
