//go:build windows

package main

import (
	"os"
	"syscall"
)

const windowsFileAttributeReparsePoint = 0x400

func resetFileInfoIsLinkOrReparse(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && attributes.FileAttributes&windowsFileAttributeReparsePoint != 0
}
