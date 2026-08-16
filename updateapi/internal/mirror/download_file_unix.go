//go:build !windows

package mirror

import (
	"os"
	"syscall"
)

func openDownloadFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	descriptor, err := syscall.Open(path, flag|syscall.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func syncDownloadDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func replaceDownloadFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
