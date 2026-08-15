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

var giftInboxRecordLimit = maxGiftInboxRecords
var giftInboxByteLimit = maxGiftInboxBytes

var (
	errGiftInboxCapacity = errors.New("gift inbox capacity reached")
	errGiftInboxClosed   = errors.New("gift inbox handle is closed")
)

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
	Revision        uint64 `json:"-"`
	PendingCount    int    `json:"pendingCount"`
	PendingBytes    int64  `json:"pendingBytes,omitempty"`
	OldestPendingAt int64  `json:"oldestPendingAt,omitempty"`
	CapacityError   bool   `json:"capacityError,omitempty"`
}

type giftInboxRecordMetadata struct {
	filename    string
	ingestionID string
	receivedAt  int64
	size        int64
}

type giftInboxSequence struct {
	NextSequence uint64 `json:"nextSequence"`
}

type giftInbox struct {
	shared       *giftInboxShared
	handleID     uint64
	pendingPath  string
	sequencePath string
	closed       bool
}

type giftInboxShared struct {
	mu                  sync.Mutex
	pendingPath         string
	sequencePath        string
	nextSequence        uint64
	health              giftInboxHealth
	pendingBytes        int64
	revision            uint64
	pending             []giftInboxRecordMetadata
	claimedBy           uint64
	claimedID           string
	rootInfo            os.FileInfo
	openHandles         int
	writeAtomically     func(string, []byte) atomicWriteOutcome
	retireResetArtifact func(string) error
	syncResetDirectory  func(string) error
}

type giftInboxDurabilityWarning struct {
	err error
}

func (warning *giftInboxDurabilityWarning) Error() string {
	return fmt.Sprintf("gift inbox record committed with durability warning: %v", warning.err)
}

func (warning *giftInboxDurabilityWarning) Unwrap() error { return warning.err }

func giftInboxRecordCommitted(err error) bool {
	var warning *giftInboxDurabilityWarning
	return errors.As(err, &warning)
}

var giftInboxRegistry = struct {
	sync.Mutex
	roots        map[string]*giftInboxShared
	nextHandleID uint64
}{roots: make(map[string]*giftInboxShared)}

func openGiftInbox(root string) (*giftInbox, error) {
	inboxRoot, err := filepath.Abs(filepath.Join(root, "gift-inbox"))
	if err != nil {
		return nil, fmt.Errorf("resolve gift inbox root path: %w", err)
	}
	inboxRoot = filepath.Clean(inboxRoot)
	pendingPath := filepath.Join(inboxRoot, "pending")
	if err := os.MkdirAll(pendingPath, 0o700); err != nil {
		return nil, fmt.Errorf("create gift inbox directory: %w", err)
	}
	rootInfo, err := os.Stat(inboxRoot)
	if err != nil {
		return nil, fmt.Errorf("stat gift inbox root: %w", err)
	}

	giftInboxRegistry.Lock()
	defer giftInboxRegistry.Unlock()
	giftInboxRegistry.nextHandleID++
	handleID := giftInboxRegistry.nextHandleID
	if shared := giftInboxRegistry.roots[inboxRoot]; shared != nil {
		if !os.SameFile(rootInfo, shared.rootInfo) {
			return nil, fmt.Errorf("gift inbox root identity changed while handles remain open")
		}
		shared.openHandles++
		return &giftInbox{shared: shared, handleID: handleID, pendingPath: shared.pendingPath, sequencePath: shared.sequencePath}, nil
	}
	for _, shared := range giftInboxRegistry.roots {
		if os.SameFile(rootInfo, shared.rootInfo) {
			giftInboxRegistry.roots[inboxRoot] = shared
			shared.openHandles++
			return &giftInbox{shared: shared, handleID: handleID, pendingPath: shared.pendingPath, sequencePath: shared.sequencePath}, nil
		}
	}
	shared := &giftInboxShared{
		pendingPath:     pendingPath,
		sequencePath:    filepath.Join(inboxRoot, "sequence.json"),
		nextSequence:    1,
		rootInfo:        rootInfo,
		openHandles:     1,
		writeAtomically: writeFileAtomicallyOutcome,
	}
	inbox := &giftInbox{shared: shared, handleID: handleID, pendingPath: shared.pendingPath, sequencePath: shared.sequencePath}
	if err := inbox.removeOrphanTempsLocked(); err != nil {
		return nil, err
	}
	if err := inbox.loadSequenceLocked(); err != nil {
		return nil, err
	}
	if _, err := inbox.reconcileHealthLocked(); err != nil {
		return nil, err
	}
	giftInboxRegistry.roots[inboxRoot] = shared
	return inbox, nil
}

