package mirror

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

const (
	stateFileName = "state.json"
	maxStateBytes = 16 << 10
)

// MirrorState is the last fully mirrored release identity.
type MirrorState struct {
	ETag        string    `json:"etag"`
	Tag         string    `json:"tag"`
	SHA256      string    `json:"sha256"`
	PublishedAt time.Time `json:"publishedAt"`
	CompletedAt time.Time `json:"completedAt"`
}

// StateRepository stores the last fully mirrored release identity.
type StateRepository interface {
	Load() (MirrorState, error)
	Save(MirrorState) error
}

var (
	// ErrInvalidState identifies untrusted or corrupt on-disk mirror state.
	ErrInvalidState = errors.New("invalid mirror state")
	// ErrIndeterminateStateCommit identifies a save that replaced a state file but could not verify its durability or restore the prior state.
	ErrIndeterminateStateCommit = errors.New("indeterminate mirror state commit")
)

type fileStateOptions struct {
	write         func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	beforeReplace func() error
	replace       func(string, *os.Root, string, string) error
	syncDirectory func(*os.Root) error
	openFile      func(*os.Root, string, int, os.FileMode) (*os.File, error)
}

type fileStateRepository struct {
	stateDir  string
	stateInfo os.FileInfo
	options   fileStateOptions
	gate      chan struct{}
}

// NewFileStateRepository opens a repository rooted at an existing state directory.
func NewFileStateRepository(stateDir string) (StateRepository, error) {
	return newFileStateRepositoryWithOptions(stateDir, fileStateOptions{
		write:         func(file *os.File, data []byte) (int, error) { return file.Write(data) },
		syncFile:      func(file *os.File) error { return file.Sync() },
		replace:       replaceStateFile,
		syncDirectory: syncStateDirectory,
	})
}

func newFileStateRepositoryWithOptions(stateDir string, options fileStateOptions) (*fileStateRepository, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, errors.New("mirror state directory must be an absolute clean path")
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		return nil, errors.New("mirror state directory is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("mirror state directory must be a real directory")
	}
	if options.write == nil {
		options.write = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	}
	if options.syncFile == nil {
		options.syncFile = func(file *os.File) error { return file.Sync() }
	}
	if options.replace == nil {
		options.replace = replaceStateFile
	}
	if options.syncDirectory == nil {
		options.syncDirectory = syncStateDirectory
	}
	if options.openFile == nil {
		options.openFile = openStateFile
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &fileStateRepository{stateDir: stateDir, stateInfo: info, options: options, gate: gate}, nil
}

func (repository *fileStateRepository) Load() (MirrorState, error) {
	<-repository.gate
	defer func() { repository.gate <- struct{}{} }()

	root, err := repository.openRoot()
	if err != nil {
		return MirrorState{}, invalidStateError("could not open state")
	}
	defer root.Close()
	if err := repository.validateStateRoot(root); err != nil {
		return MirrorState{}, invalidStateError("state directory changed")
	}
	state, _, _, _, err := repository.readState(root)
	if err != nil {
		return MirrorState{}, err
	}
	return state, nil
}

func (repository *fileStateRepository) Save(state MirrorState) error {
	state, err := canonicalMirrorState(state)
	if err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil || len(data) > maxStateBytes {
		return errors.New("could not encode mirror state")
	}

	<-repository.gate
	defer func() { repository.gate <- struct{}{} }()
	root, err := repository.openRoot()
	if err != nil {
		return errors.New("could not open mirror state directory")
	}
	defer root.Close()
	if err := repository.validateStateRoot(root); err != nil {
		return errors.New("mirror state directory changed")
	}
	_, priorData, priorInfo, priorExists, err := repository.readState(root)
	if err != nil {
		return err
	}
	temporary, temporaryName, err := createStateTemp(root)
	if err != nil {
		return errors.New("could not create mirror state")
	}
	defer root.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("could not secure mirror state")
	}
	if err := writeAll(temporary, data, repository.options.write); err != nil {
		_ = temporary.Close()
		return errors.New("could not write mirror state")
	}
	if err := repository.options.syncFile(temporary); err != nil {
		_ = temporary.Close()
		return errors.New("could not sync mirror state")
	}
	temporaryInfo, err := temporary.Stat()
	if err != nil || !temporaryInfo.Mode().IsRegular() {
		_ = temporary.Close()
		return errors.New("could not verify mirror state")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("could not close mirror state")
	}
	if err := repository.validateStateRoot(root); err != nil {
		return errors.New("mirror state directory changed")
	}
	if err := ensurePriorStateUnchanged(root, priorInfo, priorExists); err != nil {
		return err
	}
	if repository.options.beforeReplace != nil {
		if err := repository.options.beforeReplace(); err != nil {
			return err
		}
	}
	if err := repository.validateStateRoot(root); err != nil {
		return errors.New("mirror state directory changed")
	}
	if err := ensurePriorStateUnchanged(root, priorInfo, priorExists); err != nil {
		return err
	}
	if err := repository.options.replace(repository.stateDir, root, temporaryName, stateFileName); err != nil {
		return errors.New("could not atomically replace mirror state")
	}
	installedInfo, err := root.Lstat(stateFileName)
	if err != nil || !safeStateFile(installedInfo) || !os.SameFile(temporaryInfo, installedInfo) || repository.validateStateRoot(root) != nil {
		return indeterminateStateCommitError("state changed during replacement")
	}
	if err := repository.options.syncDirectory(root); err != nil {
		if !priorExists {
			return indeterminateStateCommitError("directory sync failed without prior state")
		}
		if restoreErr := repository.restorePriorState(root, priorData); restoreErr != nil {
			return indeterminateStateCommitError("directory sync failed and prior state could not be restored")
		}
		return errors.New("could not sync mirror state directory; prior state restored")
	}
	return nil
}

