package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

const channelKey = "channels/stable/latest.json"

type fakeStore struct {
	mu       sync.Mutex
	gets     []getCall
	get      func(key string, maxBytes int64) ([]byte, string, error)
	presigns []presignCall
	presign  func(key string, ttl time.Duration) (string, error)
}

type getCall struct {
	key      string
	maxBytes int64
}

type presignCall struct {
	key string
	ttl time.Duration
}

func (store *fakeStore) Get(_ context.Context, key string, maxBytes int64) ([]byte, string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.gets = append(store.gets, getCall{key: key, maxBytes: maxBytes})
	return store.get(key, maxBytes)
}

func (store *fakeStore) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.presigns = append(store.presigns, presignCall{key: key, ttl: ttl})
	return store.presign(key, ttl)
}

func TestLatestCachesManifestButPresignsEveryResponse(t *testing.T) {
	clock := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	manifest := validManifest(t)
	urls := []string{"https://cos.example.invalid/one", "https://cos.example.invalid/two"}
	store := &fakeStore{
		get: func(key string, maxBytes int64) ([]byte, string, error) {
			if key != channelKey || maxBytes != 64<<10 {
				t.Fatalf("Get(%q, %d), want channel key and 64 KiB limit", key, maxBytes)
			}
			return manifest, "channel-etag", nil
		},
		presign: func(key string, ttl time.Duration) (string, error) {
			if key != "releases/v0.4.4/gift-panel-windows-x64.exe" || ttl != 10*time.Minute {
				t.Fatalf("PresignGet(%q, %s), want manifest asset and 10 minutes", key, ttl)
			}
			url := urls[0]
			urls = urls[1:]
			return url, nil
		},
	}

	sut := service.New(store, func() time.Time { return clock })
	first, err := sut.Latest(context.Background(), release.ChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	second, err := sut.Latest(context.Background(), release.ChannelStable)
	if err != nil {
		t.Fatal(err)
	}

	if len(store.gets) != 1 {
		t.Fatalf("channel reads = %d, want 1", len(store.gets))
	}
	if first.Assets[0].DownloadURL == second.Assets[0].DownloadURL {
		t.Fatalf("download URLs = %q and %q, want separately signed URLs", first.Assets[0].DownloadURL, second.Assets[0].DownloadURL)
	}
	if len(store.presigns) != 2 {
		t.Fatalf("presigns = %d, want 2", len(store.presigns))
	}
}

func TestLatestUsesLastValidManifestWhenRefreshFails(t *testing.T) {
	clock := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	manifest := validManifest(t)
	reads := 0
	store := &fakeStore{
		get: func(string, int64) ([]byte, string, error) {
			reads++
			if reads == 1 {
				return manifest, "etag-1", nil
			}
			return nil, "", errors.New("COS unavailable")
		},
		presign: func(string, time.Duration) (string, error) { return "https://cos.example.invalid/signed", nil },
	}
	sut := service.New(store, func() time.Time { return clock })
	if _, err := sut.Latest(context.Background(), release.ChannelStable); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(61 * time.Second)
	got, err := sut.Latest(context.Background(), release.ChannelStable)
	if err != nil {
		t.Fatalf("Latest after refresh failure: %v", err)
	}
	if got.TagName != "v0.4.4" {
		t.Fatalf("TagName = %q, want cached v0.4.4", got.TagName)
	}
	if reads != 2 {
		t.Fatalf("channel reads = %d, want 2", reads)
	}
}

func TestLatestUsesLastValidManifestWhenRefreshIsInvalid(t *testing.T) {
	clock := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	reads := 0
	store := &fakeStore{
		get: func(string, int64) ([]byte, string, error) {
			reads++
			if reads == 1 {
				return validManifest(t), "etag-1", nil
			}
			return []byte(`{"schemaVersion":2}`), "etag-2", nil
		},
		presign: func(string, time.Duration) (string, error) { return "https://cos.example.invalid/signed", nil },
	}
	sut := service.New(store, func() time.Time { return clock })
	if _, err := sut.Latest(context.Background(), release.ChannelStable); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(61 * time.Second)
	got, err := sut.Latest(context.Background(), release.ChannelStable)
	if err != nil {
		t.Fatalf("Latest after invalid refresh: %v", err)
	}
	if got.TagName != "v0.4.4" {
		t.Fatalf("TagName = %q, want cached v0.4.4", got.TagName)
	}
}

func TestLatestClassifiesColdStartAndSignerFailures(t *testing.T) {
	t.Run("cold start read failure", func(t *testing.T) {
		store := &fakeStore{
			get: func(string, int64) ([]byte, string, error) { return nil, "", errors.New("COS unavailable") },
			presign: func(string, time.Duration) (string, error) {
				t.Fatal("PresignGet should not be called")
				return "", nil
			},
		}
		_, err := service.New(store, time.Now).Latest(context.Background(), release.ChannelStable)
		if !errors.Is(err, service.ErrReleaseUnavailable) {
			t.Fatalf("Latest() error = %v, want ErrReleaseUnavailable", err)
		}
	})

	t.Run("invalid cold start metadata", func(t *testing.T) {
		store := &fakeStore{
			get: func(string, int64) ([]byte, string, error) { return []byte(`{"schemaVersion":2}`), "etag", nil },
			presign: func(string, time.Duration) (string, error) {
				t.Fatal("PresignGet should not be called")
				return "", nil
			},
		}
		_, err := service.New(store, time.Now).Latest(context.Background(), release.ChannelStable)
		if !errors.Is(err, service.ErrReleaseInvalid) {
			t.Fatalf("Latest() error = %v, want ErrReleaseInvalid", err)
		}
	})

	t.Run("signer failure", func(t *testing.T) {
		store := &fakeStore{
			get:     func(string, int64) ([]byte, string, error) { return validManifest(t), "etag", nil },
			presign: func(string, time.Duration) (string, error) { return "", errors.New("signer unavailable") },
		}
		_, err := service.New(store, time.Now).Latest(context.Background(), release.ChannelStable)
		if !errors.Is(err, service.ErrDownloadUnavailable) {
			t.Fatalf("Latest() error = %v, want ErrDownloadUnavailable", err)
		}
	})
}

func TestInvalidManifestErrorsExposeSanitizedReasonCodes(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		mutate func(*release.ChannelManifest)
	}{
		{name: "tag", reason: "manifest_tag", mutate: func(manifest *release.ChannelManifest) {
			manifest.TagName = "v0.04.4"
			manifest.Asset.ObjectKey = "releases/v0.04.4/gift-panel-windows-x64.exe"
			manifest.ChangelogObjectKey = "releases/v0.04.4/gift-panel-changelog.json"
		}},
		{name: "size", reason: "manifest_asset_size", mutate: func(manifest *release.ChannelManifest) {
			manifest.Asset.Size = 0
		}},
		{name: "digest", reason: "manifest_asset_sha256", mutate: func(manifest *release.ChannelManifest) {
			manifest.Asset.SHA256 = strings.Repeat("g", 64)
		}},
		{name: "path", reason: "manifest_asset_key", mutate: func(manifest *release.ChannelManifest) {
			manifest.Asset.ObjectKey = "releases/v0.4.4/other.exe"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var manifest release.ChannelManifest
			if err := json.Unmarshal(validManifest(t), &manifest); err != nil {
				t.Fatal(err)
			}
			test.mutate(&manifest)
			body, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			store := &fakeStore{
				get: func(string, int64) ([]byte, string, error) { return body, "etag", nil },
				presign: func(string, time.Duration) (string, error) {
					t.Fatal("PresignGet should not be called for invalid metadata")
					return "", nil
				},
			}

			_, err = service.New(store, time.Now).Latest(context.Background(), release.ChannelStable)
			if !errors.Is(err, service.ErrReleaseInvalid) {
				t.Fatalf("Latest() error = %v, want ErrReleaseInvalid", err)
			}
			if got := service.InvalidReason(err); got != test.reason {
				t.Fatalf("InvalidReason() = %q, want %q", got, test.reason)
			}
			if strings.Contains(service.InvalidReason(err), "releases/") {
				t.Fatalf("reason %q contains object path", service.InvalidReason(err))
			}
		})
	}
}

