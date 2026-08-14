package main

import "testing"

func TestGiftIdentityLevelUsesProductOrdering(t *testing.T) {
	tests := map[string]float64{
		"":         0,
		"unknown":  0,
		"fan":      1,
		"captain":  2,
		"admiral":  3,
		"governor": 4,
	}
	for membership, want := range tests {
		if got := giftIdentityLevel(membership); got != want {
			t.Fatalf("membership %q = %v, want %v", membership, got, want)
		}
	}
}

func TestBuildGiftFormulaEnvironmentReservesIdentityNames(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{
		{Name: "积分", Value: 7},
		{Name: "用户身份", Value: 99},
		{Name: "舰长", Value: 99},
	}
	env := buildGiftFormulaEnvironment(state, "积分", 8, 5200, "captain")
	want := map[string]float64{
		"积分":    8,
		"price": 5200,
		"用户身份":  2,
		"普通用户":  0,
		"粉丝团":   1,
		"舰长":    2,
		"提督":    3,
		"总督":    4,
	}
	for name, value := range want {
		if env[name] != value {
			t.Fatalf("%s = %v, want %v", name, env[name], value)
		}
	}
}

func TestBuildGiftFormulaEnvironmentPreservesLegacyPriceOverride(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "price", Value: 99}}
	env := buildGiftFormulaEnvironmentWithIdentity(state, "积分", 8, 5200, giftIdentityCaptain)
	if env["price"] != 99 {
		t.Fatalf("price = %v, want legacy attribute value 99", env["price"])
	}
}

func TestReservedFormulaNames(t *testing.T) {
	for _, name := range []string{"用户身份", "普通用户", "粉丝团", "舰长", "提督", "总督"} {
		if !isReservedFormulaName(name) {
			t.Fatalf("%q was not reserved", name)
		}
	}
	for _, name := range []string{"积分", "price", "用户身份等级"} {
		if isReservedFormulaName(name) {
			t.Fatalf("%q was reserved", name)
		}
	}
}
