//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var giftClipMoveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

const (
	giftClipMoveFileReplaceExisting = 0x1
	giftClipMoveFileWriteThrough    = 0x8
)

func replaceGiftClipFileAtomically(source, target string) error {
	sourcePointer, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPointer, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	result, _, callErr := giftClipMoveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePointer)),
		uintptr(unsafe.Pointer(targetPointer)),
		giftClipMoveFileReplaceExisting|giftClipMoveFileWriteThrough,
	)
	if result == 0 {
		return callErr
	}
	return nil
}
