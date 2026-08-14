package main

import (
	"fmt"
	"strings"
)

type attributeEditTarget struct {
	Kind        string `json:"kind"`
	AttributeID string `json:"attributeId,omitempty"`
	LeaseToken  string `json:"leaseToken,omitempty"`
}

type attributeEditCommand struct {
	Target             attributeEditTarget `json:"target"`
	Attribute          attributeState      `json:"attribute"`
	GiftRules          []giftRule          `json:"giftRules"`
	TimerRules         []timerRule         `json:"timerRules"`
	GiftCatalogUpserts []giftInfo          `json:"giftCatalogUpserts"`
}

type attributeEditResult struct {
	State       appState
	ID          string
	Name        string
	Created     bool
	Previous    appState
	PreviousErr error
}

type attributeEditInputError struct{ err error }

func (e *attributeEditInputError) Error() string { return e.err.Error() }
func (e *attributeEditInputError) Unwrap() error { return e.err }

type attributeEditConflictError struct{ err error }

func (e *attributeEditConflictError) Error() string { return e.err.Error() }
func (e *attributeEditConflictError) Unwrap() error { return e.err }

type attributeEditNotFoundError struct{ err error }

func (e *attributeEditNotFoundError) Error() string { return e.err.Error() }
func (e *attributeEditNotFoundError) Unwrap() error { return e.err }

