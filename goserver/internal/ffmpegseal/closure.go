package ffmpegseal

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bilibili-live-gift-panel/internal/artifactinspect"
	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/securefile"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

const MaximumUncompressedBytes int64 = 40 << 20

var naisNetIdentity = certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}

const naisNetStructuredSigner = "C=CN;O=NaisNet Technology Co., Ltd.;SERIALNUMBER=91210103MA7CJ3C094"

type Hooks struct {
	AfterSnapshots func() error
}

type PublishHooks struct {
	AfterOpenDestination func() error
}

type Options struct {
	ArchivePath, ManifestPath, SealedDirectory string
	InspectAuthenticode                        func(string) (certidentity.Identity, error)
	Hooks                                      *Hooks
}

type PublishOptions struct {
	ArchivePath, ManifestPath, Destination string
	InspectAuthenticode                    func(string) (certidentity.Identity, error)
	Hooks                                  *PublishHooks
}

type Evidence struct {
	SchemaVersion   uint64                `json:"schemaVersion"`
	Version         string                `json:"version"`
	ArchiveSHA256   string                `json:"archiveSha256"`
	ArchiveSize     int64                 `json:"archiveSize"`
	ManifestSHA256  string                `json:"manifestSha256"`
	ManifestSize    int64                 `json:"manifestSize"`
	FFmpegSHA256    string                `json:"ffmpegSha256"`
	FFmpegSize      int64                 `json:"ffmpegSize"`
	FFmpegIdentity  certidentity.Identity `json:"ffmpegIdentity"`
	SignatureStatus string                `json:"signatureStatus"`
}

type manifest struct {
	Schema               uint64 `json:"schema"`
	ComponentFingerprint string `json:"component_fingerprint"`
	Descriptor           string `json:"descriptor"`
	DescriptorSHA256     string `json:"descriptor_sha256"`
	Version              string `json:"version"`
	SHA256               string `json:"sha256"`
	ArchiveSHA256        string `json:"archive_sha256"`
	ComponentGate        string `json:"component_gate"`
	ComponentGateSHA256  string `json:"component_gate_sha256"`
	Size                 int64  `json:"size"`
	Authenticode         bool   `json:"authenticode"`
	SignerSubject        string `json:"signer_subject"`
	SourceReleaseCommit  string `json:"source_release_commit"`
}

type verifiedPair struct {
	archive, manifest []byte
	evidence          Evidence
}

func VerifyAndSeal(options Options) (Evidence, error) {
	verified, err := verifyPair(options.ArchivePath, options.ManifestPath, options.InspectAuthenticode, options.Hooks)
	if err != nil {
		return Evidence{}, err
	}
	archive, err := securefile.WriteExactToDirectory(options.SealedDirectory, "ffmpeg.zip", verified.archive, nil)
	if err != nil {
		return Evidence{}, errors.New("sealed FFmpeg archive publication failed")
	}
	if _, err := securefile.WriteExactToDirectory(options.SealedDirectory, "manifest.json", verified.manifest, nil); err != nil {
		root, openErr := os.OpenRoot(archive.Directory)
		if openErr == nil {
			_ = root.Remove(archive.Name)
			_ = root.Close()
		}
		return Evidence{}, errors.New("sealed FFmpeg manifest publication failed")
	}
	return verified.evidence, nil
}

func PublishSealed(options PublishOptions) (Evidence, error) {
	if filepath.Base(options.ArchivePath) != "ffmpeg.zip" || filepath.Base(options.ManifestPath) != "manifest.json" {
		return Evidence{}, errors.New("sealed FFmpeg input names are invalid")
	}
	verified, err := verifyPair(options.ArchivePath, options.ManifestPath, options.InspectAuthenticode, nil)
	if err != nil {
		return Evidence{}, err
	}
	var hooks *securefile.SealHooks
	if options.Hooks != nil {
		hooks = &securefile.SealHooks{AfterOpenDirectory: options.Hooks.AfterOpenDestination}
	}
	if err := securefile.PublishExactFiles(options.Destination, []securefile.ExactFile{{Name: "ffmpeg.zip", Bytes: verified.archive}, {Name: "manifest.json", Bytes: verified.manifest}}, hooks); err != nil {
		return Evidence{}, errors.New("sealed FFmpeg destination publication failed")
	}
	return verified.evidence, nil
}

