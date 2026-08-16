//go:build !windows

package mirror

import (
	"os"
	"syscall"
)

func openDownloadFile(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag|syscall.O_NOFOLLOW, perm)
}

func syncDownloadDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func replaceDownloadFile(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
}
