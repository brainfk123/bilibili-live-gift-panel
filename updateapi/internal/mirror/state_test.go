package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFileStateRepositoryLoadsMissingStateAsEmptyCache(t *testing.T) {
	// Mutation caught: treating a missing state file as corrupt blocks the first mirror run.
	repository := mustNewFileStateRepository(t, t.TempDir())

	got, err := repository.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != (MirrorState{}) {
		t.Fatalf("Load() = %#v, want empty cache", got)
	}
}

func TestFileStateRepositoryRoundTripsValidatedCanonicalState(t *testing.T) {
	// Mutation caught: omitting fields or serializing a non-canonical state loses the last verified release.
	repository := mustNewFileStateRepository(t, t.TempDir())
	want := validMirrorState()

	if err := repository.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := repository.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestFileStateRepositoryRejectsCorruptJSON(t *testing.T) {
	// Mutation caught: accepting ambiguous or schema-drifting JSON can advance the conditional GitHub cache from untrusted data.
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: validStateJSON(`,"extra":true`)},
		{name: "case-variant canonical field", body: strings.Replace(validStateJSON(""), `"etag":"\"current\""`, `"ETag":"\"current\""`, 1)},
		{name: "duplicate field", body: `{"etag":"\"old\"","etag":"\"new\"","tag":"v0.4.4","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-08-16T12:00:00Z","completedAt":"2026-08-16T12:05:00Z"}`},
		{name: "case-variant duplicate canonical field", body: `{"etag":"\"old\"","ETag":"\"new\"","tag":"v0.4.4","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-08-16T12:00:00Z","completedAt":"2026-08-16T12:05:00Z"}`},
		{name: "trailing value", body: validStateJSON("") + " {}"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeStateFile(t, directory, test.body)
			_, err := mustNewFileStateRepository(t, directory).Load()
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Load() error = %v, want ErrInvalidState", err)
			}
		})
	}
}

func TestFileStateRepositoryReplacesCorruptStateAfterSuccessfulRevalidation(t *testing.T) {
	// Mutation caught: rereading corrupt prior state during Save makes corruption a permanent recovery blocker.
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, stateFileName), []byte(`{"etag":`), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := mustNewFileStateRepository(t, directory)
	want := validMirrorState()
	if err := repository.Save(want); err != nil {
		t.Fatalf("Save() error = %v, want corrupt cache replaced", err)
	}
	got, err := repository.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestFileStateRepositoryReportsIndeterminateCommitWhenCorruptPriorCannotBeDurablyReplaced(t *testing.T) {
	// Mutation caught: restoring or treating corrupt bytes as a valid prior cache understates post-replace commit uncertainty.
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, stateFileName), []byte(`{"etag":`), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		syncDirectory: func(*os.Root) error { return errors.New("directory sync failed") },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := validMirrorState()
	err = repository.Save(want)
	if !errors.Is(err, ErrIndeterminateStateCommit) {
		t.Fatalf("Save() error = %v, want ErrIndeterminateStateCommit", err)
	}
	got, loadErr := mustNewFileStateRepository(t, directory).Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want complete replacement %#v", got, want)
	}
}

