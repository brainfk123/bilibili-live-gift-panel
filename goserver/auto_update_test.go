package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
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

func TestAutomaticUpdateRunsWhileAPageIsOpen(t *testing.T) {
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
	ready := make(chan string, 1)
	updater.SetOnReady(func(version string) { ready <- version })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go updater.Run(ctx)

	select {
	case version := <-ready:
		if version != "1.1.0" {
			t.Fatalf("ready version = %q", version)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic update did not download while a page was open")
	}
	if status := updater.Status(); status.State != "ready" || !status.RestartRequired {
		t.Fatalf("status = %#v", status)
	}
}

func TestCheckNowDoesNotResetAnActiveDownload(t *testing.T) {
	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"), UpdatesDir: root,
		ReleaseSources: []updateReleaseSource{{Name: "test", URL: "https://example.test/release"}},
	})
	updater.setStatus("downloading", "1.1.0", "正在下载", 42, false)

	status := updater.CheckNow()
	if status.State != "downloading" || status.Progress != 42 {
		t.Fatalf("reentrant check status = %#v", status)
	}
}

func TestDownloadPublishesRealByteProgressBeforeCompletion(t *testing.T) {
	binary := []byte("0123456789abcdef")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	firstHalfSent := make(chan struct{})
	finishDownload := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "16")
		_, _ = w.Write(binary[:8])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(firstHalfSent)
		<-finishDownload
		_, _ = w.Write(binary[8:])
	}))
	defer server.Close()

	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
	})
	updater.setStatus("downloading", "1.1.0", "正在下载", 0, false)
	done := make(chan error, 1)
	go func() {
		_, err := updater.downloadAsset(context.Background(), "1.1.0", githubAsset{
			Name: updateAssetName, DownloadURL: server.URL, Size: int64(len(binary)), Digest: "sha256:" + digest,
		})
		done <- err
	}()

	<-firstHalfSent
	deadline := time.Now().Add(time.Second)
	for updater.Status().Progress == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	progress := updater.Status().Progress
	close(finishDownload)
	if progress <= 0 || progress >= 100 {
		t.Fatalf("in-flight progress = %d, want 1..99", progress)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSlowVerificationBecomesVisibleOnlyAfterTheNoticeDelay(t *testing.T) {
	binary := []byte("signed executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) }))
	defer server.Close()
	verificationStarted := make(chan struct{})
	finishVerification := make(chan struct{})
	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		VerifyExecutable: func(string) error {
			select {
			case <-verificationStarted:
			default:
				close(verificationStarted)
			}
			<-finishVerification
			return nil
		},
	})
	updater.setStatus("downloading", "1.1.0", "正在下载", 0, false)
	done := make(chan error, 1)
	go func() {
		_, err := updater.downloadAsset(context.Background(), "1.1.0", githubAsset{
			Name: updateAssetName, DownloadURL: server.URL, Size: int64(len(binary)), Digest: "sha256:" + digest,
		})
		done <- err
	}()
	<-verificationStarted
	if status := updater.Status(); status.State != "downloading" {
		t.Fatalf("immediate verification state = %q, want downloading to avoid a flash", status.State)
	}
	time.Sleep(350 * time.Millisecond)
	if status := updater.Status(); status.State != "verifying" || status.Message != "正在校验更新文件…" {
		t.Fatalf("delayed verification status = %#v", status)
	}
	close(finishVerification)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInstallNowEndpointRequestsPendingUpdateInstallation(t *testing.T) {
	updater := newAutoUpdater(autoUpdaterOptions{CurrentVersion: "1.0.0"})
	updater.pending = &pendingUpdate{Version: "1.1.0"}
	requested := make(chan struct{}, 1)
	updater.SetOnInstallNow(func() { requested <- struct{}{} })

	response := httptest.NewRecorder()
	updater.handleInstall(response, httptest.NewRequest(http.MethodPost, "/api/update/install", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	select {
	case <-requested:
	default:
		t.Fatal("install endpoint did not request installation")
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

func TestRunSchedulesARestoredPendingUpdateAfterReadyCallbackRegistration(t *testing.T) {
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("restored signed executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.WriteFile(pendingPath, binary, 0o600); err != nil {
		t.Fatal(err)
	}
	pending := pendingUpdate{Version: "1.1.0", Size: int64(len(binary)), SHA256: digest, PendingPath: pendingPath, TargetPath: targetPath}
	metadata, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(updatesDir, "pending-update.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	disabled := false
	state.Settings.AutoUpdate = &disabled
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	updater := newAutoUpdater(autoUpdaterOptions{
		Store: store, CurrentVersion: "1.0.0", ExecutablePath: targetPath, UpdatesDir: updatesDir,
		ReleaseSources:   []updateReleaseSource{{Name: "test", URL: "https://example.test/release"}},
		VerifyExecutable: func(string) error { return nil },
	})
	ready := make(chan string, 1)
	updater.SetOnReady(func(version string) { ready <- version })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go updater.Run(ctx)
	select {
	case version := <-ready:
		if version != "1.1.0" {
			t.Fatalf("ready version = %q", version)
		}
	case <-time.After(time.Second):
		t.Fatal("restored pending update was not scheduled after callback registration")
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
			w.Header().Set("X-Gift-Panel-Update-Channel", string(updateChannelStable))
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
			if r.URL.Path == "/domestic" {
				w.Header().Set("X-Gift-Panel-Update-Channel", string(updateChannelStable))
			}
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
			if r.URL.Path == "/domestic" {
				w.Header().Set("X-Gift-Panel-Update-Channel", string(updateChannelStable))
			}
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

func TestAutoUpdaterTrustEnrollmentOptionsAreDisabledByDefault(t *testing.T) {
	updater := newAutoUpdater(autoUpdaterOptions{CurrentVersion: "1.0.0"})
	if updater.trustStore != nil || len(updater.trustSources) != 0 {
		t.Fatalf("default trust enrollment = store %v, sources %#v; want disabled", updater.trustStore, updater.trustSources)
	}
}

func TestAutoUpdaterPinsTrustStoreClockAndCopiesSources(t *testing.T) {
	pinned := time.Date(2029, 2, 3, 4, 5, 6, 0, time.UTC)
	store := &updateTrustStore{}
	sources := []updateTrustSource{{Name: "domestic", URL: "https://updates.example.invalid/policy"}}
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "1.0.0",
		Now:            func() time.Time { return pinned },
		TrustStore:     store,
		TrustSources:   sources,
	})
	sources[0].Name = "mutated"
	if updater.trustStore != store || len(updater.trustSources) != 1 || updater.trustSources[0].Name != "domestic" {
		t.Fatalf("trust enrollment options were not retained safely: store=%v sources=%#v", updater.trustStore, updater.trustSources)
	}
	if got := updater.trustStore.Now(); !got.Equal(pinned) {
		t.Fatalf("trust clock = %s, want %s", got, pinned)
	}
}

func TestUpdaterPolicyEnrollmentRequiresValidEmbeddedTrust(t *testing.T) {
	originalRoot, originalPolicy := updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64
	t.Cleanup(func() {
		updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64 = originalRoot, originalPolicy
	})
	tests := []struct {
		name        string
		root        string
		policy      string
		wantEnabled bool
		wantError   bool
	}{
		{name: "historical build", wantEnabled: false},
		{name: "valid enrollment build", root: base64.StdEncoding.EncodeToString(readFixture(t, "root-epoch-1-spki.der")), policy: base64.StdEncoding.EncodeToString(readFixture(t, "policy-epoch-1.json")), wantEnabled: true},
		{name: "partial enrollment build fails closed", root: base64.StdEncoding.EncodeToString(readFixture(t, "root-epoch-1-spki.der")), wantEnabled: true, wantError: true},
		{name: "invalid enrollment build fails closed", root: "not-base64", policy: "not-base64", wantEnabled: true, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64 = test.root, test.policy
			store, sources, err := defaultEmbeddedUpdateTrust(t.TempDir(), func() time.Time { return testTrustNow })
			if (store != nil) != test.wantEnabled {
				t.Fatalf("trust store enabled = %v, want %v", store != nil, test.wantEnabled)
			}
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %v", err, test.wantError)
			}
			if test.wantEnabled && len(sources) == 0 {
				t.Fatal("enrollment trust has no configured policy source")
			}
			if !test.wantEnabled && len(sources) != 0 {
				t.Fatalf("historical trust sources = %#v, want none", sources)
			}
		})
	}
}

func TestUpdaterDefaultEnrollmentClockAdvancesIntoExpiredFallback(t *testing.T) {
	originalRoot, originalPolicy := updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64
	t.Cleanup(func() {
		updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64 = originalRoot, originalPolicy
	})
	updateTrustRootSPKIBase64 = base64.StdEncoding.EncodeToString(readFixture(t, "root-epoch-1-spki.der"))
	updateTrustBootstrapPolicyBase64 = base64.StdEncoding.EncodeToString(readFixture(t, "policy-epoch-1.json"))
	now := testTrustNow
	clock := func() time.Time { return now }
	store, _, err := defaultEmbeddedUpdateTrust(t.TempDir(), clock)
	if err != nil {
		t.Fatal(err)
	}
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "0.4.12", TrustStore: store, Now: clock,
		ReleaseSources: []updateReleaseSource{{Name: "GitHub", URL: "https://example.invalid/release", GitHub: true}},
	})
	current, err := updater.resolveUpdateTrustPolicy(context.Background())
	if err != nil || current.Mode != updateTrustModeCurrent {
		t.Fatalf("current resolution = mode %q, error %v", current.Mode, err)
	}

	now = time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	expired, err := updater.resolveUpdateTrustPolicy(context.Background())
	if err != nil || expired.Mode != updateTrustModeExpiredIdentityFallback {
		t.Fatalf("post-expiry resolution = mode %q, error %v, want explicit fallback", expired.Mode, err)
	}
}

func TestUpdaterEnrollmentPendingVerificationBindingDeletionFailsClosed(t *testing.T) {
	github := false
	pending := pendingUpdate{
		SchemaVersion: pendingUpdateSchemaVersion,
		Version:       "0.4.12",
		Size:          123,
		SHA256:        strings.Repeat("a", 64),
		PendingPath:   `C:\Users\recognizable-secret\gift-panel-pending.exe`,
		TargetPath:    `C:\Program Files\GiftPanel\gift-panel.exe`,
		Verification: pendingUpdateVerification{
			Provenance: pendingVerificationSignedPolicy,
			SourceName: "domestic", SourceURLSHA256: strings.Repeat("b", 64), SourceGitHub: &github,
			Tag: "v0.4.12", Channel: updateChannelStable, ArtifactSHA256: strings.Repeat("a", 64),
			PolicyEpoch: 7, PolicySHA256: strings.Repeat("c", 64), PolicyMode: updateTrustModeCurrent,
		},
	}
	data, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(map[string]any)
	}{
		{name: "complete binding deleted", edit: func(document map[string]any) { delete(document, "verification") }},
		{name: "provenance deleted", edit: func(document map[string]any) { delete(document["verification"].(map[string]any), "provenance") }},
		{name: "tag and channel deleted", edit: func(document map[string]any) {
			delete(document["verification"].(map[string]any), "tag")
			delete(document["verification"].(map[string]any), "channel")
		}},
		{name: "tag deleted", edit: func(document map[string]any) { delete(document["verification"].(map[string]any), "tag") }},
		{name: "channel deleted", edit: func(document map[string]any) { delete(document["verification"].(map[string]any), "channel") }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			mutation.edit(document)
			tampered, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = decodePendingUpdateMetadata(tampered)
			assertUpdateCode(t, err, "pending_verification_invalid")
		})
	}
}

