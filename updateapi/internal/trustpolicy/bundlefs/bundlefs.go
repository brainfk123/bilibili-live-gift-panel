package bundlefs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

const (
	maxCommittedPolicyBytes = 256 << 10
	maxCommittedAuditBytes  = 64 << 10
	maxCommitMarkerBytes    = 4 << 10
)

var errBundleFilesystem = errors.New("publisher policy bundle filesystem is invalid")

type writeCheckpoint string

const (
	writeAfterParentAcquired           writeCheckpoint = "after-parent-acquired"
	writeAfterDirectoryAcquired        writeCheckpoint = "after-directory-acquired"
	writeAfterPolicyOpened             writeCheckpoint = "after-policy-opened"
	writeAfterPolicySynced             writeCheckpoint = "after-policy-synced"
	writeAfterAuditOpened              writeCheckpoint = "after-audit-opened"
	writeAfterAuditSynced              writeCheckpoint = "after-audit-synced"
	writeAfterDataDirectorySynced      writeCheckpoint = "after-data-directory-synced"
	writeAfterParentSynced             writeCheckpoint = "after-parent-synced"
	writeAfterCommitOpened             writeCheckpoint = "after-commit-opened"
	writeAfterCommitSynced             writeCheckpoint = "after-commit-synced"
	writeAfterCommittedDirectorySynced writeCheckpoint = "after-committed-directory-synced"
)

// WriteCheckpoint identifies a completed writer step. It is exposed so the
// CLI's crash/failure harness can stop at exact durability boundaries.
type WriteCheckpoint = writeCheckpoint

const (
	WriteAfterParentAcquired           WriteCheckpoint = writeAfterParentAcquired
	WriteAfterDirectoryAcquired        WriteCheckpoint = writeAfterDirectoryAcquired
	WriteAfterPolicyOpened             WriteCheckpoint = writeAfterPolicyOpened
	WriteAfterPolicySynced             WriteCheckpoint = writeAfterPolicySynced
	WriteAfterAuditOpened              WriteCheckpoint = writeAfterAuditOpened
	WriteAfterAuditSynced              WriteCheckpoint = writeAfterAuditSynced
	WriteAfterDataDirectorySynced      WriteCheckpoint = writeAfterDataDirectorySynced
	WriteAfterParentSynced             WriteCheckpoint = writeAfterParentSynced
	WriteAfterCommitOpened             WriteCheckpoint = writeAfterCommitOpened
	WriteAfterCommitSynced             WriteCheckpoint = writeAfterCommitSynced
	WriteAfterCommittedDirectorySynced WriteCheckpoint = writeAfterCommittedDirectorySynced
)

type directorySyncTarget string

const (
	syncDataDirectory      directorySyncTarget = "data-directory"
	syncParentDirectory    directorySyncTarget = "parent-directory"
	syncCommittedDirectory directorySyncTarget = "committed-directory"
)

type writeHooks struct {
	checkpoint          func(writeCheckpoint) error
	beforeDirectorySync func(directorySyncTarget) error
	closeFile           func(string, *os.File) error
}

// WriteHooks is limited to deterministic crash/failure verification. A nil
// callback is the production behavior.
type WriteHooks struct {
	Checkpoint func(WriteCheckpoint) error
}

func (hooks writeHooks) reach(checkpoint writeCheckpoint) error {
	if hooks.checkpoint == nil {
		return nil
	}
	return hooks.checkpoint(checkpoint)
}

func (hooks writeHooks) beforeSync(target directorySyncTarget) error {
	if hooks.beforeDirectorySync == nil {
		return nil
	}
	return hooks.beforeDirectorySync(target)
}

type readCheckpoint string

const (
	readAfterParentAcquired    readCheckpoint = "after-parent-acquired"
	readAfterDirectoryAcquired readCheckpoint = "after-directory-acquired"
	readAfterEntriesValidated  readCheckpoint = "after-entries-validated"
	readAfterPolicyStat        readCheckpoint = "after-policy-stat"
	readAfterPolicyOpened      readCheckpoint = "after-policy-opened"
	readAfterPolicyRead        readCheckpoint = "after-policy-read"
	readAfterAuditStat         readCheckpoint = "after-audit-stat"
	readAfterAuditOpened       readCheckpoint = "after-audit-opened"
	readAfterAuditRead         readCheckpoint = "after-audit-read"
	readAfterCommitStat        readCheckpoint = "after-commit-stat"
	readAfterCommitOpened      readCheckpoint = "after-commit-opened"
	readAfterCommitRead        readCheckpoint = "after-commit-read"
	readAfterFilesystemRecheck readCheckpoint = "after-filesystem-recheck"
	readAfterContentValidation readCheckpoint = "after-content-validation"
)

