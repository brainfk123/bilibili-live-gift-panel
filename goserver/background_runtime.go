package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

type giftEventSource interface {
	Run(context.Context, string, runtimeCallbacks) error
}

type runtimeCallbacks struct {
	onGift  func(giftEvent)
	onState func(string)
}

type runtimeStatus struct {
	State      string `json:"state"`
	RoomID     string `json:"roomId"`
	LastError  string `json:"lastError,omitempty"`
	LastGiftAt int64  `json:"lastGiftAt,omitempty"`
}

type backgroundRuntime struct {
	store           *configStore
	sourceFactory   func() giftEventSource
	reload          chan struct{}
	mu              sync.RWMutex
	status          runtimeStatus
	seen            map[string]time.Time
	timerMu         sync.Mutex
	timerSchedules  map[string]timerSchedule
	timerTicks      <-chan time.Time
	notifications   *notificationCenter
	giftQueue       chan giftEvent
	profileResolver userProfileResolver
}

type timerSchedule struct {
	interval time.Duration
	next     time.Time
}

func newBackgroundRuntime(store *configStore, sourceFactory func() giftEventSource, notifications ...*notificationCenter) *backgroundRuntime {
	if sourceFactory == nil {
		sourceFactory = func() giftEventSource { return &bilibiliGiftSource{} }
	}
	var center *notificationCenter
	if len(notifications) > 0 {
		center = notifications[0]
	}
	return &backgroundRuntime{
		store:           store,
		sourceFactory:   sourceFactory,
		reload:          make(chan struct{}, 1),
		status:          runtimeStatus{State: "idle"},
		seen:            map[string]time.Time{},
		timerSchedules:  map[string]timerSchedule{},
		notifications:   center,
		giftQueue:       make(chan giftEvent, 256),
		profileResolver: newBilibiliUserProfileResolver(nil, ""),
	}
}

func (runtime *backgroundRuntime) Run(ctx context.Context) {
	go runtime.runGiftLoop(ctx)
	go runtime.runTimerLoop(ctx)
	runtime.runConnectionLoop(ctx)
}

func (runtime *backgroundRuntime) runConnectionLoop(ctx context.Context) {
	for {
		state, err := runtime.store.readState()
		if err != nil {
			runtime.setStatus("error", "", err)
			if !runtime.wait(ctx, 2*time.Second) {
				return
			}
			continue
		}
		if strings.TrimSpace(state.RoomID) == "" {
			runtime.setStatus("idle", "", nil)
			select {
			case <-ctx.Done():
				return
			case <-runtime.reload:
				continue
			}
		}

		roomID := state.RoomID
		connectionContext, cancel := context.WithCancel(ctx)
		finished := make(chan error, 1)
		source := runtime.sourceFactory()
		runtime.setStatus("connecting", roomID, nil)
		go func() {
			finished <- source.Run(connectionContext, roomID, runtimeCallbacks{
				onGift: func(gift giftEvent) {
					runtime.enqueueGift(connectionContext, gift)
				},
				onState: func(status string) {
					runtime.setStatus(status, roomID, nil)
				},
			})
		}()

		select {
		case <-ctx.Done():
			cancel()
			return
		case <-runtime.reload:
			cancel()
			<-finished
			continue
		case err := <-finished:
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				runtime.setStatus("reconnecting", roomID, err)
			}
			if !runtime.wait(ctx, 2*time.Second) {
				return
			}
		}
	}
}

func (runtime *backgroundRuntime) enqueueGift(ctx context.Context, gift giftEvent) {
	select {
	case runtime.giftQueue <- gift:
	case <-ctx.Done():
	}
}

func (runtime *backgroundRuntime) runGiftLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case gift := <-runtime.giftQueue:
			runtime.handleGift(gift)
		}
	}
}

func (runtime *backgroundRuntime) runTimerLoop(ctx context.Context) {
	if runtime.timerTicks != nil {
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-runtime.timerTicks:
				runtime.handleTimerTick(now)
			}
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			runtime.handleTimerTick(now)
		}
	}
}

