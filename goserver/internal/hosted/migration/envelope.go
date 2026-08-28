// Package migration accepts a bounded desktop export and prepares an
// account-owned, server-normalized preview. Raw uploads never cross this
// package's Decode boundary.
package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
)

const (
	maximumEnvelopeBytes       int64 = 2 << 20
	maximumJSONDepth                 = 32
	maximumStringRunes               = 4096
	configurationSchemaVersion       = 5
)

var ErrInvalidEnvelope = errors.New("migration: invalid envelope")

// Envelope is the normalized migration representation. CanonicalJSON is the
// only byte representation retained long enough to calculate Hash; callers
// must persist Definition and Runtime instead.
type Envelope struct {
	Definition        configuration.Definition
	Runtime           configuration.RuntimeState
	RoomSuggestion    string
	GeneralSettings   GeneralSettings
	CropPresets       []CropPreset
	Units             []GameplayUnit
	Groups            []GameplayGroup
	ClientDeclaration GameplayDependencyDeclaration
	Source            Source
	Counts            Counts
	Report            Report
	CanonicalJSON     []byte
	Hash              [sha256.Size]byte
}

type GeneralSettings = configuration.GeneralSettings
type CropPreset = configuration.CropPreset
type Crop = configuration.Crop

type Source struct {
	AppVersion                 string `json:"appVersion"`
	ConfigurationSchemaVersion int    `json:"configurationSchemaVersion"`
}

type Counts struct {
	Attributes       int `json:"attributes"`
	Rules            int `json:"rules"`
	Activities       int `json:"activities"`
	GiftTargetPanels int `json:"giftTargetPanels"`
	GiftTargetItems  int `json:"giftTargetItems"`
}

// Report is safe to show to a client: it identifies filtered paths but never
// retains their values or another copy of the upload.
type Report struct {
	Counts   Counts   `json:"counts"`
	Ignored  []string `json:"ignored"`
	Warnings []string `json:"warnings"`
}

type wireEnvelope struct {
	Kind             string      `json:"kind"`
	MigrationVersion int         `json:"migrationVersion"`
	Source           wireSource  `json:"source"`
	ExportedAt       string      `json:"exportedAt"`
	Payload          wirePayload `json:"payload"`
}
type wireSource struct {
	AppVersion                 string `json:"appVersion"`
	ConfigurationSchemaVersion int    `json:"configSchemaVersion"`
}
type wirePayload struct {
	RoomSuggestion        *string                       `json:"roomSuggestion"`
	GeneralSettings       GeneralSettings               `json:"generalSettings"`
	CropPresets           []CropPreset                  `json:"cropPresets"`
	RejectedCropPresets   []wireRejectedCropPreset      `json:"rejectedCropPresets"`
	DependencyDeclaration GameplayDependencyDeclaration `json:"dependencyDeclaration"`
	Definition            wireDefinition                `json:"definition"`
	Runtime               wireRuntime                   `json:"runtime"`
}
type wireRejectedCropPreset struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}
type wireDefinition struct {
	Attributes       []wireAttribute          `json:"attributes"`
	DisplayScenes    []gameplay.DisplayScene  `json:"displayScenes"`
	GiftTargetPanels []wireGiftTargetPanel    `json:"giftTargetPanels"`
	Activities       []wireActivity           `json:"activities"`
	Rules            []gameplay.Rule          `json:"rules"`
	TimerRules       []gameplay.TimerRule     `json:"timerRules"`
	FormulaPresets   []gameplay.FormulaPreset `json:"formulaPresets"`
	SimplePlay       *wireSimplePlay          `json:"simplePlay"`
	Gifts            []wireGiftDefinition     `json:"gifts"`
}
type wireGiftDefinition struct {
	ID                  int     `json:"id"`
	Name                string  `json:"name"`
	Price               float64 `json:"price"`
	CoinType            string  `json:"coinType"`
	EffectID            *int    `json:"effectId"`
	BlindBoxParentID    int     `json:"blindBoxParentId,omitempty"`
	BlindBoxParentName  string  `json:"blindBoxParentName,omitempty"`
	BlindBoxParentPrice float64 `json:"blindBoxParentPrice,omitempty"`
}
type wireAttribute struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Unit             string            `json:"unit"`
	Format           string            `json:"format"`
	Decimals         int               `json:"decimals"`
	Suffix           string            `json:"suffix"`
	Color            string            `json:"color"`
	BroadcastMessage string            `json:"broadcastMessage"`
	Display          *gameplay.Display `json:"display"`
}
type wireGiftTargetPanel struct {
	ID     string               `json:"id"`
	Name   string               `json:"name"`
	Layout string               `json:"layout"`
	Items  []wireGiftTargetItem `json:"items"`
}
type wireGiftTargetItem struct {
	GiftID   int    `json:"giftId"`
	Name     string `json:"name"`
	Target   int    `json:"target"`
	BarStyle string `json:"barStyle"`
}
type wireActivity struct {
	ID            string                               `json:"id"`
	Name          string                               `json:"name"`
	AttributeIDs  []string                             `json:"attributeIds"`
	SceneID       string                               `json:"sceneId"`
	ResultMode    string                               `json:"resultMode"`
	GateRules     bool                                 `json:"gateRules"`
	InitialValues map[string]float64                   `json:"initialValues"`
	Milestones    []configuration.MilestoneDefinition  `json:"milestones"`
	GiftTimeout   *configuration.GiftTimeoutDefinition `json:"giftTimeout"`
}
type wireSimplePlay struct {
	Version             int                           `json:"version"`
	TemplateID          string                        `json:"templateId"`
	TemplateVersion     int                           `json:"templateVersion"`
	AttributeID         string                        `json:"attributeId"`
	Parameters          map[string]json.RawMessage    `json:"parameters"`
	Gifts               map[string][]int              `json:"gifts"`
	OvertimeGiftActions []gameplay.OvertimeGiftAction `json:"overtimeGiftActions"`
	ManagedFingerprint  string                        `json:"managedFingerprint"`
}
type wireRuntime struct {
	AttributeValues    map[string]float64                     `json:"attributeValues"`
	GiftTargetReceived []configuration.GiftTargetRuntimeState `json:"giftTargetReceived"`
	Activities         []configuration.ActivityRuntimeState   `json:"activities"`
	RuleLimits         gameplay.RuleLimitState                `json:"ruleLimits"`
}

