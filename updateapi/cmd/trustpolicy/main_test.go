package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

func TestKMSSignerCLIWritesCreateOnlyPrivateOutputs(t *testing.T) {
	fake := newCommandSigner(t)
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	auditPath := filepath.Join(directory, "audit.json")
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	environment := map[string]string{
		"REVIEWED_KMS_KEY_ID":      "kms-key-id",
		"REVIEWED_KMS_SPKI_SHA256": fake.digest,
		"GITHUB_ACTOR":             "release-approver",
	}
	var output bytes.Buffer
	factoryCalls := 0
	err := run(context.Background(), []string{
		"sign",
		"--candidate", candidatePath,
		"--expected-previous-epoch", "0",
		"--kms-region", "ap-shanghai",
		"--kms-key-id-env", "REVIEWED_KMS_KEY_ID",
		"--expected-spki-sha256-env", "REVIEWED_KMS_SPKI_SHA256",
		"--output", policyPath,
		"--audit-output", auditPath,
	}, func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	}, func(region, expectedDigest string) (trustpolicy.Signer, error) {
		factoryCalls++
		if region != "ap-shanghai" || expectedDigest != fake.digest {
			t.Fatalf("factory input = %q/%q", region, expectedDigest)
		}
		return fake, nil
	}, &output, func() time.Time {
		return time.Date(2029, 1, 2, 3, 4, 5, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
	policy := mustReadPrivateFile(t, policyPath)
	if !bytes.Contains(policy, []byte(`"algorithm":"ecdsa-p256-sha256"`)) {
		t.Fatalf("policy = %s, want client-compatible algorithm", policy)
	}
	auditBytes := mustReadPrivateFile(t, auditPath)
	var audit map[string]any
	if err := json.Unmarshal(auditBytes, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit) != 6 || audit["keyId"] != "kms-key-id" || audit["ciActor"] != "release-approver" {
		t.Fatalf("audit = %v, want exact six-field non-secret audit", audit)
	}
	if output.String() != "publisher policy signed\n" {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestKMSSignerCLIRefusesOverwriteBeforeSignerConstruction(t *testing.T) {
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	auditPath := filepath.Join(directory, "audit.json")
	if err := os.WriteFile(policyPath, []byte("preserve-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	called := false
	err := run(context.Background(), validCommandArgs(candidatePath, policyPath, auditPath), func(name string) (string, bool) {
		values := map[string]string{"REVIEWED_KMS_KEY_ID": "kms-key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GITHUB_ACTOR": "release-approver"}
		value, ok := values[name]
		return value, ok
	}, func(string, string) (trustpolicy.Signer, error) {
		called = true
		return nil, errors.New("must not construct signer")
	}, os.Stdout, time.Now)
	if err == nil {
		t.Fatal("run() error = nil, want create-only rejection")
	}
	if called {
		t.Fatal("run() constructed signer before output preflight")
	}
	got, readErr := os.ReadFile(policyPath)
	if readErr != nil || string(got) != "preserve-me" {
		t.Fatalf("existing output changed: %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(auditPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("audit created on rejected overwrite: %v", statErr)
	}
}

func TestKMSSignerCLIRedactsEnvironmentAndProviderErrors(t *testing.T) {
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	auditPath := filepath.Join(directory, "audit.json")
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	const secret = "credential-secret-from-provider"
	var output bytes.Buffer
	err := run(context.Background(), validCommandArgs(candidatePath, policyPath, auditPath), func(name string) (string, bool) {
		values := map[string]string{"REVIEWED_KMS_KEY_ID": "kms-key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GITHUB_ACTOR": "release-approver"}
		value, ok := values[name]
		return value, ok
	}, func(string, string) (trustpolicy.Signer, error) {
		return nil, errors.New(secret)
	}, &output, time.Now)
	if err == nil {
		t.Fatal("run() error = nil, want provider failure")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(output.String(), secret) || strings.Contains(err.Error(), "kms-key-id") || strings.Contains(err.Error(), strings.Repeat("a", 64)) {
		t.Fatalf("command leaked provider or environment value: err=%q output=%q", err, output.String())
	}
}

func TestKMSSignerCLIRejectsUnsafeEnvironmentBeforeSignerConstruction(t *testing.T) {
	directory := t.TempDir()
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	for _, test := range []struct {
		name   string
		values map[string]string
	}{
		{name: "unsafe key ID", values: map[string]string{"REVIEWED_KMS_KEY_ID": " key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GITHUB_ACTOR": "release-approver"}},
		{name: "unsafe digest", values: map[string]string{"REVIEWED_KMS_KEY_ID": "key-id", "REVIEWED_KMS_SPKI_SHA256": "not-a-digest", "GITHUB_ACTOR": "release-approver"}},
		{name: "unsafe actor", values: map[string]string{"REVIEWED_KMS_KEY_ID": "key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GITHUB_ACTOR": "actor\nsecret"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			policyPath := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+"-policy.json")
			auditPath := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+"-audit.json")
			err := run(context.Background(), validCommandArgs(candidatePath, policyPath, auditPath), func(name string) (string, bool) {
				value, ok := test.values[name]
				return value, ok
			}, func(string, string) (trustpolicy.Signer, error) {
				called = true
				return nil, errors.New("must not construct signer")
			}, ioDiscard{}, time.Now)
			if err == nil {
				t.Fatal("run() error = nil, want environment rejection")
			}
			if called {
				t.Fatal("run() constructed signer before validating environment values")
			}
		})
	}
}

func TestKMSSignerCLIRejectsSecretValueFlagsInSubprocess(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "trustpolicy.exe")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	const secret = "must-not-appear-in-output"
	command := exec.Command(binary, "sign", "--kms-key-id", secret, "--expected-spki-sha256", secret)
	command.Env = append(os.Environ(), "TENCENTCLOUD_SECRET_ID="+secret, "TENCENTCLOUD_SECRET_KEY="+secret)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("CLI accepted direct secret-bearing flags")
	}
	if strings.Contains(string(output), secret) {
		t.Fatalf("CLI output leaked flag or credential value: %q", output)
	}
}

func validCommandArgs(candidate, output, audit string) []string {
	return []string{
		"sign", "--candidate", candidate, "--expected-previous-epoch", "0", "--kms-region", "ap-shanghai",
		"--kms-key-id-env", "REVIEWED_KMS_KEY_ID", "--expected-spki-sha256-env", "REVIEWED_KMS_SPKI_SHA256",
		"--output", output, "--audit-output", audit,
	}
}

func mustReadPrivateFile(t testing.TB, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s permissions = %o, want no group/other access", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type commandSigner struct {
	private *ecdsa.PrivateKey
	public  []byte
	digest  string
}

type ioDiscard struct{}

func (ioDiscard) Write(data []byte) (int, error) { return len(data), nil }

func newCommandSigner(t testing.TB) *commandSigner {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(public)
	return &commandSigner{private: private, public: public, digest: hex.EncodeToString(digest[:])}
}

func (signer *commandSigner) PublicKey(context.Context, string) ([]byte, string, error) {
	return append([]byte(nil), signer.public...), "public-request-id", nil
}

func (signer *commandSigner) SignDigest(_ context.Context, _ string, digest []byte) ([]byte, string, error) {
	signature, err := ecdsa.SignASN1(rand.Reader, signer.private, digest)
	return signature, "sign-request-id", err
}
