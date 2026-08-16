package main

import (
	"sort"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
)

// snapshotFromAppState crosses the shared gameplay seam. Viewer history,
// receipts, logs, contribution data, and ingestion ledgers deliberately have
// no representation here.
func snapshotFromAppState(state appState) gameplay.Snapshot {
	return snapshotFromAppStateAt(state, time.Now())
}

func snapshotFromAppStateAt(state appState, processingNow time.Time) gameplay.Snapshot {
	attributeIDs := make(map[string]string, len(state.Attributes))
	attributes := make([]gameplay.Attribute, len(state.Attributes))
	for index, attribute := range state.Attributes {
		id := strings.TrimSpace(attribute.ID)
		if id == "" {
			id = attribute.Name
		}
		attributeIDs[attribute.Name] = id
		attributes[index] = gameplay.Attribute{
			ID: id, Name: attribute.Name, Value: attribute.Value, Unit: attribute.Unit,
			Format: attribute.Format, Decimals: attribute.Decimals, Suffix: attribute.Suffix,
			Color: attribute.Color, BroadcastMessage: attribute.BroadcastMessage,
		}
		if attribute.Display != nil {
			display := attribute.Display
			attributes[index].Display = &gameplay.Display{
				Variant: display.Variant, ThemeID: display.ThemeID, Title: display.Title,
				Min: cloneFloat64Pointer(display.Min), Max: cloneFloat64Pointer(display.Max),
				LowThreshold: cloneFloat64Pointer(display.LowThreshold), LeftLabel: display.LeftLabel,
				RightLabel: display.RightLabel, ValueMappings: make([]gameplay.ValueMapping, len(display.ValueMappings)),
			}
			for mappingIndex, mapping := range display.ValueMappings {
				attributes[index].Display.ValueMappings[mappingIndex] = gameplay.ValueMapping{
					Value: mapping.Value, Label: mapping.Label, Color: mapping.Color,
				}
			}
		}
	}

	snapshot := gameplay.Snapshot{
		RoomID:           state.RoomID,
		Attributes:       attributes,
		DisplayScenes:    make([]gameplay.DisplayScene, len(state.DisplayScenes)),
		GiftTargetPanels: make([]gameplay.GiftTargetPanel, len(state.GiftKPIPanels)),
		Activities:       make([]gameplay.Activity, len(state.Activities)),
		Rules:            make([]gameplay.Rule, len(state.Rules)),
		TimerRules:       make([]gameplay.TimerRule, len(state.TimerRules)),
		FormulaPresets:   make([]gameplay.FormulaPreset, len(state.FormulaPresets)),
		Gifts:            make([]gameplay.GiftInfo, len(state.GiftCatalog)),
		RuleLimits:       gameplayRuleLimitState(state, processingNow),
	}
	for index, scene := range state.DisplayScenes {
		snapshot.DisplayScenes[index] = gameplay.DisplayScene{
			ID: scene.ID, Name: scene.Name, AttributeIDs: gameplayAttributeIDs(scene.AttributeNames, attributeIDs),
			Layout: scene.Layout, ThemeID: scene.ThemeID,
		}
	}
	for panelIndex, panel := range state.GiftKPIPanels {
		snapshot.GiftTargetPanels[panelIndex] = gameplay.GiftTargetPanel{
			ID: panel.ID, Name: panel.Name, Layout: panel.Layout, Items: make([]gameplay.GiftTargetItem, len(panel.Items)),
		}
		for itemIndex, item := range panel.Items {
			snapshot.GiftTargetPanels[panelIndex].Items[itemIndex] = gameplay.GiftTargetItem{
				GiftID: item.GiftID, Name: item.GiftName, ImageURL: item.ImageURL,
				Target: item.Target, Received: item.Received, BarStyle: item.BarStyle,
			}
		}
	}
	for index, activity := range state.Activities {
		snapshot.Activities[index] = gameplayActivity(activity, attributeIDs)
	}
	for index, rule := range state.Rules {
		snapshot.Rules[index] = gameplay.Rule{
			ID: rule.ID, GiftID: rule.GiftID, AttributeID: attributeIDs[rule.AttributeName],
			FormulaName: rule.FormulaName, Condition: rule.Condition, Formula: rule.Formula,
			Enabled: cloneBoolPointer(rule.Enabled), MatchGiftIDs: append([]int(nil), rule.MatchGiftIDs...),
			MinPrice: cloneFloat64Pointer(rule.MinPrice), Cap: cloneFloat64Pointer(rule.Cap), DailyLimit: cloneIntPointer(rule.DailyLimit),
		}
	}
	for index, rule := range state.TimerRules {
		snapshot.TimerRules[index] = gameplay.TimerRule{
			ID: rule.ID, AttributeID: attributeIDs[rule.AttributeName], FormulaName: rule.FormulaName,
			IntervalSeconds: rule.IntervalSeconds, Condition: rule.Condition, Formula: rule.Formula, Enabled: rule.Enabled,
		}
	}
	for index, preset := range state.FormulaPresets {
		snapshot.FormulaPresets[index] = gameplay.FormulaPreset{
			ID: preset.ID, Name: preset.Name, Context: preset.Context, Formula: preset.Formula,
			AttributeID: attributeIDs[preset.SourceAttributeName],
		}
	}
	for index, gift := range state.GiftCatalog {
		snapshot.Gifts[index] = gameplay.GiftInfo{
			ID: gift.ID, Name: gift.Name, Price: gift.Price, CoinType: gift.CoinType, ImageURL: gift.ImgBasic,
			BlindBoxParentID: gift.BlindBoxParentID, BlindBoxParentName: gift.BlindBoxParentName,
			BlindBoxParentPrice: gift.BlindBoxParentPrice,
		}
	}
	if state.SimplePlay != nil {
		snapshot.SimplePlay = &gameplay.SimplePlay{
			Version: state.SimplePlay.Version, TemplateID: state.SimplePlay.TemplateID,
			TemplateVersion: state.SimplePlay.TemplateVersion, AttributeID: state.SimplePlay.AttributeID,
			Parameters: state.SimplePlay.Parameters, Gifts: state.SimplePlay.Gifts,
			ManagedFingerprint:  state.SimplePlay.ManagedFingerprint,
			OvertimeGiftActions: make([]gameplay.OvertimeGiftAction, len(state.SimplePlay.OvertimeGiftActions)),
		}
		for index, action := range state.SimplePlay.OvertimeGiftActions {
			snapshot.SimplePlay.OvertimeGiftActions[index] = gameplay.OvertimeGiftAction{
				GiftID: action.GiftID, Operation: action.Operation, Seconds: cloneIntPointer(action.Seconds),
			}
		}
	}
	return snapshot
}