func (runtime *backgroundRuntime) handleTimerTick(now time.Time) {
	state, err := runtime.store.readState()
	if err != nil {
		status := runtime.Status()
		runtime.setStatus("error", status.RoomID, err)
		return
	}
	dueRuleIDs := runtime.dueTimerRuleIDs(state, now)
	if len(dueRuleIDs) == 0 {
		return
	}
	_, err = runtime.store.updateState(func(current *appState) error {
		if applyTimerRules(current, dueRuleIDs, now) == 0 {
			return errNoTimerChanges
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoTimerChanges) {
		status := runtime.Status()
		runtime.setStatus("error", status.RoomID, err)
	}
}

func (runtime *backgroundRuntime) dueTimerRuleIDs(state appState, now time.Time) []string {
	runtime.timerMu.Lock()
	defer runtime.timerMu.Unlock()
	valid := make(map[string]struct{}, len(state.TimerRules))
	due := []string{}
	for _, rule := range state.TimerRules {
		if !rule.Enabled || rule.IntervalSeconds < 1 {
			continue
		}
		valid[rule.ID] = struct{}{}
		interval := time.Duration(rule.IntervalSeconds) * time.Second
		schedule, exists := runtime.timerSchedules[rule.ID]
		if !exists || schedule.interval != interval {
			runtime.timerSchedules[rule.ID] = timerSchedule{interval: interval, next: now.Add(interval)}
			continue
		}
		if !now.Before(schedule.next) {
			due = append(due, rule.ID)
			schedule.next = now.Add(interval)
			runtime.timerSchedules[rule.ID] = schedule
		}
	}
	for ruleID := range runtime.timerSchedules {
		if _, exists := valid[ruleID]; !exists {
			delete(runtime.timerSchedules, ruleID)
		}
	}
	return due
}

func (runtime *backgroundRuntime) NotifyConfigChanged() {
	select {
	case runtime.reload <- struct{}{}:
	default:
	}
}

func (runtime *backgroundRuntime) Status() runtimeStatus {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.status
}

func (runtime *backgroundRuntime) handleGift(gift giftEvent) {
	if runtime.isDuplicate(gift.Rnd) {
		return
	}
	if needsUserProfile(gift) && runtime.profileResolver != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		profile, err := runtime.profileResolver.Resolve(ctx, gift.UID)
		cancel()
		if err == nil {
			if isMaskedUsername(gift.Uname) && profile.Name != "" {
				gift.Uname = profile.Name
			}
			if strings.TrimSpace(gift.Avatar) == "" && profile.Avatar != "" {
				gift.Avatar = profile.Avatar
			}
		}
	}
	_, err := runtime.store.updateState(func(state *appState) error {
		applyGiftEvent(state, gift)
		return nil
	})
	if err != nil {
		status := runtime.Status()
		runtime.setStatus("error", status.RoomID, err)
		return
	}
	runtime.mu.Lock()
	runtime.status.LastGiftAt = time.Now().UnixMilli()
	runtime.mu.Unlock()
}

func (runtime *backgroundRuntime) isDuplicate(key string) bool {
	if key == "" {
		return false
	}
	now := time.Now()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	last, exists := runtime.seen[key]
	runtime.seen[key] = now
	if len(runtime.seen) > 500 {
		for candidate, timestamp := range runtime.seen {
			if now.Sub(timestamp) > time.Minute {
				delete(runtime.seen, candidate)
			}
		}
	}
	return exists && now.Sub(last) < time.Minute
}

func (runtime *backgroundRuntime) setStatus(state, roomID string, err error) {
	runtime.mu.Lock()
	previous := runtime.status
	runtime.status.State = state
	runtime.status.RoomID = roomID
	if err == nil {
		runtime.status.LastError = ""
	} else {
		runtime.status.LastError = err.Error()
	}
	runtime.mu.Unlock()

	if previous.State != "connected" && state == "connected" {
		runtime.notifications.Publish(notificationRoomConnected, roomID)
	}
	if previous.State == "connected" && state != "connected" {
		disconnectedRoomID := previous.RoomID
		if disconnectedRoomID == "" {
			disconnectedRoomID = roomID
		}
		runtime.notifications.Publish(notificationRoomDisconnected, disconnectedRoomID)
	}
}

func (runtime *backgroundRuntime) wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-runtime.reload:
		return true
	case <-timer.C:
		return true
	}
}

