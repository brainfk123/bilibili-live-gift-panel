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

type ValidationCode string

const (
	ValidationManifestJSON         ValidationCode = "manifest_json"
	ValidationManifestSchema       ValidationCode = "manifest_schema"
	ValidationManifestTag          ValidationCode = "manifest_tag"
	ValidationManifestPublishedAt  ValidationCode = "manifest_published_at"
	ValidationManifestAssetName    ValidationCode = "manifest_asset_name"
	ValidationManifestAssetSize    ValidationCode = "manifest_asset_size"
	ValidationManifestAssetSHA256  ValidationCode = "manifest_asset_sha256"
	ValidationManifestAssetKey     ValidationCode = "manifest_asset_key"
	ValidationManifestChangelogKey ValidationCode = "manifest_changelog_key"
	ValidationChangelogSize        ValidationCode = "changelog_size"
	ValidationChangelogJSON        ValidationCode = "changelog_json"
	ValidationChangelogSchema      ValidationCode = "changelog_schema"
)

type validationError struct {
	code  ValidationCode
	cause error
}

func (err validationError) Error() string { return err.cause.Error() }
func (err validationError) Unwrap() error { return err.cause }

func validationFailure(code ValidationCode, cause error) error {
	return validationError{code: code, cause: cause}
}

// ValidationCodeOf returns a stable, non-sensitive diagnosis for invalid release metadata.
func ValidationCodeOf(err error) ValidationCode {
	var validation validationError
	if errors.As(err, &validation) {
		return validation.code
	}
	return ""
}

func IsValidationCode(value string) bool {
	switch ValidationCode(value) {
	case ValidationManifestJSON, ValidationManifestSchema, ValidationManifestTag, ValidationManifestPublishedAt,
		ValidationManifestAssetName, ValidationManifestAssetSize, ValidationManifestAssetSHA256,
		ValidationManifestAssetKey, ValidationManifestChangelogKey, ValidationChangelogSize,
		ValidationChangelogJSON, ValidationChangelogSchema:
		return true
	default:
		return false
	}
}

func ParseChannelManifest(data []byte) (ChannelManifest, error) {
	var manifest ChannelManifest
	if err := decodeJSON(data, &manifest); err != nil {
		return ChannelManifest{}, validationFailure(ValidationManifestJSON, fmt.Errorf("parse channel manifest: %w", err))
	}
	if err := manifest.Validate(); err != nil {
		return ChannelManifest{}, err
	}
	return manifest, nil
}

func (manifest ChannelManifest) Validate() error {
	if manifest.SchemaVersion != 1 {
		return validationFailure(ValidationManifestSchema, fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion))
	}
	if _, err := parseStableVersion(manifest.TagName); err != nil {
		return validationFailure(ValidationManifestTag, fmt.Errorf("invalid release tag: %w", err))
	}
	if _, err := time.Parse(time.RFC3339, manifest.PublishedAt); err != nil {
		return validationFailure(ValidationManifestPublishedAt, fmt.Errorf("invalid published timestamp: %w", err))
	}
	if manifest.Asset.Name != updateAssetName {
		return validationFailure(ValidationManifestAssetName, fmt.Errorf("unexpected asset name %q", manifest.Asset.Name))
	}
	if manifest.Asset.Size <= 0 || manifest.Asset.Size > maxAssetBytes {
		return validationFailure(ValidationManifestAssetSize, fmt.Errorf("invalid asset size %d", manifest.Asset.Size))
	}
	if len(manifest.Asset.SHA256) != 64 {
		return validationFailure(ValidationManifestAssetSHA256, errors.New("asset SHA-256 must be 64 hexadecimal characters"))
	}
	if _, err := hex.DecodeString(manifest.Asset.SHA256); err != nil {
		return validationFailure(ValidationManifestAssetSHA256, fmt.Errorf("invalid asset SHA-256: %w", err))
	}

	releasePrefix := "releases/" + manifest.TagName + "/"
	if hasPathTraversal(manifest.Asset.ObjectKey) || manifest.Asset.ObjectKey != releasePrefix+updateAssetName {
		return validationFailure(ValidationManifestAssetKey, fmt.Errorf("asset key must be %q", releasePrefix+updateAssetName))
	}
	if hasPathTraversal(manifest.ChangelogObjectKey) || manifest.ChangelogObjectKey != releasePrefix+changelogFileName {
		return validationFailure(ValidationManifestChangelogKey, fmt.Errorf("changelog key must be %q", releasePrefix+changelogFileName))
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
		return ChangelogDocument{}, validationFailure(ValidationChangelogSize, errors.New("changelog exceeds 2 MiB"))
	}
	var document ChangelogDocument
	if err := decodeJSON(data, &document); err != nil {
		return ChangelogDocument{}, validationFailure(ValidationChangelogJSON, fmt.Errorf("parse changelog: %w", err))
	}
	if document.SchemaVersion != 1 || len(document.Releases) == 0 {
		return ChangelogDocument{}, validationFailure(ValidationChangelogSchema, errors.New("invalid changelog document"))
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
	if len(value) < 2 || value[0] != 'v' {
		return [3]int{}, fmt.Errorf("%q is not a stable version", value)
	}
	parts := strings.Split(value[1:], ".")
	if len(parts) != 3 {
		return [3]int{}, fmt.Errorf("%q must use vMAJOR.MINOR.PATCH", value)
	}
	var result [3]int
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return [3]int{}, fmt.Errorf("%q contains a noncanonical number", value)
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return [3]int{}, fmt.Errorf("%q contains an invalid number", value)
			}
		}
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
