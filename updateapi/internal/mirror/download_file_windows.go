//go:build windows

package mirror

import (
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func openDownloadFile(path string, flag int, _ os.FileMode) (*os.File, error) {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(syscall.GENERIC_READ)
	if flag&os.O_WRONLY != 0 {
		access = syscall.GENERIC_WRITE
	}
	if flag&os.O_RDWR != 0 {
		access = syscall.GENERIC_READ | syscall.GENERIC_WRITE
	}
	disposition := uint32(syscall.OPEN_EXISTING)
	switch {
	case flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		disposition = syscall.CREATE_NEW
	case flag&os.O_CREATE != 0 && flag&os.O_TRUNC != 0:
		disposition = syscall.CREATE_ALWAYS
	case flag&os.O_CREATE != 0:
		disposition = syscall.OPEN_ALWAYS
	case flag&os.O_TRUNC != 0:
		disposition = syscall.TRUNCATE_EXISTING
	}
	handle, err := syscall.CreateFile(
		pathPointer,
		access,
		syscall.FILE_SHARE_READ,
		nil,
		disposition,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if flag&os.O_APPEND != 0 {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

// Windows FlushFileBuffers on a directory handle is not portable across the
// supported filesystems. The completed file itself is flushed before rename.
func syncDownloadDirectory(string) error {
	return nil
}

func replaceDownloadFile(oldPath, newPath string) error {
	oldPointer, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPointer, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(oldPointer)),
		uintptr(unsafe.Pointer(newPointer)),
		uintptr(moveFileReplaceExisting|moveFileWriteThrough),
	)
	if result != 0 {
		return nil
	}
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return callErr
	}
	return syscall.EINVAL
}
