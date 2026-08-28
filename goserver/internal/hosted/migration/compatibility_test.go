package migration

import "testing"

func TestAssessCompatibilityUsesOnlyStableReasonCodes(t *testing.T) {
	unit := GameplayUnit{
		ID:                        "simple-play:score",
		Kind:                      "simple-play",
		SimplePlayTemplateID:      "counter",
		SimplePlayTemplateVersion: 2,
		DisplaySceneIDs:           []string{"score-scene"},
		CropPresetIDs:             []string{"gift:1"},
	}

	partial := AssessCompatibility(unit, CapabilitySet{
		UnitKinds:              map[string]bool{"simple-play": true},
		SimplePlayTemplates:    map[string]int{"counter": 2},
		DisplayScenesSupported: false,
		CropPresetsSupported:   false,
	})
	if partial.Status != CompatibilityPartial || !sameStrings(partial.ReasonCodes, []string{"crop_presets_unsupported", "display_scenes_unsupported"}) {
		t.Fatalf("partial compatibility = %#v", partial)
	}

	incompatible := AssessCompatibility(unit, CapabilitySet{UnitKinds: map[string]bool{"simple-play": true}, SimplePlayTemplates: map[string]int{"counter": 1}})
	if incompatible.Status != CompatibilityIncompatible || !sameStrings(incompatible.ReasonCodes, []string{"simple_play_template_unsupported"}) {
		t.Fatalf("incompatible compatibility = %#v", incompatible)
	}
	for _, reason := range append(partial.ReasonCodes, incompatible.ReasonCodes...) {
		if reason == "counter" || reason == "score-scene" || reason == "gift:1" {
			t.Fatalf("reason leaked raw configuration: %q", reason)
		}
	}
}
