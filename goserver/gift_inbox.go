package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxGiftInboxRecords = 10000
const maxGiftInboxBytes int64 = 64 << 20

var errGiftInboxCapacity = errors.New("gift inbox capacity reached")

type giftInboxRecord struct {
	SchemaVersion int       `json:"schemaVersion"`
	LocalSequence uint64    `json:"localSequence"`
	IngestionID   string    `json:"ingestionId"`
	RoomID        string    `json:"roomId"`
	Command       string    `json:"command"`
	ReceivedAt    int64     `json:"receivedAt"`
	Gift          giftEvent `json:"gift"`
}

type giftInboxHealth struct {
	PendingCount    int   `json:"pendingCount"`
	OldestPendingAt int64 `json:"oldestPendingAt,omitempty"`
	CapacityError   bool  `json:"capacityError,omitempty"`
}

type giftInboxSequence struct {
	NextSequence uint64 `json:"nextSequence"`
}

type giftInbox struct {
	mu           sync.Mutex
	pendingPath  string
	sequencePath string
	nextSequence uint64
	health       giftInboxHealth
}

func openGiftInbox(root string) (*giftInbox, error) {
	inbox := &giftInbox{
		pendingPath:  filepath.Join(root, "gift-inbox", "pending"),
		sequencePath: filepath.Join(root, "gift-inbox", "sequence.json"),
		nextSequence: 1,
	}
	if err := os.MkdirAll(inbox.pendingPath, 0o700); err != nil {
		return nil, fmt.Errorf("create gift inbox directory: %w", err)
	}
	if err := inbox.removeOrphanTempsLocked(); err != nil {
		return nil, err
	}
	if err := inbox.loadSequenceLocked(); err != nil {
		return nil, err
	}
	if _, err := inbox.reconcileHealthLocked(); err != nil {
		return nil, err
	}
	return inbox, nil
}

func (inbox *giftInbox) Accept(roomID, command string, gift giftEvent) (giftInboxRecord, error) {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()

	health, err := inbox.reconcileHealthLocked()
	if err != nil {
		return giftInboxRecord{}, err
	}
	if health.PendingCount >= maxGiftInboxRecords || health.CapacityError {
		return giftInboxRecord{}, errGiftInboxCapacity
	}
	ingestionID, err := newGiftInboxIngestionID()
	if err != nil {
		return giftInboxRecord{}, err
	}
	record := giftInboxRecord{
		SchemaVersion: 1,
		LocalSequence: inbox.nextSequence,
		IngestionID:   ingestionID,
		RoomID:        roomID,
		Command:       command,
		ReceivedAt:    time.Now().Unix(),
		Gift:          gift,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return giftInboxRecord{}, fmt.Errorf("serialize gift inbox record: %w", err)
	}
	if healthBytes, err := inbox.pendingBytesLocked(); err != nil {
		return giftInboxRecord{}, err
	} else if healthBytes+int64(len(data)) > maxGiftInboxBytes {
		inbox.health.CapacityError = true
		return giftInboxRecord{}, errGiftInboxCapacity
	}
	if err := inbox.persistNextSequenceLocked(inbox.nextSequence + 1); err != nil {
		return giftInboxRecord{}, err
	}
	inbox.nextSequence++
	if err := writeFileAtomically(inbox.recordPath(record.LocalSequence, record.IngestionID), append(data, '\n')); err != nil {
		return giftInboxRecord{}, fmt.Errorf("persist gift inbox record: %w", err)
	}
	inbox.health.PendingCount++
	if inbox.health.OldestPendingAt == 0 {
		inbox.health.OldestPendingAt = record.ReceivedAt
	}
	inbox.health.CapacityError = false
	return record, nil
}

func (inbox *giftInbox) Next() (giftInboxRecord, bool, error) {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()

	files, err := inbox.pendingFilesLocked()
	if err != nil {
		return giftInboxRecord{}, false, err
	}
	if len(files) == 0 {
		return giftInboxRecord{}, false, nil
	}
	record, err := inbox.readRecordLocked(files[0])
	if err != nil {
		return giftInboxRecord{}, false, err
	}
	return record, true, nil
}

func (inbox *giftInbox) Acknowledge(ingestionID string) error {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()

	files, err := inbox.pendingFilesLocked()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	selected := files[0]
	if pendingIngestionID(selected) != ingestionID {
		for _, file := range files[1:] {
			if pendingIngestionID(file) == ingestionID {
				return fmt.Errorf("gift inbox acknowledgement is out of order")
			}
		}
		return nil
	}
	record, err := inbox.readRecordLocked(selected)
	if err != nil {
		return err
	}
	if record.IngestionID != ingestionID {
		return fmt.Errorf("gift inbox record ingestion ID does not match filename")
	}
	if err := os.Remove(filepath.Join(inbox.pendingPath, selected)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("acknowledge gift inbox record: %w", err)
	}
	_, err = inbox.reconcileHealthLocked()
	return err
}

func (inbox *giftInbox) Health() giftInboxHealth {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	health, err := inbox.reconcileHealthLocked()
	if err != nil {
		return giftInboxHealth{CapacityError: true}
	}
	return health
}

