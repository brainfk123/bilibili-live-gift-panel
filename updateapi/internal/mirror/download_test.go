package mirror

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDownloaderCompletesBoundedDownloadAndSyncsBeforeRename(t *testing.T) {
	content := "checksum"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"release-1"`)
		writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = writer.Write([]byte(content))
	}))
	defer server.Close()

	stateDir := t.TempDir()
	var events []string
	downloader, err := newDownloaderWithOptions(server.Client(), stateDir, downloaderOptions{
		overallTimeout: time.Second,
		maxAttempts:    3,
		backoff:        func(context.Context, int) error { return nil },
		syncFile: func(file *os.File) error {
			events = append(events, "sync")
			return file.Sync()
		},
		rename: func(oldPath, newPath string) error {
			events = append(events, "rename")
			return os.Rename(oldPath, newPath)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	path, err := downloader.Download(context.Background(), DownloadSpec{
		Name: AssetChecksum, URL: server.URL + "/asset?token=do-not-log", Size: int64(len(content)), MaxBytes: maxChecksumBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(stateDir, AssetChecksum) {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("content = %q", data)
	}
	if strings.Join(events, ",") != "sync,rename" {
		t.Fatalf("events = %v, want sync before rename", events)
	}
	assertPathMissing(t, path+".part")
	assertPathMissing(t, path+".part.meta")
}

func TestResumeUsesRangeAndIfRangeBoundToExactMetadata(t *testing.T) {
	content := "abcdef"
	stateDir := t.TempDir()
	finalPath := filepath.Join(stateDir, AssetManifest)

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") != "bytes=3-" {
			t.Errorf("Range = %q", request.Header.Get("Range"))
		}
		if request.Header.Get("If-Range") != `"strong"` {
			t.Errorf("If-Range = %q", request.Header.Get("If-Range"))
		}
		writer.Header().Set("ETag", `"strong"`)
		writer.Header().Set("Content-Range", "bytes 3-5/6")
		writer.Header().Set("Content-Length", "3")
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write([]byte(content[3:]))
	}))
	defer server.Close()
	serverURL = server.URL + "/asset?signature=secret"
	writePartialState(t, finalPath, "abc", fmt.Sprintf(`{"url":%q,"etag":"\"strong\"","size":6}`, serverURL))

	downloader := mustNewDownloader(t, server.Client(), stateDir)
	path, err := downloader.Download(context.Background(), DownloadSpec{
		Name: AssetManifest, URL: serverURL, Size: 6, MaxBytes: maxManifestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, content)
}

func TestResumeDiscardsUntrustworthyMetadataBeforeRequest(t *testing.T) {
	tests := []struct {
		name     string
		metadata func(string) string
	}{
		{name: "URL mismatch", metadata: func(string) string { return `{"url":"https://wrong.invalid/a","etag":"\"strong\"","size":6}` }},
		{name: "declared size mismatch", metadata: func(url string) string { return fmt.Sprintf(`{"url":%q,"etag":"\"strong\"","size":7}`, url) }},
		{name: "weak ETag", metadata: func(url string) string { return fmt.Sprintf(`{"url":%q,"etag":"W/\"weak\"","size":6}`, url) }},
		{name: "missing ETag", metadata: func(url string) string { return fmt.Sprintf(`{"url":%q,"etag":"","size":6}`, url) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := "abcdef"
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Range") != "" || request.Header.Get("If-Range") != "" {
					t.Errorf("untrustworthy state sent range headers: %v", request.Header)
				}
				writer.Header().Set("ETag", `"fresh"`)
				_, _ = writer.Write([]byte(content))
			}))
			defer server.Close()

			stateDir := t.TempDir()
			finalPath := filepath.Join(stateDir, AssetManifest)
			writePartialState(t, finalPath, "stale", test.metadata(server.URL+"/asset"))
			downloader := mustNewDownloader(t, server.Client(), stateDir)
			path, err := downloader.Download(context.Background(), DownloadSpec{
				Name: AssetManifest, URL: server.URL + "/asset", Size: 6, MaxBytes: maxManifestBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertFileContent(t, path, content)
		})
	}
}

func TestResumeRestartsWhenServerIgnoresRangeOrReturnsMalformedRange(t *testing.T) {
	for _, response := range []string{"ignored", "malformed"} {
		t.Run(response, func(t *testing.T) {
			content := "abcdef"
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				writer.Header().Set("ETag", `"strong"`)
				if requests == 1 && response == "malformed" {
					writer.Header().Set("Content-Range", "bytes 2-5/6")
					writer.WriteHeader(http.StatusPartialContent)
					_, _ = writer.Write([]byte("def"))
					return
				}
				_, _ = writer.Write([]byte(content))
			}))
			defer server.Close()

			stateDir := t.TempDir()
			finalPath := filepath.Join(stateDir, AssetManifest)
			url := server.URL + "/asset"
			writePartialState(t, finalPath, "abc", fmt.Sprintf(`{"url":%q,"etag":"\"strong\"","size":6}`, url))
			downloader := mustNewDownloader(t, server.Client(), stateDir)
			path, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: url, Size: 6, MaxBytes: maxManifestBytes})
			if err != nil {
				t.Fatal(err)
			}
			assertFileContent(t, path, content)
			wantRequests := 1
			if response == "malformed" {
				wantRequests = 2
			}
			if requests != wantRequests {
				t.Fatalf("requests = %d, want %d", requests, wantRequests)
			}
		})
	}
}

func TestDownloaderRejectsUnsafeResponsesAndBoundsRetries(t *testing.T) {
	t.Run("416", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requests++
			writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		}))
		defer server.Close()
		stateDir := t.TempDir()
		url := server.URL + "/asset"
		finalPath := filepath.Join(stateDir, AssetManifest)
		writePartialState(t, finalPath, "abc", fmt.Sprintf(`{"url":%q,"etag":"\"strong\"","size":6}`, url))
		_, err := mustNewDownloader(t, server.Client(), stateDir).Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: url, Size: 6, MaxBytes: maxManifestBytes})
		if err == nil || requests != 1 {
			t.Fatalf("error = %v, requests = %d; want one rejected request", err, requests)
		}
	})

	t.Run("premature EOF reaches attempt limit and preserves resumable state", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requests++
			writer.Header().Set("ETag", `"strong"`)
			writer.Header().Set("Content-Length", "6")
			_, _ = writer.Write([]byte("a"))
		}))
		defer server.Close()
		stateDir := t.TempDir()
		finalPath := filepath.Join(stateDir, AssetManifest)
		_, err := mustNewDownloader(t, server.Client(), stateDir).Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL + "/asset?token=secret", Size: 6, MaxBytes: maxManifestBytes})
		if err == nil || requests != 3 {
			t.Fatalf("error = %v, requests = %d; want three failed attempts", err, requests)
		}
		if strings.Contains(err.Error(), "token=secret") {
			t.Fatalf("error leaked URL query: %v", err)
		}
		if info, statErr := os.Stat(finalPath + ".part"); statErr != nil || info.Size() <= 0 || info.Size() >= 6 {
			t.Fatalf("partial state is not safely resumable: info=%v err=%v", info, statErr)
		}
		if _, statErr := os.Stat(finalPath + ".part.meta"); statErr != nil {
			t.Fatalf("resume metadata missing: %v", statErr)
		}
	})

	for _, test := range []struct {
		name     string
		declared int64
		limit    int64
		body     string
	}{
		{name: "oversized stream", declared: 6, limit: 6, body: "1234567"},
		{name: "final size mismatch", declared: 6, limit: 10, body: "12345"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			stateDir := t.TempDir()
			_, err := mustNewDownloader(t, server.Client(), stateDir).Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: test.declared, MaxBytes: test.limit})
			if err == nil {
				t.Fatal("Download() error = nil, want rejection")
			}
			assertPathMissing(t, filepath.Join(stateDir, AssetManifest))
		})
	}
}

func TestDownloaderEnforcesOverallDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	downloader, err := newDownloaderWithOptions(server.Client(), t.TempDir(), downloaderOptions{
		overallTimeout: 30 * time.Millisecond,
		maxAttempts:    3,
		backoff:        func(context.Context, int) error { return nil },
		syncFile:       func(file *os.File) error { return file.Sync() },
		rename:         os.Rename,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Download() error = %v, want deadline exceeded", err)
	}
}

func TestDownloaderRejectsUnsafeStatePaths(t *testing.T) {
	if _, err := NewDownloader(http.DefaultClient, "relative"); err == nil {
		t.Fatal("relative state directory accepted")
	}

	stateDir := t.TempDir()
	downloader := mustNewDownloader(t, http.DefaultClient, stateDir)
	for _, name := range []string{"../escape", "nested/file", "unknown.bin"} {
		if _, err := downloader.Download(context.Background(), DownloadSpec{Name: name, URL: "https://github.com/file", Size: 1, MaxBytes: 1}); err == nil {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}

	destination := filepath.Join(stateDir, AssetManifest)
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: "https://github.com/file", Size: 1, MaxBytes: maxManifestBytes}); err == nil {
		t.Fatal("non-regular destination accepted")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, destination); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: "https://github.com/file", Size: 1, MaxBytes: maxManifestBytes}); err == nil {
		t.Fatal("symlink destination accepted")
	}
	assertFileContent(t, outside, "do not replace")
}

func TestDownloaderRejectsSymlinkPartialState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()
	stateDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("do not append"), 0o600); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(stateDir, AssetManifest) + ".part"
	if err := os.Symlink(outside, partPath); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	downloader := mustNewDownloader(t, server.Client(), stateDir)
	if _, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes}); err == nil {
		t.Fatal("symlink partial state accepted")
	}
	assertFileContent(t, outside, "do not append")
}

func mustNewDownloader(t *testing.T, client *http.Client, stateDir string) ArtifactFetcher {
	t.Helper()
	downloader, err := NewDownloader(client, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return downloader
}

func writePartialState(t *testing.T, finalPath, content, metadata string) {
	t.Helper()
	if err := os.WriteFile(finalPath+".part", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath+".part.meta", []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("%s content = %q, want %q", filepath.Base(path), data, expected)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists or cannot be checked: %v", filepath.Base(path), err)
	}
}

func TestDownloaderIsSafeForConcurrentIndependentAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()
	downloader := mustNewDownloader(t, server.Client(), t.TempDir())

	var wait sync.WaitGroup
	for _, name := range []string{AssetChecksum, AssetManifest, AssetChangelog} {
		name := name
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := downloader.Download(context.Background(), DownloadSpec{Name: name, URL: server.URL + "/" + name, Size: 1, MaxBytes: 2}); err != nil {
				t.Errorf("Download(%s): %v", name, err)
			}
		}()
	}
	wait.Wait()
}
