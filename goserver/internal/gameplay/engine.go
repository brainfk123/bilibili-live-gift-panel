package gameplay

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const localDateLayout = "2006-01-02"

// ApplyGift applies one identity-free gift to a detached snapshot.
func (Engine) ApplyGift(current Snapshot, gift Gift, now time.Time) (Transition, error) {
	return applyGift(current, gift, now, rand.Intn)
}

// ApplyGiftWithRandom is the desktop compatibility adapter for its existing
// deterministic test hook. Hosted callers use ApplyGift.
func (Engine) ApplyGiftWithRandom(current Snapshot, gift Gift, now time.Time, randomIntn func(int) int) (Transition, error) {
	if randomIntn == nil {
		randomIntn = rand.Intn
	}
	return applyGift(current, gift, now, randomIntn)
}

func applyGift(current Snapshot, gift Gift, now time.Time, randomIntn func(int) int) (Transition, error) {
	next, err := Normalize(current)
	if err != nil {
		return Transition{}, err
	}
	changed := rollRuleLimits(&next, now)
	effects := make([]Effect, 0)

	count := gift.Count
	if count < 1 {
		count = 1
	}
	if applyGiftTargets(&next, gift, count, &effects) {
		changed = true
	}

	attributeEffects := make([]Effect, 0)
	effectIndexes := make(map[string]int)
	changedAttributeIDs := make(map[string]struct{})
	for occurrence := 0; occurrence < count; occurrence++ {
		for _, rule := range next.Rules {
			if !ruleEnabled(rule) || !activityAllowsRules(next, rule.AttributeID) || !ruleMatchesGift(rule, gift) {
				continue
			}
			attribute := findAttribute(&next, rule.AttributeID)
			if attribute == nil || rule.MinPrice != nil && gift.Price < *rule.MinPrice {
				continue
			}
			triggerCount := next.RuleLimits.AppliedCounts[rule.ID]
			if rule.DailyLimit != nil && triggerCount >= *rule.DailyLimit {
				continue
			}
			environment := giftFormulaEnvironment(next, attribute.Name, gift.Price, gift.IdentityRank)
			if strings.TrimSpace(rule.Condition) != "" {
				condition, evaluateErr := evaluateFormula(rule.Condition, environment, randomIntn)
				if evaluateErr != nil || condition == 0 || !finite(condition) {
					continue
				}
			}
			nextValue, evaluateErr := evaluateFormula(rule.Formula, environment, randomIntn)
			if evaluateErr != nil || !finite(nextValue) {
				continue
			}
			if rule.Cap != nil {
				nextValue = math.Min(nextValue, *rule.Cap)
			}
			before := attribute.Value
			attribute.Value = nextValue
			next.RuleLimits.AppliedCounts[rule.ID] = triggerCount + 1
			changed = true
			changedAttributeIDs[attribute.ID] = struct{}{}
			delta := nextValue - before
			if index, exists := effectIndexes[attribute.Name]; exists {
				attributeEffects[index].Delta += delta
				attributeEffects[index].ValueAfter = nextValue
				continue
			}
			effectIndexes[attribute.Name] = len(attributeEffects)
			attributeEffects = append(attributeEffects, Effect{
				RuleID: rule.ID, AttributeName: attribute.Name, Delta: delta,
				ValueAfter: nextValue, TriggerName: rule.FormulaName,
			})
		}
	}
	effects = append(effects, attributeEffects...)
	if len(attributeEffects) > 0 {
		activityNow := now
		if gift.OccurredAtMillis > 0 {
			activityNow = time.UnixMilli(gift.OccurredAtMillis)
		}
		if resetGiftTimeouts(&next, changedAttributeIDs, activityNow, &effects) > 0 {
			changed = true
		}
		if evaluateMilestones(&next, activityNow, &effects) > 0 {
			changed = true
		}
	}
	return Transition{Next: next, Effects: effects, Changed: changed}, nil
}

// ApplyGiftTargets applies only target-counter progress. It exists for the
// desktop target-progress write adapter; normal gift processing uses
// Engine.ApplyGift so target and rule changes remain atomic.
func ApplyGiftTargets(current Snapshot, gift Gift) (Transition, error) {
	next, err := Normalize(current)
	if err != nil {
		return Transition{}, err
	}
	count := gift.Count
	if count < 1 {
		count = 1
	}
	effects := make([]Effect, 0)
	changed := applyGiftTargets(&next, gift, count, &effects)
	return Transition{Next: next, Effects: effects, Changed: changed}, nil
}

func applyGiftTargets(snapshot *Snapshot, gift Gift, count int, effects *[]Effect) bool {
	changed := false
	for panelIndex := range snapshot.GiftTargetPanels {
		panel := &snapshot.GiftTargetPanels[panelIndex]
		for itemIndex := range panel.Items {
			item := &panel.Items[itemIndex]
			if item.GiftID != gift.GiftID && (gift.BlindGiftID <= 0 || item.GiftID != gift.BlindGiftID) {
				continue
			}
			item.Received += count
			changed = true
			*effects = append(*effects, Effect{Target: &TargetNotice{
				PanelID: panel.ID, GiftID: item.GiftID, Received: item.Received, Target: item.Target,
			}})
		}
	}
	return changed
}

// ApplyTimers applies the selected timer rules to a detached snapshot.
func (Engine) ApplyTimers(current Snapshot, dueRuleIDs []string, now time.Time) (Transition, error) {
	return applyTimers(current, dueRuleIDs, now, rand.Intn)
}

