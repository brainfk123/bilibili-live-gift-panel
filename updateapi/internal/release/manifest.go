package release

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	updateAssetName   = "gift-panel-windows-x64.exe"
	changelogFileName = "gift-panel-changelog.json"
	maxAssetBytes     = int64(256 << 20)
	maxChangelogBytes = 2 << 20
)

type ChannelManifest struct {
	SchemaVersion      int           `json:"schemaVersion"`
	TagName            string        `json:"tagName"`
	PublishedAt        string        `json:"publishedAt"`
	Asset              AssetManifest `json:"asset"`
	ChangelogObjectKey string        `json:"changelogObjectKey"`
}

type AssetManifest struct {
	Name      string `json:"name"`
	ObjectKey string `json:"objectKey"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

type PublicRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []PublicAsset `json:"assets"`
}

type PublicAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
}

type ChangelogDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	Releases      []json.RawMessage `json:"releases"`
}

func ParseChannelManifest(data []byte) (ChannelManifest, error) {
	var manifest ChannelManifest
	if err := decodeJSON(data, &manifest); err != nil {
		return ChannelManifest{}, fmt.Errorf("parse channel manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return ChannelManifest{}, err
	}
	return manifest, nil
}

func (manifest ChannelManifest) Validate() error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)
	}
	if _, err := parseStableVersion(manifest.TagName); err != nil {
		return fmt.Errorf("invalid release tag: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, manifest.PublishedAt); err != nil {
		return fmt.Errorf("invalid published timestamp: %w", err)
	}
	if manifest.Asset.Name != updateAssetName {
		return fmt.Errorf("unexpected asset name %q", manifest.Asset.Name)
	}
	if manifest.Asset.Size <= 0 || manifest.Asset.Size > maxAssetBytes {
		return fmt.Errorf("invalid asset size %d", manifest.Asset.Size)
	}
	if len(manifest.Asset.SHA256) != 64 {
		return errors.New("asset SHA-256 must be 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(manifest.Asset.SHA256); err != nil {
		return fmt.Errorf("invalid asset SHA-256: %w", err)
	}

	releasePrefix := "releases/" + manifest.TagName + "/"
	if hasPathTraversal(manifest.Asset.ObjectKey) || manifest.Asset.ObjectKey != releasePrefix+updateAssetName {
		return fmt.Errorf("asset key must be %q", releasePrefix+updateAssetName)
	}
	if hasPathTraversal(manifest.ChangelogObjectKey) || manifest.ChangelogObjectKey != releasePrefix+changelogFileName {
		return fmt.Errorf("changelog key must be %q", releasePrefix+changelogFileName)
	}
	return nil
}

func (manifest ChannelManifest) Public(downloadURL string) PublicRelease {
	return PublicRelease{
		TagName: manifest.TagName,
		Assets: []PublicAsset{{
			Name:        manifest.Asset.Name,
			DownloadURL: downloadURL,
			Size:        manifest.Asset.Size,
			Digest:      "sha256:" + manifest.Asset.SHA256,
		}},
	}
}

func ParseChangelog(data []byte) (ChangelogDocument, error) {
	if len(data) > maxChangelogBytes {
		return ChangelogDocument{}, errors.New("changelog exceeds 2 MiB")
	}
	var document ChangelogDocument
	if err := decodeJSON(data, &document); err != nil {
		return ChangelogDocument{}, fmt.Errorf("parse changelog: %w", err)
	}
	if document.SchemaVersion != 1 || len(document.Releases) == 0 {
		return ChangelogDocument{}, errors.New("invalid changelog document")
	}
	return document, nil
}

func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("JSON document contains multiple values")
		}
		return err
	}
	return nil
}

func parseStableVersion(value string) ([3]int, error) {
	normalized := strings.TrimPrefix(strings.TrimSpace(value), "v")
	if normalized == "" || strings.Contains(normalized, "-") {
		return [3]int{}, fmt.Errorf("%q is not a stable version", value)
	}
	if metadata := strings.IndexByte(normalized, '+'); metadata >= 0 {
		normalized = normalized[:metadata]
	}
	parts := strings.Split(normalized, ".")
	if len(parts) != 3 {
		return [3]int{}, fmt.Errorf("%q must use major.minor.patch", value)
	}
	var result [3]int
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return [3]int{}, fmt.Errorf("%q contains an invalid number", value)
		}
		result[index] = number
	}
	return result, nil
}

func hasPathTraversal(key string) bool {
	for _, segment := range strings.Split(key, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}
