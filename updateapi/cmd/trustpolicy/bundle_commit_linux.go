//go:build linux && amd64

package main

import (
	"syscall"
	"unsafe"
)

const bundleRenameNoReplace = 1

var bundleATFDCWD = ^uintptr(99)

const bundleRenameat2AMD64 = 316

func renameBundleDirectory(source, target string) error {
	sourcePointer, err := syscall.BytePtrFromString(source)
	if err != nil {
		return errCommand
	}
	targetPointer, err := syscall.BytePtrFromString(target)
	if err != nil {
		return errCommand
	}
	_, _, errno := syscall.Syscall6(
		bundleRenameat2AMD64,
		bundleATFDCWD,
		uintptr(unsafe.Pointer(sourcePointer)),
		bundleATFDCWD,
		uintptr(unsafe.Pointer(targetPointer)),
		bundleRenameNoReplace,
		0,
	)
	if errno != 0 {
		return errCommand
	}
	return nil
}
