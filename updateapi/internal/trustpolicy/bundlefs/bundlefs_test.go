package bundlefs

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
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

func TestWriteCommittedBundleBindsParentBundleAndFilesToRetainedObjects(t *testing.T) {
	for _, test := range []struct {
		name       string
		checkpoint writeCheckpoint
		replace    func(t *testing.T, paths testPaths) bool
		assert     func(t *testing.T, paths testPaths)
	}{
		{
			name:       "parent replacement after acquisition",
			checkpoint: writeAfterParentAcquired,
			replace: func(t *testing.T, paths testPaths) bool {
				if !tryRename(t, paths.parent, paths.relocatedParent) {
					return false
				}
				mustCreatePrivateDirectory(t, paths.parent)
				return true
			},
			assert: func(t *testing.T, paths testPaths) {
				assertDirectoryEmpty(t, paths.parent)
				assertPathAbsent(t, filepath.Join(paths.parent, "bundle", trustpolicy.BundleCommitFileName))
			},
		},
		{
			name:       "bundle replacement after identity check",
			checkpoint: writeAfterDirectoryAcquired,
			replace: func(t *testing.T, paths testPaths) bool {
				if !tryRename(t, paths.bundle, paths.relocatedBundle) {
					return false
				}
				mustCreatePrivateDirectory(t, paths.bundle)
				return true
			},
			assert: func(t *testing.T, paths testPaths) {
				assertDirectoryEmpty(t, paths.bundle)
				assertPathAbsent(t, filepath.Join(paths.bundle, trustpolicy.BundleCommitFileName))
			},
		},
		{
			name:       "policy replacement after retained open",
			checkpoint: writeAfterPolicyOpened,
			replace: func(t *testing.T, paths testPaths) bool {
				if !tryRename(t, paths.policy, paths.relocatedPolicy) {
					return false
				}
				mustWriteFile(t, paths.policy, []byte("foreign-policy"))
				return true
			},
			assert: func(t *testing.T, paths testPaths) {
				if got := mustReadFile(t, paths.policy); !bytes.Equal(got, []byte("foreign-policy")) {
					t.Fatalf("foreign policy was modified: %q", got)
				}
				assertPathAbsent(t, filepath.Join(paths.bundle, trustpolicy.BundleCommitFileName))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := newTestPaths(t)
			policy, audit := testBundlePayload(t)
			reached := false
			replaced := false
			err := writeCommittedBundle(paths.policy, policy, paths.audit, audit, writeHooks{
				checkpoint: func(got writeCheckpoint) error {
					if got == test.checkpoint {
						reached = true
						replaced = test.replace(t, paths)
					}
					return nil
				},
			})
			if !reached {
				t.Fatal("replacement gap was not reached")
			}
			if !replaced {
				if err != nil {
					t.Fatalf("blocked replacement damaged transaction: %v", err)
				}
				if _, readErr := ReadCommittedBundle(paths.policy, paths.audit); readErr != nil {
					t.Fatalf("blocked replacement did not leave a readable commit: %v", readErr)
				}
				return
			}
			if err == nil {
				t.Fatal("completed replacement race was accepted")
			}
			test.assert(t, paths)
		})
	}
}