func verifyPair(archivePath, manifestPath string, inspect func(string) (certidentity.Identity, error), hooks *Hooks) (verifiedPair, error) {
	archiveSnapshot, err := securefile.SnapshotRegular(archivePath, MaximumUncompressedBytes, "gift-panel-ffmpeg-archive-", "ffmpeg.zip")
	if err != nil {
		return verifiedPair{}, errors.New("FFmpeg archive is unavailable")
	}
	defer archiveSnapshot.Close()
	manifestSnapshot, err := securefile.SnapshotRegular(manifestPath, 64<<10, "gift-panel-ffmpeg-manifest-", "manifest.json")
	if err != nil {
		return verifiedPair{}, errors.New("FFmpeg manifest is unavailable")
	}
	defer manifestSnapshot.Close()
	if hooks != nil && hooks.AfterSnapshots != nil {
		if err := hooks.AfterSnapshots(); err != nil {
			return verifiedPair{}, errors.New("FFmpeg source changed after snapshot")
		}
	}
	parsed, err := parseManifest(manifestSnapshot.Bytes, archiveSnapshot.Bytes)
	if err != nil {
		return verifiedPair{}, err
	}
	ffmpeg, err := extractBoundedFFmpeg(archiveSnapshot.Bytes, parsed.Size)
	if err != nil {
		return verifiedPair{}, err
	}
	if hashHex(ffmpeg) != parsed.SHA256 {
		return verifiedPair{}, errors.New("FFmpeg entry hash does not match manifest")
	}
	if inspect == nil {
		inspect = artifactinspect.InspectAuthenticodeFile
	}
	ffmpegSnapshot, err := securefile.SnapshotBytes(ffmpeg, "gift-panel-ffmpeg-entry-", "ffmpeg.exe")
	if err != nil {
		return verifiedPair{}, errors.New("FFmpeg Authenticode snapshot is unavailable")
	}
	defer ffmpegSnapshot.Close()
	identity, err := inspect(ffmpegSnapshot.Path)
	if err != nil || identity != naisNetIdentity {
		return verifiedPair{}, errors.New("FFmpeg Authenticode identity is not exact NaisNet")
	}
	if err := ffmpegSnapshot.Revalidate(); err != nil {
		return verifiedPair{}, errors.New("FFmpeg Authenticode snapshot changed")
	}
	if err := archiveSnapshot.Revalidate(); err != nil {
		return verifiedPair{}, errors.New("FFmpeg archive snapshot changed")
	}
	if err := manifestSnapshot.Revalidate(); err != nil {
		return verifiedPair{}, errors.New("FFmpeg manifest snapshot changed")
	}
	return verifiedPair{archive: append([]byte(nil), archiveSnapshot.Bytes...), manifest: append([]byte(nil), manifestSnapshot.Bytes...), evidence: Evidence{
		SchemaVersion: 1, Version: parsed.Version,
		ArchiveSHA256: hashHex(archiveSnapshot.Bytes), ArchiveSize: int64(len(archiveSnapshot.Bytes)),
		ManifestSHA256: hashHex(manifestSnapshot.Bytes), ManifestSize: int64(len(manifestSnapshot.Bytes)),
		FFmpegSHA256: parsed.SHA256, FFmpegSize: parsed.Size, FFmpegIdentity: identity, SignatureStatus: "Valid",
	}}, nil
}

func parseManifest(contents, archive []byte) (manifest, error) {
	if err := updatepolicy.ValidateJSON(contents); err != nil {
		return manifest{}, errors.New("FFmpeg manifest JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var value manifest
	if err := decoder.Decode(&value); err != nil {
		return manifest{}, errors.New("FFmpeg manifest schema is invalid")
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return manifest{}, errors.New("FFmpeg manifest has trailing data")
	}
	if value.Schema != 1 || value.Version != "9.0" || value.Size <= 0 || value.Size > MaximumUncompressedBytes || !value.Authenticode || value.SignerSubject != naisNetStructuredSigner ||
		!isLowerHex(value.ComponentFingerprint, 64) || !isLowerHex(value.DescriptorSHA256, 64) || !isLowerHex(value.SHA256, 64) || !isLowerHex(value.ArchiveSHA256, 64) || !isLowerHex(value.ComponentGateSHA256, 64) ||
		!isLowerHex(value.SourceReleaseCommit, 40) || strings.Trim(value.SourceReleaseCommit, "0") == "" || value.Descriptor == "" || value.ComponentGate == "" {
		return manifest{}, errors.New("FFmpeg manifest binding is invalid")
	}
	if hashHex([]byte(value.Descriptor)) != value.DescriptorSHA256 || value.ComponentFingerprint != value.DescriptorSHA256 || hashHex([]byte(value.ComponentGate)) != value.ComponentGateSHA256 || hashHex(archive) != value.ArchiveSHA256 {
		return manifest{}, errors.New("FFmpeg manifest digest binding is invalid")
	}
	return value, nil
}

func extractBoundedFFmpeg(archive []byte, expectedSize int64) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || len(reader.File) != 1 {
		return nil, errors.New("FFmpeg archive closure is invalid")
	}
	var total uint64
	for _, entry := range reader.File {
		if entry.UncompressedSize64 > uint64(MaximumUncompressedBytes) || total > uint64(MaximumUncompressedBytes)-entry.UncompressedSize64 {
			return nil, errors.New("FFmpeg archive uncompressed size exceeds policy")
		}
		total += entry.UncompressedSize64
	}
	if total > uint64(MaximumUncompressedBytes) {
		return nil, errors.New("FFmpeg archive total uncompressed size exceeds policy")
	}
	entry := reader.File[0]
	if entry.Name != "ffmpeg.exe" || entry.FileInfo().IsDir() || !entry.Mode().IsRegular() || entry.Flags&1 != 0 || entry.UncompressedSize64 != uint64(expectedSize) {
		return nil, errors.New("FFmpeg archive entry binding is invalid")
	}
	opened, err := entry.Open()
	if err != nil {
		return nil, errors.New("FFmpeg archive entry cannot be opened")
	}
	defer opened.Close()
	contents, err := io.ReadAll(io.LimitReader(opened, MaximumUncompressedBytes+1))
	if err != nil || int64(len(contents)) != expectedSize || int64(len(contents)) > MaximumUncompressedBytes {
		return nil, errors.New("FFmpeg archive entry read exceeds policy")
	}
	return contents, nil
}

func hashHex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func isLowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
