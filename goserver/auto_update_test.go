package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareStableVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"1.2.0", "1.1.9", 1},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3", "2.0.0", -1},
		{"1.2.3+build.4", "1.2.3", 0},
	}
	for _, test := range tests {
		got, err := compareStableVersions(test.left, test.right)
		if err != nil {
			t.Fatalf("compare %q and %q: %v", test.left, test.right, err)
		}
		if got != test.want {
			t.Fatalf("compare %q and %q = %d, want %d", test.left, test.right, got, test.want)
		}
	}
	if _, err := compareStableVersions("1.2.3-beta.1", "1.2.2"); err == nil {
		t.Fatal("prerelease version must be rejected")
	}
}

func TestDefaultUpdateSourcesPreferDomesticMirror(t *testing.T) {
	previous := updateAPIBaseURLHex
	updateAPIBaseURLHex = hex.EncodeToString([]byte("https://updates.example.test"))
	t.Cleanup(func() { updateAPIBaseURLHex = previous })

	sources := defaultUpdateReleaseSources()
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	if sources[0].Name != "国内镜像" || sources[0].URL != "https://updates.example.test/api/v1/releases/latest" || sources[0].GitHub {
		t.Fatalf("domestic source = %#v", sources[0])
	}
	if sources[1].Name != "GitHub" || sources[1].URL != updateGitHubReleaseURL || !sources[1].GitHub {
		t.Fatalf("GitHub source = %#v", sources[1])
	}
}

func TestDefaultUpdateSourcesUseGitHubOnlyWithoutDomesticConfiguration(t *testing.T) {
	previous := updateAPIBaseURLHex
	updateAPIBaseURLHex = ""
	t.Cleanup(func() { updateAPIBaseURLHex = previous })

	sources := defaultUpdateReleaseSources()
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	if sources[0].Name != "GitHub" || sources[0].URL != updateGitHubReleaseURL || !sources[0].GitHub {
		t.Fatalf("GitHub source = %#v", sources[0])
	}
}

func TestExpectedUpdatePublisherConfiguration(t *testing.T) {
	t.Run("development allows missing publisher", func(t *testing.T) {
		publisher, err := decodeExpectedUpdatePublisher("dev", "")
		if err != nil || publisher != "" {
			t.Fatalf("publisher = %q, err = %v", publisher, err)
		}
	})
	t.Run("release requires publisher", func(t *testing.T) {
		if _, err := decodeExpectedUpdatePublisher("1.2.3", ""); err == nil {
			t.Fatal("release build must reject a missing publisher")
		}
	})
	t.Run("decodes exact UTF-8 subject", func(t *testing.T) {
		const subject = "CN=预期发布者, O=Expected Publisher"
		publisher, err := decodeExpectedUpdatePublisher("1.2.3", hex.EncodeToString([]byte(subject)))
		if err != nil || publisher != subject {
			t.Fatalf("publisher = %q, err = %v", publisher, err)
		}
	})
	t.Run("rejects invalid hex and UTF-8", func(t *testing.T) {
		for _, encoded := range []string{"not-hex", "ff"} {
			if _, err := decodeExpectedUpdatePublisher("1.2.3", encoded); err == nil {
				t.Fatalf("publisher encoding %q must be rejected", encoded)
			}
		}
	})
}

func TestDomesticUpdateURLAcceptsUppercaseHexAndNormalizesRootPath(t *testing.T) {
	previous := updateAPIBaseURLHex
	updateAPIBaseURLHex = strings.ToUpper(hex.EncodeToString([]byte("https://updates.example.test/")))
	t.Cleanup(func() { updateAPIBaseURLHex = previous })

	if got := domesticUpdateReleaseURL(); got != "https://updates.example.test/api/v1/releases/latest" {
		t.Fatalf("domestic update release URL = %q", got)
	}
}

func TestDomesticUpdateURLAcceptsCanonicalExplicitPort(t *testing.T) {
	previous := updateAPIBaseURLHex
	updateAPIBaseURLHex = hex.EncodeToString([]byte("https://updates.example.test:65535"))
	t.Cleanup(func() { updateAPIBaseURLHex = previous })

	if got := domesticUpdateReleaseURL(); got != "https://updates.example.test:65535/api/v1/releases/latest" {
		t.Fatalf("domestic update release URL = %q", got)
	}
}

