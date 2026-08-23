//go:build windows

package mirror

import (
	"os"
)

func openDownloadFile(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag, perm)
}

// Windows FlushFileBuffers on a directory handle is not portable across the
// supported filesystems. The completed file itself is flushed before rename.
func syncDownloadDirectory(*os.Root) error {
	return nil
}

func replaceDownloadFile(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
}
