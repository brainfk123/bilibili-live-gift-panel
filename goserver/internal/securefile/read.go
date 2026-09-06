package securefile

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// ReadHooks exists so same-process race tests can deterministically attempt a
// replacement while the production read handle is retained.
type ReadHooks struct {
	AfterOpen func() error
}

func ReadBoundedRegular(path string, maximum int64, hooks *ReadHooks) ([]byte, error) {
	if maximum <= 0 {
		return nil, errors.New("invalid size policy")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !validRegular(pathInfo, maximum) || pathChainHasReparsePoint(path) {
		return nil, errors.New("input is unavailable")
	}
	file, err := openReadLocked(path)
	if err != nil {
		return nil, errors.New("input is unavailable")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !validRegular(openedInfo, maximum) || !os.SameFile(pathInfo, openedInfo) {
		return nil, errors.New("input identity is invalid")
	}
	if hooks != nil && hooks.AfterOpen != nil {
		if err := hooks.AfterOpen(); err != nil {
			return nil, errors.New("input changed while open")
		}
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(contents)) != openedInfo.Size() || int64(len(contents)) > maximum {
		return nil, errors.New("input read is invalid")
	}
	postReadInfo, err := file.Stat()
	if err != nil || !validRegular(postReadInfo, maximum) || !os.SameFile(openedInfo, postReadInfo) || postReadInfo.Size() != openedInfo.Size() {
		return nil, errors.New("input changed while read")
	}
	finalPathInfo, err := os.Lstat(path)
	if err != nil || !validRegular(finalPathInfo, maximum) || pathChainHasReparsePoint(path) || !os.SameFile(openedInfo, finalPathInfo) {
		return nil, errors.New("input path changed while read")
	}
	return contents, nil
}

func validRegular(info os.FileInfo, maximum int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() > 0 && info.Size() <= maximum
}

func sameBytes(left, right []byte) bool { return bytes.Equal(left, right) }
