//go:build windows

package securefile

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openReadLocked(path string) (*os.File, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pointer,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func pathHasReparsePoint(path string) bool {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	return err != nil || attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func pathChainHasReparsePoint(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	volume := filepath.VolumeName(absolute)
	remainder := strings.TrimPrefix(absolute, volume)
	current := volume + string(filepath.Separator)
	for _, part := range strings.FieldsFunc(remainder, func(character rune) bool { return character == '\\' || character == '/' }) {
		current = filepath.Join(current, part)
		if pathHasReparsePoint(current) {
			return true
		}
	}
	return false
}
