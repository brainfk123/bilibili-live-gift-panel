package main

import (
	"os"
	"path/filepath"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

type bundleCheckpoint string

const (
	bundleAfterCreateDirectory        bundleCheckpoint = "after-create-directory"
	bundleAfterWritePolicy            bundleCheckpoint = "after-write-policy"
	bundleAfterWriteAudit             bundleCheckpoint = "after-write-audit"
	bundleAfterSyncDataDirectory      bundleCheckpoint = "after-sync-data-directory"
	bundleAfterSyncParent             bundleCheckpoint = "after-sync-parent"
	bundleAfterWriteCommitMarker      bundleCheckpoint = "after-write-commit-marker"
	bundleAfterSyncCommittedDirectory bundleCheckpoint = "after-sync-committed-directory"
)

func (checkpoint bundleCheckpoint) markerAbsent() bool {
	switch checkpoint {
	case bundleAfterCreateDirectory, bundleAfterWritePolicy, bundleAfterWriteAudit, bundleAfterSyncDataDirectory, bundleAfterSyncParent:
		return true
	default:
		return false
	}
}

func (checkpoint bundleCheckpoint) durablyCommitted() bool {
	return checkpoint == bundleAfterSyncCommittedDirectory
}

type bundleHooks struct {
	checkpoint func(bundleCheckpoint, string) error
}

func (hooks bundleHooks) reach(checkpoint bundleCheckpoint, finalDirectory string) error {
	if hooks.checkpoint == nil {
		return nil
	}
	return hooks.checkpoint(checkpoint, finalDirectory)
}

type outputBundlePaths struct {
	policy     string
	audit      string
	policyName string
	auditName  string
	final      string
	parent     string
}

func resolveOutputBundlePaths(policyPath, auditPath string) (outputBundlePaths, error) {
	policy, err := filepath.Abs(policyPath)
	if err != nil {
		return outputBundlePaths{}, errCommand
	}
	audit, err := filepath.Abs(auditPath)
	if err != nil {
		return outputBundlePaths{}, errCommand
	}
	policy = filepath.Clean(policy)
	audit = filepath.Clean(audit)
	policyParent := filepath.Dir(policy)
	auditParent := filepath.Dir(audit)
	policyName := filepath.Base(policy)
	auditName := filepath.Base(audit)
	if samePath(policy, audit) || !samePath(policyParent, auditParent) || policyName == "." || auditName == "." ||
		samePath(policyName, trustpolicy.BundleCommitFileName) || samePath(auditName, trustpolicy.BundleCommitFileName) {
		return outputBundlePaths{}, errCommand
	}
	parent := filepath.Dir(policyParent)
	if samePath(parent, policyParent) || filepath.Base(policyParent) == "." || filepath.Base(policyParent) == string(filepath.Separator) {
		return outputBundlePaths{}, errCommand
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return outputBundlePaths{}, errCommand
	}
	return outputBundlePaths{
		policy: policy, audit: audit, policyName: policyName, auditName: auditName,
		final: policyParent, parent: parent,
	}, nil
}

func validateOutputBundlePaths(policyPath, auditPath string) (outputBundlePaths, error) {
	policy, audit, err := bundlefs.ValidateOutputPaths(policyPath, auditPath)
	if err != nil {
		return outputBundlePaths{}, errCommand
	}
	paths, err := resolveOutputBundlePaths(policy, audit)
	if err != nil {
		return outputBundlePaths{}, errCommand
	}
	return paths, nil
}

func writeOutputBundle(policyPath string, policy []byte, auditPath string, audit []byte, hooks bundleHooks) error {
	paths, err := resolveOutputBundlePaths(policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	err = bundlefs.WriteCommittedBundleWithHooks(paths.policy, policy, paths.audit, audit, bundlefs.WriteHooks{
		Checkpoint: func(checkpoint bundlefs.WriteCheckpoint) error {
			mapped, ok := mapBundleCheckpoint(checkpoint)
			if !ok {
				return nil
			}
			return hooks.reach(mapped, paths.final)
		},
	})
	if err != nil {
		return errCommand
	}
	return nil
}

func mapBundleCheckpoint(checkpoint bundlefs.WriteCheckpoint) (bundleCheckpoint, bool) {
	switch checkpoint {
	case bundlefs.WriteAfterDirectoryAcquired:
		return bundleAfterCreateDirectory, true
	case bundlefs.WriteAfterPolicySynced:
		return bundleAfterWritePolicy, true
	case bundlefs.WriteAfterAuditSynced:
		return bundleAfterWriteAudit, true
	case bundlefs.WriteAfterDataDirectorySynced:
		return bundleAfterSyncDataDirectory, true
	case bundlefs.WriteAfterParentSynced:
		return bundleAfterSyncParent, true
	case bundlefs.WriteAfterCommitSynced:
		return bundleAfterWriteCommitMarker, true
	case bundlefs.WriteAfterCommittedDirectorySynced:
		return bundleAfterSyncCommittedDirectory, true
	default:
		return "", false
	}
}
