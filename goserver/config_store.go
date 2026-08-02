package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxConfigBytes = 8 << 20

type configStore struct {
	path     string
	mu       sync.RWMutex
	onChange func()
}

func newDefaultConfigStore() (*configStore, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("无法确定配置目录：%w", err)
	}
	return &configStore{path: filepath.Join(root, "BilibiliLiveGiftPanel", "config.json")}, nil
}

func (s *configStore) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGet(w)
	case http.MethodPut:
		s.handlePut(w, r)
	case http.MethodDelete:
		s.handleDelete(w)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
	}
}

func (s *configStore) handleGet(w http.ResponseWriter) {
	s.mu.RLock()
	data, err := os.ReadFile(s.path)
	s.mu.RUnlock()
	if errors.Is(err, os.ErrNotExist) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *configStore) handlePut(w http.ResponseWriter, r *http.Request) {
	previous, previousErr := s.readState()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"code": -1, "message": "配置文件过大"})
		return
	}
	state := defaultAppState()
	state.Settings.ShowTutorial = nil
	if err := json.Unmarshal(body, &state); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "配置必须是有效的 JSON 对象"})
		return
	}
	normalizeAppState(&state)
	if err := validateAppState(state); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
		return
	}
	err = s.replaceState(state)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
		return
	}
	if previousErr != nil || strings.TrimSpace(previous.RoomID) != strings.TrimSpace(state.RoomID) {
		s.notifyChanged()
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0})
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
	}
	for _, rule := range state.Rules {
		attribute := state.findAttribute(rule.AttributeName)
		if attribute == nil {
			return fmt.Errorf("公式 %q 引用了不存在的属性 %q", rule.FormulaName, rule.AttributeName)
		}
		if strings.TrimSpace(rule.Formula) == "" {
			return fmt.Errorf("公式 %q 不能为空", rule.FormulaName)
		}
		if _, err := formulaPreview(state, rule.Formula, attribute.Name, attribute.Value); err != nil {
			return fmt.Errorf("公式 %q 无效：%w", rule.FormulaName, err)
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
			return fmt.Errorf("定时器 %q 的公式不能为空", rule.FormulaName)
		}
		if strings.TrimSpace(rule.Condition) != "" {
			if _, err := timerFormulaPreview(state, rule.Condition, attribute.Name, attribute.Value); err != nil {
				return fmt.Errorf("定时器 %q 的运行条件无效：%w", rule.FormulaName, err)
			}
		}
		if _, err := timerFormulaPreview(state, rule.Formula, attribute.Name, attribute.Value); err != nil {
			return fmt.Errorf("定时器 %q 的公式无效：%w", rule.FormulaName, err)
		}
	}
	return nil
}

func (s *configStore) handleDelete(w http.ResponseWriter) {
	previous, _ := s.readState()
	s.mu.Lock()
	err := os.Remove(s.path)
	s.mu.Unlock()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
		return
	}
	if strings.TrimSpace(previous.RoomID) != "" {
		s.notifyChanged()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *configStore) setOnChange(callback func()) {
	s.mu.Lock()
	s.onChange = callback
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

func (s *configStore) readState() (appState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readStateLocked()
}

func (s *configStore) readStateLocked() (appState, error) {
	state := defaultAppState()
	state.Settings.ShowTutorial = nil
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return appState{}, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return appState{}, fmt.Errorf("读取配置失败：%w", err)
	}
	normalizeAppState(&state)
	return state, nil
}

func (s *configStore) replaceState(state appState) error {
	normalizeAppState(&state)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败：%w", err)
	}
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomically(s.path, data)
}

func (s *configStore) updateState(update func(*appState) error) (appState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readStateLocked()
	if err != nil {
		return appState{}, err
	}
	if err := update(&state); err != nil {
		return appState{}, err
	}
	normalizeAppState(&state)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return appState{}, fmt.Errorf("序列化配置失败：%w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomically(s.path, data); err != nil {
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
