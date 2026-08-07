package main

const (
	outputConfigSchemaVersion  = 1
	displayFontSizeMin         = 24
	displayFontSizeMax         = 96
	displayFontSizeDefault     = 48
	displayPanelOpacityMin     = 10
	displayPanelOpacityMax     = 100
	displayPanelOpacityDefault = 55
	blindBoxViewerSlotsMin     = 1
	blindBoxViewerSlotsMax     = 10
	blindBoxViewerSlotsDefault = 3
	defaultDisplayAccentColor  = "#fb7299"
)

var displaySceneLayoutIDs = map[string]struct{}{
	"stack": {}, "grid": {}, "focus": {}, "versus": {}, "dashboard": {},
}

var giftTargetLayoutIDs = map[string]struct{}{
	"stack": {}, "grid": {}, "dashboard": {},
}

func isDisplaySceneLayout(value string) bool {
	_, exists := displaySceneLayoutIDs[value]
	return exists
}

func normalizeDisplaySceneLayout(value string) string {
	if isDisplaySceneLayout(value) {
		return value
	}
	return "stack"
}

func isGiftTargetLayout(value string) bool {
	_, exists := giftTargetLayoutIDs[value]
	return exists
}

func normalizeGiftTargetLayout(value string) string {
	if isGiftTargetLayout(value) {
		return value
	}
	return "grid"
}

func displayAppearanceDefaults(settings settingsState) displayAppearanceState {
	return displayAppearanceState{
		ThemeID:        settings.DefaultDisplayThemeID,
		FontSize:       settings.FontSize,
		AccentColor:    settings.AccentColor,
		ShowConnection: settings.ShowConnection,
		Align:          settings.Align,
		PanelOpacity:   settings.PanelOpacity,
	}
}

func normalizeDisplayAppearanceState(appearance *displayAppearanceState, fallback displayAppearanceState) {
	if appearance == nil {
		return
	}
	if !isDisplayThemeID(appearance.ThemeID) {
		appearance.ThemeID = fallback.ThemeID
	}
	if appearance.FontSize <= 0 {
		appearance.FontSize = fallback.FontSize
	}
	appearance.FontSize = minInt(displayFontSizeMax, maxInt(displayFontSizeMin, appearance.FontSize))
	if !isHexColor(appearance.AccentColor) {
		appearance.AccentColor = fallback.AccentColor
	}
	if appearance.Align != "left" && appearance.Align != "center" && appearance.Align != "right" {
		appearance.Align = fallback.Align
	}
	if appearance.PanelOpacity <= 0 {
		appearance.PanelOpacity = fallback.PanelOpacity
	}
	appearance.PanelOpacity = minInt(displayPanelOpacityMax, maxInt(displayPanelOpacityMin, appearance.PanelOpacity))
}

func normalizeBlindBoxDisplayAppearanceState(appearance *blindBoxDisplayAppearanceState, fallback displayAppearanceState) {
	normalizeDisplayAppearanceState(&appearance.displayAppearanceState, fallback)
	if appearance.ViewerSlots <= 0 {
		appearance.ViewerSlots = blindBoxViewerSlotsDefault
	}
	appearance.ViewerSlots = minInt(blindBoxViewerSlotsMax, maxInt(blindBoxViewerSlotsMin, appearance.ViewerSlots))
}

func validateDisplayAppearanceState(appearance displayAppearanceState) bool {
	return isDisplayThemeID(appearance.ThemeID) &&
		isHexColor(appearance.AccentColor) &&
		appearance.FontSize >= displayFontSizeMin && appearance.FontSize <= displayFontSizeMax &&
		appearance.PanelOpacity >= displayPanelOpacityMin && appearance.PanelOpacity <= displayPanelOpacityMax &&
		(appearance.Align == "left" || appearance.Align == "center" || appearance.Align == "right")
}
