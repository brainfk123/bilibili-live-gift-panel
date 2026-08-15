package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const resetTombstoneName = ".reset-tombstone"

func retireFileWithDirectorySync(
	path string,
	exists func(string) (bool, error),
	move func(string, string) error,
	syncDirectory func(string) error,
	remove func(string) error,
) error {
	dir := filepath.Dir(path)
	tombstone := filepath.Join(dir, resetTombstoneName)
	sourceExists, err := exists(path)
	if err != nil {
		return err
	}
	if sourceExists {
		if err := move(path, tombstone); err != nil {
			return fmt.Errorf("move reset artifact to tombstone: %w", err)
		}
	} else {
		tombstoneExists, err := exists(tombstone)
		if err != nil {
			return err
		}
		if !tombstoneExists {
			return nil
		}
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync reset artifact tombstone: %w", err)
	}
	_ = remove(tombstone)
	return nil
}

func resetArtifactExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
