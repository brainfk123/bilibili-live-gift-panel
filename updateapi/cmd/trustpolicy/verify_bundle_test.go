//go:build windows || linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

func TestVerifyBundleRejectsDuplicateUnknownAndExtraArgumentsBeforeRead(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "duplicate policy", args: []string{"--policy", "first", "--audit", "audit", "--policy=second"}},
		{name: "duplicate audit", args: []string{"--policy", "policy", "--audit=audit", "--audit", "second"}},
		{name: "unknown", args: []string{"--policy", "policy", "--audit", "audit", "--unknown", "secret"}},
		{name: "extra", args: []string{"--policy", "policy", "--audit", "audit", "extra-secret"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			var output bytes.Buffer
			err := runVerifyBundle(test.args, &output, func(string, string) (trustpolicy.CommittedBundle, error) {
				reads++
				return trustpolicy.CommittedBundle{}, nil
			})
			if err == nil {
				t.Fatal("invalid verify-bundle arguments were accepted")
			}
			if reads != 0 {
				t.Fatalf("reader called %d times before argument rejection", reads)
			}
			if output.Len() != 0 {
				t.Fatalf("invalid arguments produced trusted stdout: %q", output.String())
			}
		})
	}
}

func TestVerifyBundleSubprocessEmitsCanonicalEnvelopeWithExactBytes(t *testing.T) {
	base := newPrivateTestBase(t)
	bundle := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundle, "policy.json")
	auditPath := filepath.Join(bundle, "audit.json")
	policy, audit := testBundlePayload(t, "verify-bundle")
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		t.Fatal(err)
	}
	binary := buildTrustPolicyCLI(t)
	command := exec.Command(binary, "verify-bundle", "--policy", policyPath, "--audit", auditPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("verify-bundle failed: %v stderr=%q", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("verify-bundle stderr = %q", stderr.String())
	}
	policyDigest := sha256.Sum256(policy)
	auditDigest := sha256.Sum256(audit)
	want := fmt.Sprintf("{\"schemaVersion\":1,\"policy\":{\"length\":%d,\"sha256\":%q,\"bytesBase64\":%q},\"audit\":{\"length\":%d,\"sha256\":%q,\"bytesBase64\":%q}}\n",
		len(policy), hex.EncodeToString(policyDigest[:]), base64.StdEncoding.EncodeToString(policy),
		len(audit), hex.EncodeToString(auditDigest[:]), base64.StdEncoding.EncodeToString(audit))
	if stdout.String() != want {
		t.Fatalf("verify-bundle stdout is not the canonical envelope\n got: %s\nwant: %s", stdout.String(), want)
	}
	var envelope struct {
		SchemaVersion uint64 `json:"schemaVersion"`
		Policy        struct {
			Length      uint64 `json:"length"`
			SHA256      string `json:"sha256"`
			BytesBase64 string `json:"bytesBase64"`
		} `json:"policy"`
		Audit struct {
			Length      uint64 `json:"length"`
			SHA256      string `json:"sha256"`
			BytesBase64 string `json:"bytesBase64"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	decodedPolicy, err := base64.StdEncoding.Strict().DecodeString(envelope.Policy.BytesBase64)
	if err != nil || !bytes.Equal(decodedPolicy, policy) {
		t.Fatal("policy Base64 did not decode to exact retained-reader bytes")
	}
	decodedAudit, err := base64.StdEncoding.Strict().DecodeString(envelope.Audit.BytesBase64)
	if err != nil || !bytes.Equal(decodedAudit, audit) {
		t.Fatal("audit Base64 did not decode to exact retained-reader bytes")
	}
}

func TestVerifyBundleSubprocessErrorsReturnNoTrustedStdoutOrInputEcho(t *testing.T) {
	base := newPrivateTestBase(t)
	bundle := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundle, "policy.json")
	auditPath := filepath.Join(bundle, "audit.json")
	policy, audit := testBundlePayload(t, "verify-error")
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, trustpolicy.BundleCommitFileName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := buildTrustPolicyCLI(t)
	command := exec.Command(binary, "verify-bundle", "--policy", policyPath, "--audit", auditPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		t.Fatal("verify-bundle accepted a raced or invalid marker")
	}
	if stdout.Len() != 0 {
		t.Fatalf("verify-bundle error exposed trusted stdout: %q", stdout.String())
	}
	if stderr.String() != "trust policy command failed\n" {
		t.Fatalf("verify-bundle stderr = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), policyPath) || strings.Contains(stderr.String(), auditPath) || strings.Contains(stderr.String(), string(policy)) {
		t.Fatalf("verify-bundle error echoed path or content: %q", stderr.String())
	}
}

func TestVerifyBundleSubprocessRejectsUnknownFlagWithoutEcho(t *testing.T) {
	binary := buildTrustPolicyCLI(t)
	const secret = "verify-argument-must-not-echo"
	command := exec.Command(binary, "verify-bundle", "--policy", secret, "--audit", secret, "--unknown", secret)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("verify-bundle accepted an unknown flag")
	}
	if string(output) != "trust policy command failed\n" || strings.Contains(string(output), secret) {
		t.Fatalf("verify-bundle error was not fixed and redacted: %q", output)
	}
}

func TestRunDispatchesVerifyBundleWithoutProviderDependencies(t *testing.T) {
	base := newPrivateTestBase(t)
	bundle := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundle, "policy.json")
	auditPath := filepath.Join(bundle, "audit.json")
	policy, audit := testBundlePayload(t, "verify-dispatch")
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"verify-bundle", "--policy", policyPath, "--audit", auditPath}, nil, nil, &output, nil); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("verify-bundle produced no machine envelope")
	}
}

func buildTrustPolicyCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "trustpolicy.exe")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binary
}
