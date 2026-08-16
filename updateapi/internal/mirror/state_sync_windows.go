//go:build windows

package mirror

import "os"

func openStateFile(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag, perm)
}

// Windows has no portable directory-flush guarantee for the supported filesystems.
// The replacement is still atomic and the new file is flushed before rename.
func syncStateDirectory(*os.Root) error {
	return nil
}

func replaceStateFile(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
}

func stateFilePermissionsSafe(os.FileInfo) bool {
	return true
}
