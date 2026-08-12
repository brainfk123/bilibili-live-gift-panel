package main

import (
	"bufio"
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
	if err := validatePendingStateTransaction(tx); err != nil {
		return err
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
			return err
		}
	}
	if err := os.Remove(s.stateTransactionPath()); err != nil {
		return fmt.Errorf("删除已完成状态事务失败：%w", err)
	}
	return syncStateDirectory(filepath.Dir(s.stateTransactionPath()))
}

func validatePendingStateTransaction(tx pendingStateTransaction) error {
	if tx.EventLog == nil || tx.History == nil || tx.Cache == nil || tx.Config == nil {
		return fmt.Errorf("状态事务缺少必需分片")
	}
	scanner := bufio.NewScanner(bytes.NewReader(tx.EventLog))
	scanner.Buffer(make([]byte, 16*1024), 256*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := requireJSONObject(line); err != nil {
			return fmt.Errorf("状态事务送礼记录无效：%w", err)
		}
		var entry logEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("状态事务送礼记录无效：%w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("状态事务送礼记录无效：%w", err)
	}
	shards := []struct {
		name string
		data []byte
		into any
	}{
		{name: "主配置", data: tx.Config, into: &configStateShard{}},
		{name: "礼物缓存", data: tx.Cache, into: &cacheStateShard{}},
		{name: "历史数据", data: tx.History, into: &historyStateShard{}},
	}
	for _, shard := range shards {
		if err := requireJSONObject(shard.data); err != nil {
			return fmt.Errorf("状态事务%s无效：%w", shard.name, err)
		}
		if _, err := readStateShardVersion(shard.data, shard.name); err != nil {
			return fmt.Errorf("状态事务%s无效：%w", shard.name, err)
		}
		if err := json.Unmarshal(shard.data, shard.into); err != nil {
			return fmt.Errorf("状态事务%s无效：%w", shard.name, err)
		}
	}
	return nil
}

func requireJSONObject(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fmt.Errorf("必须是非空 JSON 对象")
	}
	return nil
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
