package main

import (
	"fmt"
	"strings"
	"time"
)

const maxLogEntries = 200

type attributeState struct {
	ID                         string                 `json:"id,omitempty"`
	Name                       string                 `json:"name"`
	Value                      float64                `json:"value"`
	Unit                       string                 `json:"unit"`
	Format                     string                 `json:"format"`
	Decimals                   int                    `json:"decimals"`
	Suffix                     string                 `json:"suffix"`
	Color                      string                 `json:"color,omitempty"`
	BroadcastMessage           string                 `json:"broadcastMessage,omitempty"`
	Display                    *attributeDisplayState `json:"display,omitempty"`
	CreatedFromTemplateID      string                 `json:"createdFromTemplateId,omitempty"`
	CreatedFromTemplateVersion int                    `json:"createdFromTemplateVersion,omitempty"`
}

type attributeDisplayState struct {
	Variant       string                       `json:"variant"`
	ThemeID       string                       `json:"themeId,omitempty"`
	Appearance    *displayAppearanceState      `json:"appearance,omitempty"`
	Title         string                       `json:"title,omitempty"`
	Min           *float64                     `json:"min,omitempty"`
	Max           *float64                     `json:"max,omitempty"`
	LowThreshold  *float64                     `json:"lowThreshold,omitempty"`
	LeftLabel     string                       `json:"leftLabel,omitempty"`
	RightLabel    string                       `json:"rightLabel,omitempty"`
	ValueMappings []attributeValueMappingState `json:"valueMappings,omitempty"`
}

type displayAppearanceState struct {
	ThemeID        string `json:"themeId"`
	FontSize       int    `json:"fontSize"`
	AccentColor    string `json:"accentColor"`
	ShowConnection bool   `json:"showConnection"`
	Align          string `json:"align"`
	PanelOpacity   int    `json:"panelOpacity"`
}

type blindBoxDisplayAppearanceState struct {
	displayAppearanceState
	ViewerSlots int `json:"viewerSlots"`
}

type attributeValueMappingState struct {
	Value    float64 `json:"value"`
	Label    string  `json:"label"`
	Color    string  `json:"color,omitempty"`
	ImageURL string  `json:"imageUrl,omitempty"`
}

type displaySceneState struct {
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	AttributeNames []string                `json:"attributeNames"`
	Layout         string                  `json:"layout"`
	ThemeID        string                  `json:"themeId"`
	Appearance     *displayAppearanceState `json:"appearance,omitempty"`
}

type giftKPIItemState struct {
	GiftID   int    `json:"giftId"`
	GiftName string `json:"giftName"`
	ImageURL string `json:"imageUrl,omitempty"`
	Target   int    `json:"target"`
	Received int    `json:"received,omitempty"`
	BarStyle string `json:"barStyle"`
}

type giftKPIPanelState struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Layout     string                 `json:"layout"`
	Items      []giftKPIItemState     `json:"items"`
	Appearance displayAppearanceState `json:"appearance"`
}

type activityResultState struct {
	WinnerAttributeName string             `json:"winnerAttributeName,omitempty"`
	Values              map[string]float64 `json:"values"`
}

type activityMilestoneState struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	AttributeName string   `json:"attributeName"`
	Comparison    string   `json:"comparison"`
	Threshold     float64  `json:"threshold"`
	Action        string   `json:"action"`
	Message       string   `json:"message"`
	TriggeredAt   int64    `json:"triggeredAt,omitempty"`
	TriggerValue  *float64 `json:"triggerValue,omitempty"`
}

type activityGiftTimeoutState struct {
	Seconds    int    `json:"seconds"`
	Action     string `json:"action"`
	LastGiftAt int64  `json:"lastGiftAt,omitempty"`
	DeadlineAt int64  `json:"deadlineAt,omitempty"`
}

type activitySessionState struct {
	ID             string                    `json:"id"`
	Name           string                    `json:"name"`
	AttributeNames []string                  `json:"attributeNames"`
	SceneID        string                    `json:"sceneId,omitempty"`
	Status         string                    `json:"status"`
	ResultMode     string                    `json:"resultMode"`
	GateRules      bool                      `json:"gateRules"`
	InitialValues  map[string]float64        `json:"initialValues"`
	Milestones     []activityMilestoneState  `json:"milestones"`
	GiftTimeout    *activityGiftTimeoutState `json:"giftTimeout,omitempty"`
	StartedAt      int64                     `json:"startedAt,omitempty"`
	LockedAt       int64                     `json:"lockedAt,omitempty"`
	SettledAt      int64                     `json:"settledAt,omitempty"`
	Result         *activityResultState      `json:"result,omitempty"`
}