func TestUpdaterLegacyPendingMetadataMigratesOnceToExplicitProvenance(t *testing.T) {
	legacy := []byte(`{"version":"1.1.0","size":12,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pendingPath":"C:\\updates\\gift-panel-pending.exe","targetPath":"C:\\gift-panel.exe"}`)
	pending, migrated, err := decodePendingUpdateMetadata(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || pending.SchemaVersion != pendingUpdateSchemaVersion || pending.Verification.Provenance != pendingVerificationLegacyMigrated {
		t.Fatalf("legacy migration = migrated %v, schema %d, verification %#v", migrated, pending.SchemaVersion, pending.Verification)
	}
	migratedBytes, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(migratedBytes, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "verification")
	tampered, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = decodePendingUpdateMetadata(tampered)
	assertUpdateCode(t, err, "pending_verification_invalid")
}

func TestUpdaterPendingPolicyContextRejectsPolicyOrModeSubstitution(t *testing.T) {
	candidate := updateReleaseCandidate{
		Source:  updateReleaseSource{Name: "domestic", URL: "https://updates.example.invalid/release?token=recognizable-secret"},
		Release: githubRelease{TagName: "v0.4.12"}, Version: "0.4.12", Channel: updateChannelStable,
	}
	artifactSHA := strings.Repeat("a", 64)
	original := resolvedUpdateTrustPolicy{
		Policy: verifiedUpdateTrustPolicy{Epoch: 7, SignedRaw: []byte(`{"epoch":7}`)},
		Mode:   updateTrustModeCurrent,
	}
	verification, err := pendingVerificationForCandidate(candidate, artifactSHA, original)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(verification.SourceURLSHA256, "recognizable-secret") {
		t.Fatalf("source fingerprint leaked URL query: %#v", verification)
	}
	tests := []struct {
		name     string
		resolved resolvedUpdateTrustPolicy
	}{
		{name: "higher epoch", resolved: resolvedUpdateTrustPolicy{Policy: verifiedUpdateTrustPolicy{Epoch: 8, SignedRaw: []byte(`{"epoch":8}`)}, Mode: updateTrustModeCurrent}},
		{name: "same epoch different policy", resolved: resolvedUpdateTrustPolicy{Policy: verifiedUpdateTrustPolicy{Epoch: 7, SignedRaw: []byte(`{"epoch":7,"changed":true}`)}, Mode: updateTrustModeCurrent}},
		{name: "expiry transition changes mode", resolved: resolvedUpdateTrustPolicy{Policy: original.Policy, Mode: updateTrustModeExpiredIdentityFallback}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertUpdateCode(t, verifyPendingResolvedPolicyContext(verification, test.resolved), "pending_policy_context_changed")
		})
	}
	assertUpdateCode(t, verifyPendingResolvedPolicyContext(verification, original), "")
}

