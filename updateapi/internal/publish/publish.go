// Package publish implements an immutable release transaction for COS.
package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

const (
	stableKey       = "channels/stable/latest.json"
	assetName       = "gift-panel-windows-x64.exe"
	checksumName    = assetName + ".sha256"
	changelogName   = "gift-panel-changelog.json"
	releaseName     = "release.json"
	maxChecksumSize = 16 << 10
	maxStableBytes  = 1 << 20
)

// Store is the COS subset required by a publisher transaction.
type Store interface {
	Head(context.Context, string) (cosstore.ObjectInfo, error)
	PutImmutable(context.Context, string, io.Reader, int64, string, string) error
	Put(context.Context, string, io.Reader, int64, string, string) error
	Get(context.Context, string, int64) ([]byte, string, error)
}

var canonicalTag = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)

// Input identifies the locally built release materials.
type Input struct {
	Tag           string
	AssetPath     string
	ChecksumPath  string
	ChangelogPath string
	PublishedAt   time.Time
}

type object struct {
	key         string
	body        []byte
	contentType string
	digest      string
}

// Run validates local release inputs, publishes immutable versioned objects,
// verifies them by metadata, and updates the stable pointer only at the end.
func Run(ctx context.Context, store Store, input Input) error {
	if store == nil {
		return errors.New("COS store is required")
	}
	if !canonicalTag.MatchString(input.Tag) {
		return fmt.Errorf("release tag %q must use canonical vMAJOR.MINOR.PATCH syntax", input.Tag)
	}
	asset, err := readAsset(input.AssetPath)
	if err != nil {
		return err
	}
	if err := validateChecksum(input.ChecksumPath, asset.digest); err != nil {
		return err
	}
	changelog, err := os.ReadFile(input.ChangelogPath)
	if err != nil {
		return fmt.Errorf("read changelog: %w", err)
	}
	if _, err := release.ParseChangelog(changelog); err != nil {
		return fmt.Errorf("validate changelog: %w", err)
	}

	prefix := "releases/" + input.Tag + "/"
	manifest := release.ChannelManifest{
		SchemaVersion: 1,
		TagName:       input.Tag,
		PublishedAt:   input.PublishedAt.UTC().Format(time.RFC3339),
		Asset: release.AssetManifest{
			Name:      assetName,
			ObjectKey: prefix + assetName,
			Size:      int64(len(asset.body)),
			SHA256:    asset.digest,
		},
		ChangelogObjectKey: prefix + changelogName,
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("validate release manifest: %w", err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode release manifest: %w", err)
	}
	checksum, err := os.ReadFile(input.ChecksumPath)
	if err != nil {
		return fmt.Errorf("read checksum: %w", err)
	}
	objects := []object{
		withKey(asset, prefix+assetName),
		newObject(prefix+checksumName, checksum, "text/plain; charset=utf-8"),
		newObject(prefix+changelogName, changelog, "application/json"),
		newObject(prefix+releaseName, manifestBody, "application/json"),
	}
	for _, candidate := range objects {
		if err := publishImmutable(ctx, store, candidate); err != nil {
			return err
		}
	}

	stable := newObject(stableKey, manifestBody, "application/json")
	if err := store.Put(ctx, stable.key, strings.NewReader(string(stable.body)), int64(len(stable.body)), stable.contentType, stable.digest); err != nil {
		return fmt.Errorf("write stable pointer: %w", err)
	}
	readback, _, err := store.Get(ctx, stableKey, maxStableBytes)
	if err != nil {
		return fmt.Errorf("read stable pointer: %w", err)
	}
	verified, err := release.ParseChannelManifest(readback)
	if err != nil {
		return fmt.Errorf("parse stable pointer: %w", err)
	}
	if verified != manifest {
		return errors.New("stable pointer readback does not match published release")
	}
	return nil
}

func readAsset(path string) (object, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return object{}, fmt.Errorf("read asset: %w", err)
	}
	if filepath.Base(path) != assetName {
		return object{}, fmt.Errorf("asset file must be named %q", assetName)
	}
	return newObject("", body, "application/vnd.microsoft.portable-executable"), nil
}

func validateChecksum(path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read checksum: %w", err)
	}
	if len(data) > maxChecksumSize {
		return errors.New("checksum file exceeds 16 KiB")
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return errors.New("checksum file must start with a SHA-256 digest")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return fmt.Errorf("checksum file has invalid SHA-256: %w", err)
	}
	if !strings.EqualFold(fields[0], want) {
		return errors.New("checksum does not match asset")
	}
	return nil
}

func newObject(key string, body []byte, contentType string) object {
	sum := sha256.Sum256(body)
	return object{key: key, body: body, contentType: contentType, digest: hex.EncodeToString(sum[:])}
}

func withKey(candidate object, key string) object {
	candidate.key = key
	return candidate
}

func publishImmutable(ctx context.Context, store Store, candidate object) error {
	info, err := store.Head(ctx, candidate.key)
	if err == nil {
		return verifyObject(candidate, info)
	}
	if !errors.Is(err, cosstore.ErrNotFound) {
		return fmt.Errorf("head %q: %w", candidate.key, err)
	}
	if err := store.PutImmutable(ctx, candidate.key, strings.NewReader(string(candidate.body)), int64(len(candidate.body)), candidate.contentType, candidate.digest); err != nil {
		if !errors.Is(err, cosstore.ErrAlreadyExists) {
			return fmt.Errorf("create immutable %q: %w", candidate.key, err)
		}
		info, headErr := store.Head(ctx, candidate.key)
		if headErr != nil {
			return fmt.Errorf("verify concurrent immutable %q: %w", candidate.key, headErr)
		}
		return verifyObject(candidate, info)
	}
	info, err = store.Head(ctx, candidate.key)
	if err != nil {
		return fmt.Errorf("verify %q: %w", candidate.key, err)
	}
	return verifyObject(candidate, info)
}

func verifyObject(candidate object, info cosstore.ObjectInfo) error {
	if info.Size != int64(len(candidate.body)) || !strings.EqualFold(info.SHA256, candidate.digest) {
		return fmt.Errorf("immutable object %q does not match local release", candidate.key)
	}
	return nil
}
