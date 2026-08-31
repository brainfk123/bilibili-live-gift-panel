//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

const bundleMoveFileWriteThrough = 0x00000008

var bundleMoveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func renameBundleDirectory(source, target string) error {
	sourcePointer, err := syscall.UTF16PtrFromString(windowsBundlePath(source))
	if err != nil {
		return errCommand
	}
	targetPointer, err := syscall.UTF16PtrFromString(windowsBundlePath(target))
	if err != nil {
		return errCommand
	}
	result, _, _ := bundleMoveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePointer)),
		uintptr(unsafe.Pointer(targetPointer)),
		bundleMoveFileWriteThrough,
	)
	if result == 0 {
		return errCommand
	}
	return nil
}
