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
	"time"
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

func TestEnrollmentInstallerAndReplaceUsePolicyVerifierForAllFiveChecks(t *testing.T) {
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("policy-authorized enrollment executable")
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("previous executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	github := false
	pending := pendingUpdate{
		SchemaVersion: pendingUpdateSchemaVersion, Version: "0.4.12",
		Size: int64(len(binary)), SHA256: hex.EncodeToString(digest[:]), PendingPath: pendingPath, TargetPath: targetPath,
		Verification: pendingUpdateVerification{
			Provenance: pendingVerificationSignedPolicy, SourceName: "domestic", SourceURLSHA256: strings.Repeat("b", 64), SourceGitHub: &github,
			Tag: "v0.4.12", Channel: updateChannelStable, ArtifactSHA256: hex.EncodeToString(digest[:]),
			PolicyEpoch: 7, PolicySHA256: strings.Repeat("c", 64), PolicyMode: updateTrustModeCurrent,
		},
	}
	writer := &autoUpdater{updatesDir: updatesDir}
	if err := writer.writePendingMetadata(pending); err != nil {
		t.Fatal(err)
	}

	previousResolver := pendingUpdateVerifierForBuild
	previousStart := startUpdateInstallerExecutable
	previousVersion, previousPublisher := appVersion, updateExpectedPublisherHex
	resolverCalls := 0
	verifiedPaths := make([]string, 0, 5)
	pendingUpdateVerifierForBuild = func(got pendingUpdate) (func(string) error, error) {
		resolverCalls++
		if got.Verification.Provenance != pending.Verification.Provenance || got.Verification.SourceName != pending.Verification.SourceName ||
			got.Verification.SourceURLSHA256 != pending.Verification.SourceURLSHA256 || got.Verification.SourceGitHub == nil || *got.Verification.SourceGitHub != github ||
			got.Verification.Tag != pending.Verification.Tag || got.Verification.Channel != pending.Verification.Channel || got.Verification.ArtifactSHA256 != pending.Verification.ArtifactSHA256 ||
			got.Verification.PolicyEpoch != pending.Verification.PolicyEpoch || got.Verification.PolicySHA256 != pending.Verification.PolicySHA256 || got.Verification.PolicyMode != pending.Verification.PolicyMode || got.SHA256 != pending.SHA256 {
			t.Fatalf("policy verifier candidate = %#v, want tag/channel/hash from pending metadata", got)
		}
		return func(path string) error {
			verifiedPaths = append(verifiedPaths, filepath.Clean(path))
			return nil
		}, nil
	}
	startUpdateInstallerExecutable = func(string, ...string) error { return nil }
	appVersion, updateExpectedPublisherHex = "0.4.12", ""
	t.Cleanup(func() {
		pendingUpdateVerifierForBuild = previousResolver
		startUpdateInstallerExecutable = previousStart
		appVersion, updateExpectedPublisherHex = previousVersion, previousPublisher
	})

	if err := launchUpdateInstaller(writer.metadataPath(), 1234, false); err != nil {
		t.Fatalf("launchUpdateInstaller rejected policy enrollment: %v", err)
	}
	if err := replaceDownloadedExecutable(pendingPath, pending, 2147483647); err != nil {
		t.Fatalf("replaceDownloadedExecutable rejected policy enrollment: %v", err)
	}
	if resolverCalls != 2 {
		t.Fatalf("policy verifier resolutions = %d, want once per launch/replace phase", resolverCalls)
	}
	wantPaths := []string{pendingPath, pendingPath, pendingPath, targetPath + ".new", targetPath}
	if len(verifiedPaths) != len(wantPaths) {
		t.Fatalf("policy verification paths = %#v, want five checks", verifiedPaths)
	}
	for index := range wantPaths {
		if verifiedPaths[index] != filepath.Clean(wantPaths[index]) {
			t.Fatalf("policy verification path %d = %q, want %q", index, verifiedPaths[index], filepath.Clean(wantPaths[index]))
		}
	}
}

func TestEnrollmentWindowsFiveChecksRedactSensitiveVerificationErrors(t *testing.T) {
	const sensitive = `recognizable-secret C:\Users\private-user\artifact.exe`
	tests := []struct {
		name   string
		phase  string
		failAt int
	}{
		{name: "launch first", phase: "launch", failAt: 1},
		{name: "launch second", phase: "launch", failAt: 2},
		{name: "replace source", phase: "replace", failAt: 1},
		{name: "replace new", phase: "replace", failAt: 2},
		{name: "replace final", phase: "replace", failAt: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			pending, metadataPath := writeWindowsEnrollmentPending(t, root)
			previousResolver := pendingUpdateVerifierForBuild
			previousStart := startUpdateInstallerExecutable
			calls := 0
			pendingUpdateVerifierForBuild = func(pendingUpdate) (func(string) error, error) {
				return func(string) error {
					calls++
					if calls == test.failAt {
						return errors.New(sensitive)
					}
					return nil
				}, nil
			}
			startUpdateInstallerExecutable = func(string, ...string) error { return nil }
			t.Cleanup(func() {
				pendingUpdateVerifierForBuild = previousResolver
				startUpdateInstallerExecutable = previousStart
			})

			var operationErr error
			diagnostics := captureAutoUpdateStderr(t, func() {
				if test.phase == "launch" {
					operationErr = launchUpdateInstaller(metadataPath, 1234, false)
				} else {
					operationErr = replaceDownloadedExecutable(pending.PendingPath, pending, 2147483647)
				}
			})
			if operationErr == nil {
				t.Fatal("sensitive enrollment verification error was accepted")
			}
			if strings.Contains(diagnostics, "recognizable-secret") || strings.Contains(diagnostics, "private-user") {
				t.Fatalf("enrollment diagnostics leaked sensitive verifier error: %q", diagnostics)
			}
			if !strings.Contains(diagnostics, "update_result=artifact_verification_failed") {
				t.Fatalf("enrollment diagnostics = %q, want bounded result code", diagnostics)
			}
		})
	}
}

