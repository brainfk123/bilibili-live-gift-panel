package mirror

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultDownloadTimeout = 2 * time.Minute
	defaultMaxAttempts     = 3
	maxResumeMetadataBytes = 4 << 10
)

type DownloadSpec struct {
	Name     string
	URL      string
	Size     int64
	MaxBytes int64
}

type ArtifactFetcher interface {
	Download(context.Context, DownloadSpec) (string, error)
}

type resumeMetadata struct {
	URL  string `json:"url"`
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}

type downloaderOptions struct {
	overallTimeout time.Duration
	maxAttempts    int
	backoff        func(context.Context, int) error
	syncFile       func(*os.File) error
	beforeRename   func()
	rename         func(*os.Root, string, string) error
}

type downloader struct {
	client    *http.Client
	stateDir  string
	stateInfo os.FileInfo
	options   downloaderOptions
	gate      chan struct{}
}

type partialDownload struct {
	offset int64
	etag   string
	file   *os.File
}

type attemptError struct {
	err       error
	retryable bool
}

func (err *attemptError) Error() string { return err.err.Error() }
func (err *attemptError) Unwrap() error { return err.err }

func NewDownloader(stateDir string) (ArtifactFetcher, error) {
	return newDownloaderWithOptions(NewRestrictedHTTPClient(), stateDir, downloaderOptions{
		overallTimeout: defaultDownloadTimeout,
		maxAttempts:    defaultMaxAttempts,
		backoff:        defaultDownloadBackoff,
		syncFile:       func(file *os.File) error { return file.Sync() },
		rename:         replaceDownloadFile,
	})
}