func applyGiftEvent(state *appState, gift giftEvent) {
	normalizeAppState(state)
	upsertRecentGiftState(state, gift)
	stats := state.todayStats()
	stats.GiftTotals[giftKey(gift.GiftID)] += maxInt(1, gift.Num)
	repetitions := maxInt(1, gift.Num)
	changes := []logEntry{}
	changeIndexes := map[string]int{}
	for occurrence := 0; occurrence < repetitions; occurrence++ {
		for _, rule := range state.Rules {
			if !rule.enabled() {
				continue
			}
			configuredGift := state.findGift(rule.GiftID)
			matchesAlias := configuredGift != nil && sameGiftIdentity(*configuredGift, gift)
			matchesBlindBoxParent := gift.BlindGiftID > 0 && rule.GiftID == gift.BlindGiftID
			if !rule.matchesGiftID(gift.GiftID) && !matchesAlias && !matchesBlindBoxParent {
				continue
			}
			attribute := state.findAttribute(rule.AttributeName)
			if attribute == nil {
				continue
			}
			if rule.MinPrice != nil && gift.Price < *rule.MinPrice {
				continue
			}
			triggerCount := stats.RuleTriggers[rule.ID]
			if rule.DailyLimit != nil && triggerCount >= *rule.DailyLimit {
				continue
			}
			environment := map[string]float64{"price": gift.Price}
			for _, candidate := range state.Attributes {
				environment[candidate.Name] = candidate.Value
			}
			nextValue, err := evaluateFormula(rule.Formula, environment)
			if err != nil || math.IsInf(nextValue, 0) || math.IsNaN(nextValue) {
				continue
			}
			if rule.Cap != nil {
				nextValue = math.Min(nextValue, *rule.Cap)
			}
			before := attribute.Value
			attribute.Value = nextValue
			stats.RuleTriggers[rule.ID] = triggerCount + 1
			delta := nextValue - before
			if index, exists := changeIndexes[attribute.Name]; exists {
				changes[index].Delta += delta
				changes[index].ValueAfter = nextValue
				continue
			}
			changeIndexes[attribute.Name] = len(changes)
			changes = append(changes, logEntry{
				Time:          gift.Timestamp,
				GiftID:        gift.GiftID,
				GiftName:      gift.GiftName,
				Num:           repetitions,
				Uname:         gift.Uname,
				Avatar:        gift.Avatar,
				SenderUID:     gift.UID,
				AttributeName: attribute.Name,
				Delta:         delta,
				ValueAfter:    nextValue,
				RuleID:        rule.ID,
				Source:        "gift",
				TriggerName:   rule.FormulaName,
				EventID:       fmt.Sprintf("%s:%s", gift.Rnd, attribute.Name),
			})
		}
	}
	if len(changes) > 0 {
		state.Log = append(changes, state.Log...)
		if len(state.Log) > maxLogEntries {
			state.Log = state.Log[:maxLogEntries]
		}
	}
	state.Stats[stats.Date] = stats
}

var errNoTimerChanges = errors.New("no timer changes")

