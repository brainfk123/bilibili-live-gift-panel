package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// Every persisted shard has its own version. Missing versions are treated as
// legacy version 0, while newer versions are rejected so an older executable
// cannot silently discard fields it does not understand.
const stateShardSchemaVersion = 5

type unsupportedStateVersionError struct {
	Shard   string
	Version int
}

func (e *unsupportedStateVersionError) Error() string {
	return fmt.Sprintf("%s 由更新版本的程序创建（格式版本 %d），请先更新程序", e.Shard, e.Version)
}

type configStateShard struct {
	SchemaVersion   int                            `json:"schemaVersion"`
	RoomID          string                         `json:"roomId"`
	Attributes      []attributeState               `json:"attributes"`
	DisplayScenes   []displaySceneState            `json:"displayScenes"`
	BlindBoxDisplay blindBoxDisplayAppearanceState `json:"blindBoxDisplay"`
	GiftKPIPanels   []giftKPIPanelState            `json:"giftKpiPanels"`
	Activities      []activitySessionState         `json:"activities"`
	Rules           []giftRule                     `json:"rules"`
	TimerRules      []timerRule                    `json:"timerRules"`
	FormulaPresets  []formulaPreset                `json:"formulaPresets"`
	Settings        settingsState                  `json:"settings"`
}

type cacheStateShard struct {
	SchemaVersion int          `json:"schemaVersion"`
	GiftCatalog   []giftInfo   `json:"giftCatalog"`
	RecentGifts   []recentGift `json:"recentGifts"`
}

type historyStateShard struct {
	// This version also governs the event schema used by events.log.
	SchemaVersion int                     `json:"schemaVersion"`
	Stats         map[string]dayStats     `json:"stats"`
	Contributions contributionLedgerState `json:"contributions"`
}

func configShardFromState(state appState) configStateShard {
	return configStateShard{
		SchemaVersion:   stateShardSchemaVersion,
		RoomID:          state.RoomID,
		Attributes:      state.Attributes,
		DisplayScenes:   state.DisplayScenes,
		BlindBoxDisplay: state.BlindBoxDisplay,
		GiftKPIPanels:   state.GiftKPIPanels,
		Activities:      state.Activities,
		Rules:           state.Rules,
		TimerRules:      state.TimerRules,
		FormulaPresets:  state.FormulaPresets,
		Settings:        state.Settings,
	}
}

func cacheShardFromState(state appState) cacheStateShard {
	return cacheStateShard{
		SchemaVersion: stateShardSchemaVersion,
		GiftCatalog:   state.GiftCatalog,
		RecentGifts:   state.RecentGifts,
	}
}

func historyShardFromState(state appState) historyStateShard {
	return historyStateShard{
		SchemaVersion: stateShardSchemaVersion,
		Stats:         state.Stats,
		Contributions: state.Contributions,
	}
}

func (s *configStore) cachePath() string {
	return filepath.Join(filepath.Dir(s.path), "cache.json")
}

func (s *configStore) historyPath() string {
	return filepath.Join(filepath.Dir(s.path), "history.json")
}

func (s *configStore) eventLogPath() string {
	return filepath.Join(filepath.Dir(s.path), "events.log")
}

func (s *configStore) statePaths() []string {
	return []string{s.path, s.cachePath(), s.historyPath(), s.eventLogPath()}
}