func TestUpdaterSameVersionCandidatesPersistIndependentSourceContext(t *testing.T) {
	policy := resolvedUpdateTrustPolicy{Policy: verifiedUpdateTrustPolicy{Epoch: 3, SignedRaw: []byte(`{"epoch":3}`)}, Mode: updateTrustModeCurrent}
	first := updateReleaseCandidate{Source: updateReleaseSource{Name: "domestic-a", URL: "https://a.example.invalid/release"}, Release: githubRelease{TagName: "v0.4.12"}, Version: "0.4.12", Channel: updateChannelStable}
	second := updateReleaseCandidate{Source: updateReleaseSource{Name: "domestic-b", URL: "https://b.example.invalid/release"}, Release: githubRelease{TagName: "v0.4.12"}, Version: "0.4.12", Channel: updateChannelStable}
	firstContext, err := pendingVerificationForCandidate(first, strings.Repeat("a", 64), policy)
	if err != nil {
		t.Fatal(err)
	}
	secondContext, err := pendingVerificationForCandidate(second, strings.Repeat("a", 64), policy)
	if err != nil {
		t.Fatal(err)
	}
	if firstContext.SourceName == secondContext.SourceName || firstContext.SourceURLSHA256 == secondContext.SourceURLSHA256 {
		t.Fatalf("same-version source contexts were transferred: first=%#v second=%#v", firstContext, secondContext)
	}
}

