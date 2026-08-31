//go:build !windows

package bundlefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func makeDirectoryUntrustedWritable(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
}

func makeFileNonPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUnixPrivateDirectoryUsesOwnerOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := CreatePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		t.Fatalf("private directory mode/owner = %v/%v", info.Mode().Perm(), stat)
	}
}
