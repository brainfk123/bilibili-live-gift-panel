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
	bundlePath := filepath.Join(directory, "signed-policy-bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	environment := map[string]string{
		"REVIEWED_KMS_KEY_ID":          "kms-key-id",
		"REVIEWED_KMS_SPKI_SHA256":     fake.digest,
		"GIFT_PANEL_KMS_PROVIDER_MODE": "environment-session",
		"GITHUB_ACTOR":                 "release-approver",
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
	}, func(region, expectedDigest, providerMode string) (trustpolicy.Signer, error) {
		factoryCalls++
		if region != "ap-shanghai" || expectedDigest != fake.digest || providerMode != "environment-session" {
			t.Fatalf("factory input = %q/%q/%q", region, expectedDigest, providerMode)
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
	bundlePath := filepath.Join(directory, "signed-policy-bundle")
	if err := os.Mkdir(bundlePath, 0o700); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	if err := os.WriteFile(policyPath, []byte("preserve-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	called := false
	err := run(context.Background(), validCommandArgs(candidatePath, policyPath, auditPath), func(name string) (string, bool) {
		values := map[string]string{"REVIEWED_KMS_KEY_ID": "kms-key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GIFT_PANEL_KMS_PROVIDER_MODE": "environment-session", "GITHUB_ACTOR": "release-approver"}
		value, ok := values[name]
		return value, ok
	}, func(string, string, string) (trustpolicy.Signer, error) {
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
	bundlePath := filepath.Join(directory, "signed-policy-bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	const secret = "credential-secret-from-provider"
	var output bytes.Buffer
	err := run(context.Background(), validCommandArgs(candidatePath, policyPath, auditPath), func(name string) (string, bool) {
		values := map[string]string{"REVIEWED_KMS_KEY_ID": "kms-key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GIFT_PANEL_KMS_PROVIDER_MODE": "environment-session", "GITHUB_ACTOR": "release-approver"}
		value, ok := values[name]
		return value, ok
	}, func(string, string, string) (trustpolicy.Signer, error) {
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
		{name: "unsafe key ID", values: map[string]string{"REVIEWED_KMS_KEY_ID": " key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GIFT_PANEL_KMS_PROVIDER_MODE": "environment-session", "GITHUB_ACTOR": "release-approver"}},
		{name: "unsafe digest", values: map[string]string{"REVIEWED_KMS_KEY_ID": "key-id", "REVIEWED_KMS_SPKI_SHA256": "not-a-digest", "GIFT_PANEL_KMS_PROVIDER_MODE": "environment-session", "GITHUB_ACTOR": "release-approver"}},
		{name: "unsafe actor", values: map[string]string{"REVIEWED_KMS_KEY_ID": "key-id", "REVIEWED_KMS_SPKI_SHA256": strings.Repeat("a", 64), "GIFT_PANEL_KMS_PROVIDER_MODE": "environment-session", "GITHUB_ACTOR": "actor\nsecret"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			bundlePath := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+"-bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			err := run(context.Background(), validCommandArgs(candidatePath, policyPath, auditPath), func(name string) (string, bool) {
				value, ok := test.values[name]
				return value, ok
			}, func(string, string, string) (trustpolicy.Signer, error) {
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

func TestKMSSignerCLIRejectsCrossParentExistingParentAndSymlinkBeforeSignerConstruction(t *testing.T) {
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	base := t.TempDir()
	existingBundle := filepath.Join(base, "existing-bundle")
	if err := os.Mkdir(existingBundle, 0o700); err != nil {
		t.Fatal(err)
	}
	otherBundle := filepath.Join(base, "other-bundle")
	if err := os.Mkdir(otherBundle, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		output string
		audit  string
	}{
		{name: "existing final parent", output: filepath.Join(existingBundle, "policy.json"), audit: filepath.Join(existingBundle, "audit.json")},
		{name: "cross parent", output: filepath.Join(existingBundle, "policy.json"), audit: filepath.Join(otherBundle, "audit.json")},
	}
	alias := filepath.Join(base, "bundle-alias")
	if err := os.Symlink(existingBundle, alias); err == nil {
		tests = append(tests, struct {
			name   string
			output string
			audit  string
		}{name: "symlink parent", output: filepath.Join(alias, "policy.json"), audit: filepath.Join(alias, "audit.json")})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := run(context.Background(), validCommandArgs(candidatePath, test.output, test.audit), commandEnvironment(strings.Repeat("a", 64)), func(string, string, string) (trustpolicy.Signer, error) {
				called = true
				return nil, errors.New("must not construct signer")
			}, ioDiscard{}, time.Now)
			if err == nil {
				t.Fatal("run() error = nil, want dedicated bundle path rejection")
			}
			if called {
				t.Fatal("run() constructed signer before bundle path rejection")
			}
		})
	}
}

func TestKMSSignerCLIRequiresExplicitProviderModeBeforeFactory(t *testing.T) {
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	for _, mode := range []string{"", "ambient", "tke-oidc-with-fallback"} {
		name := mode
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			bundlePath := filepath.Join(t.TempDir(), "bundle")
			values := map[string]string{
				"REVIEWED_KMS_KEY_ID":          "kms-key-id",
				"REVIEWED_KMS_SPKI_SHA256":     strings.Repeat("a", 64),
				"GIFT_PANEL_KMS_PROVIDER_MODE": mode,
				"GITHUB_ACTOR":                 "release-approver",
			}
			called := false
			err := run(context.Background(), validCommandArgs(candidatePath, filepath.Join(bundlePath, "policy.json"), filepath.Join(bundlePath, "audit.json")), func(name string) (string, bool) {
				value, ok := values[name]
				return value, ok
			}, func(string, string, string) (trustpolicy.Signer, error) {
				called = true
				return nil, errors.New("must not construct signer")
			}, ioDiscard{}, time.Now)
			if err == nil {
				t.Fatal("run() accepted missing or unknown provider mode")
			}
			if called {
				t.Fatal("run() constructed signer before provider mode validation")
			}
		})
	}
}

func TestOutputBundleInjectedFailuresAreAllOrNothing(t *testing.T) {
	checkpoints := []bundleCheckpoint{
		bundleAfterRecovery,
		bundleAfterCreateStaging,
		bundleAfterWriteMarker,
		bundleAfterWritePolicy,
		bundleAfterWriteAudit,
		bundleAfterRemoveMarker,
		bundleAfterSyncStaging,
		bundleAfterRename,
		bundleAfterVerifyFinal,
		bundleAfterSyncParent,
	}
	for _, checkpoint := range checkpoints {
		t.Run(string(checkpoint), func(t *testing.T) {
			base := t.TempDir()
			bundlePath := filepath.Join(base, "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			reached := false
			err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
				checkpoint: func(got bundleCheckpoint) error {
					if got == checkpoint {
						reached = true
						return errors.New("injected failure")
					}
					return nil
				},
			})
			if err == nil {
				t.Fatal("writeOutputBundle() error = nil, want injected failure")
			}
			if !reached {
				t.Fatal("writeOutputBundle() failed before injected checkpoint")
			}
			if checkpoint.beforeRename() {
				assertPathAbsent(t, bundlePath)
				assertPathAbsent(t, stagingPathFor(bundlePath))
				return
			}
			assertCompleteBundle(t, policyPath, auditPath)
			assertPathAbsent(t, stagingPathFor(bundlePath))
		})
	}
}

func TestOutputBundleCrashLeavesRecoverableOwnStagingOrCompleteFinal(t *testing.T) {
	checkpoints := []bundleCheckpoint{
		bundleAfterRecovery,
		bundleAfterCreateStaging,
		bundleAfterWriteMarker,
		bundleAfterWritePolicy,
		bundleAfterWriteAudit,
		bundleAfterRemoveMarker,
		bundleAfterSyncStaging,
		bundleAfterRename,
		bundleAfterVerifyFinal,
		bundleAfterSyncParent,
	}
	for _, checkpoint := range checkpoints {
		t.Run(string(checkpoint), func(t *testing.T) {
			base := t.TempDir()
			bundlePath := filepath.Join(base, "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			command := exec.Command(os.Args[0], "-test.run=^TestOutputBundleCrashHelper$")
			command.Env = append(os.Environ(),
				"TRUSTPOLICY_CRASH_HELPER=1",
				"TRUSTPOLICY_CRASH_CHECKPOINT="+string(checkpoint),
				"TRUSTPOLICY_CRASH_POLICY="+policyPath,
				"TRUSTPOLICY_CRASH_AUDIT="+auditPath,
			)
			if err := command.Run(); err == nil {
				t.Fatal("crash helper exited successfully")
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 86 {
				t.Fatalf("crash helper failed before checkpoint: %v", err)
			}
			if checkpoint == bundleAfterRecovery {
				assertPathAbsent(t, bundlePath)
				assertPathAbsent(t, stagingPathFor(bundlePath))
				if err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{}); err != nil {
					t.Fatalf("next invocation after pre-create crash failed: %v", err)
				}
				assertCompleteBundle(t, policyPath, auditPath)
				return
			}
			if checkpoint.beforeRename() {
				assertPathAbsent(t, bundlePath)
				if _, err := os.Lstat(stagingPathFor(bundlePath)); err != nil {
					t.Fatalf("stale staging missing after crash: %v", err)
				}
				if err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{}); err != nil {
					t.Fatalf("next invocation did not recover owned staging: %v", err)
				}
				assertCompleteBundle(t, policyPath, auditPath)
				return
			}
			assertCompleteBundle(t, policyPath, auditPath)
			if err := writeOutputBundle(policyPath, []byte("replacement"), auditPath, []byte("replacement"), bundleHooks{}); err == nil {
				t.Fatal("next invocation overwrote complete final bundle")
			}
			assertCompleteBundle(t, policyPath, auditPath)
		})
	}
}

func TestOutputBundleCrashHelper(t *testing.T) {
	if os.Getenv("TRUSTPOLICY_CRASH_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	checkpoint := bundleCheckpoint(os.Getenv("TRUSTPOLICY_CRASH_CHECKPOINT"))
	err := writeOutputBundle(
		os.Getenv("TRUSTPOLICY_CRASH_POLICY"), []byte("policy"),
		os.Getenv("TRUSTPOLICY_CRASH_AUDIT"), []byte("audit"),
		bundleHooks{checkpoint: func(got bundleCheckpoint) error {
			if got == checkpoint {
				os.Exit(86)
			}
			return nil
		}},
	)
	if err != nil {
		os.Exit(87)
	}
	os.Exit(0)
}

func TestOutputBundleNeverRemovesUnknownStaging(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	staging := stagingPathFor(bundlePath)
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(staging, "belongs-to-user.txt")
	if err := os.WriteFile(unknown, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{}); err == nil {
		t.Fatal("writeOutputBundle() removed or reused unknown staging")
	}
	got, err := os.ReadFile(unknown)
	if err != nil || string(got) != "preserve" {
		t.Fatalf("unknown staging content changed: %q, %v", got, err)
	}
	assertPathAbsent(t, bundlePath)
}

func TestOutputBundleDoesNotRemoveRacedStagingItDidNotCreate(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	staging := stagingPathFor(bundlePath)
	err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
		checkpoint: func(checkpoint bundleCheckpoint) error {
			if checkpoint == bundleAfterRecovery {
				return os.Mkdir(staging, 0o700)
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("writeOutputBundle() accepted staging created by a racing invocation")
	}
	if info, statErr := os.Lstat(staging); statErr != nil || !info.IsDir() {
		t.Fatalf("raced staging was removed or changed: %v", statErr)
	}
	assertPathAbsent(t, bundlePath)
}

func commandEnvironment(digest string) environmentLookup {
	return func(name string) (string, bool) {
		values := map[string]string{
			"REVIEWED_KMS_KEY_ID":          "kms-key-id",
			"REVIEWED_KMS_SPKI_SHA256":     digest,
			"GIFT_PANEL_KMS_PROVIDER_MODE": "environment-session",
			"GITHUB_ACTOR":                 "release-approver",
		}
		value, ok := values[name]
		return value, ok
	}
}

func assertPathAbsent(t testing.TB, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q exists or could not be checked: %v", path, err)
	}
}

func assertCompleteBundle(t testing.TB, policyPath, auditPath string) {
	t.Helper()
	policy, policyErr := os.ReadFile(policyPath)
	audit, auditErr := os.ReadFile(auditPath)
	if policyErr != nil || auditErr != nil || string(policy) != "policy" || string(audit) != "audit" {
		t.Fatalf("bundle is not complete: policy=%q/%v audit=%q/%v", policy, policyErr, audit, auditErr)
	}
	entries, err := os.ReadDir(filepath.Dir(policyPath))
	if err != nil || len(entries) != 2 || entries[0].Name() != filepath.Base(auditPath) || entries[1].Name() != filepath.Base(policyPath) {
		t.Fatalf("bundle contains entries beyond the policy/audit pair: entries=%v error=%v", entries, err)
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

func TestKMSSignerCLIRejectsEveryRepeatedRegisteredFlagBeforeFactory(t *testing.T) {
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	for _, flagName := range []string{
		"candidate",
		"expected-previous-epoch",
		"kms-region",
		"kms-key-id-env",
		"expected-spki-sha256-env",
		"output",
		"audit-output",
	} {
		for _, firstEquals := range []bool{false, true} {
			form := "separated-then-equals"
			if firstEquals {
				form = "equals-then-separated"
			}
			t.Run(flagName+"/"+form, func(t *testing.T) {
				fake := newCommandSigner(t)
				bundlePath := filepath.Join(t.TempDir(), "bundle")
				policyPath := filepath.Join(bundlePath, "policy.json")
				auditPath := filepath.Join(bundlePath, "audit.json")
				args := validCommandArgs(candidatePath, policyPath, auditPath)
				value := registeredFlagValue(args, flagName)
				args = repeatRegisteredFlag(args, flagName, value, firstEquals)
				called := false
				var output bytes.Buffer
				err := run(context.Background(), args, commandEnvironment(fake.digest), func(string, string, string) (trustpolicy.Signer, error) {
					called = true
					return fake, nil
				}, &output, func() time.Time { return time.Date(2029, 1, 2, 3, 4, 5, 0, time.UTC) })
				if err == nil {
					t.Fatal("run() accepted repeated registered flag")
				}
				if called {
					t.Fatal("run() constructed signer before rejecting repeated flag")
				}
				if output.Len() != 0 {
					t.Fatalf("run() logged repeated flag value: %q", output.String())
				}
				assertPathAbsent(t, bundlePath)
			})
		}
	}
}

func TestKMSSignerCLIRepeatedFlagSubprocessDoesNotEchoValue(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "trustpolicy.exe")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	const secret = "repeated-value-must-not-appear"
	command := exec.Command(binary, "sign", "--candidate", secret, "--candidate="+secret)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("CLI accepted repeated candidate flag")
	}
	if strings.Contains(string(output), secret) {
		t.Fatalf("CLI output leaked repeated flag value: %q", output)
	}
}

func validCommandArgs(candidate, output, audit string) []string {
	return []string{
		"sign", "--candidate", candidate, "--expected-previous-epoch", "0", "--kms-region", "ap-shanghai",
		"--kms-key-id-env", "REVIEWED_KMS_KEY_ID", "--expected-spki-sha256-env", "REVIEWED_KMS_SPKI_SHA256",
		"--output", output, "--audit-output", audit,
	}
}

func registeredFlagValue(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--"+name {
			return args[index+1]
		}
	}
	return ""
}

func repeatRegisteredFlag(args []string, name, value string, firstEquals bool) []string {
	result := append([]string(nil), args...)
	if firstEquals {
		for index := 0; index+1 < len(result); index++ {
			if result[index] == "--"+name {
				result[index] = "--" + name + "=" + result[index+1]
				result = append(result[:index+1], result[index+2:]...)
				break
			}
		}
		return append(result, "--"+name, value)
	}
	return append(result, "--"+name+"="+value)
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
