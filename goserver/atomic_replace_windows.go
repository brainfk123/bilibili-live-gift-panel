//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceFileAtomically(temporaryPath, finalPath string) atomicReplaceOutcome {
	temporaryPath = windowsExtendedPath(temporaryPath)
	finalPath = windowsExtendedPath(finalPath)
	temporaryPathUTF16, err := syscall.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return atomicReplaceOutcome{Err: err}
	}
	finalPathUTF16, err := syscall.UTF16PtrFromString(finalPath)
	if err != nil {
		return atomicReplaceOutcome{Err: err}
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(temporaryPathUTF16)),
		uintptr(unsafe.Pointer(finalPathUTF16)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = syscall.EINVAL
		}
		return atomicReplaceOutcome{Err: callErr}
	}
	return atomicReplaceOutcome{Committed: true, Durable: true}
}

// windowsExtendedPath prepares long paths for a direct Windows API call. Go's
// os helpers do this internally, but MoveFileExW otherwise receives raw paths.
func windowsExtendedPath(path string) string {
	if len(path) >= 4 {
		namespace := filepath.ToSlash(path[:4])
		if namespace == `/??/` || namespace == `//?/` || namespace == `//./` {
			return path
		}
	}
	if filepath.IsAbs(path) && len(path) < 248 {
		return path
	}
	resolved, err := syscall.FullPath(path)
	if err != nil {
		return path
	}
	if len(path) < 248 && len(resolved) < 248 {
		return path
	}
	if strings.HasPrefix(resolved, `\\`) {
		return `\\?\UNC\` + resolved[2:]
	}
	return `\\?\` + resolved
}
