package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

const (
	manifestMaxBytes  = int64(64 << 10)
	changelogMaxBytes = int64(2 << 20)
	policyMaxBytes    = int64(256 << 10)
	cacheFreshness    = time.Minute
	presignTTL        = 10 * time.Minute
	policyKey         = "trust/publisher/latest.json"
)

var channelKeys = map[release.Channel]string{
	release.ChannelStable:         "channels/stable/latest.json",
	release.ChannelLegacyRushRush: "channels/legacy-rushrush/latest.json",
}

var (
	ErrReleaseUnavailable  = errors.New("release unavailable")
	ErrReleaseInvalid      = errors.New("release invalid")
	ErrDownloadUnavailable = errors.New("download unavailable")
)

type invalidReleaseError struct {
	reason string
	cause  error
}

func (err invalidReleaseError) Error() string {
	return fmt.Sprintf("%s: %v", ErrReleaseInvalid, err.cause)
}
func (err invalidReleaseError) Is(target error) bool  { return target == ErrReleaseInvalid }
func (err invalidReleaseError) InvalidReason() string { return err.reason }

func releaseInvalid(reason string, cause error) error {
	return invalidReleaseError{reason: reason, cause: cause}
}

// InvalidReason returns a stable, non-sensitive code for invalid release metadata.
func InvalidReason(err error) string {
	var reasoned interface{ InvalidReason() string }
	if !errors.As(err, &reasoned) {
		return ""
	}
	reason := reasoned.InvalidReason()
	if reason == "unsupported_channel_key" || reason == "manifest_size" || reason == "publisher_policy_size" || release.IsValidationCode(reason) {
		return reason
	}
	return ""
}

type Store interface {
	Get(ctx context.Context, key string, maxBytes int64) (body []byte, etag string, err error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type Document struct {
	Body []byte
	ETag string
}

type Service struct {
	store Store
	now   func() time.Time

	cacheMu sync.RWMutex
	cache   map[release.Channel]manifestCache

	refreshMu sync.Mutex
}

type manifestCache struct {
	manifest  release.ChannelManifest
	fetchedAt time.Time
	valid     bool
}

func New(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now, cache: make(map[release.Channel]manifestCache)}
}

func (service *Service) Latest(ctx context.Context, channel release.Channel) (release.PublicRelease, error) {
	manifest, err := service.manifest(ctx, channel)
	if err != nil {
		return release.PublicRelease{}, err
	}

	downloadURL, err := service.store.PresignGet(ctx, manifest.Asset.ObjectKey, presignTTL)
	if err != nil {
		return release.PublicRelease{}, fmt.Errorf("%w: %v", ErrDownloadUnavailable, err)
	}
	return manifest.Public(downloadURL), nil
}

func (service *Service) Changelog(ctx context.Context) (Document, error) {
	manifest, err := service.manifest(ctx, release.ChannelStable)
	if err != nil {
		return Document{}, err
	}

	body, etag, err := service.store.Get(ctx, manifest.ChangelogObjectKey, changelogMaxBytes)
	if err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrReleaseUnavailable, err)
	}
	if len(body) > int(changelogMaxBytes) {
		return Document{}, releaseInvalid("changelog_size", errors.New("changelog exceeds 2 MiB"))
	}
	if _, err := release.ParseChangelog(body); err != nil {
		return Document{}, releaseInvalid(string(release.ValidationCodeOf(err)), err)
	}
	return Document{Body: append([]byte(nil), body...), ETag: etag}, nil
}

func (service *Service) PublisherPolicy(ctx context.Context) ([]byte, error) {
	body, _, err := service.store.Get(ctx, policyKey, policyMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReleaseUnavailable, err)
	}
	if len(body) == 0 || len(body) > int(policyMaxBytes) {
		return nil, releaseInvalid("publisher_policy_size", errors.New("publisher policy size is invalid"))
	}
	return append([]byte(nil), body...), nil
}

func (service *Service) manifest(ctx context.Context, channel release.Channel) (release.ChannelManifest, error) {
	channelKey, ok := channelKeys[channel]
	if !ok {
		return release.ChannelManifest{}, releaseInvalid("unsupported_channel_key", errors.New("unsupported channel key"))
	}

	now := service.now()
	if manifest, ok := service.freshManifest(channel, now); ok {
		return manifest, nil
	}

	service.refreshMu.Lock()
	defer service.refreshMu.Unlock()

	now = service.now()
	if manifest, ok := service.freshManifest(channel, now); ok {
		return manifest, nil
	}

	body, _, err := service.store.Get(ctx, channelKey, manifestMaxBytes)
	if err != nil {
		if manifest, ok := service.lastValidManifest(channel); ok {
			return manifest, nil
		}
		return release.ChannelManifest{}, fmt.Errorf("%w: %v", ErrReleaseUnavailable, err)
	}
	if len(body) > int(manifestMaxBytes) {
		if manifest, ok := service.lastValidManifest(channel); ok {
			return manifest, nil
		}
		return release.ChannelManifest{}, releaseInvalid("manifest_size", errors.New("channel manifest exceeds 64 KiB"))
	}

	manifest, err := release.ParseChannelManifest(body)
	if err != nil {
		if cached, ok := service.lastValidManifest(channel); ok {
			return cached, nil
		}
		return release.ChannelManifest{}, releaseInvalid(string(release.ValidationCodeOf(err)), err)
	}

	service.cacheMu.Lock()
	service.cache[channel] = manifestCache{manifest: manifest, fetchedAt: now, valid: true}
	service.cacheMu.Unlock()
	return manifest, nil
}

func (service *Service) freshManifest(channel release.Channel, now time.Time) (release.ChannelManifest, bool) {
	service.cacheMu.RLock()
	defer service.cacheMu.RUnlock()
	cached := service.cache[channel]
	if !cached.valid || now.Sub(cached.fetchedAt) >= cacheFreshness {
		return release.ChannelManifest{}, false
	}
	return cached.manifest, true
}

func (service *Service) lastValidManifest(channel release.Channel) (release.ChannelManifest, bool) {
	service.cacheMu.RLock()
	defer service.cacheMu.RUnlock()
	cached := service.cache[channel]
	if !cached.valid {
		return release.ChannelManifest{}, false
	}
	return cached.manifest, true
}