func (s *configStore) applyAttributeEdit(command attributeEditCommand, newID func() (string, error)) (attributeEditResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureMutationsAllowedLocked(); err != nil {
		return attributeEditResult{}, err
	}
	previous, err := s.readStateLocked()
	if err != nil {
		return attributeEditResult{}, err
	}
	if err := s.ensureMutationsAllowedLocked(); err != nil {
		return attributeEditResult{}, err
	}
	if err := validateUniqueAttributeIDs(previous.Attributes); err != nil {
		return attributeEditResult{}, err
	}
	state, err := cloneAppState(previous)
	if err != nil {
		return attributeEditResult{}, err
	}

	command.Attribute.Name = strings.TrimSpace(command.Attribute.Name)
	if command.Attribute.Name == "" {
		return attributeEditResult{}, attributeEditInput(fmt.Errorf("属性名不能为空"))
	}

	var (
		index   int
		oldName string
		id      string
		created bool
	)
	switch strings.TrimSpace(command.Target.Kind) {
	case "existing":
		id = strings.TrimSpace(command.Target.AttributeID)
		if id == "" {
			return attributeEditResult{}, attributeEditInput(fmt.Errorf("属性 ID 不能为空"))
		}
		index = -1
		for candidateIndex := range state.Attributes {
			if state.Attributes[candidateIndex].ID == id {
				index = candidateIndex
				break
			}
		}
		if index < 0 {
			return attributeEditResult{}, attributeEditNotFound(fmt.Errorf("属性不存在：%s", id))
		}
		oldName = state.Attributes[index].Name
		attribute := command.Attribute
		attribute.ID = state.Attributes[index].ID
		attribute.Color = state.Attributes[index].Color
		attribute.CreatedFromTemplateID = state.Attributes[index].CreatedFromTemplateID
		attribute.CreatedFromTemplateVersion = state.Attributes[index].CreatedFromTemplateVersion
		state.Attributes[index] = attribute
	case "new":
		if strings.TrimSpace(command.Target.AttributeID) != "" {
			return attributeEditResult{}, attributeEditInput(fmt.Errorf("新属性不能指定属性 ID"))
		}
		if newID == nil {
			return attributeEditResult{}, fmt.Errorf("生成属性 ID 的函数不能为空")
		}
		id, err = newID()
		if err != nil {
			return attributeEditResult{}, err
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return attributeEditResult{}, fmt.Errorf("生成的属性 ID 不能为空")
		}
		for _, attribute := range state.Attributes {
			if attribute.ID == id {
				return attributeEditResult{}, attributeEditConflict(fmt.Errorf("属性 ID 不能重复：%s", id))
			}
		}
		attribute := command.Attribute
		attribute.ID = id
		state.Attributes = append(state.Attributes, attribute)
		index = len(state.Attributes) - 1
		created = true
	default:
		return attributeEditResult{}, attributeEditInput(fmt.Errorf("属性编辑目标无效"))
	}

	if attributeNameConflict(state.Attributes, index, command.Attribute.Name) {
		return attributeEditResult{}, attributeEditConflict(fmt.Errorf("属性名不能重复：%s", command.Attribute.Name))
	}
	if err := validateUniqueAttributeIDs(state.Attributes); err != nil {
		return attributeEditResult{}, err
	}

	if err := validateUniqueGiftRuleIDs(previous.Rules); err != nil {
		return attributeEditResult{}, err
	}
	if err := validateUniqueTimerRuleIDs(previous.TimerRules); err != nil {
		return attributeEditResult{}, err
	}
	ownedGiftRules := ownedGiftRuleIDs(previous.Rules, oldName)
	ownedTimerRules := ownedTimerRuleIDs(previous.TimerRules, oldName)
	peerGiftRuleIDs := nonTargetGiftRuleIDs(previous.Rules, oldName)
	peerTimerRuleIDs := nonTargetTimerRuleIDs(previous.TimerRules, oldName)
	if oldName != "" && oldName != command.Attribute.Name {
		if err := rewriteAttributeReferences(&state, oldName, command.Attribute.Name); err != nil {
			return attributeEditResult{}, attributeEditInput(err)
		}
	}
	if err := validateSubmittedGiftRules(command.GiftRules, command.Attribute.Name, peerGiftRuleIDs); err != nil {
		return attributeEditResult{}, err
	}
	if err := validateSubmittedTimerRules(command.TimerRules, command.Attribute.Name, peerTimerRuleIDs); err != nil {
		return attributeEditResult{}, err
	}
	state.Rules = mergeGiftRuleGroup(state.Rules, ownedGiftRules, command.GiftRules)
	state.TimerRules = mergeTimerRuleGroup(state.TimerRules, ownedTimerRules, command.TimerRules)
	state.GiftCatalog = mergeGiftCatalog(state.GiftCatalog, command.GiftCatalogUpserts)

	normalizeAppState(&state)
	if err := validateAppState(state); err != nil {
		return attributeEditResult{}, attributeEditInput(err)
	}
	if err := s.persistStateLocked(previous, state, false); err != nil {
		return attributeEditResult{}, err
	}
	return attributeEditResult{State: state, ID: id, Name: command.Attribute.Name, Created: created, Previous: previous}, nil
}