func TestLatestReadsOnlySelectedChannelPointer(t *testing.T) {
	store := &fakeStore{
		get: func(key string, maxBytes int64) ([]byte, string, error) {
			if key != "channels/legacy-rushrush/latest.json" || maxBytes != 64<<10 {
				t.Fatalf("Get(%q, %d), want legacy pointer and 64 KiB", key, maxBytes)
			}
			return validManifest(t), "legacy-etag", nil
		},
		presign: func(string, time.Duration) (string, error) { return "https://cos.example.invalid/legacy", nil },
	}

	got, err := service.New(store, time.Now).Latest(context.Background(), release.ChannelLegacyRushRush)
	if err != nil {
		t.Fatal(err)
	}
	if got.TagName != "v0.4.4" || len(store.gets) != 1 {
		t.Fatalf("Latest() = %#v, reads=%#v", got, store.gets)
	}
}

func TestLatestRejectsUnknownTypedChannelWithoutStorageAccess(t *testing.T) {
	store := &fakeStore{
		get: func(string, int64) ([]byte, string, error) {
			t.Fatal("unknown channel reached Store.Get")
			return nil, "", nil
		},
		presign: func(string, time.Duration) (string, error) {
			t.Fatal("unknown channel reached Store.PresignGet")
			return "", nil
		},
	}

	_, err := service.New(store, time.Now).Latest(context.Background(), release.Channel("private/arbitrary-key"))
	if !errors.Is(err, service.ErrReleaseInvalid) || service.InvalidReason(err) != "unsupported_channel_key" {
		t.Fatalf("Latest() error = %v reason=%q", err, service.InvalidReason(err))
	}
}

