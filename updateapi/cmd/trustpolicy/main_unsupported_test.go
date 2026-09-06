//go:build !windows && !linux

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestVerifyBundleFailsClosedWithoutTrustedStdoutOnUnsupportedPlatform(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "bundle")
	var output bytes.Buffer
	err := run(context.Background(), []string{
		"verify-bundle",
		"--policy", filepath.Join(bundle, "policy.json"),
		"--audit", filepath.Join(bundle, "audit.json"),
	}, nil, nil, &output, nil)
	if err == nil {
		t.Fatal("verify-bundle succeeded on an unsupported platform")
	}
	if output.Len() != 0 {
		t.Fatalf("unsupported-platform error exposed trusted stdout: %q", output.String())
	}
}
