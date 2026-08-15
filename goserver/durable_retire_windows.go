//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func retireFileDurably(path string) error {
	exists, err := resetArtifactExists(path)
	if err != nil {
		return err
	}
	tombstone := filepath.Join(filepath.Dir(path), resetTombstoneName)
	if !exists {
		_ = os.Remove(tombstone)
		return nil
	}
	outcome := replaceFileAtomically(path, tombstone)
	if outcome.Err != nil {
		return fmt.Errorf("move reset artifact to tombstone: %w", outcome.Err)
	}
	if !outcome.Durable {
		return fmt.Errorf("reset artifact tombstone was not durable")
	}
	_ = os.Remove(tombstone)
	return nil
}