func TestUpdaterPendingPolicyCacheChangeRequiresRedownload(t *testing.T) {
	fixture := newDurablePolicyPendingFixture(t, testTrustNow.Add(time.Hour))
	higher := signedTestTrustPolicy(t, fixture.Key, 2, testTrustNow.AddDate(1, 0, 0), fixture.Rule)
	seedTestTrustCache(t, fixture.Store.CacheDir, higher, 2, []updateCertificateIdentity{fixture.Identity})

	err := fixture.Updater.InstallOnExit(false)
	if err == nil {
		t.Fatal("InstallOnExit accepted pending artifact under a different cached policy")
	}
	if fixture.Launched() || fixture.Updater.HasPending() {
		t.Fatalf("rotated policy result = launched %v, pending %v; want re-download", fixture.Launched(), fixture.Updater.HasPending())
	}
}

func TestUpdaterPendingPolicyExpiryTransitionRequiresRedownload(t *testing.T) {
	fixture := newDurablePolicyPendingFixture(t, testTrustNow.Add(time.Hour))
	fixture.SetNow(testTrustNow.Add(2 * time.Hour))

	err := fixture.Updater.InstallOnExit(false)
	if err == nil {
		t.Fatal("InstallOnExit accepted a current-mode pending artifact after policy entered expiry fallback")
	}
	if fixture.Launched() || fixture.Updater.HasPending() {
		t.Fatalf("expiry transition result = launched %v, pending %v; want re-download", fixture.Launched(), fixture.Updater.HasPending())
	}
}

func TestUpdaterPendingPolicyContextSurvivesRestartWithoutSubstitution(t *testing.T) {
	fixture := newDurablePolicyPendingFixture(t, testTrustNow.Add(time.Hour))
	restartedStore := &updateTrustStore{Root: &fixture.Key.PublicKey, EmbeddedPolicy: fixture.Policy, CacheDir: fixture.Store.CacheDir}
	restarted := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "0.4.11", ExecutablePath: fixture.TargetPath, UpdatesDir: fixture.UpdatesDir,
		ReleaseSources: []updateReleaseSource{fixture.Source}, AssetName: updateAssetName,
		TrustStore: restartedStore, Now: func() time.Time { return testTrustNow },
		InspectAuthenticode: func(string) (inspectedUpdateCertificate, error) {
			return inspectedUpdateCertificate{LegalIdentity: fixture.Identity}, nil
		},
		VerifyExecutable: func(string) error { return errors.New("legacy verifier must not run for restarted enrollment pending") },
	})
	if status := restarted.Status(); status.State != "ready" || !restarted.HasPending() {
		t.Fatalf("restarted status = %#v, pending %v; want exact-context ready", status, restarted.HasPending())
	}
}

func TestUpdaterPendingPolicyMetadataTamperRequiresRedownload(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "source fingerprint", field: "sourceUrlSha256", value: strings.Repeat("d", 64)},
		{name: "policy fingerprint", field: "policySha256", value: strings.Repeat("d", 64)},
		{name: "artifact fingerprint", field: "artifactSha256", value: strings.Repeat("d", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDurablePolicyPendingFixture(t, testTrustNow.Add(time.Hour))
			metadataPath := filepath.Join(fixture.UpdatesDir, "pending-update.json")
			data, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			document["verification"].(map[string]any)[test.field] = test.value
			tampered, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metadataPath, tampered, 0o600); err != nil {
				t.Fatal(err)
			}
			restartedStore := &updateTrustStore{Root: &fixture.Key.PublicKey, EmbeddedPolicy: fixture.Policy, CacheDir: fixture.Store.CacheDir}
			restarted := newAutoUpdater(autoUpdaterOptions{
				CurrentVersion: "0.4.11", ExecutablePath: fixture.TargetPath, UpdatesDir: fixture.UpdatesDir,
				ReleaseSources: []updateReleaseSource{fixture.Source}, AssetName: updateAssetName,
				TrustStore: restartedStore, Now: func() time.Time { return testTrustNow },
				InspectAuthenticode: func(string) (inspectedUpdateCertificate, error) {
					return inspectedUpdateCertificate{LegalIdentity: fixture.Identity}, nil
				},
			})
			if restarted.HasPending() || restarted.Status().State == "ready" {
				t.Fatalf("tampered pending survived restart: status=%#v", restarted.Status())
			}
		})
	}
}

