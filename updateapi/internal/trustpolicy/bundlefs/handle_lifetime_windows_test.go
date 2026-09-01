//go:build windows

package bundlefs

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

func TestWindowsRetainedDirectoryHandleIsDerivedFromSuppliedRoot(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	mustCreatePrivateDirectory(t, first)
	mustCreatePrivateDirectory(t, second)
	root, err := os.OpenRoot(first)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	handle, err := openRetainedDirectoryHandle(root, second)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	rootDot, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer rootDot.Close()
	rootInfo, err := rootDot.Stat()
	if err != nil {
		t.Fatal(err)
	}
	handleInfo, err := handle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(rootInfo, handleInfo) {
		t.Fatal("retained handle was reopened from the supplied path instead of derived from Root")
	}
}

func TestWindowsWriterFailsClosedOnTransientParentJunctionRestore(t *testing.T) {
	paths := newTestPaths(t)
	policy, audit := testBundlePayload(t)
	attacker := filepath.Join(filepath.Dir(paths.parent), "junction-target")
	mustCreatePrivateDirectory(t, attacker)
	reached := false
	err := writeCommittedBundle(paths.policy, policy, paths.audit, audit, writeHooks{checkpoint: func(checkpoint writeCheckpoint) error {
		if checkpoint != writeAfterParentAcquired {
			return nil
		}
		reached = true
		if err := os.Rename(paths.parent, paths.relocatedParent); err != nil {
			return errors.New("retained parent blocked namespace replacement")
		}
		command := exec.Command("cmd.exe", "/c", "mklink", "/J", paths.parent, attacker)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("create transient junction: %v: %s", err, output)
		}
		if err := os.Remove(paths.parent); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(paths.relocatedParent, paths.parent); err != nil {
			t.Fatal(err)
		}
		return nil
	}})
	if !reached {
		t.Fatal("parent race window was not reached")
	}
	if err == nil {
		t.Fatal("writer accepted a transient parent junction/path restore race")
	}
	assertPathAbsent(t, filepath.Join(paths.bundle, trustpolicy.BundleCommitFileName))
}

func TestWindowsWriterRetainsEveryArtifactHandleThroughTransactionCompletion(t *testing.T) {
	for _, test := range []struct {
		name       string
		checkpoint writeCheckpoint
		path       func(testPaths) string
	}{
		{name: "policy", checkpoint: writeAfterPolicySynced, path: func(paths testPaths) string { return paths.policy }},
		{name: "audit", checkpoint: writeAfterAuditSynced, path: func(paths testPaths) string { return paths.audit }},
		{name: "marker", checkpoint: writeAfterCommitSynced, path: func(paths testPaths) string { return filepath.Join(paths.bundle, trustpolicy.BundleCommitFileName) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := newTestPaths(t)
			policy, audit := testBundlePayload(t)
			reached := false
			replaced := false
			err := writeCommittedBundle(paths.policy, policy, paths.audit, audit, writeHooks{checkpoint: func(checkpoint writeCheckpoint) error {
				if checkpoint == test.checkpoint {
					reached = true
					if renameErr := os.Rename(test.path(paths), test.path(paths)+".replaced"); renameErr == nil {
						replaced = true
					}
				}
				return nil
			}})
			if !reached {
				t.Fatal("post-sync replacement window was not reached")
			}
			if replaced {
				t.Fatal("artifact replacement was permitted while transaction was incomplete")
			}
			if err != nil {
				t.Fatalf("blocked replacement damaged transaction: %v", err)
			}
		})
	}
}

func TestWindowsReaderRetainsEveryArtifactHandleThroughReadCompletion(t *testing.T) {
	for _, test := range []struct {
		name       string
		checkpoint readCheckpoint
		path       func(testPaths) string
	}{
		{name: "policy", checkpoint: readAfterPolicyRead, path: func(paths testPaths) string { return paths.policy }},
		{name: "audit", checkpoint: readAfterAuditRead, path: func(paths testPaths) string { return paths.audit }},
		{name: "marker", checkpoint: readAfterCommitRead, path: func(paths testPaths) string { return filepath.Join(paths.bundle, trustpolicy.BundleCommitFileName) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := newTestPaths(t)
			policy, audit := testBundlePayload(t)
			if err := WriteCommittedBundle(paths.policy, policy, paths.audit, audit); err != nil {
				t.Fatal(err)
			}
			reached := false
			replaced := false
			committed, err := readCommittedBundle(paths.policy, paths.audit, readHooks{checkpoint: func(checkpoint readCheckpoint) error {
				if checkpoint == test.checkpoint {
					reached = true
					if renameErr := os.Rename(test.path(paths), test.path(paths)+".replaced"); renameErr == nil {
						replaced = true
					}
				}
				return nil
			}})
			if !reached {
				t.Fatal("post-read replacement window was not reached")
			}
			if replaced {
				t.Fatal("artifact replacement was permitted while trusted bytes were pending")
			}
			if err != nil || !bytes.Equal(committed.Policy, policy) || !bytes.Equal(committed.Audit, audit) {
				t.Fatalf("blocked replacement damaged trusted read: %v", err)
			}
		})
	}
}