func (repository *fileStateRepository) openRoot() (*os.Root, error) {
	return os.OpenRoot(repository.stateDir)
}

func (repository *fileStateRepository) validateStateRoot(root *os.Root) error {
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(repository.stateInfo, rootInfo) {
		return errors.New("state directory identity changed")
	}
	pathInfo, err := os.Lstat(repository.stateDir)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(repository.stateInfo, pathInfo) {
		return errors.New("state directory was replaced")
	}
	return nil
}

func safeStateFile(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() && stateFilePermissionsSafe(info)
}

func (repository *fileStateRepository) readState(root *os.Root) (MirrorState, []byte, os.FileInfo, bool, error) {
	info, err := root.Lstat(stateFileName)
	if errors.Is(err, os.ErrNotExist) {
		return MirrorState{}, nil, nil, false, nil
	}
	if err != nil || !safeStateFile(info) {
		return MirrorState{}, nil, nil, false, invalidStateError("state file is unsafe")
	}
	// On Windows this open may follow an in-root reparse point. It remains
	// read-only and is not used until the root-relative Lstat identity check.
	file, err := repository.options.openFile(root, stateFileName, os.O_RDONLY, 0)
	if err != nil {
		return MirrorState{}, nil, nil, false, invalidStateError("state file cannot be opened")
	}
	defer file.Close()
	openedInfo, statErr := file.Stat()
	pathInfo, pathErr := root.Lstat(stateFileName)
	if statErr != nil || pathErr != nil || !safeStateFile(openedInfo) || !safeStateFile(pathInfo) || !os.SameFile(openedInfo, pathInfo) {
		return MirrorState{}, nil, nil, false, invalidStateError("state file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil || len(data) > maxStateBytes {
		return MirrorState{}, nil, nil, false, invalidStateError("state file is too large")
	}
	state, err := decodeMirrorState(data)
	if err != nil {
		return MirrorState{}, nil, nil, false, err
	}
	return state, data, pathInfo, true, nil
}

func ensurePriorStateUnchanged(root *os.Root, priorInfo os.FileInfo, priorExists bool) error {
	info, err := root.Lstat(stateFileName)
	if !priorExists {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("mirror state appeared before replacement")
	}
	if err != nil || !safeStateFile(info) || !os.SameFile(priorInfo, info) {
		return errors.New("mirror state changed before replacement")
	}
	return nil
}

func (repository *fileStateRepository) restorePriorState(root *os.Root, priorData []byte) error {
	if err := repository.validateStateRoot(root); err != nil {
		return err
	}
	temporary, temporaryName, err := createStateTemp(root)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := writeAll(temporary, priorData, repository.options.write); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := repository.options.syncFile(temporary); err != nil {
		_ = temporary.Close()
		return err
	}
	priorInfo, err := temporary.Stat()
	if err != nil || !priorInfo.Mode().IsRegular() {
		_ = temporary.Close()
		return errors.New("could not verify restored mirror state")
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := repository.validateStateRoot(root); err != nil {
		return err
	}
	if err := repository.options.replace(repository.stateDir, root, temporaryName, stateFileName); err != nil {
		return err
	}
	installedInfo, err := root.Lstat(stateFileName)
	if err != nil || !safeStateFile(installedInfo) || !os.SameFile(priorInfo, installedInfo) || repository.validateStateRoot(root) != nil {
		return errors.New("restored mirror state changed during replacement")
	}
	if err := repository.options.syncDirectory(root); err != nil {
		return err
	}
	_, restoredData, _, restored, err := repository.readState(root)
	if err != nil || !restored || !bytes.Equal(restoredData, priorData) {
		return errors.New("restored mirror state could not be verified")
	}
	return nil
}

func createStateTemp(root *os.Root) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var randomBytes [16]byte
		if _, err := rand.Read(randomBytes[:]); err != nil {
			return nil, "", err
		}
		name := ".state-" + hex.EncodeToString(randomBytes[:]) + ".tmp"
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not allocate mirror state name")
}

func writeAll(file *os.File, data []byte, write func(*os.File, []byte) (int, error)) error {
	for len(data) > 0 {
		written, err := write(file, data)
		if written < 0 || written > len(data) {
			return errors.New("invalid mirror state write")
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

type mirrorStatePayload struct {
	ETag        string `json:"etag"`
	Tag         string `json:"tag"`
	SHA256      string `json:"sha256"`
	PublishedAt string `json:"publishedAt"`
	CompletedAt string `json:"completedAt"`
}

func decodeMirrorState(data []byte) (MirrorState, error) {
	if err := rejectDuplicateStateFields(data); err != nil {
		return MirrorState{}, invalidStateError("state JSON is ambiguous")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload mirrorStatePayload
	if err := decoder.Decode(&payload); err != nil {
		return MirrorState{}, invalidStateError("state JSON is invalid")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return MirrorState{}, invalidStateError("state JSON has trailing data")
	}
	publishedAt, err := parseCanonicalStateTime(payload.PublishedAt)
	if err != nil {
		return MirrorState{}, invalidStateError("state publication time is invalid")
	}
	completedAt, err := parseCanonicalStateTime(payload.CompletedAt)
	if err != nil {
		return MirrorState{}, invalidStateError("state completion time is invalid")
	}
	return canonicalMirrorState(MirrorState{
		ETag:        payload.ETag,
		Tag:         payload.Tag,
		SHA256:      payload.SHA256,
		PublishedAt: publishedAt,
		CompletedAt: completedAt,
	})
}

func rejectDuplicateStateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("state JSON must be one object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return errors.New("state JSON key is invalid")
		}
		if _, allowed := canonicalStateJSONFields[key]; !allowed {
			return errors.New("state JSON key is not canonical")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("state JSON contains duplicate fields")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return errors.New("state JSON is incomplete")
	}
	return ensureJSONEOF(decoder)
}

var canonicalStateJSONFields = map[string]struct{}{
	"etag":        {},
	"tag":         {},
	"sha256":      {},
	"publishedAt": {},
	"completedAt": {},
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return err
	}
	return nil
}

func canonicalMirrorState(state MirrorState) (MirrorState, error) {
	if !isStrongETag(state.ETag) {
		return MirrorState{}, invalidStateError("state ETag is invalid")
	}
	if _, err := release.ParseStableTag(state.Tag); err != nil {
		return MirrorState{}, invalidStateError("state tag is invalid")
	}
	if len(state.SHA256) != 64 || state.SHA256 != strings.ToLower(state.SHA256) {
		return MirrorState{}, invalidStateError("state SHA-256 is invalid")
	}
	for _, character := range state.SHA256 {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return MirrorState{}, invalidStateError("state SHA-256 is invalid")
		}
	}
	if state.PublishedAt.IsZero() || state.CompletedAt.IsZero() || state.CompletedAt.Before(state.PublishedAt) {
		return MirrorState{}, invalidStateError("state timestamps are invalid")
	}
	state.PublishedAt = state.PublishedAt.UTC().Round(0)
	state.CompletedAt = state.CompletedAt.UTC().Round(0)
	return state, nil
}

func parseCanonicalStateTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() || !strings.HasSuffix(value, "Z") || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("timestamp is not canonical UTC RFC3339")
	}
	return parsed.UTC(), nil
}

func invalidStateError(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidState, message)
}

func indeterminateStateCommitError(message string) error {
	return fmt.Errorf("%w: %s", ErrIndeterminateStateCommit, message)
}