func TestPublisherPolicyReadsCompleteBoundedEnvelope(t *testing.T) {
	policy := []byte(`{"signed":{"epoch":7},"signatures":[{"algorithm":"ecdsa-p256-sha256","signature":"opaque"}]}`)
	store := &fakeStore{
		get: func(key string, maxBytes int64) ([]byte, string, error) {
			if key != "trust/publisher/latest.json" || maxBytes != 256<<10 {
				t.Fatalf("Get(%q, %d), want publisher policy key and 256 KiB", key, maxBytes)
			}
			return policy, "policy-etag", nil
		},
		presign: func(string, time.Duration) (string, error) {
			t.Fatal("PublisherPolicy must not presign an object")
			return "", nil
		},
	}

	got, err := service.New(store, time.Now).PublisherPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(policy) {
		t.Fatalf("PublisherPolicy() = %s, want complete envelope %s", got, policy)
	}
	got[0] = 'x'
	if policy[0] == 'x' {
		t.Fatal("PublisherPolicy returned aliased storage bytes")
	}
}

func TestPublisherPolicyRejectsOversizedEnvelope(t *testing.T) {
	store := &fakeStore{
		get: func(string, int64) ([]byte, string, error) {
			return []byte(strings.Repeat("x", (256<<10)+1)), "", nil
		},
		presign: func(string, time.Duration) (string, error) { return "", nil },
	}
	_, err := service.New(store, time.Now).PublisherPolicy(context.Background())
	if !errors.Is(err, service.ErrReleaseInvalid) || service.InvalidReason(err) != "publisher_policy_size" {
		t.Fatalf("PublisherPolicy() error=%v reason=%q", err, service.InvalidReason(err))
	}
}