type readHooks struct {
	checkpoint func(readCheckpoint) error
	closeFile  func(string, *os.File) error
}

func (hooks readHooks) reach(checkpoint readCheckpoint) error {
	if hooks.checkpoint == nil {
		return nil
	}
	return hooks.checkpoint(checkpoint)
}

type bundlePaths struct {
	policy     string
	audit      string
	policyName string
	auditName  string
	bundle     string
	bundleName string
	parent     string
}

type retainedDirectory struct {
	absolute string
	name     string
	root     *os.Root
	handle   *os.File
	identity fileIdentity
}

type retainedFile struct {
	handle   *os.File
	identity fileIdentity
	size     int64
}

type retainedArtifact struct {
	role string
	file *retainedFile
}

func (directory *retainedDirectory) close() {
	if directory == nil {
		return
	}
	if directory.handle != nil {
		_ = directory.handle.Close()
	}
	if directory.root != nil {
		_ = directory.root.Close()
	}
}

func (file *retainedFile) close() error {
	if file == nil || file.handle == nil {
		return nil
	}
	handle := file.handle
	file.handle = nil
	return handle.Close()
}

func closeBundleFiles(closeFile func(string, *os.File) error, files ...retainedArtifact) error {
	var closeErrors []error
	for _, retained := range files {
		if retained.file == nil || retained.file.handle == nil {
			continue
		}
		handle := retained.file.handle
		retained.file.handle = nil
		var err error
		if closeFile != nil {
			err = closeFile(retained.role, handle)
		} else {
			err = handle.Close()
		}
		if err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

// WriteCommittedBundle creates one dedicated bundle directory, writes and
// synchronizes policy/audit, persists the new directory in its parent, and
// only then creates and synchronizes the canonical commit marker.
func WriteCommittedBundle(policyPath string, policy []byte, auditPath string, audit []byte) error {
	return writeCommittedBundle(policyPath, policy, auditPath, audit, writeHooks{})
}

// WriteCommittedBundleWithHooks performs the same transaction while reporting
// exact checkpoints to the CLI's subprocess crash harness.
func WriteCommittedBundleWithHooks(policyPath string, policy []byte, auditPath string, audit []byte, hooks WriteHooks) error {
	return writeCommittedBundle(policyPath, policy, auditPath, audit, writeHooks{checkpoint: hooks.Checkpoint})
}

// ValidateOutputPaths performs the same static and protected-parent preflight
// as the writer and returns canonical absolute artifact paths. Ownership is
// still reacquired and revalidated by WriteCommittedBundle.
func ValidateOutputPaths(policyPath, auditPath string) (string, string, error) {
	paths, err := resolveBundlePaths(policyPath, auditPath)
	if err != nil {
		return "", "", errBundleFilesystem
	}
	parent, err := openRetainedParent(paths.parent)
	if err != nil {
		return "", "", errBundleFilesystem
	}
	defer parent.close()
	if validateParentBinding(parent) != nil {
		return "", "", errBundleFilesystem
	}
	if _, err := parent.root.Lstat(paths.bundleName); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", "", errBundleFilesystem
	}
	return paths.policy, paths.audit, nil
}

func writeCommittedBundle(policyPath string, policy []byte, auditPath string, audit []byte, hooks writeHooks) error {
	paths, err := resolveBundlePaths(policyPath, auditPath)
	if err != nil {
		return errBundleFilesystem
	}
	marker, err := trustpolicy.BuildBundleCommit(paths.policyName, policy, paths.auditName, audit)
	if err != nil {
		return errBundleFilesystem
	}
	parent, err := openRetainedParent(paths.parent)
	if err != nil {
		return errBundleFilesystem
	}
	defer parent.close()
	if err := hooks.reach(writeAfterParentAcquired); err != nil || validateParentBinding(parent) != nil {
		return errBundleFilesystem
	}
	if _, err := parent.root.Lstat(paths.bundleName); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errBundleFilesystem
	}
	bundle, err := createRetainedPrivateDirectory(parent, paths.bundleName, paths.bundle)
	if err != nil {
		return errBundleFilesystem
	}
	defer bundle.close()
	if err := hooks.reach(writeAfterDirectoryAcquired); err != nil || validateWriterDirectories(parent, bundle) != nil {
		return errBundleFilesystem
	}
	if err := validateExactEntries(bundle, nil); err != nil {
		return errBundleFilesystem
	}
	policyFile, err := writePrivateFile(bundle, paths.policyName, policy, writeAfterPolicyOpened, writeAfterPolicySynced, hooks)
	if err != nil {
		return errBundleFilesystem
	}
	defer policyFile.close()
	auditFile, err := writePrivateFile(bundle, paths.auditName, audit, writeAfterAuditOpened, writeAfterAuditSynced, hooks)
	if err != nil {
		return errBundleFilesystem
	}
	defer auditFile.close()
	dataEntries := map[string]*retainedFile{paths.policyName: policyFile, paths.auditName: auditFile}
	if validateWriterDirectories(parent, bundle) != nil || validateExactEntries(bundle, dataEntries) != nil {
		return errBundleFilesystem
	}
	if err := syncDirectory(bundle, syncDataDirectory, hooks); err != nil {
		return errBundleFilesystem
	}
	if err := hooks.reach(writeAfterDataDirectorySynced); err != nil {
		return errBundleFilesystem
	}
	if validateWriterDirectories(parent, bundle) != nil || validateExactEntries(bundle, dataEntries) != nil {
		return errBundleFilesystem
	}
	if err := syncDirectory(parent, syncParentDirectory, hooks); err != nil {
		return errBundleFilesystem
	}
	if err := hooks.reach(writeAfterParentSynced); err != nil {
		return errBundleFilesystem
	}
	if validateWriterDirectories(parent, bundle) != nil || validateExactEntries(bundle, dataEntries) != nil {
		return errBundleFilesystem
	}
	commitFile, err := writePrivateFile(bundle, trustpolicy.BundleCommitFileName, marker, writeAfterCommitOpened, writeAfterCommitSynced, hooks)
	if err != nil {
		return errBundleFilesystem
	}
	defer commitFile.close()
	committedEntries := map[string]*retainedFile{
		paths.policyName:                 policyFile,
		paths.auditName:                  auditFile,
		trustpolicy.BundleCommitFileName: commitFile,
	}
	if validateWriterDirectories(parent, bundle) != nil || validateExactEntries(bundle, committedEntries) != nil {
		return errBundleFilesystem
	}
	if err := syncDirectory(bundle, syncCommittedDirectory, hooks); err != nil {
		return errBundleFilesystem
	}
	if validateWriterDirectories(parent, bundle) != nil || validateExactEntries(bundle, committedEntries) != nil {
		return errBundleFilesystem
	}
	if err := hooks.reach(writeAfterCommittedDirectorySynced); err != nil {
		return errBundleFilesystem
	}
	if validateWriterDirectories(parent, bundle) != nil || validateExactEntries(bundle, committedEntries) != nil {
		return errBundleFilesystem
	}
	if err := closeBundleFiles(hooks.closeFile,
		retainedArtifact{role: "policy", file: policyFile},
		retainedArtifact{role: "audit", file: auditFile},
		retainedArtifact{role: "marker", file: commitFile},
	); err != nil {
		return errBundleFilesystem
	}
	return nil
}

