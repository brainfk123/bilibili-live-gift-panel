package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
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

type attributeEditLeaseLostError struct{}

func (*attributeEditLeaseLostError) Error() string { return "编辑租约已失效" }

type attributeEditSessionRequest struct {
	AttributeID string `json:"attributeId,omitempty"`
	LegacyName  string `json:"legacyName,omitempty"`
}

type attributeEditSession struct {
	AttributeID string    `json:"attributeId"`
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expiresAt"`
	State       appState  `json:"state"`
}

type attributeEditService struct {
	store             *configStore
	leases            *attributeEditLeaseCoordinator
	newID             func() (string, error)
	beforeLeaseCreate func()
}

func newAttributeEditService(store *configStore, leases *attributeEditLeaseCoordinator, newID func() (string, error)) *attributeEditService {
	return &attributeEditService{store: store, leases: leases, newID: newID}
}

func newAttributeEditID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		return "", err
	}
	return "attribute-" + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (service *attributeEditService) Prepare(request attributeEditSessionRequest) (attributeEditSession, error) {
	if service == nil || service.store == nil || service.leases == nil {
		return attributeEditSession{}, fmt.Errorf("属性编辑服务未初始化")
	}
	service.store.mu.Lock()
	defer service.store.mu.Unlock()
	state, attributeID, err := service.store.ensureAttributeIDLocked(request.AttributeID, request.LegacyName, service.newID)
	if err != nil {
		return attributeEditSession{}, err
	}
	if service.beforeLeaseCreate != nil {
		service.beforeLeaseCreate()
	}
	token, expiresAt, err := service.leases.Create(attributeID)
	if err != nil {
		return attributeEditSession{}, err
	}
	return attributeEditSession{AttributeID: attributeID, Token: token, ExpiresAt: expiresAt, State: state}, nil
}

func (service *attributeEditService) Submit(command attributeEditCommand) (attributeEditResult, error) {
	if service == nil || service.store == nil || service.leases == nil {
		return attributeEditResult{}, fmt.Errorf("属性编辑服务未初始化")
	}
	var (
		result attributeEditResult
		err    error
	)
	if strings.TrimSpace(command.Target.Kind) == "existing" {
		claim, ok := service.leases.Begin(command.Target.AttributeID, command.Target.LeaseToken)
		if !ok {
			return attributeEditResult{}, &attributeEditLeaseLostError{}
		}
		defer claim.Finish()
		result, err = service.store.applyAttributeEditAuthorized(command, service.newID, claim.Live)
	} else {
		result, err = service.store.applyAttributeEdit(command, service.newID)
	}
	if err != nil {
		return attributeEditResult{}, err
	}
	// applyAttributeEditLocked releases the store mutex before this callback.
	service.store.notifyStateChanges(result.Previous, result.PreviousErr, result.State)
	return result, nil
}

func newAttributeEditHandler(service *attributeEditService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/api/attribute-edits/session" && r.URL.Path != "/api/attribute-edits" {
			attributeEditHTTPError(w, http.StatusNotFound, "not_found", "请求地址不存在")
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			attributeEditHTTPError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持的请求方法")
			return
		}
		if !isSameOriginAttributeEditRequest(r) {
			attributeEditHTTPError(w, http.StatusForbidden, "forbidden", "拒绝跨站请求")
			return
		}
		if r.URL.Path == "/api/attribute-edits/session" {
			var request attributeEditSessionRequest
			if status, message := decodeAttributeEditHTTPBody(w, r, &request); status != 0 {
				attributeEditHTTPError(w, status, "invalid_request", message)
				return
			}
			if !validAttributeEditSessionRequest(request) {
				attributeEditHTTPError(w, http.StatusBadRequest, "invalid_request", "属性选择无效")
				return
			}
			session, err := service.Prepare(request)
			if err != nil {
				attributeEditHTTPWriteServiceError(w, err)
				return
			}
			attributeEditHTTPJSON(w, http.StatusOK, struct {
				Code int `json:"code"`
				attributeEditSession
			}{Code: 0, attributeEditSession: session})
			return
		}

		var command attributeEditCommand
		if status, message := decodeAttributeEditHTTPBody(w, r, &command); status != 0 {
			attributeEditHTTPError(w, status, "invalid_request", message)
			return
		}
		if !validAttributeEditCommandTarget(command.Target) {
			attributeEditHTTPError(w, http.StatusBadRequest, "invalid_request", "属性编辑目标无效")
			return
		}
		result, err := service.Submit(command)
		if err != nil {
			attributeEditHTTPWriteServiceError(w, err)
			return
		}
		attributeEditHTTPJSON(w, http.StatusOK, map[string]any{
			"code":   0,
			"target": map[string]any{"id": result.ID, "name": result.Name, "created": result.Created},
			"state":  result.State,
		})
	})
}

