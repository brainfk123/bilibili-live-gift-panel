package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

type bundleCheckpoint string

const (
	bundleAfterCreateDirectory        bundleCheckpoint = "after-create-directory"
	bundleAfterWritePolicy            bundleCheckpoint = "after-write-policy"
	bundleAfterWriteAudit             bundleCheckpoint = "after-write-audit"
	bundleAfterSyncDataDirectory      bundleCheckpoint = "after-sync-data-directory"
	bundleAfterWriteCommitMarker      bundleCheckpoint = "after-write-commit-marker"
	bundleAfterSyncCommittedDirectory bundleCheckpoint = "after-sync-committed-directory"
	bundleAfterSyncParent             bundleCheckpoint = "after-sync-parent"
)

func (checkpoint bundleCheckpoint) beforeCommit() bool {
	switch checkpoint {
	case bundleAfterCreateDirectory, bundleAfterWritePolicy, bundleAfterWriteAudit, bundleAfterSyncDataDirectory:
		return true
	default:
		return false
	}
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
		policyName == trustpolicy.BundleCommitFileName || auditName == trustpolicy.BundleCommitFileName {
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
	paths, err := resolveOutputBundlePaths(policyPath, auditPath)
	if err != nil {
		return outputBundlePaths{}, errCommand
	}
	if _, err := os.Lstat(paths.final); err == nil || !errors.Is(err, os.ErrNotExist) {
		return outputBundlePaths{}, errCommand
	}
	return paths, nil
}

func writeOutputBundle(policyPath string, policy []byte, auditPath string, audit []byte, hooks bundleHooks) error {
	paths, err := validateOutputBundlePaths(policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	if err := createPrivateDirectory(paths.final); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterCreateDirectory, paths.final); err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(paths.policy, policy); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWritePolicy, paths.final); err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(paths.audit, audit); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWriteAudit, paths.final); err != nil {
		return errCommand
	}
	if err := syncBundleDirectory(paths.final); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterSyncDataDirectory, paths.final); err != nil {
		return errCommand
	}
	marker, err := trustpolicy.BuildBundleCommit(paths.policyName, policy, paths.auditName, audit)
	if err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(filepath.Join(paths.final, trustpolicy.BundleCommitFileName), marker); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWriteCommitMarker, paths.final); err != nil {
		return errCommand
	}
	if err := syncBundleDirectory(paths.final); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterSyncCommittedDirectory, paths.final); err != nil {
		return errCommand
	}
	if err := syncBundleDirectory(paths.parent); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterSyncParent, paths.final); err != nil {
		return errCommand
	}
	return nil
}

func writePrivateBundleFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errCommand
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return errCommand
	}
	if _, err := file.Write(data); err != nil {
		return errCommand
	}
	if err := file.Sync(); err != nil {
		return errCommand
	}
	if err := file.Close(); err != nil {
		return errCommand
	}
	closed = true
	return nil
}

func syncBundleDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errCommand
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil && !isUnsupportedBundleDirectorySyncError(syncErr) {
		return errCommand
	}
	if closeErr != nil {
		return errCommand
	}
	return nil
}
