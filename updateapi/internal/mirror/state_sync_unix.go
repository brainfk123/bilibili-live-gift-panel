//go:build !windows

package mirror

import (
	"os"
	"syscall"
)

func openStateFile(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag|syscall.O_NOFOLLOW, perm)
}

func syncStateDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func replaceStateFile(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
}

func stateFilePermissionsSafe(info os.FileInfo) bool {
	return info.Mode().Perm() == 0o600
}
