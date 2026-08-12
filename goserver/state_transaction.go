package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func (s *configStore) persistPreparedStateLocked(state appState, ingestionID string) error {
	eventLog, err := serializeEventLog(state.Log)
	if err != nil {
		return err
	}
	history, err := serializeStateShard(historyShardFromState(state))
	if err != nil {
		return err
	}
	cache, err := serializeStateShard(cacheShardFromState(state))
	if err != nil {
		return err
	}
	config, err := serializeStateShard(configShardFromState(state))
	if err != nil {
		return err
	}
	tx := pendingStateTransaction{
		SchemaVersion: stateTransactionSchemaVersion,
		IngestionID:   ingestionID,
		EventLog:      eventLog,
		History:       history,
		Cache:         cache,
		Config:        config,
	}
	data, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态事务失败：%w", err)
	}
	if err := s.writeAtomicFile(s.stateTransactionPath(), append(data, '\n')); err != nil {
		return err
	}
	if err := s.applyPendingStateTransactionLocked(tx); err != nil {
		return err
	}
	s.migrationRequired = false
	return nil
}

func (s *configStore) applyPendingStateTransactionLocked(tx pendingStateTransaction) error {
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
			return err
		}
	}
	if err := os.Remove(s.stateTransactionPath()); err != nil {
		return fmt.Errorf("删除已完成状态事务失败：%w", err)
	}
	return syncStateDirectory(filepath.Dir(s.stateTransactionPath()))
}

func (s *configStore) recoverPendingStateTransactionLocked() error {
	data, err := os.ReadFile(s.stateTransactionPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取状态事务失败：%w", err)
	}
	var tx pendingStateTransaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return fmt.Errorf("读取状态事务失败：%w", err)
	}
	if tx.SchemaVersion != stateTransactionSchemaVersion {
		return fmt.Errorf("状态事务格式版本不受支持：%d", tx.SchemaVersion)
	}
	if tx.EventLog == nil || tx.History == nil || tx.Cache == nil || tx.Config == nil {
		return fmt.Errorf("状态事务缺少必需分片")
	}
	return s.applyPendingStateTransactionLocked(tx)
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
		key = strings.TrimSpace(key)
		if key == "" || timestamp < cutoff {
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
