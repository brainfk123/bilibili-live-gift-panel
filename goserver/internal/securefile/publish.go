package securefile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type ExactFile struct {
	Name  string
	Bytes []byte
}

func PublishExactFiles(directory string, files []ExactFile, hooks *SealHooks) (err error) {
	if len(files) == 0 || hasLexicalTraversal(directory) {
		return errors.New("exact publication arguments are invalid")
	}
	seen := map[string]struct{}{}
	for _, file := range files {
		if len(file.Bytes) == 0 || filepath.Base(file.Name) != file.Name || file.Name == "." || file.Name == ".." {
			return errors.New("exact publication file is invalid")
		}
		if _, exists := seen[file.Name]; exists {
			return errors.New("exact publication file is duplicated")
		}
		seen[file.Name] = struct{}{}
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return errors.New("exact publication directory is unavailable")
	}
	pathInfo, err := os.Lstat(absolute)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || pathChainHasReparsePoint(absolute) {
		return errors.New("exact publication directory is invalid")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return errors.New("exact publication directory is unavailable")
	}
	defer root.Close()
	openedInfo, err := root.Stat(".")
	if err != nil || !openedInfo.IsDir() || !os.SameFile(pathInfo, openedInfo) {
		return errors.New("exact publication directory identity is invalid")
	}
	if hooks != nil && hooks.AfterOpenDirectory != nil {
		if err := hooks.AfterOpenDirectory(); err != nil {
			return errors.New("exact publication directory changed while open")
		}
	}
	if err := revalidateDirectoryPath(absolute, openedInfo); err != nil {
		return err
	}
	nonceBytes := make([]byte, 8)
	if _, err := rand.Read(nonceBytes); err != nil {
		return errors.New("exact publication nonce is unavailable")
	}
	nonce := hex.EncodeToString(nonceBytes)
	partials := make([]string, len(files))
	backups := make([]string, len(files))
	existed := make([]bool, len(files))
	backedUp := make([]bool, len(files))
	published := make([]bool, len(files))
	rollback := func() {
		for index := len(files) - 1; index >= 0; index-- {
			if published[index] {
				_ = root.Remove(files[index].Name)
			}
			if backedUp[index] {
				_ = root.Rename(backups[index], files[index].Name)
			}
			_ = root.Remove(partials[index])
		}
	}
	committed := false
	defer func() {
		if !committed {
			rollback()
		}
	}()
	for index, file := range files {
		partials[index] = "." + file.Name + ".partial-" + nonce
		backups[index] = "." + file.Name + ".backup-" + nonce
		info, statErr := root.Lstat(file.Name)
		switch {
		case statErr == nil:
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("exact publication target is not a regular file")
			}
			existed[index] = true
		case !os.IsNotExist(statErr):
			return errors.New("exact publication target is unavailable")
		}
		if err := writeRootFile(root, partials[index], file.Bytes); err != nil {
			return err
		}
	}
	for index, file := range files {
		if existed[index] {
			if err := root.Rename(file.Name, backups[index]); err != nil {
				return errors.New("exact publication backup failed")
			}
			backedUp[index] = true
		}
		if err := root.Rename(partials[index], file.Name); err != nil {
			return errors.New("exact publication rename failed")
		}
		published[index] = true
	}
	if err := revalidateDirectoryPath(absolute, openedInfo); err != nil {
		return err
	}
	for _, file := range files {
		got, err := readRootFile(root, file.Name, int64(len(file.Bytes)))
		if err != nil || !bytes.Equal(got, file.Bytes) {
			return errors.New("exact publication bytes differ")
		}
	}
	committed = true
	for index := range files {
		if backedUp[index] {
			if hooks != nil && hooks.BeforeBackupCleanup != nil {
				if err := hooks.BeforeBackupCleanup(index); err != nil {
					return errors.New("exact publication backup cleanup failed")
				}
			}
			if err := root.Remove(backups[index]); err != nil {
				return errors.New("exact publication backup cleanup failed")
			}
			backedUp[index] = false
		}
	}
	return nil
}

func writeRootFile(root *os.Root, name string, contents []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("exact publication temporary file cannot be created")
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return errors.New("exact publication write failed")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("exact publication sync failed")
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !validRegular(info, int64(len(contents))) || info.Size() != int64(len(contents)) {
		return errors.New("exact publication temporary file is invalid")
	}
	return nil
}

func readRootFile(root *os.Root, name string, maximum int64) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !validRegular(info, maximum) || info.Size() != maximum {
		return nil, errors.New("exact publication output is invalid")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(contents)) != maximum {
		return nil, errors.New("exact publication output read failed")
	}
	return contents, nil
}
