package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const stagingTokenBytes = 32

type bundleCheckpoint string

const (
	bundleAfterCreateStaging bundleCheckpoint = "after-create-staging"
	bundleAfterWritePolicy   bundleCheckpoint = "after-write-policy"
	bundleAfterWriteAudit    bundleCheckpoint = "after-write-audit"
	bundleAfterSyncStaging   bundleCheckpoint = "after-sync-staging"
	bundleAfterCommit        bundleCheckpoint = "after-commit"
	bundleAfterVerifyFinal   bundleCheckpoint = "after-verify-final"
	bundleAfterSyncParent    bundleCheckpoint = "after-sync-parent"
)

func (checkpoint bundleCheckpoint) beforeCommit() bool {
	switch checkpoint {
	case bundleAfterCreateStaging, bundleAfterWritePolicy, bundleAfterWriteAudit, bundleAfterSyncStaging:
		return true
	default:
		return false
	}
}

type bundleHooks struct {
	checkpoint          func(bundleCheckpoint, string) error
	entropy             io.Reader
	beforeCleanupRemove func(int) error
}

func (hooks bundleHooks) reach(checkpoint bundleCheckpoint, staging string) error {
	if hooks.checkpoint == nil {
		return nil
	}
	return hooks.checkpoint(checkpoint, staging)
}

type outputBundlePaths struct {
	policy     string
	audit      string
	policyName string
	auditName  string
	final      string
	parent     string
}

type bundleFileIdentity struct {
	volume uint64
	file   uint64
}

type stagingClaim struct {
	paths    outputBundlePaths
	path     string
	token    [stagingTokenBytes]byte
	identity bundleFileIdentity
	files    map[string]bundleFileIdentity
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
	if samePath(policy, audit) || !samePath(policyParent, auditParent) || policyName == "." || auditName == "." {
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
		final: policyParent, parent: parent,
	}, nil
}

func writeOutputBundle(policyPath string, policy []byte, auditPath string, audit []byte, hooks bundleHooks) (resultErr error) {
	paths, err := validateOutputBundlePaths(policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	claim, err := createUniqueStagingClaim(paths, hooks.entropy)
	if err != nil {
		return errCommand
	}
	committed := false
	defer func() {
		if !committed {
			if cleanupErr := cleanupStagingClaim(claim, hooks); cleanupErr != nil {
				resultErr = errCommand
			}
		}
	}()
	if err := hooks.reach(bundleAfterCreateStaging, claim.path); err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(filepath.Join(claim.path, paths.policyName), policy); err != nil {
		return errCommand
	}
	if err := recordStagingFile(&claim, paths.policyName); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWritePolicy, claim.path); err != nil {
		return errCommand
	}
	if err := writePrivateBundleFile(filepath.Join(claim.path, paths.auditName), audit); err != nil {
		return errCommand
	}
	if err := recordStagingFile(&claim, paths.auditName); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterWriteAudit, claim.path); err != nil {
		return errCommand
	}
	if err := syncBundleDirectory(claim.path); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterSyncStaging, claim.path); err != nil {
		return errCommand
	}
	if err := renameBundleDirectory(claim.path, paths.final); err != nil {
		return errCommand
	}
	committed = true
	if err := hooks.reach(bundleAfterCommit, claim.path); err != nil {
		return errCommand
	}
	if err := verifyPrivateDirectory(paths.final); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterVerifyFinal, claim.path); err != nil {
		return errCommand
	}
	if err := syncBundleDirectory(paths.parent); err != nil {
		return errCommand
	}
	if err := hooks.reach(bundleAfterSyncParent, claim.path); err != nil {
		return errCommand
	}
	return nil
}