func (inbox *giftInbox) Accept(roomID, command string, gift giftEvent) (giftInboxRecord, error) {
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	if err := inbox.checkOpenLocked(); err != nil {
		return giftInboxRecord{}, err
	}

	health := inbox.shared.health
	// A newly opened empty inbox can have a committed record written by an
	// interrupted/other process between startup and the first Accept. Reconcile
	// just this empty-cache boundary; normal ingress uses the maintained O(1)
	// counters and never scans a deep inbox.
	if health.PendingCount == 0 && inbox.shared.pendingBytes == 0 {
		var err error
		health, err = inbox.reconcileHealthLocked()
		if err != nil {
			return giftInboxRecord{}, err
		}
	}
	if health.PendingCount >= giftInboxRecordLimit || health.CapacityError {
		return giftInboxRecord{}, errGiftInboxCapacity
	}
	ingestionID, err := newGiftInboxIngestionID()
	if err != nil {
		return giftInboxRecord{}, err
	}
	record := giftInboxRecord{
		SchemaVersion: 1,
		LocalSequence: inbox.shared.nextSequence,
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
	if inbox.shared.pendingBytes+int64(len(data)+1) > giftInboxByteLimit {
		inbox.shared.health.CapacityError = true
		inbox.touchHealthLocked()
		return giftInboxRecord{}, errGiftInboxCapacity
	}
	sequenceWarning, err := inbox.persistNextSequenceLocked(inbox.shared.nextSequence + 1)
	if err != nil {
		return giftInboxRecord{}, err
	}
	inbox.shared.nextSequence++
	writeOutcome := inbox.shared.writeAtomically(inbox.recordPath(record.LocalSequence, record.IngestionID), append(data, '\n'))
	if writeOutcome.Err != nil && !writeOutcome.Committed {
		return giftInboxRecord{}, fmt.Errorf("persist gift inbox record: %w", writeOutcome.Err)
	}
	inbox.shared.health.PendingCount++
	inbox.shared.pendingBytes += int64(len(data) + 1)
	inbox.shared.pending = append(inbox.shared.pending, giftInboxRecordMetadata{
		filename: inbox.recordFilename(record.LocalSequence, record.IngestionID), ingestionID: record.IngestionID,
		receivedAt: record.ReceivedAt, size: int64(len(data) + 1),
	})
	if inbox.shared.health.OldestPendingAt == 0 || record.ReceivedAt < inbox.shared.health.OldestPendingAt {
		inbox.shared.health.OldestPendingAt = record.ReceivedAt
	}
	inbox.shared.health.PendingBytes = inbox.shared.pendingBytes
	inbox.shared.health.CapacityError = inbox.shared.health.PendingCount >= giftInboxRecordLimit || inbox.shared.pendingBytes >= giftInboxByteLimit
	inbox.touchHealthLocked()
	if writeOutcome.Err != nil || sequenceWarning != nil {
		return record, &giftInboxDurabilityWarning{err: errors.Join(sequenceWarning, writeOutcome.Err)}
	}
	return record, nil
}

func (inbox *giftInbox) Next() (giftInboxRecord, bool, error) {
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	if err := inbox.checkOpenLocked(); err != nil {
		return giftInboxRecord{}, false, err
	}
	if inbox.shared.claimedBy != 0 {
		return giftInboxRecord{}, false, nil
	}

	if len(inbox.shared.pending) == 0 {
		return giftInboxRecord{}, false, nil
	}
	record, err := inbox.readRecordLocked(inbox.shared.pending[0].filename)
	if err != nil {
		return giftInboxRecord{}, false, err
	}
	inbox.shared.claimedBy = inbox.handleID
	inbox.shared.claimedID = record.IngestionID
	inbox.touchHealthLocked()
	return record, true, nil
}

func (inbox *giftInbox) Acknowledge(ingestionID string) error {
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	if err := inbox.checkOpenLocked(); err != nil {
		return err
	}
	if !isValidGiftInboxID(ingestionID) {
		return fmt.Errorf("invalid gift inbox acknowledgement ID")
	}
	if inbox.shared.claimedBy != 0 && inbox.shared.claimedBy != inbox.handleID {
		return fmt.Errorf("gift inbox record is claimed by another handle")
	}
	if inbox.shared.claimedBy == inbox.handleID && inbox.shared.claimedID != ingestionID {
		return fmt.Errorf("gift inbox acknowledgement does not match current claim")
	}

	if len(inbox.shared.pending) == 0 {
		return nil
	}
	selected := inbox.shared.pending[0]
	if selected.ingestionID != ingestionID {
		for _, pending := range inbox.shared.pending[1:] {
			if pending.ingestionID == ingestionID {
				return fmt.Errorf("gift inbox acknowledgement is out of order")
			}
		}
		return nil
	}
	record, err := inbox.readRecordLocked(selected.filename)
	if err != nil {
		return err
	}
	if record.IngestionID != ingestionID {
		return fmt.Errorf("gift inbox record ingestion ID does not match filename")
	}
	if err := os.Remove(filepath.Join(inbox.pendingPath, selected.filename)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("acknowledge gift inbox record: %w", err)
	}
	inbox.shared.claimedBy = 0
	inbox.shared.claimedID = ""
	inbox.shared.health.PendingCount = maxInt(0, inbox.shared.health.PendingCount-1)
	inbox.shared.pendingBytes = maxInt64(0, inbox.shared.pendingBytes-selected.size)
	inbox.shared.pending = inbox.shared.pending[1:]
	if inbox.shared.health.PendingCount == 0 {
		inbox.shared.health.OldestPendingAt = 0
	} else {
		inbox.shared.health.OldestPendingAt = inbox.shared.pending[0].receivedAt
	}
	inbox.shared.health.PendingBytes = inbox.shared.pendingBytes
	inbox.shared.health.CapacityError = inbox.shared.health.PendingCount >= giftInboxRecordLimit || inbox.shared.pendingBytes >= giftInboxByteLimit
	inbox.touchHealthLocked()
	return nil
}

// Release relinquishes this handle's matching claim without deleting the
// record. Callers must defer Release after Next whenever processing may fail.
func (inbox *giftInbox) Release(ingestionID string) error {
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	if err := inbox.checkOpenLocked(); err != nil {
		return err
	}
	if !isValidGiftInboxID(ingestionID) {
		return fmt.Errorf("invalid gift inbox release ID")
	}
	if inbox.shared.claimedBy != inbox.handleID || inbox.shared.claimedID != ingestionID {
		return fmt.Errorf("gift inbox release does not match current claim")
	}
	inbox.shared.claimedBy = 0
	inbox.shared.claimedID = ""
	inbox.touchHealthLocked()
	return nil
}

// Close releases any claim owned by this handle and ends its ownership. Runtime
// owners should defer Close immediately after opening an inbox.
func (inbox *giftInbox) Close() error {
	giftInboxRegistry.Lock()
	defer giftInboxRegistry.Unlock()
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	if inbox.closed {
		return nil
	}
	if inbox.shared.claimedBy == inbox.handleID {
		inbox.shared.claimedBy = 0
		inbox.shared.claimedID = ""
	}
	inbox.closed = true
	inbox.shared.openHandles--
	if inbox.shared.openHandles == 0 {
		for path, shared := range giftInboxRegistry.roots {
			if shared == inbox.shared {
				delete(giftInboxRegistry.roots, path)
			}
		}
	}
	return nil
}

// Reset clears only the durable inbox artifacts owned by this inbox root. The
// runtime reset barrier guarantees there is no in-flight producer or consumer
// claim while this method holds the shared inbox lock.
func (inbox *giftInbox) Reset() error {
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	if err := inbox.checkOpenLocked(); err != nil {
		return err
	}
	if inbox.shared.claimedBy != 0 {
		return fmt.Errorf("cannot reset gift inbox while a record is claimed")
	}
	retire := inbox.shared.retireResetArtifact
	if retire == nil {
		retire = retireFileDurably
	}
	resetDirectories := []struct {
		path           string
		includeRecords bool
	}{
		{path: inbox.pendingPath, includeRecords: true},
		{path: filepath.Dir(inbox.sequencePath)},
	}
	root := filepath.Dir(inbox.sequencePath)
	for _, directory := range resetDirectories {
		if err := validateResetScanDirectory(root, directory.path); err != nil {
			return fmt.Errorf("validate gift inbox directory for reset: %w", err)
		}
	}
	for _, directory := range resetDirectories {
		entries, err := os.ReadDir(directory.path)
		if err != nil {
			return fmt.Errorf("read gift inbox directory for reset: %w", err)
		}
		for _, entry := range entries {
			if !isOwnedGiftInboxResetEntry(entry.Name(), directory.includeRecords) {
				continue
			}
			if err := retire(filepath.Join(directory.path, entry.Name())); err != nil {
				_, _ = inbox.reconcileHealthLocked()
				return fmt.Errorf("remove gift inbox artifact during reset: %w", err)
			}
		}
	}
	if err := retire(inbox.sequencePath); err != nil {
		_, _ = inbox.reconcileHealthLocked()
		return fmt.Errorf("remove gift inbox sequence during reset: %w", err)
	}
	syncDirectory := inbox.shared.syncResetDirectory
	if syncDirectory == nil {
		syncDirectory = syncStateDirectory
	}
	for _, directory := range resetDirectories {
		if err := syncDirectory(directory.path); err != nil {
			_, _ = inbox.reconcileHealthLocked()
			return fmt.Errorf("sync gift inbox directory during reset: %w", err)
		}
		_ = os.Remove(filepath.Join(directory.path, resetTombstoneName))
	}
	inbox.shared.nextSequence = 1
	inbox.shared.pendingBytes = 0
	inbox.shared.pending = nil
	inbox.shared.claimedBy = 0
	inbox.shared.claimedID = ""
	inbox.shared.health = giftInboxHealth{}
	inbox.touchHealthLocked()
	return nil
}

func (inbox *giftInbox) checkOpenLocked() error {
	if inbox.closed {
		return errGiftInboxClosed
	}
	return nil
}

func (inbox *giftInbox) Health() giftInboxHealth {
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	health, err := inbox.reconcileHealthLocked()
	if err != nil {
		return giftInboxHealth{CapacityError: true}
	}
	return health
}

// SnapshotHealth returns the last reconciled in-memory health value without
// scanning the inbox directory or parsing records. Runtime status polling uses
// this constant-time snapshot; Health remains the explicit reconciliation API.
func (inbox *giftInbox) SnapshotHealth() giftInboxHealth {
	inbox.shared.mu.Lock()
	defer inbox.shared.mu.Unlock()
	return inbox.shared.health
}

func (inbox *giftInbox) removeOrphanTempsLocked() error {
	for _, directory := range []string{filepath.Dir(inbox.sequencePath), inbox.pendingPath} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return fmt.Errorf("read gift inbox directory for temporary files: %w", err)
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !isGiftInboxTempName(entry.Name()) {
				continue
			}
			path := filepath.Join(directory, entry.Name())
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
		inbox.shared.nextSequence = sequence.NextSequence
	}
	files, err := inbox.pendingFilesLocked()
	if err != nil {
		return err
	}
	for _, file := range files {
		sequence, _ := strconv.ParseUint(file[:20], 10, 64)
		if sequence >= inbox.shared.nextSequence {
			if sequence == ^uint64(0) {
				return errors.New("gift inbox sequence overflow")
			}
			inbox.shared.nextSequence = sequence + 1
		}
	}
	return nil
}