func applyTimerRules(state *appState, dueRuleIDs []string, now time.Time) int {
	normalizeAppState(state)
	due := make(map[string]struct{}, len(dueRuleIDs))
	for _, ruleID := range dueRuleIDs {
		due[ruleID] = struct{}{}
	}
	stats := state.todayStats()
	applied := 0
	for _, rule := range state.TimerRules {
		if _, exists := due[rule.ID]; !exists || !rule.Enabled {
			continue
		}
		attribute := state.findAttribute(rule.AttributeName)
		if attribute == nil {
			continue
		}
		environment := make(map[string]float64, len(state.Attributes))
		for _, candidate := range state.Attributes {
			environment[candidate.Name] = candidate.Value
		}
		if strings.TrimSpace(rule.Condition) != "" {
			conditionResult, err := evaluateFormula(rule.Condition, environment)
			if err != nil || conditionResult == 0 || math.IsInf(conditionResult, 0) || math.IsNaN(conditionResult) {
				continue
			}
		}
		nextValue, err := evaluateFormula(rule.Formula, environment)
		if err != nil || math.IsInf(nextValue, 0) || math.IsNaN(nextValue) {
			continue
		}
		before := attribute.Value
		attribute.Value = nextValue
		stats.RuleTriggers[rule.ID]++
		state.Log = append([]logEntry{{
			Time:          now.Unix(),
			AttributeName: attribute.Name,
			Delta:         nextValue - before,
			ValueAfter:    nextValue,
			RuleID:        rule.ID,
			Source:        "timer",
			TriggerName:   rule.FormulaName,
		}}, state.Log...)
		if len(state.Log) > maxLogEntries {
			state.Log = state.Log[:maxLogEntries]
		}
		applied++
	}
	state.Stats[stats.Date] = stats
	return applied
}

func upsertRecentGiftState(state *appState, gift giftEvent) {
	for index := range state.RecentGifts {
		if state.RecentGifts[index].ID == gift.GiftID {
			state.RecentGifts[index].Count += maxInt(1, gift.Num)
			state.RecentGifts[index].LastReceived = gift.Timestamp
			state.RecentGifts[index].Name = gift.GiftName
			state.RecentGifts[index].Price = gift.Price
			state.RecentGifts[index].CoinType = gift.CoinType
			if gift.ImgBasic != "" {
				state.RecentGifts[index].ImgBasic = gift.ImgBasic
			}
			return
		}
	}
	state.RecentGifts = append([]recentGift{{
		giftInfo:     giftInfo{ID: gift.GiftID, Name: gift.GiftName, Price: gift.Price, CoinType: gift.CoinType, ImgBasic: gift.ImgBasic},
		LastReceived: gift.Timestamp,
		Count:        maxInt(1, gift.Num),
	}}, state.RecentGifts...)
	if len(state.RecentGifts) > 100 {
		state.RecentGifts = state.RecentGifts[:100]
	}
}

func sameGiftIdentity(configured giftInfo, gift giftEvent) bool {
	if strings.TrimSpace(configured.Name) != strings.TrimSpace(gift.GiftName) {
		return false
	}
	if configured.Price != gift.Price || configured.CoinType != gift.CoinType {
		return false
	}
	return configured.ImgBasic == "" || gift.ImgBasic == "" || configured.ImgBasic == gift.ImgBasic
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func formulaPreview(state appState, formula, attributeName string, attributeValue float64) (float64, error) {
	environment := map[string]float64{"price": 1000}
	for _, attribute := range state.Attributes {
		environment[attribute.Name] = attribute.Value
	}
	environment[attributeName] = attributeValue
	result, err := evaluateFormula(formula, environment)
	if err != nil {
		return 0, err
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, fmt.Errorf("公式结果不是有效数字")
	}
	return result, nil
}

func timerFormulaPreview(state appState, formula, attributeName string, attributeValue float64) (float64, error) {
	environment := map[string]float64{}
	for _, attribute := range state.Attributes {
		environment[attribute.Name] = attribute.Value
	}
	environment[attributeName] = attributeValue
	result, err := evaluateFormula(formula, environment)
	if err != nil {
		return 0, err
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, fmt.Errorf("公式结果不是有效数字")
	}
	return result, nil
}
