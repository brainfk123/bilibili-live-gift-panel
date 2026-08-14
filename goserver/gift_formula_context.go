package main

import "strings"

const (
	giftFormulaUserIdentity = "用户身份"
	giftIdentityOrdinary    = 0
	giftIdentityFan         = 1
	giftIdentityCaptain     = 2
	giftIdentityAdmiral     = 3
	giftIdentityGovernor    = 4
)

func giftIdentityLevel(membership string) float64 {
	switch strings.TrimSpace(membership) {
	case "fan":
		return giftIdentityFan
	case "captain":
		return giftIdentityCaptain
	case "admiral":
		return giftIdentityAdmiral
	case "governor":
		return giftIdentityGovernor
	default:
		return giftIdentityOrdinary
	}
}

func isReservedFormulaName(name string) bool {
	switch name {
	case giftFormulaUserIdentity, "普通用户", "粉丝团", "舰长", "提督", "总督":
		return true
	default:
		return false
	}
}

func buildGiftFormulaEnvironment(state appState, attributeName string, attributeValue, giftPrice float64, membership string) map[string]float64 {
	return buildGiftFormulaEnvironmentWithIdentity(state, attributeName, attributeValue, giftPrice, giftIdentityLevel(membership))
}

func buildGiftFormulaEnvironmentWithIdentity(state appState, attributeName string, attributeValue, giftPrice, identityLevel float64) map[string]float64 {
	systemValues := map[string]float64{
		giftFormulaUserIdentity: identityLevel,
		"普通用户":                  giftIdentityOrdinary,
		"粉丝团":                   giftIdentityFan,
		"舰长":                    giftIdentityCaptain,
		"提督":                    giftIdentityAdmiral,
		"总督":                    giftIdentityGovernor,
	}
	environment := make(map[string]float64, len(state.Attributes)+len(systemValues)+1)
	environment["price"] = giftPrice
	for _, attribute := range state.Attributes {
		environment[attribute.Name] = attribute.Value
	}
	environment[attributeName] = attributeValue
	for name, value := range systemValues {
		environment[name] = value
	}
	return environment
}
