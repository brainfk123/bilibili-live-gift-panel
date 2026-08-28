package migration

import "sort"

type CompatibilityStatus string

const (
	CompatibilityComplete     CompatibilityStatus = "complete"
	CompatibilityPartial      CompatibilityStatus = "partial"
	CompatibilityIncompatible CompatibilityStatus = "incompatible"
)

type Compatibility struct {
	Status      CompatibilityStatus `json:"status"`
	ReasonCodes []string            `json:"reasonCodes,omitempty"`
}

// CapabilitySet describes hosted support without carrying a client payload.
type CapabilitySet struct {
	UnitKinds               map[string]bool
	SimplePlayTemplates     map[string]int
	RulesSupported          bool
	TimerRulesSupported     bool
	FormulaPresetsSupported bool
	DisplayScenesSupported  bool
	CropPresetsSupported    bool
}

func AssessCompatibility(unit GameplayUnit, capabilities CapabilitySet) Compatibility {
	incompatible := make([]string, 0, 4)
	if !capabilities.UnitKinds[unit.Kind] {
		incompatible = append(incompatible, "unit_kind_unsupported")
	}
	if len(unit.RuleIDs) != 0 && !capabilities.RulesSupported {
		incompatible = append(incompatible, "rules_unsupported")
	}
	if len(unit.TimerRuleIDs) != 0 && !capabilities.TimerRulesSupported {
		incompatible = append(incompatible, "timer_rules_unsupported")
	}
	if len(unit.FormulaPresetIDs) != 0 && !capabilities.FormulaPresetsSupported {
		incompatible = append(incompatible, "formula_presets_unsupported")
	}
	if unit.Kind == "simple-play" && capabilities.SimplePlayTemplates[unit.SimplePlayTemplateID] < unit.SimplePlayTemplateVersion {
		incompatible = append(incompatible, "simple_play_template_unsupported")
	}
	if len(incompatible) != 0 {
		return Compatibility{Status: CompatibilityIncompatible, ReasonCodes: sortedReasonCodes(incompatible)}
	}
	partial := make([]string, 0, 2)
	if len(unit.DisplaySceneIDs) != 0 && !capabilities.DisplayScenesSupported {
		partial = append(partial, "display_scenes_unsupported")
	}
	if len(unit.CropPresetIDs) != 0 && !capabilities.CropPresetsSupported {
		partial = append(partial, "crop_presets_unsupported")
	}
	if len(partial) != 0 {
		return Compatibility{Status: CompatibilityPartial, ReasonCodes: sortedReasonCodes(partial)}
	}
	return Compatibility{Status: CompatibilityComplete}
}

func sortedReasonCodes(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