type durablePolicyPendingFixture struct {
	Updater    *autoUpdater
	Store      *updateTrustStore
	Key        *ecdsa.PrivateKey
	Policy     []byte
	Rule       updatePublisherRule
	Identity   updateCertificateIdentity
	Source     updateReleaseSource
	UpdatesDir string
	TargetPath string
	SetNow     func(time.Time)
	Launched   func() bool
}

func newDurablePolicyPendingFixture(t testing.TB, expiresAt time.Time) durablePolicyPendingFixture {
	t.Helper()
	binary := []byte("durable policy pending executable")
	digest := sha256.Sum256(binary)
	artifactSHA := hex.EncodeToString(digest[:])
	identity := updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	rule := stableTestRule("naisnet-primary", identity.Organization, identity.OrganizationID)
	rule.ManifestSHA256 = artifactSHA
	key := newTestTrustKey(t)
	policy := signedTestTrustPolicy(t, key, 1, expiresAt, rule)
	root := t.TempDir()
	updatesDir := filepath.Join(root, "updates")
	pendingPath := filepath.Join(updatesDir, "gift-panel-pending.exe")
	targetPath := filepath.Join(root, "gift-panel.exe")
	if err := os.MkdirAll(updatesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	now := testTrustNow
	clock := func() time.Time { return now }
	source := updateReleaseSource{Name: "domestic", URL: "https://updates.example.invalid/release"}
	store := &updateTrustStore{Root: &key.PublicKey, EmbeddedPolicy: policy, CacheDir: filepath.Join(updatesDir, "update-trust"), Now: clock}
	launched := false
	updater := newAutoUpdater(autoUpdaterOptions{
		CurrentVersion: "0.4.11", ExecutablePath: targetPath, UpdatesDir: updatesDir,
		ReleaseSources: []updateReleaseSource{source}, AssetName: updateAssetName,
		TrustStore: store, Now: clock,
		InspectAuthenticode: func(string) (inspectedUpdateCertificate, error) {
			return inspectedUpdateCertificate{LegalIdentity: identity}, nil
		},
		VerifyExecutable: func(string) error { return errors.New("legacy verifier must not run") },
		LaunchInstaller:  func(string, int, bool) error { launched = true; return nil },
	})
	resolved, err := updater.resolveUpdateTrustPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	candidate := updateReleaseCandidate{Source: source, Release: githubRelease{TagName: "v0.4.12"}, Version: "0.4.12", Channel: updateChannelStable}
	verification, err := pendingVerificationForCandidate(candidate, artifactSHA, resolved)
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingUpdate{
		SchemaVersion: pendingUpdateSchemaVersion, Version: "0.4.12", Size: int64(len(binary)), SHA256: artifactSHA,
		PendingPath: pendingPath, TargetPath: targetPath, Verification: verification,
	}
	if err := updater.writePendingMetadata(pending); err != nil {
		t.Fatal(err)
	}
	updater.pending = &pending
	return durablePolicyPendingFixture{
		Updater: updater, Store: store, Key: key, Policy: policy, Rule: rule, Identity: identity, Source: source,
		UpdatesDir: updatesDir, TargetPath: targetPath,
		SetNow: func(value time.Time) { now = value }, Launched: func() bool { return launched },
	}
}

func TestUpdaterLegacyBridgeChannelPolicyAndSignerAreBound(t *testing.T) {
	binary := []byte("exact v0.4.11 RushRush bridge executable")
	digest := sha256.Sum256(binary)
	rule := bridgeTestRule()
	rule.ManifestSHA256 = hex.EncodeToString(digest[:])
	updater, requests, legacyVerifierCalls := newPolicyUpdater(t, policyUpdaterFixture{
		CurrentVersion: "0.4.7",
		Tag:            "v0.4.11",
		ChannelHeaders: []string{string(updateChannelLegacyRushRush)},
		Binary:         binary,
		Certificate: updateCertificateIdentity{
			Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P",
		},
		Rules: []updatePublisherRule{rule},
	})

	err := updater.checkAndDownload(context.Background(), true)
	assertUpdateCode(t, err, "")
	if status := updater.Status(); status.State != "ready" || status.LatestVersion != "0.4.11" {
		t.Fatalf("status = %#v, want ready v0.4.11", status)
	}
	assertPolicyUpdaterUserAgents(t, requests, "bilibili-live-gift-panel/0.4.7")
	if *legacyVerifierCalls != 0 {
		t.Fatalf("legacy verifier calls = %d, want 0 for enrollment path", *legacyVerifierCalls)
	}
}

func TestUpdaterStableChannelPolicyAndSignerAreBound(t *testing.T) {
	binary := []byte("v0.4.12 NaisNet stable executable")
	digest := sha256.Sum256(binary)
	rule := stableTestRule("naisnet-primary", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094")
	rule.ManifestSHA256 = hex.EncodeToString(digest[:])
	updater, requests, legacyVerifierCalls := newPolicyUpdater(t, policyUpdaterFixture{
		CurrentVersion: "0.4.11",
		Tag:            "v0.4.12",
		ChannelHeaders: []string{string(updateChannelStable)},
		Binary:         binary,
		Certificate: updateCertificateIdentity{
			Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094",
		},
		Rules: []updatePublisherRule{rule},
	})

	err := updater.checkAndDownload(context.Background(), true)
	assertUpdateCode(t, err, "")
	if status := updater.Status(); status.State != "ready" || status.LatestVersion != "0.4.12" {
		t.Fatalf("status = %#v, want ready v0.4.12", status)
	}
	assertPolicyUpdaterUserAgents(t, requests, "bilibili-live-gift-panel/0.4.11")
	if *legacyVerifierCalls != 0 {
		t.Fatalf("legacy verifier calls = %d, want 0 for enrollment path", *legacyVerifierCalls)
	}
}

func TestUpdaterPolicyEnrollmentNeverCallsLegacyVerifierAtInstall(t *testing.T) {
	binary := []byte("policy-authorized executable revalidated before install")
	digest := sha256.Sum256(binary)
	rule := stableTestRule("naisnet-primary", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094")
	rule.ManifestSHA256 = hex.EncodeToString(digest[:])
	updater, _, legacyVerifierCalls := newPolicyUpdater(t, policyUpdaterFixture{
		CurrentVersion: "0.4.11", Tag: "v0.4.12", ChannelHeaders: []string{string(updateChannelStable)}, Binary: binary,
		Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"},
		Rules:       []updatePublisherRule{rule},
	})
	updater.launchInstaller = func(string, int, bool) error { return nil }
	assertUpdateCode(t, updater.checkAndDownload(context.Background(), true), "")

	if err := updater.InstallOnExit(false); err != nil {
		t.Fatalf("InstallOnExit rejected policy-authorized pending executable: %v", err)
	}
	if *legacyVerifierCalls != 0 {
		t.Fatalf("legacy verifier calls = %d, want 0 throughout enrollment download and install", *legacyVerifierCalls)
	}
}

func TestUpdaterExpiredPolicyFallbackUsesResolvedSignerIdentity(t *testing.T) {
	identity := updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	policy := resolvedUpdateTrustPolicy{
		Policy: verifiedUpdateTrustPolicy{Epoch: 2, ExpiresAt: testTrustNow.Add(-time.Hour)},
		Mode:   updateTrustModeExpiredIdentityFallback, FrozenIdentities: []updateCertificateIdentity{identity}, resolvedAt: testTrustNow,
	}
	candidate := updateReleaseCandidate{Release: githubRelease{TagName: "v9.9.9"}, Version: "9.9.9", Channel: updateChannelStable}
	err := verifyUpdateArtifactWithInspector("ignored.exe", candidate, strings.Repeat("a", 64), policy, func(string) (inspectedUpdateCertificate, error) {
		return inspectedUpdateCertificate{LegalIdentity: identity}, nil
	})
	assertUpdateCode(t, err, "")
}

func TestUpdaterGitHubFallbackUsesStableChannelOnly(t *testing.T) {
	binary := []byte("GitHub NaisNet stable fallback executable")
	digest := sha256.Sum256(binary)
	rule := stableTestRule("naisnet-primary", "NaisNet Technology Co., Ltd.", "91210103MA7CJ3C094")
	rule.ManifestSHA256 = hex.EncodeToString(digest[:])
	updater, _, _ := newPolicyUpdater(t, policyUpdaterFixture{
		CurrentVersion: "0.4.11",
		Tag:            "v0.4.12",
		ChannelHeaders: []string{string(updateChannelLegacyRushRush)},
		GitHub:         true,
		Binary:         binary,
		Certificate: updateCertificateIdentity{
			Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094",
		},
		Rules: []updatePublisherRule{rule},
	})

	err := updater.checkAndDownload(context.Background(), true)
	assertUpdateCode(t, err, "")
	if status := updater.Status(); status.State != "ready" {
		t.Fatalf("status = %#v, want GitHub stable fallback ready", status)
	}
}

func TestUpdaterRejectsChannelPolicyAndSignerMismatches(t *testing.T) {
	naisNet := updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	rushRush := updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}
	tests := []struct {
		name           string
		currentVersion string
		tag            string
		channelHeaders []string
		certificate    updateCertificateIdentity
		rules          []updatePublisherRule
		invalidPolicy  bool
		wantCode       string
	}{
		{name: "RushRush signer on stable", currentVersion: "0.4.10", tag: "v0.4.11", channelHeaders: []string{string(updateChannelStable)}, certificate: rushRush, rules: []updatePublisherRule{bridgeTestRule()}, wantCode: "publisher_not_authorized"},
		{name: "RushRush signer beyond bridge tag", currentVersion: "0.4.11", tag: "v0.4.12", channelHeaders: []string{string(updateChannelLegacyRushRush)}, certificate: rushRush, rules: []updatePublisherRule{bridgeTestRule()}, wantCode: "publisher_not_authorized"},
		{name: "NaisNet wrong organization ID", currentVersion: "0.4.11", tag: "v0.4.12", channelHeaders: []string{string(updateChannelStable)}, certificate: updateCertificateIdentity{Country: "CN", Organization: naisNet.Organization, OrganizationID: "DIFFERENT"}, rules: []updatePublisherRule{stableTestRule("naisnet-primary", naisNet.Organization, naisNet.OrganizationID)}, wantCode: "publisher_not_authorized"},
		{name: "policy manifest hash mismatch", currentVersion: "0.4.11", tag: "v0.4.12", channelHeaders: []string{string(updateChannelStable)}, certificate: naisNet, rules: []updatePublisherRule{{ID: "naisnet-primary", Role: "primary", Country: "CN", Organization: naisNet.Organization, OrganizationID: naisNet.OrganizationID, AllowedChannel: updateChannelStable, AllowedTags: []string{"v0.4.12"}, ManifestSHA256: strings.Repeat("0", 64)}}, wantCode: "publisher_not_authorized"},
		{name: "missing domestic channel", currentVersion: "0.4.11", tag: "v0.4.12", certificate: naisNet, rules: []updatePublisherRule{stableTestRule("naisnet-primary", naisNet.Organization, naisNet.OrganizationID)}, wantCode: "update_channel_invalid"},
		{name: "duplicate domestic channel", currentVersion: "0.4.11", tag: "v0.4.12", channelHeaders: []string{string(updateChannelStable), string(updateChannelStable)}, certificate: naisNet, rules: []updatePublisherRule{stableTestRule("naisnet-primary", naisNet.Organization, naisNet.OrganizationID)}, wantCode: "update_channel_invalid"},
		{name: "unknown domestic channel", currentVersion: "0.4.11", tag: "v0.4.12", channelHeaders: []string{"beta"}, certificate: naisNet, rules: []updatePublisherRule{stableTestRule("naisnet-primary", naisNet.Organization, naisNet.OrganizationID)}, wantCode: "update_channel_invalid"},
		{name: "invalid signed policy", currentVersion: "0.4.11", tag: "v0.4.12", channelHeaders: []string{string(updateChannelStable)}, certificate: naisNet, rules: []updatePublisherRule{stableTestRule("naisnet-primary", naisNet.Organization, naisNet.OrganizationID)}, invalidPolicy: true, wantCode: "policy_embedded_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updater, _, _ := newPolicyUpdater(t, policyUpdaterFixture{
				CurrentVersion: test.currentVersion,
				Tag:            test.tag, ChannelHeaders: test.channelHeaders,
				Binary: []byte("candidate bytes for " + test.name), Certificate: test.certificate,
				Rules: test.rules, InvalidPolicy: test.invalidPolicy,
			})
			err := updater.checkAndDownload(context.Background(), true)
			assertUpdateCode(t, err, test.wantCode)
			if status := updater.Status(); status.State != "error" {
				t.Fatalf("status = %#v, want error", status)
			}
		})
	}
}