func rewriteAttributeReferences(state *appState, oldName, newName string) error {
	for index := range state.Rules {
		rule := &state.Rules[index]
		if rule.AttributeName == oldName {
			rule.AttributeName = newName
		}
		formula, err := rewriteFormulaIdentifier(rule.Formula, oldName, newName)
		if err != nil {
			return err
		}
		rule.Formula = formula
		condition, err := rewriteFormulaIdentifier(rule.Condition, oldName, newName)
		if err != nil {
			return err
		}
		rule.Condition = condition
	}
	for index := range state.TimerRules {
		rule := &state.TimerRules[index]
		if rule.AttributeName == oldName {
			rule.AttributeName = newName
		}
		formula, err := rewriteFormulaIdentifier(rule.Formula, oldName, newName)
		if err != nil {
			return err
		}
		rule.Formula = formula
		condition, err := rewriteFormulaIdentifier(rule.Condition, oldName, newName)
		if err != nil {
			return err
		}
		rule.Condition = condition
	}
	for index := range state.DisplayScenes {
		for nameIndex := range state.DisplayScenes[index].AttributeNames {
			if state.DisplayScenes[index].AttributeNames[nameIndex] == oldName {
				state.DisplayScenes[index].AttributeNames[nameIndex] = newName
			}
		}
	}
	for index := range state.Activities {
		activity := &state.Activities[index]
		for nameIndex := range activity.AttributeNames {
			if activity.AttributeNames[nameIndex] == oldName {
				activity.AttributeNames[nameIndex] = newName
			}
		}
		renameFloatMapKey(activity.InitialValues, oldName, newName)
		for milestoneIndex := range activity.Milestones {
			if activity.Milestones[milestoneIndex].AttributeName == oldName {
				activity.Milestones[milestoneIndex].AttributeName = newName
			}
		}
		if activity.Result != nil {
			if activity.Result.WinnerAttributeName == oldName {
				activity.Result.WinnerAttributeName = newName
			}
			renameFloatMapKey(activity.Result.Values, oldName, newName)
		}
	}
	for index := range state.FormulaPresets {
		preset := &state.FormulaPresets[index]
		if preset.SourceAttributeName == oldName {
			preset.SourceAttributeName = newName
		}
		formula, err := rewriteFormulaIdentifier(preset.Formula, oldName, newName)
		if err != nil {
			return err
		}
		preset.Formula = formula
	}
	return nil
}

func attributeEditInput(err error) error    { return &attributeEditInputError{err: err} }
func attributeEditConflict(err error) error { return &attributeEditConflictError{err: err} }
func attributeEditNotFound(err error) error { return &attributeEditNotFoundError{err: err} }

func validateUniqueAttributeIDs(attributes []attributeState) error {
	seen := make(map[string]struct{}, len(attributes))
	for _, attribute := range attributes {
		id := strings.TrimSpace(attribute.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			return attributeEditConflict(fmt.Errorf("属性 ID 不能重复：%s", id))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func attributeNameConflict(attributes []attributeState, targetIndex int, name string) bool {
	for index, attribute := range attributes {
		if index != targetIndex && strings.TrimSpace(attribute.Name) == name {
			return true
		}
	}
	return false
}

func ownedGiftRuleIDs(rules []giftRule, name string) map[string]struct{} {
	owned := make(map[string]struct{})
	if name == "" {
		return owned
	}
	for _, rule := range rules {
		if rule.AttributeName == name {
			owned[rule.ID] = struct{}{}
		}
	}
	return owned
}

func ownedTimerRuleIDs(rules []timerRule, name string) map[string]struct{} {
	owned := make(map[string]struct{})
	if name == "" {
		return owned
	}
	for _, rule := range rules {
		if rule.AttributeName == name {
			owned[rule.ID] = struct{}{}
		}
	}
	return owned
}

func nonTargetGiftRuleIDs(rules []giftRule, targetName string) map[string]struct{} {
	ids := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if rule.AttributeName != targetName {
			ids[rule.ID] = struct{}{}
		}
	}
	return ids
}

func nonTargetTimerRuleIDs(rules []timerRule, targetName string) map[string]struct{} {
	ids := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if rule.AttributeName != targetName {
			ids[rule.ID] = struct{}{}
		}
	}
	return ids
}

func validateUniqueGiftRuleIDs(rules []giftRule) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			return attributeEditConflict(fmt.Errorf("现有礼物规则 ID 不能为空"))
		}
		if _, exists := seen[id]; exists {
			return attributeEditConflict(fmt.Errorf("现有礼物规则 ID 不能重复：%s", id))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateUniqueTimerRuleIDs(rules []timerRule) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			return attributeEditConflict(fmt.Errorf("现有定时器规则 ID 不能为空"))
		}
		if _, exists := seen[id]; exists {
			return attributeEditConflict(fmt.Errorf("现有定时器规则 ID 不能重复：%s", id))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateSubmittedGiftRules(rules []giftRule, name string, peerIDs map[string]struct{}) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if rule.AttributeName != name {
			return attributeEditInput(fmt.Errorf("提交的礼物规则必须引用目标属性 %q", name))
		}
		if strings.TrimSpace(rule.ID) == "" {
			return attributeEditInput(fmt.Errorf("提交的礼物规则 ID 不能为空"))
		}
		if _, exists := seen[rule.ID]; exists {
			return attributeEditInput(fmt.Errorf("提交的礼物规则 ID 不能重复：%s", rule.ID))
		}
		if _, exists := peerIDs[rule.ID]; exists {
			return attributeEditConflict(fmt.Errorf("提交的礼物规则 ID 与同伴规则冲突：%s", rule.ID))
		}
		seen[rule.ID] = struct{}{}
	}
	return nil
}