func TestDomesticUpdateURLPreservesDNSHostWithNumericSubdomain(t *testing.T) {
	previous := updateAPIBaseURLHex
	updateAPIBaseURLHex = hex.EncodeToString([]byte("https://123.updates.example.test"))
	t.Cleanup(func() { updateAPIBaseURLHex = previous })

	if got := domesticUpdateReleaseURL(); got != "https://123.updates.example.test/api/v1/releases/latest" {
		t.Fatalf("domestic update release URL = %q", got)
	}
}

func TestDomesticUpdateURLRejectsInvalidUTF8Hex(t *testing.T) {
	previous := updateAPIBaseURLHex
	updateAPIBaseURLHex = "ff"
	t.Cleanup(func() { updateAPIBaseURLHex = previous })

	if got := domesticUpdateReleaseURL(); got != "" {
		t.Fatalf("domestic update release URL = %q, want empty", got)
	}
}

func TestDomesticUpdateURLRejectsUnsafeBaseURLs(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "non-HTTPS", value: "http://updates.example.test"},
		{name: "credentials", value: "https://user:password@updates.example.test"},
		{name: "empty userinfo", value: "https://@updates.example.test"},
		{name: "query", value: "https://updates.example.test?channel=stable"},
		{name: "empty query", value: "https://updates.example.test?"},
		{name: "fragment", value: "https://updates.example.test#stable"},
		{name: "path", value: "https://updates.example.test/releases"},
		{name: "dot path", value: "https://updates.example.test/."},
		{name: "dot segments", value: "https://updates.example.test/releases/../"},
		{name: "escaped dot path", value: "https://updates.example.test/%2e"},
		{name: "escaped slash path", value: "https://updates.example.test/%2F"},
		{name: "hostless port", value: "https://:443"},
		{name: "empty port", value: "https://updates.example.test:"},
		{name: "non-numeric port", value: "https://updates.example.test:invalid"},
		{name: "zero port", value: "https://updates.example.test:0"},
		{name: "out of range port", value: "https://updates.example.test:65536"},
		{name: "default port spelling", value: "https://updates.example.test:443"},
		{name: "noncanonical port spelling", value: "https://updates.example.test:065535"},
		{name: "uppercase host", value: "https://UPDATES.example.test"},
		{name: "Unicode host", value: "https://例子.测试"},
		{name: "canonical IPv4 host", value: "https://127.0.0.1"},
		{name: "noncanonical IPv4 host", value: "https://127.1"},
		{name: "expanded IPv6 host", value: "https://[0:0:0:0:0:0:0:1]"},
		{name: "compressed IPv6 host", value: "https://[::1]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := updateAPIBaseURLHex
			updateAPIBaseURLHex = hex.EncodeToString([]byte(test.value))
			t.Cleanup(func() { updateAPIBaseURLHex = previous })
			if got := domesticUpdateReleaseURL(); got != "" {
				t.Fatalf("domestic update release URL = %q, want empty", got)
			}
		})
	}
}

func TestAutomaticUpdateWaitsUntilBackgroundIsIdle(t *testing.T) {
	binary := []byte("idle update executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	var requests atomic.Int32

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/release":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/asset", Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/asset":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName, CheckPeriod: time.Hour,
		ReleaseSources: []updateReleaseSource{{Name: "GitHub", URL: server.URL + "/release", GitHub: true}},
	})
	var idle atomic.Bool
	ready := make(chan string, 1)
	updater.SetAutomaticAllowed(idle.Load)
	updater.SetOnReady(func(version string) { ready <- version })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go updater.Run(ctx)

	time.Sleep(40 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("automatic update made %d request(s) while a page was open", got)
	}
	idle.Store(true)
	updater.NotifyIdle()
	select {
	case version := <-ready:
		if version != "1.1.0" {
			t.Fatalf("ready version = %q", version)
		}
	case <-time.After(time.Second):
		t.Fatal("idle automatic update did not download")
	}
	if status := updater.Status(); status.State != "ready" || !status.RestartRequired {
		t.Fatalf("status = %#v", status)
	}
}