func gameplayActivity(activity activitySessionState, attributeIDs map[string]string) gameplay.Activity {
	converted := gameplay.Activity{
		ID: activity.ID, Name: activity.Name, AttributeIDs: gameplayAttributeIDs(activity.AttributeNames, attributeIDs),
		SceneID: activity.SceneID, Status: activity.Status, ResultMode: activity.ResultMode, GateRules: activity.GateRules,
		InitialValues:   make(map[string]float64, len(activity.InitialValues)),
		Milestones:      make([]gameplay.Milestone, len(activity.Milestones)),
		StartedAtMillis: activity.StartedAt, LockedAtMillis: activity.LockedAt, SettledAtMillis: activity.SettledAt,
	}
	for name, value := range activity.InitialValues {
		converted.InitialValues[attributeIDs[name]] = value
	}
	for index, milestone := range activity.Milestones {
		converted.Milestones[index] = gameplay.Milestone{
			ID: milestone.ID, Name: milestone.Name, AttributeID: attributeIDs[milestone.AttributeName],
			Comparison: milestone.Comparison, Threshold: milestone.Threshold, Action: milestone.Action,
			Message: milestone.Message, TriggeredAtMillis: milestone.TriggeredAt,
			TriggerValue: cloneFloat64Pointer(milestone.TriggerValue),
		}
	}
	if activity.GiftTimeout != nil {
		converted.GiftTimeout = &gameplay.GiftTimeout{
			Seconds: activity.GiftTimeout.Seconds, Action: activity.GiftTimeout.Action,
			LastGiftAtMillis: activity.GiftTimeout.LastGiftAt, DeadlineAtMillis: activity.GiftTimeout.DeadlineAt,
		}
	}
	if activity.Result != nil {
		converted.Result = &gameplay.ActivityResult{
			WinnerAttributeID: attributeIDs[activity.Result.WinnerAttributeName],
			Values:            make(map[string]float64, len(activity.Result.Values)),
		}
		for name, value := range activity.Result.Values {
			converted.Result.Values[attributeIDs[name]] = value
		}
	}
	return converted
}

func gameplayAttributeIDs(names []string, ids map[string]string) []string {
	result := make([]string, len(names))
	for index, name := range names {
		result[index] = ids[name]
	}
	return result
}

