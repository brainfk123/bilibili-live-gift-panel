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
	"time"

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
		{name: "duplicate reviewed SPKI", args: []string{"--reviewed-spki", "first", "--reviewed-spki=second"}},
		{name: "duplicate SPKI digest", args: []string{"--expected-spki-sha256", strings.Repeat("a", 64), "--expected-spki-sha256=" + strings.Repeat("b", 64)}},
		{name: "duplicate previous epoch", args: []string{"--expected-previous-epoch", "0", "--expected-previous-epoch=1"}},
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
	signer := newCommandSigner(t)
	policy, audit := testBundlePayloadWithSigner(t, "verify-bundle", signer)
	if _, err := trustpolicy.VerifySignedPolicy(policy, signer.public, signer.digest, 0, time.Now().UTC()); err != nil {
		t.Fatalf("test policy does not satisfy the independent verifier: %v", err)
	}
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		t.Fatal(err)
	}
	verifyArgs := verifyBundleArgs(t, policyPath, auditPath, signer.public, signer.digest, "0")
	if _, err := readReviewedPublicFile(verifyArgs[5], maxReviewedSPKIBytes); err != nil {
		t.Fatalf("test SPKI file does not satisfy reviewed-file preflight: %v", err)
	}
	binary := buildTrustPolicyCLI(t)
	command := exec.Command(binary, append([]string{"verify-bundle"}, verifyArgs...)...)
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
	want := fmt.Sprintf("{\"schemaVersion\":2,\"verification\":{\"epoch\":1,\"expectedPreviousEpoch\":0,\"spkiSha256\":%q},\"commit\":{\"schemaVersion\":1,\"policy\":{\"name\":\"policy.json\",\"length\":%d,\"sha256\":%q},\"audit\":{\"name\":\"audit.json\",\"length\":%d,\"sha256\":%q}},\"policy\":{\"name\":\"policy.json\",\"length\":%d,\"sha256\":%q,\"bytesBase64\":%q},\"audit\":{\"name\":\"audit.json\",\"length\":%d,\"sha256\":%q,\"bytesBase64\":%q}}\n",
		signer.digest,
		len(policy), hex.EncodeToString(policyDigest[:]), len(audit), hex.EncodeToString(auditDigest[:]),
		len(policy), hex.EncodeToString(policyDigest[:]), base64.StdEncoding.EncodeToString(policy),
		len(audit), hex.EncodeToString(auditDigest[:]), base64.StdEncoding.EncodeToString(audit))
	if stdout.String() != want {
		t.Fatalf("verify-bundle stdout is not the canonical envelope\n got: %s\nwant: %s", stdout.String(), want)
	}
	var envelope struct {
		SchemaVersion uint64 `json:"schemaVersion"`
		Verification  struct {
			Epoch                 uint64 `json:"epoch"`
			ExpectedPreviousEpoch uint64 `json:"expectedPreviousEpoch"`
			SPKISHA256            string `json:"spkiSha256"`
		} `json:"verification"`
		Policy struct {
			Name        string `json:"name"`
			Length      uint64 `json:"length"`
			SHA256      string `json:"sha256"`
			BytesBase64 string `json:"bytesBase64"`
		} `json:"policy"`
		Audit struct {
			Name        string `json:"name"`
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

func TestVerifyBundleRejectsUnreviewedRootEpochAndSignatureBeforeEmission(t *testing.T) {
	trustedSigner := newCommandSigner(t)
	untrustedSigner := newCommandSigner(t)
	trustedPolicy, trustedAudit := testBundlePayloadWithSigner(t, "trusted-verify", trustedSigner)
	untrustedPolicy, untrustedAudit := testBundlePayloadWithSigner(t, "untrusted-verify", untrustedSigner)
	trusted := committedBundleForTest(t, trustedPolicy, trustedAudit)
	untrusted := committedBundleForTest(t, untrustedPolicy, untrustedAudit)

	for _, test := range []struct {
		name           string
		committed      trustpolicy.CommittedBundle
		spki           []byte
		digest         string
		expectedBefore string
	}{
		{name: "reviewed digest mismatch", committed: trusted, spki: trustedSigner.public, digest: strings.Repeat("0", 64), expectedBefore: "0"},
		{name: "different P-256 root", committed: trusted, spki: untrustedSigner.public, digest: untrustedSigner.digest, expectedBefore: "0"},
		{name: "signature from another root", committed: untrusted, spki: trustedSigner.public, digest: trustedSigner.digest, expectedBefore: "0"},
		{name: "wrong previous epoch", committed: trusted, spki: trustedSigner.public, digest: trustedSigner.digest, expectedBefore: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runVerifyBundle(
				verifyBundleArgs(t, "private-policy-path", "private-audit-path", test.spki, test.digest, test.expectedBefore),
				&output,
				func(string, string) (trustpolicy.CommittedBundle, error) { return test.committed, nil },
			)
			if err == nil {
				t.Fatal("verify-bundle emitted a machine envelope without all reviewed cryptographic bindings")
			}
			if output.Len() != 0 {
				t.Fatalf("failed verification emitted trusted bytes: %q", output.String())
			}
		})
	}
}

func TestVerifyBundleSubprocessErrorsReturnNoTrustedStdoutOrInputEcho(t *testing.T) {
	base := newPrivateTestBase(t)
	bundle := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundle, "policy.json")
	auditPath := filepath.Join(bundle, "audit.json")
	signer := newCommandSigner(t)
	policy, audit := testBundlePayloadWithSigner(t, "verify-error", signer)
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, trustpolicy.BundleCommitFileName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := buildTrustPolicyCLI(t)
	command := exec.Command(binary, append([]string{"verify-bundle"}, verifyBundleArgs(t, policyPath, auditPath, signer.public, signer.digest, "0")...)...)
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
	signer := newCommandSigner(t)
	policy, audit := testBundlePayloadWithSigner(t, "verify-dispatch", signer)
	if err := bundlefs.WriteCommittedBundle(policyPath, policy, auditPath, audit); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), append([]string{"verify-bundle"}, verifyBundleArgs(t, policyPath, auditPath, signer.public, signer.digest, "0")...), nil, nil, &output, nil); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("verify-bundle produced no machine envelope")
	}
}

func verifyBundleArgs(t testing.TB, policyPath, auditPath string, spki []byte, digest, expectedPrevious string) []string {
	t.Helper()
	spkiPath := filepath.Join(t.TempDir(), "reviewed-spki.der")
	if err := os.WriteFile(spkiPath, spki, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{
		"--policy", policyPath,
		"--audit", auditPath,
		"--reviewed-spki", spkiPath,
		"--expected-spki-sha256", digest,
		"--expected-previous-epoch", expectedPrevious,
	}
}

func committedBundleForTest(t testing.TB, policy, audit []byte) trustpolicy.CommittedBundle {
	t.Helper()
	marker, err := trustpolicy.BuildBundleCommit("policy.json", policy, "audit.json", audit)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := trustpolicy.ValidateCommittedBundle("policy.json", policy, "audit.json", audit, marker)
	if err != nil {
		t.Fatal(err)
	}
	return committed
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
