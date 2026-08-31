package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

const (
	maxCommittedPolicyBytes = 256 << 10
	maxCommittedAuditBytes  = 64 << 10
	maxCommitMarkerBytes    = 4 << 10
)

type bundleFileIdentity struct {
	volume uint64
	file   uint64
}

// ReadCommittedBundle returns bytes only after physical and logical commit
// validation. Task 9 can use the exported content contract in trustpolicy.
func ReadCommittedBundle(policyPath, auditPath string) (trustpolicy.CommittedBundle, error) {
	return readCommittedBundle(policyPath, auditPath)
}

func readCommittedBundle(policyPath, auditPath string) (trustpolicy.CommittedBundle, error) {
	paths, err := resolveOutputBundlePaths(policyPath, auditPath)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	info, err := os.Lstat(paths.final)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || bundlePathIsReparsePoint(paths.final) {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	if err := verifyPrivateDirectory(paths.final); err != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	directoryIdentity, err := readBundleFileIdentity(paths.final)
	if err != nil || validateCommittedEntries(paths) != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	policy, err := readStableCommittedFile(paths.policy, maxCommittedPolicyBytes)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	audit, err := readStableCommittedFile(paths.audit, maxCommittedAuditBytes)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	marker, err := readStableCommittedFile(filepath.Join(paths.final, trustpolicy.BundleCommitFileName), maxCommitMarkerBytes)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	if currentIdentity, err := readBundleFileIdentity(paths.final); err != nil || currentIdentity != directoryIdentity ||
		verifyPrivateDirectory(paths.final) != nil || validateCommittedEntries(paths) != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	committed, err := trustpolicy.ValidateCommittedBundle(paths.policyName, policy, paths.auditName, audit, marker)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errCommand
	}
	return committed, nil
}

func validateCommittedEntries(paths outputBundlePaths) error {
	entries, err := os.ReadDir(paths.final)
	if err != nil || len(entries) != 3 {
		return errCommand
	}
	want := map[string]struct{}{
		paths.policyName:                 {},
		paths.auditName:                  {},
		trustpolicy.BundleCommitFileName: {},
	}
	for _, entry := range entries {
		if _, ok := want[entry.Name()]; !ok || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return errCommand
		}
		path := filepath.Join(paths.final, entry.Name())
		entryInfo, err := os.Lstat(path)
		if err != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 || bundlePathIsReparsePoint(path) {
			return errCommand
		}
	}
	return nil
}

func readStableCommittedFile(path string, maximum int64) ([]byte, error) {
	beforeInfo, err := os.Lstat(path)
	if err != nil || !beforeInfo.Mode().IsRegular() || beforeInfo.Mode()&os.ModeSymlink != 0 || bundlePathIsReparsePoint(path) {
		return nil, errCommand
	}
	beforeIdentity, err := readBundleFileIdentity(path)
	if err != nil {
		return nil, errCommand
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errCommand
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() <= 0 || openedInfo.Size() > maximum {
		return nil, errCommand
	}
	openedIdentity, err := readOpenBundleFileIdentity(file)
	if err != nil || openedIdentity != beforeIdentity {
		return nil, errCommand
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maximum || int64(len(data)) != openedInfo.Size() {
		return nil, errCommand
	}
	endingIdentity, err := readOpenBundleFileIdentity(file)
	if err != nil || endingIdentity != openedIdentity {
		return nil, errCommand
	}
	if err := file.Close(); err != nil {
		return nil, errCommand
	}
	closed = true
	afterIdentity, err := readBundleFileIdentity(path)
	if err != nil || afterIdentity != beforeIdentity || bundlePathIsReparsePoint(path) {
		return nil, errCommand
	}
	afterInfo, err := os.Lstat(path)
	if err != nil || !afterInfo.Mode().IsRegular() || !os.SameFile(beforeInfo, afterInfo) {
		return nil, errCommand
	}
	return data, nil
}