// ReadCommittedBundle returns no policy or audit bytes unless the retained
// parent, directory, exact entries, marker, policy, and audit all validate.
func ReadCommittedBundle(policyPath, auditPath string) (trustpolicy.CommittedBundle, error) {
	return readCommittedBundle(policyPath, auditPath, readHooks{})
}

func readCommittedBundle(policyPath, auditPath string, hooks readHooks) (trustpolicy.CommittedBundle, error) {
	paths, err := resolveBundlePaths(policyPath, auditPath)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	parent, err := openRetainedParent(paths.parent)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	defer parent.close()
	if err := hooks.reach(readAfterParentAcquired); err != nil || validateParentBinding(parent) != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	bundle, err := openRetainedPrivateDirectory(parent, paths.bundleName, paths.bundle)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	defer bundle.close()
	if err := hooks.reach(readAfterDirectoryAcquired); err != nil || validateReaderDirectories(parent, bundle) != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	wanted := map[string]*retainedFile{
		paths.policyName: nil, paths.auditName: nil, trustpolicy.BundleCommitFileName: nil,
	}
	if err := validateExactEntryNames(bundle, wanted); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if err := hooks.reach(readAfterEntriesValidated); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	policy, policyFile, err := readStableFile(bundle, paths.policyName, maxCommittedPolicyBytes, hooks, readAfterPolicyStat, readAfterPolicyOpened)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	defer policyFile.close()
	if err := hooks.reach(readAfterPolicyRead); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if validateEntryIdentity(bundle, paths.policyName, policyFile) != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	audit, auditFile, err := readStableFile(bundle, paths.auditName, maxCommittedAuditBytes, hooks, readAfterAuditStat, readAfterAuditOpened)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	defer auditFile.close()
	if err := hooks.reach(readAfterAuditRead); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if validateEntryIdentity(bundle, paths.auditName, auditFile) != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	marker, markerFile, err := readStableFile(bundle, trustpolicy.BundleCommitFileName, maxCommitMarkerBytes, hooks, readAfterCommitStat, readAfterCommitOpened)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	defer markerFile.close()
	if err := hooks.reach(readAfterCommitRead); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if validateEntryIdentity(bundle, trustpolicy.BundleCommitFileName, markerFile) != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	entries := map[string]*retainedFile{
		paths.policyName:                 policyFile,
		paths.auditName:                  auditFile,
		trustpolicy.BundleCommitFileName: markerFile,
	}
	if validateReaderDirectories(parent, bundle) != nil || validateExactEntries(bundle, entries) != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if err := hooks.reach(readAfterFilesystemRecheck); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	committed, err := trustpolicy.ValidateCommittedBundle(paths.policyName, policy, paths.auditName, audit, marker)
	if err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if err := hooks.reach(readAfterContentValidation); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if validateReaderDirectories(parent, bundle) != nil || validateExactEntries(bundle, entries) != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	if err := closeBundleFiles(hooks.closeFile,
		retainedArtifact{role: "policy", file: policyFile},
		retainedArtifact{role: "audit", file: auditFile},
		retainedArtifact{role: "marker", file: markerFile},
	); err != nil {
		return trustpolicy.CommittedBundle{}, errBundleFilesystem
	}
	return committed, nil
}

