//go:build !windows

package main

import "os"

func retireFileDurably(path string) error {
	return retireFileWithDirectorySync(path, resetArtifactExists, os.Rename, syncStateDirectory, os.Remove)
}