func validateSubmittedTimerRules(rules []timerRule, name string, peerIDs map[string]struct{}) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if rule.AttributeName != name {
			return attributeEditInput(fmt.Errorf("提交的定时器规则必须引用目标属性 %q", name))
		}
		if strings.TrimSpace(rule.ID) == "" {
			return attributeEditInput(fmt.Errorf("提交的定时器规则 ID 不能为空"))
		}
		if _, exists := seen[rule.ID]; exists {
			return attributeEditInput(fmt.Errorf("提交的定时器规则 ID 不能重复：%s", rule.ID))
		}
		if _, exists := peerIDs[rule.ID]; exists {
			return attributeEditConflict(fmt.Errorf("提交的定时器规则 ID 与同伴规则冲突：%s", rule.ID))
		}
		seen[rule.ID] = struct{}{}
	}
	return nil
}

func mergeGiftRuleGroup(current []giftRule, owned map[string]struct{}, submitted []giftRule) []giftRule {
	result := make([]giftRule, 0, len(current)-len(owned)+len(submitted))
	inserted := false
	for _, rule := range current {
		if _, isOwned := owned[rule.ID]; isOwned {
			if !inserted {
				result = append(result, submitted...)
				inserted = true
			}
			continue
		}
		result = append(result, rule)
	}
	if !inserted {
		result = append(result, submitted...)
	}
	return result
}

func mergeTimerRuleGroup(current []timerRule, owned map[string]struct{}, submitted []timerRule) []timerRule {
	result := make([]timerRule, 0, len(current)-len(owned)+len(submitted))
	inserted := false
	for _, rule := range current {
		if _, isOwned := owned[rule.ID]; isOwned {
			if !inserted {
				result = append(result, submitted...)
				inserted = true
			}
			continue
		}
		result = append(result, rule)
	}
	if !inserted {
		result = append(result, submitted...)
	}
	return result
}

func mergeGiftCatalog(current []giftInfo, upserts []giftInfo) []giftInfo {
	byID := make(map[int]giftInfo, len(upserts))
	for _, gift := range upserts {
		byID[gift.ID] = gift
	}
	result := make([]giftInfo, 0, len(current)+len(upserts))
	used := make(map[int]struct{}, len(upserts))
	for _, gift := range current {
		if replacement, exists := byID[gift.ID]; exists {
			result = append(result, replacement)
			used[gift.ID] = struct{}{}
			continue
		}
		result = append(result, gift)
	}
	for _, gift := range upserts {
		if _, exists := used[gift.ID]; exists {
			continue
		}
		if _, exists := findGiftInfoByID(current, gift.ID); exists {
			continue
		}
		result = append(result, gift)
		used[gift.ID] = struct{}{}
	}
	return result
}

func findGiftInfoByID(gifts []giftInfo, id int) (giftInfo, bool) {
	for _, gift := range gifts {
		if gift.ID == id {
			return gift, true
		}
	}
	return giftInfo{}, false
}

func renameFloatMapKey(values map[string]float64, oldName, newName string) {
	if values == nil || oldName == newName {
		return
	}
	if value, exists := values[oldName]; exists {
		delete(values, oldName)
		values[newName] = value
	}
}
