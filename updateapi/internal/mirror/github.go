// Package mirror retrieves release metadata before it is mirrored to COS.
package mirror

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

const (
	AssetExecutable = "gift-panel-windows-x64.exe"
	AssetChecksum   = "gift-panel-windows-x64.exe.sha256"
	AssetManifest   = "gift-panel-update.json"
	AssetChangelog  = "gift-panel-changelog.json"

	maxExecutableBytes = 256 << 20
	maxChecksumBytes   = 16 << 10
	maxManifestBytes   = 1 << 20
	maxChangelogBytes  = 2 << 20

	githubAPIBase          = "https://api.github.com"
	githubRepository       = "brainfk123/bilibili-live-gift-panel"
	githubAPIVersion       = "2022-11-28"
	githubUserAgent        = "bilibili-live-gift-panel-release-mirror/1.0"
	githubAssetMediaType   = "application/octet-stream"
	maxReleaseResponseSize = 1 << 20
)

// RemoteAsset is trusted only after GitHubReleaseSource validates its metadata.
type RemoteAsset struct {
	Name        string
	DownloadURL string
	Size        int64
	Digest      string
}

// RemoteRelease is the validated public release selected for mirroring.
type RemoteRelease struct {
	Tag         string
	PublishedAt time.Time
	Assets      map[string]RemoteAsset
}

// LatestResult represents either a changed release or an ETag no-op.
type LatestResult struct {
	NotModified bool
	ETag        string
	Release     RemoteRelease
}

// ReleaseSource obtains either the latest public release or one exact tag,
// conditionally by the ETag owned by that channel.
type ReleaseSource interface {
	Latest(context.Context, string) (LatestResult, error)
	ByTag(context.Context, string, string) (LatestResult, error)
}

// GitHubReleaseSource reads the latest release from the fixed public repository.
type GitHubReleaseSource struct {
	client  *http.Client
	apiBase string
}

// NewGitHubReleaseSource always selects GitHub's public API and the fixed repository.
func NewGitHubReleaseSource(client *http.Client) *GitHubReleaseSource {
	return newGitHubReleaseSource(client, githubAPIBase)
}

func newGitHubReleaseSource(client *http.Client, apiBase string) *GitHubReleaseSource {
	if client == nil {
		client = http.DefaultClient
	}
	return &GitHubReleaseSource{client: client, apiBase: strings.TrimRight(apiBase, "/")}
}

func (source *GitHubReleaseSource) Latest(ctx context.Context, etag string) (LatestResult, error) {
	return source.get(ctx, "/repos/"+githubRepository+"/releases/latest", etag)
}

// ByTag retrieves one exact canonical GitHub release tag and never consults latest.
func (source *GitHubReleaseSource) ByTag(ctx context.Context, tag, etag string) (LatestResult, error) {
	if _, err := release.ParseStableTag(tag); err != nil {
		return LatestResult{}, errors.New("GitHub release tag is not canonical")
	}
	return source.get(ctx, "/repos/"+githubRepository+"/releases/tags/"+url.PathEscape(tag), etag)
}

func (source *GitHubReleaseSource) get(ctx context.Context, endpoint, etag string) (LatestResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.apiBase+endpoint, nil)
	if err != nil {
		return LatestResult{}, errors.New("could not create GitHub release request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", githubUserAgent)
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}

	response, err := source.client.Do(request)
	if err != nil {
		return LatestResult{}, errors.New("GitHub release request failed")
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotModified {
		if etag == "" {
			return LatestResult{}, errors.New("GitHub returned not modified without a conditional ETag")
		}
		return LatestResult{NotModified: true}, nil
	}
	if response.StatusCode != http.StatusOK {
		return LatestResult{}, fmt.Errorf("unexpected GitHub release response status %d", response.StatusCode)
	}
	if response.Header.Get("ETag") == "" {
		return LatestResult{}, errors.New("GitHub release response is missing ETag")
	}

	releasePayload, err := decodeGitHubRelease(response.Body)
	if err != nil {
		return LatestResult{}, err
	}
	trustedRelease, err := validateGitHubRelease(releasePayload)
	if err != nil {
		return LatestResult{}, err
	}
	return LatestResult{ETag: response.Header.Get("ETag"), Release: trustedRelease}, nil
}

