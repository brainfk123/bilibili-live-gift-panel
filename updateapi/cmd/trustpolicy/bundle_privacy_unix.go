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
		_ = os.Remove(path)
		return errCommand
	}
	if err := verifyPrivateDirectory(path); err != nil {
		_ = os.Remove(path)
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

func renameBundleDirectory(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return errCommand
	}
	return nil
}

func isUnsupportedBundleDirectorySyncError(error) bool { return false }
