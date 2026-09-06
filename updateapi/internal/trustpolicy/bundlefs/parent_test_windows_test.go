//go:build windows

package bundlefs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func makeDirectoryUntrustedWritable(t *testing.T, path string) {
	t.Helper()
	command := exec.Command("icacls.exe", path, "/grant", "*S-1-1-0:(OI)(CI)M")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make parent writable by Everyone: %v: %s", err, output)
	}
}

func makeFileNonPrivate(t *testing.T, path string) {
	t.Helper()
	command := exec.Command("icacls.exe", path, "/grant", "*S-1-1-0:R")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make file readable by Everyone: %v: %s", err, output)
	}
}

func TestWindowsDirectorySyncUsesRealWritableHandle(t *testing.T) {
	outer := t.TempDir()
	parentPath := filepath.Join(outer, "private-parent")
	mustCreatePrivateDirectory(t, parentPath)
	parent, err := openRetainedParent(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.close()
	if err := syncDirectoryHandle(parent.handle); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPrivateDirectoryUsesExactProtectedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := CreatePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	handle, err := openRetainedDirectoryHandle(root, path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := verifyPrivateDirectoryHandle(handle); err != nil {
		t.Fatal("created directory does not have the exact protected current-user and SYSTEM DACL")
	}
}