func TestInstalledUpdateMarkerIsConsumedOnce(t *testing.T) {
	root := t.TempDir()
	metadataPath := filepath.Join(root, "pending-update.json")
	if err := writeInstalledUpdateMarker(metadataPath, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "1.2.3", ExecutablePath: filepath.Join(root, "gift-panel.exe"), UpdatesDir: root,
		ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.com/release"}}, AssetName: updateAssetName,
	})
	if version := updater.ConsumeInstalledVersion(); version != "1.2.3" {
		t.Fatalf("installed version = %q", version)
	}
	if version := updater.ConsumeInstalledVersion(); version != "" {
		t.Fatalf("installed marker was consumed twice: %q", version)
	}
}

func TestFindReleaseAssetRejectsInvalidDigest(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("z", 64)
	_, err := findReleaseAsset(githubRelease{Assets: []githubAsset{{
		Name: updateAssetName, Size: 10, Digest: validDigest, DownloadURL: "https://example.com/app.exe",
	}}}, updateAssetName)
	if err == nil {
		t.Fatal("non-hex digest must be rejected")
	}
}

func TestReleaseRejectsMissingExecutableSize(t *testing.T) {
	binary := []byte("mirrored executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	assetRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{
					{Name: updateAssetName, DownloadURL: server.URL + "/asset"},
					{Name: updateAssetName + ".sha256", DownloadURL: server.URL + "/checksum"},
				},
			})
		case "/checksum":
			_, _ = w.Write([]byte(digest + "  " + updateAssetName))
		case "/asset":
			assetRequests++
			w.Header().Del("Content-Length")
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{{Name: "Primary", URL: server.URL + "/release"}},
	})
	updater.checkAndDownload(context.Background(), true)
	status := updater.Status()
	if status.State != "error" {
		t.Fatalf("status = %#v", status)
	}
	if assetRequests != 0 {
		t.Fatalf("asset requests = %d, want 0 for missing required size", assetRequests)
	}
	if _, err := os.Stat(filepath.Join(root, "updates", "gift-panel-pending.exe")); !os.IsNotExist(err) {
		t.Fatalf("pending executable must not exist: %v", err)
	}
}

func TestUpdaterFallsBackToGitHubWhenPrimarySourceFails(t *testing.T) {
	binary := []byte("fallback executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	primaryRequests := 0
	gitHubRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/primary":
			primaryRequests++
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/github":
			gitHubRequests++
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/asset", Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/asset":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{
			{Name: "Primary", URL: server.URL + "/primary"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
	})
	updater.checkAndDownload(context.Background(), true)
	if status := updater.Status(); status.State != "ready" {
		t.Fatalf("status = %#v", status)
	}
	if primaryRequests != 1 || gitHubRequests != 1 {
		t.Fatalf("requests: Primary=%d GitHub=%d", primaryRequests, gitHubRequests)
	}
}

func TestUpdaterFallsBackToGitHubWhenPrimaryAssetIsIncomplete(t *testing.T) {
	binary := []byte("fallback after incomplete primary asset")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	primaryRequests := 0
	gitHubRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/primary":
			primaryRequests++
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/primary-asset", Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/github":
			gitHubRequests++
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/github-asset", Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/primary-asset":
			_, _ = w.Write(binary[:len(binary)-1])
		case "/github-asset":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{
			{Name: "Primary", URL: server.URL + "/primary"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
	})
	updater.checkAndDownload(context.Background(), true)
	if status := updater.Status(); status.State != "ready" || status.LatestVersion != "1.1.0" {
		t.Fatalf("status = %#v", status)
	}
	if primaryRequests != 1 || gitHubRequests != 1 {
		t.Fatalf("requests: Primary=%d GitHub=%d", primaryRequests, gitHubRequests)
	}
}

func TestUpdaterSignatureFailureFallsBackToSameVersionGitHubCandidate(t *testing.T) {
	domesticBinary := []byte("domestic executable with the wrong publisher")
	domesticDigestBytes := sha256.Sum256(domesticBinary)
	domesticDigest := hex.EncodeToString(domesticDigestBytes[:])
	githubBinary := []byte("GitHub executable with the expected publisher")
	githubDigestBytes := sha256.Sum256(githubBinary)
	githubDigest := hex.EncodeToString(githubDigestBytes[:])

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/domestic":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/domestic-asset", Size: int64(len(domesticBinary)), Digest: "sha256:" + domesticDigest,
				}},
			})
		case "/github":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/github-asset", Size: int64(len(githubBinary)), Digest: "sha256:" + githubDigest,
				}},
			})
		case "/domestic-asset":
			_, _ = w.Write(domesticBinary)
		case "/github-asset":
			_, _ = w.Write(githubBinary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{
			{Name: "Domestic", URL: server.URL + "/domestic"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
		VerifyExecutable: func(path string) error {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(contents) == string(domesticBinary) {
				return errors.New("publisher mismatch")
			}
			return nil
		},
	})

	updater.checkAndDownload(context.Background(), true)
	if status := updater.Status(); status.State != "ready" || status.LatestVersion != "1.1.0" {
		t.Fatalf("status = %#v", status)
	}
	downloaded, err := os.ReadFile(filepath.Join(root, "updates", "gift-panel-pending.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(githubBinary) {
		t.Fatalf("downloaded = %q, want GitHub candidate", downloaded)
	}
}

func TestUpdaterSignatureFailuresLeaveNoPendingExecutable(t *testing.T) {
	binary := []byte("correct digest but invalid publisher")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/domestic", "/github":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/asset", Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/asset":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: updatesDir, AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{
			{Name: "Domestic", URL: server.URL + "/domestic"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
		VerifyExecutable: func(string) error { return errors.New("CN=Unexpected Publisher, O=Diagnostic Detail") },
	})

	updater.checkAndDownload(context.Background(), true)
	status := updater.Status()
	if status.State != "error" || status.RestartRequired {
		t.Fatalf("status = %#v", status)
	}
	if !strings.Contains(status.Message, "安全校验失败") {
		t.Fatalf("status message = %q, want stable safety verification error", status.Message)
	}
	if strings.Contains(status.Message, "Unexpected Publisher") || strings.Contains(status.Message, "Diagnostic Detail") {
		t.Fatalf("status message leaked signer diagnostics: %q", status.Message)
	}
	if updater.HasPending() {
		t.Fatal("signature failure must not create a pending update")
	}
	if _, err := os.Stat(filepath.Join(updatesDir, "gift-panel-pending.exe")); !os.IsNotExist(err) {
		t.Fatalf("pending executable must not survive signature failure: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(updatesDir, "*.download")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary downloads after signature failure = %v, err = %v", matches, err)
	}
}

