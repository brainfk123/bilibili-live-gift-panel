//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestorePendingLogsVerificationDetailButExposesGenericStatus(t *testing.T) {
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("restored pending executable")
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(binary)), SHA256: hex.EncodeToString(digest[:]),
		PendingPath: pendingPath, TargetPath: targetPath,
	}
	writer := &autoUpdater{updatesDir: updatesDir}
	if err := writer.writePendingMetadata(pending); err != nil {
		t.Fatal(err)
	}

	var updater *autoUpdater
	diagnostics := captureAutoUpdateStderr(t, func() {
		updater = newAutoUpdater(autoUpdaterOptions{
			CurrentVersion: "1.0.0", ExecutablePath: targetPath, UpdatesDir: updatesDir,
			ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.com/release"}}, AssetName: updateAssetName,
			VerifyExecutable: func(string) error { return errors.New("CN=Restore Diagnostic Publisher") },
		})
	})
	if !strings.Contains(diagnostics, "Restore Diagnostic Publisher") {
		t.Fatalf("diagnostics = %q, want detailed restore verification cause", diagnostics)
	}
	status := updater.Status()
	if status.State != "error" || !strings.Contains(status.Message, "安全校验") {
		t.Fatalf("status = %#v", status)
	}
	if strings.Contains(status.Message, "Restore Diagnostic Publisher") {
		t.Fatalf("status leaked restore diagnostic: %q", status.Message)
	}
}

func captureAutoUpdateStderr(t *testing.T, action func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = writer
	action()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = previous
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}

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
	allowRemoval := false
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
				if !allowRemoval {
					return errors.New("locked pending executable")
				}
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
	if launched || !updater.HasPending() {
		t.Fatalf("launched = %v, has pending = %v", launched, updater.HasPending())
	}
	for _, path := range []string{pendingPath, updater.metadataPath()} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("retry artifact %q must remain after failed executable removal: %v", path, statErr)
		}
	}
	if status := updater.Status(); status.State != "error" || !strings.Contains(status.Message, "清理") {
		t.Fatalf("status = %#v", status)
	}

	allowRemoval = true
	err = updater.InstallOnExit(false)
	if err == nil || !strings.Contains(err.Error(), "安全校验") {
		t.Fatalf("retry error = %v, want generic verification rejection", err)
	}
	if launched || updater.HasPending() {
		t.Fatalf("after recovery launched = %v, has pending = %v", launched, updater.HasPending())
	}
	for _, path := range []string{pendingPath, updater.metadataPath()} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("recovered cleanup artifact %q survived: %v", path, statErr)
		}
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

