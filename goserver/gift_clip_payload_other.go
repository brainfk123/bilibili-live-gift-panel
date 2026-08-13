//go:build !windows

package main

import "os"

func replaceGiftClipFileAtomically(source, target string) error {
	return os.Rename(source, target)
}