func resolveBundlePaths(policyPath, auditPath string) (bundlePaths, error) {
	policy, err := filepath.Abs(policyPath)
	if err != nil {
		return bundlePaths{}, errBundleFilesystem
	}
	audit, err := filepath.Abs(auditPath)
	if err != nil {
		return bundlePaths{}, errBundleFilesystem
	}
	policy = filepath.Clean(policy)
	audit = filepath.Clean(audit)
	bundle := filepath.Dir(policy)
	if samePath(policy, audit) || !samePath(bundle, filepath.Dir(audit)) {
		return bundlePaths{}, errBundleFilesystem
	}
	policyName := filepath.Base(policy)
	auditName := filepath.Base(audit)
	bundleName := filepath.Base(bundle)
	parent := filepath.Dir(bundle)
	if !validLeafName(policyName) || !validLeafName(auditName) || !validLeafName(bundleName) ||
		samePath(policyName, auditName) || samePath(policyName, trustpolicy.BundleCommitFileName) || samePath(auditName, trustpolicy.BundleCommitFileName) ||
		samePath(parent, bundle) {
		return bundlePaths{}, errBundleFilesystem
	}
	return bundlePaths{
		policy: policy, audit: audit, policyName: policyName, auditName: auditName,
		bundle: bundle, bundleName: bundleName, parent: parent,
	}, nil
}

func validLeafName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func openRetainedParent(path string) (*retainedDirectory, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || pathIsReparsePoint(path) {
		return nil, errBundleFilesystem
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errBundleFilesystem
	}
	handle, err := openRetainedDirectoryHandle(root, path)
	if err != nil {
		_ = root.Close()
		return nil, errBundleFilesystem
	}
	identity, err := identityFromOpenFile(handle)
	opened, statErr := handle.Stat()
	if err != nil || statErr != nil || !os.SameFile(before, opened) || verifyParentDirectoryHandle(handle) != nil {
		_ = handle.Close()
		_ = root.Close()
		return nil, errBundleFilesystem
	}
	directory := &retainedDirectory{absolute: path, root: root, handle: handle, identity: identity}
	if validateParentBinding(directory) != nil {
		directory.close()
		return nil, errBundleFilesystem
	}
	return directory, nil
}

