package main

import (
	"fmt"
	"strings"
	"time"
)

const maxLogEntries = 200

type attributeState struct {
	Name             string  `json:"name"`
	Value            float64 `json:"value"`
	Unit             string  `json:"unit"`
	Format           string  `json:"format"`
	Decimals         int     `json:"decimals"`
	Suffix           string  `json:"suffix"`
	Color            string  `json:"color,omitempty"`
	BroadcastMessage string  `json:"broadcastMessage,omitempty"`
}

type giftRule struct {
	ID            string   `json:"id"`
	GiftID        int      `json:"giftId"`
	AttributeName string   `json:"attributeName"`
	FormulaName   string   `json:"formulaName,omitempty"`
	Formula       string   `json:"formula"`
	Enabled       *bool    `json:"enabled,omitempty"`
	MinPrice      *float64 `json:"minPrice,omitempty"`
	Cap           *float64 `json:"cap,omitempty"`
	DailyLimit    *int     `json:"dailyLimit,omitempty"`
}

func (rule giftRule) enabled() bool {
	return rule.Enabled == nil || *rule.Enabled
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
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	CoinType string  `json:"coinType"`
	ImgBasic string  `json:"imgBasic"`
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

type settingsState struct {
	FontSize       int    `json:"fontSize"`
	AccentColor    string `json:"accentColor"`
	ShowStats      bool   `json:"showStats"`
	ShowConnection bool   `json:"showConnection"`
	Align          string `json:"align"`
	Theme          string `json:"theme"`
	GiftView       string `json:"giftView"`
	PanelOpacity   int    `json:"panelOpacity"`
	ShowTutorial   *bool  `json:"showTutorial"`
}

type appState struct {
	RoomID         string              `json:"roomId"`
	Attributes     []attributeState    `json:"attributes"`
	Rules          []giftRule          `json:"rules"`
	TimerRules     []timerRule         `json:"timerRules"`
	FormulaPresets []formulaPreset     `json:"formulaPresets"`
	Settings       settingsState       `json:"settings"`
	GiftCatalog    []giftInfo          `json:"giftCatalog"`
	RecentGifts    []recentGift        `json:"recentGifts"`
	Stats          map[string]dayStats `json:"stats"`
	Log            []logEntry          `json:"log"`
}

type giftEvent struct {
	GiftID    int
	GiftName  string
	Num       int
	Price     float64
	CoinType  string
	TotalCoin float64
	Uname     string
	Avatar    string
	UID       int64
	Timestamp int64
	ImgBasic  string
	Rnd       string
}

func defaultAppState() appState {
	showTutorial := true
	return appState{
		Attributes:     []attributeState{},
		Rules:          []giftRule{},
		TimerRules:     []timerRule{},
		FormulaPresets: []formulaPreset{},
		GiftCatalog:    []giftInfo{},
		RecentGifts:    []recentGift{},
		Stats:          map[string]dayStats{},
		Log:            []logEntry{},
		Settings: settingsState{
			FontSize:       48,
			AccentColor:    "#fb7299",
			ShowStats:      true,
			ShowConnection: true,
			Align:          "center",
			Theme:          "dark",
			GiftView:       "list",
			PanelOpacity:   55,
			ShowTutorial:   &showTutorial,
		},
	}
}

func normalizeAppState(state *appState) {
	if state.Attributes == nil {
		state.Attributes = []attributeState{}
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
	if state.Settings.ShowTutorial == nil {
		showTutorial := !(strings.TrimSpace(state.RoomID) != "" && len(state.Attributes) > 0 && len(state.Rules) > 0)
		state.Settings.ShowTutorial = &showTutorial
	}
}

func (state *appState) findAttribute(name string) *attributeState {
	for index := range state.Attributes {
		if state.Attributes[index].Name == name {
			return &state.Attributes[index]
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
