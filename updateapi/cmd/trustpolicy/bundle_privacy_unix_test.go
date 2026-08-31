//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundleUnixPrivacyRequiresOwnerOnlyDirectoryAndFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-bundle-staging")
	if err := createPrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v, %v", info.Mode().Perm(), err)
	}
	file := filepath.Join(path, "policy.json")
	if err := writePrivateBundleFile(file, []byte("policy")); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestBundleUnixPrivacyRejectsGroupReadableDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateDirectory(path); err == nil {
		t.Fatal("verifyPrivateDirectory() accepted group-readable directory")
	}
}
