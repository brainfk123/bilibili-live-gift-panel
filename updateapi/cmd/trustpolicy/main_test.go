//go:build windows || linux

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
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

func TestKMSSignerCLIWritesCreateOnlyPrivateOutputs(t *testing.T) {
	fake := newCommandSigner(t)
	directory := newPrivateTestBase(t)
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

func TestValidateCandidateRequiresExactDeclaredEpochWithoutProviderDependencies(t *testing.T) {
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	for _, test := range []struct {
		name      string
		epoch     string
		previous  string
		wantValid bool
	}{
		{name: "exact transition", epoch: "1", previous: "0", wantValid: true},
		{name: "declared epoch mismatch", epoch: "2", previous: "0"},
		{name: "previous epoch mismatch", epoch: "1", previous: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := run(context.Background(), []string{
				"validate-candidate",
				"--candidate", candidatePath,
				"--candidate-epoch", test.epoch,
				"--expected-previous-epoch", test.previous,
			}, nil, nil, &output, func() time.Time { return time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC) })
			if test.wantValid {
				if err != nil || output.String() != "publisher policy candidate valid\n" {
					t.Fatalf("validate candidate = (%q, %v), want fixed success", output.String(), err)
				}
				return
			}
			if err == nil || output.Len() != 0 {
				t.Fatalf("invalid declared transition = (%q, %v), want silent failure", output.String(), err)
			}
		})
	}
}