func TestEnrollmentInstallHelperAndRestartDiagnosticsAreBounded(t *testing.T) {
	const sensitive = `recognizable-secret C:\Users\private-user\process.exe`
	t.Run("install launcher", func(t *testing.T) {
		fixture := newDurablePolicyPendingFixture(t, testTrustNow.Add(time.Hour))
		fixture.Updater.launchInstaller = func(string, int, bool) error { return errors.New(sensitive) }
		var installErr error
		diagnostics := captureAutoUpdateStderr(t, func() { installErr = fixture.Updater.InstallOnExit(false) })
		assertBoundedEnrollmentDiagnostics(t, diagnostics, sensitive, "installer_launch_failed")
		if installErr == nil {
			t.Fatal("sensitive installer launch error was accepted")
		}
	})

	t.Run("helper apply", func(t *testing.T) {
		root := t.TempDir()
		_, metadataPath := writeWindowsEnrollmentPending(t, root)
		previousApply := applyPendingUpdate
		applyPendingUpdate = func(pendingUpdate, int) error { return errors.New(sensitive) }
		t.Cleanup(func() { applyPendingUpdate = previousApply })
		var helperErr error
		diagnostics := captureAutoUpdateStderr(t, func() {
			_, helperErr = runUpdateHelper([]string{"--apply-update", "--state", metadataPath, "2147483647"})
		})
		assertBoundedEnrollmentDiagnostics(t, diagnostics, sensitive, "update_apply_failed")
		if helperErr == nil {
			t.Fatal("sensitive helper apply error was accepted")
		}
	})

	t.Run("restart process", func(t *testing.T) {
		root := t.TempDir()
		pending, _ := writeWindowsEnrollmentPending(t, root)
		binary, err := os.ReadFile(pending.PendingPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pending.TargetPath, binary, 0o700); err != nil {
			t.Fatal(err)
		}
		previousResolver := pendingUpdateVerifierForBuild
		previousStart := startUpdatedTargetExecutable
		started := false
		pendingUpdateVerifierForBuild = func(pendingUpdate) (func(string) error, error) {
			return func(string) error { return nil }, nil
		}
		startUpdatedTargetExecutable = func(string, ...string) error { started = true; return errors.New(sensitive) }
		t.Cleanup(func() {
			pendingUpdateVerifierForBuild = previousResolver
			startUpdatedTargetExecutable = previousStart
		})
		var restartErr error
		diagnostics := captureAutoUpdateStderr(t, func() { restartErr = startVerifiedUpdatedExecutable(pending) })
		assertBoundedEnrollmentDiagnostics(t, diagnostics, sensitive, "restart_launch_failed")
		if restartErr == nil || !started {
			t.Fatalf("restart error = %v, started = %v", restartErr, started)
		}
	})

	t.Run("restore verification", func(t *testing.T) {
		fixture := newDurablePolicyPendingFixture(t, testTrustNow.Add(time.Hour))
		restartedStore := &updateTrustStore{Root: &fixture.Key.PublicKey, EmbeddedPolicy: fixture.Policy, CacheDir: fixture.Store.CacheDir}
		diagnostics := captureAutoUpdateStderr(t, func() {
			_ = newAutoUpdater(autoUpdaterOptions{
				CurrentVersion: "0.4.11", ExecutablePath: fixture.TargetPath, UpdatesDir: fixture.UpdatesDir,
				ReleaseSources: []updateReleaseSource{fixture.Source}, AssetName: updateAssetName,
				TrustStore: restartedStore, Now: func() time.Time { return testTrustNow },
				InspectAuthenticode: func(string) (inspectedUpdateCertificate, error) {
					return inspectedUpdateCertificate{}, errors.New(sensitive)
				},
			})
		})
		assertBoundedEnrollmentDiagnostics(t, diagnostics, sensitive, "authenticode_invalid")
	})
}