func TestUpdaterBridgeFallbackKeepsEachCandidateChannelAndPolicyBinding(t *testing.T) {
	binary := []byte("same v0.4.11 RushRush bridge from independent sources")
	digest := sha256.Sum256(binary)
	rule := bridgeTestRule()
	rule.ManifestSHA256 = hex.EncodeToString(digest[:])
	key := newTestTrustKey(t)
	policy := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), rule)
	assetRequests := make(map[string]int)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/first-release":
			response.Header().Set("X-Gift-Panel-Update-Channel", string(updateChannelStable))
			_ = json.NewEncoder(response).Encode(policyTestRelease("v0.4.11", server.URL+"/first-asset", server.URL+"/first-checksum", int64(len(binary))))
		case "/second-release":
			response.Header().Set("X-Gift-Panel-Update-Channel", string(updateChannelLegacyRushRush))
			_ = json.NewEncoder(response).Encode(policyTestRelease("v0.4.11", server.URL+"/second-asset", server.URL+"/second-checksum", int64(len(binary))))
		case "/policy":
			_, _ = response.Write(policy)
		case "/first-checksum", "/second-checksum":
			_, _ = response.Write([]byte(hex.EncodeToString(digest[:]) + "  " + updateAssetName + "\n"))
		case "/first-asset", "/second-asset":
			assetRequests[request.URL.Path]++
			_, _ = response.Write(binary)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "0.4.7", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{
			{Name: "wrong-channel", URL: server.URL + "/first-release", DefaultChannel: updateChannelStable},
			{Name: "legacy-bridge", URL: server.URL + "/second-release", DefaultChannel: updateChannelLegacyRushRush},
		},
		TrustStore:   &updateTrustStore{Root: &key.PublicKey, EmbeddedPolicy: policy, CacheDir: filepath.Join(root, "trust-cache"), Client: server.Client()},
		TrustSources: []updateTrustSource{{Name: "policy", URL: server.URL + "/policy"}},
		Now:          func() time.Time { return testTrustNow },
		InspectAuthenticode: func(string) (inspectedUpdateCertificate, error) {
			return inspectedUpdateCertificate{LegalIdentity: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}, nil
		},
		VerifyExecutable: func(string) error { return errors.New("legacy verifier must not run") },
	})

	err := updater.checkAndDownload(context.Background(), true)
	assertUpdateCode(t, err, "")
	if status := updater.Status(); status.State != "ready" || status.LatestVersion != "0.4.11" {
		t.Fatalf("status = %#v, want authorized second bridge source", status)
	}
	if assetRequests["/first-asset"] != 1 || assetRequests["/second-asset"] != 1 {
		t.Fatalf("asset requests = %#v, want first rejected then second authorized", assetRequests)
	}
}

