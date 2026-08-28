package migration

import (
	"testing"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
)

func TestDeriveUnitsIncludesFormulaDependenciesAndConnectedGroups(t *testing.T) {
	definition := configuration.Definition{
		Attributes: []configuration.AttributeDefinition{
			{ID: "score", Name: "积分"},
			{ID: "bonus", Name: "加成"},
		},
		Rules: []gameplay.Rule{{ID: "combined", GiftID: 1, AttributeID: "score", Formula: "积分+加成"}},
		Gifts: []configuration.GiftDefinition{{ID: 1, Name: "礼物", Price: 1, CoinType: "gold"}},
	}

	units := DeriveUnits(definition, configuration.DefaultRuntime(definition))
	if len(units) != 2 {
		t.Fatalf("unit count = %d, want 2", len(units))
	}
	if got := units[1]; got.ID != "attribute:score" || !sameStrings(got.AttributeIDs, []string{"bonus", "score"}) || !sameStrings(got.RuleIDs, []string{"combined"}) || !sameInts(got.GiftIDs, []int{1}) {
		t.Fatalf("score unit = %#v, want formula dependency and rule content", got)
	}
	groups := ConnectedGroups(units)
	if len(groups) != 1 || !sameStrings(groups[0].UnitIDs, []string{"attribute:bonus", "attribute:score"}) || len(groups[0].Reasons) != 1 || groups[0].Reasons[0] != (GameplayGroupReason{Kind: "shared-attribute", ReferenceID: "bonus"}) {
		t.Fatalf("groups = %#v, want shared formula dependency group", groups)
	}
}

func TestDeriveUnitsUsesStableCodePointOrdering(t *testing.T) {
	definition := configuration.Definition{Attributes: []configuration.AttributeDefinition{{ID: "ä", Name: "A"}, {ID: "z", Name: "Z"}}}
	units := DeriveUnits(definition, configuration.DefaultRuntime(definition))
	if len(units) != 2 || units[0].ID != "attribute:z" || units[1].ID != "attribute:ä" {
		t.Fatalf("units = %#v, want code-point stable order", units)
	}
}

func TestConnectedGroupsUsesStableReasonOrder(t *testing.T) {
	groups := ConnectedGroups([]GameplayUnit{
		{ID: "attribute:a", DisplaySceneIDs: []string{"scene"}},
		{ID: "attribute:b", DisplaySceneIDs: []string{"scene"}, CropPresetIDs: []string{"gift:1"}},
		{ID: "attribute:c", CropPresetIDs: []string{"gift:1"}, AttributeIDs: []string{"shared"}},
		{ID: "attribute:d", AttributeIDs: []string{"shared"}},
	})
	if len(groups) != 1 || len(groups[0].Reasons) != 3 {
		t.Fatalf("groups = %#v, want one three-edge group", groups)
	}
	want := []GameplayGroupReason{
		{Kind: "shared-attribute", ReferenceID: "shared"},
		{Kind: "shared-scene", ReferenceID: "scene"},
		{Kind: "shared-crop-preset", ReferenceID: "gift:1"},
	}
	for index := range want {
		if groups[0].Reasons[index] != want[index] {
			t.Fatalf("reasons = %#v, want %#v", groups[0].Reasons, want)
		}
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func sameInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
