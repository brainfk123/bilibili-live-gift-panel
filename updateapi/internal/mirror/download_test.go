package mirror

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
		rename: func(root *os.Root, oldName, newName string) error {
			events = append(events, "rename")
			return root.Rename(oldName, newName)
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

// Mutation caught: accepting an arbitrary absolute path lets cleanup delete a file the downloader never returned.
func TestDownloaderCleanupRejectsWrongOrOutsidePathWithoutDeletingIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()
	downloader := mustNewConcreteDownloader(t, server.Client(), t.TempDir())
	if _, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), AssetManifest)
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := downloader.CleanupCompleted(context.Background(), outside); err == nil {
		t.Fatal("CleanupCompleted() accepted an outside path")
	}
	assertFileContent(t, outside, "outside")
}

// Mutation caught: deleting only by root-relative name removes a replacement file that is not the completed artifact returned by Download.
func TestDownloaderCleanupRejectsReplacedCompletedArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("original"))
	}))
	defer server.Close()
	stateDir := t.TempDir()
	downloader := mustNewConcreteDownloader(t, server.Client(), stateDir)
	completed, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: int64(len("original")), MaxBytes: maxManifestBytes})
	if err != nil {
		t.Fatal(err)
	}
	retainedOriginal := completed + ".retained"
	if err := os.Rename(completed, retainedOriginal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(completed, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := downloader.CleanupCompleted(context.Background(), completed); err == nil {
		t.Fatal("CleanupCompleted() deleted a replacement path")
	}
	assertFileContent(t, completed, "replacement")
	assertFileContent(t, retainedOriginal, "original")
}

// Mutation caught: discarding a root-relative removal failure reports cleanup success while the completed artifact remains.
func TestDownloaderCleanupReturnsRemovalFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()
	stateDir := t.TempDir()
	downloader, err := newDownloaderWithOptions(server.Client(), stateDir, downloaderOptions{
		overallTimeout: time.Second,
		maxAttempts:    1,
		backoff:        func(context.Context, int) error { return nil },
		syncFile:       func(file *os.File) error { return file.Sync() },
		rename:         replaceDownloadFile,
		removeCompleted: func(*os.Root, string) error {
			return errors.New("injected removal failure")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes})
	if err != nil {
		t.Fatal(err)
	}

	if err := downloader.CleanupCompleted(context.Background(), completed); err == nil {
		t.Fatal("CleanupCompleted() error = nil, want removal failure")
	}
	assertFileContent(t, completed, "x")
}

func TestResumeUsesRangeAndIfRangeBoundToExactMetadata(t *testing.T) {
	content := "abcdef"
	stateDir := t.TempDir()
	finalPath := filepath.Join(stateDir, AssetExecutable)

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
		Name: AssetExecutable, URL: serverURL, Size: 6, MaxBytes: maxExecutableBytes, Resumable: true,
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
			finalPath := filepath.Join(stateDir, AssetExecutable)
			writePartialState(t, finalPath, "stale", test.metadata(server.URL+"/asset"))
			downloader := mustNewDownloader(t, server.Client(), stateDir)
			path, err := downloader.Download(context.Background(), DownloadSpec{
				Name: AssetExecutable, URL: server.URL + "/asset", Size: 6, MaxBytes: maxExecutableBytes, Resumable: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertFileContent(t, path, content)
		})
	}
}

