//go:build !windows

package main

import (
	"os"
	"syscall"
)

func createPrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return errCommand
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errCommand
	}
	if err := verifyPrivateDirectory(path); err != nil {
		return errCommand
	}
	return nil
}

func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errCommand
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errCommand
	}
	return nil
}

func readBundleFileIdentity(path string) (bundleFileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return bundleFileIdentity{}, errCommand
	}
	file, err := os.Open(path)
	if err != nil {
		return bundleFileIdentity{}, errCommand
	}
	identity, identityErr := readOpenBundleFileIdentity(file)
	closeErr := file.Close()
	if identityErr != nil || closeErr != nil {
		return bundleFileIdentity{}, errCommand
	}
	return identity, nil
}

func readOpenBundleFileIdentity(file *os.File) (bundleFileIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return bundleFileIdentity{}, errCommand
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return bundleFileIdentity{}, errCommand
	}
	return bundleFileIdentity{volume: uint64(stat.Dev), file: uint64(stat.Ino)}, nil
}

func bundlePathIsReparsePoint(string) bool { return false }

func isUnsupportedBundleDirectorySyncError(error) bool { return false }
