//go:build windows

package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

func TestVerifyBundleSubprocessSharingRaceReturnsNoTrustedStdout(t *testing.T) {
	base := newPrivateTestBase(t)
	bundle := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundle, "policy.json")
	auditPath := filepath.Join(bundle, "audit.json")
	policy, audit := testBundlePayload(t, "verify-sharing-race")
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		t.Fatal(err)
	}
	pointer, err := syscall.UTF16PtrFromString(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(pointer, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	binary := buildTrustPolicyCLI(t)
	command := exec.Command(binary, "verify-bundle", "--policy", policyPath, "--audit", auditPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err == nil {
		t.Fatal("verify-bundle succeeded while an artifact was exclusively raced")
	}
	if stdout.Len() != 0 {
		t.Fatalf("sharing-race error exposed trusted stdout: %q", stdout.String())
	}
	if stderr.String() != "trust policy command failed\n" {
		t.Fatalf("sharing-race stderr = %q", stderr.String())
	}
}
