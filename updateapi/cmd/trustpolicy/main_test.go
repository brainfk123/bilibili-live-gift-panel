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
	"reflect"
	"runtime"
	"strings"
	"sync"
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

func TestOutputBundleUsesUniquePrivateStagesAndIgnoresQuarantine(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	quarantine := filepath.Join(base, ".bundle.trustpolicy-staging-"+strings.Repeat("f", 64))
	if err := os.Mkdir(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(quarantine, "foreign.txt")
	if err := os.WriteFile(foreign, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	stages := make([]string, 0, 2)
	for range 2 {
		err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
			checkpoint: func(checkpoint bundleCheckpoint, stage string) error {
				if checkpoint == bundleAfterCreateStaging {
					stages = append(stages, stage)
					return errors.New("stop before commit")
				}
				return nil
			},
		})
		if err == nil {
			t.Fatal("injected staging failure was accepted")
		}
	}
	if len(stages) != 2 || samePath(stages[0], stages[1]) {
		t.Fatalf("staging paths are not unique: %v", stages)
	}
	for _, stage := range stages {
		if filepath.Dir(stage) != base || !validStagingBasename(filepath.Base(stage), "bundle") {
			t.Fatal("staging directory is not a same-parent cryptographic name")
		}
		assertPathAbsent(t, stage)
	}
	if err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{}); err != nil {
		t.Fatal(err)
	}
	assertCompleteBundle(t, policyPath, auditPath)
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "preserve" {
		t.Fatalf("quarantined foreign staging changed: %q, %v", got, err)
	}
}