// Decode reads at most the smaller caller limit or two MiB, accepts exactly
// one JSON value, removes all non-allowlisted content, then hashes canonical
// normalized configuration rather than any raw input bytes.
func Decode(reader io.Reader, maxBytes int64) (Envelope, Report, error) {
	if reader == nil || maxBytes <= 0 {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	limit := maxBytes
	if limit > maximumEnvelopeBytes {
		limit = maximumEnvelopeBytes
	}
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(raw)) > limit || len(raw) == 0 {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	if err := validateJSONTokens(raw); err != nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}

	var top map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&top); err != nil || top == nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	if !requiredObject(top, "source") || !requiredObject(top, "payload") {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(top["payload"], &payload) != nil || payload == nil || !requiredObject(payload, "definition") || !requiredObject(payload, "runtime") {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}

	report := Report{}
	var wire wireEnvelope
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Kind != "gift-panel-online-migration" || wire.ExportedAt == "" || wire.Source.AppVersion == "" || wire.Source.ConfigurationSchemaVersion < 1 || wire.Source.ConfigurationSchemaVersion > configurationSchemaVersion {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	if wire.MigrationVersion != 1 && wire.MigrationVersion != 2 {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	scanNode(raw, "", envelopeSchema(wire.MigrationVersion), &report)
	if err := validateWire(wire); err != nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	definition, runtime, err := normalizeWire(wire, &report)
	if err != nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	settings := GeneralSettings{}
	crops := []CropPreset(nil)
	clientDeclaration := GameplayDependencyDeclaration{}
	units := DeriveUnits(definition, runtime)
	if wire.MigrationVersion == 2 {
		if err := validateV2Payload(payload); err != nil {
			return Envelope{}, Report{}, ErrInvalidEnvelope
		}
		settings = normalizeGeneralSettings(wire.Payload.GeneralSettings)
		effectIDs := effectIDsByGift(wire.Payload.Definition.Gifts)
		crops = normalizeCropPresets(wire.Payload.CropPresets, definition.Gifts, effectIDs, &report)
		units = deriveUnits(definition, runtime, crops, effectIDs)
		clientDeclaration = normalizeClientDeclaration(wire.Payload.DependencyDeclaration)
		report.Warnings = append(report.Warnings, "玩法关系已由服务器重新整理")
	}
	groups := ConnectedGroups(units)
	var canonical []byte
	if wire.MigrationVersion == 1 {
		canonical, err = json.Marshal(struct {
			Definition configuration.Definition   `json:"definition"`
			Runtime    configuration.RuntimeState `json:"runtime"`
		}{definition, runtime})
	} else {
		canonical, err = json.Marshal(struct {
			Definition      configuration.Definition   `json:"definition"`
			Runtime         configuration.RuntimeState `json:"runtime"`
			GeneralSettings GeneralSettings            `json:"generalSettings"`
			CropPresets     []CropPreset               `json:"cropPresets"`
		}{definition, runtime, settings, crops})
	}
	if err != nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	if wire.MigrationVersion == 2 {
		if settings.ConfigurationMode != "" {
			copy := settings
			definition.GeneralSettings = &copy
		}
		definition.CropPresets = append([]CropPreset(nil), crops...)
	}
	roomSuggestion := ""
	if wire.Payload.RoomSuggestion != nil {
		roomSuggestion = *wire.Payload.RoomSuggestion
	}
	report.Counts = countWire(wire.Payload.Definition)
	report.finalize()
	return Envelope{Definition: definition, Runtime: runtime, RoomSuggestion: roomSuggestion, GeneralSettings: settings, CropPresets: crops, Units: units, Groups: groups, ClientDeclaration: clientDeclaration, Source: Source{AppVersion: wire.Source.AppVersion, ConfigurationSchemaVersion: wire.Source.ConfigurationSchemaVersion}, Counts: report.Counts, Report: report, CanonicalJSON: canonical, Hash: sha256.Sum256(canonical)}, report, nil
}

func requiredObject(object map[string]json.RawMessage, key string) bool {
	raw, exists := object[key]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var child map[string]json.RawMessage
	return json.Unmarshal(raw, &child) == nil && child != nil
}

func normalizeWire(wire wireEnvelope, report *Report) (configuration.Definition, configuration.RuntimeState, error) {
	gifts := make([]configuration.GiftDefinition, len(wire.Payload.Definition.Gifts))
	for index, gift := range wire.Payload.Definition.Gifts {
		gifts[index] = configuration.GiftDefinition{ID: gift.ID, Name: gift.Name, Price: gift.Price, CoinType: gift.CoinType, BlindBoxParentID: gift.BlindBoxParentID, BlindBoxParentName: gift.BlindBoxParentName, BlindBoxParentPrice: gift.BlindBoxParentPrice}
	}
	definition := configuration.Definition{Attributes: make([]configuration.AttributeDefinition, len(wire.Payload.Definition.Attributes)), DisplayScenes: wire.Payload.Definition.DisplayScenes, GiftTargetPanels: make([]configuration.GiftTargetPanelDefinition, len(wire.Payload.Definition.GiftTargetPanels)), Activities: make([]configuration.ActivityDefinition, len(wire.Payload.Definition.Activities)), Rules: wire.Payload.Definition.Rules, TimerRules: wire.Payload.Definition.TimerRules, FormulaPresets: wire.Payload.Definition.FormulaPresets, Gifts: gifts}
	for index, item := range wire.Payload.Definition.Attributes {
		definition.Attributes[index] = configuration.AttributeDefinition{ID: item.ID, Name: item.Name, Unit: item.Unit, Format: item.Format, Decimals: item.Decimals, Suffix: item.Suffix, Color: item.Color, BroadcastMessage: item.BroadcastMessage, Display: item.Display}
	}
	for index, panel := range wire.Payload.Definition.GiftTargetPanels {
		items := make([]configuration.GiftTargetItemDefinition, len(panel.Items))
		for itemIndex, item := range panel.Items {
			items[itemIndex] = configuration.GiftTargetItemDefinition{GiftID: item.GiftID, Name: item.Name, Target: item.Target, BarStyle: item.BarStyle}
		}
		definition.GiftTargetPanels[index] = configuration.GiftTargetPanelDefinition{ID: panel.ID, Name: panel.Name, Layout: panel.Layout, Items: items}
	}
	for index, activity := range wire.Payload.Definition.Activities {
		definition.Activities[index] = configuration.ActivityDefinition{ID: activity.ID, Name: activity.Name, AttributeIDs: activity.AttributeIDs, SceneID: activity.SceneID, ResultMode: activity.ResultMode, GateRules: activity.GateRules, InitialValues: activity.InitialValues, Milestones: activity.Milestones, GiftTimeout: activity.GiftTimeout}
	}
	if wire.Payload.Definition.SimplePlay != nil {
		simple, err := normalizeSimplePlay(*wire.Payload.Definition.SimplePlay, definition.Gifts, report)
		if err != nil {
			return configuration.Definition{}, configuration.RuntimeState{}, err
		}
		definition.SimplePlay = simple
	}
	runtime := configuration.RuntimeState{AttributeValues: wire.Payload.Runtime.AttributeValues, GiftTargetReceived: wire.Payload.Runtime.GiftTargetReceived, Activities: wire.Payload.Runtime.Activities, RuleLimits: wire.Payload.Runtime.RuleLimits}
	snapshot, err := configuration.Join(definition, runtime)
	if err != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, err
	}
	return configuration.Split(snapshot)
}

type simpleParameter struct {
	fallback any
	text     bool
	toggle   bool
	min, max float64
	integer  bool
	choices  map[string]struct{}
}
type simpleSlot struct {
	id       string
	minimum  int
	multiple bool
}
type simpleTemplate struct {
	parameters map[string]simpleParameter
	slots      []simpleSlot
	actions    bool
}

func normalizeSimplePlay(wire wireSimplePlay, catalog []configuration.GiftDefinition, report *Report) (*gameplay.SimplePlay, error) {
	template, ok := migrationSimpleTemplate(wire.TemplateID, wire.TemplateVersion)
	if !ok || wire.Version != 1 || !safeSimpleText(wire.ManagedFingerprint) {
		reportIgnored(report, "/payload/definition/simplePlay")
		return nil, nil
	}
	parameters := make(map[string]any, len(template.parameters))
	for key := range wire.Parameters {
		if _, known := template.parameters[key]; !known {
			reportIgnored(report, "/payload/definition/simplePlay/parameters/"+escapePointer(key))
		}
	}
	for key, rule := range template.parameters {
		value := rule.fallback
		if raw, present := wire.Parameters[key]; present {
			var candidate any
			if json.Unmarshal(raw, &candidate) == nil && validSimpleParameter(candidate, rule) {
				value = candidate
			} else {
				reportIgnored(report, "/payload/definition/simplePlay/parameters/"+escapePointer(key))
				reportIgnored(report, "/payload/definition/simplePlay")
				return nil, nil
			}
		}
		parameters[key] = value
	}
	knownSlots := make(map[string]struct{}, len(template.slots))
	for _, slot := range template.slots {
		knownSlots[slot.id] = struct{}{}
	}
	for slot := range wire.Gifts {
		if _, known := knownSlots[slot]; !known {
			reportIgnored(report, "/payload/definition/simplePlay/gifts/"+escapePointer(slot))
		}
	}
	knownGifts := make(map[int]struct{}, len(catalog))
	for _, gift := range catalog {
		knownGifts[gift.ID] = struct{}{}
	}
	seen := make(map[int]struct{})
	gifts := make(map[string][]int, len(template.slots))
	for _, policy := range template.slots {
		for index, giftID := range wire.Gifts[policy.id] {
			if giftID <= 0 {
				reportIgnored(report, "/payload/definition/simplePlay/gifts/"+policy.id+"/"+strconv.Itoa(index))
				continue
			}
			if _, exists := knownGifts[giftID]; !exists {
				reportIgnored(report, "/payload/definition/simplePlay/gifts/"+policy.id+"/"+strconv.Itoa(index))
				continue
			}
			if _, duplicate := seen[giftID]; duplicate {
				reportIgnored(report, "/payload/definition/simplePlay/gifts/"+policy.id+"/"+strconv.Itoa(index))
				continue
			}
			if !policy.multiple && len(gifts[policy.id]) > 0 {
				reportIgnored(report, "/payload/definition/simplePlay/gifts/"+policy.id+"/"+strconv.Itoa(index))
				continue
			}
			seen[giftID] = struct{}{}
			gifts[policy.id] = append(gifts[policy.id], giftID)
		}
		if len(gifts[policy.id]) < policy.minimum {
			reportIgnored(report, "/payload/definition/simplePlay/gifts/"+policy.id)
			return nil, nil
		}
	}
	actions := make([]gameplay.OvertimeGiftAction, 0)
	if template.actions {
		for index, action := range wire.OvertimeGiftActions {
			if _, selected := seen[action.GiftID]; !selected || !validOvertimeAction(action) || containsAction(actions, action.GiftID) {
				reportIgnored(report, "/payload/definition/simplePlay/overtimeGiftActions/"+strconv.Itoa(index)+"/operation")
				continue
			}
			actions = append(actions, action)
		}
	} else if len(wire.OvertimeGiftActions) != 0 {
		reportIgnored(report, "/payload/definition/simplePlay/overtimeGiftActions")
	}
	return &gameplay.SimplePlay{Version: 1, TemplateID: wire.TemplateID, TemplateVersion: wire.TemplateVersion, AttributeID: wire.AttributeID, Parameters: parameters, Gifts: gifts, OvertimeGiftActions: actions, ManagedFingerprint: wire.ManagedFingerprint}, nil
}

func migrationSimpleTemplate(id string, version int) (simpleTemplate, bool) {
	text := func(value string) simpleParameter { return simpleParameter{fallback: value, text: true} }
	number := func(value, min, max float64, integer bool) simpleParameter {
		return simpleParameter{fallback: value, min: min, max: max, integer: integer}
	}
	selectValue := func(value string, values ...string) simpleParameter {
		choices := make(map[string]struct{}, len(values))
		for _, item := range values {
			choices[item] = struct{}{}
		}
		return simpleParameter{fallback: value, choices: choices}
	}
	broadcast := text("感谢大家的支持，欢迎投喂礼物")
	slots := func(items ...struct {
		id      string
		minimum int
	}) []simpleSlot {
		result := make([]simpleSlot, 0, len(items))
		for _, item := range items {
			result = append(result, simpleSlot{id: item.id, minimum: item.minimum, multiple: true})
		}
		return result
	}
	switch {
	case id == "overtime" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"overtime", 1}), parameters: map[string]simpleParameter{"name": text("加班时间"), "minutesPerYuan": number(60, 1, 3600, false), "maxHours": number(0, 0, 240, false), "broadcastMessage": broadcast}}, true
	case id == "overtime" && version == 2:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"overtime", 1}), actions: true, parameters: map[string]simpleParameter{"name": text("加班时间"), "maxSeconds": number(0, 0, 864000, true), "broadcastMessage": broadcast}}, true
	case id == "countdown" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"extend", 1}), parameters: map[string]simpleParameter{"name": text("剩余时间"), "initialSeconds": number(1800, 10, 86400, false), "growthMode": selectValue("fixed", "fixed", "price"), "addSeconds": number(60, 1, 3600, false), "maxSeconds": number(7200, 60, 86400, false), "broadcastMessage": broadcast}}, true
	case id == "counter" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"count", 1}), parameters: map[string]simpleParameter{"name": text("挑战次数"), "suffix": selectValue("次", "次", "局", "个", "组", "分"), "amount": number(1, .01, 100000, false), "cap": number(0, 0, 1000000, false), "broadcastMessage": broadcast}}, true
	case id == "goal" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"progress", 1}), parameters: map[string]simpleParameter{"name": text("目标进度"), "target": number(100, 1, 100000000, false), "perYuan": number(1, .01, 100000, false), "broadcastMessage": broadcast}}, true
	case id == "boss" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"attack", 1}, struct {
			id      string
			minimum int
		}{"heavy", 0}, struct {
			id      string
			minimum int
		}{"heal", 0}), parameters: map[string]simpleParameter{"name": text("Boss血量"), "bossName": text("最终 Boss"), "maxHealth": number(1000, 1, 100000000, false), "attack": number(50, 0, 100000000, false), "heavy": number(200, 0, 100000000, false), "heal": number(100, 0, 100000000, false), "regenEnabled": simpleParameter{fallback: false, toggle: true}, "regenInterval": number(10, 1, 3600, false), "regenAmount": number(10, 0, 100000000, false), "broadcastMessage": broadcast}}, true
	case id == "resource" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"small", 1}, struct {
			id      string
			minimum int
		}{"large", 0}, struct {
			id      string
			minimum int
		}{"interference", 0}), parameters: map[string]simpleParameter{"name": text("氧气"), "maximum": number(100, 1, 100000000, false), "consumeInterval": number(5, 1, 3600, false), "consumeAmount": number(1, 0, 100000000, false), "smallSupply": number(10, 0, 100000000, false), "largeSupply": number(30, 0, 100000000, false), "interference": number(10, 0, 100000000, false), "broadcastMessage": broadcast}}, true
	case id == "tug" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"left", 1}, struct {
			id      string
			minimum int
		}{"right", 1}), parameters: map[string]simpleParameter{"name": text("局势"), "leftLabel": text("继续挑战"), "rightLabel": text("结束挑战"), "initial": number(50, 0, 100, false), "leftAmount": number(10, 0, 100, false), "rightAmount": number(10, 0, 100, false), "broadcastMessage": broadcast}}, true
	case id == "team-duel" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"left", 1}, struct {
			id      string
			minimum int
		}{"right", 1}), parameters: map[string]simpleParameter{"activityName": text("红蓝阵营对战"), "leftName": text("红队"), "rightName": text("蓝队"), "target": number(100, 1, 100000000, false), "points": number(1, .01, 1000000, false), "broadcastMessage": broadcast}}, true
	case id == "gift-vote" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"left", 1}, struct {
			id      string
			minimum int
		}{"right", 1}), parameters: map[string]simpleParameter{"activityName": text("下一项挑战投票"), "leftName": text("继续挑战"), "rightName": text("休息一下"), "votes": number(1, .01, 1000000, false), "broadcastMessage": broadcast}}, true
	case id == "combo" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"combo", 1}), parameters: map[string]simpleParameter{"name": text("全房连击"), "timeout": number(15, 1, 3600, false), "goal": number(50, 0, 100000000, false), "broadcastMessage": broadcast}}, true
	case id == "milestone" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"progress", 1}), parameters: map[string]simpleParameter{"name": text("应援目标"), "target": number(100, 1, 100000000, false), "amount": number(1, .01, 1000000, false), "message": text("全房目标达成！"), "broadcastMessage": broadcast}}, true
	case id == "random-event" && version == 1:
		return simpleTemplate{slots: slots(struct {
			id      string
			minimum int
		}{"draw", 1}), parameters: map[string]simpleParameter{"name": text("随机事件"), "event1": text("主播喝水"), "event2": text("做 10 个深蹲"), "event3": text("唱一句歌"), "event4": text("安全通过"), "broadcastMessage": broadcast}}, true
	}
	return simpleTemplate{}, false
}
func validSimpleParameter(value any, rule simpleParameter) bool {
	if rule.text {
		text, ok := value.(string)
		return ok && safeSimpleText(text)
	}
	if len(rule.choices) > 0 {
		text, ok := value.(string)
		_, found := rule.choices[text]
		return ok && found
	}
	if rule.toggle {
		_, ok := value.(bool)
		return ok
	}
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= rule.min && number <= rule.max && (!rule.integer || math.Trunc(number) == number)
}

var simpleSchemePattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*:`)
var simpleDrivePattern = regexp.MustCompile(`[A-Za-z]:[\\/]`)
var simpleMediaPattern = regexp.MustCompile(`(?i)\.(apng|avif|bmp|gif|jpe?g|png|svg|webp|mp3|wav|ogg|m4a|mp4|m4v|mov|webm)\b`)

func safeSimpleText(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= maximumStringRunes && !containsSimpleResourceReference(value)
}
func containsSimpleResourceReference(value string) bool {
	for _, index := range simpleSchemePattern.FindAllStringIndex(value, -1) {
		scheme := value[index[0] : index[1]-1]
		remainder := value[index[1]:]
		lower := strings.ToLower(scheme)
		switch lower {
		case "http", "https", "data", "file", "blob", "javascript", "vbscript":
			return true
		}
		if scheme != "PK" && scheme != "HP" && (len(remainder) == 0 || !unicode.IsSpace(firstRune(remainder))) {
			return true
		}
	}
	if strings.Contains(value, "//") || strings.Contains(value, "\\\\") || simpleDrivePattern.MatchString(value) || simpleMediaPattern.MatchString(value) {
		return true
	}
	runes := []rune(value)
	for index, character := range runes {
		if character != '/' && character != '\\' {
			continue
		}
		if index+1 >= len(runes) || unicode.IsSpace(runes[index+1]) {
			continue
		}
		if index == 0 || unicode.IsSpace(runes[index-1]) || unicode.IsPunct(runes[index-1]) || unicode.IsSymbol(runes[index-1]) {
			return true
		}
	}
	return false
}
func firstRune(value string) rune {
	for _, character := range value {
		return character
	}
	return 0
}
func validOvertimeAction(action gameplay.OvertimeGiftAction) bool {
	if action.Operation != "add" && action.Operation != "subtract" && action.Operation != "double" && action.Operation != "halve" && action.Operation != "reset" {
		return false
	}
	if action.Operation != "add" && action.Operation != "subtract" {
		return action.Seconds == nil
	}
	return action.Seconds != nil && *action.Seconds > 0
}
func containsAction(actions []gameplay.OvertimeGiftAction, giftID int) bool {
	for _, action := range actions {
		if action.GiftID == giftID {
			return true
		}
	}
	return false
}
func reportIgnored(report *Report, pointer string) {
	if report == nil {
		return
	}
	report.Ignored = append(report.Ignored, pointer)
	report.Warnings = append(report.Warnings, "desktop-only field removed: "+pointer)
}

func validateWire(wire wireEnvelope) error {
	d := wire.Payload.Definition
	if len(d.Attributes) > 200 || len(d.Rules) > 500 || len(d.Activities) > 100 || len(d.GiftTargetPanels) > 100 || len(d.TimerRules) > 100 {
		return ErrInvalidEnvelope
	}
	for _, panel := range d.GiftTargetPanels {
		if len(panel.Items) > 200 {
			return ErrInvalidEnvelope
		}
	}
	if wire.Payload.RoomSuggestion != nil && (*wire.Payload.RoomSuggestion == "" || len(*wire.Payload.RoomSuggestion) > 128) {
		return ErrInvalidEnvelope
	}
	return nil
}
func countWire(definition wireDefinition) Counts {
	counts := Counts{Attributes: len(definition.Attributes), Rules: len(definition.Rules), Activities: len(definition.Activities), GiftTargetPanels: len(definition.GiftTargetPanels)}
	for _, panel := range definition.GiftTargetPanels {
		counts.GiftTargetItems += len(panel.Items)
	}
	return counts
}

func validateV2Payload(payload map[string]json.RawMessage) error {
	for _, key := range []string{"generalSettings", "cropPresets", "rejectedCropPresets", "dependencyDeclaration"} {
		if _, exists := payload[key]; !exists {
			return ErrInvalidEnvelope
		}
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(payload["generalSettings"], &settings) != nil || settings == nil {
		return ErrInvalidEnvelope
	}
	var crops []json.RawMessage
	if json.Unmarshal(payload["cropPresets"], &crops) != nil {
		return ErrInvalidEnvelope
	}
	var rejected []json.RawMessage
	if json.Unmarshal(payload["rejectedCropPresets"], &rejected) != nil {
		return ErrInvalidEnvelope
	}
	return nil
}

func normalizeGeneralSettings(settings GeneralSettings) GeneralSettings {
	if settings.ConfigurationMode != "simple" && settings.ConfigurationMode != "advanced" {
		return GeneralSettings{}
	}
	return settings
}

var stableCropIdentity = regexp.MustCompile(`^(gift|effect):([1-9][0-9]*)$`)

func effectIDsByGift(gifts []wireGiftDefinition) map[int]int {
	result := make(map[int]int, len(gifts))
	for _, gift := range gifts {
		if gift.EffectID != nil && *gift.EffectID > 0 {
			result[gift.ID] = *gift.EffectID
		}
	}
	return result
}

func normalizeCropPresets(crops []CropPreset, gifts []configuration.GiftDefinition, effectIDs map[int]int, report *Report) []CropPreset {
	knownGifts, knownEffects := make(map[int]struct{}, len(gifts)), make(map[int]struct{}, len(effectIDs))
	for _, gift := range gifts {
		knownGifts[gift.ID] = struct{}{}
	}
	for _, effectID := range effectIDs {
		knownEffects[effectID] = struct{}{}
	}
	accepted := make([]CropPreset, 0, len(crops))
	for index, preset := range crops {
		match := stableCropIdentity.FindStringSubmatch(preset.ID)
		identity, _ := strconv.Atoi(func() string {
			if len(match) == 3 {
				return match[2]
			}
			return "0"
		}())
		_, giftExists := knownGifts[identity]
		_, effectExists := knownEffects[identity]
		if len(match) != 3 || (match[1] == "gift" && !giftExists) || (match[1] == "effect" && !effectExists) || !validCrop(preset.Crop) {
			reportIgnored(report, "/payload/cropPresets/"+strconv.Itoa(index))
			continue
		}
		accepted = append(accepted, CropPreset{ID: preset.ID, Crop: preset.Crop})
	}
	sort.Slice(accepted, func(left, right int) bool { return compareCodeUnits(accepted[left].ID, accepted[right].ID) < 0 })
	return accepted
}

func validCrop(crop Crop) bool {
	return !math.IsNaN(crop.X) && !math.IsInf(crop.X, 0) && !math.IsNaN(crop.Y) && !math.IsInf(crop.Y, 0) && !math.IsNaN(crop.Width) && !math.IsInf(crop.Width, 0) && !math.IsNaN(crop.Height) && !math.IsInf(crop.Height, 0) && crop.X >= 0 && crop.Y >= 0 && crop.Width > 0 && crop.Height > 0 && crop.X+crop.Width <= 1 && crop.Y+crop.Height <= 1
}

func cropBelongsToUnit(id string, giftIDs map[int]struct{}, effectIDs map[int]int) bool {
	match := stableCropIdentity.FindStringSubmatch(id)
	if len(match) != 3 {
		return false
	}
	value, _ := strconv.Atoi(match[2])
	if match[1] == "gift" {
		_, exists := giftIDs[value]
		return exists
	}
	for giftID := range giftIDs {
		if effectIDs[giftID] == value {
			return true
		}
	}
	return false
}

func normalizeClientDeclaration(declaration GameplayDependencyDeclaration) GameplayDependencyDeclaration {
	if declaration.AlgorithmVersion != gameplayDependencyAlgorithmVersion {
		return GameplayDependencyDeclaration{}
	}
	result := GameplayDependencyDeclaration{AlgorithmVersion: gameplayDependencyAlgorithmVersion, Units: make([]GameplayUnit, 0, len(declaration.Units)), Groups: make([]GameplayGroup, 0, len(declaration.Groups))}
	for _, unit := range declaration.Units {
		if !validDeclaredUnitID(unit.ID) || !validUnitKind(unit.Kind) {
			continue
		}
		result.Units = append(result.Units, GameplayUnit{ID: unit.ID, Kind: unit.Kind, AttributeIDs: stableStrings(unit.AttributeIDs), RuleIDs: stableStrings(unit.RuleIDs), TimerRuleIDs: stableStrings(unit.TimerRuleIDs), FormulaPresetIDs: stableStrings(unit.FormulaPresetIDs), ActivityIDs: stableStrings(unit.ActivityIDs), DisplaySceneIDs: stableStrings(unit.DisplaySceneIDs), GiftTargetPanelIDs: stableStrings(unit.GiftTargetPanelIDs), GiftIDs: sortedPositiveInts(unit.GiftIDs), CropPresetIDs: stableCropStrings(unit.CropPresetIDs)})
	}
	sort.Slice(result.Units, func(left, right int) bool { return compareCodeUnits(result.Units[left].ID, result.Units[right].ID) < 0 })
	for _, group := range declaration.Groups {
		if !strings.HasPrefix(group.ID, "group:") {
			continue
		}
		ids := make([]string, 0, len(group.UnitIDs))
		for _, id := range group.UnitIDs {
			if validDeclaredUnitID(id) {
				ids = append(ids, id)
			}
		}
		if len(ids) < 2 {
			continue
		}
		reasons := make([]GameplayGroupReason, 0, len(group.Reasons))
		for _, reason := range group.Reasons {
			if (reason.Kind == "shared-attribute" || reason.Kind == "shared-scene" || reason.Kind == "shared-crop-preset") && strings.TrimSpace(reason.ReferenceID) != "" {
				reasons = append(reasons, GameplayGroupReason{Kind: reason.Kind, ReferenceID: reason.ReferenceID})
			}
		}
		result.Groups = append(result.Groups, GameplayGroup{ID: group.ID, UnitIDs: sortedUniqueStrings(ids), Reasons: reasons})
	}
	return result
}

func validDeclaredUnitID(id string) bool {
	return strings.HasPrefix(id, "simple-play:") || strings.HasPrefix(id, "activity:") || strings.HasPrefix(id, "attribute:") || strings.HasPrefix(id, "gift-target:")
}
func validUnitKind(kind string) bool {
	return kind == "simple-play" || kind == "activity" || kind == "attribute" || kind == "gift-target"
}
func stableStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= maximumStringRunes {
			result = append(result, value)
		}
	}
	return sortedUniqueStrings(result)
}
func stableCropStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if stableCropIdentity.MatchString(value) {
			result = append(result, value)
		}
	}
	return sortedUniqueStrings(result)
}
func sortedPositiveInts(values []int) []int {
	seen := map[int]struct{}{}
	for _, value := range values {
		if value > 0 {
			seen[value] = struct{}{}
		}
	}
	return sortedIntSet(seen)
}

func validateJSONTokens(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case json.Delim:
			if value == '{' || value == '[' {
				depth++
				if depth > maximumJSONDepth {
					return ErrInvalidEnvelope
				}
			} else {
				depth--
			}
		case string:
			if utf8.RuneCountInString(value) > maximumStringRunes {
				return ErrInvalidEnvelope
			}
		case json.Number:
			number, err := strconv.ParseFloat(string(value), 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return ErrInvalidEnvelope
			}
		}
	}
}

type schemaNode struct {
	fields    map[string]schemaNode
	array     *schemaNode
	forbidden bool
}

func envelopeSchema(version int) schemaNode {
	leaf := schemaNode{}
	display := schemaNode{fields: map[string]schemaNode{"variant": leaf, "themeId": leaf, "title": leaf, "min": leaf, "max": leaf, "lowThreshold": leaf, "leftLabel": leaf, "rightLabel": leaf, "valueMappings": {array: &schemaNode{fields: map[string]schemaNode{"value": leaf, "label": leaf, "color": leaf}}}, "appearance": {forbidden: true}}}
	attribute := schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "unit": leaf, "format": leaf, "decimals": leaf, "suffix": leaf, "color": leaf, "broadcastMessage": leaf, "display": display, "value": {forbidden: true}}}
	item := schemaNode{fields: map[string]schemaNode{"giftId": leaf, "name": leaf, "target": leaf, "barStyle": leaf, "received": {forbidden: true}, "imageUrl": {forbidden: true}}}
	panel := schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "layout": leaf, "items": {array: &item}}}
	milestone := schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "attributeId": leaf, "comparison": leaf, "threshold": leaf, "action": leaf, "message": leaf}}
	activity := schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "attributeIds": {array: &leaf}, "sceneId": leaf, "resultMode": leaf, "gateRules": leaf, "initialValues": leaf, "milestones": {array: &milestone}, "giftTimeout": {fields: map[string]schemaNode{"seconds": leaf, "action": leaf}}}}
	rule := schemaNode{fields: map[string]schemaNode{"id": leaf, "giftId": leaf, "attributeId": leaf, "formulaName": leaf, "condition": leaf, "formula": leaf, "enabled": leaf, "matchGiftIds": {array: &leaf}, "minPrice": leaf, "cap": leaf, "dailyLimit": leaf}}
	timerRule := schemaNode{fields: map[string]schemaNode{"id": leaf, "attributeId": leaf, "formulaName": leaf, "intervalSeconds": leaf, "condition": leaf, "formula": leaf, "enabled": leaf}}
	preset := schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "context": leaf, "formula": leaf, "attributeId": leaf}}
	overtimeAction := schemaNode{fields: map[string]schemaNode{"giftId": leaf, "operation": leaf, "seconds": leaf}}
	simplePlay := schemaNode{fields: map[string]schemaNode{"version": leaf, "templateId": leaf, "templateVersion": leaf, "attributeId": leaf, "parameters": leaf, "gifts": leaf, "overtimeGiftActions": {array: &overtimeAction}, "managedFingerprint": leaf}}
	gift := schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "price": leaf, "coinType": leaf, "effectId": leaf, "blindBoxParentId": leaf, "blindBoxParentName": leaf, "blindBoxParentPrice": leaf, "imageUrl": {forbidden: true}, "imgBasic": {forbidden: true}, "gif": {forbidden: true}, "webp": {forbidden: true}, "effectMp4": {forbidden: true}, "effectMp4Json": {forbidden: true}}}
	definition := schemaNode{fields: map[string]schemaNode{"attributes": {array: &attribute}, "displayScenes": {array: &schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "attributeIds": {array: &leaf}, "layout": leaf, "themeId": leaf, "appearance": {forbidden: true}}}}, "giftTargetPanels": {array: &panel}, "activities": {array: &activity}, "rules": {array: &rule}, "timerRules": {array: &timerRule}, "formulaPresets": {array: &preset}, "simplePlay": simplePlay, "gifts": {array: &gift}, "appearance": {forbidden: true}, "blindBoxDisplay": {forbidden: true}}}
	runtimeMilestone := schemaNode{fields: map[string]schemaNode{"id": leaf, "triggeredAtMillis": leaf, "triggerValue": leaf}}
	runtimeActivity := schemaNode{fields: map[string]schemaNode{"id": leaf, "status": leaf, "startedAtMillis": leaf, "lockedAtMillis": leaf, "settledAtMillis": leaf, "result": {fields: map[string]schemaNode{"winnerAttributeId": leaf, "values": leaf}}, "milestones": {array: &runtimeMilestone}, "giftTimeout": {fields: map[string]schemaNode{"lastGiftAtMillis": leaf, "deadlineAtMillis": leaf}}}}
	runtime := schemaNode{fields: map[string]schemaNode{"attributeValues": leaf, "giftTargetReceived": {array: &schemaNode{fields: map[string]schemaNode{"panelId": leaf, "giftId": leaf, "received": leaf}}}, "activities": {array: &runtimeActivity}, "ruleLimits": leaf}}
	payload := schemaNode{fields: map[string]schemaNode{"roomSuggestion": leaf, "definition": definition, "runtime": runtime}}
	if version == 2 {
		crop := schemaNode{fields: map[string]schemaNode{"id": leaf, "crop": {fields: map[string]schemaNode{"x": leaf, "y": leaf, "width": leaf, "height": leaf}}}}
		declarationUnit := schemaNode{fields: map[string]schemaNode{"id": leaf, "kind": leaf, "name": leaf, "attributeIds": {array: &leaf}, "ruleIds": {array: &leaf}, "timerRuleIds": {array: &leaf}, "formulaPresetIds": {array: &leaf}, "activityIds": {array: &leaf}, "displaySceneIds": {array: &leaf}, "giftTargetPanelIds": {array: &leaf}, "giftIds": {array: &leaf}, "cropPresetIds": {array: &leaf}}}
		declarationGroup := schemaNode{fields: map[string]schemaNode{"id": leaf, "unitIds": {array: &leaf}, "reasons": {array: &schemaNode{fields: map[string]schemaNode{"kind": leaf, "referenceId": leaf}}}}}
		payload.fields["generalSettings"] = schemaNode{fields: map[string]schemaNode{"configurationMode": leaf}}
		payload.fields["cropPresets"] = schemaNode{array: &crop}
		payload.fields["rejectedCropPresets"] = schemaNode{array: &schemaNode{fields: map[string]schemaNode{"reason": leaf, "count": leaf}}}
		payload.fields["dependencyDeclaration"] = schemaNode{fields: map[string]schemaNode{"algorithmVersion": leaf, "units": {array: &declarationUnit}, "groups": {array: &declarationGroup}}}
	}
	return schemaNode{fields: map[string]schemaNode{"kind": leaf, "migrationVersion": leaf, "source": {fields: map[string]schemaNode{"appVersion": leaf, "configSchemaVersion": leaf}}, "exportedAt": leaf, "payload": payload}}
}
func scanNode(raw []byte, pointer string, schema schemaNode, report *Report) {
	if schema.forbidden {
		report.Ignored = append(report.Ignored, pointer)
		report.Warnings = append(report.Warnings, "desktop-only field removed: "+pointer)
		return
	}
	if schema.array != nil {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return
		}
		for index, value := range values {
			scanNode(value, pointer+"/"+strconv.Itoa(index), *schema.array, report)
		}
		return
	}
	if schema.fields == nil {
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return
	}
	for key, value := range object {
		child, exists := schema.fields[key]
		path := pointer + "/" + escapePointer(key)
		if !exists {
			report.Ignored = append(report.Ignored, path)
			if sensitiveKey(key) {
				report.Warnings = append(report.Warnings, "sensitive field removed: "+path)
			}
			continue
		}
		scanNode(value, path, child, report)
	}
}
func (report *Report) finalize() {
	sort.Strings(report.Ignored)
	sort.Strings(report.Warnings)
	report.Ignored = uniqueStrings(report.Ignored)
	report.Warnings = uniqueStrings(report.Warnings)
}
func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
func sensitiveKey(key string) bool {
	switch strings.ToLower(key) {
	case "cookie", "token", "uid", "senderuid", "uname", "imageurl", "imgbasic", "gif", "webp", "effectmp4", "effectmp4json", "recentgifts", "stats", "log", "giftreceipts", "contributions", "giftclipcrops", "autoupdate", "tutorialcompletedlessons":
		return true
	}
	return false
}