// ApplyTimersWithRandom is the desktop compatibility adapter for its existing
// deterministic test hook. Hosted callers use ApplyTimers.
func (Engine) ApplyTimersWithRandom(current Snapshot, dueRuleIDs []string, now time.Time, randomIntn func(int) int) (Transition, error) {
	if randomIntn == nil {
		randomIntn = rand.Intn
	}
	return applyTimers(current, dueRuleIDs, now, randomIntn)
}

func applyTimers(current Snapshot, dueRuleIDs []string, now time.Time, randomIntn func(int) int) (Transition, error) {
	next, err := Normalize(current)
	if err != nil {
		return Transition{}, err
	}
	changed := rollRuleLimits(&next, now)
	due := make(map[string]struct{}, len(dueRuleIDs))
	for _, ruleID := range dueRuleIDs {
		due[ruleID] = struct{}{}
	}
	effects := make([]Effect, 0)
	applied := 0
	for _, rule := range next.TimerRules {
		if _, exists := due[rule.ID]; !exists || !rule.Enabled || !activityAllowsRules(next, rule.AttributeID) {
			continue
		}
		attribute := findAttribute(&next, rule.AttributeID)
		if attribute == nil {
			continue
		}
		environment := attributeEnvironment(next)
		if strings.TrimSpace(rule.Condition) != "" {
			condition, evaluateErr := evaluateFormula(rule.Condition, environment, randomIntn)
			if evaluateErr != nil || condition == 0 || !finite(condition) {
				continue
			}
		}
		nextValue, evaluateErr := evaluateFormula(rule.Formula, environment, randomIntn)
		if evaluateErr != nil || !finite(nextValue) {
			continue
		}
		before := attribute.Value
		attribute.Value = nextValue
		next.RuleLimits.AppliedCounts[rule.ID]++
		changed = true
		applied++
		effects = append(effects, Effect{
			RuleID: rule.ID, AttributeName: attribute.Name, Delta: nextValue - before,
			ValueAfter: nextValue, TriggerName: rule.FormulaName,
		})
	}
	if applied > 0 && evaluateMilestones(&next, now, &effects) > 0 {
		changed = true
	}
	return Transition{Next: next, Effects: effects, Changed: changed}, nil
}

// TransitionActivity applies one explicit activity lifecycle action.
func (Engine) TransitionActivity(current Snapshot, activityID, action string, now time.Time) (Transition, error) {
	next, err := Normalize(current)
	if err != nil {
		return Transition{}, err
	}
	activity := findActivity(&next, strings.TrimSpace(activityID))
	if activity == nil {
		return Transition{}, fmt.Errorf("找不到活动会话")
	}
	if err := transitionActivity(&next, activity, strings.TrimSpace(action), now); err != nil {
		return Transition{}, err
	}
	return Transition{
		Next: next, Changed: true,
		Effects: []Effect{{Activity: &ActivityNotice{ActivityID: activity.ID, Action: strings.TrimSpace(action), Status: activity.Status}}},
	}, nil
}

// ResetActivityGiftTimeouts resets active gift-timeout windows whose activity
// watches one of the changed attributes.
func ResetActivityGiftTimeouts(current Snapshot, changedAttributeIDs []string, now time.Time) (Transition, int, error) {
	next, err := Normalize(current)
	if err != nil {
		return Transition{}, 0, err
	}
	changed := make(map[string]struct{}, len(changedAttributeIDs))
	for _, attributeID := range changedAttributeIDs {
		changed[attributeID] = struct{}{}
	}
	effects := make([]Effect, 0)
	count := resetGiftTimeouts(&next, changed, now, &effects)
	return Transition{Next: next, Effects: effects, Changed: count > 0}, count, nil
}

// EvaluateActivityMilestones evaluates all currently active milestones once.
func EvaluateActivityMilestones(current Snapshot, now time.Time) (Transition, int, error) {
	next, err := Normalize(current)
	if err != nil {
		return Transition{}, 0, err
	}
	effects := make([]Effect, 0)
	count := evaluateMilestones(&next, now, &effects)
	return Transition{Next: next, Effects: effects, Changed: count > 0}, count, nil
}

// AllowsRulesForAttribute reports the activity-gate decision for one
// attribute without mutating the snapshot.
func AllowsRulesForAttribute(current Snapshot, attributeID string) bool {
	return activityAllowsRules(current, attributeID)
}

func rollRuleLimits(snapshot *Snapshot, now time.Time) bool {
	date := now.In(time.Local).Format(localDateLayout)
	if snapshot.RuleLimits.LocalDate != date {
		snapshot.RuleLimits = RuleLimitState{LocalDate: date, AppliedCounts: map[string]int{}}
		return true
	}
	if snapshot.RuleLimits.AppliedCounts == nil {
		snapshot.RuleLimits.AppliedCounts = map[string]int{}
	}
	return false
}

func ruleEnabled(rule Rule) bool { return rule.Enabled == nil || *rule.Enabled }

func ruleMatchesGift(rule Rule, gift Gift) bool {
	if rule.GiftID == gift.GiftID || gift.BlindGiftID > 0 && rule.GiftID == gift.BlindGiftID {
		return true
	}
	for _, giftID := range rule.MatchGiftIDs {
		if giftID == gift.GiftID {
			return true
		}
	}
	return false
}

func findAttribute(snapshot *Snapshot, id string) *Attribute {
	for index := range snapshot.Attributes {
		if snapshot.Attributes[index].ID == id {
			return &snapshot.Attributes[index]
		}
	}
	return nil
}

