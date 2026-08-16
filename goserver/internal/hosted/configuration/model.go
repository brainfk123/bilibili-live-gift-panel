// Package configuration persists immutable gameplay definitions separately
// from the current, optimistic runtime state of a hosted account.
package configuration

import (
	"errors"
	"fmt"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
)

var ErrRevisionConflict = errors.New("configuration revision conflict")

// Version is an immutable, account-owned definition revision.
type Version struct {
	ID         int64
	AccountID  int64
	Number     uint64
	Definition Definition
	Source     string
	CreatedAt  time.Time
}

// State is the current mutable gameplay state for an account.
type State struct {
	AccountID       int64
	ConfigVersionID int64
	Revision        uint64
	Runtime         RuntimeState
	UpdatedAt       time.Time
}

// Definition is the room-independent, executable part of a gameplay
// snapshot. It deliberately contains neither current values nor asset URLs.
type Definition struct {
	Attributes       []AttributeDefinition       `json:"attributes"`
	DisplayScenes    []gameplay.DisplayScene     `json:"displayScenes"`
	GiftTargetPanels []GiftTargetPanelDefinition `json:"giftTargetPanels"`
	Activities       []ActivityDefinition        `json:"activities"`
	Rules            []gameplay.Rule             `json:"rules"`
	TimerRules       []gameplay.TimerRule        `json:"timerRules"`
	FormulaPresets   []gameplay.FormulaPreset    `json:"formulaPresets"`
	SimplePlay       *gameplay.SimplePlay        `json:"simplePlay,omitempty"`
	Gifts            []GiftDefinition            `json:"gifts"`
}

type AttributeDefinition struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Unit             string            `json:"unit"`
	Format           string            `json:"format"`
	Decimals         int               `json:"decimals"`
	Suffix           string            `json:"suffix"`
	Color            string            `json:"color,omitempty"`
	BroadcastMessage string            `json:"broadcastMessage,omitempty"`
	Display          *gameplay.Display `json:"display,omitempty"`
}

type GiftTargetPanelDefinition struct {
	ID     string                     `json:"id"`
	Name   string                     `json:"name"`
	Layout string                     `json:"layout"`
	Items  []GiftTargetItemDefinition `json:"items"`
}

type GiftTargetItemDefinition struct {
	GiftID   int    `json:"giftId"`
	Name     string `json:"name,omitempty"`
	Target   int    `json:"target"`
	BarStyle string `json:"barStyle"`
}

// GiftDefinition is the allowlisted catalog metadata needed by gameplay.
// Asset URLs are resolved by the hosted Bilibili gateway and never persisted.
type GiftDefinition struct {
	ID                  int     `json:"id"`
	Name                string  `json:"name"`
	Price               float64 `json:"price"`
	CoinType            string  `json:"coinType"`
	BlindBoxParentID    int     `json:"blindBoxParentId,omitempty"`
	BlindBoxParentName  string  `json:"blindBoxParentName,omitempty"`
	BlindBoxParentPrice float64 `json:"blindBoxParentPrice,omitempty"`
}

type ActivityDefinition struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	AttributeIDs  []string               `json:"attributeIds"`
	SceneID       string                 `json:"sceneId,omitempty"`
	ResultMode    string                 `json:"resultMode"`
	GateRules     bool                   `json:"gateRules"`
	InitialValues map[string]float64     `json:"initialValues"`
	Milestones    []MilestoneDefinition  `json:"milestones"`
	GiftTimeout   *GiftTimeoutDefinition `json:"giftTimeout,omitempty"`
}

type MilestoneDefinition struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	AttributeID string  `json:"attributeId"`
	Comparison  string  `json:"comparison"`
	Threshold   float64 `json:"threshold"`
	Action      string  `json:"action"`
	Message     string  `json:"message"`
}

type GiftTimeoutDefinition struct {
	Seconds int    `json:"seconds"`
	Action  string `json:"action"`
}

