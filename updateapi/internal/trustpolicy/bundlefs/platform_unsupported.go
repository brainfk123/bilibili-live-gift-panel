//go:build !windows && !linux

package bundlefs

import "os"

type fileIdentity struct {
	value uint64
}

func openRetainedDirectoryHandle(*os.Root, string) (*os.File, error) {
	return nil, errBundleFilesystem
}

func identityFromOpenFile(*os.File) (fileIdentity, error) { return fileIdentity{}, errBundleFilesystem }

func createPrivateChildDirectory(*os.File, string) (*os.File, error) {
	return nil, errBundleFilesystem
}

func openPrivateBundleFile(*os.File, string, bool) (*os.File, error) {
	return nil, errBundleFilesystem
}

func verifyParentDirectoryHandle(*os.File) error  { return errBundleFilesystem }
func verifyPrivateDirectoryHandle(*os.File) error { return errBundleFilesystem }
func verifyPrivateFileHandle(*os.File) error      { return errBundleFilesystem }
func verifyPrivateFileInfo(os.FileInfo) error     { return errBundleFilesystem }
func pathIsReparsePoint(string) bool              { return true }
func syncDirectoryHandle(*os.File) error          { return errBundleFilesystem }
