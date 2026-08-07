package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxConfigBytes = 8 << 20

type configStore struct {
	path              string
	mu                sync.RWMutex
	onChange          func()
	onTimerChange     func()
	onUpdateChange    func()
	migrationRequired bool
}

func newDefaultConfigStore() (*configStore, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("无法确定配置目录：%w", err)
	}
	store := &configStore{path: filepath.Join(root, "BilibiliLiveGiftPanel", "config.json")}
	if err := store.migrateLegacy(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *configStore) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGet(w)
	case http.MethodPut:
		s.handlePut(w, r)
	case http.MethodPatch:
		s.handlePatch(w, r)
	case http.MethodDelete:
		s.handleDelete(w)
	default:
		w.Header().Set("Allow", "GET, PUT, PATCH, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
	}
}

func (s *configStore) handleGet(w http.ResponseWriter) {
	s.mu.Lock()
	if !s.hasStoredStateLocked() {
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	state, err := s.readStateLocked()
	s.mu.Unlock()
	if err != nil {
		writeConfigStoreError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(state)
}

func (s *configStore) handlePut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"code": -1, "message": "配置文件过大"})
		return
	}
	state := defaultAppState()
	state.Settings.ShowTutorial = nil
	state.Settings.TutorialReplayMode = nil
	state.Settings.AutoUpdate = nil
	if err := json.Unmarshal(body, &state); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "配置必须是有效的 JSON 对象"})
		return
	}
	normalizeAppState(&state)
	if err := validateAppState(state); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
		return
	}
	replaced, err := s.replaceClientState(state)
	if err != nil {
		writeConfigStoreError(w, err)
		return
	}
	previous := replaced.Previous
	previousErr := replaced.PreviousErr
	state = replaced.State
	s.notifyStateChanges(previous, previousErr, state)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0})
}

type configInputError struct {
	err error
}

func (e *configInputError) Error() string {
	return e.err.Error()
}

func (s *configStore) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"code": -1, "message": "配置补丁过大"})
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil || len(fields) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "配置补丁必须是非空 JSON 对象"})
		return
	}
	replaced, err := s.patchClientState(fields)
	if err != nil {
		var inputError *configInputError
		if errors.As(err, &inputError) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": inputError.Error()})
			return
		}
		writeConfigStoreError(w, err)
		return
	}
	s.notifyStateChanges(replaced.Previous, replaced.PreviousErr, replaced.State)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0})
}

