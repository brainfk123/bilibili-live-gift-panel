//go:build !windows

package main

import "os"

func replaceFileAtomically(temporaryPath, finalPath string) atomicReplaceOutcome {
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return atomicReplaceOutcome{Err: err}
	}
	return atomicReplaceOutcome{Committed: true}
}
