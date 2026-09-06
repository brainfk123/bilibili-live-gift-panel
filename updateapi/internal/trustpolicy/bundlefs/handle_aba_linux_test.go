//go:build linux

package bundlefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

func TestLinuxWriterRejectsDeleteRecreateABAAtPostSyncBoundaries(t *testing.T) {
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
			err := writeCommittedBundle(paths.policy, policy, paths.audit, audit, writeHooks{checkpoint: func(checkpoint writeCheckpoint) error {
				if checkpoint != test.checkpoint {
					return nil
				}
				reached = true
				path := test.path(paths)
				data := mustReadFile(t, path)
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				mustWriteFile(t, path, data)
				return nil
			}})
			if !reached {
				t.Fatal("post-sync ABA window was not reached")
			}
			if err == nil {
				t.Fatal("writer accepted delete/recreate ABA")
			}
			if test.checkpoint != writeAfterCommitSynced {
				assertPathAbsent(t, filepath.Join(paths.bundle, trustpolicy.BundleCommitFileName))
			}
		})
	}
}

func TestLinuxReaderRejectsDeleteRecreateABAAtPostReadBoundariesWithZeroBytes(t *testing.T) {
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
			committed, err := readCommittedBundle(paths.policy, paths.audit, readHooks{checkpoint: func(checkpoint readCheckpoint) error {
				if checkpoint != test.checkpoint {
					return nil
				}
				reached = true
				path := test.path(paths)
				data := mustReadFile(t, path)
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				mustWriteFile(t, path, data)
				return nil
			}})
			if !reached {
				t.Fatal("post-read ABA window was not reached")
			}
			if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
				t.Fatalf("reader exposed bytes across delete/recreate ABA: policy=%d audit=%d error=%v", len(committed.Policy), len(committed.Audit), err)
			}
		})
	}
}
