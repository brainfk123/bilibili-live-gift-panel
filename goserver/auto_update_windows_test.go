//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallOnExitRejectsPendingIntegrityFailureBeforeLaunch(t *testing.T) {
	tests := []struct {
		name            string
		sizeDelta       int64
		verificationErr error
	}{
		{name: "size mismatch", sizeDelta: 1},
		{name: "signature mismatch", verificationErr: errors.New("CN=Unexpected Publisher")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			updatesDir := filepath.Join(root, "updates")
			if err := os.MkdirAll(updatesDir, 0o700); err != nil {
				t.Fatal(err)
			}
			binary := []byte("pending executable")
			pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
			if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(binary)
			launched := false
			updater := newAutoUpdater(autoUpdaterOptions{
				CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"), UpdatesDir: updatesDir,
				ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.com/release"}}, AssetName: updateAssetName,
				VerifyExecutable: func(string) error { return test.verificationErr },
				LaunchInstaller: func(string, int, bool) error {
					launched = true
					return nil
				},
			})
			pending := pendingUpdate{
				Version: "1.1.0", Size: int64(len(binary)) + test.sizeDelta, SHA256: hex.EncodeToString(digest[:]),
				PendingPath: pendingPath, TargetPath: filepath.Join(root, "gift-panel.exe"),
			}
			if err := updater.writePendingMetadata(pending); err != nil {
				t.Fatal(err)
			}
			updater.pending = &pending

			if err := updater.InstallOnExit(false); err == nil {
				t.Fatal("invalid pending executable must be rejected")
			}
			if launched {
				t.Fatal("installer must not launch after pending integrity failure")
			}
			if updater.HasPending() {
				t.Fatal("invalid pending executable must be cleared from memory")
			}
			for _, path := range []string{pendingPath, updater.metadataPath()} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("invalid pending artifact %q survived: %v", path, err)
				}
			}
		})
	}
}

func TestInstallOnExitCleanupFailureIsObservableAndNeverLaunches(t *testing.T) {
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("pending executable")
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	removeAttempts := 0
	launched := false
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"), UpdatesDir: updatesDir,
		ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.com/release"}}, AssetName: updateAssetName,
		VerifyExecutable: func(string) error { return errors.New("CN=Unexpected Publisher") },
		LaunchInstaller: func(string, int, bool) error {
			launched = true
			return nil
		},
		RemoveFile: func(path string) error {
			if path == pendingPath {
				removeAttempts++
				return errors.New("locked pending executable")
			}
			return os.Remove(path)
		},
	})
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(binary)), SHA256: hex.EncodeToString(digest[:]),
		PendingPath: pendingPath, TargetPath: filepath.Join(root, "gift-panel.exe"),
	}
	if err := updater.writePendingMetadata(pending); err != nil {
		t.Fatal(err)
	}
	updater.pending = &pending

	err := updater.InstallOnExit(false)
	if err == nil || !strings.Contains(err.Error(), "清理") {
		t.Fatalf("error = %v, want observable cleanup failure", err)
	}
	if strings.Contains(err.Error(), "locked pending executable") || strings.Contains(err.Error(), "Unexpected Publisher") {
		t.Fatalf("user-visible error leaked diagnostic detail: %v", err)
	}
	if removeAttempts != 3 {
		t.Fatalf("remove attempts = %d, want 3", removeAttempts)
	}
	if launched || updater.HasPending() {
		t.Fatalf("launched = %v, has pending = %v", launched, updater.HasPending())
	}
	if status := updater.Status(); status.State != "error" || !strings.Contains(status.Message, "清理") {
		t.Fatalf("status = %#v", status)
	}
}

