// Package gameplay contains the identity-free state and event model shared by
// the desktop adapter and the hosted service. It deliberately excludes
// ingestion, viewer, receipt, logging, and persistence concerns.
package gameplay

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"
)

const (
	maxAttributes   = 200
	maxRules        = 500
	maxTimerRules   = 100
	maxActivities   = 100
	maxGiftPanels   = 100
	maxPanelItems   = 200
	maxStringRunes  = 4096
	maxDynamicDepth = 64
	maxDynamicNodes = 10000
)

// Engine applies pure gameplay transitions. Its operations are implemented in
// engine.go so callers never need access to storage or live event plumbing.
type Engine struct{}

// Snapshot is the complete, identity-free gameplay state at one instant.
type Snapshot struct {
	RoomID           string            `json:"roomId"`
	Attributes       []Attribute       `json:"attributes"`
	DisplayScenes    []DisplayScene    `json:"displayScenes"`
	GiftTargetPanels []GiftTargetPanel `json:"giftTargetPanels"`
	Activities       []Activity        `json:"activities"`
	Rules            []Rule            `json:"rules"`
	TimerRules       []TimerRule       `json:"timerRules"`
	FormulaPresets   []FormulaPreset   `json:"formulaPresets"`
	SimplePlay       *SimplePlay       `json:"simplePlay,omitempty"`
	Gifts            []GiftInfo        `json:"gifts"`
}

type Attribute struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Value            float64  `json:"value"`
	Unit             string   `json:"unit"`
	Format           string   `json:"format"`
	Decimals         int      `json:"decimals"`
	Suffix           string   `json:"suffix"`
	Color            string   `json:"color,omitempty"`
	BroadcastMessage string   `json:"broadcastMessage,omitempty"`
	Display          *Display `json:"display,omitempty"`
}

type Display struct {
	Variant       string         `json:"variant"`
	ThemeID       string         `json:"themeId,omitempty"`
	Title         string         `json:"title,omitempty"`
	Min           *float64       `json:"min,omitempty"`
	Max           *float64       `json:"max,omitempty"`
	LowThreshold  *float64       `json:"lowThreshold,omitempty"`
	LeftLabel     string         `json:"leftLabel,omitempty"`
	RightLabel    string         `json:"rightLabel,omitempty"`
	ValueMappings []ValueMapping `json:"valueMappings,omitempty"`
}

type ValueMapping struct {
	Value float64 `json:"value"`
	Label string  `json:"label"`
	Color string  `json:"color,omitempty"`
}

type DisplayScene struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	AttributeIDs []string `json:"attributeIds"`
	Layout       string   `json:"layout"`
	ThemeID      string   `json:"themeId"`
}

type GiftTargetPanel struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Layout string           `json:"layout"`
	Items  []GiftTargetItem `json:"items"`
}

type GiftTargetItem struct {
	GiftID   int    `json:"giftId"`
	Name     string `json:"name,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
	Target   int    `json:"target"`
	Received int    `json:"received"`
	BarStyle string `json:"barStyle"`
}

type Activity struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	AttributeIDs    []string           `json:"attributeIds"`
	SceneID         string             `json:"sceneId,omitempty"`
	Status          string             `json:"status"`
	ResultMode      string             `json:"resultMode"`
	GateRules       bool               `json:"gateRules"`
	InitialValues   map[string]float64 `json:"initialValues"`
	Milestones      []Milestone        `json:"milestones"`
	GiftTimeout     *GiftTimeout       `json:"giftTimeout,omitempty"`
	StartedAtMillis int64              `json:"startedAtMillis,omitempty"`
	LockedAtMillis  int64              `json:"lockedAtMillis,omitempty"`
	SettledAtMillis int64              `json:"settledAtMillis,omitempty"`
	Result          *ActivityResult    `json:"result,omitempty"`
}

type Milestone struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	AttributeID       string   `json:"attributeId"`
	Comparison        string   `json:"comparison"`
	Threshold         float64  `json:"threshold"`
	Action            string   `json:"action"`
	Message           string   `json:"message"`
	TriggeredAtMillis int64    `json:"triggeredAtMillis,omitempty"`
	TriggerValue      *float64 `json:"triggerValue,omitempty"`
}

type GiftTimeout struct {
	Seconds          int    `json:"seconds"`
	Action           string `json:"action"`
	LastGiftAtMillis int64  `json:"lastGiftAtMillis,omitempty"`
	DeadlineAtMillis int64  `json:"deadlineAtMillis,omitempty"`
}

type ActivityResult struct {
	WinnerAttributeID string             `json:"winnerAttributeId,omitempty"`
	Values            map[string]float64 `json:"values"`
}

type Rule struct {
	ID           string   `json:"id"`
	GiftID       int      `json:"giftId"`
	AttributeID  string   `json:"attributeId"`
	FormulaName  string   `json:"formulaName,omitempty"`
	Condition    string   `json:"condition,omitempty"`
	Formula      string   `json:"formula"`
	Enabled      *bool    `json:"enabled,omitempty"`
	MatchGiftIDs []int    `json:"matchGiftIds,omitempty"`
	MinPrice     *float64 `json:"minPrice,omitempty"`
	Cap          *float64 `json:"cap,omitempty"`
	DailyLimit   *int     `json:"dailyLimit,omitempty"`
}

type TimerRule struct {
	ID              string `json:"id"`
	AttributeID     string `json:"attributeId"`
	FormulaName     string `json:"formulaName"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Condition       string `json:"condition,omitempty"`
	Formula         string `json:"formula"`
	Enabled         bool   `json:"enabled"`
}

type FormulaPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Context     string `json:"context"`
	Formula     string `json:"formula"`
	AttributeID string `json:"attributeId"`
}

// GiftInfo is a catalog entry referenced by rules and target panels.
type GiftInfo struct {
	ID                  int     `json:"id"`
	Name                string  `json:"name"`
	Price               float64 `json:"price"`
	CoinType            string  `json:"coinType"`
	ImageURL            string  `json:"imageUrl,omitempty"`
	AnimationGIF        string  `json:"gif,omitempty"`
	AnimationWebP       string  `json:"webp,omitempty"`
	AnimationDurationMS int     `json:"animationDurationMs,omitempty"`
	EffectID            int     `json:"effectId,omitempty"`
	EffectMP4           string  `json:"effectMp4,omitempty"`
	EffectMP4JSON       string  `json:"effectMp4Json,omitempty"`
	BlindBoxParentID    int     `json:"blindBoxParentId,omitempty"`
	BlindBoxParentName  string  `json:"blindBoxParentName,omitempty"`
	BlindBoxParentPrice float64 `json:"blindBoxParentPrice,omitempty"`
}

// Gift is the identity-free input to a gift transition.
type Gift struct {
	GiftID       int     `json:"giftId"`
	BlindGiftID  int     `json:"blindGiftId,omitempty"`
	Count        int     `json:"count"`
	Price        float64 `json:"price"`
	IdentityRank int     `json:"identityRank"`
	EventID      string  `json:"eventId,omitempty"`
}

// Effect is one observable gameplay state update made by a transition.
type Effect struct {
	RuleID        string          `json:"ruleId,omitempty"`
	AttributeName string          `json:"attributeName,omitempty"`
	Delta         float64         `json:"delta"`
	ValueAfter    float64         `json:"valueAfter"`
	TriggerName   string          `json:"triggerName,omitempty"`
	Target        *TargetNotice   `json:"target,omitempty"`
	Activity      *ActivityNotice `json:"activity,omitempty"`
}

// TargetNotice identifies one gift-target counter affected by a transition.
type TargetNotice struct {
	PanelID  string `json:"panelId"`
	GiftID   int    `json:"giftId"`
	Received int    `json:"received"`
	Target   int    `json:"target"`
}