func TestStrongETagRequiresCompleteEntityTagGrammar(t *testing.T) {
	for _, value := range []string{`""`, `"strong"`, `"back\\slash"`, "\"obs\x80text\""} {
		if !isStrongETag(value) {
			t.Errorf("isStrongETag(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"", `W/"weak"`, `w/"weak"`, "strong", `"a" "b"`, "\"space here\"", "\"tab\there\"", "\"control\x01\"", "\"delete\x7f\"",
	} {
		if isStrongETag(value) {
			t.Errorf("isStrongETag(%q) = true, want false", value)
		}
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
			finalPath := filepath.Join(stateDir, AssetExecutable)
			url := server.URL + "/asset"
			writePartialState(t, finalPath, "abc", fmt.Sprintf(`{"url":%q,"etag":"\"strong\"","size":6}`, url))
			downloader := mustNewDownloader(t, server.Client(), stateDir)
			path, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetExecutable, URL: url, Size: 6, MaxBytes: maxExecutableBytes, Resumable: true})
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

func TestResumeRejectsPartSwappedAfterOffsetValidation(t *testing.T) {
	stateDir := t.TempDir()
	finalPath := filepath.Join(stateDir, AssetExecutable)
	partPath := finalPath + ".part"
	var swapped atomic.Bool
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") != "bytes=3-" {
			t.Errorf("Range = %q, want bytes=3-", request.Header.Get("Range"))
		}
		if err := os.Remove(partPath); err == nil {
			if err := os.WriteFile(partPath, []byte("XYZ"), 0o600); err != nil {
				t.Errorf("replace validated part: %v", err)
			} else {
				swapped.Store(true)
			}
		}
		writer.Header().Set("ETag", `"strong"`)
		writer.Header().Set("Content-Range", "bytes 3-5/6")
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write([]byte("def"))
	}))
	defer server.Close()
	serverURL = server.URL + "/asset"
	writePartialState(t, finalPath, "abc", fmt.Sprintf(`{"url":%q,"etag":"\"strong\"","size":6}`, serverURL))

	_, err := mustNewDownloader(t, server.Client(), stateDir).Download(context.Background(), DownloadSpec{
		Name: AssetExecutable, URL: serverURL, Size: 6, MaxBytes: maxExecutableBytes, Resumable: true,
	})
	if swapped.Load() {
		if err == nil {
			t.Fatal("Download() accepted a partial file swapped after offset validation")
		}
		assertPathMissing(t, finalPath)
		return
	}
	if err != nil {
		t.Fatalf("Download() failed after the open handle prevented swapping: %v", err)
	}
	assertFileContent(t, finalPath, "abcdef")
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
		finalPath := filepath.Join(stateDir, AssetExecutable)
		_, err := mustNewDownloader(t, server.Client(), stateDir).Download(context.Background(), DownloadSpec{Name: AssetExecutable, URL: server.URL + "/asset?token=secret", Size: 6, MaxBytes: maxExecutableBytes, Resumable: true})
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
		rename:         replaceDownloadFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Download() error = %v, want deadline exceeded", err)
	}
}