func writeWindowsEnrollmentPending(t testing.TB, root string) (pendingUpdate, string) {
	t.Helper()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("Windows enrollment pending executable")
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("previous executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	github := false
	pending := pendingUpdate{
		SchemaVersion: pendingUpdateSchemaVersion, Version: "0.4.12", Size: int64(len(binary)), SHA256: hex.EncodeToString(digest[:]),
		PendingPath: pendingPath, TargetPath: targetPath,
		Verification: pendingUpdateVerification{
			Provenance: pendingVerificationSignedPolicy, SourceName: "domestic", SourceURLSHA256: strings.Repeat("b", 64), SourceGitHub: &github,
			Tag: "v0.4.12", Channel: updateChannelStable, ArtifactSHA256: hex.EncodeToString(digest[:]),
			PolicyEpoch: 7, PolicySHA256: strings.Repeat("c", 64), PolicyMode: updateTrustModeCurrent,
		},
	}
	writer := &autoUpdater{updatesDir: updatesDir}
	if err := writer.writePendingMetadata(pending); err != nil {
		t.Fatal(err)
	}
	return pending, writer.metadataPath()
}

func assertBoundedEnrollmentDiagnostics(t testing.TB, diagnostics, sensitive, code string) {
	t.Helper()
	if strings.Contains(diagnostics, sensitive) || strings.Contains(diagnostics, "private-user") {
		t.Fatalf("enrollment diagnostics leaked sensitive text: %q", diagnostics)
	}
	if !strings.Contains(diagnostics, "update_result="+code) {
		t.Fatalf("enrollment diagnostics = %q, want code %q", diagnostics, code)
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

func TestRunUpdateHelperBoundsMissingStateDiagnostics(t *testing.T) {
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
	if strings.Contains(stderr, missingStatePath) || !strings.Contains(stderr, "update_result=pending_metadata_unavailable") {
		t.Fatalf("state-read diagnostics were not bounded: %q", stderr)
	}
}
