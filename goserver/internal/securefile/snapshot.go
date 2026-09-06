package securefile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Snapshot struct {
	Path      string
	Bytes     []byte
	directory string
	file      *os.File
	identity  os.FileInfo
}

func SnapshotRegular(path string, maximum int64, prefix, name string) (*Snapshot, error) {
	contents, err := ReadBoundedRegular(path, maximum, nil)
	if err != nil {
		return nil, err
	}
	return SnapshotBytes(contents, prefix, name)
}

func SnapshotBytes(contents []byte, prefix, name string) (*Snapshot, error) {
	if len(contents) == 0 || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("snapshot input is invalid")
	}
	directory, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, errors.New("snapshot directory is unavailable")
	}
	path := filepath.Join(directory, name)
	cleanup := func() {
		_ = os.Remove(path)
		_ = os.Remove(directory)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return nil, errors.New("snapshot directory is unavailable")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || pathChainHasReparsePoint(directory) {
		cleanup()
		return nil, errors.New("snapshot directory is invalid")
	}
	file, err := createLockedSnapshot(path, contents)
	if err != nil {
		cleanup()
		return nil, errors.New("snapshot file is unavailable")
	}
	identity, err := file.Stat()
	if err != nil || !validRegular(identity, int64(len(contents))) || identity.Size() != int64(len(contents)) {
		_ = file.Close()
		cleanup()
		return nil, errors.New("snapshot file is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !validRegular(pathInfo, int64(len(contents))) || pathChainHasReparsePoint(path) || !os.SameFile(identity, pathInfo) {
		_ = file.Close()
		cleanup()
		return nil, errors.New("snapshot path is invalid")
	}
	return &Snapshot{Path: path, Bytes: append([]byte(nil), contents...), directory: directory, file: file, identity: identity}, nil
}

func (snapshot *Snapshot) Revalidate() error {
	if snapshot == nil || snapshot.file == nil {
		return errors.New("snapshot is unavailable")
	}
	before, err := snapshot.file.Stat()
	if err != nil || !validRegular(before, int64(len(snapshot.Bytes))) || before.Size() != int64(len(snapshot.Bytes)) || !os.SameFile(snapshot.identity, before) {
		return errors.New("snapshot identity changed")
	}
	if _, err := snapshot.file.Seek(0, io.SeekStart); err != nil {
		return errors.New("snapshot cannot be re-read")
	}
	contents, err := io.ReadAll(io.LimitReader(snapshot.file, int64(len(snapshot.Bytes))+1))
	if err != nil || !sameBytes(contents, snapshot.Bytes) {
		return errors.New("snapshot bytes changed")
	}
	after, err := snapshot.file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		return errors.New("snapshot changed while re-read")
	}
	pathInfo, err := os.Lstat(snapshot.Path)
	if err != nil || !validRegular(pathInfo, int64(len(snapshot.Bytes))) || pathChainHasReparsePoint(snapshot.Path) || !os.SameFile(snapshot.identity, pathInfo) {
		return errors.New("snapshot path changed")
	}
	return nil
}

func (snapshot *Snapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	var closeErr error
	if snapshot.file != nil {
		closeErr = snapshot.file.Close()
		snapshot.file = nil
	}
	removeErr := os.Remove(snapshot.Path)
	directoryErr := os.Remove(snapshot.directory)
	if closeErr != nil || removeErr != nil || directoryErr != nil {
		return errors.New("snapshot cleanup failed")
	}
	return nil
}
