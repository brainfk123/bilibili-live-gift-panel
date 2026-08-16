package mirror

import (
	"errors"
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
		{name: "duplicate field", body: `{"etag":"\"old\"","etag":"\"new\"","tag":"v0.4.4","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-08-16T12:00:00Z","completedAt":"2026-08-16T12:05:00Z"}`},
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
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires elevated Windows test privileges")
		}
		directory := t.TempDir()
		target := filepath.Join(directory, "target.json")
		if err := os.WriteFile(target, []byte(validStateJSON("")), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(directory, "state.json")); err != nil {
			t.Fatal(err)
		}
		_, err := mustNewFileStateRepository(t, directory).Load()
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Load() error = %v, want ErrInvalidState", err)
		}
	})
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