func TestChannelRefreshesDoNotBlockEachOther(t *testing.T) {
	for _, test := range []struct {
		name           string
		blockedChannel release.Channel
		blockedKey     string
		freeChannel    release.Channel
		stale          bool
	}{
		{name: "legacy cold does not block stable cold", blockedChannel: release.ChannelLegacyRushRush, blockedKey: "channels/legacy-rushrush/latest.json", freeChannel: release.ChannelStable},
		{name: "stable cold does not block legacy cold", blockedChannel: release.ChannelStable, blockedKey: "channels/stable/latest.json", freeChannel: release.ChannelLegacyRushRush},
		{name: "legacy stale does not block stable stale", blockedChannel: release.ChannelLegacyRushRush, blockedKey: "channels/legacy-rushrush/latest.json", freeChannel: release.ChannelStable, stale: true},
		{name: "stable stale does not block legacy stale", blockedChannel: release.ChannelStable, blockedKey: "channels/stable/latest.json", freeChannel: release.ChannelLegacyRushRush, stale: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			blockedKey := test.blockedKey
			if test.stale {
				blockedKey = ""
			}
			store := newBlockingStore(validManifest(t), blockedKey)
			defer store.unblock()
			sut := service.New(store, func() time.Time { return clock })
			if test.stale {
				if _, err := sut.Latest(context.Background(), release.ChannelStable); err != nil {
					t.Fatal(err)
				}
				if _, err := sut.Latest(context.Background(), release.ChannelLegacyRushRush); err != nil {
					t.Fatal(err)
				}
				clock = clock.Add(61 * time.Second)
				store.blockedKey = test.blockedKey
			}
			blockedDone := make(chan error, 1)
			go func() {
				_, err := sut.Latest(context.Background(), test.blockedChannel)
				blockedDone <- err
			}()
			select {
			case <-store.started:
			case <-time.After(time.Second):
				t.Fatal("blocked channel read did not start")
			}

			freeDone := make(chan error, 1)
			go func() {
				_, err := sut.Latest(context.Background(), test.freeChannel)
				freeDone <- err
			}()
			select {
			case err := <-freeDone:
				if err != nil {
					t.Fatalf("independent channel refresh: %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("independent channel refresh waited for blocked channel")
			}

			store.unblock()
			if err := <-blockedDone; err != nil {
				t.Fatalf("blocked channel after release: %v", err)
			}
		})
	}
}

func TestSameChannelColdRefreshRemainsSingleflight(t *testing.T) {
	store := newBlockingStore(validManifest(t), "channels/stable/latest.json")
	defer store.unblock()
	sut := service.New(store, time.Now)
	const callers = 12
	done := make(chan error, callers)
	for range callers {
		go func() {
			_, err := sut.Latest(context.Background(), release.ChannelStable)
			done <- err
		}()
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("stable channel read did not start")
	}
	store.unblock()
	for range callers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := store.getCount("channels/stable/latest.json"); got != 1 {
		t.Fatalf("stable pointer reads = %d, want one singleflight read", got)
	}
}

func TestPublisherPolicyDoesNotWaitForBlockedChannelRefresh(t *testing.T) {
	store := newBlockingStore(validManifest(t), "channels/stable/latest.json")
	defer store.unblock()
	sut := service.New(store, time.Now)
	channelDone := make(chan error, 1)
	go func() {
		_, err := sut.Latest(context.Background(), release.ChannelStable)
		channelDone <- err
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("stable channel read did not start")
	}

	policyDone := make(chan error, 1)
	go func() {
		_, err := sut.PublisherPolicy(context.Background())
		policyDone <- err
	}()
	select {
	case err := <-policyDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publisher policy read waited for blocked channel")
	}
	store.unblock()
	if err := <-channelDone; err != nil {
		t.Fatal(err)
	}
}

type blockingStore struct {
	manifest    []byte
	blockedKey  string
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	gets        map[string]int
}

func newBlockingStore(manifest []byte, blockedKey string) *blockingStore {
	return &blockingStore{
		manifest: append([]byte(nil), manifest...), blockedKey: blockedKey,
		started: make(chan struct{}), release: make(chan struct{}), gets: make(map[string]int),
	}
}

func (store *blockingStore) Get(ctx context.Context, key string, _ int64) ([]byte, string, error) {
	store.mu.Lock()
	store.gets[key]++
	store.mu.Unlock()
	if key == store.blockedKey {
		store.startOnce.Do(func() { close(store.started) })
		select {
		case <-store.release:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if key == "trust/publisher/latest.json" {
		return []byte(`{"signed":{},"signatures":[]}`), "policy-etag", nil
	}
	return append([]byte(nil), store.manifest...), "manifest-etag", nil
}

func (store *blockingStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "https://cos.example.invalid/signed", nil
}

func (store *blockingStore) unblock() {
	store.releaseOnce.Do(func() { close(store.release) })
}

func (store *blockingStore) getCount(key string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.gets[key]
}

func TestChangelogReturnsBodyAndUpstreamETag(t *testing.T) {
	manifest := validManifest(t)
	changelog := []byte(`{"schemaVersion":1,"releases":[{"version":"0.4.4"}]}`)
	store := &fakeStore{
		get: func(key string, maxBytes int64) ([]byte, string, error) {
			switch key {
			case channelKey:
				return manifest, "channel-etag", nil
			case "releases/v0.4.4/gift-panel-changelog.json":
				if maxBytes != 2<<20 {
					t.Fatalf("changelog maxBytes = %d, want 2 MiB", maxBytes)
				}
				return changelog, "changelog-etag", nil
			default:
				t.Fatalf("Get key = %q, want known key", key)
				return nil, "", nil
			}
		},
		presign: func(string, time.Duration) (string, error) { return "", nil },
	}

	document, err := service.New(store, time.Now).Changelog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(document.Body) != string(changelog) {
		t.Fatalf("Body = %s, want upstream changelog", document.Body)
	}
	if document.ETag != "changelog-etag" {
		t.Fatalf("ETag = %q, want upstream ETag", document.ETag)
	}
}

func TestChangelogRejectsOversizedAndInvalidDocuments(t *testing.T) {
	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "oversized", body: append([]byte(`{"schemaVersion":1,"releases":[{}],"padding":"`), append([]byte(strings.Repeat("x", (2<<20)+1)), []byte(`"}`)...)...)},
		{name: "invalid schema", body: []byte(`{"schemaVersion":2,"releases":[{}]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{
				get: func(key string, _ int64) ([]byte, string, error) {
					if key == channelKey {
						return validManifest(t), "channel-etag", nil
					}
					return test.body, "changelog-etag", nil
				},
				presign: func(string, time.Duration) (string, error) { return "", nil },
			}

			_, err := service.New(store, time.Now).Changelog(context.Background())
			if !errors.Is(err, service.ErrReleaseInvalid) {
				t.Fatalf("Changelog() error = %v, want ErrReleaseInvalid", err)
			}
		})
	}
}

func validManifest(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(release.ChannelManifest{
		SchemaVersion: 1,
		TagName:       "v0.4.4",
		PublishedAt:   "2026-08-14T12:00:00Z",
		Asset: release.AssetManifest{
			Name:      "gift-panel-windows-x64.exe",
			ObjectKey: "releases/v0.4.4/gift-panel-windows-x64.exe",
			Size:      12345678,
			SHA256:    strings.Repeat("a", 64),
		},
		ChangelogObjectKey: "releases/v0.4.4/gift-panel-changelog.json",
	})
	if err != nil {
		t.Fatal(fmt.Errorf("marshal manifest: %w", err))
	}
	return body
}