type policyUpdaterFixture struct {
	CurrentVersion string
	Tag            string
	ChannelHeaders []string
	GitHub         bool
	Binary         []byte
	Certificate    updateCertificateIdentity
	Rules          []updatePublisherRule
	InvalidPolicy  bool
}

func newPolicyUpdater(t testing.TB, fixture policyUpdaterFixture) (*autoUpdater, map[string][]string, *int) {
	t.Helper()
	key := newTestTrustKey(t)
	policy := signedTestTrustPolicy(t, key, 1, testTrustNow.AddDate(1, 0, 0), fixture.Rules...)
	if fixture.InvalidPolicy {
		policy = []byte(`{"signed":`)
	}
	digest := sha256.Sum256(fixture.Binary)
	requests := make(map[string][]string)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests[request.URL.Path] = append(requests[request.URL.Path], request.Header.Get("User-Agent"))
		switch request.URL.Path {
		case "/release":
			for _, value := range fixture.ChannelHeaders {
				response.Header().Add("X-Gift-Panel-Update-Channel", value)
			}
			_ = json.NewEncoder(response).Encode(policyTestRelease(fixture.Tag, server.URL+"/asset", server.URL+"/checksum", int64(len(fixture.Binary))))
		case "/policy":
			_, _ = response.Write(policy)
		case "/checksum":
			_, _ = response.Write([]byte(hex.EncodeToString(digest[:]) + "  " + updateAssetName + "\n"))
		case "/asset":
			_, _ = response.Write(fixture.Binary)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	legacyVerifierCalls := 0
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: fixture.CurrentVersion, ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), AssetName: updateAssetName,
		ReleaseSources: []updateReleaseSource{{Name: "candidate", URL: server.URL + "/release", GitHub: fixture.GitHub}},
		TrustStore:     &updateTrustStore{Root: &key.PublicKey, EmbeddedPolicy: policy, CacheDir: filepath.Join(root, "trust-cache"), Client: server.Client()},
		TrustSources:   []updateTrustSource{{Name: "policy", URL: server.URL + "/policy"}},
		Now:            func() time.Time { return testTrustNow },
		InspectAuthenticode: func(string) (inspectedUpdateCertificate, error) {
			return inspectedUpdateCertificate{LegalIdentity: fixture.Certificate}, nil
		},
		VerifyExecutable: func(string) error {
			legacyVerifierCalls++
			return errors.New("legacy verifier must not run")
		},
	})
	return updater, requests, &legacyVerifierCalls
}

func policyTestRelease(tag, assetURL, checksumURL string, size int64) githubRelease {
	return githubRelease{TagName: tag, Assets: []githubAsset{
		{Name: updateAssetName, DownloadURL: assetURL, Size: size},
		{Name: updateAssetName + ".sha256", DownloadURL: checksumURL, Size: 65},
	}}
}

func assertPolicyUpdaterUserAgents(t testing.TB, requests map[string][]string, want string) {
	t.Helper()
	for _, path := range []string{"/release", "/policy", "/checksum", "/asset"} {
		values := requests[path]
		if len(values) == 0 {
			t.Fatalf("%s requests = 0, want at least one", path)
		}
		for _, value := range values {
			if value != want {
				t.Fatalf("%s User-Agent = %q, want %q", path, value, want)
			}
		}
	}
}

func assertUpdateCode(t testing.TB, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("update error = %v, want nil", err)
		}
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("update error = %v, want code %q", err, want)
	}
}