func createUniqueStagingClaim(paths outputBundlePaths, entropy io.Reader) (stagingClaim, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	for range 8 {
		var token [stagingTokenBytes]byte
		if _, err := io.ReadFull(entropy, token[:]); err != nil {
			return stagingClaim{}, errCommand
		}
		staging := stagingPathForToken(paths, token)
		if err := createPrivateDirectory(staging); err != nil {
			if _, statErr := os.Lstat(staging); statErr == nil {
				continue
			}
			return stagingClaim{}, errCommand
		}
		identity, err := readBundleFileIdentity(staging)
		if err != nil {
			return stagingClaim{}, errCommand
		}
		return stagingClaim{paths: paths, path: staging, token: token, identity: identity, files: make(map[string]bundleFileIdentity, 2)}, nil
	}
	return stagingClaim{}, errCommand
}

func stagingPathForToken(paths outputBundlePaths, token [stagingTokenBytes]byte) string {
	name := "." + filepath.Base(paths.final) + ".trustpolicy-staging-" + hex.EncodeToString(token[:])
	return filepath.Join(paths.parent, name)
}

func cleanupStagingClaim(claim stagingClaim, hooks bundleHooks) error {
	if !stagingClaimDirectoryMatches(claim) {
		return errCommand
	}
	entries, err := os.ReadDir(claim.path)
	if err != nil {
		return errCommand
	}
	allowed := map[string]struct{}{claim.paths.policyName: {}, claim.paths.auditName: {}}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || entry.Type()&os.ModeSymlink != 0 {
			return errCommand
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() {
			return errCommand
		}
		expectedIdentity, ok := claim.files[entry.Name()]
		if !ok {
			return errCommand
		}
		identity, err := readBundleFileIdentity(filepath.Join(claim.path, entry.Name()))
		if err != nil || identity != expectedIdentity {
			return errCommand
		}
	}
	for index, entry := range entries {
		if hooks.beforeCleanupRemove != nil {
			if err := hooks.beforeCleanupRemove(index); err != nil {
				return errCommand
			}
		}
		if !stagingClaimDirectoryMatches(claim) {
			return errCommand
		}
		expectedIdentity, ok := claim.files[entry.Name()]
		if !ok {
			return errCommand
		}
		identity, err := readBundleFileIdentity(filepath.Join(claim.path, entry.Name()))
		if err != nil || identity != expectedIdentity {
			return errCommand
		}
		if err := os.Remove(filepath.Join(claim.path, entry.Name())); err != nil {
			return errCommand
		}
	}
	if hooks.beforeCleanupRemove != nil {
		if err := hooks.beforeCleanupRemove(len(entries)); err != nil {
			return errCommand
		}
	}
	if !stagingClaimDirectoryMatches(claim) {
		return errCommand
	}
	remaining, err := os.ReadDir(claim.path)
	if err != nil || len(remaining) != 0 {
		return errCommand
	}
	if err := os.Remove(claim.path); err != nil {
		return errCommand
	}
	return syncBundleDirectory(claim.paths.parent)
}

func recordStagingFile(claim *stagingClaim, name string) error {
	identity, err := readBundleFileIdentity(filepath.Join(claim.path, name))
	if err != nil {
		return errCommand
	}
	claim.files[name] = identity
	return nil
}

func stagingClaimDirectoryMatches(claim stagingClaim) bool {
	if !claimTokenMatchesPath(claim) {
		return false
	}
	info, err := os.Lstat(claim.path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if err := verifyPrivateDirectory(claim.path); err != nil {
		return false
	}
	identity, err := readBundleFileIdentity(claim.path)
	return err == nil && identity == claim.identity
}

func claimTokenMatchesPath(claim stagingClaim) bool {
	prefix := "." + filepath.Base(claim.paths.final) + ".trustpolicy-staging-"
	base := filepath.Base(claim.path)
	if !strings.HasPrefix(base, prefix) || !samePath(filepath.Dir(claim.path), claim.paths.parent) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(base, prefix))
	if err != nil || len(decoded) != stagingTokenBytes {
		return false
	}
	return subtle.ConstantTimeCompare(decoded, claim.token[:]) == 1
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