func TestDownloaderSyncsRetryablePartialBeforeRetainingResumeState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"strong"`)
		writer.Header().Set("Content-Length", "6")
		_, _ = writer.Write([]byte("a"))
	}))
	defer server.Close()

	stateDir := t.TempDir()
	syncCalls := 0
	downloader, err := newDownloaderWithOptions(server.Client(), stateDir, downloaderOptions{
		overallTimeout: time.Second,
		maxAttempts:    1,
		backoff:        func(context.Context, int) error { return nil },
		syncFile: func(file *os.File) error {
			syncCalls++
			info, statErr := file.Stat()
			if statErr != nil {
				return statErr
			}
			if info.Size() != 1 {
				return fmt.Errorf("partial size at sync = %d, want 1", info.Size())
			}
			return file.Sync()
		},
		rename: replaceDownloadFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Download(context.Background(), DownloadSpec{
		Name: AssetExecutable, URL: server.URL, Size: 6, MaxBytes: maxExecutableBytes, Resumable: true,
	})
	if err == nil {
		t.Fatal("Download() error = nil, want premature EOF")
	}
	if syncCalls != 1 {
		t.Fatalf("partial sync calls = %d, want 1", syncCalls)
	}
	if _, err := os.Stat(filepath.Join(stateDir, AssetExecutable) + ".part"); err != nil {
		t.Fatalf("durable partial missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, AssetExecutable) + ".part.meta"); err != nil {
		t.Fatalf("durable resume metadata missing: %v", err)
	}
}

func TestDownloaderRetainsRetryablePartialOnlyForExecutable(t *testing.T) {
	// Mutation caught: deriving retention only from a strong server ETag leaves validation artifacts resumable too.
	for _, name := range []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("ETag", `"strong"`)
				writer.Header().Set("Content-Length", "6")
				_, _ = writer.Write([]byte("a"))
			}))
			defer server.Close()
			stateDir := t.TempDir()
			downloader, err := newDownloaderWithOptions(server.Client(), stateDir, downloaderOptions{
				overallTimeout: time.Second,
				maxAttempts:    1,
				backoff:        func(context.Context, int) error { return nil },
				syncFile:       func(file *os.File) error { return file.Sync() },
				rename:         replaceDownloadFile,
			})
			if err != nil {
				t.Fatal(err)
			}
			limit, _ := assetLimit(name)
			_, err = downloader.Download(context.Background(), DownloadSpec{
				Name: name, URL: server.URL, Size: 6, MaxBytes: limit, Resumable: name == AssetExecutable,
			})
			if err == nil {
				t.Fatal("Download() error = nil, want premature EOF")
			}
			partPath := filepath.Join(stateDir, name) + ".part"
			metadataPath := partPath + ".meta"
			if name == AssetExecutable {
				if _, statErr := os.Stat(partPath); statErr != nil {
					t.Fatalf("EXE partial missing: %v", statErr)
				}
				if _, statErr := os.Stat(metadataPath); statErr != nil {
					t.Fatalf("EXE resume metadata missing: %v", statErr)
				}
				return
			}
			assertPathMissing(t, partPath)
			assertPathMissing(t, metadataPath)
		})
	}
}

func TestDownloaderRejectsPartSwappedBeforeRename(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"strong"`)
		_, _ = writer.Write([]byte("abcdef"))
	}))
	defer server.Close()

	stateDir := t.TempDir()
	partPath := filepath.Join(stateDir, AssetManifest) + ".part"
	swapped := false
	downloader, err := newDownloaderWithOptions(server.Client(), stateDir, downloaderOptions{
		overallTimeout: time.Second,
		maxAttempts:    1,
		backoff:        func(context.Context, int) error { return nil },
		syncFile:       func(file *os.File) error { return file.Sync() },
		beforeRename: func() {
			if err := os.Remove(partPath); err != nil {
				t.Errorf("remove completed part: %v", err)
				return
			}
			if err := os.WriteFile(partPath, []byte("UVWXYZ"), 0o600); err != nil {
				t.Errorf("replace completed part: %v", err)
				return
			}
			swapped = true
		},
		rename: replaceDownloadFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Download(context.Background(), DownloadSpec{
		Name: AssetManifest, URL: server.URL, Size: 6, MaxBytes: maxManifestBytes,
	})
	if !swapped {
		t.Fatal("test did not replace the completed part")
	}
	if err == nil {
		t.Fatal("Download() accepted a completed part swapped before rename")
	}
	assertPathMissing(t, filepath.Join(stateDir, AssetManifest))
}

func TestDownloaderRejectsSourceSwappedInsideRenameBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("abcdef"))
	}))
	defer server.Close()

	stateDir := t.TempDir()
	downloader, err := newDownloaderWithOptions(server.Client(), stateDir, downloaderOptions{
		overallTimeout: time.Second,
		maxAttempts:    1,
		backoff:        func(context.Context, int) error { return nil },
		syncFile:       func(file *os.File) error { return file.Sync() },
		rename: func(root *os.Root, oldName, newName string) error {
			if err := root.Remove(oldName); err != nil {
				return err
			}
			if err := root.WriteFile(oldName, []byte("UVWXYZ"), 0o600); err != nil {
				return err
			}
			return root.Rename(oldName, newName)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Download(context.Background(), DownloadSpec{
		Name: AssetManifest, URL: server.URL, Size: 6, MaxBytes: maxManifestBytes,
	})
	if err == nil {
		t.Fatal("Download() accepted a source swapped inside the rename boundary")
	}
	assertPathMissing(t, filepath.Join(stateDir, AssetManifest))
}

func TestDownloaderRejectsStateDirectoryReplacementDuringRequest(t *testing.T) {
	parent := t.TempDir()
	stateDir := filepath.Join(parent, "state")
	movedDir := filepath.Join(parent, "original-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var replaced atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := os.Rename(stateDir, movedDir); err != nil {
			_, _ = writer.Write([]byte("x"))
			return
		}
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Errorf("create replacement state directory: %v", err)
			return
		}
		replaced.Store(true)
		writer.Header().Set("ETag", `"strong"`)
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()

	downloader := mustNewDownloader(t, server.Client(), stateDir)
	_, err := downloader.Download(context.Background(), DownloadSpec{
		Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes,
	})
	if replaced.Load() {
		if err == nil {
			t.Fatal("Download() accepted a replaced state directory")
		}
		assertPathMissing(t, filepath.Join(stateDir, AssetManifest))
		assertPathMissing(t, filepath.Join(movedDir, AssetManifest))
		return
	}
	if err != nil {
		t.Fatalf("Download() failed after the open root prevented replacement: %v", err)
	}
	assertFileContent(t, filepath.Join(stateDir, AssetManifest), "x")
}

func TestDownloaderRejectsUnsafeStatePaths(t *testing.T) {
	if _, err := NewDownloader("relative"); err == nil {
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

func TestNewDownloaderAlwaysUsesRestrictedNetworkBoundary(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested <- struct{}{}
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()

	fetcher, err := NewDownloader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = fetcher.Download(context.Background(), DownloadSpec{
		Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes,
	})
	if err == nil {
		t.Fatal("production downloader accepted an unrestricted HTTP URL")
	}
	select {
	case <-requested:
		t.Fatal("production downloader reached the unrestricted server")
	default:
	}
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

func TestDownloaderVerifiesCandidateBeforeDestructiveOpenMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()

	stateDir := t.TempDir()
	const sentinelName = "sentinel"
	sentinelPath := filepath.Join(stateDir, sentinelName)
	const sentinelContent = "do not truncate or append"
	if err := os.WriteFile(sentinelPath, []byte(sentinelContent), 0o600); err != nil {
		t.Fatal(err)
	}
	var openedFlags []int
	downloader, err := newDownloaderWithOptions(server.Client(), stateDir, downloaderOptions{
		overallTimeout: time.Second,
		maxAttempts:    1,
		backoff:        func(context.Context, int) error { return nil },
		syncFile:       func(file *os.File) error { return file.Sync() },
		openFile: func(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
			if name == AssetManifest+".part" {
				openedFlags = append(openedFlags, flag)
				return root.OpenFile(sentinelName, flag, perm)
			}
			return openDownloadFile(root, name, flag, perm)
		},
		rename: replaceDownloadFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = downloader.Download(context.Background(), DownloadSpec{
		Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes,
	})
	if err == nil {
		t.Fatal("Download() accepted an opener that returned a different file")
	}
	if len(openedFlags) == 0 {
		t.Fatal("test opener was not called for the fresh partial")
	}
	for _, flag := range openedFlags {
		if flag&(os.O_TRUNC|os.O_APPEND) != 0 {
			t.Fatalf("candidate opened with destructive flags %#x", flag)
		}
		if flag&os.O_CREATE != 0 && flag&os.O_EXCL == 0 {
			t.Fatalf("candidate creation can follow an existing symlink: flags %#x", flag)
		}
	}
	assertFileContent(t, sentinelPath, sentinelContent)
}

func TestDownloaderRejectsInRootSymlinkPartWithoutMutatingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ordinary Windows symlink creation requires unavailable privilege")
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()

	stateDir := t.TempDir()
	const sentinelName = "sentinel"
	sentinelPath := filepath.Join(stateDir, sentinelName)
	const sentinelContent = "do not truncate"
	if err := os.WriteFile(sentinelPath, []byte(sentinelContent), 0o600); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(stateDir, AssetManifest) + ".part"
	if err := os.Symlink(sentinelName, partPath); err != nil {
		t.Fatal(err)
	}

	_, err := mustNewDownloader(t, server.Client(), stateDir).Download(context.Background(), DownloadSpec{
		Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes,
	})
	if err == nil {
		t.Fatal("Download() accepted an in-root symlink partial")
	}
	assertFileContent(t, sentinelPath, sentinelContent)
}

func TestDownloaderRejectsWindowsJunctionStateDirectory(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junction evidence")
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	junction := filepath.Join(parent, "junction")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create nonprivileged junction: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if err := os.Remove(junction); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove junction: %v", err)
		}
	})

	if _, err := NewDownloader(junction); err == nil {
		t.Fatal("production downloader accepted a junction state directory")
	}
}

func mustNewDownloader(t *testing.T, client *http.Client, stateDir string) ArtifactFetcher {
	t.Helper()
	downloader, err := newDownloaderWithOptions(client, stateDir, downloaderOptions{
		overallTimeout: defaultDownloadTimeout,
		maxAttempts:    defaultMaxAttempts,
		backoff:        defaultDownloadBackoff,
		syncFile:       func(file *os.File) error { return file.Sync() },
		rename:         replaceDownloadFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	return downloader
}

func mustNewConcreteDownloader(t *testing.T, client *http.Client, stateDir string) *downloader {
	t.Helper()
	downloader, err := newDownloaderWithOptions(client, stateDir, downloaderOptions{
		overallTimeout: defaultDownloadTimeout,
		maxAttempts:    defaultMaxAttempts,
		backoff:        defaultDownloadBackoff,
		syncFile:       func(file *os.File) error { return file.Sync() },
		rename:         replaceDownloadFile,
	})
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

func TestDownloaderObservesCancellationWhileQueued(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()

	downloader := mustNewDownloader(t, server.Client(), t.TempDir())
	firstDone := make(chan error, 1)
	go func() {
		_, err := downloader.Download(context.Background(), DownloadSpec{Name: AssetManifest, URL: server.URL, Size: 1, MaxBytes: maxManifestBytes})
		firstDone <- err
	}()
	<-started

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := downloader.Download(canceled, DownloadSpec{Name: AssetChecksum, URL: server.URL, Size: 1, MaxBytes: maxChecksumBytes})
		secondDone <- err
	}()

	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			close(release)
			<-firstDone
			t.Fatalf("queued Download() error = %v, want context canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		close(release)
		<-firstDone
		t.Fatal("queued Download() did not observe canceled context promptly")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Download(): %v", err)
	}
}
