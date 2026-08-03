//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

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
		Version: "1.1.0", SHA256: hex.EncodeToString(digestBytes[:]), PendingPath: pendingPath, TargetPath: targetPath,
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