func createRetainedPrivateDirectory(parent *retainedDirectory, name, absolute string) (*retainedDirectory, error) {
	created, err := createPrivateChildDirectory(parent.handle, name)
	if err != nil {
		return nil, errBundleFilesystem
	}
	createdInfo, err := created.Stat()
	if err != nil || verifyPrivateDirectoryHandle(created) != nil {
		_ = created.Close()
		return nil, errBundleFilesystem
	}
	root, err := parent.root.OpenRoot(name)
	if err != nil {
		_ = created.Close()
		return nil, errBundleFilesystem
	}
	opened, err := root.Open(".")
	if err != nil {
		_ = created.Close()
		_ = root.Close()
		return nil, errBundleFilesystem
	}
	openedInfo, statErr := opened.Stat()
	identity, identityErr := identityFromOpenFile(created)
	if statErr != nil || identityErr != nil || !os.SameFile(createdInfo, openedInfo) || verifyPrivateDirectoryHandle(opened) != nil {
		_ = opened.Close()
		_ = created.Close()
		_ = root.Close()
		return nil, errBundleFilesystem
	}
	if err := opened.Close(); err != nil {
		_ = created.Close()
		_ = root.Close()
		return nil, errBundleFilesystem
	}
	directory := &retainedDirectory{absolute: absolute, name: name, root: root, handle: created, identity: identity}
	if validateChildBinding(parent, directory) != nil {
		directory.close()
		return nil, errBundleFilesystem
	}
	return directory, nil
}

func openRetainedPrivateDirectory(parent *retainedDirectory, name, absolute string) (*retainedDirectory, error) {
	info, err := parent.root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errBundleFilesystem
	}
	root, err := parent.root.OpenRoot(name)
	if err != nil {
		return nil, errBundleFilesystem
	}
	handle, err := openRetainedDirectoryHandle(root, absolute)
	if err != nil {
		_ = root.Close()
		return nil, errBundleFilesystem
	}
	openedInfo, statErr := handle.Stat()
	identity, identityErr := identityFromOpenFile(handle)
	if statErr != nil || identityErr != nil || !os.SameFile(info, openedInfo) || verifyPrivateDirectoryHandle(handle) != nil {
		_ = handle.Close()
		_ = root.Close()
		return nil, errBundleFilesystem
	}
	directory := &retainedDirectory{absolute: absolute, name: name, root: root, handle: handle, identity: identity}
	if validateChildBinding(parent, directory) != nil {
		directory.close()
		return nil, errBundleFilesystem
	}
	return directory, nil
}

func validateWriterDirectories(parent, bundle *retainedDirectory) error {
	if validateParentBinding(parent) != nil || validateChildBinding(parent, bundle) != nil ||
		verifyParentDirectoryHandle(parent.handle) != nil || verifyPrivateDirectoryHandle(bundle.handle) != nil {
		return errBundleFilesystem
	}
	return nil
}

func validateReaderDirectories(parent, bundle *retainedDirectory) error {
	return validateWriterDirectories(parent, bundle)
}

func validateParentBinding(parent *retainedDirectory) error {
	info, err := os.Lstat(parent.absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || pathIsReparsePoint(parent.absolute) {
		return errBundleFilesystem
	}
	opened, err := parent.handle.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errBundleFilesystem
	}
	if validateRootHandleBinding(parent, verifyParentDirectoryHandle) != nil {
		return errBundleFilesystem
	}
	return nil
}

func validateChildBinding(parent, child *retainedDirectory) error {
	if validateParentBinding(parent) != nil {
		return errBundleFilesystem
	}
	info, err := parent.root.Lstat(child.name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errBundleFilesystem
	}
	opened, err := child.handle.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errBundleFilesystem
	}
	if validateRootHandleBinding(child, verifyPrivateDirectoryHandle) != nil {
		return errBundleFilesystem
	}
	return nil
}