func TestOutputBundleRandomCollisionDoesNotAdoptOrDeleteForeignStage(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	collidingToken := bytes.Repeat([]byte{0xaa}, 32)
	nextToken := bytes.Repeat([]byte{0xbb}, 32)
	foreignStage := filepath.Join(base, ".bundle.trustpolicy-staging-"+hex.EncodeToString(collidingToken))
	if err := os.Mkdir(foreignStage, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignFile := filepath.Join(foreignStage, "foreign.txt")
	if err := os.WriteFile(foreignFile, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stage string
	err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
		entropy: bytes.NewReader(append(collidingToken, nextToken...)),
		checkpoint: func(checkpoint bundleCheckpoint, currentStage string) error {
			if checkpoint == bundleAfterCreateStaging {
				stage = currentStage
				return errors.New("stop")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("injected failure was accepted")
	}
	if !strings.HasSuffix(stage, hex.EncodeToString(nextToken)) {
		t.Fatal("random collision did not advance to a new owner token")
	}
	assertPathAbsent(t, stage)
	if got, err := os.ReadFile(foreignFile); err != nil || string(got) != "preserve" {
		t.Fatalf("colliding foreign staging changed: %q, %v", got, err)
	}
}

func TestOutputBundleInjectedFailuresAreAllOrNothing(t *testing.T) {
	for _, checkpoint := range bundleTestCheckpoints() {
		t.Run(string(checkpoint), func(t *testing.T) {
			base := t.TempDir()
			bundlePath := filepath.Join(base, "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			stage := ""
			reached := false
			err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
				checkpoint: func(got bundleCheckpoint, currentStage string) error {
					stage = currentStage
					if got == checkpoint {
						reached = true
						return errors.New("injected failure")
					}
					return nil
				},
			})
			if err == nil || !reached {
				t.Fatalf("injected checkpoint was not enforced: reached=%v error=%v", reached, err)
			}
			if checkpoint.beforeCommit() {
				assertPathAbsent(t, bundlePath)
				assertPathAbsent(t, stage)
				return
			}
			assertCompleteBundle(t, policyPath, auditPath)
			assertPathAbsent(t, stage)
		})
	}
}

func TestOutputBundleCrashQuarantinesPreCommitStageAndNextRunIgnoresIt(t *testing.T) {
	for _, checkpoint := range bundleTestCheckpoints() {
		t.Run(string(checkpoint), func(t *testing.T) {
			base := t.TempDir()
			bundlePath := filepath.Join(base, "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			runBundleCrashHelper(t, checkpoint, policyPath, auditPath)
			stages := listBundleStages(t, base, "bundle")
			if checkpoint.beforeCommit() {
				assertPathAbsent(t, bundlePath)
				if len(stages) != 1 {
					t.Fatalf("pre-commit crash stages = %v, want one quarantine", stages)
				}
				before := snapshotQuarantine(t, stages[0])
				if err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{}); err != nil {
					t.Fatalf("next invocation was blocked by quarantine: %v", err)
				}
				assertCompleteBundle(t, policyPath, auditPath)
				after := snapshotQuarantine(t, stages[0])
				if !reflect.DeepEqual(before, after) {
					t.Fatal("next invocation adopted or modified quarantined staging")
				}
				return
			}
			assertCompleteBundle(t, policyPath, auditPath)
			if len(stages) != 0 {
				t.Fatalf("post-commit crash left staging: %v", stages)
			}
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
		bundleHooks{checkpoint: func(got bundleCheckpoint, _ string) error {
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

func TestOutputBundleCleanupPreservesReplacedStageOnIdentityMismatch(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	original := ""
	replacement := ""
	err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
		checkpoint: func(checkpoint bundleCheckpoint, stage string) error {
			if checkpoint != bundleAfterWritePolicy {
				return nil
			}
			original = stage + ".quarantine"
			replacement = stage
			if err := os.Rename(stage, original); err != nil {
				return err
			}
			if err := createPrivateDirectory(replacement); err != nil {
				return err
			}
			return errors.New("trigger cleanup")
		},
	})
	if err == nil {
		t.Fatal("replacement race was accepted")
	}
	if info, statErr := os.Lstat(original); statErr != nil || !info.IsDir() {
		t.Fatalf("original moved stage changed: %v", statErr)
	}
	if info, statErr := os.Lstat(replacement); statErr != nil || !info.IsDir() {
		t.Fatalf("foreign replacement stage was removed: %v", statErr)
	}
	assertPathAbsent(t, bundlePath)
}

func TestOutputBundleCleanupRechecksIdentityAfterCleanupRace(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	stage := ""
	replaced := false
	err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
		checkpoint: func(checkpoint bundleCheckpoint, currentStage string) error {
			stage = currentStage
			if checkpoint == bundleAfterWriteAudit {
				return errors.New("trigger cleanup")
			}
			return nil
		},
		beforeCleanupRemove: func(index int) error {
			if index != 0 || replaced {
				return nil
			}
			replaced = true
			if err := os.Rename(stage, stage+".original"); err != nil {
				return err
			}
			if err := createPrivateDirectory(stage); err != nil {
				return err
			}
			if err := writePrivateBundleFile(filepath.Join(stage, "policy.json"), []byte("foreign-policy")); err != nil {
				return err
			}
			return writePrivateBundleFile(filepath.Join(stage, "audit.json"), []byte("foreign-audit"))
		},
	})
	if err == nil || !replaced {
		t.Fatalf("cleanup replacement race was not exercised: replaced=%v error=%v", replaced, err)
	}
	if got, err := os.ReadFile(filepath.Join(stage, "policy.json")); err != nil || string(got) != "foreign-policy" {
		t.Fatalf("cleanup changed foreign policy: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(stage, "audit.json")); err != nil || string(got) != "foreign-audit" {
		t.Fatalf("cleanup changed foreign audit: %q, %v", got, err)
	}
	assertPathAbsent(t, bundlePath)
}

func TestOutputBundleCleanupRequiresMatchingOwnerToken(t *testing.T) {
	base := t.TempDir()
	paths, err := validateOutputBundlePaths(filepath.Join(base, "bundle", "policy.json"), filepath.Join(base, "bundle", "audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var token [stagingTokenBytes]byte
	copy(token[:], bytes.Repeat([]byte{0x42}, stagingTokenBytes))
	stage := stagingPathForToken(paths, token)
	if err := createPrivateDirectory(stage); err != nil {
		t.Fatal(err)
	}
	identity, err := readBundleFileIdentity(stage)
	if err != nil {
		t.Fatal(err)
	}
	claim := stagingClaim{paths: paths, path: stage, token: token, identity: identity}
	claim.token[0] ^= 0xff
	if err := cleanupStagingClaim(claim, bundleHooks{}); err == nil {
		t.Fatal("cleanup accepted a mismatched owner token")
	}
	if info, err := os.Lstat(stage); err != nil || !info.IsDir() {
		t.Fatalf("token-mismatched staging was removed: %v", err)
	}
}

func TestOutputBundleFailureDoesNotExposeStagingIdentity(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	stage := ""
	err := writeOutputBundle(filepath.Join(bundlePath, "policy.json"), []byte("policy-secret"), filepath.Join(bundlePath, "audit.json"), []byte("audit-secret"), bundleHooks{
		checkpoint: func(checkpoint bundleCheckpoint, currentStage string) error {
			stage = currentStage
			if checkpoint == bundleAfterCreateStaging {
				return errors.New("provider detail " + currentStage)
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("injected failure was accepted")
	}
	for _, value := range []string{stage, filepath.Base(stage), "policy-secret", "audit-secret"} {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("bundle error leaked private value: %q", err)
		}
	}
}

func TestOutputBundlePartialCleanupInterruptionLeavesQuarantineAndDoesNotBlockNextRun(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	stage := ""
	err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
		checkpoint: func(checkpoint bundleCheckpoint, currentStage string) error {
			stage = currentStage
			if checkpoint == bundleAfterWriteAudit {
				return errors.New("trigger cleanup")
			}
			return nil
		},
		beforeCleanupRemove: func(index int) error {
			if index == 1 {
				return errors.New("cleanup interrupted")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("partial cleanup interruption was accepted")
	}
	before := snapshotQuarantine(t, stage)
	if err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{}); err != nil {
		t.Fatalf("next invocation was blocked by partial quarantine: %v", err)
	}
	assertCompleteBundle(t, policyPath, auditPath)
	after := snapshotQuarantine(t, stage)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("next invocation modified partial quarantine")
	}
}

func TestOutputBundleConcurrentInvocationsCommitExactlyOneCompletePair(t *testing.T) {
	base := t.TempDir()
	bundlePath := filepath.Join(base, "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	foreignStage := filepath.Join(base, ".bundle.trustpolicy-staging-"+strings.Repeat("f", 64))
	if err := os.Mkdir(foreignStage, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignFile := filepath.Join(foreignStage, "foreign.txt")
	if err := os.WriteFile(foreignFile, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	stages := make(chan string, 2)
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(2)
	for _, pair := range [][2]string{{"policy-a", "audit-a"}, {"policy-b", "audit-b"}} {
		pair := pair
		go func() {
			start.Done()
			start.Wait()
			results <- writeOutputBundle(policyPath, []byte(pair[0]), auditPath, []byte(pair[1]), bundleHooks{
				checkpoint: func(checkpoint bundleCheckpoint, stage string) error {
					if checkpoint == bundleAfterSyncStaging {
						stages <- stage
						ready <- struct{}{}
						<-release
					}
					return nil
				},
			})
		}()
	}
	<-ready
	<-ready
	close(release)
	firstErr := <-results
	secondErr := <-results
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("concurrent results = %v/%v, want exactly one winner", firstErr, secondErr)
	}
	firstStage := <-stages
	secondStage := <-stages
	if samePath(firstStage, secondStage) {
		t.Fatal("concurrent invocations shared a staging directory")
	}
	assertPathAbsent(t, firstStage)
	assertPathAbsent(t, secondStage)
	policy, policyErr := os.ReadFile(policyPath)
	audit, auditErr := os.ReadFile(auditPath)
	if policyErr != nil || auditErr != nil || !((string(policy) == "policy-a" && string(audit) == "audit-a") || (string(policy) == "policy-b" && string(audit) == "audit-b")) {
		t.Fatalf("concurrent winner pair is mixed or incomplete: %q/%q errors=%v/%v", policy, audit, policyErr, auditErr)
	}
	assertOnlyPolicyAuditEntries(t, policyPath, auditPath)
	if got, err := os.ReadFile(foreignFile); err != nil || string(got) != "preserve" {
		t.Fatalf("concurrent loser or winner changed foreign quarantine: %q, %v", got, err)
	}
}

func TestOutputBundleFinalReplacementRacePreservesForeignTarget(t *testing.T) {
	for _, target := range []string{"directory", "symlink"} {
		t.Run(target, func(t *testing.T) {
			base := t.TempDir()
			bundlePath := filepath.Join(base, "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			foreignTarget := filepath.Join(base, "foreign-target")
			if target == "symlink" {
				if err := os.Mkdir(foreignTarget, 0o700); err != nil {
					t.Fatal(err)
				}
				probe := filepath.Join(base, "symlink-probe")
				if err := os.Symlink(foreignTarget, probe); err != nil {
					t.Skip("symlink creation is not permitted")
				}
				if err := os.Remove(probe); err != nil {
					t.Fatal(err)
				}
			}
			stage := ""
			err := writeOutputBundle(policyPath, []byte("policy"), auditPath, []byte("audit"), bundleHooks{
				checkpoint: func(checkpoint bundleCheckpoint, currentStage string) error {
					stage = currentStage
					if checkpoint != bundleAfterSyncStaging {
						return nil
					}
					if target == "directory" {
						return os.Mkdir(bundlePath, 0o700)
					}
					return os.Symlink(foreignTarget, bundlePath)
				},
			})
			if err == nil {
				t.Fatal("no-replace commit overwrote a raced final target")
			}
			assertPathAbsent(t, stage)
			info, statErr := os.Lstat(bundlePath)
			if statErr != nil {
				t.Fatalf("foreign final target was removed: %v", statErr)
			}
			if target == "directory" && !info.IsDir() {
				t.Fatal("foreign empty directory was replaced")
			}
			if target == "symlink" && info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("foreign symlink was replaced")
			}
		})
	}
}

func bundleTestCheckpoints() []bundleCheckpoint {
	return []bundleCheckpoint{
		bundleAfterCreateStaging,
		bundleAfterWritePolicy,
		bundleAfterWriteAudit,
		bundleAfterSyncStaging,
		bundleAfterCommit,
		bundleAfterVerifyFinal,
		bundleAfterSyncParent,
	}
}

func runBundleCrashHelper(t testing.TB, checkpoint bundleCheckpoint, policyPath, auditPath string) {
	t.Helper()
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
}

func validStagingBasename(name, bundleName string) bool {
	prefix := "." + bundleName + ".trustpolicy-staging-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	token := strings.TrimPrefix(name, prefix)
	decoded, err := hex.DecodeString(token)
	return err == nil && len(decoded) == 32 && strings.ToLower(token) == token
}

func listBundleStages(t testing.TB, parent, bundleName string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "." + bundleName + ".trustpolicy-staging-"
	var result []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			result = append(result, filepath.Join(parent, entry.Name()))
		}
	}
	return result
}

type quarantineSnapshot struct {
	identity bundleFileIdentity
	entries  map[string]string
}

func snapshotQuarantine(t testing.TB, path string) quarantineSnapshot {
	t.Helper()
	identity, err := readBundleFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := quarantineSnapshot{identity: identity, entries: make(map[string]string, len(entries))}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			t.Fatalf("quarantine contains unsafe entry %q", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot.entries[entry.Name()] = string(data)
	}
	return snapshot
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
	assertOnlyPolicyAuditEntries(t, policyPath, auditPath)
}

func assertOnlyPolicyAuditEntries(t testing.TB, policyPath, auditPath string) {
	t.Helper()
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
