//go:build windows

package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

func openStateFile(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag, perm)
}

// Windows has no portable directory-flush guarantee for the supported filesystems.
// The replacement uses MoveFileExW's replace-existing and write-through flags;
// the new file is already flushed before this call.
func syncStateDirectory(*os.Root) error {
	return nil
}

const (
	moveFileReplaceExisting = 0x00000001
	moveFileWriteThrough    = 0x00000008
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceStateFile(stateDir string, _ *os.Root, oldName, newName string) error {
	source, err := syscall.UTF16PtrFromString(filepath.Join(stateDir, oldName))
	if err != nil {
		return err
	}
	destination, err := syscall.UTF16PtrFromString(filepath.Join(stateDir, newName))
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(source)),
		uintptr(unsafe.Pointer(destination)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	runtime.KeepAlive(source)
	runtime.KeepAlive(destination)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return errors.New("MoveFileExW failed")
}

func stateFilePermissionsSafe(os.FileInfo) bool {
	return true
}