func TestKMSSignerCLIRefusesOverwriteBeforeSignerConstruction(t *testing.T) {
	directory := newPrivateTestBase(t)
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
	directory := newPrivateTestBase(t)
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
	directory := newPrivateTestBase(t)
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
	base := newPrivateTestBase(t)
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
			bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
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

func TestKMSSignerCLIRejectsUntrustedWritableParentBeforeFactory(t *testing.T) {
	parent := newPrivateTestBase(t)
	makeCLIParentUntrustedWritable(t, parent)
	bundlePath := filepath.Join(parent, "bundle")
	candidatePath := filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json")
	called := false
	err := run(context.Background(), validCommandArgs(candidatePath, filepath.Join(bundlePath, "policy.json"), filepath.Join(bundlePath, "audit.json")), commandEnvironment(strings.Repeat("a", 64)), func(string, string, string) (trustpolicy.Signer, error) {
		called = true
		return nil, errors.New("must not construct signer")
	}, ioDiscard{}, time.Now)
	if err == nil {
		t.Fatal("run() accepted a parent writable by untrusted principals")
	}
	if called {
		t.Fatal("run() constructed signer before protected-parent preflight")
	}
	assertPathAbsent(t, bundlePath)
}

func TestOutputBundleWritesCommitMarkerLastAndReaderValidates(t *testing.T) {
	policy, audit := testBundlePayload(t, "marker-test")
	bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	var checkpoints []bundleCheckpoint
	if err := writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{
		checkpoint: func(checkpoint bundleCheckpoint, _ string) error {
			checkpoints = append(checkpoints, checkpoint)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	wantOrder := bundleTestCheckpoints()
	if !reflect.DeepEqual(checkpoints, wantOrder) {
		t.Fatalf("checkpoint order = %v, want %v", checkpoints, wantOrder)
	}
	committed, err := readCommittedBundle(policyPath, auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed.Policy, policy) || !bytes.Equal(committed.Audit, audit) {
		t.Fatal("reader did not return the committed pair")
	}
	assertCommittedBundleEntries(t, bundlePath, "policy.json", "audit.json")
}

func TestOutputBundleCheckpointFailuresPreservePhysicalPartialAndLogicalCommit(t *testing.T) {
	for _, checkpoint := range bundleTestCheckpoints() {
		t.Run(string(checkpoint), func(t *testing.T) {
			policy, audit := testBundlePayload(t, string(checkpoint))
			bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			reached := false
			err := writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{
				checkpoint: func(got bundleCheckpoint, _ string) error {
					if got == checkpoint {
						reached = true
						return errors.New("injected failure")
					}
					return nil
				},
			})
			if err == nil || !reached {
				t.Fatalf("checkpoint failure not enforced: reached=%v error=%v", reached, err)
			}
			if _, statErr := os.Lstat(bundlePath); statErr != nil {
				t.Fatalf("owned final directory was deleted: %v", statErr)
			}
			committed, readErr := readCommittedBundle(policyPath, auditPath)
			if checkpoint.markerAbsent() {
				if readErr == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
					t.Fatal("pre-marker physical partial was exposed as committed")
				}
				return
			}
			if readErr != nil || !bytes.Equal(committed.Policy, policy) || !bytes.Equal(committed.Audit, audit) {
				t.Fatalf("post-marker bundle did not validate: %v", readErr)
			}
		})
	}
}

func TestOutputBundleCrashLeavesUncommittedFinalOrValidatedCommit(t *testing.T) {
	for _, checkpoint := range bundleTestCheckpoints() {
		t.Run(string(checkpoint), func(t *testing.T) {
			base := newPrivateTestBase(t)
			bundlePath := filepath.Join(base, "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			runBundleCrashHelper(t, checkpoint, policyPath, auditPath)
			before := snapshotBundlePath(t, bundlePath)
			committed, readErr := readCommittedBundle(policyPath, auditPath)
			if checkpoint.markerAbsent() {
				if readErr == nil || len(committed.Policy) != 0 {
					t.Fatal("pre-marker crash exposed trusted bytes")
				}
				policy, audit := testBundlePayload(t, "same-path-retry")
				if err := writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{}); err == nil {
					t.Fatal("same-path retry reused an uncommitted final directory")
				}
				afterRetry := snapshotBundlePath(t, bundlePath)
				if !reflect.DeepEqual(before, afterRetry) {
					t.Fatal("same-path retry modified physical partial")
				}
				newBundle := filepath.Join(base, "bundle-next")
				newPolicy := filepath.Join(newBundle, "policy.json")
				newAudit := filepath.Join(newBundle, "audit.json")
				if err := writeOutputBundle(newPolicy, policy, newAudit, audit, bundleHooks{}); err != nil {
					t.Fatalf("new final path was blocked by old partial: %v", err)
				}
				if _, err := readCommittedBundle(newPolicy, newAudit); err != nil {
					t.Fatalf("new path did not commit: %v", err)
				}
				if !reflect.DeepEqual(before, snapshotBundlePath(t, bundlePath)) {
					t.Fatal("new-path invocation modified old partial")
				}
				return
			}
			if readErr != nil || len(committed.Policy) == 0 || len(committed.Audit) == 0 {
				t.Fatalf("post-marker crash did not leave a validated commit: %v", readErr)
			}
		})
	}
}

func TestOutputBundleCrashHelper(t *testing.T) {
	if os.Getenv("TRUSTPOLICY_CRASH_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	policy, audit := testBundlePayload(t, "crash-helper")
	checkpoint := bundleCheckpoint(os.Getenv("TRUSTPOLICY_CRASH_CHECKPOINT"))
	err := writeOutputBundle(
		os.Getenv("TRUSTPOLICY_CRASH_POLICY"), policy,
		os.Getenv("TRUSTPOLICY_CRASH_AUDIT"), audit,
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

func TestOutputBundleExistingFinalAlwaysFailsWithoutMutation(t *testing.T) {
	for _, state := range []string{"empty", "partial", "complete", "symlink"} {
		t.Run(state, func(t *testing.T) {
			base := newPrivateTestBase(t)
			bundlePath := filepath.Join(base, "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			policy, audit := testBundlePayload(t, state)
			switch state {
			case "empty":
				if err := bundlefs.CreatePrivateDirectory(bundlePath); err != nil {
					t.Fatal(err)
				}
			case "partial":
				if err := bundlefs.CreatePrivateDirectory(bundlePath); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
					t.Fatal(err)
				}
			case "complete":
				if err := writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{}); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(base, "target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, bundlePath); err != nil {
					t.Skip("symlink creation is not permitted")
				}
			}
			before := snapshotBundlePath(t, bundlePath)
			if err := writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{}); err == nil {
				t.Fatal("existing final directory was reused or replaced")
			}
			if !reflect.DeepEqual(before, snapshotBundlePath(t, bundlePath)) {
				t.Fatal("existing final directory changed")
			}
		})
	}
}

func TestReadCommittedBundleRejectsMarkerContentAndEntryMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, bundlePath, policyPath, auditPath string)
	}{
		{name: "missing marker", mutate: func(t *testing.T, bundlePath, _, _ string) {
			t.Helper()
			mustRemove(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName))
		}},
		{name: "corrupt marker", mutate: func(t *testing.T, bundlePath, _, _ string) {
			t.Helper()
			mustOverwrite(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName), []byte(`{}`))
		}},
		{name: "extra entry", mutate: func(t *testing.T, bundlePath, _, _ string) {
			t.Helper()
			mustOverwrite(t, filepath.Join(bundlePath, "extra.txt"), []byte("extra"))
		}},
		{name: "policy substitution", mutate: func(t *testing.T, _, policyPath, _ string) { t.Helper(); mustOverwrite(t, policyPath, []byte(`{}`)) }},
		{name: "audit substitution", mutate: func(t *testing.T, _, _, auditPath string) { t.Helper(); mustOverwrite(t, auditPath, []byte(`{}`)) }},
		{name: "marker trailing JSON", mutate: func(t *testing.T, bundlePath, _, _ string) {
			t.Helper()
			marker := mustRead(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName))
			mustOverwrite(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName), append(marker, []byte(`{}`)...))
		}},
		{name: "marker unknown field", mutate: func(t *testing.T, bundlePath, _, _ string) {
			t.Helper()
			marker := mustRead(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName))
			mustOverwrite(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName), bytes.Replace(marker, []byte(`{"schemaVersion":1`), []byte(`{"schemaVersion":1,"unknown":true`), 1))
		}},
		{name: "marker duplicate field", mutate: func(t *testing.T, bundlePath, _, _ string) {
			t.Helper()
			marker := mustRead(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName))
			mustOverwrite(t, filepath.Join(bundlePath, trustpolicy.BundleCommitFileName), bytes.Replace(marker, []byte(`{"schemaVersion":1`), []byte(`{"schemaVersion":1,"schemaVersion":1`), 1))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, audit := testBundlePayload(t, test.name)
			bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			if err := writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{}); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, bundlePath, policyPath, auditPath)
			committed, err := readCommittedBundle(policyPath, auditPath)
			if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
				t.Fatal("mutated bundle exposed trusted bytes")
			}
			if err.Error() != errCommand.Error() {
				t.Fatalf("reader error leaked detail: %q", err)
			}
		})
	}
}

