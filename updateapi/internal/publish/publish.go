// Package publish implements an immutable release transaction for COS.
package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

const (
	stableKey       = "channels/stable/latest.json"
	legacyKey       = "channels/legacy-rushrush/latest.json"
	assetName       = "gift-panel-windows-x64.exe"
	checksumName    = assetName + ".sha256"
	changelogName   = "gift-panel-changelog.json"
	releaseName     = "release.json"
	maxChecksumSize = 16 << 10
	maxPointerBytes = 1 << 20
)

// Store is the COS subset required by a publisher transaction.
type Store interface {
	Head(context.Context, string) (cosstore.ObjectInfo, error)
	PutImmutable(context.Context, string, io.Reader, int64, string, string) error
	Put(context.Context, string, io.Reader, int64, string, string) error
	Get(context.Context, string, int64) ([]byte, string, error)
}

// ErrPromotionIndeterminate means the selected channel pointer may have advanced and
// automatic restoration could not be verified. Operators must inspect COS.
var ErrPromotionIndeterminate = errors.New("channel promotion outcome is indeterminate")

// Outcome reports whether this transaction advanced the stable pointer.
type Outcome string

const (
	OutcomeStablePromoted  Outcome = "stable promoted"
	OutcomeStableUnchanged Outcome = "stable unchanged"
	OutcomeLegacyPromoted  Outcome = "legacy promoted"
	OutcomeLegacyUnchanged Outcome = "legacy unchanged"
)

// Input identifies the locally built release materials.
type Input struct {
	Channel       release.Channel
	Tag           string
	AssetPath     string
	ChecksumPath  string
	ChangelogPath string
	PublishedAt   time.Time
	Prepared      *PreparedRelease
}

// PreparedRelease is an immutable, in-memory snapshot of already validated
// release bytes. Its fields are private and the constructor copies all input.
type PreparedRelease struct {
	asset     []byte
	checksum  []byte
	changelog []byte
}

// NewPreparedRelease takes an immutable snapshot for the publisher transaction.
func NewPreparedRelease(asset, checksum, changelog []byte) (*PreparedRelease, error) {
	if len(asset) == 0 || len(checksum) == 0 || len(changelog) == 0 {
		return nil, errors.New("prepared release bytes are incomplete")
	}
	return &PreparedRelease{
		asset:     append([]byte(nil), asset...),
		checksum:  append([]byte(nil), checksum...),
		changelog: append([]byte(nil), changelog...),
	}, nil
}

// Publisher binds the existing immutable release transaction to one COS store.
// It is intentionally a narrow object so orchestrators cannot reorder its steps.
type Publisher struct {
	store Store
}

// NewPublisher constructs an object adapter around the existing transaction.
func NewPublisher(store Store) (*Publisher, error) {
	if store == nil {
		return nil, errors.New("COS store is required")
	}
	return &Publisher{store: store}, nil
}

// Publish delegates to the package transaction without changing its semantics.
func (publisher *Publisher) Publish(ctx context.Context, input Input) (Outcome, error) {
	if publisher == nil {
		return "", errors.New("COS publisher is required")
	}
	return Publish(ctx, publisher.store, input)
}

type object struct {
	key         string
	body        []byte
	contentType string
	digest      string
}

// Run preserves the original error-only publisher API.
func Run(ctx context.Context, store Store, input Input) error {
	_, err := Publish(ctx, store, input)
	return err
}

