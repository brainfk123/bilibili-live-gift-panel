//go:build !windows && !linux

package bundlefs

import (
	"path/filepath"
	"testing"
)

func TestUnsupportedPlatformFailsClosed(t *testing.T) {
	parent := t.TempDir()
	bundle := filepath.Join(parent, "bundle")
	policy := filepath.Join(bundle, "policy.json")
	audit := filepath.Join(bundle, "audit.json")
	if err := CreatePrivateDirectory(filepath.Join(parent, "private")); err == nil {
		t.Fatal("private directory bootstrap succeeded on an unsupported platform")
	}
	if _, _, err := ValidateOutputPaths(policy, audit); err == nil {
		t.Fatal("output preflight succeeded on an unsupported platform")
	}
	if err := WriteCommittedBundle(policy, []byte("policy"), audit, []byte("audit")); err == nil {
		t.Fatal("bundle write succeeded on an unsupported platform")
	}
	committed, err := ReadCommittedBundle(policy, audit)
	if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
		t.Fatal("bundle read exposed bytes on an unsupported platform")
	}
}