func gameplayGift(gift giftEvent) gameplay.Gift {
	occurredAtMillis := int64(0)
	if gift.Timestamp > 0 {
		occurredAtMillis = gift.Timestamp * 1000
	}
	return gameplay.Gift{
		GiftID: gift.GiftID, BlindGiftID: gift.BlindGiftID, Count: gift.Num, Price: gift.Price,
		IdentityRank: int(giftIdentityLevel(gift.Membership)), EventID: gift.Rnd, OccurredAtMillis: occurredAtMillis,
	}
}

func gameplaySnapshotForGift(state appState, gift giftEvent, freezes attributeFreezeChecker) gameplay.Snapshot {
	return gameplaySnapshotForGiftAt(state, gift, freezes, time.Now())
}

func gameplaySnapshotForGiftAt(state appState, gift giftEvent, freezes attributeFreezeChecker, processingNow time.Time) gameplay.Snapshot {
	snapshot := snapshotFromAppStateAt(state, processingNow)
	snapshot.DisplayScenes = nil
	snapshot.TimerRules = nil
	snapshot.FormulaPresets = nil
	snapshot.SimplePlay = nil
	snapshot.Gifts = nil
	for index, rule := range state.Rules {
		if attribute := state.findAttribute(rule.AttributeName); attribute != nil && freezes != nil && attribute.ID != "" && freezes.IsFrozen(attribute.ID) {
			disabled := false
			snapshot.Rules[index].Enabled = &disabled
		}
		configuredGift := state.findGift(rule.GiftID)
		if configuredGift != nil && sameGiftIdentity(*configuredGift, gift) {
			snapshot.Rules[index].MatchGiftIDs = append(snapshot.Rules[index].MatchGiftIDs, gift.GiftID)
		}
	}
	return snapshot
}

func gameplaySnapshotForTimers(state appState, dueRuleIDs []string, freezes attributeFreezeChecker) gameplay.Snapshot {
	snapshot := snapshotFromAppState(state)
	snapshot.DisplayScenes = nil
	snapshot.GiftTargetPanels = nil
	snapshot.Rules = nil
	snapshot.FormulaPresets = nil
	snapshot.SimplePlay = nil
	snapshot.Gifts = nil
	due := make(map[string]struct{}, len(dueRuleIDs))
	for _, ruleID := range dueRuleIDs {
		due[ruleID] = struct{}{}
	}
	timerRules := make([]gameplay.TimerRule, 0, len(due))
	for index, rule := range state.TimerRules {
		if _, exists := due[rule.ID]; !exists {
			continue
		}
		attribute := state.findAttribute(rule.AttributeName)
		if attribute != nil && freezes != nil && attribute.ID != "" && freezes.IsFrozen(attribute.ID) {
			snapshot.TimerRules[index].Enabled = false
		}
		timerRules = append(timerRules, snapshot.TimerRules[index])
	}
	snapshot.TimerRules = timerRules
	return snapshot
}

func gameplaySnapshotForActivities(state appState) gameplay.Snapshot {
	snapshot := snapshotFromAppState(state)
	snapshot.DisplayScenes = nil
	snapshot.GiftTargetPanels = nil
	snapshot.Rules = nil
	snapshot.TimerRules = nil
	snapshot.FormulaPresets = nil
	snapshot.SimplePlay = nil
	snapshot.Gifts = nil
	snapshot.RuleLimits = gameplay.RuleLimitState{}
	return snapshot
}

func gameplaySnapshotForTargets(state appState) gameplay.Snapshot {
	snapshot := snapshotFromAppState(state)
	return gameplay.Snapshot{GiftTargetPanels: snapshot.GiftTargetPanels}
}