func newDownloaderWithOptions(client *http.Client, stateDir string, options downloaderOptions) (*downloader, error) {
	if client == nil {
		return nil, errors.New("download client is required")
	}
	if stateDir == "" || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, errors.New("download state directory must be an absolute clean path")
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		return nil, errors.New("download state directory is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("download state directory must be a real directory")
	}
	if options.overallTimeout <= 0 || options.maxAttempts <= 0 || options.maxAttempts > defaultMaxAttempts ||
		options.backoff == nil || options.syncFile == nil || options.rename == nil {
		return nil, errors.New("download options are invalid")
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &downloader{client: client, stateDir: stateDir, stateInfo: info, options: options, gate: gate}, nil
}

func (downloader *downloader) Download(ctx context.Context, spec DownloadSpec) (string, error) {
	if err := validateDownloadSpec(spec); err != nil {
		return "", err
	}
	finalPath := filepath.Join(downloader.stateDir, spec.Name)
	if filepath.Dir(finalPath) != downloader.stateDir {
		return "", errors.New("artifact path escapes download state directory")
	}

	downloadCtx, cancel := context.WithTimeout(ctx, downloader.options.overallTimeout)
	defer cancel()
	select {
	case <-downloadCtx.Done():
		return "", downloadCtx.Err()
	case <-downloader.gate:
	}
	defer func() { downloader.gate <- struct{}{} }()
	root, err := os.OpenRoot(downloader.stateDir)
	if err != nil {
		return "", errors.New("could not open download state directory")
	}
	defer root.Close()
	if err := downloader.validateStateRoot(root); err != nil {
		return "", err
	}

	for attempt := 1; attempt <= downloader.options.maxAttempts; attempt++ {
		if err := downloadCtx.Err(); err != nil {
			return "", err
		}
		err := downloader.downloadOnce(downloadCtx, root, spec)
		if err == nil {
			return finalPath, nil
		}
		var failure *attemptError
		if !errors.As(err, &failure) || !failure.retryable {
			return "", err
		}
		if attempt == downloader.options.maxAttempts {
			if contextErr := downloadCtx.Err(); contextErr != nil {
				return "", contextErr
			}
			return "", errors.New("artifact download failed after retry limit")
		}
		if err := downloader.options.backoff(downloadCtx, attempt); err != nil {
			return "", err
		}
	}
	return "", errors.New("artifact download failed")
}

func (downloader *downloader) downloadOnce(ctx context.Context, root *os.Root, spec DownloadSpec) error {
	finalName := spec.Name
	if err := rejectUnsafeExistingPath(root, finalName); err != nil {
		return err
	}
	partial, err := downloader.loadPartial(root, spec, finalName)
	if err != nil {
		return err
	}
	defer func() {
		if partial.file != nil {
			_ = partial.file.Close()
		}
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return errors.New("could not create artifact request")
	}
	if partial.offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", partial.offset))
		request.Header.Set("If-Range", partial.etag)
	}
	response, err := downloader.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return &attemptError{err: ctx.Err(), retryable: true}
		}
		return &attemptError{err: errors.New("artifact request failed"), retryable: true}
	}
	defer response.Body.Close()
	if err := downloader.validateStateRoot(root); err != nil {
		return err
	}

	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		closePartial(&partial)
		downloader.removePartial(root, finalName)
		return errors.New("artifact server rejected requested range")
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		return &attemptError{err: errors.New("artifact server returned a retryable status"), retryable: true}
	}

	resume := partial.offset > 0
	if resume && response.StatusCode == http.StatusPartialContent {
		expectedRange := fmt.Sprintf("bytes %d-%d/%d", partial.offset, spec.Size-1, spec.Size)
		if response.Header.Get("Content-Range") != expectedRange || response.Header.Get("ETag") != partial.etag || !isStrongETag(response.Header.Get("ETag")) {
			closePartial(&partial)
			downloader.removePartial(root, finalName)
			return &attemptError{err: errors.New("artifact server returned an untrustworthy range"), retryable: true}
		}
	} else if response.StatusCode == http.StatusOK {
		if resume {
			closePartial(&partial)
			downloader.removePartial(root, finalName)
			partial = partialDownload{}
		}
	} else {
		closePartial(&partial)
		downloader.removePartial(root, finalName)
		return errors.New("artifact server returned an unexpected status")
	}

	resumable := partial.offset > 0
	if partial.offset == 0 {
		etag := response.Header.Get("ETag")
		if isStrongETag(etag) {
			metadata := resumeMetadata{URL: spec.URL, ETag: etag, Size: spec.Size}
			if err := downloader.writeResumeMetadata(root, finalName, metadata); err != nil {
				return err
			}
			partial.etag = etag
			resumable = true
		} else {
			_ = root.Remove(finalName + ".part.meta")
		}
	}

	flags := os.O_WRONLY | os.O_CREATE
	if partial.offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	partName := finalName + ".part"
	var file *os.File
	if partial.offset > 0 {
		file = partial.file
		partial.file = nil
		if _, err := file.Seek(partial.offset, io.SeekStart); err != nil {
			_ = file.Close()
			downloader.removePartial(root, finalName)
			return errors.New("could not seek partial artifact")
		}
	} else {
		file, err = openRegularNoFollow(root, partName, flags, 0o600)
		if err != nil {
			return errors.New("could not safely open partial artifact")
		}
	}
	defer file.Close()
	remaining := spec.Size - partial.offset
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, remaining+1))
	if copyErr != nil || written != remaining {
		if written > remaining {
			_ = file.Close()
			downloader.removePartial(root, finalName)
			return errors.New("artifact response exceeds declared size")
		}
		if resumable {
			if err := downloader.options.syncFile(file); err != nil {
				_ = file.Close()
				downloader.removePartial(root, finalName)
				return errors.New("could not sync partial artifact")
			}
		}
		if err := file.Close(); err != nil {
			downloader.removePartial(root, finalName)
			return errors.New("could not close partial artifact")
		}
		if resumable {
			if err := syncDownloadDirectory(root); err != nil {
				downloader.removePartial(root, finalName)
				return errors.New("could not sync partial artifact directory")
			}
		} else {
			downloader.removePartial(root, finalName)
		}
		if ctx.Err() != nil {
			return &attemptError{err: ctx.Err(), retryable: true}
		}
		return &attemptError{err: errors.New("artifact response ended prematurely"), retryable: true}
	}
	if err := downloader.options.syncFile(file); err != nil {
		_ = file.Close()
		return errors.New("could not sync completed artifact")
	}
	completedInfo, err := file.Stat()
	if err != nil || completedInfo.Size() != spec.Size {
		_ = file.Close()
		downloader.removePartial(root, finalName)
		return errors.New("completed artifact identity is unavailable")
	}
	if err := downloader.validateStateRoot(root); err != nil {
		return err
	}
	if err := rejectUnsafeExistingPath(root, finalName); err != nil {
		return err
	}
	if downloader.options.beforeRename != nil {
		downloader.options.beforeRename()
	}
	sourceInfo, err := root.Lstat(partName)
	if err != nil || !sourceInfo.Mode().IsRegular() || !os.SameFile(completedInfo, sourceInfo) || sourceInfo.Size() != spec.Size {
		downloader.removePartial(root, finalName)
		return errors.New("completed artifact changed before rename")
	}
	if err := downloader.options.rename(root, partName, finalName); err != nil {
		return errors.New("could not atomically complete artifact")
	}
	installedInfo, installedErr := root.Lstat(finalName)
	handleInfo, handleErr := file.Stat()
	if installedErr != nil || handleErr != nil || !installedInfo.Mode().IsRegular() ||
		!os.SameFile(handleInfo, installedInfo) || installedInfo.Size() != spec.Size {
		_ = root.Remove(finalName)
		return errors.New("completed artifact changed during rename")
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(finalName)
		return errors.New("could not close completed artifact")
	}
	if err := downloader.validateStateRoot(root); err != nil {
		_ = root.Remove(finalName)
		return err
	}
	if err := root.Remove(finalName + ".part.meta"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not remove completed artifact metadata")
	}
	if err := syncDownloadDirectory(root); err != nil {
		return errors.New("could not sync completed artifact directory")
	}
	return nil
}