type githubReleasePayload struct {
	TagName     string               `json:"tag_name"`
	Draft       *bool                `json:"draft"`
	Prerelease  *bool                `json:"prerelease"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubAssetPayload `json:"assets"`
}

type githubAssetPayload struct {
	APIURL      string `json:"url"`
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
}

func decodeGitHubRelease(body io.Reader) (githubReleasePayload, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxReleaseResponseSize+1))
	if err != nil {
		return githubReleasePayload{}, errors.New("could not read GitHub release response")
	}
	if len(data) > maxReleaseResponseSize {
		return githubReleasePayload{}, errors.New("GitHub release response exceeds size limit")
	}
	if err := rejectDuplicateSecurityFields(data); err != nil {
		return githubReleasePayload{}, err
	}
	var payload githubReleasePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return githubReleasePayload{}, errors.New("could not parse GitHub release response")
	}
	return payload, nil
}

type jsonObjectKind uint8

const (
	jsonGenericObject jsonObjectKind = iota
	jsonReleaseObject
	jsonAssetObject
)

func rejectDuplicateSecurityFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("could not parse GitHub release response")
	}
	if err := scanJSONObject(decoder, jsonReleaseObject); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("GitHub release response must contain one JSON value")
	}
	return nil
}

func scanJSONObject(decoder *json.Decoder, kind jsonObjectKind) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return errors.New("could not parse GitHub release response")
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("could not parse GitHub release response")
		}
		if isSecurityField(kind, key) {
			if _, duplicated := seen[key]; duplicated {
				return errors.New("GitHub release response contains duplicate security field")
			}
			seen[key] = struct{}{}
		}

		value, err := decoder.Token()
		if err != nil {
			return errors.New("could not parse GitHub release response")
		}
		if kind == jsonReleaseObject && key == "assets" {
			if delimiter, ok := value.(json.Delim); ok && delimiter == '[' {
				if err := scanAssetsArray(decoder); err != nil {
					return err
				}
				continue
			}
		}
		if err := scanJSONValue(decoder, value); err != nil {
			return err
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return errors.New("could not parse GitHub release response")
	}
	return nil
}

func scanAssetsArray(decoder *json.Decoder) error {
	for decoder.More() {
		value, err := decoder.Token()
		if err != nil {
			return errors.New("could not parse GitHub release response")
		}
		if delimiter, ok := value.(json.Delim); ok && delimiter == '{' {
			if err := scanJSONObject(decoder, jsonAssetObject); err != nil {
				return err
			}
			continue
		}
		if err := scanJSONValue(decoder, value); err != nil {
			return err
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
		return errors.New("could not parse GitHub release response")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, value json.Token) error {
	delimiter, ok := value.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return scanJSONObject(decoder, jsonGenericObject)
	case '[':
		for decoder.More() {
			child, err := decoder.Token()
			if err != nil {
				return errors.New("could not parse GitHub release response")
			}
			if err := scanJSONValue(decoder, child); err != nil {
				return err
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return errors.New("could not parse GitHub release response")
		}
	}
	return nil
}

func isSecurityField(kind jsonObjectKind, key string) bool {
	switch kind {
	case jsonReleaseObject:
		switch key {
		case "tag_name", "draft", "prerelease", "published_at", "assets":
			return true
		}
	case jsonAssetObject:
		switch key {
		case "url", "name", "browser_download_url", "size", "digest":
			return true
		}
	}
	return false
}

func validateGitHubRelease(payload githubReleasePayload) (RemoteRelease, error) {
	if payload.Draft == nil || *payload.Draft {
		return RemoteRelease{}, errors.New("GitHub release must not be a draft")
	}
	if payload.Prerelease == nil || *payload.Prerelease {
		return RemoteRelease{}, errors.New("GitHub release must not be a prerelease")
	}
	if _, err := release.ParseStableTag(payload.TagName); err != nil {
		return RemoteRelease{}, errors.New("GitHub release tag is not canonical")
	}
	publishedAt, err := time.Parse(time.RFC3339, payload.PublishedAt)
	if err != nil {
		return RemoteRelease{}, errors.New("GitHub release publication timestamp is invalid")
	}

	assets := make(map[string]RemoteAsset, 4)
	for _, asset := range payload.Assets {
		limit, required := assetLimit(asset.Name)
		if !required {
			continue
		}
		if _, duplicated := assets[asset.Name]; duplicated {
			return RemoteRelease{}, errors.New("GitHub release contains duplicate required asset")
		}
		if asset.Size <= 0 || asset.Size > limit {
			return RemoteRelease{}, errors.New("GitHub release asset has an invalid declared size")
		}
		if err := validateAssetURL(asset.DownloadURL, payload.TagName, asset.Name); err != nil {
			return RemoteRelease{}, err
		}
		if err := validateAssetAPIURL(asset.APIURL); err != nil {
			return RemoteRelease{}, err
		}
		if err := validateAssetDigest(asset.Digest); err != nil {
			return RemoteRelease{}, err
		}
		assets[asset.Name] = RemoteAsset{
			Name:        asset.Name,
			DownloadURL: asset.APIURL,
			Size:        asset.Size,
			Digest:      asset.Digest,
		}
	}
	if len(assets) != 4 {
		return RemoteRelease{}, errors.New("GitHub release is missing required assets")
	}
	return RemoteRelease{Tag: payload.TagName, PublishedAt: publishedAt, Assets: assets}, nil
}

func validateAssetAPIURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "api.github.com" || parsed.User != nil ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("GitHub release asset API URL is invalid")
	}
	prefix := "/repos/" + githubRepository + "/releases/assets/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return errors.New("GitHub release asset API URL does not match the repository")
	}
	identifier := strings.TrimPrefix(parsed.Path, prefix)
	numericID, err := strconv.ParseUint(identifier, 10, 64)
	if err != nil || numericID == 0 || strconv.FormatUint(numericID, 10) != identifier {
		return errors.New("GitHub release asset API URL has an invalid asset ID")
	}
	return nil
}

func assetLimit(name string) (int64, bool) {
	switch name {
	case AssetExecutable:
		return maxExecutableBytes, true
	case AssetChecksum:
		return maxChecksumBytes, true
	case AssetManifest:
		return maxManifestBytes, true
	case AssetChangelog:
		return maxChangelogBytes, true
	default:
		return 0, false
	}
}

func validateAssetURL(value, tag, name string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("GitHub release asset URL is invalid")
	}
	expectedPath := "/" + githubRepository + "/releases/download/" + tag + "/" + name
	if parsed.Path != expectedPath {
		return errors.New("GitHub release asset URL does not match the release")
	}
	return nil
}

func validateAssetDigest(digest string) error {
	if digest == "" {
		return nil
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) || len(digest) != len(prefix)+64 {
		return errors.New("GitHub release asset digest is invalid")
	}
	if digest != strings.ToLower(digest) {
		return errors.New("GitHub release asset digest is invalid")
	}
	if _, err := hex.DecodeString(digest[len(prefix):]); err != nil {
		return errors.New("GitHub release asset digest is invalid")
	}
	return nil
}
