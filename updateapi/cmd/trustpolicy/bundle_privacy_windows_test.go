//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleWindowsPrivateDirectoryHasVerifiedProtectedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-bundle-staging")
	if err := createPrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateDirectory(path); err != nil {
		t.Fatalf("verify created private directory: %v", err)
	}
	file := filepath.Join(path, "policy.json")
	if err := writePrivateBundleFile(file, []byte("policy")); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateDirectory(path); err != nil {
		t.Fatalf("verify private directory after child creation: %v", err)
	}
}

func TestBundleWindowsPrivacyVerifierRejectsAmbientInheritedDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ambient-directory")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateDirectory(path); err == nil {
		t.Fatal("verifyPrivateDirectory() accepted an ambient inherited DACL")
	}
}

func TestBundleWindowsPrivacyErrorsDoNotLeakSIDOrPath(t *testing.T) {
	const marker = "privacy-path-must-not-leak"
	path := filepath.Join(t.TempDir(), marker, "bundle")
	err := createPrivateDirectory(path)
	if err == nil {
		t.Fatal("createPrivateDirectory() unexpectedly succeeded without parent")
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "S-1-") {
		t.Fatalf("privacy error leaked path or SID: %q", err)
	}
}
