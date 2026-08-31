//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxBundleCommitNoReplacePreservesExistingDirectory(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	target := filepath.Join(base, "target")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "policy.json"), []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(target, "foreign.txt")
	if err := os.WriteFile(foreign, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameBundleDirectory(source, target); err == nil {
		t.Fatal("renameBundleDirectory() replaced an existing final directory")
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "preserve" {
		t.Fatalf("existing final changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "policy.json")); err != nil || string(got) != "candidate" {
		t.Fatalf("source staging changed after failed no-replace commit: %q, %v", got, err)
	}
}
