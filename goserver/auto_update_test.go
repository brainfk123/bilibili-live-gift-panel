package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestFindReleaseAssetRejectsInvalidDigest(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("z", 64)
	_, err := findReleaseAsset(githubRelease{Assets: []githubAsset{{
		Name: updateAssetName, Size: 10, Digest: validDigest, DownloadURL: "https://example.com/app.exe",
	}}}, updateAssetName)
	if err == nil {
		t.Fatal("non-hex digest must be rejected")
	}
}

func TestGitCodeReleaseUsesChecksumAssetAndUnknownDownloadSize(t *testing.T) {
	binary := []byte("gitcode mirrored executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])

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
		ReleaseSources: []updateReleaseSource{{Name: "GitCode", URL: server.URL + "/release"}},
	})
	updater.checkAndDownload(context.Background(), true)
	status := updater.Status()
	if status.State != "ready" || status.LatestVersion != "1.1.0" {
		t.Fatalf("status = %#v", status)
	}
	downloaded, err := os.ReadFile(filepath.Join(root, "updates", "gift-panel-pending.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(binary) {
		t.Fatalf("downloaded = %q", downloaded)
	}
}

func TestUpdaterFallsBackToGitHubWhenGitCodeFails(t *testing.T) {
	binary := []byte("fallback executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	gitCodeRequests := 0
	gitHubRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gitcode":
			gitCodeRequests++
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
			{Name: "GitCode", URL: server.URL + "/gitcode"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
	})
	updater.checkAndDownload(context.Background(), true)
	if status := updater.Status(); status.State != "ready" {
		t.Fatalf("status = %#v", status)
	}
	if gitCodeRequests != 1 || gitHubRequests != 1 {
		t.Fatalf("requests: GitCode=%d GitHub=%d", gitCodeRequests, gitHubRequests)
	}
}

func TestUpdaterFallsBackToGitHubWhenGitCodeAssetIsIncomplete(t *testing.T) {
	binary := []byte("fallback after incomplete GitCode asset")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	gitCodeRequests := 0
	gitHubRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gitcode":
			gitCodeRequests++
			_ = json.NewEncoder(w).Encode(githubRelease{
				TagName: "v1.1.0",
				Assets: []githubAsset{{
					Name: updateAssetName, DownloadURL: server.URL + "/gitcode-asset",
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
			{Name: "GitCode", URL: server.URL + "/gitcode"},
			{Name: "GitHub", URL: server.URL + "/github", GitHub: true},
		},
	})
	updater.checkAndDownload(context.Background(), true)
	if status := updater.Status(); status.State != "ready" || status.LatestVersion != "1.1.0" {
		t.Fatalf("status = %#v", status)
	}
	if gitCodeRequests != 1 || gitHubRequests != 1 {
		t.Fatalf("requests: GitCode=%d GitHub=%d", gitCodeRequests, gitHubRequests)
	}
}

func TestUpdaterChecksGitHubWhenGitCodeMirrorIsBehind(t *testing.T) {
	binary := []byte("newer GitHub executable")
	digestBytes := sha256.Sum256(binary)
	digest := hex.EncodeToString(digestBytes[:])
	gitHubRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gitcode":
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
			{Name: "GitCode", URL: server.URL + "/gitcode"},
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
	updater := newAutoUpdater(autoUpdaterOptions{
		Client: server.Client(), CurrentVersion: "1.0.0", ExecutablePath: filepath.Join(root, "gift-panel.exe"),
		UpdatesDir: filepath.Join(root, "updates"), ReleaseURL: server.URL, AssetName: updateAssetName,
	})
	_, err := updater.downloadAsset(context.Background(), "1.1.0", githubAsset{
		Name: updateAssetName, DownloadURL: server.URL, Size: int64(len(binary)), Digest: "sha256:" + strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatal("digest mismatch must fail")
	}
	if _, statErr := os.Stat(filepath.Join(root, "updates", "gift-panel-pending.exe")); !os.IsNotExist(statErr) {
		t.Fatalf("pending file must not survive mismatch: %v", statErr)
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