type giftRule struct {
	ID            string   `json:"id"`
	GiftID        int      `json:"giftId"`
	AttributeName string   `json:"attributeName"`
	FormulaName   string   `json:"formulaName,omitempty"`
	Formula       string   `json:"formula"`
	Enabled       *bool    `json:"enabled,omitempty"`
	MatchGiftIDs  []int    `json:"matchGiftIds,omitempty"`
	MinPrice      *float64 `json:"minPrice,omitempty"`
	Cap           *float64 `json:"cap,omitempty"`
	DailyLimit    *int     `json:"dailyLimit,omitempty"`
}

func (rule giftRule) enabled() bool {
	return rule.Enabled == nil || *rule.Enabled
}

func (rule giftRule) matchesGiftID(giftID int) bool {
	if rule.GiftID == giftID {
		return true
	}
	for _, candidate := range rule.MatchGiftIDs {
		if candidate == giftID {
			return true
		}
	}
	return false
}

type timerRule struct {
	ID              string `json:"id"`
	AttributeName   string `json:"attributeName"`
	FormulaName     string `json:"formulaName"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Condition       string `json:"condition,omitempty"`
	Formula         string `json:"formula"`
	Enabled         bool   `json:"enabled"`
}

type formulaPreset struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Context             string `json:"context"`
	Formula             string `json:"formula"`
	SourceAttributeName string `json:"sourceAttributeName"`
}

type giftInfo struct {
	ID                  int     `json:"id"`
	Name                string  `json:"name"`
	Price               float64 `json:"price"`
	CoinType            string  `json:"coinType"`
	ImgBasic            string  `json:"imgBasic"`
	BlindBoxParentID    int     `json:"blindBoxParentId,omitempty"`
	BlindBoxParentName  string  `json:"blindBoxParentName,omitempty"`
	BlindBoxParentPrice float64 `json:"blindBoxParentPrice,omitempty"`
}

type recentGift struct {
	giftInfo
	LastReceived int64 `json:"lastReceived"`
	Count        int   `json:"count"`
}

func (g recentGift) MarshalJSON() ([]byte, error) {
	type wire struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		Price        float64 `json:"price"`
		CoinType     string  `json:"coinType"`
		ImgBasic     string  `json:"imgBasic"`
		LastReceived int64   `json:"lastReceived"`
		Count        int     `json:"count"`
	}
	return marshalJSON(wire{g.ID, g.Name, g.Price, g.CoinType, g.ImgBasic, g.LastReceived, g.Count})
}

func (g *recentGift) UnmarshalJSON(data []byte) error {
	type wire struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		Price        float64 `json:"price"`
		CoinType     string  `json:"coinType"`
		ImgBasic     string  `json:"imgBasic"`
		LastReceived int64   `json:"lastReceived"`
		Count        int     `json:"count"`
	}
	var value wire
	if err := unmarshalJSON(data, &value); err != nil {
		return err
	}
	g.giftInfo = giftInfo{ID: value.ID, Name: value.Name, Price: value.Price, CoinType: value.CoinType, ImgBasic: value.ImgBasic}
	g.LastReceived = value.LastReceived
	g.Count = value.Count
	return nil
}

type dayStats struct {
	Date         string         `json:"date"`
	GiftTotals   map[string]int `json:"giftTotals"`
	RuleTriggers map[string]int `json:"ruleTriggers"`
}

type logEntry struct {
	Time          int64   `json:"time"`
	GiftID        int     `json:"giftId"`
	GiftName      string  `json:"giftName"`
	Num           int     `json:"num"`
	Uname         string  `json:"uname"`
	Avatar        string  `json:"avatar,omitempty"`
	SenderUID     int64   `json:"senderUid,omitempty"`
	AttributeName string  `json:"attributeName"`
	Delta         float64 `json:"delta"`
	ValueAfter    float64 `json:"valueAfter"`
	RuleID        string  `json:"ruleId"`
	Source        string  `json:"source,omitempty"`
	TriggerName   string  `json:"triggerName,omitempty"`
	EventID       string  `json:"eventId,omitempty"`
}

type viewerContribution struct {
	Key                   string                 `json:"key"`
	UID                   int64                  `json:"uid,omitempty"`
	Uname                 string                 `json:"uname"`
	Avatar                string                 `json:"avatar,omitempty"`
	GiftCount             int                    `json:"giftCount"`
	GoldValue             float64                `json:"goldValue"`
	SilverValue           float64                `json:"silverValue"`
	RuleTriggers          int                    `json:"ruleTriggers"`
	AttributeDeltas       map[string]float64     `json:"attributeDeltas"`
	BlindBoxCount         int                    `json:"blindBoxCount"`
	BlindBoxCost          float64                `json:"blindBoxCost"`
	BlindBoxValue         float64                `json:"blindBoxValue"`
	BlindBoxProfit        float64                `json:"blindBoxProfit"`
	UnpricedBlindBoxCount int                    `json:"unpricedBlindBoxCount,omitempty"`
	BlindBoxes            []blindBoxContribution `json:"blindBoxes,omitempty"`
	LastGiftAt            int64                  `json:"lastGiftAt"`
}

type blindBoxContribution struct {
	GiftID        int     `json:"giftId"`
	GiftName      string  `json:"giftName"`
	Count         int     `json:"count"`
	Cost          float64 `json:"cost"`
	Value         float64 `json:"value"`
	Profit        float64 `json:"profit"`
	UnpricedCount int     `json:"unpricedCount,omitempty"`
	LastGiftAt    int64   `json:"lastGiftAt"`
}

type contributionLedgerState struct {
	Viewers   []viewerContribution `json:"viewers"`
	UpdatedAt int64                `json:"updatedAt,omitempty"`
}

type settingsState struct {
	FontSize                  int      `json:"fontSize"`
	AccentColor               string   `json:"accentColor"`
	ShowStats                 bool     `json:"showStats"`
	ShowConnection            bool     `json:"showConnection"`
	Align                     string   `json:"align"`
	Theme                     string   `json:"theme"`
	GiftView                  string   `json:"giftView"`
	PanelOpacity              int      `json:"panelOpacity"`
	DefaultDisplayThemeID     string   `json:"defaultDisplayThemeId"`
	ShowTutorial              *bool    `json:"showTutorial"`
	TutorialVersion           int      `json:"tutorialVersion"`
	TutorialCompletedLessons  []string `json:"tutorialCompletedLessons"`
	TutorialReplayMode        *bool    `json:"tutorialReplayMode"`
	TutorialTargetAttributeID string   `json:"tutorialTargetAttributeId,omitempty"`
	TrainingCompletedTopics   []string `json:"trainingCompletedTopics"`
	LastSeenChangelogVersion  string   `json:"lastSeenChangelogVersion"`
	AutoUpdate                *bool    `json:"autoUpdate"`
}

type appState struct {
	RoomID          string                         `json:"roomId"`
	Attributes      []attributeState               `json:"attributes"`
	DisplayScenes   []displaySceneState            `json:"displayScenes"`
	BlindBoxDisplay blindBoxDisplayAppearanceState `json:"blindBoxDisplay"`
	GiftKPIPanels   []giftKPIPanelState            `json:"giftKpiPanels"`
	Activities      []activitySessionState         `json:"activities"`
	Rules           []giftRule                     `json:"rules"`
	TimerRules      []timerRule                    `json:"timerRules"`
	FormulaPresets  []formulaPreset                `json:"formulaPresets"`
	Settings        settingsState                  `json:"settings"`
	GiftCatalog     []giftInfo                     `json:"giftCatalog"`
	RecentGifts     []recentGift                   `json:"recentGifts"`
	Stats           map[string]dayStats            `json:"stats"`
	Log             []logEntry                     `json:"log"`
	Contributions   contributionLedgerState        `json:"contributions"`
}

type giftEvent struct {
	GiftID         int
	BlindGiftID    int
	BlindGiftName  string
	BlindGiftPrice float64
	GiftName       string
	Num            int
	Price          float64
	CoinType       string
	TotalCoin      float64
	Uname          string
	Avatar         string
	UID            int64
	Timestamp      int64
	ImgBasic       string
	Rnd            string
}

func defaultAppState() appState {
	showTutorial := true
	tutorialReplayMode := false
	autoUpdate := true
	return appState{
		Attributes:    []attributeState{},
		DisplayScenes: []displaySceneState{},
		BlindBoxDisplay: blindBoxDisplayAppearanceState{
			displayAppearanceState: displayAppearanceState{
				ThemeID: "glass", FontSize: displayFontSizeDefault, AccentColor: defaultDisplayAccentColor,
				ShowConnection: true, Align: "center", PanelOpacity: displayPanelOpacityDefault,
			},
			ViewerSlots: blindBoxViewerSlotsDefault,
		},
		Activities:     []activitySessionState{},
		GiftKPIPanels:  []giftKPIPanelState{},
		Rules:          []giftRule{},
		TimerRules:     []timerRule{},
		FormulaPresets: []formulaPreset{},
		GiftCatalog:    []giftInfo{},
		RecentGifts:    []recentGift{},
		Stats:          map[string]dayStats{},
		Log:            []logEntry{},
		Contributions:  contributionLedgerState{Viewers: []viewerContribution{}},
		Settings: settingsState{
			FontSize:                 48,
			AccentColor:              "#fb7299",
			ShowStats:                true,
			ShowConnection:           true,
			Align:                    "center",
			Theme:                    "dark",
			GiftView:                 "list",
			PanelOpacity:             55,
			DefaultDisplayThemeID:    "glass",
			ShowTutorial:             &showTutorial,
			TutorialVersion:          3,
			TutorialCompletedLessons: []string{},
			TutorialReplayMode:       &tutorialReplayMode,
			TrainingCompletedTopics:  []string{},
			AutoUpdate:               &autoUpdate,
		},
	}
}

func normalizeAppState(state *appState) {
	if state.Attributes == nil {
		state.Attributes = []attributeState{}
	}
	if state.DisplayScenes == nil {
		state.DisplayScenes = []displaySceneState{}
	}
	if state.GiftKPIPanels == nil {
		state.GiftKPIPanels = []giftKPIPanelState{}
	}
	for panelIndex := range state.GiftKPIPanels {
		panel := &state.GiftKPIPanels[panelIndex]
		if panel.Items == nil {
			panel.Items = []giftKPIItemState{}
		}
		for itemIndex := range panel.Items {
			item := &panel.Items[itemIndex]
			item.Target = maxInt(1, item.Target)
			item.Received = maxInt(0, item.Received)
			if item.BarStyle != "resource" && item.BarStyle != "health" {
				item.BarStyle = "progress"
			}
		}
	}
	if state.Activities == nil {
		state.Activities = []activitySessionState{}
	}
	if state.Rules == nil {
		state.Rules = []giftRule{}
	}
	if state.TimerRules == nil {
		state.TimerRules = []timerRule{}
	}
	if state.FormulaPresets == nil {
		state.FormulaPresets = []formulaPreset{}
	}
	if state.GiftCatalog == nil {
		state.GiftCatalog = []giftInfo{}
	}
	if state.RecentGifts == nil {
		state.RecentGifts = []recentGift{}
	}
	if state.Stats == nil {
		state.Stats = map[string]dayStats{}
	}
	if state.Log == nil {
		state.Log = []logEntry{}
	}
	normalizeContributionLedger(&state.Contributions)
	for index := range state.Rules {
		state.Rules[index].MatchGiftIDs = normalizeGiftIDs(state.Rules[index].MatchGiftIDs)
	}
	defaults := defaultAppState().Settings
	if state.Settings.FontSize == 0 {
		state.Settings.FontSize = defaults.FontSize
	}
	if state.Settings.AccentColor == "" {
		state.Settings.AccentColor = defaults.AccentColor
	}
	if state.Settings.Align == "" {
		state.Settings.Align = defaults.Align
	}
	if state.Settings.Theme == "" {
		state.Settings.Theme = defaults.Theme
	}
	if state.Settings.GiftView == "" {
		state.Settings.GiftView = defaults.GiftView
	}
	if state.Settings.PanelOpacity <= 0 {
		state.Settings.PanelOpacity = defaults.PanelOpacity
	}
	if state.Settings.PanelOpacity < 10 {
		state.Settings.PanelOpacity = 10
	}
	if state.Settings.PanelOpacity > 100 {
		state.Settings.PanelOpacity = 100
	}
	if !isDisplayThemeID(state.Settings.DefaultDisplayThemeID) {
		state.Settings.DefaultDisplayThemeID = defaults.DefaultDisplayThemeID
	}
	appearanceDefaults := displayAppearanceDefaults(state.Settings)
	normalizeBlindBoxDisplayAppearanceState(&state.BlindBoxDisplay, appearanceDefaults)
	for index := range state.GiftKPIPanels {
		panel := &state.GiftKPIPanels[index]
		panel.Layout = normalizeGiftTargetLayout(panel.Layout)
		normalizeDisplayAppearanceState(&panel.Appearance, appearanceDefaults)
	}
	for index := range state.Attributes {
		display := state.Attributes[index].Display
		if display == nil {
			continue
		}
		if !isDisplayThemeID(display.ThemeID) {
			display.ThemeID = state.Settings.DefaultDisplayThemeID
		}
		normalizeDisplayAppearanceState(display.Appearance, appearanceDefaults)
		if !isDisplayVariant(display.Variant) {
			if state.Attributes[index].Format == "hhmmss" {
				display.Variant = "timer"
			} else {
				display.Variant = "number"
			}
		}
		for mappingIndex := range display.ValueMappings {
			mapping := &display.ValueMappings[mappingIndex]
			mapping.Label = strings.TrimSpace(mapping.Label)
			mapping.Color = strings.TrimSpace(mapping.Color)
			mapping.ImageURL = strings.TrimSpace(mapping.ImageURL)
		}
	}
	for index := range state.DisplayScenes {
		scene := &state.DisplayScenes[index]
		scene.ID = strings.TrimSpace(scene.ID)
		scene.Name = strings.TrimSpace(scene.Name)
		scene.AttributeNames = normalizeStrings(scene.AttributeNames)
		scene.Layout = normalizeDisplaySceneLayout(scene.Layout)
		if !isDisplayThemeID(scene.ThemeID) {
			scene.ThemeID = state.Settings.DefaultDisplayThemeID
		}
		normalizeDisplayAppearanceState(scene.Appearance, appearanceDefaults)
	}
	attributeValues := make(map[string]float64, len(state.Attributes))
	for _, attribute := range state.Attributes {
		attributeValues[attribute.Name] = attribute.Value
	}
	for index := range state.Activities {
		activity := &state.Activities[index]
		activity.ID = strings.TrimSpace(activity.ID)
		activity.Name = strings.TrimSpace(activity.Name)
		activity.SceneID = strings.TrimSpace(activity.SceneID)
		activity.AttributeNames = normalizeStrings(activity.AttributeNames)
		if !isActivityStatus(activity.Status) {
			activity.Status = "not_started"
		}
		if !isActivityResultMode(activity.ResultMode) {
			activity.ResultMode = "none"
		}
		if activity.InitialValues == nil {
			activity.InitialValues = map[string]float64{}
		}
		if activity.Milestones == nil {
			activity.Milestones = []activityMilestoneState{}
		}
		for milestoneIndex := range activity.Milestones {
			milestone := &activity.Milestones[milestoneIndex]
			milestone.ID = strings.TrimSpace(milestone.ID)
			milestone.Name = strings.TrimSpace(milestone.Name)
			milestone.AttributeName = strings.TrimSpace(milestone.AttributeName)
			milestone.Message = strings.TrimSpace(milestone.Message)
			if milestone.Comparison != "lte" {
				milestone.Comparison = "gte"
			}
			if milestone.Action != "lock" && milestone.Action != "settle" {
				milestone.Action = "announce"
			}
		}
		if activity.GiftTimeout != nil {
			if activity.GiftTimeout.Action != "settle" && activity.GiftTimeout.Action != "reset" {
				activity.GiftTimeout.Action = "lock"
			}
			if activity.Status != "active" {
				activity.GiftTimeout.LastGiftAt = 0
				activity.GiftTimeout.DeadlineAt = 0
			}
		}
		for _, attributeName := range activity.AttributeNames {
			if _, exists := activity.InitialValues[attributeName]; !exists {
				activity.InitialValues[attributeName] = attributeValues[attributeName]
			}
		}
		for attributeName := range activity.InitialValues {
			if !containsString(activity.AttributeNames, attributeName) {
				delete(activity.InitialValues, attributeName)
			}
		}
		if activity.Result != nil {
			activity.Result.WinnerAttributeName = strings.TrimSpace(activity.Result.WinnerAttributeName)
			if activity.Result.Values == nil {
				activity.Result.Values = map[string]float64{}
			}
			for attributeName := range activity.Result.Values {
				if !containsString(activity.AttributeNames, attributeName) {
					delete(activity.Result.Values, attributeName)
				}
			}
		}
	}
	if state.Settings.ShowTutorial == nil {
		showTutorial := !(strings.TrimSpace(state.RoomID) != "" && len(state.Attributes) > 0 && len(state.Rules) > 0)
		state.Settings.ShowTutorial = &showTutorial
	}
	if state.Settings.TutorialVersion <= 0 {
		state.Settings.TutorialVersion = 3
	}
	if state.Settings.TutorialCompletedLessons == nil {
		state.Settings.TutorialCompletedLessons = []string{}
	}
	if state.Settings.TutorialReplayMode == nil {
		tutorialReplayMode := state.Settings.ShowTutorial != nil &&
			*state.Settings.ShowTutorial &&
			strings.TrimSpace(state.RoomID) != "" &&
			len(state.Attributes) > 0 &&
			len(state.Rules) > 0 &&
			len(state.Settings.TutorialCompletedLessons) == 0
		state.Settings.TutorialReplayMode = &tutorialReplayMode
	}
	if state.Settings.TrainingCompletedTopics == nil {
		state.Settings.TrainingCompletedTopics = []string{}
	}
	if state.Settings.AutoUpdate == nil {
		autoUpdate := true
		state.Settings.AutoUpdate = &autoUpdate
	}
}

func isDisplayThemeID(value string) bool {
	switch value {
	case "minimal", "glass", "rpg", "pixel", "neon", "kawaii":
		return true
	default:
		return false
	}
}

func isDisplayVariant(value string) bool {
	switch value {
	case "number", "timer", "progress", "health", "resource", "tug", "enum":
		return true
	default:
		return false
	}
}

func isHexColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, char := range value[1:] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return false
		}
	}
	return true
}

func isDisplayImageURL(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "data:image/")
}

func isActivityStatus(value string) bool {
	switch value {
	case "not_started", "active", "locked", "settled":
		return true
	default:
		return false
	}
}

func isActivityResultMode(value string) bool {
	switch value {
	case "none", "highest", "lowest":
		return true
	default:
		return false
	}
}

func autoUpdateEnabled(state appState) bool {
	return state.Settings.AutoUpdate == nil || *state.Settings.AutoUpdate
}

func normalizeGiftIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	result := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (state *appState) findAttribute(name string) *attributeState {
	for index := range state.Attributes {
		if state.Attributes[index].Name == name {
			return &state.Attributes[index]
		}
	}
	return nil
}

func (state *appState) findActivity(id string) *activitySessionState {
	for index := range state.Activities {
		if state.Activities[index].ID == id {
			return &state.Activities[index]
		}
	}
	return nil
}

func (state *appState) findGift(id int) *giftInfo {
	for index := range state.GiftCatalog {
		if state.GiftCatalog[index].ID == id {
			return &state.GiftCatalog[index]
		}
	}
	for index := range state.RecentGifts {
		if state.RecentGifts[index].ID == id {
			return &state.RecentGifts[index].giftInfo
		}
	}
	return nil
}

func (state *appState) todayStats() dayStats {
	date := time.Now().Format("2006-01-02")
	stats, ok := state.Stats[date]
	if !ok {
		stats = dayStats{Date: date, GiftTotals: map[string]int{}, RuleTriggers: map[string]int{}}
	}
	if stats.GiftTotals == nil {
		stats.GiftTotals = map[string]int{}
	}
	if stats.RuleTriggers == nil {
		stats.RuleTriggers = map[string]int{}
	}
	return stats
}

func giftKey(id int) string {
	return fmt.Sprintf("%d", id)
}
