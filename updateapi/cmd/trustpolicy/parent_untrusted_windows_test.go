//go:build windows

package main

import (
	"os/exec"
	"testing"
)

func makeCLIParentUntrustedWritable(t *testing.T, path string) {
	t.Helper()
	command := exec.Command("icacls.exe", path, "/grant", "*S-1-1-0:(OI)(CI)M")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make CLI parent writable by Everyone: %v: %s", err, output)
	}
}