func findActivity(snapshot *Snapshot, id string) *Activity {
	for index := range snapshot.Activities {
		if snapshot.Activities[index].ID == id {
			return &snapshot.Activities[index]
		}
	}
	return nil
}

func attributeEnvironment(snapshot Snapshot) map[string]float64 {
	environment := make(map[string]float64, len(snapshot.Attributes))
	for _, attribute := range snapshot.Attributes {
		environment[attribute.Name] = attribute.Value
	}
	return environment
}

func giftFormulaEnvironment(snapshot Snapshot, attributeName string, giftPrice float64, identityRank int) map[string]float64 {
	environment := attributeEnvironment(snapshot)
	environment["price"] = giftPrice
	environment["用户身份"] = float64(identityRank)
	environment["普通用户"] = 0
	environment["粉丝团"] = 1
	environment["舰长"] = 2
	environment["提督"] = 3
	environment["总督"] = 4
	if attribute := attributeByName(snapshot, attributeName); attribute != nil {
		environment[attributeName] = attribute.Value
	}
	return environment
}

func attributeByName(snapshot Snapshot, name string) *Attribute {
	for index := range snapshot.Attributes {
		if snapshot.Attributes[index].Name == name {
			return &snapshot.Attributes[index]
		}
	}
	return nil
}

func activityAllowsRules(snapshot Snapshot, attributeID string) bool {
	for _, activity := range snapshot.Activities {
		if activity.GateRules && containsString(activity.AttributeIDs, attributeID) {
			return activity.Status == "active"
		}
	}
	return true
}

func transitionActivity(snapshot *Snapshot, activity *Activity, action string, now time.Time) error {
	nowMillis := now.UnixMilli()
	switch action {
	case "start":
		if activity.Status != "not_started" {
			return fmt.Errorf("只有未开始的活动才能开始")
		}
		restoreActivityInitialValues(snapshot, activity)
		activity.Status = "active"
		activity.StartedAtMillis = nowMillis
		activity.LockedAtMillis = 0
		activity.SettledAtMillis = 0
		activity.Result = nil
		clearActivityMilestones(activity)
		clearActivityGiftTimeout(activity)
	case "lock":
		if activity.Status != "active" {
			return fmt.Errorf("只有进行中的活动才能锁定")
		}
		activity.Status = "locked"
		activity.LockedAtMillis = nowMillis
		clearActivityGiftTimeout(activity)
	case "settle":
		if activity.Status != "active" && activity.Status != "locked" {
			return fmt.Errorf("只有进行中或已锁定的活动才能结算")
		}
		if activity.LockedAtMillis == 0 {
			activity.LockedAtMillis = nowMillis
		}
		activity.Status = "settled"
		activity.SettledAtMillis = nowMillis
		activity.Result = settleActivity(snapshot, activity)
		clearActivityGiftTimeout(activity)
	case "reset":
		restoreActivityInitialValues(snapshot, activity)
		activity.Status = "not_started"
		activity.StartedAtMillis = 0
		activity.LockedAtMillis = 0
		activity.SettledAtMillis = 0
		activity.Result = nil
		clearActivityMilestones(activity)
		clearActivityGiftTimeout(activity)
	default:
		return fmt.Errorf("不支持的活动操作")
	}
	return nil
}

func clearActivityMilestones(activity *Activity) {
	for index := range activity.Milestones {
		activity.Milestones[index].TriggeredAtMillis = 0
		activity.Milestones[index].TriggerValue = nil
	}
}

func clearActivityGiftTimeout(activity *Activity) {
	if activity.GiftTimeout == nil {
		return
	}
	activity.GiftTimeout.LastGiftAtMillis = 0
	activity.GiftTimeout.DeadlineAtMillis = 0
}

func restoreActivityInitialValues(snapshot *Snapshot, activity *Activity) {
	for _, attributeID := range activity.AttributeIDs {
		attribute := findAttribute(snapshot, attributeID)
		if attribute == nil {
			continue
		}
		if value, exists := activity.InitialValues[attributeID]; exists {
			attribute.Value = value
		}
	}
}

func settleActivity(snapshot *Snapshot, activity *Activity) *ActivityResult {
	result := &ActivityResult{Values: map[string]float64{}}
	for _, attributeID := range activity.AttributeIDs {
		if attribute := findAttribute(snapshot, attributeID); attribute != nil {
			result.Values[attributeID] = attribute.Value
		}
	}
	if activity.ResultMode == "none" || len(result.Values) == 0 {
		return result
	}
	var winner string
	var winnerValue float64
	tied := false
	for _, attributeID := range activity.AttributeIDs {
		value, exists := result.Values[attributeID]
		if !exists {
			continue
		}
		if winner == "" {
			winner, winnerValue, tied = attributeID, value, false
			continue
		}
		better := activity.ResultMode == "highest" && value > winnerValue || activity.ResultMode == "lowest" && value < winnerValue
		if better {
			winner, winnerValue, tied = attributeID, value, false
		} else if value == winnerValue {
			tied = true
		}
	}
	if !tied {
		result.WinnerAttributeID = winner
	}
	return result
}