func TestReadCommittedBundleReturnsZeroBytesAcrossReplacementGaps(t *testing.T) {
	for _, test := range []struct {
		name       string
		checkpoint readCheckpoint
		replace    func(t *testing.T, paths testPaths) bool
	}{
		{
			name:       "parent replacement after acquisition",
			checkpoint: readAfterParentAcquired,
			replace: func(t *testing.T, paths testPaths) bool {
				if !tryRename(t, paths.parent, paths.relocatedParent) {
					return false
				}
				mustCreatePrivateDirectory(t, paths.parent)
				copyCommittedBundle(t, filepath.Join(paths.relocatedParent, "bundle"), filepath.Join(paths.parent, "bundle"))
				return true
			},
		},
		{
			name:       "bundle replacement after identity check",
			checkpoint: readAfterDirectoryAcquired,
			replace: func(t *testing.T, paths testPaths) bool {
				if !tryRename(t, paths.bundle, paths.relocatedBundle) {
					return false
				}
				copyCommittedBundle(t, paths.relocatedBundle, paths.bundle)
				return true
			},
		},
		{
			name:       "policy replacement after entry stat",
			checkpoint: readAfterPolicyStat,
			replace: func(t *testing.T, paths testPaths) bool {
				data := mustReadFile(t, paths.policy)
				if !tryRename(t, paths.policy, paths.relocatedPolicy) {
					return false
				}
				mustWriteFile(t, paths.policy, data)
				return true
			},
		},
		{
			name:       "policy replacement after retained open",
			checkpoint: readAfterPolicyOpened,
			replace: func(t *testing.T, paths testPaths) bool {
				data := mustReadFile(t, paths.policy)
				if !tryRename(t, paths.policy, paths.relocatedPolicy) {
					return false
				}
				mustWriteFile(t, paths.policy, data)
				return true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := newTestPaths(t)
			policy, audit := testBundlePayload(t)
			if err := WriteCommittedBundle(paths.policy, policy, paths.audit, audit); err != nil {
				t.Fatal(err)
			}
			reached := false
			replaced := false
			committed, err := readCommittedBundle(paths.policy, paths.audit, readHooks{
				checkpoint: func(got readCheckpoint) error {
					if got == test.checkpoint {
						reached = true
						replaced = test.replace(t, paths)
					}
					return nil
				},
			})
			if !reached {
				t.Fatal("replacement gap was not reached")
			}
			if !replaced {
				if err != nil || !bytes.Equal(committed.Policy, policy) || !bytes.Equal(committed.Audit, audit) {
					t.Fatalf("blocked replacement damaged read: %v", err)
				}
				return
			}
			if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
				t.Fatalf("replacement race exposed trusted bytes: replaced=%v policy=%d audit=%d error=%v", replaced, len(committed.Policy), len(committed.Audit), err)
			}
		})
	}
}

func TestWriteCommittedBundleDurabilityOrder(t *testing.T) {
	paths := newTestPaths(t)
	policy, audit := testBundlePayload(t)
	var got []writeCheckpoint
	var syncs []directorySyncTarget
	if err := writeCommittedBundle(paths.policy, policy, paths.audit, audit, writeHooks{
		checkpoint: func(checkpoint writeCheckpoint) error {
			got = append(got, checkpoint)
			return nil
		},
		beforeDirectorySync: func(target directorySyncTarget) error {
			syncs = append(syncs, target)
			return nil
		},
	}); err != nil {
		t.Fatalf("write failed after checkpoints %v and syncs %v: %v", got, syncs, err)
	}
	want := []writeCheckpoint{
		writeAfterParentAcquired,
		writeAfterDirectoryAcquired,
		writeAfterPolicyOpened,
		writeAfterPolicySynced,
		writeAfterAuditOpened,
		writeAfterAuditSynced,
		writeAfterDataDirectorySynced,
		writeAfterParentSynced,
		writeAfterCommitOpened,
		writeAfterCommitSynced,
		writeAfterCommittedDirectorySynced,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durability order = %v, want %v", got, want)
	}
}

func TestWriteThenReadCommittedBundle(t *testing.T) {
	paths := newTestPaths(t)
	policy, audit := testBundlePayload(t)
	marker, markerErr := trustpolicy.BuildBundleCommit("policy.json", policy, "audit.json", audit)
	if markerErr != nil {
		t.Fatal(markerErr)
	}
	if _, validationErr := trustpolicy.ValidateCommittedBundle("policy.json", policy, "audit.json", audit, marker); validationErr != nil {
		t.Fatalf("test fixture is invalid: %v", validationErr)
	}
	if err := WriteCommittedBundle(paths.policy, policy, paths.audit, audit); err != nil {
		t.Fatal(err)
	}
	var checkpoints []readCheckpoint
	committed, err := readCommittedBundle(paths.policy, paths.audit, readHooks{checkpoint: func(checkpoint readCheckpoint) error {
		checkpoints = append(checkpoints, checkpoint)
		return nil
	}})
	if err != nil {
		t.Fatalf("read failed after checkpoints %v: %v", checkpoints, err)
	}
	if !bytes.Equal(committed.Policy, policy) || !bytes.Equal(committed.Audit, audit) {
		t.Fatal("round trip returned different bytes")
	}
}

func TestReadCommittedBundleRejectsArtifactPrivacyChange(t *testing.T) {
	paths := newTestPaths(t)
	policy, audit := testBundlePayload(t)
	if err := WriteCommittedBundle(paths.policy, policy, paths.audit, audit); err != nil {
		t.Fatal(err)
	}
	makeFileNonPrivate(t, paths.policy)
	committed, err := ReadCommittedBundle(paths.policy, paths.audit)
	if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
		t.Fatal("reader exposed bytes from a bundle with non-private artifact permissions")
	}
}