func isSameOriginAttributeEditRequest(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, scheme) && strings.EqualFold(parsed.Host, r.Host)
}

func validAttributeEditSessionRequest(request attributeEditSessionRequest) bool {
	attributeID := strings.TrimSpace(request.AttributeID)
	legacyName := strings.TrimSpace(request.LegacyName)
	if (attributeID == "") == (legacyName == "") {
		return false
	}
	if attributeID != "" {
		return validAttributeEditLeaseAttributeID(attributeID)
	}
	return utf8.ValidString(legacyName) && len(legacyName) <= 160
}

func validAttributeEditCommandTarget(target attributeEditTarget) bool {
	switch strings.TrimSpace(target.Kind) {
	case "existing":
		if !validAttributeEditLeaseAttributeID(strings.TrimSpace(target.AttributeID)) {
			return false
		}
		return target.LeaseToken == "" || validAttributeEditLeaseToken(target.LeaseToken)
	case "new":
		return strings.TrimSpace(target.AttributeID) == "" && strings.TrimSpace(target.LeaseToken) == ""
	default:
		return false
	}
}

func decodeAttributeEditHTTPBody(w http.ResponseWriter, r *http.Request, target any) (int, string) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return http.StatusBadRequest, "请求必须使用 JSON 格式"
	}
	reader := http.MaxBytesReader(w, r.Body, maxConfigBytes)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, "请求内容过大"
		}
		return http.StatusBadRequest, "请求格式不正确"
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, "请求内容过大"
		}
		return http.StatusBadRequest, "请求格式不正确"
	}
	return 0, ""
}

func attributeEditHTTPWriteServiceError(w http.ResponseWriter, err error) {
	var input *attributeEditInputError
	var notFound *attributeEditNotFoundError
	var conflict *attributeEditConflictError
	var leaseLost *attributeEditLeaseLostError
	var blocked *stateMutationsBlockedError
	switch {
	case errors.As(err, &input):
		attributeEditHTTPError(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
	case errors.As(err, &notFound):
		attributeEditHTTPError(w, http.StatusNotFound, "not_found", "属性不存在")
	case errors.As(err, &leaseLost):
		attributeEditHTTPError(w, http.StatusConflict, "lease_lost", "编辑租约已失效")
	case errors.As(err, &conflict):
		attributeEditHTTPError(w, http.StatusConflict, "name_conflict", "属性名称或引用发生冲突")
	case errors.As(err, &blocked):
		attributeEditHTTPError(w, http.StatusConflict, "mutations_blocked", "本地状态暂不可修改")
	default:
		attributeEditHTTPError(w, http.StatusInternalServerError, "internal_error", "服务器暂时无法处理请求")
	}
}

func attributeEditHTTPError(w http.ResponseWriter, status int, code, message string) {
	attributeEditHTTPJSON(w, status, map[string]any{"code": code, "message": message})
}

func attributeEditHTTPJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *configStore) applyAttributeEdit(command attributeEditCommand, newID func() (string, error)) (attributeEditResult, error) {
	return s.applyAttributeEditAuthorized(command, newID, nil)
}

