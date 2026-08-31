//go:build !windows

package main

import (
	"os"
	"testing"
)

func makeCLIParentUntrustedWritable(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
}
