//go:build !windows

package securefile

import (
	"os"
	"path/filepath"
)

func openReadLocked(path string) (*os.File, error) {
	return os.Open(path)
}

func pathHasReparsePoint(string) bool { return false }

func pathChainHasReparsePoint(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	real, err := filepath.EvalSymlinks(absolute)
	return err != nil || real != absolute
}