// RuntimeState contains no formulas or other executable definition fields.
type RuntimeState struct {
	AttributeValues    map[string]float64       `json:"attributeValues"`
	GiftTargetReceived []GiftTargetRuntimeState `json:"giftTargetReceived"`
	Activities         []ActivityRuntimeState   `json:"activities"`
	RuleLimits         gameplay.RuleLimitState  `json:"ruleLimits"`
}

type GiftTargetRuntimeState struct {
	PanelID  string `json:"panelId"`
	GiftID   int    `json:"giftId"`
	Received int    `json:"received"`
}

type ActivityRuntimeState struct {
	ID              string                   `json:"id"`
	Status          string                   `json:"status"`
	StartedAtMillis int64                    `json:"startedAtMillis,omitempty"`
	LockedAtMillis  int64                    `json:"lockedAtMillis,omitempty"`
	SettledAtMillis int64                    `json:"settledAtMillis,omitempty"`
	Result          *gameplay.ActivityResult `json:"result,omitempty"`
	Milestones      []MilestoneRuntimeState  `json:"milestones"`
	GiftTimeout     *GiftTimeoutRuntimeState `json:"giftTimeout,omitempty"`
}

type MilestoneRuntimeState struct {
	ID                string   `json:"id"`
	TriggeredAtMillis int64    `json:"triggeredAtMillis,omitempty"`
	TriggerValue      *float64 `json:"triggerValue,omitempty"`
}

type GiftTimeoutRuntimeState struct {
	LastGiftAtMillis int64 `json:"lastGiftAtMillis,omitempty"`
	DeadlineAtMillis int64 `json:"deadlineAtMillis,omitempty"`
}

