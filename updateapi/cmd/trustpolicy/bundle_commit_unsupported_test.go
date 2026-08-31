//go:build (!windows && !linux) || (linux && !amd64)

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedUnixBundleCommitFailsClosed(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	target := filepath.Join(base, "target")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := renameBundleDirectory(source, target); err == nil {
		t.Fatal("unsupported platform performed a fallback rename")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source changed on fail-closed commit: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target created on fail-closed commit: %v", err)
	}
}
