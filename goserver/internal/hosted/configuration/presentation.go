package configuration

// DisplayPresentation is the narrow, non-executable projection sent to OBS.
// It intentionally excludes names, formulas, rules, gifts, and runtime state.
type DisplayPresentation struct {
	Appearance            *GlobalAppearance            `json:"appearance,omitempty"`
	AttributeAppearances  map[string]DisplayAppearance `json:"attributeAppearances,omitempty"`
	SceneAppearances      map[string]DisplayAppearance `json:"sceneAppearances,omitempty"`
	GiftTargetAppearances map[string]DisplayAppearance `json:"giftTargetAppearances,omitempty"`
	BlindBoxDisplay       *DisplayAppearance           `json:"blindBoxDisplay,omitempty"`
}

// PresentationFor returns nil when a definition has no explicit appearance,
// keeping legacy snapshots byte-compatible apart from the optional field.
func PresentationFor(definition Definition) *DisplayPresentation {
	presentation := &DisplayPresentation{
		Appearance:      cloneGlobalAppearance(definition.Appearance),
		BlindBoxDisplay: cloneDisplayAppearance(definition.BlindBoxDisplay),
	}
	for _, attribute := range definition.Attributes {
		if attribute.Display == nil || attribute.Display.Appearance == nil {
			continue
		}
		if presentation.AttributeAppearances == nil {
			presentation.AttributeAppearances = make(map[string]DisplayAppearance)
		}
		presentation.AttributeAppearances[attribute.ID] = *attribute.Display.Appearance
	}
	for _, scene := range definition.DisplayScenes {
		if scene.Appearance == nil {
			continue
		}
		if presentation.SceneAppearances == nil {
			presentation.SceneAppearances = make(map[string]DisplayAppearance)
		}
		presentation.SceneAppearances[scene.ID] = *scene.Appearance
	}
	for _, panel := range definition.GiftTargetPanels {
		if panel.Appearance == nil {
			continue
		}
		if presentation.GiftTargetAppearances == nil {
			presentation.GiftTargetAppearances = make(map[string]DisplayAppearance)
		}
		presentation.GiftTargetAppearances[panel.ID] = *panel.Appearance
	}
	if presentation.Appearance == nil && presentation.BlindBoxDisplay == nil && len(presentation.AttributeAppearances) == 0 && len(presentation.SceneAppearances) == 0 && len(presentation.GiftTargetAppearances) == 0 {
		return nil
	}
	return presentation
}

func cloneGlobalAppearance(appearance *GlobalAppearance) *GlobalAppearance {
	if appearance == nil {
		return nil
	}
	copy := *appearance
	return &copy
}

func cloneDisplayAppearance(appearance *DisplayAppearance) *DisplayAppearance {
	if appearance == nil {
		return nil
	}
	copy := *appearance
	return &copy
}
