//go:build linux

package bundlefs

import (
	"os"
	"syscall"
)

type fileIdentity struct {
	device uint64
	inode  uint64
}

func openRetainedDirectoryHandle(root *os.Root, _ string) (*os.File, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, errBundleFilesystem
	}
	return file, nil
}

func identityFromOpenFile(file *os.File) (fileIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return fileIdentity{}, errBundleFilesystem
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, errBundleFilesystem
	}
	return fileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func createPrivateChildDirectory(parent *os.File, name string) (*os.File, error) {
	parentFD := int(parent.Fd())
	if err := syscall.Mkdirat(parentFD, name, 0o700); err != nil {
		return nil, errBundleFilesystem
	}
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errBundleFilesystem
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errBundleFilesystem
	}
	if err := file.Chmod(0o700); err != nil {
		_ = file.Close()
		return nil, errBundleFilesystem
	}
	return file, nil
}

func verifyParentDirectoryHandle(file *os.File) error {
	info, stat, err := unixFileStat(file)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return errBundleFilesystem
	}
	return nil
}

func verifyPrivateDirectoryHandle(file *os.File) error {
	info, stat, err := unixFileStat(file)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		return errBundleFilesystem
	}
	return nil
}

func verifyPrivateFileHandle(file *os.File) error {
	info, stat, err := unixFileStat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Geteuid()) {
		return errBundleFilesystem
	}
	return nil
}

func verifyPrivateFileInfo(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Geteuid()) {
		return errBundleFilesystem
	}
	return nil
}

func unixFileStat(file *os.File) (os.FileInfo, *syscall.Stat_t, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, nil, errBundleFilesystem
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, nil, errBundleFilesystem
	}
	return info, stat, nil
}

func pathIsReparsePoint(string) bool { return false }

func syncDirectoryHandle(file *os.File) error {
	if err := file.Sync(); err != nil {
		return errBundleFilesystem
	}
	return nil
}