func validateDownloadSpec(spec DownloadSpec) error {
	limit, allowed := assetLimit(spec.Name)
	if !allowed {
		return errors.New("artifact name is not allowed")
	}
	if spec.URL == "" {
		return errors.New("artifact URL is required")
	}
	if spec.Size <= 0 || spec.MaxBytes <= 0 || spec.Size > spec.MaxBytes || spec.MaxBytes > limit {
		return errors.New("artifact size bounds are invalid")
	}
	return nil
}

func (downloader *downloader) loadPartial(root *os.Root, spec DownloadSpec, finalName string) (partialDownload, error) {
	partName := finalName + ".part"
	metadataName := partName + ".meta"
	partInfo, partErr := root.Lstat(partName)
	metadataInfo, metadataErr := root.Lstat(metadataName)
	partMissing := errors.Is(partErr, os.ErrNotExist)
	metadataMissing := errors.Is(metadataErr, os.ErrNotExist)
	if partMissing && metadataMissing {
		return partialDownload{}, nil
	}
	if partErr != nil && !partMissing || metadataErr != nil && !metadataMissing {
		return partialDownload{}, errors.New("could not inspect partial artifact state")
	}
	if partMissing != metadataMissing {
		if !partMissing && (partInfo.Mode()&os.ModeSymlink != 0 || !partInfo.Mode().IsRegular()) ||
			!metadataMissing && (metadataInfo.Mode()&os.ModeSymlink != 0 || !metadataInfo.Mode().IsRegular()) {
			return partialDownload{}, errors.New("partial artifact state is not regular")
		}
		downloader.removePartial(root, finalName)
		return partialDownload{}, nil
	}
	if !partInfo.Mode().IsRegular() || partInfo.Mode()&os.ModeSymlink != 0 ||
		!metadataInfo.Mode().IsRegular() || metadataInfo.Mode()&os.ModeSymlink != 0 {
		return partialDownload{}, errors.New("partial artifact state is not regular")
	}
	metadata, err := readResumeMetadata(root, metadataName)
	if err != nil || metadata.URL != spec.URL || metadata.Size != spec.Size || !isStrongETag(metadata.ETag) || partInfo.Size() <= 0 || partInfo.Size() >= spec.Size {
		downloader.removePartial(root, finalName)
		return partialDownload{}, nil
	}
	file, err := openRegularNoFollow(root, partName, os.O_RDWR, 0)
	if err != nil {
		return partialDownload{}, errors.New("could not safely open resumable artifact")
	}
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(partInfo, openedInfo) || openedInfo.Size() != partInfo.Size() {
		_ = file.Close()
		return partialDownload{}, errors.New("resumable artifact changed while opening")
	}
	return partialDownload{offset: openedInfo.Size(), etag: metadata.ETag, file: file}, nil
}