func (inbox *giftInbox) persistNextSequenceLocked(next uint64) (warning error, err error) {
	if next == 0 {
		return nil, errors.New("gift inbox sequence overflow")
	}
	data, err := json.Marshal(giftInboxSequence{NextSequence: next})
	if err != nil {
		return nil, fmt.Errorf("serialize gift inbox sequence: %w", err)
	}
	outcome := inbox.shared.writeAtomically(inbox.sequencePath, append(data, '\n'))
	if outcome.Err != nil && !outcome.Committed {
		return nil, fmt.Errorf("persist gift inbox sequence: %w", outcome.Err)
	}
	if outcome.Err != nil {
		return fmt.Errorf("persist gift inbox sequence durability: %w", outcome.Err), nil
	}
	return nil, nil
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
	inbox.shared.health = giftInboxHealth{
		PendingCount:    len(files),
		PendingBytes:    bytes,
		OldestPendingAt: oldest,
		CapacityError:   len(files) >= giftInboxRecordLimit || bytes >= giftInboxByteLimit,
	}
	inbox.shared.pendingBytes = bytes
	inbox.shared.pending = make([]giftInboxRecordMetadata, 0, len(files))
	for _, file := range files {
		info, statErr := os.Stat(filepath.Join(inbox.pendingPath, file))
		if statErr != nil {
			return giftInboxHealth{}, fmt.Errorf("stat gift inbox record: %w", statErr)
		}
		receivedAt := info.ModTime().Unix()
		record, recordErr := inbox.readRecordLocked(file)
		if recordErr == nil {
			receivedAt = record.ReceivedAt
		}
		inbox.shared.pending = append(inbox.shared.pending, giftInboxRecordMetadata{
			filename: file, ingestionID: pendingIngestionID(file), receivedAt: receivedAt, size: info.Size(),
		})
	}
	inbox.touchHealthLocked()
	return inbox.shared.health, nil
}

