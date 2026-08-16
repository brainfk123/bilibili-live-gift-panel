package mirror

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	rename         func(string, string) error
}

type downloader struct {
	client   *http.Client
	stateDir string
	options  downloaderOptions
	mu       sync.Mutex
}

type partialDownload struct {
	offset int64
	etag   string
}

type attemptError struct {
	err       error
	retryable bool
}

func (err *attemptError) Error() string { return err.err.Error() }
func (err *attemptError) Unwrap() error { return err.err }

func NewDownloader(client *http.Client, stateDir string) (ArtifactFetcher, error) {
	return newDownloaderWithOptions(client, stateDir, downloaderOptions{
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
	return &downloader{client: client, stateDir: stateDir, options: options}, nil
}

func (downloader *downloader) Download(ctx context.Context, spec DownloadSpec) (string, error) {
	if err := validateDownloadSpec(spec); err != nil {
		return "", err
	}
	finalPath := filepath.Join(downloader.stateDir, spec.Name)
	if filepath.Dir(finalPath) != downloader.stateDir {
		return "", errors.New("artifact path escapes download state directory")
	}

	downloader.mu.Lock()
	defer downloader.mu.Unlock()

	downloadCtx, cancel := context.WithTimeout(ctx, downloader.options.overallTimeout)
	defer cancel()
	for attempt := 1; attempt <= downloader.options.maxAttempts; attempt++ {
		if err := downloadCtx.Err(); err != nil {
			return "", err
		}
		err := downloader.downloadOnce(downloadCtx, spec, finalPath)
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

func (downloader *downloader) downloadOnce(ctx context.Context, spec DownloadSpec, finalPath string) error {
	if err := rejectUnsafeExistingPath(finalPath); err != nil {
		return err
	}
	partial, err := downloader.loadPartial(spec, finalPath)
	if err != nil {
		return err
	}

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

	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		downloader.removePartial(finalPath)
		return errors.New("artifact server rejected requested range")
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		return &attemptError{err: errors.New("artifact server returned a retryable status"), retryable: true}
	}

	resume := partial.offset > 0
	if resume && response.StatusCode == http.StatusPartialContent {
		expectedRange := fmt.Sprintf("bytes %d-%d/%d", partial.offset, spec.Size-1, spec.Size)
		if response.Header.Get("Content-Range") != expectedRange || response.Header.Get("ETag") != partial.etag || !isStrongETag(response.Header.Get("ETag")) {
			downloader.removePartial(finalPath)
			return &attemptError{err: errors.New("artifact server returned an untrustworthy range"), retryable: true}
		}
	} else if response.StatusCode == http.StatusOK {
		if resume {
			downloader.removePartial(finalPath)
			partial = partialDownload{}
		}
	} else {
		downloader.removePartial(finalPath)
		return errors.New("artifact server returned an unexpected status")
	}

	resumable := partial.offset > 0
	if partial.offset == 0 {
		etag := response.Header.Get("ETag")
		if isStrongETag(etag) {
			metadata := resumeMetadata{URL: spec.URL, ETag: etag, Size: spec.Size}
			if err := downloader.writeResumeMetadata(finalPath, metadata); err != nil {
				return err
			}
			partial.etag = etag
			resumable = true
		} else {
			_ = os.Remove(finalPath + ".part.meta")
		}
	}

	flags := os.O_WRONLY | os.O_CREATE
	if partial.offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	partPath := finalPath + ".part"
	file, err := openRegularNoFollow(partPath, flags, 0o600)
	if err != nil {
		return errors.New("could not safely open partial artifact")
	}
	remaining := spec.Size - partial.offset
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, remaining+1))
	if copyErr != nil || written != remaining {
		_ = file.Close()
		if written > remaining {
			downloader.removePartial(finalPath)
			return errors.New("artifact response exceeds declared size")
		}
		if !resumable {
			downloader.removePartial(finalPath)
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
	if err := file.Close(); err != nil {
		return errors.New("could not close completed artifact")
	}
	if err := rejectUnsafeExistingPath(finalPath); err != nil {
		return err
	}
	if err := downloader.options.rename(partPath, finalPath); err != nil {
		return errors.New("could not atomically complete artifact")
	}
	if err := os.Remove(finalPath + ".part.meta"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("could not remove completed artifact metadata")
	}
	if err := syncDownloadDirectory(downloader.stateDir); err != nil {
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

func (downloader *downloader) loadPartial(spec DownloadSpec, finalPath string) (partialDownload, error) {
	partPath := finalPath + ".part"
	metadataPath := partPath + ".meta"
	partInfo, partErr := os.Lstat(partPath)
	metadataInfo, metadataErr := os.Lstat(metadataPath)
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
		downloader.removePartial(finalPath)
		return partialDownload{}, nil
	}
	if !partInfo.Mode().IsRegular() || partInfo.Mode()&os.ModeSymlink != 0 ||
		!metadataInfo.Mode().IsRegular() || metadataInfo.Mode()&os.ModeSymlink != 0 {
		return partialDownload{}, errors.New("partial artifact state is not regular")
	}
	metadata, err := readResumeMetadata(metadataPath)
	if err != nil || metadata.URL != spec.URL || metadata.Size != spec.Size || !isStrongETag(metadata.ETag) || partInfo.Size() <= 0 || partInfo.Size() >= spec.Size {
		downloader.removePartial(finalPath)
		return partialDownload{}, nil
	}
	return partialDownload{offset: partInfo.Size(), etag: metadata.ETag}, nil
}

func readResumeMetadata(path string) (resumeMetadata, error) {
	file, err := openRegularNoFollow(path, os.O_RDONLY, 0)
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

func (downloader *downloader) writeResumeMetadata(finalPath string, metadata resumeMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return errors.New("could not encode resume metadata")
	}
	temporary, err := os.CreateTemp(downloader.stateDir, ".resume-*.tmp")
	if err != nil {
		return errors.New("could not create resume metadata")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
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
	if err := replaceDownloadFile(temporaryPath, finalPath+".part.meta"); err != nil {
		return errors.New("could not install resume metadata")
	}
	if err := syncDownloadDirectory(downloader.stateDir); err != nil {
		return errors.New("could not sync resume metadata directory")
	}
	return nil
}

func (downloader *downloader) removePartial(finalPath string) {
	_ = os.Remove(finalPath + ".part")
	_ = os.Remove(finalPath + ".part.meta")
}

func rejectUnsafeExistingPath(path string) error {
	info, err := os.Lstat(path)
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

func openRegularNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	file, err := openDownloadFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("opened artifact is not regular")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		return nil, errors.New("artifact path changed while opening")
	}
	return file, nil
}

func isStrongETag(value string) bool {
	return len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' && !strings.HasPrefix(value, "W/")
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