// Split normalizes and detaches snapshot before projecting it into storage.
func Split(snapshot gameplay.Snapshot) (Definition, RuntimeState, error) {
	normalized, err := gameplay.Normalize(snapshot)
	if err != nil {
		return Definition{}, RuntimeState{}, err
	}

	definition := Definition{
		Attributes:       make([]AttributeDefinition, len(normalized.Attributes)),
		DisplayScenes:    normalized.DisplayScenes,
		GiftTargetPanels: make([]GiftTargetPanelDefinition, len(normalized.GiftTargetPanels)),
		Activities:       make([]ActivityDefinition, len(normalized.Activities)),
		Rules:            normalized.Rules,
		TimerRules:       normalized.TimerRules,
		FormulaPresets:   normalized.FormulaPresets,
		SimplePlay:       normalized.SimplePlay,
		Gifts:            make([]GiftDefinition, len(normalized.Gifts)),
	}
	runtime := RuntimeState{
		AttributeValues:    make(map[string]float64, len(normalized.Attributes)),
		GiftTargetReceived: make([]GiftTargetRuntimeState, 0),
		Activities:         make([]ActivityRuntimeState, len(normalized.Activities)),
		RuleLimits:         normalized.RuleLimits,
	}

	for index, attribute := range normalized.Attributes {
		definition.Attributes[index] = AttributeDefinition{ID: attribute.ID, Name: attribute.Name, Unit: attribute.Unit, Format: attribute.Format, Decimals: attribute.Decimals, Suffix: attribute.Suffix, Color: attribute.Color, BroadcastMessage: attribute.BroadcastMessage, Display: attribute.Display}
		runtime.AttributeValues[attribute.ID] = attribute.Value
	}
	for panelIndex, panel := range normalized.GiftTargetPanels {
		projected := GiftTargetPanelDefinition{ID: panel.ID, Name: panel.Name, Layout: panel.Layout, Items: make([]GiftTargetItemDefinition, len(panel.Items))}
		for itemIndex, item := range panel.Items {
			projected.Items[itemIndex] = GiftTargetItemDefinition{GiftID: item.GiftID, Name: item.Name, Target: item.Target, BarStyle: item.BarStyle}
			runtime.GiftTargetReceived = append(runtime.GiftTargetReceived, GiftTargetRuntimeState{PanelID: panel.ID, GiftID: item.GiftID, Received: item.Received})
		}
		definition.GiftTargetPanels[panelIndex] = projected
	}
	for activityIndex, activity := range normalized.Activities {
		projected := ActivityDefinition{ID: activity.ID, Name: activity.Name, AttributeIDs: activity.AttributeIDs, SceneID: activity.SceneID, ResultMode: activity.ResultMode, GateRules: activity.GateRules, InitialValues: activity.InitialValues, Milestones: make([]MilestoneDefinition, len(activity.Milestones))}
		state := ActivityRuntimeState{ID: activity.ID, Status: activity.Status, StartedAtMillis: activity.StartedAtMillis, LockedAtMillis: activity.LockedAtMillis, SettledAtMillis: activity.SettledAtMillis, Result: activity.Result, Milestones: make([]MilestoneRuntimeState, len(activity.Milestones))}
		if activity.GiftTimeout != nil {
			projected.GiftTimeout = &GiftTimeoutDefinition{Seconds: activity.GiftTimeout.Seconds, Action: activity.GiftTimeout.Action}
			state.GiftTimeout = &GiftTimeoutRuntimeState{LastGiftAtMillis: activity.GiftTimeout.LastGiftAtMillis, DeadlineAtMillis: activity.GiftTimeout.DeadlineAtMillis}
		}
		for milestoneIndex, milestone := range activity.Milestones {
			projected.Milestones[milestoneIndex] = MilestoneDefinition{ID: milestone.ID, Name: milestone.Name, AttributeID: milestone.AttributeID, Comparison: milestone.Comparison, Threshold: milestone.Threshold, Action: milestone.Action, Message: milestone.Message}
			state.Milestones[milestoneIndex] = MilestoneRuntimeState{ID: milestone.ID, TriggeredAtMillis: milestone.TriggeredAtMillis, TriggerValue: milestone.TriggerValue}
		}
		definition.Activities[activityIndex] = projected
		runtime.Activities[activityIndex] = state
	}
	for index, gift := range normalized.Gifts {
		definition.Gifts[index] = GiftDefinition{ID: gift.ID, Name: gift.Name, Price: gift.Price, CoinType: gift.CoinType, BlindBoxParentID: gift.BlindBoxParentID, BlindBoxParentName: gift.BlindBoxParentName, BlindBoxParentPrice: gift.BlindBoxParentPrice}
	}
	if err := validateDefinition(definition); err != nil {
		return Definition{}, RuntimeState{}, err
	}
	return definition, runtime, nil
}

// DefaultRuntime makes the complete zero/idle state required for a newly
// activated definition.
func DefaultRuntime(definition Definition) RuntimeState {
	runtime := RuntimeState{
		AttributeValues:    make(map[string]float64, len(definition.Attributes)),
		GiftTargetReceived: make([]GiftTargetRuntimeState, 0),
		Activities:         make([]ActivityRuntimeState, len(definition.Activities)),
		RuleLimits:         gameplay.RuleLimitState{AppliedCounts: map[string]int{}},
	}
	for _, attribute := range definition.Attributes {
		runtime.AttributeValues[attribute.ID] = 0
	}
	for _, panel := range definition.GiftTargetPanels {
		for _, item := range panel.Items {
			runtime.GiftTargetReceived = append(runtime.GiftTargetReceived, GiftTargetRuntimeState{PanelID: panel.ID, GiftID: item.GiftID})
		}
	}
	for index, activity := range definition.Activities {
		state := ActivityRuntimeState{ID: activity.ID, Status: "not_started", Milestones: make([]MilestoneRuntimeState, len(activity.Milestones))}
		if activity.GiftTimeout != nil {
			state.GiftTimeout = &GiftTimeoutRuntimeState{}
		}
		for milestoneIndex, milestone := range activity.Milestones {
			state.Milestones[milestoneIndex] = MilestoneRuntimeState{ID: milestone.ID}
		}
		runtime.Activities[index] = state
	}
	return runtime
}