func TestReadCommittedBundleRejectsSelfConsistentMalformedPolicyOrAudit(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy []byte
		audit  []byte
	}{
		{name: "malformed policy", policy: []byte(`{}`), audit: []byte(`{}`)},
		{name: "malformed audit", policy: testBundlePayloadPolicy(t), audit: []byte(`{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
			policyPath := filepath.Join(bundlePath, "policy.json")
			auditPath := filepath.Join(bundlePath, "audit.json")
			if err := writeOutputBundle(policyPath, test.policy, auditPath, test.audit, bundleHooks{}); err != nil {
				t.Fatal(err)
			}
			if _, err := readCommittedBundle(policyPath, auditPath); err == nil {
				t.Fatal("self-consistent malformed committed files were accepted")
			}
		})
	}
}

func TestReadCommittedBundleRejectsSymlinkSubstitution(t *testing.T) {
	policy, audit := testBundlePayload(t, "symlink-substitution")
	bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	if err := writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{}); err != nil {
		t.Fatal(err)
	}
	relocated := filepath.Join(filepath.Dir(bundlePath), "relocated-policy.json")
	if err := os.Rename(policyPath, relocated); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relocated, policyPath); err != nil {
		t.Skip("symlink creation is not permitted")
	}
	committed, err := readCommittedBundle(policyPath, auditPath)
	if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
		t.Fatal("symlink-substituted policy was exposed")
	}
}

func TestOutputBundleConcurrentSamePathHasOneOwnerWithoutDeadlock(t *testing.T) {
	policyA, auditA := testBundlePayload(t, "concurrent-a")
	policyB, auditB := testBundlePayload(t, "concurrent-b")
	bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
	policyPath := filepath.Join(bundlePath, "policy.json")
	auditPath := filepath.Join(bundlePath, "audit.json")
	start := make(chan struct{})
	ownerCreated := make(chan struct{}, 1)
	releaseOwner := make(chan struct{})
	var releaseOwnerOnce sync.Once
	releaseOwnerNow := func() { releaseOwnerOnce.Do(func() { close(releaseOwner) }) }
	defer releaseOwnerNow()
	results := make(chan error, 2)
	for _, pair := range [][2][]byte{{policyA, auditA}, {policyB, auditB}} {
		pair := pair
		go func() {
			<-start
			results <- writeOutputBundle(policyPath, pair[0], auditPath, pair[1], bundleHooks{
				checkpoint: func(checkpoint bundleCheckpoint, _ string) error {
					if checkpoint == bundleAfterCreateDirectory {
						ownerCreated <- struct{}{}
						<-releaseOwner
					}
					return nil
				},
			})
		}()
	}
	close(start)
	waitSignal(t, ownerCreated, "winner did not acquire directory")
	loserErr := waitResult(t, results, "loser did not return while winner was paused")
	if loserErr == nil {
		t.Fatal("concurrent loser unexpectedly succeeded")
	}
	releaseOwnerNow()
	if winnerErr := waitResult(t, results, "winner did not finish"); winnerErr != nil {
		t.Fatalf("concurrent winner failed: %v", winnerErr)
	}
	committed, err := readCommittedBundle(policyPath, auditPath)
	if err != nil || !((bytes.Equal(committed.Policy, policyA) && bytes.Equal(committed.Audit, auditA)) || (bytes.Equal(committed.Policy, policyB) && bytes.Equal(committed.Audit, auditB))) {
		t.Fatalf("concurrent final is missing or mixed: %v", err)
	}
}

func TestOutputBundleConcurrentDifferentPathsProceedIndependently(t *testing.T) {
	base := newPrivateTestBase(t)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseNow()
	results := make(chan error, 2)
	paths := make([][2]string, 0, 2)
	for _, name := range []string{"bundle-a", "bundle-b"} {
		bundlePath := filepath.Join(base, name)
		policyPath := filepath.Join(bundlePath, "policy.json")
		auditPath := filepath.Join(bundlePath, "audit.json")
		paths = append(paths, [2]string{policyPath, auditPath})
		policy, audit := testBundlePayload(t, name)
		go func() {
			results <- writeOutputBundle(policyPath, policy, auditPath, audit, bundleHooks{
				checkpoint: func(checkpoint bundleCheckpoint, _ string) error {
					if checkpoint == bundleAfterCreateDirectory {
						ready <- struct{}{}
						<-release
					}
					return nil
				},
			})
		}()
	}
	waitSignal(t, ready, "first independent owner not ready")
	waitSignal(t, ready, "second independent owner not ready")
	releaseNow()
	if err := waitResult(t, results, "first independent result missing"); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, results, "second independent result missing"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range paths {
		if _, err := readCommittedBundle(pair[0], pair[1]); err != nil {
			t.Fatalf("independent path did not commit: %v", err)
		}
	}
}

func bundleTestCheckpoints() []bundleCheckpoint {
	return []bundleCheckpoint{
		bundleAfterCreateDirectory,
		bundleAfterWritePolicy,
		bundleAfterWriteAudit,
		bundleAfterSyncDataDirectory,
		bundleAfterSyncParent,
		bundleAfterWriteCommitMarker,
		bundleAfterSyncCommittedDirectory,
	}
}

func TestOutputBundleOnlyFinalDirectorySyncCheckpointIsDurablyCommitted(t *testing.T) {
	for _, checkpoint := range bundleTestCheckpoints() {
		want := checkpoint == bundleAfterSyncCommittedDirectory
		if checkpoint.durablyCommitted() != want {
			t.Fatalf("checkpoint %q durable=%v, want %v", checkpoint, checkpoint.durablyCommitted(), want)
		}
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

type bundlePathSnapshot struct {
	mode    os.FileMode
	target  string
	entries map[string]string
}

func snapshotBundlePath(t testing.TB, path string) bundlePathSnapshot {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := bundlePathSnapshot{mode: info.Mode(), entries: make(map[string]string)}
	if info.Mode()&os.ModeSymlink != 0 {
		snapshot.target, err = os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot.entries[entry.Name()] = string(data)
	}
	return snapshot
}

func testBundlePayload(t testing.TB, label string) ([]byte, []byte) {
	t.Helper()
	return testBundlePayloadWithSigner(t, label, newCommandSigner(t))
}

func testBundlePayloadWithSigner(t testing.TB, label string, signer *commandSigner) ([]byte, []byte) {
	t.Helper()
	candidateBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := trustpolicy.ParseCandidate(candidateBytes, trustpolicy.CandidateOptions{
		ExpectedPreviousEpoch: 0,
		Now:                   time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	labelDigest := sha256.Sum256([]byte(label))
	signed, audit, err := trustpolicy.Sign(context.Background(), signer, candidate, trustpolicy.SignOptions{
		KeyID:                 "kms-key-id",
		ExpectedPreviousEpoch: 0,
		ExpectedSPKISHA256:    signer.digest,
		Now:                   time.Date(2029, 1, 2, 3, 4, 5, 0, time.UTC),
		CIActor:               "bundle-" + hex.EncodeToString(labelDigest[:4]),
	})
	if err != nil {
		t.Fatal(err)
	}
	auditBytes, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	return signed.Policy, auditBytes
}

func testBundlePayloadPolicy(t testing.TB) []byte {
	t.Helper()
	policy, _ := testBundlePayload(t, "policy-only")
	return policy
}

func assertCommittedBundleEntries(t testing.TB, bundlePath, policyName, auditName string) {
	t.Helper()
	entries, err := os.ReadDir(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{policyName: true, auditName: true, trustpolicy.BundleCommitFileName: true}
	if len(entries) != len(want) {
		t.Fatalf("bundle entries = %v, want exact committed triplet", entries)
	}
	for _, entry := range entries {
		if !want[entry.Name()] || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			t.Fatalf("unexpected committed entry: %v", entry)
		}
	}
}

func mustRead(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustOverwrite(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRemove(t testing.TB, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func waitSignal(t testing.TB, channel <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func waitResult(t testing.TB, channel <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal(message)
		return errCommand
	}
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

func newPrivateTestBase(t testing.TB) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "private-parent")
	if err := bundlefs.CreatePrivateDirectory(base); err != nil {
		t.Fatal(err)
	}
	return base
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
				bundlePath := filepath.Join(newPrivateTestBase(t), "bundle")
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