func (inbox *giftInbox) touchHealthLocked() {
	if inbox.shared.revision != ^uint64(0) {
		inbox.shared.revision++
	}
	inbox.shared.health.Revision = inbox.shared.revision
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
	filenameSequence, filenameID, ok := parseGiftInboxRecordName(filename)
	if !ok {
		return giftInboxRecord{}, fmt.Errorf("invalid gift inbox record filename %s", filename)
	}
	if record.SchemaVersion != 1 {
		return giftInboxRecord{}, fmt.Errorf("unsupported gift inbox schema version %d in %s", record.SchemaVersion, filename)
	}
	if record.LocalSequence == 0 || record.LocalSequence != filenameSequence {
		return giftInboxRecord{}, fmt.Errorf("gift inbox record sequence does not match filename %s", filename)
	}
	if !isValidGiftInboxID(record.IngestionID) || record.IngestionID != filenameID {
		return giftInboxRecord{}, fmt.Errorf("gift inbox record ingestion ID does not match filename %s", filename)
	}
	return record, nil
}

func (inbox *giftInbox) recordPath(sequence uint64, ingestionID string) string {
	return filepath.Join(inbox.pendingPath, inbox.recordFilename(sequence, ingestionID))
}

func (*giftInbox) recordFilename(sequence uint64, ingestionID string) string {
	return fmt.Sprintf("%020d-%s.json", sequence, ingestionID)
}

func newGiftInboxIngestionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate gift inbox ingestion ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func isGiftInboxRecordName(name string) bool {
	_, _, ok := parseGiftInboxRecordName(name)
	return ok
}

func parseGiftInboxRecordName(name string) (uint64, string, bool) {
	if len(name) < 27 || !strings.HasSuffix(name, ".json") || name[20] != '-' {
		return 0, "", false
	}
	sequence, err := strconv.ParseUint(name[:20], 10, 64)
	if err != nil || sequence == 0 {
		return 0, "", false
	}
	id := strings.TrimSuffix(name[21:], ".json")
	if !isValidGiftInboxID(id) {
		return 0, "", false
	}
	return sequence, id, true
}

func isValidGiftInboxID(id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func isGiftInboxTempName(name string) bool {
	return strings.HasPrefix(name, "config-") && strings.HasSuffix(name, ".tmp") && len(name) > len("config-.tmp")
}

func isOwnedGiftInboxResetEntry(name string, includeRecords bool) bool {
	return isGiftInboxTempName(name) || includeRecords && isGiftInboxRecordName(name)
}

func pendingIngestionID(filename string) string {
	return strings.TrimSuffix(filename[21:], ".json")
}