func validateRootHandleBinding(directory *retainedDirectory, verify func(*os.File) error) error {
	if directory == nil || directory.root == nil || directory.handle == nil || verify == nil {
		return errBundleFilesystem
	}
	rootDot, err := directory.root.Open(".")
	if err != nil {
		return errBundleFilesystem
	}
	defer rootDot.Close()
	rootInfo, rootStatErr := rootDot.Stat()
	handleInfo, handleStatErr := directory.handle.Stat()
	rootIdentity, rootIdentityErr := identityFromOpenFile(rootDot)
	handleIdentity, handleIdentityErr := identityFromOpenFile(directory.handle)
	if rootStatErr != nil || handleStatErr != nil || rootIdentityErr != nil || handleIdentityErr != nil ||
		!os.SameFile(rootInfo, handleInfo) || rootIdentity != directory.identity || handleIdentity != directory.identity ||
		verify(rootDot) != nil || verify(directory.handle) != nil {
		return errBundleFilesystem
	}
	return nil
}

func writePrivateFile(directory *retainedDirectory, name string, data []byte, openedCheckpoint, syncedCheckpoint writeCheckpoint, hooks writeHooks) (*retainedFile, error) {
	file, err := openPrivateBundleFile(directory.handle, name, true)
	if err != nil {
		return nil, errBundleFilesystem
	}
	retained := false
	defer func() {
		if !retained {
			_ = file.Close()
		}
	}()
	if err := file.Chmod(0o600); err != nil || verifyPrivateFileHandle(file) != nil {
		return nil, errBundleFilesystem
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return nil, errBundleFilesystem
	}
	identity, err := identityFromOpenFile(file)
	if err != nil {
		return nil, errBundleFilesystem
	}
	result := &retainedFile{handle: file, identity: identity, size: openedInfo.Size()}
	if validateRetainedFile(result) != nil || validateEntryIdentity(directory, name, result) != nil {
		return nil, errBundleFilesystem
	}
	if err := hooks.reach(openedCheckpoint); err != nil {
		return nil, errBundleFilesystem
	}
	if validateRetainedFile(result) != nil || validateEntryIdentity(directory, name, result) != nil {
		return nil, errBundleFilesystem
	}
	if err := writeAll(file, data); err != nil || file.Sync() != nil {
		return nil, errBundleFilesystem
	}
	result.size = int64(len(data))
	endingInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, endingInfo) || validateRetainedFile(result) != nil {
		return nil, errBundleFilesystem
	}
	if err := validateEntryIdentity(directory, name, result); err != nil {
		return nil, errBundleFilesystem
	}
	if err := hooks.reach(syncedCheckpoint); err != nil {
		return nil, errBundleFilesystem
	}
	if validateRetainedFile(result) != nil || validateEntryIdentity(directory, name, result) != nil {
		return nil, errBundleFilesystem
	}
	retained = true
	return result, nil
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil || written <= 0 || written > len(data) {
			return errBundleFilesystem
		}
		data = data[written:]
	}
	return nil
}