func TestInstallOnExitLogsLauncherDetailButReturnsGenericError(t *testing.T) {
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("valid pending executable")
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "1.0.0", ExecutablePath: targetPath, UpdatesDir: updatesDir,
		ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.com/release"}}, AssetName: updateAssetName,
		VerifyExecutable: func(string) error { return nil },
		LaunchInstaller: func(string, int, bool) error {
			return errors.New(`CreateProcess access denied C:\sensitive\path.exe`)
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

	var installErr error
	diagnostics := captureAutoUpdateStderr(t, func() { installErr = updater.InstallOnExit(false) })
	if installErr == nil || !strings.Contains(installErr.Error(), "启动更新替换器失败") {
		t.Fatalf("error = %v, want generic launcher failure", installErr)
	}
	if strings.Contains(installErr.Error(), "CreateProcess") || strings.Contains(installErr.Error(), "sensitive") {
		t.Fatalf("user-visible error leaked launcher diagnostic: %v", installErr)
	}
	if !strings.Contains(diagnostics, "CreateProcess") || !strings.Contains(diagnostics, "sensitive") {
		t.Fatalf("diagnostics = %q, want raw launcher cause", diagnostics)
	}
}

func TestLaunchUpdateInstallerRevalidatesPendingAfterStaleCleanup(t *testing.T) {
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("verified pending executable")
	tampered := []byte("tampered pending executable")
	if len(binary) != len(tampered) {
		t.Fatal("test fixtures must have equal size")
	}
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(binary)), SHA256: hex.EncodeToString(digest[:]),
		PendingPath: pendingPath, TargetPath: targetPath,
	}
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "1.0.0", ExecutablePath: targetPath, UpdatesDir: updatesDir,
		ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.com/release"}}, AssetName: updateAssetName,
	})
	if err := updater.writePendingMetadata(pending); err != nil {
		t.Fatal(err)
	}

	previousRemove := removeWindowsUpdateFile
	previousStart := startUpdateInstallerExecutable
	started := false
	removeWindowsUpdateFile = func(path string) error {
		if path == targetPath+".new" {
			if err := os.WriteFile(pendingPath, tampered, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return os.Remove(path)
	}
	startUpdateInstallerExecutable = func(string, ...string) error {
		started = true
		return nil
	}
	t.Cleanup(func() {
		removeWindowsUpdateFile = previousRemove
		startUpdateInstallerExecutable = previousStart
	})

	err := launchUpdateInstaller(updater.metadataPath(), 1234, false)
	if err == nil || !strings.Contains(err.Error(), "安全校验") {
		t.Fatalf("error = %v, want final pending verification failure", err)
	}
	if started {
		t.Fatal("tampered pending executable must not start")
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

func TestReplaceDownloadedExecutableRevalidatesFinalTargetAfterRename(t *testing.T) {
	root := t.TempDir()
	pendingPath := filepath.Join(root, "updates", "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.MkdirAll(filepath.Dir(pendingPath), 0o700); err != nil {
		t.Fatal(err)
	}
	newBinary := []byte("verified new executable")
	tampered := []byte("tampered new executable")
	if len(newBinary) != len(tampered) {
		t.Fatal("test fixtures must have equal size")
	}
	oldBinary := []byte("old executable")
	if err := os.WriteFile(pendingPath, newBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, oldBinary, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(newBinary)
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(newBinary)), SHA256: hex.EncodeToString(digest[:]),
		PendingPath: pendingPath, TargetPath: targetPath,
	}
	previousRename := renameWindowsUpdateFile
	renameWindowsUpdateFile = func(sourcePath, destinationPath string) error {
		if err := renameUpdateFile(sourcePath, destinationPath); err != nil {
			return err
		}
		if destinationPath == targetPath && sourcePath == targetPath+".new" {
			return os.WriteFile(destinationPath, tampered, 0o700)
		}
		return nil
	}
	t.Cleanup(func() { renameWindowsUpdateFile = previousRename })

	err := replaceDownloadedExecutable(pendingPath, pending, 2147483647)
	if err == nil || !strings.Contains(err.Error(), "安全校验") {
		t.Fatalf("error = %v, want final target verification failure", err)
	}
	restored, readErr := os.ReadFile(targetPath)
	if readErr != nil || string(restored) != string(oldBinary) {
		t.Fatalf("restored target = %q, err = %v", restored, readErr)
	}
}

func TestStartVerifiedUpdatedExecutableRejectsTamperedTarget(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "gift-panel.exe")
	verified := []byte("verified final executable")
	tampered := []byte("tampered final executable")
	if len(verified) != len(tampered) {
		t.Fatal("test fixtures must have equal size")
	}
	if err := os.WriteFile(targetPath, tampered, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(verified)
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(verified)), SHA256: hex.EncodeToString(digest[:]), TargetPath: targetPath,
	}
	previousStart := startUpdatedTargetExecutable
	started := false
	startUpdatedTargetExecutable = func(string, ...string) error {
		started = true
		return nil
	}
	t.Cleanup(func() { startUpdatedTargetExecutable = previousStart })

	err := startVerifiedUpdatedExecutable(pending)
	if err == nil || !strings.Contains(err.Error(), "安全校验") {
		t.Fatalf("error = %v, want restart verification failure", err)
	}
	if started {
		t.Fatal("tampered final target must not restart")
	}
}

func TestStartVerifiedUpdatedExecutableLogsStartDetailButReturnsGenericError(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "gift-panel.exe")
	binary := []byte("verified final executable")
	if err := os.WriteFile(targetPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	pending := pendingUpdate{
		Version: "1.1.0", Size: int64(len(binary)), SHA256: hex.EncodeToString(digest[:]), TargetPath: targetPath,
	}
	previousStart := startUpdatedTargetExecutable
	startUpdatedTargetExecutable = func(string, ...string) error {
		return errors.New(`CreateProcess restart denied C:\sensitive\target.exe`)
	}
	t.Cleanup(func() { startUpdatedTargetExecutable = previousStart })

	var startErr error
	diagnostics := captureAutoUpdateStderr(t, func() { startErr = startVerifiedUpdatedExecutable(pending) })
	if startErr == nil || !strings.Contains(startErr.Error(), "启动更新后程序失败") {
		t.Fatalf("error = %v, want generic restart failure", startErr)
	}
	if strings.Contains(startErr.Error(), "CreateProcess") || strings.Contains(startErr.Error(), "sensitive") {
		t.Fatalf("user-visible error leaked restart diagnostic: %v", startErr)
	}
	if !strings.Contains(diagnostics, "CreateProcess") || !strings.Contains(diagnostics, "sensitive") {
		t.Fatalf("diagnostics = %q, want raw restart cause", diagnostics)
	}
}

func TestRunUpdateHelperLogsStateReadDetailButReturnsGenericError(t *testing.T) {
	missingStatePath := filepath.Join(t.TempDir(), "missing-update-state.json")
	stderr := captureAutoUpdateStderr(t, func() {
		handled, helperErr := runUpdateHelper([]string{"--apply-update", "--state", missingStatePath, "123"})
		if !handled {
			t.Fatal("expected update helper arguments to be handled")
		}
		if helperErr == nil || helperErr.Error() != "读取更新状态失败" {
			t.Fatalf("expected stable generic state-read error, got %v", helperErr)
		}
	})
	if !strings.Contains(stderr, missingStatePath) {
		t.Fatalf("expected detailed state-read diagnostic on stderr, got %q", stderr)
	}
}
