package securefile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type SealHooks struct {
	AfterOpenDirectory  func() error
	BeforeBackupCleanup func(int) error
}

type SealedFile struct {
	Directory string
	Name      string
	SHA256    string
	Size      int64
}

func (snapshot *Snapshot) SealContentAddressed(directory, extension string, hooks *SealHooks) (SealedFile, error) {
	if snapshot == nil || extension == "" || strings.ContainsAny(extension, `/\\`) || filepath.Ext("x"+extension) != extension {
		return SealedFile{}, errors.New("sealed artifact arguments are invalid")
	}
	if err := snapshot.Revalidate(); err != nil {
		return SealedFile{}, errors.New("verified snapshot is unavailable")
	}
	digest := sha256.Sum256(snapshot.Bytes)
	name := hex.EncodeToString(digest[:]) + extension
	sealed, err := WriteExactToDirectory(directory, name, snapshot.Bytes, hooks)
	if err != nil {
		return SealedFile{}, err
	}
	if err := snapshot.Revalidate(); err != nil {
		root, openErr := os.OpenRoot(sealed.Directory)
		if openErr == nil {
			_ = root.Remove(sealed.Name)
			_ = root.Close()
		}
		return SealedFile{}, errors.New("verified snapshot changed while sealing")
	}
	return sealed, nil
}

func WriteExactToDirectory(directory, name string, contents []byte, hooks *SealHooks) (sealed SealedFile, err error) {
	if len(contents) == 0 || filepath.Base(name) != name || name == "." || name == ".." || hasLexicalTraversal(directory) {
		return SealedFile{}, errors.New("sealed output arguments are invalid")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return SealedFile{}, errors.New("sealed output directory is unavailable")
	}
	pathInfo, err := os.Lstat(absolute)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || pathChainHasReparsePoint(absolute) {
		return SealedFile{}, errors.New("sealed output directory is invalid")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return SealedFile{}, errors.New("sealed output directory is unavailable")
	}
	defer root.Close()
	openedInfo, err := root.Stat(".")
	if err != nil || !openedInfo.IsDir() || !os.SameFile(pathInfo, openedInfo) {
		return SealedFile{}, errors.New("sealed output directory identity is invalid")
	}
	if hooks != nil && hooks.AfterOpenDirectory != nil {
		if err := hooks.AfterOpenDirectory(); err != nil {
			return SealedFile{}, errors.New("sealed output directory changed while open")
		}
	}
	if err := revalidateDirectoryPath(absolute, openedInfo); err != nil {
		return SealedFile{}, err
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return SealedFile{}, errors.New("sealed output file cannot be created")
	}
	created := true
	defer func() {
		if err != nil && created {
			_ = root.Remove(name)
		}
	}()
	if _, err = file.Write(contents); err != nil {
		_ = file.Close()
		return SealedFile{}, errors.New("sealed output write failed")
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return SealedFile{}, errors.New("sealed output sync failed")
	}
	writtenInfo, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !validRegular(writtenInfo, int64(len(contents))) || writtenInfo.Size() != int64(len(contents)) {
		return SealedFile{}, errors.New("sealed output file is invalid")
	}
	pathChildInfo, err := root.Lstat(name)
	if err != nil || !validRegular(pathChildInfo, int64(len(contents))) || !os.SameFile(writtenInfo, pathChildInfo) {
		return SealedFile{}, errors.New("sealed output file identity is invalid")
	}
	reader, err := root.Open(name)
	if err != nil {
		return SealedFile{}, errors.New("sealed output cannot be re-read")
	}
	got, readErr := io.ReadAll(io.LimitReader(reader, int64(len(contents))+1))
	readInfo, statErr := reader.Stat()
	closeErr = reader.Close()
	if readErr != nil || statErr != nil || closeErr != nil || !validRegular(readInfo, int64(len(contents))) || !os.SameFile(writtenInfo, readInfo) || !bytes.Equal(got, contents) {
		return SealedFile{}, errors.New("sealed output bytes differ")
	}
	if err := revalidateDirectoryPath(absolute, openedInfo); err != nil {
		return SealedFile{}, err
	}
	digest := sha256.Sum256(contents)
	created = false
	return SealedFile{Directory: absolute, Name: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(contents))}, nil
}

func revalidateDirectoryPath(path string, opened os.FileInfo) error {
	finalInfo, err := os.Lstat(path)
	if err != nil || !finalInfo.IsDir() || finalInfo.Mode()&os.ModeSymlink != 0 || pathChainHasReparsePoint(path) || !os.SameFile(opened, finalInfo) {
		return errors.New("sealed output directory changed")
	}
	return nil
}

func hasLexicalTraversal(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(character rune) bool { return character == '/' || character == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}