// applyGameplayTransition copies only gameplay-owned runtime fields back. It
// cannot overwrite desktop-only history or viewer state because those fields
// never cross the seam.
func applyGameplayTransition(state *appState, transition gameplay.Transition) {
	attributeNames := make(map[string]string, len(state.Attributes))
	for index := range state.Attributes {
		id := strings.TrimSpace(state.Attributes[index].ID)
		if id == "" {
			id = state.Attributes[index].Name
		}
		attributeNames[id] = state.Attributes[index].Name
		if next := gameplayAttributeByID(transition.Next, id); next != nil {
			state.Attributes[index].Value = next.Value
		}
	}
	for panelIndex := range state.GiftKPIPanels {
		next := gameplayTargetPanelByID(transition.Next, state.GiftKPIPanels[panelIndex].ID)
		if next == nil {
			continue
		}
		for itemIndex := range state.GiftKPIPanels[panelIndex].Items {
			giftID := state.GiftKPIPanels[panelIndex].Items[itemIndex].GiftID
			for _, item := range next.Items {
				if item.GiftID == giftID {
					state.GiftKPIPanels[panelIndex].Items[itemIndex].Received = item.Received
					break
				}
			}
		}
	}
	for activityIndex := range state.Activities {
		next := gameplayActivityByID(transition.Next, state.Activities[activityIndex].ID)
		if next == nil {
			continue
		}
		activity := &state.Activities[activityIndex]
		activity.Status = next.Status
		activity.StartedAt = next.StartedAtMillis
		activity.LockedAt = next.LockedAtMillis
		activity.SettledAt = next.SettledAtMillis
		for milestoneIndex := range activity.Milestones {
			for _, nextMilestone := range next.Milestones {
				if nextMilestone.ID == activity.Milestones[milestoneIndex].ID {
					activity.Milestones[milestoneIndex].TriggeredAt = nextMilestone.TriggeredAtMillis
					activity.Milestones[milestoneIndex].TriggerValue = cloneFloat64Pointer(nextMilestone.TriggerValue)
					break
				}
			}
		}
		if activity.GiftTimeout != nil && next.GiftTimeout != nil {
			activity.GiftTimeout.LastGiftAt = next.GiftTimeout.LastGiftAtMillis
			activity.GiftTimeout.DeadlineAt = next.GiftTimeout.DeadlineAtMillis
		}
		if next.Result == nil {
			activity.Result = nil
		} else {
			activity.Result = &activityResultState{
				WinnerAttributeName: attributeNames[next.Result.WinnerAttributeID],
				Values:              make(map[string]float64, len(next.Result.Values)),
			}
			for id, value := range next.Result.Values {
				activity.Result.Values[attributeNames[id]] = value
			}
		}
	}
	applyGameplayRuleLimits(state, transition.Next.RuleLimits)
}

func gameplayRuleLimitState(state appState, processingNow time.Time) gameplay.RuleLimitState {
	if len(state.Stats) == 0 {
		return gameplay.RuleLimitState{AppliedCounts: map[string]int{}}
	}
	dates := make([]string, 0, len(state.Stats))
	for date := range state.Stats {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	date := dates[len(dates)-1]
	currentDate := processingNow.In(time.Local).Format("2006-01-02")
	if _, exists := state.Stats[currentDate]; exists {
		date = currentDate
	}
	stats := state.Stats[date]
	counts := make(map[string]int, len(stats.RuleTriggers))
	for ruleID, count := range stats.RuleTriggers {
		counts[ruleID] = count
	}
	return gameplay.RuleLimitState{LocalDate: date, AppliedCounts: counts}
}

func applyGameplayRuleLimits(state *appState, limits gameplay.RuleLimitState) {
	if strings.TrimSpace(limits.LocalDate) == "" {
		return
	}
	if state.Stats == nil {
		state.Stats = map[string]dayStats{}
	}
	stats, exists := state.Stats[limits.LocalDate]
	if !exists {
		stats = dayStats{Date: limits.LocalDate, GiftTotals: map[string]int{}}
	}
	if stats.GiftTotals == nil {
		stats.GiftTotals = map[string]int{}
	}
	stats.Date = limits.LocalDate
	stats.RuleTriggers = make(map[string]int, len(limits.AppliedCounts))
	for ruleID, count := range limits.AppliedCounts {
		stats.RuleTriggers[ruleID] = count
	}
	state.Stats[limits.LocalDate] = stats
}

func gameplayAttributeByID(snapshot gameplay.Snapshot, id string) *gameplay.Attribute {
	for index := range snapshot.Attributes {
		if snapshot.Attributes[index].ID == id {
			return &snapshot.Attributes[index]
		}
	}
	return nil
}

func gameplayTargetPanelByID(snapshot gameplay.Snapshot, id string) *gameplay.GiftTargetPanel {
	for index := range snapshot.GiftTargetPanels {
		if snapshot.GiftTargetPanels[index].ID == id {
			return &snapshot.GiftTargetPanels[index]
		}
	}
	return nil
}

func gameplayActivityByID(snapshot gameplay.Snapshot, id string) *gameplay.Activity {
	for index := range snapshot.Activities {
		if snapshot.Activities[index].ID == id {
			return &snapshot.Activities[index]
		}
	}
	return nil
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
