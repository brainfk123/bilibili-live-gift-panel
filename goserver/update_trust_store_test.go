package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testTrustNow = time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)

func TestTrustStoreUsesEmbeddedBaseline(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("naisnet-primary", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)

	got, err := store.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != updateTrustModeCurrent || got.Policy.Epoch != 1 {
		t.Fatalf("resolved policy = %#v, want current epoch 1", got)
	}
	cache := readTestTrustCache(t, store.CacheDir)
	if cache.HighestEpoch != 1 || !bytes.Equal(cache.PolicyBytes, embedded) {
		t.Fatalf("cache epoch/bytes = %d/%t, want 1/true", cache.HighestEpoch, bytes.Equal(cache.PolicyBytes, embedded))
	}
}

func TestTrustStoreCurrentAuthorizationUsesPinnedClock(t *testing.T) {
	key := newTestTrustKey(t)
	systemNow := time.Now().UTC()
	pinned := systemNow.Add(-2 * time.Hour)
	embedded := signedTestTrustPolicy(t, key, 1, systemNow.Add(-time.Hour), stableTestRule("naisnet-primary", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	store := newTestTrustStore(t, &key.PublicKey, embedded, pinned)

	got, err := store.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity := updateArtifactIdentity{Tag: "v0.4.12", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}}
	assertErrorCode(t, got.Authorize(identity), "")
}

func TestTrustStoreChoosesHighestValidEpoch(t *testing.T) {
	tests := []struct {
		name          string
		domesticEpoch uint64
		githubEpoch   uint64
		wantEpoch     uint64
	}{
		{name: "domestic newer and GitHub stale", domesticEpoch: 3, githubEpoch: 2, wantEpoch: 3},
		{name: "GitHub newer and domestic stale", domesticEpoch: 2, githubEpoch: 4, wantEpoch: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := newTestTrustKey(t)
			embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
			domestic := signedTestTrustPolicy(t, key, test.domesticEpoch, testTrustNow.AddDate(1, 0, 0), stableTestRule("domestic", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
			github := signedTestTrustPolicy(t, key, test.githubEpoch, testTrustNow.AddDate(1, 0, 0), stableTestRule("github", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
			server := newTrustSourceServer(t, map[string][]byte{"/domestic": domestic, "/github": github})
			store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)

			got, err := store.Resolve(context.Background(),
				updateTrustSource{Name: "domestic", URL: server.URL + "/domestic"},
				updateTrustSource{Name: "github", URL: server.URL + "/github"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != updateTrustModeCurrent || got.Policy.Epoch != test.wantEpoch {
				t.Fatalf("resolved mode/epoch = %s/%d, want current/%d", got.Mode, got.Policy.Epoch, test.wantEpoch)
			}
			if cache := readTestTrustCache(t, store.CacheDir); cache.HighestEpoch != test.wantEpoch {
				t.Fatalf("persisted highest epoch = %d, want %d", cache.HighestEpoch, test.wantEpoch)
			}
		})
	}
}

func TestTrustStoreVerifiesSourcesIndependentlyAndBoundsBodies(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	valid := signedTestTrustPolicy(t, key, 2, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-2", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	server := newTrustSourceServer(t, map[string][]byte{
		"/oversized-invalid": bytes.Repeat([]byte{'x'}, maxUpdateTrustPolicyBytes+1),
		"/valid":             valid,
	})
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)

	got, err := store.Resolve(context.Background(),
		updateTrustSource{Name: "domestic", URL: server.URL + "/oversized-invalid"},
		updateTrustSource{Name: "github", URL: server.URL + "/valid"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy.Epoch != 2 {
		t.Fatalf("epoch = %d, want independently verified epoch 2", got.Policy.Epoch)
	}
}

func TestTrustStoreUsesValidCacheWhenSourcesOffline(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	cached := signedTestTrustPolicy(t, key, 2, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-2", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	seedTestTrustCache(t, store.CacheDir, cached, 2, []updateCertificateIdentity{{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}})
	store.Client = offlineTrustClient()

	got, err := store.Resolve(context.Background(),
		updateTrustSource{Name: "domestic", URL: "https://domestic.invalid/policy"},
		updateTrustSource{Name: "github", URL: "https://github.invalid/policy"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != updateTrustModeCurrent || got.Policy.Epoch != 2 {
		t.Fatalf("resolved mode/epoch = %s/%d, want current/2", got.Mode, got.Policy.Epoch)
	}
}

func TestTrustStoreExpiredCacheFreezesOnlyAcceptedStablePrimaryIdentities(t *testing.T) {
	key := newTestTrustKey(t)
	expires := testTrustNow.AddDate(1, 0, 0)
	embedded := signedTestTrustPolicy(t, key, 1, expires, stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	cached := signedTestTrustPolicy(t, key, 2, expires,
		stableTestRule("naisnet-primary", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"),
		bridgeTestRule(),
	)
	afterExpiry := expires.Add(time.Hour)
	store := newTestTrustStore(t, &key.PublicKey, embedded, afterExpiry)
	seedTestTrustCache(t, store.CacheDir, cached, 2, []updateCertificateIdentity{{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}})
	store.Client = offlineTrustClient()

	got, err := store.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: "https://offline.invalid/policy"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != updateTrustModeExpiredIdentityFallback || got.Policy.Epoch != 2 {
		t.Fatalf("resolved mode/epoch = %s/%d, want expired_identity_fallback/2", got.Mode, got.Policy.Epoch)
	}
	if len(got.FrozenIdentities) != 1 || got.FrozenIdentities[0].Organization != "NaisNet Technology Co., Ltd." {
		t.Fatalf("frozen identities = %#v, want only accepted NaisNet stable identity", got.FrozenIdentities)
	}
	accepted := updateArtifactIdentity{Tag: "v9.9.9", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}}
	assertErrorCode(t, got.Authorize(accepted), "")
	accepted.Certificate.OrganizationID = "DIFFERENT"
	assertErrorCode(t, got.Authorize(accepted), "publisher_not_authorized")
	bridge := updateArtifactIdentity{Tag: "v0.4.11", Channel: updateChannelLegacyRushRush, Certificate: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}
	assertErrorCode(t, got.Authorize(bridge), "publisher_not_authorized")
}

func TestTrustStoreExpiredDownloadedPolicyCannotCreateFrozenIdentity(t *testing.T) {
	key := newTestTrustKey(t)
	expires := testTrustNow.AddDate(1, 0, 0)
	afterExpiry := expires.Add(time.Hour)
	embedded := signedTestTrustPolicy(t, key, 1, expires, stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	expiredNewIdentity := signedTestTrustPolicy(t, key, 2, expires, stableTestRule("new-primary", "Different Technology Co., Ltd.", "DIFFERENTID"))
	server := newTrustSourceServer(t, map[string][]byte{"/policy": expiredNewIdentity})
	store := newTestTrustStore(t, &key.PublicKey, embedded, afterExpiry)

	_, err := store.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: server.URL + "/policy"})
	assertErrorCode(t, err, "policy_unavailable")
	if _, statErr := os.Stat(filepath.Join(store.CacheDir, updateTrustCacheFilename)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expired download created cache: %v", statErr)
	}
}

func TestTrustStoreRejectsLowerEpochRollbackAfterRestart(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	epoch3 := signedTestTrustPolicy(t, key, 3, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-3", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	epoch2 := signedTestTrustPolicy(t, key, 2, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-2", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	server := newTrustSourceServer(t, map[string][]byte{"/epoch-3": epoch3, "/epoch-2": epoch2})
	cacheDir := t.TempDir()
	first := &updateTrustStore{Root: &key.PublicKey, EmbeddedPolicy: embedded, CacheDir: cacheDir, Client: server.Client(), Now: func() time.Time { return testTrustNow }}
	if got, err := first.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: server.URL + "/epoch-3"}); err != nil || got.Policy.Epoch != 3 {
		t.Fatalf("initial resolve = epoch %d, error %v", got.Policy.Epoch, err)
	}
	before := readTestTrustCacheBytes(t, cacheDir)

	restarted := &updateTrustStore{Root: &key.PublicKey, EmbeddedPolicy: embedded, CacheDir: cacheDir, Client: server.Client(), Now: func() time.Time { return testTrustNow }}
	got, err := restarted.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: server.URL + "/epoch-2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy.Epoch != 3 {
		t.Fatalf("epoch after restart = %d, want persisted epoch 3", got.Policy.Epoch)
	}
	if after := readTestTrustCacheBytes(t, cacheDir); !bytes.Equal(after, before) {
		t.Fatal("lower epoch replaced the highest accepted cache")
	}
}

func TestTrustStoreConcurrentResolveNeverLetsLowerEpochOverwriteHigher(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	epoch3 := signedTestTrustPolicy(t, key, 3, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-3", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	epoch4 := signedTestTrustPolicy(t, key, 4, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-4", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	lowerStarted := make(chan struct{})
	releaseLower := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/epoch-3":
			close(lowerStarted)
			<-releaseLower
			_, _ = response.Write(epoch3)
		case "/epoch-4":
			_, _ = response.Write(epoch4)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	store.Client = server.Client()
	type result struct {
		policy resolvedUpdateTrustPolicy
		err    error
	}
	lowerDone := make(chan result, 1)
	higherDone := make(chan result, 1)
	go func() {
		policy, err := store.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: server.URL + "/epoch-3"})
		lowerDone <- result{policy: policy, err: err}
	}()
	<-lowerStarted
	go func() {
		policy, err := store.Resolve(context.Background(), updateTrustSource{Name: "github", URL: server.URL + "/epoch-4"})
		higherDone <- result{policy: policy, err: err}
	}()

	var earlyHigher *result
	select {
	case completed := <-higherDone:
		earlyHigher = &completed
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseLower)
	lower := <-lowerDone
	if lower.err != nil {
		t.Fatalf("lower concurrent Resolve error = %v", lower.err)
	}
	var higher result
	if earlyHigher != nil {
		higher = *earlyHigher
	} else {
		higher = <-higherDone
	}
	if higher.err != nil {
		t.Fatalf("higher concurrent Resolve error = %v", higher.err)
	}
	if cache := readTestTrustCache(t, store.CacheDir); cache.HighestEpoch != 4 {
		t.Fatalf("concurrent persisted highest epoch = %d, want 4", cache.HighestEpoch)
	}
}

func TestTrustStoreReplacesLowerCachedEpochWithEmbeddedBaseline(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 2, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-2", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	lower := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	seedTestTrustCache(t, store.CacheDir, lower, 1, []updateCertificateIdentity{{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}})

	got, err := store.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy.Epoch != 2 {
		t.Fatalf("resolved epoch = %d, want embedded epoch 2", got.Policy.Epoch)
	}
	if cache := readTestTrustCache(t, store.CacheDir); cache.HighestEpoch != 2 || !bytes.Equal(cache.PolicyBytes, embedded) {
		t.Fatalf("persisted cache epoch/bytes = %d/%t, want embedded 2/true", cache.HighestEpoch, bytes.Equal(cache.PolicyBytes, embedded))
	}
}

func TestTrustStoreCorruptCacheFallsBackToEmbeddedWithoutOverwriting(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	corrupt := []byte(`{"version":1,"highestEpoch":9`)
	if err := os.WriteFile(filepath.Join(store.CacheDir, updateTrustCacheFilename), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	store.Client = offlineTrustClient()

	got, err := store.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: "https://offline.invalid/policy"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != updateTrustModeCurrent || got.Policy.Epoch != 1 {
		t.Fatalf("resolved mode/epoch = %s/%d, want embedded current/1", got.Mode, got.Policy.Epoch)
	}
	if after := readTestTrustCacheBytes(t, store.CacheDir); !bytes.Equal(after, corrupt) {
		t.Fatal("corrupt cache was overwritten, erasing evidence of the unknown persisted epoch")
	}
}

func TestTrustStoreDuplicateCacheFieldsAreCorrupt(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	cached := signedTestTrustPolicy(t, key, 2, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-2", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	seedTestTrustCache(t, store.CacheDir, cached, 2, []updateCertificateIdentity{{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}})
	duplicated := bytes.Replace(readTestTrustCacheBytes(t, store.CacheDir), []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
	if err := os.WriteFile(filepath.Join(store.CacheDir, updateTrustCacheFilename), duplicated, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := store.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy.Epoch != 1 {
		t.Fatalf("duplicate-field cache resolved epoch = %d, want embedded epoch 1", got.Policy.Epoch)
	}
	if after := readTestTrustCacheBytes(t, store.CacheDir); !bytes.Equal(after, duplicated) {
		t.Fatal("duplicate-field cache was overwritten")
	}
}

func TestAtomicTrustCacheInterruptedBeforeRenamePreservesPreviousCache(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	epoch2 := signedTestTrustPolicy(t, key, 2, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-2", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	epoch3 := signedTestTrustPolicy(t, key, 3, testTrustNow.AddDate(1, 0, 0), stableTestRule("epoch-3", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	server := newTrustSourceServer(t, map[string][]byte{"/epoch-3": epoch3})
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	seedTestTrustCache(t, store.CacheDir, epoch2, 2, []updateCertificateIdentity{{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}})
	before := readTestTrustCacheBytes(t, store.CacheDir)
	interrupted := errors.New("interrupted before rename")
	store.Rename = func(temporaryPath, finalPath string) error {
		if finalPath != filepath.Join(store.CacheDir, updateTrustCacheFilename) {
			t.Fatalf("rename final base = %q, want cache file", filepath.Base(finalPath))
		}
		temporary, err := os.ReadFile(temporaryPath)
		if err != nil {
			t.Fatalf("temporary cache was not readable after sync/close: %v", err)
		}
		var envelope testTrustCacheEnvelope
		if err := json.Unmarshal(temporary, &envelope); err != nil || envelope.HighestEpoch != 3 {
			t.Fatalf("temporary cache = epoch %d, error %v, want complete epoch 3", envelope.HighestEpoch, err)
		}
		return interrupted
	}

	_, err := store.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: server.URL + "/epoch-3"})
	if !errors.Is(err, interrupted) {
		t.Fatalf("Resolve error = %v, want interruption", err)
	}
	if after := readTestTrustCacheBytes(t, store.CacheDir); !bytes.Equal(after, before) {
		t.Fatal("interrupted write changed the previous cache")
	}
	entries, err := os.ReadDir(store.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != updateTrustCacheFilename {
		t.Fatalf("cache directory entries = %#v, want only committed cache", entries)
	}

	restarted := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	restarted.CacheDir = store.CacheDir
	restarted.Client = offlineTrustClient()
	got, err := restarted.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: "https://offline.invalid/policy"})
	if err != nil || got.Policy.Epoch != 2 {
		t.Fatalf("restart resolve = epoch %d, error %v, want preserved epoch 2", got.Policy.Epoch, err)
	}
}

func TestTrustStoreErrorsNeverExposeSourceURL(t *testing.T) {
	key := newTestTrustKey(t)
	embedded := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(-1, 0, 0), stableTestRule("epoch-1", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094"))
	store := newTestTrustStore(t, &key.PublicKey, embedded, testTrustNow)
	store.Client = offlineTrustClient()
	secretURL := "https://updates.invalid/policy?token=secret-value"
	_, err := store.Resolve(context.Background(), updateTrustSource{Name: "domestic", URL: secretURL})
	if err == nil {
		t.Fatal("Resolve error = nil, want bounded failure")
	}
	if strings.Contains(err.Error(), secretURL) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("error exposed source URL or query: %q", err)
	}
}

type testTrustCacheEnvelope struct {
	Version          int                         `json:"version"`
	HighestEpoch     uint64                      `json:"highestEpoch"`
	PolicySHA256     string                      `json:"policySha256"`
	PolicyBytes      []byte                      `json:"policyBytes"`
	FrozenIdentities []updateCertificateIdentity `json:"frozenIdentities"`
}

func newTestTrustKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signedTestTrustPolicy(t testing.TB, key *ecdsa.PrivateKey, epoch uint64, expiresAt time.Time, rules ...updatePublisherRule) []byte {
	t.Helper()
	signed := publisherPolicySigned{Epoch: epoch, ExpiresAt: expiresAt.UTC().Format(time.RFC3339), Publishers: rules}
	canonical, err := canonicalizePublisherPolicySigned(signed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	document := publisherPolicyDocument{Signed: signed, Signatures: []publisherPolicySignature{{Algorithm: "ecdsa-p256-sha256", Signature: base64.StdEncoding.EncodeToString(signature)}}}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func stableTestRule(id, organization, organizationID string) updatePublisherRule {
	return updatePublisherRule{ID: id, Role: "primary", Country: "CN", Organization: organization, OrganizationID: organizationID, AllowedChannel: updateChannelStable, AllowedTags: []string{"v0.4.12"}}
}

func bridgeTestRule() updatePublisherRule {
	return updatePublisherRule{ID: "rushrush-bridge", Role: "bridge", Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P", AllowedChannel: updateChannelLegacyRushRush, AllowedTags: []string{"v0.4.11"}}
}

func newTestTrustStore(t testing.TB, root *ecdsa.PublicKey, embedded []byte, now time.Time) *updateTrustStore {
	t.Helper()
	return &updateTrustStore{Root: root, EmbeddedPolicy: bytes.Clone(embedded), CacheDir: t.TempDir(), Client: http.DefaultClient, Now: func() time.Time { return now }}
}

func newTrustSourceServer(t testing.TB, bodies map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, ok := bodies[request.URL.Path]
		if !ok {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func offlineTrustClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
}

func seedTestTrustCache(t testing.TB, cacheDir string, policy []byte, epoch uint64, identities []updateCertificateIdentity) {
	t.Helper()
	digest := sha256.Sum256(policy)
	envelope := testTrustCacheEnvelope{Version: 1, HighestEpoch: epoch, PolicySHA256: hex.EncodeToString(digest[:]), PolicyBytes: bytes.Clone(policy), FrozenIdentities: append([]updateCertificateIdentity(nil), identities...)}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, updateTrustCacheFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestTrustCache(t testing.TB, cacheDir string) testTrustCacheEnvelope {
	t.Helper()
	data := readTestTrustCacheBytes(t, cacheDir)
	var envelope testTrustCacheEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("cache has trailing data: %v", err)
	}
	return envelope
}

func readTestTrustCacheBytes(t testing.TB, cacheDir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cacheDir, updateTrustCacheFilename))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