func TestWriteCommittedBundleNeverCreatesMarkerWhenRequiredPreMarkerSyncFails(t *testing.T) {
	for _, target := range []directorySyncTarget{syncDataDirectory, syncParentDirectory} {
		t.Run(string(target), func(t *testing.T) {
			paths := newTestPaths(t)
			policy, audit := testBundlePayload(t)
			err := writeCommittedBundle(paths.policy, policy, paths.audit, audit, writeHooks{
				beforeDirectorySync: func(got directorySyncTarget) error {
					if got == target {
						return errors.New("injected required durability failure")
					}
					return nil
				},
			})
			if err == nil {
				t.Fatal("required pre-marker durability failure was ignored")
			}
			assertPathAbsent(t, filepath.Join(paths.bundle, trustpolicy.BundleCommitFileName))
			committed, readErr := ReadCommittedBundle(paths.policy, paths.audit)
			if readErr == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
				t.Fatal("pre-marker durability failure exposed a committed bundle")
			}
		})
	}
}

func TestWriteCommittedBundleRejectsUntrustedWritableParentBeforeDirectoryCreation(t *testing.T) {
	base := t.TempDir()
	makeDirectoryUntrustedWritable(t, base)
	bundle := filepath.Join(base, "bundle")
	policy, audit := testBundlePayload(t)
	if err := WriteCommittedBundle(filepath.Join(bundle, "policy.json"), policy, filepath.Join(bundle, "audit.json"), audit); err == nil {
		t.Fatal("writer accepted a parent writable by untrusted principals")
	}
	assertPathAbsent(t, bundle)
}

func newTestPaths(t *testing.T) testPaths {
	t.Helper()
	outer := t.TempDir()
	parent := filepath.Join(outer, "private-parent")
	mustCreatePrivateDirectory(t, parent)
	bundle := filepath.Join(parent, "bundle")
	return testPaths{
		parent:          parent,
		bundle:          bundle,
		policy:          filepath.Join(bundle, "policy.json"),
		audit:           filepath.Join(bundle, "audit.json"),
		relocatedParent: filepath.Join(outer, "relocated-parent"),
		relocatedBundle: filepath.Join(parent, "relocated-bundle"),
		relocatedPolicy: filepath.Join(bundle, "relocated-policy.json"),
	}
}

type testPaths struct {
	parent          string
	bundle          string
	policy          string
	audit           string
	relocatedParent string
	relocatedBundle string
	relocatedPolicy string
}

func testBundlePayload(t *testing.T) ([]byte, []byte) {
	t.Helper()
	candidateBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "trustpolicy", "epoch-1-candidate.json"))
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
	signer := newBundleTestSigner(t)
	signed, audit, err := trustpolicy.Sign(context.Background(), signer, candidate, trustpolicy.SignOptions{
		KeyID:                 "kms-key-id",
		ExpectedPreviousEpoch: 0,
		ExpectedSPKISHA256:    signer.digest,
		Now:                   time.Date(2029, 1, 2, 3, 4, 5, 0, time.UTC),
		CIActor:               "release-approver",
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

type bundleTestSigner struct {
	private *ecdsa.PrivateKey
	public  []byte
	digest  string
}

func newBundleTestSigner(t *testing.T) *bundleTestSigner {
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
	return &bundleTestSigner{private: private, public: public, digest: hex.EncodeToString(digest[:])}
}

func (signer *bundleTestSigner) PublicKey(context.Context, string) ([]byte, string, error) {
	return append([]byte(nil), signer.public...), "public-request-id", nil
}

func (signer *bundleTestSigner) SignDigest(_ context.Context, _ string, digest []byte) ([]byte, string, error) {
	signature, err := ecdsa.SignASN1(rand.Reader, signer.private, digest)
	return signature, "sign-request-id", err
}

func mustCreatePrivateDirectory(t *testing.T, path string) {
	t.Helper()
	if err := createPrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
}

func copyCommittedBundle(t *testing.T, source, destination string) {
	t.Helper()
	mustCreatePrivateDirectory(t, destination)
	for _, name := range []string{"policy.json", "audit.json", trustpolicy.BundleCommitFileName} {
		mustWriteFile(t, filepath.Join(destination, name), mustReadFile(t, filepath.Join(source, name)))
	}
}

func tryRename(t *testing.T, oldPath, newPath string) bool {
	t.Helper()
	if err := os.Rename(oldPath, newPath); err != nil {
		if runtime.GOOS == "windows" {
			return false
		}
		t.Fatal(err)
	}
	return true
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("foreign directory was modified: %v", entries)
	}
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path exists or cannot be checked: %v", err)
	}
}
