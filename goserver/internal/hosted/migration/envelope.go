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
	"sort"
	"strconv"
	"strings"
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
	Definition     configuration.Definition
	Runtime        configuration.RuntimeState
	RoomSuggestion string
	Source         Source
	Counts         Counts
	Report         Report
	CanonicalJSON  []byte
	Hash           [sha256.Size]byte
}

type Source struct {
	AppVersion                 string
	ConfigurationSchemaVersion int
}

type Counts struct {
	Attributes, Rules, Activities, GiftTargetPanels, GiftTargetItems int
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
	RoomSuggestion *string        `json:"roomSuggestion"`
	Definition     wireDefinition `json:"definition"`
	Runtime        wireRuntime    `json:"runtime"`
}
type wireDefinition struct {
	Attributes       []wireAttribute                `json:"attributes"`
	DisplayScenes    []gameplay.DisplayScene        `json:"displayScenes"`
	GiftTargetPanels []wireGiftTargetPanel          `json:"giftTargetPanels"`
	Activities       []wireActivity                 `json:"activities"`
	Rules            []gameplay.Rule                `json:"rules"`
	TimerRules       []gameplay.TimerRule           `json:"timerRules"`
	FormulaPresets   []gameplay.FormulaPreset       `json:"formulaPresets"`
	SimplePlay       *wireSimplePlay                `json:"simplePlay"`
	Gifts            []configuration.GiftDefinition `json:"gifts"`
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
	scanNode(raw, "", envelopeSchema(), &report)
	var wire wireEnvelope
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Kind != "gift-panel-online-migration" || wire.MigrationVersion != 1 || wire.ExportedAt == "" || wire.Source.AppVersion == "" || wire.Source.ConfigurationSchemaVersion < 1 || wire.Source.ConfigurationSchemaVersion > configurationSchemaVersion {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	if err := validateWire(wire); err != nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	definition, runtime, err := normalizeWire(wire, &report)
	if err != nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	canonical, err := json.Marshal(struct {
		Definition configuration.Definition   `json:"definition"`
		Runtime    configuration.RuntimeState `json:"runtime"`
	}{definition, runtime})
	if err != nil {
		return Envelope{}, Report{}, ErrInvalidEnvelope
	}
	roomSuggestion := ""
	if wire.Payload.RoomSuggestion != nil {
		roomSuggestion = *wire.Payload.RoomSuggestion
	}
	report.Counts = countWire(wire.Payload.Definition)
	report.finalize()
	return Envelope{Definition: definition, Runtime: runtime, RoomSuggestion: roomSuggestion, Source: Source{AppVersion: wire.Source.AppVersion, ConfigurationSchemaVersion: wire.Source.ConfigurationSchemaVersion}, Counts: report.Counts, Report: report, CanonicalJSON: canonical, Hash: sha256.Sum256(canonical)}, report, nil
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
	definition := configuration.Definition{Attributes: make([]configuration.AttributeDefinition, len(wire.Payload.Definition.Attributes)), DisplayScenes: wire.Payload.Definition.DisplayScenes, GiftTargetPanels: make([]configuration.GiftTargetPanelDefinition, len(wire.Payload.Definition.GiftTargetPanels)), Activities: make([]configuration.ActivityDefinition, len(wire.Payload.Definition.Activities)), Rules: wire.Payload.Definition.Rules, TimerRules: wire.Payload.Definition.TimerRules, FormulaPresets: wire.Payload.Definition.FormulaPresets, Gifts: wire.Payload.Definition.Gifts}
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
	min, max float64
	integer  bool
	choices  map[string]struct{}
}
type simpleTemplate struct {
	parameters map[string]simpleParameter
	slot       string
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
			}
		}
		parameters[key] = value
	}
	for slot := range wire.Gifts {
		if slot != template.slot {
			reportIgnored(report, "/payload/definition/simplePlay/gifts/"+escapePointer(slot))
		}
	}
	knownGifts := make(map[int]struct{}, len(catalog))
	for _, gift := range catalog {
		knownGifts[gift.ID] = struct{}{}
	}
	giftIDs := make([]int, 0)
	seen := make(map[int]struct{})
	for index, giftID := range wire.Gifts[template.slot] {
		if giftID <= 0 {
			reportIgnored(report, "/payload/definition/simplePlay/gifts/"+template.slot+"/"+strconv.Itoa(index))
			continue
		}
		if _, exists := knownGifts[giftID]; !exists {
			reportIgnored(report, "/payload/definition/simplePlay/gifts/"+template.slot+"/"+strconv.Itoa(index))
			continue
		}
		if _, duplicate := seen[giftID]; duplicate {
			reportIgnored(report, "/payload/definition/simplePlay/gifts/"+template.slot+"/"+strconv.Itoa(index))
			continue
		}
		seen[giftID] = struct{}{}
		giftIDs = append(giftIDs, giftID)
	}
	if len(giftIDs) == 0 {
		reportIgnored(report, "/payload/definition/simplePlay/gifts/"+template.slot)
		return nil, nil
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
	return &gameplay.SimplePlay{Version: 1, TemplateID: wire.TemplateID, TemplateVersion: wire.TemplateVersion, AttributeID: wire.AttributeID, Parameters: parameters, Gifts: map[string][]int{template.slot: giftIDs}, OvertimeGiftActions: actions, ManagedFingerprint: wire.ManagedFingerprint}, nil
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
	switch {
	case id == "overtime" && version == 1:
		return simpleTemplate{slot: "overtime", parameters: map[string]simpleParameter{"name": text("加班时间"), "minutesPerYuan": number(60, 1, 3600, false), "maxHours": number(0, 0, 240, false), "broadcastMessage": broadcast}}, true
	case id == "overtime" && version == 2:
		return simpleTemplate{slot: "overtime", actions: true, parameters: map[string]simpleParameter{"name": text("加班时间"), "maxSeconds": number(0, 0, 864000, true), "broadcastMessage": broadcast}}, true
	case id == "counter" && version == 1:
		return simpleTemplate{slot: "count", parameters: map[string]simpleParameter{"name": text("挑战次数"), "suffix": selectValue("次", "次", "局", "个", "组", "分"), "amount": number(1, .01, 100000, false), "cap": number(0, 0, 1000000, false), "broadcastMessage": broadcast}}, true
	case id == "goal" && version == 1:
		return simpleTemplate{slot: "progress", parameters: map[string]simpleParameter{"name": text("目标进度"), "target": number(100, 1, 100000000, false), "perYuan": number(1, .01, 100000, false), "broadcastMessage": broadcast}}, true
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
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= rule.min && number <= rule.max && (!rule.integer || math.Trunc(number) == number)
}
func safeSimpleText(value string) bool {
	lower := strings.ToLower(value)
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= maximumStringRunes && !strings.Contains(value, "//") && !strings.Contains(value, "\\") && !strings.Contains(value, "/") && !strings.Contains(lower, "http:") && !strings.Contains(lower, "https:") && !strings.Contains(lower, "file:") && !strings.Contains(lower, "data:") && !strings.Contains(lower, "blob:") && !strings.Contains(lower, "javascript:") && !strings.Contains(lower, "vbscript:")
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

func envelopeSchema() schemaNode {
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
	gift := schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "price": leaf, "coinType": leaf, "blindBoxParentId": leaf, "blindBoxParentName": leaf, "blindBoxParentPrice": leaf, "imageUrl": {forbidden: true}, "imgBasic": {forbidden: true}, "gif": {forbidden: true}, "webp": {forbidden: true}, "effectMp4": {forbidden: true}, "effectMp4Json": {forbidden: true}}}
	definition := schemaNode{fields: map[string]schemaNode{"attributes": {array: &attribute}, "displayScenes": {array: &schemaNode{fields: map[string]schemaNode{"id": leaf, "name": leaf, "attributeIds": {array: &leaf}, "layout": leaf, "themeId": leaf, "appearance": {forbidden: true}}}}, "giftTargetPanels": {array: &panel}, "activities": {array: &activity}, "rules": {array: &rule}, "timerRules": {array: &timerRule}, "formulaPresets": {array: &preset}, "simplePlay": simplePlay, "gifts": {array: &gift}, "appearance": {forbidden: true}, "blindBoxDisplay": {forbidden: true}}}
	runtimeMilestone := schemaNode{fields: map[string]schemaNode{"id": leaf, "triggeredAtMillis": leaf, "triggerValue": leaf}}
	runtimeActivity := schemaNode{fields: map[string]schemaNode{"id": leaf, "status": leaf, "startedAtMillis": leaf, "lockedAtMillis": leaf, "settledAtMillis": leaf, "result": {fields: map[string]schemaNode{"winnerAttributeId": leaf, "values": leaf}}, "milestones": {array: &runtimeMilestone}, "giftTimeout": {fields: map[string]schemaNode{"lastGiftAtMillis": leaf, "deadlineAtMillis": leaf}}}}
	runtime := schemaNode{fields: map[string]schemaNode{"attributeValues": leaf, "giftTargetReceived": {array: &schemaNode{fields: map[string]schemaNode{"panelId": leaf, "giftId": leaf, "received": leaf}}}, "activities": {array: &runtimeActivity}, "ruleLimits": leaf}}
	return schemaNode{fields: map[string]schemaNode{"kind": leaf, "migrationVersion": leaf, "source": {fields: map[string]schemaNode{"appVersion": leaf, "configSchemaVersion": leaf}}, "exportedAt": leaf, "payload": {fields: map[string]schemaNode{"roomSuggestion": leaf, "definition": definition, "runtime": runtime}}}}
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
