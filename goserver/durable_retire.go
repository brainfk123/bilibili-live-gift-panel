package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	return resetArtifactExistsWith(path, os.Lstat)
}

func resetArtifactExistsWith(path string, lstat func(string) (os.FileInfo, error)) (bool, error) {
	_, err := lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func validateResetRootDirectory(root string) error {
	current := filepath.Clean(root)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if resetFileInfoIsLinkOrReparse(info) {
				return fmt.Errorf("reset root directory is a link or reparse point")
			}
			if !info.IsDir() {
				return fmt.Errorf("reset root path is not a directory")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect reset root directory: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("inspect reset root directory: %w", err)
		}
		current = parent
	}
}

func validateResetScanDirectory(root, directory string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve reset root directory: %w", err)
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve reset scan directory: %w", err)
	}
	root = filepath.Clean(root)
	directory = filepath.Clean(directory)
	relative, err := filepath.Rel(root, directory)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("reset scan directory escapes owned root")
	}

	components := []string{root}
	current := root
	if relative != "." {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			components = append(components, current)
		}
	}
	for _, component := range components {
		info, err := os.Lstat(component)
		if err != nil {
			return fmt.Errorf("inspect reset scan directory: %w", err)
		}
		if resetFileInfoIsLinkOrReparse(info) {
			return fmt.Errorf("reset scan directory is a link or reparse point")
		}
		if !info.IsDir() {
			return fmt.Errorf("reset scan path is not a directory")
		}
	}
	return nil
}