// ActivityNotice identifies an activity state change made by a transition.
type ActivityNotice struct {
	ActivityID  string `json:"activityId"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	MilestoneID string `json:"milestoneId,omitempty"`
}

type Transition struct {
	Next    Snapshot `json:"next"`
	Effects []Effect `json:"effects"`
	Changed bool     `json:"changed"`
}

type SimplePlay struct {
	Version             int                  `json:"version"`
	TemplateID          string               `json:"templateId"`
	TemplateVersion     int                  `json:"templateVersion"`
	AttributeID         string               `json:"attributeId"`
	Parameters          map[string]any       `json:"parameters"`
	Gifts               map[string][]int     `json:"gifts"`
	OvertimeGiftActions []OvertimeGiftAction `json:"overtimeGiftActions,omitempty"`
	ManagedFingerprint  string               `json:"managedFingerprint"`
}

type OvertimeGiftAction struct {
	GiftID    int    `json:"giftId"`
	Operation string `json:"operation"`
	Seconds   *int   `json:"seconds,omitempty"`
}

// Normalize validates the bounded public model and returns a detached copy.
func Normalize(snapshot Snapshot) (Snapshot, error) {
	if len(snapshot.Attributes) > maxAttributes {
		return Snapshot{}, fmt.Errorf("attributes exceed %d", maxAttributes)
	}
	if len(snapshot.Rules) > maxRules {
		return Snapshot{}, fmt.Errorf("rules exceed %d", maxRules)
	}
	if len(snapshot.TimerRules) > maxTimerRules {
		return Snapshot{}, fmt.Errorf("timer rules exceed %d", maxTimerRules)
	}
	if len(snapshot.Activities) > maxActivities {
		return Snapshot{}, fmt.Errorf("activities exceed %d", maxActivities)
	}
	if len(snapshot.GiftTargetPanels) > maxGiftPanels {
		return Snapshot{}, fmt.Errorf("gift target panels exceed %d", maxGiftPanels)
	}
	for _, panel := range snapshot.GiftTargetPanels {
		if len(panel.Items) > maxPanelItems {
			return Snapshot{}, fmt.Errorf("gift target panel items exceed %d", maxPanelItems)
		}
	}
	if err := validateSnapshotValues(snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueStringIDs("attribute", attributeIDs(snapshot.Attributes)); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueStringIDs("display scene", displaySceneIDs(snapshot.DisplayScenes)); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueStringIDs("rule", ruleIDs(snapshot.Rules)); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueStringIDs("timer rule", timerRuleIDs(snapshot.TimerRules)); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueStringIDs("activity", activityIDs(snapshot.Activities)); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueStringIDs("gift target panel", giftTargetPanelIDs(snapshot.GiftTargetPanels)); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueStringIDs("formula preset", formulaPresetIDs(snapshot.FormulaPresets)); err != nil {
		return Snapshot{}, err
	}
	if err := validateMilestoneIDs(snapshot.Activities); err != nil {
		return Snapshot{}, err
	}
	if err := validateUniqueGiftIDs(snapshot.Gifts); err != nil {
		return Snapshot{}, err
	}

	return deepCopySnapshot(snapshot)
}

func deepCopySnapshot(snapshot Snapshot) (Snapshot, error) {
	value, err := cloneValue(reflect.ValueOf(snapshot), "snapshot", &cloneState{active: map[validationVisit]struct{}{}})
	if err != nil {
		return Snapshot{}, err
	}
	return value.Interface().(Snapshot), nil
}

type cloneState struct {
	active map[validationVisit]struct{}
}

func cloneValue(value reflect.Value, path string, state *cloneState) (reflect.Value, error) {
	if !value.IsValid() {
		return value, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		copy, err := cloneValue(value.Elem(), path, state)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(copy)
		return result, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		leave, err := state.enter(value, path)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()
		copy, err := cloneValue(value.Elem(), path, state)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(copy)
		return result, nil
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.NumField(); index++ {
			if !result.Field(index).CanSet() || !value.Field(index).CanInterface() {
				return reflect.Value{}, fmt.Errorf("%s contains unsupported dynamic type %s", path, value.Type())
			}
			copy, err := cloneValue(value.Field(index), path, state)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Field(index).Set(copy)
		}
		return result, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		leave, err := state.enter(value, path)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			key, err := cloneValue(iter.Key(), path, state)
			if err != nil {
				return reflect.Value{}, err
			}
			item, err := cloneValue(iter.Value(), path, state)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(key, item)
		}
		return result, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		leave, err := state.enter(value, path)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			item, err := cloneValue(value.Index(index), path, state)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(item)
		}
		return result, nil
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			item, err := cloneValue(value.Index(index), path, state)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(item)
		}
		return result, nil
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.String:
		return value, nil
	default:
		return reflect.Value{}, fmt.Errorf("%s contains unsupported dynamic type %s", path, value.Type())
	}
}

func (state *cloneState) enter(value reflect.Value, path string) (func(), error) {
	visit := validationVisit{typ: value.Type(), kind: value.Kind(), pointer: value.Pointer()}
	if _, exists := state.active[visit]; exists {
		return nil, fmt.Errorf("%s contains a cycle", path)
	}
	state.active[visit] = struct{}{}
	return func() { delete(state.active, visit) }, nil
}

func validateSnapshotValues(snapshot Snapshot) error {
	state := validationState{active: map[validationVisit]struct{}{}}
	if err := validateValue(reflect.ValueOf(snapshot), "snapshot", &state); err != nil {
		return err
	}
	if snapshot.SimplePlay == nil {
		return nil
	}
	return validateDynamicValue(reflect.ValueOf(snapshot.SimplePlay.Parameters), "simplePlay.parameters", 0, &dynamicValidationState{active: map[validationVisit]struct{}{}})
}

type validationVisit struct {
	typ     reflect.Type
	kind    reflect.Kind
	pointer uintptr
}

type validationState struct {
	active map[validationVisit]struct{}
}

func validateValue(value reflect.Value, path string, state *validationState) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		if value.Kind() == reflect.Pointer {
			leave, err := state.enter(value, path)
			if err != nil {
				return err
			}
			defer leave()
		}
		return validateValue(value.Elem(), path, state)
	}
	switch value.Kind() {
	case reflect.String:
		if utf8.RuneCountInString(value.String()) > maxStringRunes {
			return fmt.Errorf("%s exceeds %d runes", path, maxStringRunes)
		}
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return fmt.Errorf("%s must be finite", path)
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if err := validateValue(value.Field(index), path, state); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var leave func()
		if value.Kind() == reflect.Slice && !value.IsNil() {
			var err error
			leave, err = state.enter(value, path)
			if err != nil {
				return err
			}
			defer leave()
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateValue(value.Index(index), path, state); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		leave, err := state.enter(value, path)
		if err != nil {
			return err
		}
		defer leave()
		iter := value.MapRange()
		for iter.Next() {
			if err := validateValue(iter.Key(), path, state); err != nil {
				return err
			}
			if err := validateValue(iter.Value(), path, state); err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *validationState) enter(value reflect.Value, path string) (func(), error) {
	visit := validationVisit{typ: value.Type(), kind: value.Kind(), pointer: value.Pointer()}
	if _, exists := state.active[visit]; exists {
		return nil, fmt.Errorf("%s contains a cycle", path)
	}
	state.active[visit] = struct{}{}
	return func() { delete(state.active, visit) }, nil
}

type dynamicValidationState struct {
	active map[validationVisit]struct{}
	nodes  int
}

func validateDynamicValue(value reflect.Value, path string, depth int, state *dynamicValidationState) error {
	if depth > maxDynamicDepth {
		return fmt.Errorf("%s exceeds maximum nesting depth", path)
	}
	state.nodes++
	if state.nodes > maxDynamicNodes {
		return fmt.Errorf("%s exceeds maximum node count", path)
	}
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateDynamicValue(value.Elem(), path, depth, state)
	}
	switch value.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return nil
	case reflect.String:
		if utf8.RuneCountInString(value.String()) > maxStringRunes {
			return fmt.Errorf("%s exceeds %d runes", path, maxStringRunes)
		}
		return nil
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return fmt.Errorf("%s must be finite", path)
		}
		return nil
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		leave, err := state.enter(value, path)
		if err != nil {
			return err
		}
		defer leave()
		return validateDynamicValue(value.Elem(), path, depth+1, state)
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		leave, err := state.enter(value, path)
		if err != nil {
			return err
		}
		defer leave()
		for index := 0; index < value.Len(); index++ {
			if err := validateDynamicValue(value.Index(index), path, depth+1, state); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateDynamicValue(value.Index(index), path, depth+1, state); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("%s contains unsupported dynamic type %s", path, value.Type())
		}
		if value.IsNil() {
			return nil
		}
		leave, err := state.enter(value, path)
		if err != nil {
			return err
		}
		defer leave()
		iter := value.MapRange()
		for iter.Next() {
			if err := validateDynamicValue(iter.Key(), path, depth+1, state); err != nil {
				return err
			}
			if err := validateDynamicValue(iter.Value(), path, depth+1, state); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%s contains unsupported dynamic type %s", path, value.Type())
	}
}

func (state *dynamicValidationState) enter(value reflect.Value, path string) (func(), error) {
	visit := validationVisit{typ: value.Type(), kind: value.Kind(), pointer: value.Pointer()}
	if _, exists := state.active[visit]; exists {
		return nil, fmt.Errorf("%s contains a cycle", path)
	}
	state.active[visit] = struct{}{}
	return func() { delete(state.active, visit) }, nil
}

func validateUniqueStringIDs(kind string, ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%s ID is blank", kind)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate %s ID %q", kind, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateMilestoneIDs(activities []Activity) error {
	for _, activity := range activities {
		ids := make([]string, len(activity.Milestones))
		for index, milestone := range activity.Milestones {
			ids[index] = milestone.ID
		}
		if err := validateUniqueStringIDs("milestone", ids); err != nil {
			return err
		}
	}
	return nil
}

func validateUniqueGiftIDs(gifts []GiftInfo) error {
	seen := make(map[int]struct{}, len(gifts))
	for _, gift := range gifts {
		if gift.ID < 1 {
			return fmt.Errorf("gift catalog ID must be positive")
		}
		if _, exists := seen[gift.ID]; exists {
			return fmt.Errorf("duplicate gift catalog ID %d", gift.ID)
		}
		seen[gift.ID] = struct{}{}
	}
	return nil
}

func attributeIDs(values []Attribute) []string {
	ids := make([]string, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}

func displaySceneIDs(values []DisplayScene) []string {
	ids := make([]string, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}

func ruleIDs(values []Rule) []string {
	ids := make([]string, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}

func timerRuleIDs(values []TimerRule) []string {
	ids := make([]string, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}

func activityIDs(values []Activity) []string {
	ids := make([]string, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}

func giftTargetPanelIDs(values []GiftTargetPanel) []string {
	ids := make([]string, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}

func formulaPresetIDs(values []FormulaPreset) []string {
	ids := make([]string, len(values))
	for index, value := range values {
		ids[index] = value.ID
	}
	return ids
}