func (s *configStore) hasStoredStateLocked() bool {
	for _, path := range s.statePaths() {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func (s *configStore) readStateLocked() (appState, error) {
	state := defaultAppState()
	configData, configExists, err := readOptionalStateFile(s.path)
	if err != nil {
		return appState{}, err
	}
	if configExists {
		version, err := readStateShardVersion(configData, "主配置")
		if err != nil {
			return appState{}, err
		}
		if version < stateShardSchemaVersion {
			s.migrationRequired = true
		}
		prepareOptionalSettingsForDecode(configData, &state)
		if err := json.Unmarshal(configData, &state); err != nil {
			return appState{}, fmt.Errorf("读取配置失败：%w", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(configData, &fields); err == nil {
			for _, key := range []string{"giftCatalog", "recentGifts", "stats", "log", "contributions"} {
				if _, exists := fields[key]; exists {
					s.migrationRequired = true
					break
				}
			}
		}
	}

	cacheData, cacheExists, err := readOptionalStateFile(s.cachePath())
	if err != nil {
		return appState{}, err
	}
	if cacheExists {
		version, err := readStateShardVersion(cacheData, "礼物缓存")
		if err != nil {
			return appState{}, err
		}
		if version < stateShardSchemaVersion {
			s.migrationRequired = true
		}
		cache := cacheShardFromState(state)
		if err := json.Unmarshal(cacheData, &cache); err != nil {
			return appState{}, fmt.Errorf("读取礼物缓存失败：%w", err)
		}
		state.GiftCatalog = cache.GiftCatalog
		state.RecentGifts = cache.RecentGifts
	}

	historyData, historyExists, err := readOptionalStateFile(s.historyPath())
	if err != nil {
		return appState{}, err
	}
	if historyExists {
		version, err := readStateShardVersion(historyData, "历史数据")
		if err != nil {
			return appState{}, err
		}
		if version < stateShardSchemaVersion {
			s.migrationRequired = true
		}
		history := historyShardFromState(state)
		if err := json.Unmarshal(historyData, &history); err != nil {
			return appState{}, fmt.Errorf("读取历史数据失败：%w", err)
		}
		state.Stats = history.Stats
		state.Contributions = history.Contributions
	}

	eventLog, eventLogExists, err := readEventLog(s.eventLogPath())
	if err != nil {
		return appState{}, err
	}
	if eventLogExists {
		state.Log = eventLog
	}

	normalizeAppState(&state)
	return state, nil
}

func prepareOptionalSettingsForDecode(data []byte, state *appState) {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return
	}
	var settings map[string]json.RawMessage
	if raw, exists := root["settings"]; exists {
		_ = json.Unmarshal(raw, &settings)
	}
	if _, exists := settings["showTutorial"]; !exists {
		state.Settings.ShowTutorial = nil
	}
	if _, exists := settings["tutorialReplayMode"]; !exists {
		state.Settings.TutorialReplayMode = nil
	}
	if _, exists := settings["autoUpdate"]; !exists {
		state.Settings.AutoUpdate = nil
	}
}

func cloneAppState(state appState) (appState, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return appState{}, fmt.Errorf("复制配置失败：%w", err)
	}
	clone := defaultAppState()
	if err := json.Unmarshal(data, &clone); err != nil {
		return appState{}, fmt.Errorf("复制配置失败：%w", err)
	}
	normalizeAppState(&clone)
	return clone, nil
}

func readStateShardVersion(data []byte, shard string) (int, error) {
	var metadata struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return 0, fmt.Errorf("读取%s版本失败：%w", shard, err)
	}
	if metadata.SchemaVersion == nil {
		return 0, nil
	}
	version := *metadata.SchemaVersion
	if version < 0 {
		return 0, fmt.Errorf("%s的格式版本无效", shard)
	}
	if version > stateShardSchemaVersion {
		return 0, &unsupportedStateVersionError{Shard: shard, Version: version}
	}
	return version, nil
}

func readOptionalStateFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// events.log is JSON Lines rather than one JSON document. Each line is an
// independently readable event, keeping the file append/debug friendly.
func readEventLog(path string) ([]logEntry, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	entries := make([]logEntry, 0, maxLogEntries)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 16*1024), 256*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var entry logEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, false, fmt.Errorf("读取送礼记录失败：%w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("读取送礼记录失败：%w", err)
	}
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	if len(entries) > maxLogEntries {
		entries = entries[:maxLogEntries]
	}
	return entries, true, nil
}

func (s *configStore) persistStateLocked(previous appState, state appState, force bool) error {
	configPath := s.path
	cachePath := s.cachePath()
	historyPath := s.historyPath()
	eventLogPath := s.eventLogPath()
	configBefore, configAfter := configShardFromState(previous), configShardFromState(state)
	cacheBefore, cacheAfter := cacheShardFromState(previous), cacheShardFromState(state)
	historyBefore, historyAfter := historyShardFromState(previous), historyShardFromState(state)
	writeAll := force || s.migrationRequired

	configMissing := stateFileMissing(configPath)
	cacheMissing := stateFileMissing(cachePath)
	historyMissing := stateFileMissing(historyPath)
	eventLogMissing := stateFileMissing(eventLogPath)
	if writeAll || eventLogMissing || !reflect.DeepEqual(previous.Log, state.Log) {
		if err := writeEventLog(eventLogPath, state.Log); err != nil {
			return err
		}
	}
	if writeAll || historyMissing || !reflect.DeepEqual(historyBefore, historyAfter) {
		if err := writeStateShard(historyPath, historyAfter); err != nil {
			return err
		}
	}
	if writeAll || cacheMissing || !reflect.DeepEqual(cacheBefore, cacheAfter) {
		if err := writeStateShard(cachePath, cacheAfter); err != nil {
			return err
		}
	}
	if writeAll || configMissing || !reflect.DeepEqual(configBefore, configAfter) {
		if err := writeStateShard(configPath, configAfter); err != nil {
			return err
		}
	}
	s.migrationRequired = false
	return nil
}

func writeEventLog(path string, entries []logEntry) error {
	data := make([]byte, 0, len(entries)*256)
	for index := len(entries) - 1; index >= 0; index-- {
		line, err := json.Marshal(entries[index])
		if err != nil {
			return fmt.Errorf("序列化送礼记录失败：%w", err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	return writeFileAtomically(path, data)
}

func stateFileMissing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func writeStateShard(path string, shard any) error {
	data, err := json.MarshalIndent(shard, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败：%w", err)
	}
	return writeFileAtomically(path, append(data, '\n'))
}

func (s *configStore) migrateLegacy() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasStoredStateLocked() {
		return nil
	}
	state, err := s.readStateLocked()
	if err != nil {
		return err
	}
	force := s.migrationRequired
	needsWrite := force
	for _, path := range s.statePaths() {
		if stateFileMissing(path) {
			needsWrite = true
			break
		}
	}
	if !needsWrite {
		return nil
	}
	return s.persistStateLocked(state, state, force)
}