func TestUpdaterCleanupFailureStopsFallbackAndReportsError(t *testing.T) {
	binary := []byte("executable whose rejected download remains locked")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	githubAssetRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/domestic", "/github":
			assetPath := "/domestic-asset"
			if r.URL.Path == "/github" {
				assetPath = "/github-asset"
			}
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + assetPath, Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/domestic-asset":
			_, _ = w.Write(binary)
		case "/github-asset":
			githubAssetRequests++
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	removeAttempts := 0
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{
			{Name: "Domestic", URL: server.URL + "/domestic"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
		VerifyExecutable: func(string) error { return errors.New("CN=Unexpected Publisher") },
		RemoveFile: func(path string) error {
			if strings.HasSuffix(path, ".download") {
				removeAttempts++
				return errors.New("locked artifact")
			}
			return os.Remove(path)
		},
	})

	updater.checkAndDownload(context.Background(), true)
	status := updater.Status()
	if status.State != "error" || !strings.Contains(status.Message, "清理") {
		t.Fatalf("status = %#v", status)
	}
	if strings.Contains(status.Message, "locked artifact") || strings.Contains(status.Message, "Unexpected Publisher") {
		t.Fatalf("user-visible status leaked diagnostic detail: %q", status.Message)
	}
	if removeAttempts != 3 {
		t.Fatalf("remove attempts = %d, want 3", removeAttempts)
	}
	if githubAssetRequests != 0 {
		t.Fatalf("GitHub asset requests = %d, want 0 after cleanup integrity failure", githubAssetRequests)
	}
	if updater.HasPending() {
		t.Fatal("cleanup failure must never produce pending update")
	}
}

