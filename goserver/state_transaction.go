package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const stateTransactionSchemaVersion = 1
const maxAppliedIngressIDs = 2048
const maxRecentSourceGiftKeys = 500

type pendingStateTransaction struct {
	SchemaVersion int    `json:"schemaVersion"`
	IngestionID   string `json:"ingestionId,omitempty"`
	EventLog      []byte `json:"eventLog"`
	History       []byte `json:"history"`
	Cache         []byte `json:"cache"`
	Config        []byte `json:"config"`
}

// statePersistenceOutcome records the logical commit boundary separately
// from cleanup/application errors. Committed is true once either the complete
// recovery journal is published at its final name or every state shard has
// been durably replaced.
type statePersistenceOutcome struct {
	Committed bool
	Err       error
}

type stateTransactionApplyOutcome struct {
	ShardsCommitted bool
	Err             error
}

func (s *configStore) persistPreparedStateLocked(state appState, ingestionID string) error {
	return s.persistPreparedStateWithOutcomeLocked(state, ingestionID).Err
}

func (s *configStore) persistPreparedStateWithOutcomeLocked(state appState, ingestionID string) statePersistenceOutcome {
	eventLog, err := serializeEventLog(state.Log)
	if err != nil {
		return statePersistenceOutcome{Err: err}
	}
	history, err := serializeStateShard(historyShardFromState(state))
	if err != nil {
		return statePersistenceOutcome{Err: err}
	}
	cache, err := serializeStateShard(cacheShardFromState(state))
	if err != nil {
		return statePersistenceOutcome{Err: err}
	}
	config, err := serializeStateShard(configShardFromState(state))
	if err != nil {
		return statePersistenceOutcome{Err: err}
	}
	tx := pendingStateTransaction{
		SchemaVersion: stateTransactionSchemaVersion,
		IngestionID:   ingestionID,
		EventLog:      eventLog,
		History:       history,
		Cache:         cache,
		Config:        config,
	}
	if err := validatePendingStateTransaction(tx); err != nil {
		return statePersistenceOutcome{Err: err}
	}
	data, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return statePersistenceOutcome{Err: fmt.Errorf("序列化状态事务失败：%w", err)}
	}
	journalWrite := s.writeAtomicFileOutcome(s.stateTransactionPath(), append(data, '\n'))
	if !journalWrite.Committed {
		if journalWrite.Err == nil {
			journalWrite.Err = fmt.Errorf("发布状态事务失败：事务未提交")
		}
		return statePersistenceOutcome{Err: journalWrite.Err}
	}
	s.transactionPending = true
	applyOutcome := s.applyPendingStateTransactionWithOutcomeLocked(tx)
	if applyOutcome.Err != nil {
		return statePersistenceOutcome{
			Committed: journalWrite.Committed || applyOutcome.ShardsCommitted,
			Err:       errors.Join(journalWrite.Err, applyOutcome.Err),
		}
	}
	s.migrationRequired = false
	return statePersistenceOutcome{Committed: true}
}

func (s *configStore) applyPendingStateTransactionLocked(tx pendingStateTransaction) error {
	return s.applyPendingStateTransactionWithOutcomeLocked(tx).Err
}

func (s *configStore) applyPendingStateTransactionWithOutcomeLocked(tx pendingStateTransaction) stateTransactionApplyOutcome {
	if err := validatePendingStateTransaction(tx); err != nil {
		return stateTransactionApplyOutcome{Err: err}
	}
	writes := []struct {
		path string
		data []byte
	}{
		{path: s.eventLogPath(), data: tx.EventLog},
		{path: s.historyPath(), data: tx.History},
		{path: s.cachePath(), data: tx.Cache},
		{path: s.path, data: tx.Config},
	}
	for _, write := range writes {
		if err := s.writeAtomicFile(write.path, write.data); err != nil {
			return stateTransactionApplyOutcome{Err: err}
		}
	}
	outcome := stateTransactionApplyOutcome{ShardsCommitted: true}
	removeTransaction := os.Remove
	if s.removeStateTransaction != nil {
		removeTransaction = s.removeStateTransaction
	}
	if err := removeTransaction(s.stateTransactionPath()); err != nil {
		outcome.Err = fmt.Errorf("删除已完成状态事务失败：%w", err)
		return outcome
	}
	s.transactionPending = false
	syncDirectory := syncStateDirectory
	if s.syncStateTransactionDirectory != nil {
		syncDirectory = s.syncStateTransactionDirectory
	}
	outcome.Err = syncDirectory(filepath.Dir(s.stateTransactionPath()))
	return outcome
}