func resetGiftTimeouts(snapshot *Snapshot, changedAttributeIDs map[string]struct{}, now time.Time, effects *[]Effect) int {
	reset := 0
	for index := range snapshot.Activities {
		activity := &snapshot.Activities[index]
		if activity.Status != "active" || activity.GiftTimeout == nil || activity.GiftTimeout.Seconds < 1 {
			continue
		}
		matched := false
		for _, attributeID := range activity.AttributeIDs {
			if _, exists := changedAttributeIDs[attributeID]; exists {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		activity.GiftTimeout.LastGiftAtMillis = now.UnixMilli()
		activity.GiftTimeout.DeadlineAtMillis = now.Add(time.Duration(activity.GiftTimeout.Seconds) * time.Second).UnixMilli()
		reset++
		*effects = append(*effects, Effect{Activity: &ActivityNotice{ActivityID: activity.ID, Action: "timeout_reset", Status: activity.Status}})
	}
	return reset
}

func evaluateMilestones(snapshot *Snapshot, now time.Time, effects *[]Effect) int {
	triggered := 0
	for activityIndex := range snapshot.Activities {
		activity := &snapshot.Activities[activityIndex]
		if activity.Status != "active" {
			continue
		}
		for milestoneIndex := range activity.Milestones {
			if activity.Status != "active" {
				break
			}
			milestone := &activity.Milestones[milestoneIndex]
			if milestone.TriggeredAtMillis > 0 {
				continue
			}
			attribute := findAttribute(snapshot, milestone.AttributeID)
			if attribute == nil {
				continue
			}
			reached := milestone.Comparison == "lte" && attribute.Value <= milestone.Threshold || milestone.Comparison == "gte" && attribute.Value >= milestone.Threshold
			if !reached {
				continue
			}
			value := attribute.Value
			milestone.TriggeredAtMillis = now.UnixMilli()
			milestone.TriggerValue = &value
			triggered++
			switch milestone.Action {
			case "lock":
				activity.Status = "locked"
				activity.LockedAtMillis = now.UnixMilli()
				clearActivityGiftTimeout(activity)
			case "settle":
				activity.Status = "settled"
				activity.LockedAtMillis = now.UnixMilli()
				activity.SettledAtMillis = now.UnixMilli()
				activity.Result = settleActivity(snapshot, activity)
				clearActivityGiftTimeout(activity)
			}
			*effects = append(*effects, Effect{Activity: &ActivityNotice{
				ActivityID: activity.ID, Action: milestone.Action, Status: activity.Status, MilestoneID: milestone.ID,
			}})
		}
	}
	return triggered
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func finite(value float64) bool { return !math.IsInf(value, 0) && !math.IsNaN(value) }

type expressionNode interface {
	evaluate(map[string]float64, func(int) int) (float64, error)
}

type numberNode float64

func (node numberNode) evaluate(_ map[string]float64, _ func(int) int) (float64, error) {
	return float64(node), nil
}

type variableNode string

func (node variableNode) evaluate(environment map[string]float64, _ func(int) int) (float64, error) {
	value, exists := environment[string(node)]
	if !exists {
		return 0, fmt.Errorf("变量 %q 未定义", string(node))
	}
	return value, nil
}

type unaryNode struct{ operand expressionNode }

func (node unaryNode) evaluate(environment map[string]float64, randomIntn func(int) int) (float64, error) {
	value, err := node.operand.evaluate(environment, randomIntn)
	return -value, err
}

type binaryNode struct {
	op          string
	left, right expressionNode
}

func (node binaryNode) evaluate(environment map[string]float64, randomIntn func(int) int) (float64, error) {
	left, err := node.left.evaluate(environment, randomIntn)
	if err != nil {
		return 0, err
	}
	right, err := node.right.evaluate(environment, randomIntn)
	if err != nil {
		return 0, err
	}
	switch node.op {
	case "+":
		return left + right, nil
	case "-":
		return left - right, nil
	case "*":
		return left * right, nil
	case "/":
		if right == 0 {
			return 0, fmt.Errorf("除数为零")
		}
		return left / right, nil
	case ">":
		return boolNumber(left > right), nil
	case ">=":
		return boolNumber(left >= right), nil
	case "<":
		return boolNumber(left < right), nil
	case "<=":
		return boolNumber(left <= right), nil
	case "=":
		return boolNumber(left == right), nil
	default:
		return 0, fmt.Errorf("未知运算符 %s", node.op)
	}
}

type callNode struct {
	name string
	args []expressionNode
}

func (node callNode) evaluate(environment map[string]float64, randomIntn func(int) int) (float64, error) {
	name := strings.ToUpper(node.name)
	evaluate := func(index int) (float64, error) { return node.args[index].evaluate(environment, randomIntn) }
	switch name {
	case "IF":
		if len(node.args) != 3 {
			return 0, fmt.Errorf("IF 需要 3 个参数")
		}
		condition, err := evaluate(0)
		if err != nil {
			return 0, err
		}
		if condition != 0 {
			return evaluate(1)
		}
		return evaluate(2)
	case "RANDOMCHOICE":
		if len(node.args) == 0 {
			return 0, fmt.Errorf("RANDOMCHOICE 至少需要 1 个参数")
		}
		if len(node.args) == 1 {
			return evaluate(0)
		}
		return evaluate(randomIntn(len(node.args)))
	case "MAX", "MIN":
		if len(node.args) == 0 {
			return 0, fmt.Errorf("%s 至少需要 1 个参数", name)
		}
		value, err := evaluate(0)
		if err != nil {
			return 0, err
		}
		for index := 1; index < len(node.args); index++ {
			next, nextErr := evaluate(index)
			if nextErr != nil {
				return 0, nextErr
			}
			if name == "MAX" {
				value = math.Max(value, next)
			} else {
				value = math.Min(value, next)
			}
		}
		return value, nil
	case "ROUND":
		if len(node.args) < 1 || len(node.args) > 2 {
			return 0, fmt.Errorf("ROUND 需要 1-2 个参数")
		}
		value, err := evaluate(0)
		if err != nil {
			return 0, err
		}
		digits := 0.0
		if len(node.args) == 2 {
			digits, err = evaluate(1)
			if err != nil {
				return 0, err
			}
		}
		power := math.Pow(10, digits)
		return math.Round(value*power) / power, nil
	case "ABS":
		if len(node.args) != 1 {
			return 0, fmt.Errorf("ABS 需要 1 个参数")
		}
		value, err := evaluate(0)
		return math.Abs(value), err
	case "FLOOR":
		if len(node.args) != 1 {
			return 0, fmt.Errorf("FLOOR 需要 1 个参数")
		}
		value, err := evaluate(0)
		return math.Floor(value), err
	case "RAND":
		if len(node.args) != 0 {
			return 0, fmt.Errorf("RAND 不需要参数")
		}
		return rand.Float64(), nil
	case "RANDBETWEEN":
		if len(node.args) != 2 {
			return 0, fmt.Errorf("RANDBETWEEN 需要 2 个参数")
		}
		minimum, err := evaluate(0)
		if err != nil {
			return 0, err
		}
		maximum, err := evaluate(1)
		if err != nil {
			return 0, err
		}
		low, high := int(math.Ceil(minimum)), int(math.Floor(maximum))
		if high < low {
			return 0, fmt.Errorf("RANDBETWEEN 最小值不能大于最大值")
		}
		return float64(low + randomIntn(high-low+1)), nil
	default:
		return 0, fmt.Errorf("未知函数 %q", node.name)
	}
}

func evaluateFormula(input string, environment map[string]float64, randomIntn func(int) int) (float64, error) {
	node, err := parseFormula(input)
	if err != nil {
		return 0, err
	}
	return node.evaluate(environment, randomIntn)
}

// EvaluateFormula evaluates one gameplay formula with the caller's random
// source. The random source parameter preserves the desktop adapter's existing
// deterministic replay/test seam without adding mutable state to Engine.
func EvaluateFormula(input string, environment map[string]float64, randomIntn func(int) int) (float64, error) {
	if randomIntn == nil {
		randomIntn = rand.Intn
	}
	return evaluateFormula(input, environment, randomIntn)
}

// ValidateFormula validates names, functions, arity, and guaranteed runtime
// failures without consuming randomness.
func ValidateFormula(input string, environment map[string]float64) error {
	node, err := parseFormula(input)
	if err != nil {
		return err
	}
	if err := validateFormulaNode(node, environment); err != nil {
		return err
	}
	result, err := validateGuaranteedFormulaSemantics(node)
	if err != nil {
		return err
	}
	if !result.classes.hasFinite() {
		return fmt.Errorf("规则结果不是有效数字")
	}
	return nil
}

// ValidateFormulaSyntax parses a formula without imposing an environment.
func ValidateFormulaSyntax(input string) error {
	_, err := parseFormula(input)
	return err
}

// RewriteFormulaIdentifier rewrites only parsed identifier tokens while
// preserving all original whitespace and formatting.
func RewriteFormulaIdentifier(input, oldName, newName string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return input, nil
	}
	if _, err := parseFormula(input); err != nil {
		return "", err
	}
	tokens, err := tokenizeFormula(input)
	if err != nil {
		return "", err
	}
	runes := []rune(input)
	var output strings.Builder
	cursor := 0
	for _, token := range tokens {
		if token.kind != "ident" || token.value != oldName {
			continue
		}
		output.WriteString(string(runes[cursor:token.pos]))
		output.WriteString(newName)
		cursor = token.pos + len([]rune(token.value))
	}
	output.WriteString(string(runes[cursor:]))
	return output.String(), nil
}

func validateFormulaNode(node expressionNode, environment map[string]float64) error {
	switch typed := node.(type) {
	case numberNode:
		return nil
	case variableNode:
		if _, exists := environment[string(typed)]; !exists {
			return fmt.Errorf("变量 %q 未定义", string(typed))
		}
		return nil
	case unaryNode:
		return validateFormulaNode(typed.operand, environment)
	case binaryNode:
		if err := validateFormulaNode(typed.left, environment); err != nil {
			return err
		}
		return validateFormulaNode(typed.right, environment)
	case callNode:
		name := strings.ToUpper(typed.name)
		argumentCount := len(typed.args)
		switch name {
		case "IF":
			if argumentCount != 3 {
				return fmt.Errorf("IF 需要 3 个参数")
			}
		case "RANDOMCHOICE":
			if argumentCount == 0 {
				return fmt.Errorf("RANDOMCHOICE 至少需要 1 个参数")
			}
		case "MAX", "MIN":
			if argumentCount == 0 {
				return fmt.Errorf("%s 至少需要 1 个参数", name)
			}
		case "ROUND":
			if argumentCount < 1 || argumentCount > 2 {
				return fmt.Errorf("ROUND 需要 1-2 个参数")
			}
		case "ABS", "FLOOR":
			if argumentCount != 1 {
				return fmt.Errorf("%s 需要 1 个参数", name)
			}
		case "RAND":
			if argumentCount != 0 {
				return fmt.Errorf("RAND 不需要参数")
			}
		case "RANDBETWEEN":
			if argumentCount != 2 {
				return fmt.Errorf("RANDBETWEEN 需要 2 个参数")
			}
		default:
			return fmt.Errorf("未知函数 %q", typed.name)
		}
		for _, argument := range typed.args {
			if err := validateFormulaNode(argument, environment); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("表达式不合法")
	}
}

type formulaValueClass uint8

const (
	formulaZero formulaValueClass = 1 << iota
	formulaFiniteNonZero
	formulaPositiveInfinity
	formulaNegativeInfinity
	formulaNaN
)

const (
	formulaFinite   = formulaZero | formulaFiniteNonZero
	formulaInfinity = formulaPositiveInfinity | formulaNegativeInfinity
	formulaTop      = formulaFinite | formulaInfinity | formulaNaN
)

func (classes formulaValueClass) hasFinite() bool { return classes&formulaFinite != 0 }

type formulaSemanticResult struct {
	classes formulaValueClass
	exact   bool
	value   float64
}

func exactFormulaSemanticResult(value float64) formulaSemanticResult {
	classes := formulaFiniteNonZero
	if value == 0 {
		classes = formulaZero
	} else if math.IsInf(value, 1) {
		classes = formulaPositiveInfinity
	} else if math.IsInf(value, -1) {
		classes = formulaNegativeInfinity
	} else if math.IsNaN(value) {
		classes = formulaNaN
	}
	return formulaSemanticResult{classes: classes, exact: true, value: value}
}

func formulaClassMembers(classes formulaValueClass) []formulaValueClass {
	members := make([]formulaValueClass, 0, 4)
	for _, class := range []formulaValueClass{formulaZero, formulaFiniteNonZero, formulaPositiveInfinity, formulaNegativeInfinity, formulaNaN} {
		if classes&class != 0 {
			members = append(members, class)
		}
	}
	return members
}

func abstractBinaryFormulaClasses(operator string, left, right formulaValueClass) (formulaValueClass, bool) {
	if left == formulaNaN || right == formulaNaN {
		if operator == ">" || operator == ">=" || operator == "<" || operator == "<=" || operator == "=" {
			return formulaFinite, false
		}
		return formulaNaN, false
	}
	if operator == ">" || operator == ">=" || operator == "<" || operator == "<=" || operator == "=" {
		return formulaFinite, false
	}
	switch operator {
	case "+", "-":
		leftInfinite := left&formulaInfinity != 0
		rightInfinite := right&formulaInfinity != 0
		if leftInfinite && rightInfinite {
			if operator == "+" && left == right {
				return left, false
			}
			if operator == "-" && left != right {
				return left, false
			}
			return formulaNaN, false
		}
		if leftInfinite {
			return left, false
		}
		if rightInfinite {
			if operator == "+" {
				return right, false
			}
			if right == formulaPositiveInfinity {
				return formulaNegativeInfinity, false
			}
			return formulaPositiveInfinity, false
		}
		if left == formulaZero && right == formulaZero {
			return formulaZero, false
		}
		return formulaFinite | formulaInfinity, false
	case "*":
		leftInfinite := left&formulaInfinity != 0
		rightInfinite := right&formulaInfinity != 0
		if leftInfinite || rightInfinite {
			if left == formulaZero || right == formulaZero {
				return formulaNaN, false
			}
			if leftInfinite && rightInfinite {
				if left == right {
					return formulaPositiveInfinity, false
				}
				return formulaNegativeInfinity, false
			}
			return formulaInfinity, false
		}
		if left == formulaZero || right == formulaZero {
			return formulaZero, false
		}
		return formulaFinite | formulaInfinity, false
	case "/":
		if right == formulaZero {
			return 0, true
		}
		if right&formulaInfinity != 0 {
			if left&formulaInfinity != 0 {
				return formulaNaN, false
			}
			return formulaZero, false
		}
		if left&formulaInfinity != 0 {
			return formulaInfinity, false
		}
		if left == formulaZero {
			return formulaZero, false
		}
		return formulaFinite | formulaInfinity, false
	default:
		return formulaTop, false
	}
}

func validateGuaranteedFormulaSemantics(node expressionNode) (formulaSemanticResult, error) {
	unknownFinite := formulaSemanticResult{classes: formulaFinite}
	top := formulaSemanticResult{classes: formulaTop}
	switch typed := node.(type) {
	case numberNode:
		return exactFormulaSemanticResult(float64(typed)), nil
	case variableNode:
		return unknownFinite, nil
	case unaryNode:
		operand, err := validateGuaranteedFormulaSemantics(typed.operand)
		if err != nil {
			return operand, err
		}
		if operand.exact {
			return exactFormulaSemanticResult(-operand.value), nil
		}
		classes := operand.classes &^ formulaInfinity
		if operand.classes&formulaPositiveInfinity != 0 {
			classes |= formulaNegativeInfinity
		}
		if operand.classes&formulaNegativeInfinity != 0 {
			classes |= formulaPositiveInfinity
		}
		return formulaSemanticResult{classes: classes}, nil
	case binaryNode:
		left, err := validateGuaranteedFormulaSemantics(typed.left)
		if err != nil {
			return top, err
		}
		right, err := validateGuaranteedFormulaSemantics(typed.right)
		if err != nil {
			return top, err
		}
		if left.exact && right.exact {
			if typed.op == "/" && right.value == 0 {
				return top, fmt.Errorf("除数为零")
			}
			value, evaluateErr := (binaryNode{op: typed.op, left: numberNode(left.value), right: numberNode(right.value)}).evaluate(nil, rand.Intn)
			if evaluateErr != nil {
				return top, evaluateErr
			}
			return exactFormulaSemanticResult(value), nil
		}
		classes := formulaValueClass(0)
		hasValidOutcome := false
		for _, leftClass := range formulaClassMembers(left.classes) {
			for _, rightClass := range formulaClassMembers(right.classes) {
				result, runtimeError := abstractBinaryFormulaClasses(typed.op, leftClass, rightClass)
				if !runtimeError {
					hasValidOutcome = true
					classes |= result
				}
			}
		}
		if !hasValidOutcome {
			return top, fmt.Errorf("除数为零")
		}
		return formulaSemanticResult{classes: classes}, nil
	case callNode:
		name := strings.ToUpper(typed.name)
		switch name {
		case "IF":
			condition, err := validateGuaranteedFormulaSemantics(typed.args[0])
			if err != nil || !condition.exact {
				return top, err
			}
			if condition.value != 0 {
				return validateGuaranteedFormulaSemantics(typed.args[1])
			}
			return validateGuaranteedFormulaSemantics(typed.args[2])
		case "RANDOMCHOICE":
			if len(typed.args) == 1 {
				return validateGuaranteedFormulaSemantics(typed.args[0])
			}
			return top, nil
		case "RAND":
			return unknownFinite, nil
		}

		arguments := make([]formulaSemanticResult, len(typed.args))
		allExact := true
		for index, argument := range typed.args {
			result, err := validateGuaranteedFormulaSemantics(argument)
			if err != nil {
				return top, err
			}
			arguments[index] = result
			allExact = allExact && result.exact
		}
		if name == "RANDBETWEEN" {
			if !allExact {
				return unknownFinite, nil
			}
			low, high := int(math.Ceil(arguments[0].value)), int(math.Floor(arguments[1].value))
			if high < low {
				return top, fmt.Errorf("RANDBETWEEN 最小值不能大于最大值")
			}
			if low == high {
				return exactFormulaSemanticResult(float64(low)), nil
			}
			return unknownFinite, nil
		}
		if !allExact {
			switch name {
			case "MAX", "MIN":
				classes := arguments[0].classes
				for _, argument := range arguments[1:] {
					nextClasses := formulaValueClass(0)
					for _, leftClass := range formulaClassMembers(classes) {
						for _, rightClass := range formulaClassMembers(argument.classes) {
							if leftClass == formulaNaN || rightClass == formulaNaN {
								nextClasses |= formulaNaN
								continue
							}
							if name == "MAX" {
								switch {
								case leftClass == formulaPositiveInfinity || rightClass == formulaPositiveInfinity:
									nextClasses |= formulaPositiveInfinity
								case leftClass == formulaNegativeInfinity:
									nextClasses |= rightClass
								case rightClass == formulaNegativeInfinity:
									nextClasses |= leftClass
								default:
									nextClasses |= formulaFinite
								}
							} else {
								switch {
								case leftClass == formulaNegativeInfinity || rightClass == formulaNegativeInfinity:
									nextClasses |= formulaNegativeInfinity
								case leftClass == formulaPositiveInfinity:
									nextClasses |= rightClass
								case rightClass == formulaPositiveInfinity:
									nextClasses |= leftClass
								default:
									nextClasses |= formulaFinite
								}
							}
						}
					}
					classes = nextClasses
				}
				return formulaSemanticResult{classes: classes}, nil
			case "ROUND":
				value := arguments[0]
				if !value.classes.hasFinite() {
					return formulaSemanticResult{classes: value.classes}, nil
				}
				if len(arguments) == 1 {
					return formulaSemanticResult{classes: value.classes}, nil
				}
				digits := arguments[1]
				if !digits.exact {
					return top, nil
				}
				power := math.Pow(10, digits.value)
				if power == 0 || math.IsInf(power, 0) || math.IsNaN(power) {
					return formulaSemanticResult{classes: formulaNaN}, nil
				}
				if value.classes&^formulaInfinity == 0 {
					return formulaSemanticResult{classes: value.classes}, nil
				}
				return formulaSemanticResult{classes: value.classes | formulaInfinity}, nil
			case "ABS", "FLOOR":
				classes := arguments[0].classes
				if name == "ABS" && classes&formulaNegativeInfinity != 0 {
					classes = classes&^formulaNegativeInfinity | formulaPositiveInfinity
				}
				return formulaSemanticResult{classes: classes}, nil
			default:
				return top, nil
			}
		}
		switch name {
		case "MAX", "MIN":
			value := arguments[0].value
			for _, argument := range arguments[1:] {
				if name == "MAX" {
					value = math.Max(value, argument.value)
				} else {
					value = math.Min(value, argument.value)
				}
			}
			return exactFormulaSemanticResult(value), nil
		case "ROUND":
			digits := 0.0
			if len(arguments) == 2 {
				digits = arguments[1].value
			}
			power := math.Pow(10, digits)
			return exactFormulaSemanticResult(math.Round(arguments[0].value*power) / power), nil
		case "ABS":
			return exactFormulaSemanticResult(math.Abs(arguments[0].value)), nil
		case "FLOOR":
			return exactFormulaSemanticResult(math.Floor(arguments[0].value)), nil
		default:
			return top, nil
		}
	default:
		return top, fmt.Errorf("表达式不合法")
	}
}

func parseFormula(input string) (expressionNode, error) {
	tokens, err := tokenizeFormula(input)
	if err != nil {
		return nil, err
	}
	parser := expressionParser{tokens: tokens}
	node, err := parser.parseExpression()
	if err != nil {
		return nil, err
	}
	if parser.peek().kind != "eof" {
		return nil, fmt.Errorf("多余的内容 %q", parser.peek().value)
	}
	return node, nil
}

type expressionToken struct {
	kind  string
	value string
	pos   int
}

func tokenizeFormula(input string) ([]expressionToken, error) {
	runes := []rune(input)
	tokens := make([]expressionToken, 0, len(runes)+1)
	for index := 0; index < len(runes); {
		char := runes[index]
		if unicode.IsSpace(char) {
			index++
			continue
		}
		if unicode.IsDigit(char) || char == '.' && index+1 < len(runes) && unicode.IsDigit(runes[index+1]) {
			start := index
			for index < len(runes) && (unicode.IsDigit(runes[index]) || runes[index] == '.') {
				index++
			}
			value := string(runes[start:index])
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return nil, fmt.Errorf("数字 %q 无效", value)
			}
			tokens = append(tokens, expressionToken{kind: "number", value: value, pos: start})
			continue
		}
		if unicode.IsLetter(char) || char == '_' {
			start := index
			for index < len(runes) && (unicode.IsLetter(runes[index]) || unicode.IsDigit(runes[index]) || runes[index] == '_') {
				index++
			}
			tokens = append(tokens, expressionToken{kind: "ident", value: string(runes[start:index]), pos: start})
			continue
		}
		if index+1 < len(runes) {
			two := string(runes[index : index+2])
			if two == ">=" || two == "<=" {
				tokens = append(tokens, expressionToken{kind: "op", value: two, pos: index})
				index += 2
				continue
			}
		}
		if strings.ContainsRune("+-*/><=,", char) {
			tokens = append(tokens, expressionToken{kind: "op", value: string(char), pos: index})
			index++
			continue
		}
		if char == '(' || char == ')' {
			tokens = append(tokens, expressionToken{kind: "paren", value: string(char), pos: index})
			index++
			continue
		}
		return nil, fmt.Errorf("无法识别的字符 %q", char)
	}
	return append(tokens, expressionToken{kind: "eof", pos: len(runes)}), nil
}

type expressionParser struct {
	tokens []expressionToken
	index  int
}

func (parser *expressionParser) peek() expressionToken { return parser.tokens[parser.index] }
func (parser *expressionParser) next() expressionToken {
	token := parser.tokens[parser.index]
	parser.index++
	return token
}
func (parser *expressionParser) isOperator(value string) bool {
	token := parser.peek()
	return token.kind == "op" && token.value == value
}
func (parser *expressionParser) parseExpression() (expressionNode, error) {
	left, err := parser.parseAdditive()
	if err != nil {
		return nil, err
	}
	token := parser.peek()
	if token.kind == "op" && (token.value == ">" || token.value == ">=" || token.value == "<" || token.value == "<=" || token.value == "=") {
		parser.next()
		right, rightErr := parser.parseAdditive()
		if rightErr != nil {
			return nil, rightErr
		}
		left = binaryNode{op: token.value, left: left, right: right}
	}
	return left, nil
}
func (parser *expressionParser) parseAdditive() (expressionNode, error) {
	left, err := parser.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for parser.isOperator("+") || parser.isOperator("-") {
		op := parser.next().value
		right, rightErr := parser.parseMultiplicative()
		if rightErr != nil {
			return nil, rightErr
		}
		left = binaryNode{op: op, left: left, right: right}
	}
	return left, nil
}
func (parser *expressionParser) parseMultiplicative() (expressionNode, error) {
	left, err := parser.parseUnary()
	if err != nil {
		return nil, err
	}
	for parser.isOperator("*") || parser.isOperator("/") {
		op := parser.next().value
		right, rightErr := parser.parseUnary()
		if rightErr != nil {
			return nil, rightErr
		}
		left = binaryNode{op: op, left: left, right: right}
	}
	return left, nil
}
func (parser *expressionParser) parseUnary() (expressionNode, error) {
	if parser.isOperator("-") {
		parser.next()
		operand, err := parser.parseUnary()
		return unaryNode{operand: operand}, err
	}
	return parser.parsePrimary()
}
func (parser *expressionParser) parsePrimary() (expressionNode, error) {
	token := parser.peek()
	switch token.kind {
	case "number":
		parser.next()
		value, _ := strconv.ParseFloat(token.value, 64)
		return numberNode(value), nil
	case "ident":
		parser.next()
		if parser.peek().kind == "paren" && parser.peek().value == "(" {
			parser.next()
			arguments := []expressionNode{}
			if !(parser.peek().kind == "paren" && parser.peek().value == ")") {
				for {
					argument, err := parser.parseExpression()
					if err != nil {
						return nil, err
					}
					arguments = append(arguments, argument)
					if !parser.isOperator(",") {
						break
					}
					parser.next()
				}
			}
			if parser.peek().kind != "paren" || parser.peek().value != ")" {
				return nil, fmt.Errorf("缺少 %q", ")")
			}
			parser.next()
			return callNode{name: token.value, args: arguments}, nil
		}
		return variableNode(token.value), nil
	case "paren":
		if token.value != "(" {
			return nil, fmt.Errorf("表达式不合法")
		}
		parser.next()
		node, err := parser.parseExpression()
		if err != nil {
			return nil, err
		}
		if parser.peek().kind != "paren" || parser.peek().value != ")" {
			return nil, fmt.Errorf("缺少 %q", ")")
		}
		parser.next()
		return node, nil
	default:
		return nil, fmt.Errorf("表达式不合法")
	}
}

func boolNumber(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