func TestFileStateRepositoryLeavesCorruptStateUntouchedWhenReplacementFails(t *testing.T) {
	// Mutation caught: deleting corrupt state before the atomic replace turns a pre-commit fault into state loss.
	directory := t.TempDir()
	corrupt := []byte(`{"etag":`)
	if err := os.WriteFile(filepath.Join(directory, stateFileName), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		replace: func(string, *os.Root, string, string) error { return errors.New("replacement failed") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(validMirrorState()); err == nil || errors.Is(err, ErrIndeterminateStateCommit) {
		t.Fatalf("Save() error = %v, want known pre-commit replacement failure", err)
	}
	got, err := os.ReadFile(filepath.Join(directory, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, corrupt) {
		t.Fatalf("corrupt prior changed to %q, want %q", got, corrupt)
	}
}

func TestFileStateRepositoryReplacesOversizedRegularStateWithoutUnboundedRead(t *testing.T) {
	// Mutation caught: dropping identity when the bounded reader observes maxStateBytes+1 makes oversized corruption permanent.
	for _, size := range []int64{maxStateBytes + 1, maxStateBytes * 4096} {
		t.Run(fmt.Sprintf("size-%d", size), func(t *testing.T) {
			directory := t.TempDir()
			writeOversizedStateFile(t, directory, size)
			repository := mustNewFileStateRepository(t, directory)
			if _, err := repository.Load(); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("initial Load() error = %v, want ErrInvalidState", err)
			}
			want := validMirrorState()
			if err := repository.Save(want); err != nil {
				t.Fatalf("Save() error = %v, want oversized cache replaced", err)
			}
			got, err := repository.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got != want {
				t.Fatalf("Load() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestFileStateRepositoryReportsIndeterminateCommitWhenOversizedPriorReplacementCannotSync(t *testing.T) {
	// Mutation caught: treating an oversized prior as restorable permits a false ordinary failure after replacement.
	directory := t.TempDir()
	writeOversizedStateFile(t, directory, maxStateBytes*4096)
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		syncDirectory: func(*os.Root) error { return errors.New("directory sync failed") },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := validMirrorState()
	err = repository.Save(want)
	if !errors.Is(err, ErrIndeterminateStateCommit) {
		t.Fatalf("Save() error = %v, want ErrIndeterminateStateCommit", err)
	}
	got, loadErr := mustNewFileStateRepository(t, directory).Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want complete replacement %#v", got, want)
	}
}

func TestFileStateRepositoryRejectsInvalidFields(t *testing.T) {
	// Mutation caught: skipping field validation sends malformed ETags or release identities into a future conditional request.
	tests := []struct {
		name string
		body string
	}{
		{name: "weak ETag", body: strings.Replace(validStateJSON(""), `"etag":"\"current\""`, `"etag":"W/\"current\""`, 1)},
		{name: "noncanonical tag", body: strings.Replace(validStateJSON(""), `"tag":"v0.4.4"`, `"tag":"v00.4.4"`, 1)},
		{name: "uppercase SHA", body: strings.Replace(validStateJSON(""), `"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"sha256":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`, 1)},
		{name: "zero publication time", body: strings.Replace(validStateJSON(""), `"publishedAt":"2026-08-16T12:00:00Z"`, `"publishedAt":"0001-01-01T00:00:00Z"`, 1)},
		{name: "noncanonical completion time", body: strings.Replace(validStateJSON(""), `"completedAt":"2026-08-16T12:05:00Z"`, `"completedAt":"2026-08-16T20:05:00+08:00"`, 1)},
		{name: "completion before publication", body: strings.Replace(validStateJSON(""), `"completedAt":"2026-08-16T12:05:00Z"`, `"completedAt":"2026-08-16T11:59:59Z"`, 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeStateFile(t, directory, test.body)
			_, err := mustNewFileStateRepository(t, directory).Load()
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Load() error = %v, want ErrInvalidState", err)
			}
		})
	}
}

func TestFileStateRepositoryRejectsUnsafeOrOversizedStateFile(t *testing.T) {
	// Mutation caught: reading arbitrary filesystem objects or unbounded data can follow attacker-controlled state and exhaust memory.
	t.Run("oversized", func(t *testing.T) {
		directory := t.TempDir()
		writeStateFile(t, directory, strings.Repeat("x", 32<<10))
		_, err := mustNewFileStateRepository(t, directory).Load()
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Load() error = %v, want ErrInvalidState", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.Mkdir(filepath.Join(directory, "state.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := mustNewFileStateRepository(t, directory).Load()
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Load() error = %v, want ErrInvalidState", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.json")
		if err := os.WriteFile(target, []byte(validStateJSON("")), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(directory, "state.json")); err != nil {
			t.Skipf("symlink creation unavailable on %s: %v", runtime.GOOS, err)
		}
		_, err := mustNewFileStateRepository(t, directory).Load()
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Load() error = %v, want ErrInvalidState", err)
		}
	})
}

func TestFileStateRepositoryKeepsPriorStateWhenReplacementFails(t *testing.T) {
	// Mutation caught: exposing a new state before the replacement primitive succeeds loses the known-good cache on replacement failure.
	directory := t.TempDir()
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		replace: func(string, *os.Root, string, string) error { return errors.New("replacement failed") },
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	second := first
	second.ETag = `"second"`
	if err := repository.Save(second); err == nil {
		t.Fatal("Save() error = nil, want replacement error")
	}
	got, err := base.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != first {
		t.Fatalf("Load() = %#v, want preserved %#v", got, first)
	}
}

func TestFileStateRepositoryRestoresPriorStateAfterDirectorySyncFailure(t *testing.T) {
	// Mutation caught: returning after a post-replace directory-sync failure without recovery leaves a visible, possibly non-durable new cache.
	directory := t.TempDir()
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	var syncCalls int
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		syncDirectory: func(*os.Root) error {
			syncCalls++
			if syncCalls == 1 {
				return errors.New("directory sync failed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	second := first
	second.ETag = `"second"`
	if err := repository.Save(second); err == nil || errors.Is(err, ErrIndeterminateStateCommit) {
		t.Fatalf("Save() error = %v, want recovered directory sync error", err)
	}
	if syncCalls != 2 {
		t.Fatalf("directory sync calls = %d, want restore sync", syncCalls)
	}
	got, err := base.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != first {
		t.Fatalf("Load() = %#v, want restored %#v", got, first)
	}
}

func TestFileStateRepositoryReportsIndeterminateCommitWithoutPriorState(t *testing.T) {
	// Mutation caught: fabricating a prior cache after a post-replace sync failure hides an indeterminate first commit.
	directory := t.TempDir()
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		syncDirectory: func(*os.Root) error { return errors.New("directory sync failed") },
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	want := validMirrorState()
	err = repository.Save(want)
	if !errors.Is(err, ErrIndeterminateStateCommit) {
		t.Fatalf("Save() error = %v, want ErrIndeterminateStateCommit", err)
	}
	got, loadErr := mustNewFileStateRepository(t, directory).Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want complete new state %#v", got, want)
	}
}

func TestFileStateRepositoryReportsIndeterminateCommitWhenRestoreCannotSync(t *testing.T) {
	// Mutation caught: reporting a restored prior cache when its recovery directory sync also fails conceals an indeterminate commit.
	directory := t.TempDir()
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		syncDirectory: func(*os.Root) error { return errors.New("directory sync failed") },
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	second := first
	second.ETag = `"second"`
	err = repository.Save(second)
	if !errors.Is(err, ErrIndeterminateStateCommit) {
		t.Fatalf("Save() error = %v, want ErrIndeterminateStateCommit", err)
	}
	got, loadErr := base.Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v, want complete old or new state", loadErr)
	}
	if got != first && got != second {
		t.Fatalf("Load() = %#v, want complete old %#v or new %#v state", got, first, second)
	}
}

func TestFileStateRepositoryRejectsStateDirectoryReplacementBeforeCommit(t *testing.T) {
	// Mutation caught: continuing a save after the configured root path changes can commit into an attacker-replaced directory.
	parent := t.TempDir()
	directory := filepath.Join(parent, "state")
	priorDirectory := filepath.Join(parent, "prior-state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	rootReplaced := false
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		beforeReplace: func() error {
			if err := os.Rename(directory, priorDirectory); err != nil {
				return err
			}
			rootReplaced = true
			return os.Mkdir(directory, 0o700)
		},
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	second := first
	second.ETag = `"second"`
	if err := repository.Save(second); err == nil {
		t.Fatal("Save() error = nil, want state root replacement error")
	}
	if !rootReplaced {
		t.Skip("the current Windows directory handle prevents root replacement; Linux runtime acceptance remains required")
	}
	prior, priorErr := mustNewFileStateRepository(t, priorDirectory).Load()
	if priorErr != nil {
		t.Fatalf("prior Load() error = %v", priorErr)
	}
	if prior != first {
		t.Fatalf("prior Load() = %#v, want %#v", prior, first)
	}
	current, currentErr := mustNewFileStateRepository(t, directory).Load()
	if currentErr != nil {
		t.Fatalf("current Load() error = %v", currentErr)
	}
	if current != (MirrorState{}) {
		t.Fatalf("current Load() = %#v, want empty replacement directory", current)
	}
}

func TestFileStateRepositoryRejectsPathSwapAfterNonMutatingOpen(t *testing.T) {
	// Mutation caught: reading a handle after state.json is swapped can follow an in-root reparse point or stale inode.
	directory := t.TempDir()
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		openFile: func(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
			file, openErr := root.OpenFile(name, flag, perm)
			if openErr != nil {
				return nil, openErr
			}
			if renameErr := root.Rename(stateFileName, "swapped-state.json"); renameErr != nil {
				_ = file.Close()
				return nil, renameErr
			}
			replacement, replacementErr := root.OpenFile(stateFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if replacementErr != nil {
				_ = file.Close()
				return nil, replacementErr
			}
			if _, replacementErr = replacement.Write([]byte(validStateJSON(""))); replacementErr == nil {
				replacementErr = replacement.Close()
			} else {
				_ = replacement.Close()
			}
			if replacementErr != nil {
				_ = file.Close()
				return nil, replacementErr
			}
			return file, nil
		},
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	_, err = repository.Load()
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Load() error = %v, want ErrInvalidState", err)
	}
}

func TestFileStateRepositoryKeepsPriorStateWhenTempWriteFails(t *testing.T) {
	// Mutation caught: replacing or truncating state.json before the temporary file is fully durable destroys a usable retry cache.
	directory := t.TempDir()
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		write: func(*os.File, []byte) (int, error) { return 0, errors.New("interrupted write") },
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	second := first
	second.ETag = `"second"`
	if err := repository.Save(second); err == nil {
		t.Fatal("Save() error = nil, want interrupted write error")
	}
	got, err := base.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != first {
		t.Fatalf("Load() = %#v, want preserved %#v", got, first)
	}
}

func TestFileStateRepositoryKeepsPriorStateWhenTempSyncFails(t *testing.T) {
	// Mutation caught: replacing state.json before fsync can acknowledge a cache entry that is not durable after a crash.
	directory := t.TempDir()
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		syncFile: func(*os.File) error { return errors.New("interrupted sync") },
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	second := first
	second.ETag = `"second"`
	if err := repository.Save(second); err == nil {
		t.Fatal("Save() error = nil, want interrupted sync error")
	}
	got, err := base.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != first {
		t.Fatalf("Load() = %#v, want preserved %#v", got, first)
	}
}

func TestFileStateRepositoryAtomicallyReplacesPriorState(t *testing.T) {
	// Mutation caught: deleting or overwriting state.json before rename exposes a missing or partial cache during a save.
	directory := t.TempDir()
	first := validMirrorState()
	base := mustNewFileStateRepository(t, directory)
	if err := base.Save(first); err != nil {
		t.Fatalf("initial Save() error = %v", err)
	}
	second := first
	second.ETag = `"second"`
	repository, err := newFileStateRepositoryWithOptions(directory, fileStateOptions{
		beforeReplace: func() error {
			got, loadErr := base.Load()
			if loadErr != nil {
				return loadErr
			}
			if got != first {
				return errors.New("prior state was not visible before replacement")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newFileStateRepositoryWithOptions() error = %v", err)
	}
	if err := repository.Save(second); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := base.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != second {
		t.Fatalf("Load() = %#v, want %#v", got, second)
	}
}

func TestFileStateRepositoryWritesPrivateStateOnUnix(t *testing.T) {
	// Mutation caught: a permissive create mode exposes ETags and release provenance to other local users.
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission contract")
	}
	directory := t.TempDir()
	repository := mustNewFileStateRepository(t, directory)
	if err := repository.Save(validMirrorState()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state.json permissions = %04o, want 0600", got)
	}
}

func mustNewFileStateRepository(t *testing.T, directory string) StateRepository {
	t.Helper()
	repository, err := NewFileStateRepository(directory)
	if err != nil {
		t.Fatalf("NewFileStateRepository() error = %v", err)
	}
	return repository
}

func validMirrorState() MirrorState {
	return MirrorState{
		ETag:        `"current"`,
		Tag:         "v0.4.4",
		SHA256:      strings.Repeat("a", 64),
		PublishedAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, 8, 16, 12, 5, 0, 0, time.UTC),
	}
}

func writeOversizedStateFile(t *testing.T, directory string, size int64) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(directory, stateFileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func validStateJSON(suffix string) string {
	const valid = `{"etag":"\"current\"","tag":"v0.4.4","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-08-16T12:00:00Z","completedAt":"2026-08-16T12:05:00Z"}`
	return strings.TrimSuffix(valid, "}") + suffix + "}"
}

func writeStateFile(t *testing.T, directory, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
