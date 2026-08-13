package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

//go:embed ffmpeg/ffmpeg.zip ffmpeg/manifest.json
var giftClipFFmpegFS embed.FS

type giftClipFFmpegManifest struct {
	Version             string `json:"version"`
	SHA256              string `json:"sha256"`
	ArchiveSHA256       string `json:"archive_sha256"`
	ComponentGate       string `json:"component_gate"`
	ComponentGateSHA256 string `json:"component_gate_sha256"`
	Size                int64  `json:"size"`
	Authenticode        bool   `json:"authenticode"`
}

type giftClipPayload struct {
	Archive   []byte
	Manifest  giftClipFFmpegManifest
	CacheRoot string

	mu sync.Mutex
}

var giftClipManifestVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]*$`)
var giftClipCacheLocks sync.Map

func embeddedGiftClipPayload(cacheRoot string) (*giftClipPayload, error) {
	archive, err := giftClipFFmpegFS.ReadFile("ffmpeg/ffmpeg.zip")
	if err != nil {
		return nil, fmt.Errorf("%w: embedded archive unavailable", errGiftClipPayloadIntegrity)
	}
	manifestData, err := giftClipFFmpegFS.ReadFile("ffmpeg/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("%w: embedded manifest unavailable", errGiftClipPayloadIntegrity)
	}
	manifest, err := parseGiftClipFFmpegManifest(manifestData)
	if err != nil {
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

func parseGiftClipFFmpegManifest(data []byte) (giftClipFFmpegManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return giftClipFFmpegManifest{}, fmt.Errorf("%w: manifest must be one object", errGiftClipPayloadIntegrity)
	}
	var manifest giftClipFFmpegManifest
	seen := make(map[string]bool, 7)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return giftClipFFmpegManifest{}, fmt.Errorf("%w: manifest field is invalid or duplicated", errGiftClipPayloadIntegrity)
		}
		seen[key] = true
		switch key {
		case "version":
			err = decoder.Decode(&manifest.Version)
		case "sha256":
			err = decoder.Decode(&manifest.SHA256)
		case "archive_sha256":
			err = decoder.Decode(&manifest.ArchiveSHA256)
		case "component_gate_sha256":
			err = decoder.Decode(&manifest.ComponentGateSHA256)
		case "component_gate":
			err = decoder.Decode(&manifest.ComponentGate)
		case "size":
			err = decoder.Decode(&manifest.Size)
		case "authenticode":
			err = decoder.Decode(&manifest.Authenticode)
		default:
			return giftClipFFmpegManifest{}, fmt.Errorf("%w: unknown manifest field", errGiftClipPayloadIntegrity)
		}
		if err != nil {
			return giftClipFFmpegManifest{}, fmt.Errorf("%w: manifest field value is invalid", errGiftClipPayloadIntegrity)
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 7 {
		return giftClipFFmpegManifest{}, fmt.Errorf("%w: manifest object is incomplete", errGiftClipPayloadIntegrity)
	}
	if token, err = decoder.Token(); err != io.EOF {
		return giftClipFFmpegManifest{}, fmt.Errorf("%w: trailing manifest JSON", errGiftClipPayloadIntegrity)
	}
	if err := validateGiftClipFFmpegManifest(manifest); err != nil {
		return giftClipFFmpegManifest{}, err
	}
	return manifest, nil
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
	if !giftClipManifestVersionPattern.MatchString(manifest.Version) || manifest.Size <= 0 || len(manifest.ComponentGate) == 0 || len(manifest.ComponentGate) > 16_384 {
		return fmt.Errorf("%w: invalid manifest", errGiftClipPayloadIntegrity)
	}
	componentGateHash := sha256.Sum256([]byte(manifest.ComponentGate))
	if hex.EncodeToString(componentGateHash[:]) != manifest.ComponentGateSHA256 {
		return fmt.Errorf("%w: component gate hash mismatch", errGiftClipPayloadIntegrity)
	}
	for _, value := range []string{manifest.SHA256, manifest.ArchiveSHA256, manifest.ComponentGateSHA256} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
			return fmt.Errorf("%w: invalid manifest hash", errGiftClipPayloadIntegrity)
		}
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
	archiveHash := sha256.Sum256(payload.Archive)
	if hex.EncodeToString(archiveHash[:]) != payload.Manifest.ArchiveSHA256 {
		return "", fmt.Errorf("%w: archive hash mismatch", errGiftClipPayloadIntegrity)
	}
	if err := validateGiftClipZipShape(payload.Archive, payload.Manifest); err != nil {
		return "", err
	}
	reader, err := zip.NewReader(bytes.NewReader(payload.Archive), int64(len(payload.Archive)))
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

func validateGiftClipZipShape(archive []byte, manifest giftClipFFmpegManifest) error {
	fail := func(reason string) error { return fmt.Errorf("%w: %s", errGiftClipPayloadIntegrity, reason) }
	if len(archive) < 22 || len(archive) > 40_000_000 {
		return fail("archive size is invalid")
	}
	eocd := len(archive) - 22
	if binary.LittleEndian.Uint32(archive[eocd:eocd+4]) != 0x06054b50 ||
		binary.LittleEndian.Uint16(archive[eocd+4:eocd+6]) != 0 ||
		binary.LittleEndian.Uint16(archive[eocd+6:eocd+8]) != 0 ||
		binary.LittleEndian.Uint16(archive[eocd+8:eocd+10]) != 1 ||
		binary.LittleEndian.Uint16(archive[eocd+10:eocd+12]) != 1 ||
		binary.LittleEndian.Uint16(archive[eocd+20:eocd+22]) != 0 {
		return fail("end record is invalid")
	}
	centralSize := uint64(binary.LittleEndian.Uint32(archive[eocd+12 : eocd+16]))
	centralOffset := uint64(binary.LittleEndian.Uint32(archive[eocd+16 : eocd+20]))
	if centralOffset > uint64(eocd) || centralSize > uint64(eocd)-centralOffset || centralOffset+centralSize != uint64(eocd) || centralSize < 46 {
		return fail("central directory bounds are invalid")
	}
	central := archive[int(centralOffset):eocd]
	if binary.LittleEndian.Uint32(central[0:4]) != 0x02014b50 || central[5] != 3 ||
		binary.LittleEndian.Uint16(central[8:10]) != 0x0800 ||
		binary.LittleEndian.Uint16(central[10:12]) != 8 ||
		binary.LittleEndian.Uint16(central[30:32]) != 0 ||
		binary.LittleEndian.Uint16(central[32:34]) != 0 ||
		binary.LittleEndian.Uint16(central[34:36]) != 0 ||
		binary.LittleEndian.Uint32(central[42:46]) != 0 {
		return fail("central entry metadata is invalid")
	}
	nameLength := uint64(binary.LittleEndian.Uint16(central[28:30]))
	if 46+nameLength != centralSize || nameLength != uint64(len("ffmpeg.exe")) || string(central[46:46+nameLength]) != "ffmpeg.exe" {
		return fail("central entry name or boundaries are invalid")
	}
	mode := binary.LittleEndian.Uint32(central[38:42]) >> 16
	if mode&0o170000 != 0o100000 {
		return fail("central entry is not a regular file")
	}
	compressedSize := uint64(binary.LittleEndian.Uint32(central[20:24]))
	uncompressedSize := uint64(binary.LittleEndian.Uint32(central[24:28]))
	if compressedSize > 40_000_000 || uncompressedSize > 40_000_000 || uncompressedSize != uint64(manifest.Size) {
		return fail("entry size is invalid")
	}
	if centralOffset < 30 || binary.LittleEndian.Uint32(archive[0:4]) != 0x04034b50 ||
		binary.LittleEndian.Uint16(archive[6:8]) != 0x0800 ||
		binary.LittleEndian.Uint16(archive[8:10]) != 8 ||
		binary.LittleEndian.Uint16(archive[28:30]) != 0 {
		return fail("local entry metadata is invalid")
	}
	localNameLength := uint64(binary.LittleEndian.Uint16(archive[26:28]))
	dataOffset := uint64(30) + localNameLength
	if localNameLength != nameLength || dataOffset > centralOffset || dataOffset+compressedSize != centralOffset ||
		string(archive[30:dataOffset]) != "ffmpeg.exe" ||
		binary.LittleEndian.Uint16(archive[6:8]) != binary.LittleEndian.Uint16(central[8:10]) ||
		binary.LittleEndian.Uint16(archive[8:10]) != binary.LittleEndian.Uint16(central[10:12]) ||
		binary.LittleEndian.Uint32(archive[14:18]) != binary.LittleEndian.Uint32(central[16:20]) ||
		binary.LittleEndian.Uint32(archive[18:22]) != binary.LittleEndian.Uint32(central[20:24]) ||
		binary.LittleEndian.Uint32(archive[22:26]) != binary.LittleEndian.Uint32(central[24:28]) {
		return fail("local and central entries differ")
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

func defaultGiftClipCacheRoot() string {
	root, err := os.UserCacheDir()
	if err != nil || root == "" {
		return ""
	}
	return filepath.Join(root, "BilibiliLiveGiftPanel", "gift-clip", "ffmpeg")
}
