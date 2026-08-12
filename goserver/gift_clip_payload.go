package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"unsafe"
)

//go:embed ffmpeg/ffmpeg.zip ffmpeg/manifest.json
var giftClipFFmpegFS embed.FS

type giftClipFFmpegManifest struct {
	Version      string `json:"version"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Authenticode bool   `json:"authenticode"`
}

type giftClipPayload struct {
	Archive   []byte
	Manifest  giftClipFFmpegManifest
	CacheRoot string

	mu sync.Mutex
}

var giftClipManifestVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]*$`)
var giftClipCacheLocks sync.Map
var giftClipMoveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

const (
	giftClipMoveFileReplaceExisting = 0x1
	giftClipMoveFileWriteThrough    = 0x8
)

func embeddedGiftClipPayload(cacheRoot string) (*giftClipPayload, error) {
	archive, err := giftClipFFmpegFS.ReadFile("ffmpeg/ffmpeg.zip")
	if err != nil {
		return nil, fmt.Errorf("%w: embedded archive unavailable", errGiftClipPayloadIntegrity)
	}
	manifestData, err := giftClipFFmpegFS.ReadFile("ffmpeg/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("%w: embedded manifest unavailable", errGiftClipPayloadIntegrity)
	}
	var manifest giftClipFFmpegManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("%w: embedded manifest invalid", errGiftClipPayloadIntegrity)
	}
	if err := validateGiftClipFFmpegManifest(manifest); err != nil {
		return nil, err
	}
	if cacheRoot == "" {
		cacheRoot = defaultGiftClipCacheRoot()
	}
	if cacheRoot == "" {
		return nil, fmt.Errorf("%w: cache root unavailable", errGiftClipPayloadIntegrity)
	}
	return &giftClipPayload{Archive: archive, Manifest: manifest, CacheRoot: cacheRoot}, nil
}

func (payload *giftClipPayload) Prepare(ctx context.Context) (string, error) {
	payload.mu.Lock()
	defer payload.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateGiftClipFFmpegManifest(payload.Manifest); err != nil {
		return "", err
	}
	cacheRoot, err := filepath.Abs(payload.CacheRoot)
	if err != nil || payload.CacheRoot == "" {
		return "", fmt.Errorf("%w: invalid cache root", errGiftClipPayloadIntegrity)
	}
	targetDir := filepath.Join(cacheRoot, payload.Manifest.Version+"-"+payload.Manifest.SHA256[:12])
	target := filepath.Join(targetDir, "ffmpeg.exe")
	cacheLock, _ := giftClipCacheLocks.LoadOrStore(target, &sync.Mutex{})
	cacheLock.(*sync.Mutex).Lock()
	defer cacheLock.(*sync.Mutex).Unlock()
	if giftClipFileMatches(target, payload.Manifest) {
		return target, nil
	}
	return payload.extractAtomically(ctx, targetDir, target)
}

func validateGiftClipFFmpegManifest(manifest giftClipFFmpegManifest) error {
	if !giftClipManifestVersionPattern.MatchString(manifest.Version) || manifest.Size <= 0 || len(manifest.SHA256) != sha256.Size*2 {
		return fmt.Errorf("%w: invalid manifest", errGiftClipPayloadIntegrity)
	}
	decoded, err := hex.DecodeString(manifest.SHA256)
	if err != nil || hex.EncodeToString(decoded) != manifest.SHA256 {
		return fmt.Errorf("%w: invalid manifest hash", errGiftClipPayloadIntegrity)
	}
	return nil
}

func giftClipFileMatches(path string, manifest giftClipFFmpegManifest) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != manifest.Size {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == manifest.SHA256
}

func (payload *giftClipPayload) extractAtomically(ctx context.Context, targetDir, target string) (string, error) {
	reader, err := zip.NewReader(newByteReader(payload.Archive), int64(len(payload.Archive)))
	if err != nil || len(reader.File) != 1 {
		return "", fmt.Errorf("%w: invalid archive", errGiftClipPayloadIntegrity)
	}
	entry := reader.File[0]
	if entry.Name != "ffmpeg.exe" || entry.FileInfo().IsDir() || !entry.Mode().IsRegular() || entry.UncompressedSize64 != uint64(payload.Manifest.Size) {
		return "", fmt.Errorf("%w: unsafe archive entry", errGiftClipPayloadIntegrity)
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return "", err
	}
	directoryInfo, err := os.Lstat(targetDir)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: unsafe cache directory", errGiftClipPayloadIntegrity)
	}

	partial, err := os.CreateTemp(targetDir, ".partial-*")
	if err != nil {
		return "", err
	}
	partialPath := partial.Name()
	keepPartial := false
	defer func() {
		if !keepPartial {
			_ = os.Remove(partialPath)
		}
	}()

	archiveFile, err := entry.Open()
	if err != nil {
		_ = partial.Close()
		return "", fmt.Errorf("%w: archive entry cannot be read", errGiftClipPayloadIntegrity)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(partial, hash), contextReader{ctx: ctx, reader: archiveFile})
	closeArchiveErr := archiveFile.Close()
	syncErr := partial.Sync()
	chmodErr := partial.Chmod(0o700)
	closePartialErr := partial.Close()
	if copyErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("%w: archive extraction failed", errGiftClipPayloadIntegrity)
	}
	if closeArchiveErr != nil || syncErr != nil || chmodErr != nil || closePartialErr != nil {
		return "", fmt.Errorf("%w: extracted file could not be finalized", errGiftClipPayloadIntegrity)
	}
	if written != payload.Manifest.Size || hex.EncodeToString(hash.Sum(nil)) != payload.Manifest.SHA256 || !giftClipFileMatches(partialPath, payload.Manifest) {
		return "", fmt.Errorf("%w: extracted file mismatch", errGiftClipPayloadIntegrity)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("%w: executable target is a directory", errGiftClipPayloadIntegrity)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := replaceGiftClipFileAtomically(partialPath, target); err != nil {
		return "", err
	}
	keepPartial = true
	if !giftClipFileMatches(target, payload.Manifest) {
		_ = os.Remove(target)
		return "", fmt.Errorf("%w: installed file mismatch", errGiftClipPayloadIntegrity)
	}
	return target, nil
}

func replaceGiftClipFileAtomically(source, target string) error {
	sourcePointer, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPointer, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	result, _, callErr := giftClipMoveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePointer)),
		uintptr(unsafe.Pointer(targetPointer)),
		giftClipMoveFileReplaceExisting|giftClipMoveFileWriteThrough,
	)
	if result == 0 {
		return callErr
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

type byteReaderAt struct {
	data []byte
}

func newByteReader(data []byte) *byteReaderAt {
	return &byteReaderAt{data: data}
}

func (reader *byteReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < 0 || offset >= int64(len(reader.data)) {
		return 0, io.EOF
	}
	read := copy(buffer, reader.data[offset:])
	if read < len(buffer) {
		return read, io.EOF
	}
	return read, nil
}

func defaultGiftClipCacheRoot() string {
	root, err := os.UserCacheDir()
	if err != nil || root == "" {
		return ""
	}
	return filepath.Join(root, "BilibiliLiveGiftPanel", "gift-clip", "ffmpeg")
}
