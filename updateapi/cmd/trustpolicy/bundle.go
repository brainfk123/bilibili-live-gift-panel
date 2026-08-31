package main

import (
	"errors"
	"os"
	"path/filepath"
)

const bundleMarkerName = ".gift-panel-trustpolicy-staging-v1"

type bundleCheckpoint string

const (
	bundleAfterRecovery      bundleCheckpoint = "after-recovery"
	bundleAfterCreateStaging bundleCheckpoint = "after-create-staging"
	bundleAfterWriteMarker   bundleCheckpoint = "after-write-marker"
	bundleAfterWritePolicy   bundleCheckpoint = "after-write-policy"
	bundleAfterWriteAudit    bundleCheckpoint = "after-write-audit"
	bundleAfterRemoveMarker  bundleCheckpoint = "after-remove-marker"
	bundleAfterSyncStaging   bundleCheckpoint = "after-sync-staging"
	bundleAfterRename        bundleCheckpoint = "after-rename"
	bundleAfterVerifyFinal   bundleCheckpoint = "after-verify-final"
	bundleAfterSyncParent    bundleCheckpoint = "after-sync-parent"
)

func (checkpoint bundleCheckpoint) beforeRename() bool {
	switch checkpoint {
	case bundleAfterRecovery, bundleAfterCreateStaging, bundleAfterWriteMarker, bundleAfterWritePolicy, bundleAfterWriteAudit, bundleAfterRemoveMarker, bundleAfterSyncStaging:
		return true
	default:
		return false
	}
}

type bundleHooks struct {
	checkpoint func(bundleCheckpoint) error
}

func (hooks bundleHooks) reach(checkpoint bundleCheckpoint) error {
	if hooks.checkpoint == nil {
		return nil
	}
	return hooks.checkpoint(checkpoint)
}

type outputBundlePaths struct {
	policy     string
	audit      string
	policyName string
	auditName  string
	final      string
	staging    string
	parent     string
}

func validateOutputBundlePaths(policyPath, auditPath string) (outputBundlePaths, error) {
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
	if samePath(policy, audit) || !samePath(policyParent, auditParent) || policyName == bundleMarkerName || auditName == bundleMarkerName {
		return outputBundlePaths{}, errCommand
	}
	parent := filepath.Dir(policyParent)
	if samePath(parent, policyParent) || filepath.Base(policyParent) == "." || filepath.Base(policyParent) == string(filepath.Separator) {
		return outputBundlePaths{}, errCommand
	}
	if _, err := os.Lstat(policyParent); err == nil || !errors.Is(err, os.ErrNotExist) {
		return outputBundlePaths{}, errCommand
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return outputBundlePaths{}, errCommand
	}
	return outputBundlePaths{
		policy: policy, audit: audit, policyName: policyName, auditName: auditName,
		final: policyParent, staging: stagingPathFor(policyParent), parent: parent,
	}, nil
}

func stagingPathFor(finalParent string) string {
	return filepath.Join(filepath.Dir(finalParent), "."+filepath.Base(finalParent)+".trustpolicy-staging")
}

func writeOutputBundle(policyPath string, policy []byte, auditPath string, audit []byte, hooks bundleHooks) (resultErr error) {
	paths, err := validateOutputBundlePaths(policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	if err := recoverOwnedStaging(paths); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterRecovery); err != nil {
		return errCommand
	}

	created := false
	renamed := false
	defer func() {
		if !renamed && created {
			if cleanupErr := removeCreatedStaging(paths); cleanupErr != nil {
				resultErr = errCommand
			}
		}
	}()
	if err := createPrivateDirectory(paths.staging); err != nil {
		return errCommand
	}
	created = true
	if err := hooks.reach(bundleAfterCreateStaging); err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(filepath.Join(paths.staging, bundleMarkerName), nil); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWriteMarker); err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(filepath.Join(paths.staging, paths.policyName), policy); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWritePolicy); err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(filepath.Join(paths.staging, paths.auditName), audit); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWriteAudit); err != nil {
		return errCommand
	}
	if err := os.Remove(filepath.Join(paths.staging, bundleMarkerName)); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterRemoveMarker); err != nil {
		return errCommand
	}
	if err := syncBundleDirectory(paths.staging); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterSyncStaging); err != nil {
		return errCommand
	}
	if err := renameBundleDirectory(paths.staging, paths.final); err != nil {
		return errCommand
	}
	renamed = true
	if err := hooks.reach(bundleAfterRename); err != nil {
		return errCommand
	}
	if err := verifyPrivateDirectory(paths.final); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterVerifyFinal); err != nil {
		return errCommand
	}
	if err := syncBundleDirectory(paths.parent); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterSyncParent); err != nil {
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

func recoverOwnedStaging(paths outputBundlePaths) error {
	info, err := os.Lstat(paths.staging)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errCommand
	}
	if err := verifyPrivateDirectory(paths.staging); err != nil {
		return errCommand
	}
	entries, err := os.ReadDir(paths.staging)
	if err != nil {
		return errCommand
	}
	if len(entries) == 0 {
		if err := os.Remove(paths.staging); err != nil {
			return errCommand
		}
		return syncBundleDirectory(paths.parent)
	}
	markerFound := false
	policyFound := false
	auditFound := false
	allowed := map[string]struct{}{bundleMarkerName: {}, paths.policyName: {}, paths.auditName: {}}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || entry.Type()&os.ModeSymlink != 0 {
			return errCommand
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() {
			return errCommand
		}
		if entry.Name() == bundleMarkerName {
			if entryInfo.Size() != 0 {
				return errCommand
			}
			markerFound = true
		} else if entry.Name() == paths.policyName {
			policyFound = true
		} else if entry.Name() == paths.auditName {
			auditFound = true
		}
	}
	if !markerFound && !(policyFound && auditFound && len(entries) == 2) {
		return errCommand
	}
	if err := removeStagingEntries(paths, entries); err != nil {
		return errCommand
	}
	return syncBundleDirectory(paths.parent)
}

func removeCreatedStaging(paths outputBundlePaths) error {
	entries, err := os.ReadDir(paths.staging)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errCommand
	}
	allowed := map[string]struct{}{bundleMarkerName: {}, paths.policyName: {}, paths.auditName: {}}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || entry.Type()&os.ModeSymlink != 0 {
			return errCommand
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errCommand
		}
	}
	return removeStagingEntries(paths, entries)
}

func removeStagingEntries(paths outputBundlePaths, entries []os.DirEntry) error {
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(paths.staging, entry.Name())); err != nil {
			return errCommand
		}
	}
	if err := os.Remove(paths.staging); err != nil {
		return errCommand
	}
	return nil
}