func closePartial(partial *partialDownload) {
	if partial.file != nil {
		_ = partial.file.Close()
		partial.file = nil
	}
}

func readResumeMetadata(root *os.Root, name string) (resumeMetadata, error) {
	file, err := openRegularNoFollow(root, name, os.O_RDONLY, 0)
	if err != nil {
		return resumeMetadata{}, err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, maxResumeMetadataBytes+1))
	data, err := io.ReadAll(reader)
	if err != nil || len(data) > maxResumeMetadataBytes {
		return resumeMetadata{}, errors.New("resume metadata is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var metadata resumeMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return resumeMetadata{}, errors.New("resume metadata is invalid")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return resumeMetadata{}, errors.New("resume metadata contains trailing data")
	}
	return metadata, nil
}

func (downloader *downloader) writeResumeMetadata(root *os.Root, finalName string, metadata resumeMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return errors.New("could not encode resume metadata")
	}
	temporary, temporaryName, err := createRootTemp(root)
	if err != nil {
		return errors.New("could not create resume metadata")
	}
	defer root.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("could not secure resume metadata")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("could not write resume metadata")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("could not sync resume metadata")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("could not close resume metadata")
	}
	if err := replaceDownloadFile(root, temporaryName, finalName+".part.meta"); err != nil {
		return errors.New("could not install resume metadata")
	}
	if err := syncDownloadDirectory(root); err != nil {
		return errors.New("could not sync resume metadata directory")
	}
	return nil
}

func (downloader *downloader) removePartial(root *os.Root, finalName string) {
	_ = root.Remove(finalName + ".part")
	_ = root.Remove(finalName + ".part.meta")
}

func rejectUnsafeExistingPath(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("could not inspect artifact destination")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("artifact destination is not a regular file")
	}
	return nil
}

func openRegularNoFollow(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	file, err := openDownloadFile(root, name, flag, perm)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("opened artifact is not regular")
	}
	pathInfo, err := root.Lstat(name)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		return nil, errors.New("artifact path changed while opening")
	}
	return file, nil
}

func createRootTemp(root *os.Root) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var randomBytes [16]byte
		if _, err := rand.Read(randomBytes[:]); err != nil {
			return nil, "", err
		}
		name := ".resume-" + hex.EncodeToString(randomBytes[:]) + ".tmp"
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not allocate resume metadata name")
}

func (downloader *downloader) validateStateRoot(root *os.Root) error {
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(downloader.stateInfo, rootInfo) {
		return errors.New("download state directory identity changed")
	}
	pathInfo, err := os.Lstat(downloader.stateDir)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(downloader.stateInfo, pathInfo) {
		return errors.New("download state directory was replaced")
	}
	return nil
}

func isStrongETag(value string) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		character := value[index]
		if character != 0x21 && (character < 0x23 || character > 0x7e) && character < 0x80 {
			return false
		}
	}
	return true
}

func defaultDownloadBackoff(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt*attempt) * 100 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