func validatePendingStateTransaction(tx pendingStateTransaction) error {
	if tx.EventLog == nil || tx.History == nil || tx.Cache == nil || tx.Config == nil {
		return fmt.Errorf("状态事务缺少必需分片")
	}
	if len(tx.EventLog) > 0 && tx.EventLog[len(tx.EventLog)-1] != '\n' {
		return fmt.Errorf("状态事务送礼记录无效：记录缺少换行符")
	}
	for start := 0; start < len(tx.EventLog); {
		relativeEnd := bytes.IndexByte(tx.EventLog[start:], '\n')
		if relativeEnd < 0 {
			return fmt.Errorf("状态事务送礼记录无效：记录缺少换行符")
		}
		end := start + relativeEnd
		line := tx.EventLog[start:end]
		if len(line) == 0 {
			return fmt.Errorf("状态事务送礼记录无效：记录行不能为空")
		}
		if len(line) > 256*1024 {
			return fmt.Errorf("状态事务送礼记录无效：记录行过长")
		}
		var entry logEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("状态事务送礼记录无效：%w", err)
		}
		canonical, err := serializeEventLog([]logEntry{entry})
		if err != nil {
			return fmt.Errorf("状态事务送礼记录无效：%w", err)
		}
		if !bytes.Equal(canonical, tx.EventLog[start:end+1]) {
			return fmt.Errorf("状态事务送礼记录无效：记录行不是标准序列化格式")
		}
		start = end + 1
	}

	var config configStateShard
	if err := validatePendingStateShard("主配置", tx.Config, &config, &config.SchemaVersion); err != nil {
		return err
	}
	var cache cacheStateShard
	if err := validatePendingStateShard("礼物缓存", tx.Cache, &cache, &cache.SchemaVersion); err != nil {
		return err
	}
	var history historyStateShard
	if err := validatePendingStateShard("历史数据", tx.History, &history, &history.SchemaVersion); err != nil {
		return err
	}
	return nil
}

// stateFromPendingStateTransaction reconstructs the exact authoritative state
// represented by a validated journal. It mirrors readCommittedStateLocked, but
// reads every shard from the single journal snapshot so a partially replayed
// on-disk shard set can never leak through after a restart.
func stateFromPendingStateTransaction(tx pendingStateTransaction) (appState, error) {
	if err := validatePendingStateTransaction(tx); err != nil {
		return appState{}, err
	}

	state := defaultAppState()
	prepareOptionalSettingsForDecode(tx.Config, &state)
	if err := json.Unmarshal(tx.Config, &state); err != nil {
		return appState{}, fmt.Errorf("读取状态事务主配置失败：%w", err)
	}

	cache := cacheShardFromState(state)
	if err := json.Unmarshal(tx.Cache, &cache); err != nil {
		return appState{}, fmt.Errorf("读取状态事务礼物缓存失败：%w", err)
	}
	state.GiftCatalog = cache.GiftCatalog
	state.RecentGifts = cache.RecentGifts

	history := historyShardFromState(state)
	if err := json.Unmarshal(tx.History, &history); err != nil {
		return appState{}, fmt.Errorf("读取状态事务历史数据失败：%w", err)
	}
	state.Stats = history.Stats
	state.Contributions = history.Contributions
	state.GiftReceipts = history.GiftReceipts
	state.AppliedIngressIDs = history.AppliedIngressIDs
	state.RecentSourceGiftKeys = history.RecentSourceGiftKeys
	applyGiftTargetProgress(state.GiftKPIPanels, history.GiftTargetProgress)

	entries := make([]logEntry, 0, maxLogEntries)
	for start := 0; start < len(tx.EventLog); {
		relativeEnd := bytes.IndexByte(tx.EventLog[start:], '\n')
		if relativeEnd < 0 {
			return appState{}, fmt.Errorf("读取状态事务送礼记录失败：记录缺少换行符")
		}
		end := start + relativeEnd
		var entry logEntry
		if err := json.Unmarshal(tx.EventLog[start:end], &entry); err != nil {
			return appState{}, fmt.Errorf("读取状态事务送礼记录失败：%w", err)
		}
		entries = append(entries, entry)
		start = end + 1
	}
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	if len(entries) > maxLogEntries {
		entries = entries[:maxLogEntries]
	}
	state.Log = entries
	normalizeAppState(&state)
	return state, nil
}

