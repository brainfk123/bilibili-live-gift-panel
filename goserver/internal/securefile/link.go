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

type LinkHooks struct {
	AfterOpenSource func() error
}

func LinkContentAddressed(directory, expectedSHA256, expectedName string, hooks *LinkHooks) (linked SealedFile, err error) {
	decoded, decodeErr := hex.DecodeString(expectedSHA256)
	if decodeErr != nil || len(decoded) != sha256.Size || strings.ToLower(expectedSHA256) != expectedSHA256 || filepath.Base(expectedName) != expectedName || expectedName == "." || expectedName == ".." || hasLexicalTraversal(directory) {
		return SealedFile{}, errors.New("sealed executable link arguments are invalid")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return SealedFile{}, errors.New("sealed executable directory is unavailable")
	}
	pathInfo, err := os.Lstat(absolute)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || pathChainHasReparsePoint(absolute) {
		return SealedFile{}, errors.New("sealed executable directory is invalid")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return SealedFile{}, errors.New("sealed executable directory is unavailable")
	}
	defer root.Close()
	openedDirectory, err := root.Stat(".")
	if err != nil || !openedDirectory.IsDir() || !os.SameFile(pathInfo, openedDirectory) {
		return SealedFile{}, errors.New("sealed executable directory identity is invalid")
	}
	sourceName := expectedSHA256 + ".exe"
	source, err := root.Open(sourceName)
	if err != nil {
		return SealedFile{}, errors.New("content-addressed executable is unavailable")
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil || !validRegular(sourceInfo, 128<<20) {
		return SealedFile{}, errors.New("content-addressed executable is invalid")
	}
	if hooks != nil && hooks.AfterOpenSource != nil {
		if err := hooks.AfterOpenSource(); err != nil {
			return SealedFile{}, errors.New("content-addressed executable changed while retained")
		}
	}
	contents, err := io.ReadAll(io.LimitReader(source, (128<<20)+1))
	if err != nil || int64(len(contents)) != sourceInfo.Size() {
		return SealedFile{}, errors.New("content-addressed executable read is invalid")
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return SealedFile{}, errors.New("content-addressed executable digest is invalid")
	}
	currentSource, err := root.Lstat(sourceName)
	if err != nil || !validRegular(currentSource, 128<<20) || !os.SameFile(sourceInfo, currentSource) {
		return SealedFile{}, errors.New("content-addressed executable path changed")
	}
	if err := revalidateDirectoryPath(absolute, openedDirectory); err != nil {
		return SealedFile{}, err
	}
	if err := root.Link(sourceName, expectedName); err != nil {
		return SealedFile{}, errors.New("expected-name executable hard link failed")
	}
	linkedCreated := true
	defer func() {
		if err != nil && linkedCreated {
			_ = root.Remove(expectedName)
		}
	}()
	linkedInfo, err := root.Lstat(expectedName)
	if err != nil || !validRegular(linkedInfo, 128<<20) || !os.SameFile(sourceInfo, linkedInfo) {
		return SealedFile{}, errors.New("expected-name executable is not the sealed file")
	}
	linkedFile, err := root.Open(expectedName)
	if err != nil {
		return SealedFile{}, errors.New("expected-name executable cannot be re-opened")
	}
	linkedBytes, readErr := io.ReadAll(io.LimitReader(linkedFile, (128<<20)+1))
	linkedOpenInfo, statErr := linkedFile.Stat()
	closeErr := linkedFile.Close()
	if readErr != nil || statErr != nil || closeErr != nil || !validRegular(linkedOpenInfo, 128<<20) || !os.SameFile(sourceInfo, linkedOpenInfo) || !bytes.Equal(linkedBytes, contents) {
		return SealedFile{}, errors.New("expected-name executable bytes differ")
	}
	finalSource, sourceErr := root.Lstat(sourceName)
	finalLinked, linkedErr := root.Lstat(expectedName)
	retainedInfo, retainedErr := source.Stat()
	if sourceErr != nil || linkedErr != nil || retainedErr != nil || !os.SameFile(sourceInfo, finalSource) || !os.SameFile(sourceInfo, finalLinked) || !os.SameFile(sourceInfo, retainedInfo) || retainedInfo.Size() != sourceInfo.Size() {
		return SealedFile{}, errors.New("sealed executable hard-link identity changed")
	}
	if err := revalidateDirectoryPath(absolute, openedDirectory); err != nil {
		return SealedFile{}, err
	}
	linkedCreated = false
	return SealedFile{Directory: absolute, Name: expectedName, SHA256: expectedSHA256, Size: sourceInfo.Size()}, nil
}