func readStableFile(directory *retainedDirectory, name string, maximum int64, hooks readHooks, afterStat, afterOpen readCheckpoint) ([]byte, *retainedFile, error) {
	before, err := directory.root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() <= 0 || before.Size() > maximum ||
		verifyPrivateFileInfo(before) != nil {
		return nil, nil, errBundleFilesystem
	}
	if afterStat != "" {
		if err := hooks.reach(afterStat); err != nil {
			return nil, nil, errBundleFilesystem
		}
	}
	file, err := openPrivateBundleFile(directory.handle, name, false)
	if err != nil {
		return nil, nil, errBundleFilesystem
	}
	retained := false
	defer func() {
		if !retained {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() != before.Size() || verifyPrivateFileHandle(file) != nil {
		return nil, nil, errBundleFilesystem
	}
	identity, err := identityFromOpenFile(file)
	if err != nil {
		return nil, nil, errBundleFilesystem
	}
	result := &retainedFile{handle: file, identity: identity, size: opened.Size()}
	if validateRetainedFile(result) != nil || validateEntryIdentity(directory, name, result) != nil {
		return nil, nil, errBundleFilesystem
	}
	if afterOpen != "" {
		if err := hooks.reach(afterOpen); err != nil {
			return nil, nil, errBundleFilesystem
		}
	}
	if validateRetainedFile(result) != nil || validateEntryIdentity(directory, name, result) != nil {
		return nil, nil, errBundleFilesystem
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(data) == 0 || int64(len(data)) != opened.Size() || int64(len(data)) > maximum {
		return nil, nil, errBundleFilesystem
	}
	ending, err := file.Stat()
	if err != nil || !os.SameFile(opened, ending) || validateRetainedFile(result) != nil {
		return nil, nil, errBundleFilesystem
	}
	if err := validateEntryIdentity(directory, name, result); err != nil {
		return nil, nil, errBundleFilesystem
	}
	retained = true
	return data, result, nil
}

func validateRetainedFile(file *retainedFile) error {
	if file == nil || file.handle == nil || file.size < 0 {
		return errBundleFilesystem
	}
	info, err := file.handle.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.size || verifyPrivateFileHandle(file.handle) != nil {
		return errBundleFilesystem
	}
	identity, err := identityFromOpenFile(file.handle)
	if err != nil || identity != file.identity {
		return errBundleFilesystem
	}
	return nil
}

func validateEntryIdentity(directory *retainedDirectory, name string, expected *retainedFile) error {
	if validateRetainedFile(expected) != nil {
		return errBundleFilesystem
	}
	current, err := directory.root.Lstat(name)
	opened, openedErr := expected.handle.Stat()
	if err != nil || openedErr != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) || verifyPrivateFileInfo(current) != nil {
		return errBundleFilesystem
	}
	return nil
}

func validateExactEntryNames(directory *retainedDirectory, expected map[string]*retainedFile) error {
	opened, err := directory.root.Open(".")
	if err != nil {
		return errBundleFilesystem
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	retainedInfo, retainedErr := directory.handle.Stat()
	if err != nil || retainedErr != nil || !os.SameFile(openedInfo, retainedInfo) || verifyPrivateDirectoryHandle(opened) != nil {
		return errBundleFilesystem
	}
	entries, err := opened.ReadDir(-1)
	if err != nil || len(entries) != len(expected) {
		return errBundleFilesystem
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if _, duplicate := seen[name]; duplicate {
			return errBundleFilesystem
		}
		seen[name] = struct{}{}
		if _, ok := expected[name]; !ok {
			return errBundleFilesystem
		}
		info, err := directory.root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || verifyPrivateFileInfo(info) != nil {
			return errBundleFilesystem
		}
	}
	return nil
}

func validateExactEntries(directory *retainedDirectory, expected map[string]*retainedFile) error {
	if expected == nil {
		expected = map[string]*retainedFile{}
	}
	if err := validateExactEntryNames(directory, expected); err != nil {
		return errBundleFilesystem
	}
	for name, info := range expected {
		if info == nil || validateEntryIdentity(directory, name, info) != nil {
			return errBundleFilesystem
		}
	}
	return nil
}

func syncDirectory(directory *retainedDirectory, target directorySyncTarget, hooks writeHooks) error {
	if err := hooks.beforeSync(target); err != nil {
		return errBundleFilesystem
	}
	if err := syncDirectoryHandle(directory.handle); err != nil {
		return errBundleFilesystem
	}
	identity, err := identityFromOpenFile(directory.handle)
	if err != nil || identity != directory.identity {
		return errBundleFilesystem
	}
	return nil
}

// CreatePrivateDirectory is a create-only bootstrap for a protected publisher
// workspace. The caller must control the containing namespace; bundle writes
// still independently retain and verify this directory as their parent.
func CreatePrivateDirectory(path string) error {
	return createPrivateDirectory(path)
}

func createPrivateDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errBundleFilesystem
	}
	absolute = filepath.Clean(absolute)
	parentPath := filepath.Dir(absolute)
	name := filepath.Base(absolute)
	if !validLeafName(name) {
		return errBundleFilesystem
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return errBundleFilesystem
	}
	defer root.Close()
	parent, err := root.Open(".")
	if err != nil {
		return errBundleFilesystem
	}
	defer parent.Close()
	created, err := createPrivateChildDirectory(parent, name)
	if err != nil {
		return errBundleFilesystem
	}
	defer created.Close()
	if verifyPrivateDirectoryHandle(created) != nil {
		return errBundleFilesystem
	}
	return nil
}
