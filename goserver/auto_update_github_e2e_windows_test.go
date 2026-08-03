//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	githubUpdateE2EChild = "GIFT_PANEL_UPDATE_E2E_CHILD"
	githubUpdateE2EDir   = "GIFT_PANEL_UPDATE_E2E_DIR"
)

func TestGitHubAutomaticUpdateEndToEnd(t *testing.T) {
	if os.Getenv(githubUpdateE2EChild) == "1" {
		runGitHubUpdateE2EChild(t)
		return
	}
	if os.Getenv("RUN_GITHUB_UPDATE_E2E") != "1" {
		t.Skip("set RUN_GITHUB_UPDATE_E2E=1 to exercise the live GitHub Release update chain")
	}

	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestGitHubAutomaticUpdateEndToEnd$")
	command.Env = append(os.Environ(), githubUpdateE2EChild+"=1", githubUpdateE2EDir+"="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("update child failed: %v\n%s", err, output)
	}

	expectedData, err := os.ReadFile(filepath.Join(root, "expected-sha256.txt"))
	if err != nil {
		t.Fatal(err)
	}
	expectedSHA := strings.TrimSpace(string(expectedData))
	targetPath := filepath.Join(root, "gift-panel-e2e.exe")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fileSHA256(targetPath) == expectedSHA {
			backup, backupErr := os.ReadFile(targetPath + ".old")
			if backupErr != nil {
				t.Fatal(backupErr)
			}
			if string(backup) != "old e2e executable" {
				t.Fatalf("unexpected backup contents: %q", backup)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("downloaded release did not replace the old executable; target SHA-256 = %q, want %q", fileSHA256(targetPath), expectedSHA)
}

func runGitHubUpdateE2EChild(t *testing.T) {
	root := os.Getenv(githubUpdateE2EDir)
	if root == "" {
		t.Fatal("missing update E2E directory")
	}
	targetPath := filepath.Join(root, "gift-panel-e2e.exe")
	if err := os.WriteFile(targetPath, []byte("old e2e executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "0.0.9",
		ExecutablePath: targetPath,
		UpdatesDir:     filepath.Join(root, "updates"),
		ReleaseURL:     updateReleaseURL,
		AssetName:      updateAssetName,
	})
	updater.checkAndDownload(context.Background(), true)
	status := updater.Status()
	if status.State != "ready" || !status.RestartRequired {
		t.Fatalf("update status = %#v", status)
	}
	metadata, err := os.ReadFile(updater.metadataPath())
	if err != nil {
		t.Fatal(err)
	}
	var pending pendingUpdate
	if err := json.Unmarshal(metadata, &pending); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "expected-sha256.txt"), []byte(pending.SHA256), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updater.InstallOnExit(); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