// Join verifies that runtime has exactly the keys required by definition and
// returns a detached room-independent snapshot.
func Join(definition Definition, runtime RuntimeState) (gameplay.Snapshot, error) {
	if err := validateDefinition(definition); err != nil {
		return gameplay.Snapshot{}, err
	}
	attributes, err := joinAttributes(definition.Attributes, runtime.AttributeValues)
	if err != nil {
		return gameplay.Snapshot{}, err
	}
	panels, err := joinGiftTargets(definition.GiftTargetPanels, runtime.GiftTargetReceived)
	if err != nil {
		return gameplay.Snapshot{}, err
	}
	activities, err := joinActivities(definition.Activities, runtime.Activities)
	if err != nil {
		return gameplay.Snapshot{}, err
	}
	snapshot := gameplay.Snapshot{Attributes: attributes, DisplayScenes: definition.DisplayScenes, GiftTargetPanels: panels, Activities: activities, Rules: definition.Rules, TimerRules: definition.TimerRules, FormulaPresets: definition.FormulaPresets, SimplePlay: definition.SimplePlay, Gifts: joinGifts(definition.Gifts), RuleLimits: runtime.RuleLimits}
	return gameplay.Normalize(snapshot)
}

func validateDefinition(definition Definition) error {
	for _, panel := range definition.GiftTargetPanels {
		giftIDs := make(map[int]struct{}, len(panel.Items))
		for _, item := range panel.Items {
			if _, duplicate := giftIDs[item.GiftID]; duplicate {
				return fmt.Errorf("gift target IDs must be unique within panel %q", panel.ID)
			}
			giftIDs[item.GiftID] = struct{}{}
		}
	}
	return nil
}

func joinGifts(definitions []GiftDefinition) []gameplay.GiftInfo {
	gifts := make([]gameplay.GiftInfo, len(definitions))
	for index, definition := range definitions {
		gifts[index] = gameplay.GiftInfo{ID: definition.ID, Name: definition.Name, Price: definition.Price, CoinType: definition.CoinType, BlindBoxParentID: definition.BlindBoxParentID, BlindBoxParentName: definition.BlindBoxParentName, BlindBoxParentPrice: definition.BlindBoxParentPrice}
	}
	return gifts
}

func joinAttributes(definitions []AttributeDefinition, values map[string]float64) ([]gameplay.Attribute, error) {
	if len(definitions) != len(values) {
		return nil, errors.New("runtime attribute values do not match definition")
	}
	attributes := make([]gameplay.Attribute, len(definitions))
	for index, definition := range definitions {
		value, exists := values[definition.ID]
		if !exists {
			return nil, fmt.Errorf("runtime value missing for attribute %q", definition.ID)
		}
		attributes[index] = gameplay.Attribute{ID: definition.ID, Name: definition.Name, Value: value, Unit: definition.Unit, Format: definition.Format, Decimals: definition.Decimals, Suffix: definition.Suffix, Color: definition.Color, BroadcastMessage: definition.BroadcastMessage, Display: definition.Display}
	}
	return attributes, nil
}

func joinGiftTargets(definitions []GiftTargetPanelDefinition, states []GiftTargetRuntimeState) ([]gameplay.GiftTargetPanel, error) {
	want := make(map[string]struct{})
	for _, panel := range definitions {
		for _, item := range panel.Items {
			want[giftTargetKey(panel.ID, item.GiftID)] = struct{}{}
		}
	}
	if len(want) != len(states) {
		return nil, errors.New("runtime gift targets do not match definition")
	}
	received := make(map[string]int, len(states))
	for _, state := range states {
		key := giftTargetKey(state.PanelID, state.GiftID)
		if _, exists := want[key]; !exists {
			return nil, fmt.Errorf("runtime gift target %q is not configured", key)
		}
		if _, duplicate := received[key]; duplicate {
			return nil, fmt.Errorf("runtime gift target %q is duplicated", key)
		}
		received[key] = state.Received
	}
	panels := make([]gameplay.GiftTargetPanel, len(definitions))
	for panelIndex, definition := range definitions {
		panel := gameplay.GiftTargetPanel{ID: definition.ID, Name: definition.Name, Layout: definition.Layout, Items: make([]gameplay.GiftTargetItem, len(definition.Items))}
		for itemIndex, item := range definition.Items {
			panel.Items[itemIndex] = gameplay.GiftTargetItem{GiftID: item.GiftID, Name: item.Name, Target: item.Target, Received: received[giftTargetKey(definition.ID, item.GiftID)], BarStyle: item.BarStyle}
		}
		panels[panelIndex] = panel
	}
	return panels, nil
}