func TestUpdaterChecksGitHubWhenPrimarySourceIsBehind(t *testing.T) {
	binary := []byte("newer GitHub executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	gitHubRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/primary":
			_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.1.0"})
		case "/github":
			gitHubRequests++
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.2.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/asset", Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/asset":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{
			{Name: "Primary", URL: server.URL + "/primary"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
	})
	updater.checkAndDownload(context.Background(), true)
	if status := updater.Status(); status.State != "ready" || status.LatestVersion != "1.2.0" {
		t.Fatalf("status = %#v", status)
	}
	if gitHubRequests != 1 {
		t.Fatalf("GitHub requests = %d", gitHubRequests)
	}
	downloaded, err := os.ReadFile(filepath.Join(root, "updates", "gift-panel-pending.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(binary) {
		t.Fatalf("downloaded = %q", downloaded)
	}
}

func TestManualCheckDownloadsUpdateWhenAutomaticChecksAreDisabled(t *testing.T) {
	binary := []byte("new gift panel executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/asset", Size: int64(len(binary)), Digest: "sha256:" + digest,
				}},
			})
		case "/asset":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	disabled := false
	state.Settings.AutoUpdate = &disabled
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	updater := newAutoUpdater(autoUpdaterOptions{
		Store: store, Client: server.Client(), CurrentVersion: "1.0.0",
		ExecutablePath: filepath.Join(root, "gift-panel.exe"), UpdatesDir: filepath.Join(root, "updates"),
		ReleaseURL: server.URL + "/release", AssetName: updateAssetName, CheckPeriod: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go updater.Run(ctx)
	updater.CheckNow()

	deadline := time.Now().Add(3 * time.Second)
	for updater.Status().State != "ready" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := updater.Status()
	if status.State != "ready" || status.LatestVersion != "1.1.0" || !status.RestartRequired {
		t.Fatalf("status = %#v", status)
	}
	if status.AutoUpdate {
		t.Fatal("manual check must not enable automatic updates")
	}
	downloaded, err := os.ReadFile(filepath.Join(root, "updates", "gift-panel-pending.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(binary) {
		t.Fatalf("downloaded = %q", downloaded)
	}
}

func TestDownloadRejectsDigestMismatch(t *testing.T) {
	binary := []byte("tampered executable")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(binary)
	}))
	defer server.Close()
	root := t.TempDir()
	verificationAttempted := false
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), ReleaseURL: server.URL, AssetName: updateAssetName,
		VerifyExecutable: func(string) error {
			verificationAttempted = true
			return nil
		},
	})
	_, err := updater.downloadAsset(context.Background(), "1.1.0", githubAsset{
		Name: updateAssetName, DownloadURL: server.URL, Size: int64(len(binary)), Digest: "sha256:" + strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatal("digest mismatch must fail")
	}
	if verificationAttempted {
		t.Fatal("Authenticode verifier must not run before SHA-256 succeeds")
	}
	if _, statErr := os.Stat(filepath.Join(root, "updates", "gift-panel-pending.exe")); !os.IsNotExist(statErr) {
		t.Fatalf("pending file must not survive mismatch: %v", statErr)
	}
}

func TestDownloadRejectsPendingWhenPostRenameVerificationFails(t *testing.T) {
	binary := []byte("signed temporary executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(binary)
	}))
	defer server.Close()
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: updatesDir, ReleaseURL: server.URL, AssetName: updateAssetName,
		VerifyExecutable: func(path string) error {
			if filepath.Base(path) == "gift-panel-pending.exe" {
				return errors.New("pending publisher mismatch")
			}
			return nil
		},
	})
	_, err := updater.downloadAsset(context.Background(), "1.1.0", githubAsset{
		Name: updateAssetName, DownloadURL: server.URL, Size: int64(len(binary)), Digest: "sha256:" + digest,
	})
	if err == nil {
		t.Fatal("post-rename signature failure must reject pending executable")
	}
	if _, statErr := os.Stat(filepath.Join(updatesDir, "gift-panel-pending.exe")); !os.IsNotExist(statErr) {
		t.Fatalf("pending executable must be removed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(updatesDir, "pending-update.json")); !os.IsNotExist(statErr) {
		t.Fatalf("pending metadata must not be written: %v", statErr)
	}
}

func TestDefaultStateEnablesAutomaticUpdates(t *testing.T) {
	state := defaultAppState()
	if !autoUpdateEnabled(state) {
		t.Fatal("automatic updates should default to enabled")
	}
	state.Settings.AutoUpdate = nil
	normalizeAppState(&state)
	if !autoUpdateEnabled(state) {
		t.Fatal("missing legacy setting should migrate to enabled")
	}
}