// Publish validates local release inputs, publishes immutable versioned objects,
// verifies them by metadata, and advances the stable pointer only at the end.
// A valid stable tag greater than or equal to the candidate is never overwritten.
func Publish(ctx context.Context, store Store, input Input) (Outcome, error) {
	if store == nil {
		return "", errors.New("COS store is required")
	}
	pointerKey, promoted, unchanged, err := publicationChannel(input.Channel, input.Tag)
	if err != nil {
		return "", err
	}
	materials, err := readInputMaterials(input)
	if err != nil {
		return "", err
	}
	if err := validateChecksumBytes(materials.checksum, materials.asset.digest); err != nil {
		return "", err
	}
	if _, err := release.ParseChangelog(materials.changelog); err != nil {
		return "", fmt.Errorf("validate changelog: %w", err)
	}

	prefix := "releases/" + input.Tag + "/"
	manifest := release.ChannelManifest{
		SchemaVersion: 2,
		Channel:       input.Channel,
		TagName:       input.Tag,
		PublishedAt:   input.PublishedAt.UTC().Format(time.RFC3339),
		Asset: release.AssetManifest{
			Name:      assetName,
			ObjectKey: prefix + assetName,
			Size:      int64(len(materials.asset.body)),
			SHA256:    materials.asset.digest,
		},
		ChangelogObjectKey: prefix + changelogName,
	}
	if err := manifest.ValidateForChannel(input.Channel); err != nil {
		return "", fmt.Errorf("validate release manifest: %w", err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode release manifest: %w", err)
	}
	objects := []object{
		withKey(materials.asset, prefix+assetName),
		newObject(prefix+checksumName, materials.checksum, "text/plain; charset=utf-8"),
		newObject(prefix+changelogName, materials.changelog, "application/json"),
	}
	for _, candidate := range objects {
		if err := publishImmutable(ctx, store, candidate); err != nil {
			return "", err
		}
	}
	if err := publishImmutableManifest(ctx, store, newObject(prefix+releaseName, manifestBody, "application/json"), manifest); err != nil {
		return "", err
	}

	pointer := newObject(pointerKey, manifestBody, "application/json")
	prior, err := readPriorPointer(ctx, store, input.Channel, pointerKey)
	if err != nil {
		return "", err
	}
	if prior != nil {
		comparison, err := compareTags(input.Tag, prior.manifest.TagName)
		if err != nil {
			return "", fmt.Errorf("compare stable release tags: %w", err)
		}
		if comparison <= 0 {
			return unchanged, nil
		}
	}
	if err := store.Put(ctx, pointer.key, strings.NewReader(string(pointer.body)), int64(len(pointer.body)), pointer.contentType, pointer.digest); err != nil {
		return "", recoverPriorPointer(ctx, store, input.Channel, pointerKey, priorObject(prior), fmt.Errorf("write channel pointer: %w", err))
	}
	readback, _, err := store.Get(ctx, pointerKey, maxPointerBytes)
	if err != nil {
		return "", recoverPriorPointer(ctx, store, input.Channel, pointerKey, priorObject(prior), fmt.Errorf("read channel pointer: %w", err))
	}
	if err := verifyPointerReadback(input.Channel, pointer, readback); err != nil {
		return "", recoverPriorPointer(ctx, store, input.Channel, pointerKey, priorObject(prior), err)
	}
	return promoted, nil
}

func publicationChannel(channel release.Channel, tag string) (string, Outcome, Outcome, error) {
	if _, err := release.ParseStableTag(tag); err != nil {
		return "", "", "", fmt.Errorf("release tag %q must use canonical vMAJOR.MINOR.PATCH syntax", tag)
	}
	switch channel {
	case release.ChannelStable:
		if tag == "v0.4.11" {
			return "", "", "", errors.New("stable channel cannot publish the legacy bridge tag")
		}
		return stableKey, OutcomeStablePromoted, OutcomeStableUnchanged, nil
	case release.ChannelLegacyRushRush:
		if tag != "v0.4.11" {
			return "", "", "", errors.New("legacy channel requires exact bridge tag v0.4.11")
		}
		return legacyKey, OutcomeLegacyPromoted, OutcomeLegacyUnchanged, nil
	default:
		return "", "", "", fmt.Errorf("unsupported publication channel %q", channel)
	}
}

type priorPointer struct {
	object   object
	manifest release.ChannelManifest
}

func readPriorPointer(ctx context.Context, store Store, channel release.Channel, pointerKey string) (*priorPointer, error) {
	body, _, err := store.Get(ctx, pointerKey, maxPointerBytes)
	if errors.Is(err, cosstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read prior channel pointer: %w", err)
	}
	manifest, err := release.ParseChannelManifest(body)
	if err != nil {
		return nil, fmt.Errorf("validate prior channel pointer: %w", err)
	}
	if err := manifest.ValidateForChannel(channel); err != nil {
		return nil, fmt.Errorf("validate prior channel pointer: %w", err)
	}
	prior := newObject(pointerKey, body, "application/json")
	return &priorPointer{object: prior, manifest: manifest}, nil
}

func priorObject(prior *priorPointer) *object {
	if prior == nil {
		return nil
	}
	return &prior.object
}

func compareTags(left, right string) (int, error) {
	leftVersion, err := release.ParseStableTag(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := release.ParseStableTag(right)
	if err != nil {
		return 0, err
	}
	for index := range leftVersion {
		if leftVersion[index] < rightVersion[index] {
			return -1, nil
		}
		if leftVersion[index] > rightVersion[index] {
			return 1, nil
		}
	}
	return 0, nil
}

func verifyPointerReadback(channel release.Channel, want object, got []byte) error {
	if !bytes.Equal(got, want.body) {
		return errors.New("channel pointer exact readback does not match published release")
	}
	manifest, err := release.ParseChannelManifest(got)
	if err != nil {
		return fmt.Errorf("validate channel pointer readback: %w", err)
	}
	if err := manifest.ValidateForChannel(channel); err != nil {
		return fmt.Errorf("validate channel pointer readback: %w", err)
	}
	return nil
}

func recoverPriorPointer(ctx context.Context, store Store, channel release.Channel, pointerKey string, prior *object, promotionErr error) error {
	if prior == nil {
		return fmt.Errorf("%w: %v; no prior channel pointer is available for restoration", ErrPromotionIndeterminate, promotionErr)
	}
	if err := store.Put(ctx, prior.key, strings.NewReader(string(prior.body)), int64(len(prior.body)), prior.contentType, prior.digest); err != nil {
		return fmt.Errorf("%w: %v; restore prior channel pointer: %v", ErrPromotionIndeterminate, promotionErr, err)
	}
	readback, _, err := store.Get(ctx, pointerKey, maxPointerBytes)
	if err != nil {
		return fmt.Errorf("%w: %v; verify restored channel pointer: %v", ErrPromotionIndeterminate, promotionErr, err)
	}
	if err := verifyPointerReadback(channel, *prior, readback); err != nil {
		return fmt.Errorf("%w: %v; verify restored channel pointer: %v", ErrPromotionIndeterminate, promotionErr, err)
	}
	return fmt.Errorf("channel promotion failed; prior pointer restored and verified: %w", promotionErr)
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

type inputMaterials struct {
	asset     object
	checksum  []byte
	changelog []byte
}

func readInputMaterials(input Input) (inputMaterials, error) {
	hasAnyPath := input.AssetPath != "" || input.ChecksumPath != "" || input.ChangelogPath != ""
	hasAllPaths := input.AssetPath != "" && input.ChecksumPath != "" && input.ChangelogPath != ""
	if input.Prepared != nil {
		if hasAnyPath {
			return inputMaterials{}, errors.New("release input must not mix paths and a prepared snapshot")
		}
		if len(input.Prepared.asset) == 0 || len(input.Prepared.checksum) == 0 || len(input.Prepared.changelog) == 0 {
			return inputMaterials{}, errors.New("prepared release snapshot is invalid")
		}
		return inputMaterials{
			asset:     newObject("", input.Prepared.asset, "application/vnd.microsoft.portable-executable"),
			checksum:  input.Prepared.checksum,
			changelog: input.Prepared.changelog,
		}, nil
	}
	if !hasAllPaths {
		return inputMaterials{}, errors.New("release input paths are incomplete")
	}
	asset, err := readAsset(input.AssetPath)
	if err != nil {
		return inputMaterials{}, err
	}
	checksum, err := os.ReadFile(input.ChecksumPath)
	if err != nil {
		return inputMaterials{}, fmt.Errorf("read checksum: %w", err)
	}
	changelog, err := os.ReadFile(input.ChangelogPath)
	if err != nil {
		return inputMaterials{}, fmt.Errorf("read changelog: %w", err)
	}
	return inputMaterials{asset: asset, checksum: checksum, changelog: changelog}, nil
}

func validateChecksumBytes(data []byte, want string) error {
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

func publishImmutableManifest(ctx context.Context, store Store, candidate object, want release.ChannelManifest) error {
	info, err := store.Head(ctx, candidate.key)
	if err == nil {
		return verifyManifestObject(ctx, store, candidate, want, info)
	}
	if !errors.Is(err, cosstore.ErrNotFound) {
		return fmt.Errorf("head %q: %w", candidate.key, err)
	}
	if err := store.PutImmutable(ctx, candidate.key, strings.NewReader(string(candidate.body)), int64(len(candidate.body)), candidate.contentType, candidate.digest); err != nil {
		if !errors.Is(err, cosstore.ErrAlreadyExists) {
			return fmt.Errorf("create immutable %q: %w", candidate.key, err)
		}
	}
	info, err = store.Head(ctx, candidate.key)
	if err != nil {
		return fmt.Errorf("verify %q: %w", candidate.key, err)
	}
	return verifyManifestObject(ctx, store, candidate, want, info)
}

func verifyManifestObject(ctx context.Context, store Store, candidate object, want release.ChannelManifest, info cosstore.ObjectInfo) error {
	if verifyObject(candidate, info) == nil {
		return nil
	}
	// Existing schema-1 stable release manifests remain reusable. They are
	// immutable, so compatibility is verified by exact semantic fields rather
	// than overwriting the object with schema 2.
	if want.Channel != release.ChannelStable {
		return fmt.Errorf("immutable object %q does not match local release", candidate.key)
	}
	body, _, err := store.Get(ctx, candidate.key, maxPointerBytes)
	if err != nil {
		return fmt.Errorf("read immutable manifest %q: %w", candidate.key, err)
	}
	existing, err := release.ParseChannelManifest(body)
	if err != nil || existing.SchemaVersion != 1 || existing.Channel != release.ChannelStable {
		return fmt.Errorf("immutable object %q does not match local release", candidate.key)
	}
	existing.SchemaVersion = 2
	if existing != want {
		return fmt.Errorf("immutable object %q does not match local release", candidate.key)
	}
	return nil
}

func verifyObject(candidate object, info cosstore.ObjectInfo) error {
	if info.Size != int64(len(candidate.body)) || !strings.EqualFold(info.SHA256, candidate.digest) {
		return fmt.Errorf("immutable object %q does not match local release", candidate.key)
	}
	return nil
}