func joinActivities(definitions []ActivityDefinition, states []ActivityRuntimeState) ([]gameplay.Activity, error) {
	if len(definitions) != len(states) {
		return nil, errors.New("runtime activities do not match definition")
	}
	byID := make(map[string]ActivityRuntimeState, len(states))
	for _, state := range states {
		if _, duplicate := byID[state.ID]; duplicate {
			return nil, fmt.Errorf("runtime activity %q is duplicated", state.ID)
		}
		byID[state.ID] = state
	}
	activities := make([]gameplay.Activity, len(definitions))
	for index, definition := range definitions {
		state, exists := byID[definition.ID]
		if !exists {
			return nil, fmt.Errorf("runtime activity %q is missing", definition.ID)
		}
		activity := gameplay.Activity{ID: definition.ID, Name: definition.Name, AttributeIDs: definition.AttributeIDs, SceneID: definition.SceneID, Status: state.Status, ResultMode: definition.ResultMode, GateRules: definition.GateRules, InitialValues: definition.InitialValues, Milestones: make([]gameplay.Milestone, len(definition.Milestones)), StartedAtMillis: state.StartedAtMillis, LockedAtMillis: state.LockedAtMillis, SettledAtMillis: state.SettledAtMillis, Result: state.Result}
		if definition.GiftTimeout == nil && state.GiftTimeout != nil || definition.GiftTimeout != nil && state.GiftTimeout == nil {
			return nil, fmt.Errorf("runtime gift timeout for activity %q does not match definition", definition.ID)
		}
		if definition.GiftTimeout != nil {
			activity.GiftTimeout = &gameplay.GiftTimeout{Seconds: definition.GiftTimeout.Seconds, Action: definition.GiftTimeout.Action, LastGiftAtMillis: state.GiftTimeout.LastGiftAtMillis, DeadlineAtMillis: state.GiftTimeout.DeadlineAtMillis}
		}
		if len(definition.Milestones) != len(state.Milestones) {
			return nil, fmt.Errorf("runtime milestones for activity %q do not match definition", definition.ID)
		}
		stateMilestones := make(map[string]MilestoneRuntimeState, len(state.Milestones))
		for _, milestone := range state.Milestones {
			if _, duplicate := stateMilestones[milestone.ID]; duplicate {
				return nil, fmt.Errorf("runtime milestone %q is duplicated", milestone.ID)
			}
			stateMilestones[milestone.ID] = milestone
		}
		for milestoneIndex, definitionMilestone := range definition.Milestones {
			stateMilestone, exists := stateMilestones[definitionMilestone.ID]
			if !exists {
				return nil, fmt.Errorf("runtime milestone %q is missing", definitionMilestone.ID)
			}
			activity.Milestones[milestoneIndex] = gameplay.Milestone{ID: definitionMilestone.ID, Name: definitionMilestone.Name, AttributeID: definitionMilestone.AttributeID, Comparison: definitionMilestone.Comparison, Threshold: definitionMilestone.Threshold, Action: definitionMilestone.Action, Message: definitionMilestone.Message, TriggeredAtMillis: stateMilestone.TriggeredAtMillis, TriggerValue: stateMilestone.TriggerValue}
		}
		activities[index] = activity
	}
	return activities, nil
}

func giftTargetKey(panelID string, giftID int) string {
	return fmt.Sprintf("%s:%d", panelID, giftID)
}