func TestInstallOnExitDoesNotLaunchWhenStaleTargetArtifactCannotBeCleaned(t *testing.T) {
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("valid pending executable")
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	staleNewPath := targetPath + ".new"
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleNewPath, []byte("stale executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	removeAttempts := 0
	launched := false
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "1.0.0", ExecutablePath: targetPath, UpdatesDir: updatesDir,
		ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.com/release"}}, AssetName: updateAssetName,
		VerifyExecutable: func(string) error { return nil },
		LaunchInstaller: func(string, int, bool) error {
			launched = true
			return nil
		},
		RemoveFile: func(path string) error {
			if path == staleNewPath {
				removeAttempts++
				return errors.New("locked stale executable")
			}
			return os.Remove(path)
		},
	})
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(binary)), SHA256: hex.EncodeToString(digest[:]),
		PendingPath: pendingPath, TargetPath: targetPath,
	}
	if err := updater.writePendingMetadata(pending); err != nil {
		t.Fatal(err)
	}
	updater.pending = &pending

	err := updater.InstallOnExit(false)
	if err == nil || !strings.Contains(err.Error(), "清理") {
		t.Fatalf("error = %v, want pre-launch cleanup failure", err)
	}
	if launched || updater.HasPending() {
		t.Fatalf("launched = %v, has pending = %v", launched, updater.HasPending())
	}
	if removeAttempts != 3 {
		t.Fatalf("remove attempts = %d, want 3", removeAttempts)
	}
}

func TestReplaceDownloadedExecutableKeepsBackupUntilNextStart(t *testing.T) {
	root := t.TempDir()
	pendingPath := filepath.Join(root, "updates", "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.MkdirAll(filepath.Dir(pendingPath), 0o700); err != nil {
		t.Fatal(err)
	}
	newBinary := []byte("new executable")
	oldBinary := []byte("old executable")
	if err := os.WriteFile(pendingPath, newBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, oldBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256(newBinary)
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(newBinary)), SHA256: hex.EncodeToString(digestBytes[:]), PendingPath: pendingPath, TargetPath: targetPath,
	}
	if err := replaceDownloadedExecutable(pendingPath, pending, 2147483647); err != nil {
		t.Fatal(err)
	}
	gotTarget, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	gotBackup, err := os.ReadFile(targetPath + ".old")
	if err != nil {
		t.Fatal(err)
	}
	if string(gotTarget) != string(newBinary) || string(gotBackup) != string(oldBinary) {
		t.Fatalf("target=%q backup=%q", gotTarget, gotBackup)
	}
}

func TestReplaceDownloadedExecutableStopsWhenStaleNewFileCannotBeCleaned(t *testing.T) {
	root := t.TempDir()
	pendingPath := filepath.Join(root, "updates", "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	newPath := targetPath + ".new"
	if err := os.MkdirAll(filepath.Dir(pendingPath), 0o700); err != nil {
		t.Fatal(err)
	}
	newBinary := []byte("new executable")
	oldBinary := []byte("old executable")
	if err := os.WriteFile(pendingPath, newBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, oldBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("stale partial executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(newBinary)
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(newBinary)), SHA256: hex.EncodeToString(digest[:]),
		PendingPath: pendingPath, TargetPath: targetPath,
	}
	previousRemove := removeWindowsUpdateFile
	removeAttempts := 0
	removeWindowsUpdateFile = func(path string) error {
		if path == newPath {
			removeAttempts++
			return errors.New("locked stale new executable")
		}
		return os.Remove(path)
	}
	t.Cleanup(func() { removeWindowsUpdateFile = previousRemove })

	err := replaceDownloadedExecutable(pendingPath, pending, 2147483647)
	if err == nil || !strings.Contains(err.Error(), "清理") {
		t.Fatalf("error = %v, want cleanup failure", err)
	}
	if removeAttempts != 3 {
		t.Fatalf("remove attempts = %d, want 3", removeAttempts)
	}
	gotTarget, readErr := os.ReadFile(targetPath)
	if readErr != nil || string(gotTarget) != string(oldBinary) {
		t.Fatalf("target = %q, err = %v", gotTarget, readErr)
	}
}