func (s *configStore) applyAttributeEditAuthorized(command attributeEditCommand, newID func() (string, error), isLive func() bool) (attributeEditResult, error) {
	if s.beforeAttributeEditStoreLock != nil {
		s.beforeAttributeEditStoreLock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.afterAttributeEditStoreLock != nil {
		s.afterAttributeEditStoreLock()
	}
	return s.applyAttributeEditLockedAuthorized(command, newID, isLive)
}

// applyAttributeEditLocked is called while s.mu is held. Keeping this method
// separate lets attributeEditService hold lease ownership over the exact
// check-to-persist interval without a lock-free TOCTOU window.
func (s *configStore) applyAttributeEditLocked(command attributeEditCommand, newID func() (string, error)) (attributeEditResult, error) {
	return s.applyAttributeEditLockedAuthorized(command, newID, nil)
}

func (s *configStore) applyAttributeEditLockedAuthorized(command attributeEditCommand, newID func() (string, error), isLive func() bool) (attributeEditResult, error) {
	if isLive != nil && !isLive() {
		return attributeEditResult{}, &attributeEditLeaseLostError{}
	}
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
	if isLive != nil && !isLive() {
		return attributeEditResult{}, &attributeEditLeaseLostError{}
	}
	if err := s.persistStateLocked(previous, state, false); err != nil {
		return attributeEditResult{}, err
	}
	return attributeEditResult{State: state, ID: id, Name: command.Attribute.Name, Created: created, Previous: previous}, nil
}

// ensureAttributeID resolves exactly one existing attribute and, for legacy
// name-only records, durably assigns its ID before a lease is created.
func (s *configStore) ensureAttributeID(attributeID, legacyName string, newID func() (string, error)) (appState, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureAttributeIDLocked(attributeID, legacyName, newID)
}

func (s *configStore) ensureAttributeIDLocked(attributeID, legacyName string, newID func() (string, error)) (appState, string, error) {
	attributeID = strings.TrimSpace(attributeID)
	legacyName = strings.TrimSpace(legacyName)
	if (attributeID == "") == (legacyName == "") {
		return appState{}, "", attributeEditInput(fmt.Errorf("必须且只能指定属性 ID 或旧属性名"))
	}
	if err := s.ensureMutationsAllowedLocked(); err != nil {
		return appState{}, "", err
	}
	previous, err := s.readStateLocked()
	if err != nil {
		return appState{}, "", err
	}
	if err := s.ensureMutationsAllowedLocked(); err != nil {
		return appState{}, "", err
	}
	index := -1
	for candidateIndex, attribute := range previous.Attributes {
		matched := attribute.ID == attributeID
		if legacyName != "" {
			matched = strings.TrimSpace(attribute.Name) == legacyName
		}
		if !matched {
			continue
		}
		if index >= 0 {
			return appState{}, "", attributeEditNotFound(fmt.Errorf("属性选择不唯一"))
		}
		index = candidateIndex
	}
	if index < 0 {
		return appState{}, "", attributeEditNotFound(fmt.Errorf("属性不存在"))
	}
	if existing := strings.TrimSpace(previous.Attributes[index].ID); existing != "" {
		return previous, existing, nil
	}
	if newID == nil {
		return appState{}, "", fmt.Errorf("生成属性 ID 的函数不能为空")
	}
	generatedID, err := newID()
	if err != nil {
		return appState{}, "", err
	}
	generatedID = strings.TrimSpace(generatedID)
	if generatedID == "" {
		return appState{}, "", fmt.Errorf("生成的属性 ID 不能为空")
	}
	if err := validateUniqueAttributeIDs(previous.Attributes); err != nil {
		return appState{}, "", err
	}
	for _, attribute := range previous.Attributes {
		if attribute.ID == generatedID {
			return appState{}, "", attributeEditConflict(fmt.Errorf("属性 ID 不能重复：%s", generatedID))
		}
	}
	state, err := cloneAppState(previous)
	if err != nil {
		return appState{}, "", err
	}
	state.Attributes[index].ID = generatedID
	normalizeAppState(&state)
	if err := s.persistStateLocked(previous, state, false); err != nil {
		return appState{}, "", err
	}
	return state, generatedID, nil
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
