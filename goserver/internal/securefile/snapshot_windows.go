//go:build windows

package securefile

import (
	"io"
	"os"
	"syscall"
)

func createLockedSnapshot(path string, contents []byte) (*os.File, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(pointer, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, nil, syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	writable := os.NewFile(uintptr(handle), path)
	if _, err := writable.Write(contents); err != nil {
		_ = writable.Close()
		return nil, err
	}
	if err := writable.Sync(); err != nil {
		_ = writable.Close()
		return nil, err
	}
	if err := writable.Close(); err != nil {
		return nil, err
	}
	locked, err := openReadLocked(path)
	if err != nil {
		return nil, err
	}
	if _, err := locked.Seek(0, io.SeekStart); err != nil {
		_ = locked.Close()
		return nil, err
	}
	return locked, nil
}