func writeConfigStoreError(w http.ResponseWriter, err error) {
	var versionError *unsupportedStateVersionError
	if errors.As(err, &versionError) {
		writeJSON(w, http.StatusConflict, map[string]any{"code": -1, "message": versionError.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
}

func (s *configStore) notifyStateChanges(previous appState, previousErr error, state appState) {
	roomChanged := previousErr != nil || strings.TrimSpace(previous.RoomID) != strings.TrimSpace(state.RoomID)
	if roomChanged {
		s.notifyChanged()
	}
	if previousErr != nil || !reflect.DeepEqual(previous.TimerRules, state.TimerRules) {
		s.notifyTimerChanged()
	}
	if previousErr != nil || autoUpdateEnabled(previous) != autoUpdateEnabled(state) {
		s.notifyUpdateChanged()
	}
}

func applyClientStatePatch(state *appState, fields map[string]json.RawMessage) error {
	for key, raw := range fields {
		var target any
		switch key {
		case "roomId":
			target = &state.RoomID
		case "attributes":
			target = &state.Attributes
		case "displayScenes":
			target = &state.DisplayScenes
		case "blindBoxDisplay":
			target = &state.BlindBoxDisplay
		case "giftKpiPanels":
			target = &state.GiftKPIPanels
		case "activities":
			target = &state.Activities
		case "rules":
			target = &state.Rules
		case "timerRules":
			target = &state.TimerRules
		case "formulaPresets":
			target = &state.FormulaPresets
		case "settings":
			target = &state.Settings
		case "giftCatalog":
			target = &state.GiftCatalog
		case "recentGifts":
			target = &state.RecentGifts
		case "stats":
			target = &state.Stats
		case "log":
			target = &state.Log
		case "contributions":
			target = &state.Contributions
		default:
			return fmt.Errorf("不支持的配置字段：%s", key)
		}
		// A PATCH field replaces the complete top-level field. json.Unmarshal
		// reuses existing slice elements and struct fields, so omitted optional
		// members (for example an activity's sceneId) would otherwise survive
		// the replacement and leave stale references behind.
		targetValue := reflect.ValueOf(target)
		targetValue.Elem().Set(reflect.Zero(targetValue.Elem().Type()))
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("配置字段 %s 格式不正确", key)
		}
	}
	return nil
}

func validateAppState(state appState) error {
	attributeNames := make(map[string]struct{}, len(state.Attributes))
	for _, attribute := range state.Attributes {
		name := strings.TrimSpace(attribute.Name)
		if name == "" {
			return fmt.Errorf("属性名不能为空")
		}
		if _, exists := attributeNames[name]; exists {
			return fmt.Errorf("属性名不能重复：%s", name)
		}
		attributeNames[name] = struct{}{}
		if utf8.RuneCountInString(attribute.BroadcastMessage) > 200 {
			return fmt.Errorf("属性 %q 的默认播报消息不能超过 200 个字符", name)
		}
		if attribute.Display != nil {
			if !isDisplayVariant(attribute.Display.Variant) {
				return fmt.Errorf("属性 %q 的 OBS 展示类型无效", name)
			}
			if !isDisplayThemeID(attribute.Display.ThemeID) {
				return fmt.Errorf("属性 %q 的 OBS 主题无效", name)
			}
			if attribute.Display.Appearance != nil && !validateDisplayAppearanceState(*attribute.Display.Appearance) {
				return fmt.Errorf("属性 %q 的 OBS 外观无效", name)
			}
			if attribute.Display.Min != nil && attribute.Display.Max != nil && *attribute.Display.Max <= *attribute.Display.Min {
				return fmt.Errorf("属性 %q 的 OBS 展示上限必须大于下限", name)
			}
			if len(attribute.Display.ValueMappings) > 50 {
				return fmt.Errorf("属性 %q 最多配置 50 条枚举映射", name)
			}
			mappingValues := make(map[float64]struct{}, len(attribute.Display.ValueMappings))
			for _, mapping := range attribute.Display.ValueMappings {
				if strings.TrimSpace(mapping.Label) == "" || utf8.RuneCountInString(mapping.Label) > 80 {
					return fmt.Errorf("属性 %q 的枚举文字不能为空且不能超过 80 个字", name)
				}
				if _, exists := mappingValues[mapping.Value]; exists {
					return fmt.Errorf("属性 %q 的枚举数值不能重复：%v", name, mapping.Value)
				}
				mappingValues[mapping.Value] = struct{}{}
				if mapping.Color != "" && !isHexColor(mapping.Color) {
					return fmt.Errorf("属性 %q 的枚举颜色必须是六位十六进制颜色", name)
				}
				if utf8.RuneCountInString(mapping.ImageURL) > 2048 || mapping.ImageURL != "" && !isDisplayImageURL(mapping.ImageURL) {
					return fmt.Errorf("属性 %q 的枚举图片必须使用 http、https 或 data:image 地址", name)
				}
			}
		}
	}
	sceneIDs := make(map[string]struct{}, len(state.DisplayScenes))
	sceneNames := make(map[string]struct{}, len(state.DisplayScenes))
	for _, scene := range state.DisplayScenes {
		id := strings.TrimSpace(scene.ID)
		name := strings.TrimSpace(scene.Name)
		if id == "" || name == "" {
			return fmt.Errorf("组合面板的 ID 和名称不能为空")
		}
		if _, exists := sceneIDs[id]; exists {
			return fmt.Errorf("组合面板 ID 不能重复：%s", id)
		}
		sceneIDs[id] = struct{}{}
		nameKey := strings.ToLower(name)
		if _, exists := sceneNames[nameKey]; exists {
			return fmt.Errorf("组合面板名称不能重复：%s", name)
		}
		sceneNames[nameKey] = struct{}{}
		if len(scene.AttributeNames) == 0 || len(scene.AttributeNames) > 12 {
			return fmt.Errorf("组合面板 %q 必须包含 1 到 12 个属性", name)
		}
		for _, attributeName := range scene.AttributeNames {
			if _, exists := attributeNames[attributeName]; !exists {
				return fmt.Errorf("组合面板 %q 引用了不存在的属性 %q", name, attributeName)
			}
		}
		if !isDisplaySceneLayout(scene.Layout) {
			return fmt.Errorf("组合面板 %q 的布局无效", name)
		}
		if !isDisplayThemeID(scene.ThemeID) {
			return fmt.Errorf("组合面板 %q 的 OBS 主题无效", name)
		}
		if scene.Appearance != nil && !validateDisplayAppearanceState(*scene.Appearance) {
			return fmt.Errorf("组合面板 %q 的 OBS 外观无效", name)
		}
	}
	if !validateDisplayAppearanceState(state.BlindBoxDisplay.displayAppearanceState) ||
		state.BlindBoxDisplay.ViewerSlots < blindBoxViewerSlotsMin || state.BlindBoxDisplay.ViewerSlots > blindBoxViewerSlotsMax {
		return fmt.Errorf("盲盒盈亏榜的 OBS 外观无效")
	}
	kpiPanelIDs := make(map[string]struct{}, len(state.GiftKPIPanels))
	for _, panel := range state.GiftKPIPanels {
		id := strings.TrimSpace(panel.ID)
		name := strings.TrimSpace(panel.Name)
		if id == "" || name == "" {
			return fmt.Errorf("礼物目标面板的 ID 和名称不能为空")
		}
		if _, exists := kpiPanelIDs[id]; exists {
			return fmt.Errorf("礼物目标面板 ID 不能重复：%s", id)
		}
		kpiPanelIDs[id] = struct{}{}
		if !isGiftTargetLayout(panel.Layout) {
			return fmt.Errorf("礼物目标面板 %q 的布局无效", name)
		}
		if len(panel.Items) == 0 || len(panel.Items) > 12 {
			return fmt.Errorf("礼物目标面板 %q 必须包含 1 到 12 个礼物", name)
		}
		giftIDs := make(map[int]struct{}, len(panel.Items))
		for _, item := range panel.Items {
			if item.GiftID <= 0 || strings.TrimSpace(item.GiftName) == "" || item.Target < 1 || item.Received < 0 {
				return fmt.Errorf("礼物目标面板 %q 包含无效的礼物目标", name)
			}
			if _, exists := giftIDs[item.GiftID]; exists {
				return fmt.Errorf("礼物目标面板 %q 不能重复添加同一个礼物", name)
			}
			giftIDs[item.GiftID] = struct{}{}
			if item.BarStyle != "progress" && item.BarStyle != "resource" && item.BarStyle != "health" {
				return fmt.Errorf("礼物目标面板 %q 的进度条样式无效", name)
			}
		}
		if !validateDisplayAppearanceState(panel.Appearance) {
			return fmt.Errorf("礼物目标面板 %q 的 OBS 外观无效", name)
		}
	}
	activityIDs := make(map[string]struct{}, len(state.Activities))
	activityNames := make(map[string]struct{}, len(state.Activities))
	gatedAttributes := make(map[string]string)
	linkedScenes := make(map[string]string)
	for _, activity := range state.Activities {
		id := strings.TrimSpace(activity.ID)
		name := strings.TrimSpace(activity.Name)
		if id == "" || name == "" {
			return fmt.Errorf("活动会话的 ID 和名称不能为空")
		}
		if _, exists := activityIDs[id]; exists {
			return fmt.Errorf("活动会话 ID 不能重复：%s", id)
		}
		activityIDs[id] = struct{}{}
		nameKey := strings.ToLower(name)
		if _, exists := activityNames[nameKey]; exists {
			return fmt.Errorf("活动会话名称不能重复：%s", name)
		}
		activityNames[nameKey] = struct{}{}
		if len(activity.AttributeNames) == 0 || len(activity.AttributeNames) > 12 {
			return fmt.Errorf("活动会话 %q 必须包含 1 到 12 个属性", name)
		}
		for _, attributeName := range activity.AttributeNames {
			if _, exists := attributeNames[attributeName]; !exists {
				return fmt.Errorf("活动会话 %q 引用了不存在的属性 %q", name, attributeName)
			}
			if activity.GateRules {
				if owner, exists := gatedAttributes[attributeName]; exists {
					return fmt.Errorf("属性 %q 不能同时由活动 %q 和 %q 控制", attributeName, owner, name)
				}
				gatedAttributes[attributeName] = name
			}
		}
		if !isActivityStatus(activity.Status) || !isActivityResultMode(activity.ResultMode) {
			return fmt.Errorf("活动会话 %q 的状态或结算方式无效", name)
		}
		if activity.SceneID != "" {
			if _, exists := sceneIDs[activity.SceneID]; !exists {
				return fmt.Errorf("活动会话 %q 引用了不存在的组合面板", name)
			}
			if owner, exists := linkedScenes[activity.SceneID]; exists {
				return fmt.Errorf("组合面板不能同时关联活动 %q 和 %q", owner, name)
			}
			linkedScenes[activity.SceneID] = name
		}
		if activity.Result != nil && activity.Result.WinnerAttributeName != "" && !containsString(activity.AttributeNames, activity.Result.WinnerAttributeName) {
			return fmt.Errorf("活动会话 %q 的胜出属性无效", name)
		}
		milestoneIDs := make(map[string]struct{}, len(activity.Milestones))
		for _, milestone := range activity.Milestones {
			milestoneID := strings.TrimSpace(milestone.ID)
			milestoneName := strings.TrimSpace(milestone.Name)
			attributeName := strings.TrimSpace(milestone.AttributeName)
			if milestoneID == "" || milestoneName == "" || attributeName == "" {
				return fmt.Errorf("活动会话 %q 的里程碑 ID、名称和属性不能为空", name)
			}
			if _, exists := milestoneIDs[milestoneID]; exists {
				return fmt.Errorf("活动会话 %q 的里程碑 ID 不能重复：%s", name, milestoneID)
			}
			milestoneIDs[milestoneID] = struct{}{}
			if !containsString(activity.AttributeNames, attributeName) {
				return fmt.Errorf("活动会话 %q 的里程碑 %q 引用了未关联的属性 %q", name, milestoneName, attributeName)
			}
			if milestone.Comparison != "gte" && milestone.Comparison != "lte" {
				return fmt.Errorf("活动会话 %q 的里程碑 %q 比较方式无效", name, milestoneName)
			}
			if milestone.Action != "announce" && milestone.Action != "lock" && milestone.Action != "settle" {
				return fmt.Errorf("活动会话 %q 的里程碑 %q 动作无效", name, milestoneName)
			}
			if utf8.RuneCountInString(strings.TrimSpace(milestone.Message)) > 120 {
				return fmt.Errorf("活动会话 %q 的里程碑 %q 播报不能超过 120 个字", name, milestoneName)
			}
		}
		if activity.GiftTimeout != nil {
			if activity.GiftTimeout.Seconds < 1 || activity.GiftTimeout.Seconds > 86_400 {
				return fmt.Errorf("活动会话 %q 的送礼后倒计时必须在 1 秒到 24 小时之间", name)
			}
			if activity.GiftTimeout.Action != "lock" && activity.GiftTimeout.Action != "settle" && activity.GiftTimeout.Action != "reset" {
				return fmt.Errorf("活动会话 %q 的送礼后倒计时动作无效", name)
			}
		}
	}
	for _, rule := range state.Rules {
		attribute := state.findAttribute(rule.AttributeName)
		if attribute == nil {
			return fmt.Errorf("规则 %q 引用了不存在的属性 %q", rule.FormulaName, rule.AttributeName)
		}
		if strings.TrimSpace(rule.Formula) == "" {
			return fmt.Errorf("规则 %q 不能为空", rule.FormulaName)
		}
		if _, err := formulaPreview(state, rule.Formula, attribute.Name, attribute.Value); err != nil {
			return fmt.Errorf("规则 %q 无效：%w", rule.FormulaName, err)
		}
	}
	for _, rule := range state.TimerRules {
		attribute := state.findAttribute(rule.AttributeName)
		if attribute == nil {
			return fmt.Errorf("定时器 %q 引用了不存在的属性 %q", rule.FormulaName, rule.AttributeName)
		}
		if rule.IntervalSeconds < 1 {
			return fmt.Errorf("定时器 %q 的间隔必须至少为 1 秒", rule.FormulaName)
		}
		if strings.TrimSpace(rule.Formula) == "" {
			return fmt.Errorf("定时器 %q 的规则不能为空", rule.FormulaName)
		}
		if strings.TrimSpace(rule.Condition) != "" {
			if _, err := timerFormulaPreview(state, rule.Condition, attribute.Name, attribute.Value); err != nil {
				return fmt.Errorf("定时器 %q 的运行条件无效：%w", rule.FormulaName, err)
			}
		}
		if _, err := timerFormulaPreview(state, rule.Formula, attribute.Name, attribute.Value); err != nil {
			return fmt.Errorf("定时器 %q 的规则无效：%w", rule.FormulaName, err)
		}
	}
	presetIDs := make(map[string]struct{}, len(state.FormulaPresets))
	presetNames := make(map[string]struct{}, len(state.FormulaPresets))
	for _, preset := range state.FormulaPresets {
		id := strings.TrimSpace(preset.ID)
		name := strings.TrimSpace(preset.Name)
		formula := strings.TrimSpace(preset.Formula)
		sourceAttributeName := strings.TrimSpace(preset.SourceAttributeName)
		if id == "" || name == "" || formula == "" || sourceAttributeName == "" {
			return fmt.Errorf("规则预设的 ID、名称、规则和来源属性不能为空")
		}
		if preset.Context != "gift" && preset.Context != "timer" {
			return fmt.Errorf("规则预设 %q 的适用场景无效", name)
		}
		if _, exists := presetIDs[id]; exists {
			return fmt.Errorf("规则预设 ID 不能重复：%s", id)
		}
		presetIDs[id] = struct{}{}
		nameKey := preset.Context + "\x00" + strings.ToLower(name)
		if _, exists := presetNames[nameKey]; exists {
			return fmt.Errorf("同类规则预设名称不能重复：%s", name)
		}
		presetNames[nameKey] = struct{}{}
		tokens, err := tokenizeFormula(formula)
		if err != nil {
			return fmt.Errorf("规则预设 %q 无效：%w", name, err)
		}
		parser := formulaParser{tokens: tokens}
		if _, err := parser.parseExpression(); err != nil {
			return fmt.Errorf("规则预设 %q 无效：%w", name, err)
		}
		if parser.peek().kind != "eof" {
			return fmt.Errorf("规则预设 %q 含有多余内容 %q", name, parser.peek().value)
		}
	}
	return nil
}

func (s *configStore) handleDelete(w http.ResponseWriter) {
	previous, _ := s.readState()
	s.mu.Lock()
	var removeErr error
	for _, path := range s.statePaths() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && removeErr == nil {
			removeErr = err
		}
	}
	s.migrationRequired = false
	s.mu.Unlock()
	if removeErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": removeErr.Error()})
		return
	}
	if strings.TrimSpace(previous.RoomID) != "" {
		s.notifyChanged()
	}
	if !autoUpdateEnabled(previous) {
		s.notifyUpdateChanged()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *configStore) setOnChange(callback func()) {
	s.mu.Lock()
	s.onChange = callback
	s.mu.Unlock()
}

func (s *configStore) setOnTimerChange(callback func()) {
	s.mu.Lock()
	s.onTimerChange = callback
	s.mu.Unlock()
}

func (s *configStore) setOnUpdateChange(callback func()) {
	s.mu.Lock()
	s.onUpdateChange = callback
	s.mu.Unlock()
}

func (s *configStore) notifyChanged() {
	s.mu.RLock()
	callback := s.onChange
	s.mu.RUnlock()
	if callback != nil {
		callback()
	}
}

func (s *configStore) notifyTimerChanged() {
	s.mu.RLock()
	callback := s.onTimerChange
	s.mu.RUnlock()
	if callback != nil {
		callback()
	}
}

func (s *configStore) notifyUpdateChanged() {
	s.mu.RLock()
	callback := s.onUpdateChange
	s.mu.RUnlock()
	if callback != nil {
		callback()
	}
}

func (s *configStore) readState() (appState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readStateLocked()
}

func (s *configStore) replaceState(state appState) error {
	normalizeAppState(&state)
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.readStateLocked()
	if err != nil {
		return err
	}
	return s.persistStateLocked(previous, state, false)
}

type clientStateReplaceResult struct {
	Previous    appState
	PreviousErr error
	State       appState
}

func roomSwitchRequiresRecordReset(previousRoomID, nextRoomID string) bool {
	previousRoomID = strings.TrimSpace(previousRoomID)
	nextRoomID = strings.TrimSpace(nextRoomID)
	return previousRoomID != "" && nextRoomID != "" && previousRoomID != nextRoomID
}

func clearRoomScopedRecords(state *appState) {
	state.RecentGifts = []recentGift{}
	state.Stats = map[string]dayStats{}
	state.Log = []logEntry{}
	state.Contributions = contributionLedgerState{
		Viewers:   []viewerContribution{},
		UpdatedAt: time.Now().UnixMilli(),
	}
	for panelIndex := range state.GiftKPIPanels {
		for itemIndex := range state.GiftKPIPanels[panelIndex].Items {
			state.GiftKPIPanels[panelIndex].Items[itemIndex].Received = 0
		}
	}
}

func (s *configStore) replaceClientState(state appState) (clientStateReplaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, previousErr := s.readStateLocked()
	if previousErr != nil {
		return clientStateReplaceResult{}, previousErr
	}
	if roomSwitchRequiresRecordReset(previous.RoomID, state.RoomID) {
		clearRoomScopedRecords(&state)
	} else if previous.Contributions.UpdatedAt > state.Contributions.UpdatedAt {
		state.Contributions = previous.Contributions
	}
	normalizeAppState(&state)
	if err := s.persistStateLocked(previous, state, previousErr != nil); err != nil {
		return clientStateReplaceResult{}, err
	}
	return clientStateReplaceResult{Previous: previous, PreviousErr: previousErr, State: state}, nil
}

func (s *configStore) patchClientState(fields map[string]json.RawMessage) (clientStateReplaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, previousErr := s.readStateLocked()
	if previousErr != nil {
		return clientStateReplaceResult{}, previousErr
	}
	// PATCH decoding mutates existing slice elements in place. A shallow copy
	// would therefore also mutate previous, making persistStateLocked believe
	// that arrays such as rules and timerRules had not changed.
	state, err := cloneAppState(previous)
	if err != nil {
		return clientStateReplaceResult{}, err
	}
	previousContributions := previous.Contributions
	if err := applyClientStatePatch(&state, fields); err != nil {
		return clientStateReplaceResult{}, &configInputError{err: err}
	}
	if roomSwitchRequiresRecordReset(previous.RoomID, state.RoomID) {
		clearRoomScopedRecords(&state)
	} else if _, changed := fields["contributions"]; changed && previousContributions.UpdatedAt > state.Contributions.UpdatedAt {
		state.Contributions = previousContributions
	}
	normalizeAppState(&state)
	if err := validateAppState(state); err != nil {
		return clientStateReplaceResult{}, &configInputError{err: err}
	}
	if err := s.persistStateLocked(previous, state, false); err != nil {
		return clientStateReplaceResult{}, err
	}
	return clientStateReplaceResult{Previous: previous, PreviousErr: previousErr, State: state}, nil
}

func (s *configStore) updateState(update func(*appState) error) (appState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.readStateLocked()
	if err != nil {
		return appState{}, err
	}
	state, err := cloneAppState(previous)
	if err != nil {
		return appState{}, err
	}
	if err := update(&state); err != nil {
		return appState{}, err
	}
	normalizeAppState(&state)
	if err := s.persistStateLocked(previous, state, false); err != nil {
		return appState{}, err
	}
	return state, nil
}

func writeFileAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败：%w", err)
	}
	temporary, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置失败：%w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("写入配置失败：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭配置文件失败：%w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("替换配置文件失败：%w", err)
	}
	return nil
}