func (inbox *giftInbox) removeOrphanTempsLocked() error {
	for _, directory := range []string{filepath.Dir(inbox.sequencePath), inbox.pendingPath} {
		paths, err := filepath.Glob(filepath.Join(directory, "config-*.tmp"))
		if err != nil {
			return fmt.Errorf("find gift inbox temporary files: %w", err)
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove gift inbox temporary file: %w", err)
			}
		}
	}
	return nil
}

func (inbox *giftInbox) loadSequenceLocked() error {
	data, err := os.ReadFile(inbox.sequencePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read gift inbox sequence: %w", err)
	}
	if err == nil {
		var sequence giftInboxSequence
		if err := json.Unmarshal(data, &sequence); err != nil || sequence.NextSequence == 0 {
			if err == nil {
				err = errors.New("next sequence is zero")
			}
			return fmt.Errorf("parse gift inbox sequence: %w", err)
		}
		inbox.nextSequence = sequence.NextSequence
	}
	files, err := inbox.pendingFilesLocked()
	if err != nil {
		return err
	}
	for _, file := range files {
		sequence, _ := strconv.ParseUint(file[:20], 10, 64)
		if sequence >= inbox.nextSequence {
			if sequence == ^uint64(0) {
				return errors.New("gift inbox sequence overflow")
			}
			inbox.nextSequence = sequence + 1
		}
	}
	return nil
}

func (inbox *giftInbox) persistNextSequenceLocked(next uint64) error {
	if next == 0 {
		return errors.New("gift inbox sequence overflow")
	}
	data, err := json.Marshal(giftInboxSequence{NextSequence: next})
	if err != nil {
		return fmt.Errorf("serialize gift inbox sequence: %w", err)
	}
	if err := writeFileAtomically(inbox.sequencePath, append(data, '\n')); err != nil {
		return fmt.Errorf("persist gift inbox sequence: %w", err)
	}
	return nil
}

func (inbox *giftInbox) reconcileHealthLocked() (giftInboxHealth, error) {
	files, err := inbox.pendingFilesLocked()
	if err != nil {
		return giftInboxHealth{}, err
	}
	var bytes int64
	var oldest int64
	for _, file := range files {
		info, err := os.Stat(filepath.Join(inbox.pendingPath, file))
		if err != nil {
			return giftInboxHealth{}, fmt.Errorf("stat gift inbox record: %w", err)
		}
		bytes += info.Size()
		record, err := inbox.readRecordLocked(file)
		if err != nil {
			if oldest == 0 || info.ModTime().Unix() < oldest {
				oldest = info.ModTime().Unix()
			}
			continue
		}
		if oldest == 0 || record.ReceivedAt < oldest {
			oldest = record.ReceivedAt
		}
	}
	inbox.health = giftInboxHealth{
		PendingCount:    len(files),
		OldestPendingAt: oldest,
		CapacityError:   len(files) >= maxGiftInboxRecords || bytes >= maxGiftInboxBytes,
	}
	return inbox.health, nil
}

func (inbox *giftInbox) pendingBytesLocked() (int64, error) {
	files, err := inbox.pendingFilesLocked()
	if err != nil {
		return 0, err
	}
	var bytes int64
	for _, file := range files {
		info, err := os.Stat(filepath.Join(inbox.pendingPath, file))
		if err != nil {
			return 0, fmt.Errorf("stat gift inbox record: %w", err)
		}
		bytes += info.Size()
	}
	return bytes, nil
}

func (inbox *giftInbox) pendingFilesLocked() ([]string, error) {
	entries, err := os.ReadDir(inbox.pendingPath)
	if err != nil {
		return nil, fmt.Errorf("read gift inbox directory: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isGiftInboxRecordName(entry.Name()) {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	return files, nil
}

func (inbox *giftInbox) readRecordLocked(filename string) (giftInboxRecord, error) {
	data, err := os.ReadFile(filepath.Join(inbox.pendingPath, filename))
	if err != nil {
		return giftInboxRecord{}, fmt.Errorf("read gift inbox record: %w", err)
	}
	var record giftInboxRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return giftInboxRecord{}, fmt.Errorf("parse gift inbox record %s: %w", filename, err)
	}
	return record, nil
}

func (inbox *giftInbox) recordPath(sequence uint64, ingestionID string) string {
	return filepath.Join(inbox.pendingPath, fmt.Sprintf("%020d-%s.json", sequence, ingestionID))
}

func newGiftInboxIngestionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate gift inbox ingestion ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func isGiftInboxRecordName(name string) bool {
	if len(name) < 27 || !strings.HasSuffix(name, ".json") || name[20] != '-' {
		return false
	}
	if _, err := strconv.ParseUint(name[:20], 10, 64); err != nil {
		return false
	}
	id := strings.TrimSuffix(name[21:], ".json")
	if len(id) == 0 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func pendingIngestionID(filename string) string {
	return strings.TrimSuffix(filename[21:], ".json")
}
