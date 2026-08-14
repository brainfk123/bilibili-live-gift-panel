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

	sut := service.New(store, channelKey, func() time.Time { return clock })
	first, err := sut.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	second, err := sut.Latest(context.Background())
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
	sut := service.New(store, channelKey, func() time.Time { return clock })
	if _, err := sut.Latest(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(61 * time.Second)
	got, err := sut.Latest(context.Background())
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
	sut := service.New(store, channelKey, func() time.Time { return clock })
	if _, err := sut.Latest(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(61 * time.Second)
	got, err := sut.Latest(context.Background())
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
		_, err := service.New(store, channelKey, time.Now).Latest(context.Background())
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
		_, err := service.New(store, channelKey, time.Now).Latest(context.Background())
		if !errors.Is(err, service.ErrReleaseInvalid) {
			t.Fatalf("Latest() error = %v, want ErrReleaseInvalid", err)
		}
	})

	t.Run("signer failure", func(t *testing.T) {
		store := &fakeStore{
			get:     func(string, int64) ([]byte, string, error) { return validManifest(t), "etag", nil },
			presign: func(string, time.Duration) (string, error) { return "", errors.New("signer unavailable") },
		}
		_, err := service.New(store, channelKey, time.Now).Latest(context.Background())
		if !errors.Is(err, service.ErrDownloadUnavailable) {
			t.Fatalf("Latest() error = %v, want ErrDownloadUnavailable", err)
		}
	})
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

	document, err := service.New(store, channelKey, time.Now).Changelog(context.Background())
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

			_, err := service.New(store, channelKey, time.Now).Changelog(context.Background())
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