func validatePendingStateShard(name string, data []byte, into any, schemaVersion *int) error {
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("状态事务%s无效：%w", name, err)
	}
	if version := *schemaVersion; version != stateShardSchemaVersion {
		return fmt.Errorf("状态事务%s无效：格式版本为 %d，需要 %d", name, version, stateShardSchemaVersion)
	}
	canonical, err := serializeStateShard(into)
	if err != nil {
		return fmt.Errorf("状态事务%s无效：%w", name, err)
	}
	if !bytes.Equal(canonical, data) {
		return fmt.Errorf("状态事务%s无效：分片不是标准序列化格式", name)
	}
	return nil
}

func (s *configStore) recoverPendingStateTransactionLocked() error {
	read := os.ReadFile
	if s.readTransaction != nil {
		read = s.readTransaction
	}
	data, err := read(s.stateTransactionPath())
	if errors.Is(err, os.ErrNotExist) {
		s.transactionPending = false
		s.committedTransactionState = nil
		return nil
	}
	if err != nil {
		s.transactionPending = true
		return fmt.Errorf("读取状态事务失败：%w", err)
	}
	s.transactionPending = true
	var tx pendingStateTransaction
	if err := json.Unmarshal(data, &tx); err != nil {
		s.committedTransactionState = nil
		return fmt.Errorf("读取状态事务失败：%w", err)
	}
	if tx.SchemaVersion != stateTransactionSchemaVersion {
		s.committedTransactionState = nil
		return fmt.Errorf("状态事务格式版本不受支持：%d", tx.SchemaVersion)
	}
	candidate, err := stateFromPendingStateTransaction(tx)
	if err != nil {
		s.committedTransactionState = nil
		return err
	}
	s.committedTransactionState = &candidate
	if err := s.applyPendingStateTransactionLocked(tx); err != nil {
		return err
	}
	s.committedTransactionState = nil
	return nil
}

func normalizeInternalIngestionLedgers(state *appState, now time.Time) {
	seen := make(map[string]struct{}, len(state.AppliedIngressIDs))
	deduplicated := make([]string, 0, len(state.AppliedIngressIDs))
	for index := len(state.AppliedIngressIDs) - 1; index >= 0; index-- {
		id := state.AppliedIngressIDs[index]
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		deduplicated = append(deduplicated, id)
		if len(deduplicated) == maxAppliedIngressIDs {
			break
		}
	}
	for left, right := 0, len(deduplicated)-1; left < right; left, right = left+1, right-1 {
		deduplicated[left], deduplicated[right] = deduplicated[right], deduplicated[left]
	}
	state.AppliedIngressIDs = deduplicated

	cutoff := now.Add(-time.Minute).UnixMilli()
	type recentKey struct {
		key       string
		timestamp int64
	}
	recent := make([]recentKey, 0, len(state.RecentSourceGiftKeys))
	for key, timestamp := range state.RecentSourceGiftKeys {
		if key == "" || timestamp <= cutoff {
			continue
		}
		recent = append(recent, recentKey{key: key, timestamp: timestamp})
	}
	sort.Slice(recent, func(left, right int) bool {
		if recent[left].timestamp == recent[right].timestamp {
			return recent[left].key < recent[right].key
		}
		return recent[left].timestamp > recent[right].timestamp
	})
	if len(recent) > maxRecentSourceGiftKeys {
		recent = recent[:maxRecentSourceGiftKeys]
	}
	state.RecentSourceGiftKeys = make(map[string]int64, len(recent))
	for _, entry := range recent {
		state.RecentSourceGiftKeys[entry.key] = entry.timestamp
	}
}
