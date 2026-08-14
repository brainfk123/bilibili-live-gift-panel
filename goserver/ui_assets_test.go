package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
)

func TestEmbeddedUIAssetManifestClosesAndServesProductionAssets(t *testing.T) {
	verifyEmbeddedUIAssetClosure(t)
}

func verifyEmbeddedUIAssetClosure(t *testing.T) {
	t.Helper()

	pageFS, err := fs.Sub(embeddedFS, "dist")
	if err != nil {
		t.Fatalf("open embedded UI filesystem: %v", err)
	}
	manifestBytes, err := fs.ReadFile(pageFS, "ui-assets.json")
	if err != nil {
		t.Fatalf("read embedded UI asset manifest: %v", err)
	}
	var manifest struct {
		Version int `json:"version"`
		Files   []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode embedded UI asset manifest: %v", err)
	}
	if manifest.Version != 1 {
		t.Fatalf("manifest version = %d, want 1", manifest.Version)
	}
	if len(manifest.Files) == 0 {
		t.Fatal("embedded UI asset manifest is empty")
	}

	manifestPaths := make([]string, 0, len(manifest.Files))
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, asset := range manifest.Files {
		if !fs.ValidPath(asset.Path) || asset.Path == "." || asset.Path == "ui-assets.json" {
			t.Fatalf("manifest asset path is invalid: %q", asset.Path)
		}
		if _, exists := seen[asset.Path]; exists {
			t.Fatalf("manifest asset path is duplicated: %q", asset.Path)
		}
		seen[asset.Path] = struct{}{}
		manifestPaths = append(manifestPaths, asset.Path)
	}
	sort.Strings(manifestPaths)

	var embeddedPaths []string
	if err := fs.WalkDir(pageFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && path != "ui-assets.json" {
			embeddedPaths = append(embeddedPaths, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk embedded UI filesystem: %v", err)
	}
	sort.Strings(embeddedPaths)
	if !reflect.DeepEqual(embeddedPaths, manifestPaths) {
		t.Fatalf("manifest/embedded UI closure mismatch:\nmanifest=%q\nembedded=%q", manifestPaths, embeddedPaths)
	}

	handler := newEmbeddedPageHandler(pageFS)
	for _, asset := range manifest.Files {
		embeddedBytes, err := fs.ReadFile(pageFS, asset.Path)
		if err != nil {
			t.Fatalf("read embedded asset %q: %v", asset.Path, err)
		}
		if int64(len(embeddedBytes)) != asset.Size {
			t.Fatalf("embedded asset %q size = %d, manifest = %d", asset.Path, len(embeddedBytes), asset.Size)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(embeddedBytes)); got != asset.SHA256 {
			t.Fatalf("embedded asset %q SHA-256 = %s, manifest = %s", asset.Path, got, asset.SHA256)
		}

		publicPath := "/" + asset.Path
		if asset.Path == "index.html" {
			publicPath = "/"
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, publicPath, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s for %q status = %d, want 200", publicPath, asset.Path, response.Code)
		}
		responseBytes := response.Body.Bytes()
		if !bytes.Equal(responseBytes, embeddedBytes) {
			t.Fatalf("GET %s for %q response bytes differ from embedded asset", publicPath, asset.Path)
		}
		if int64(len(responseBytes)) != asset.Size {
			t.Fatalf("GET %s for %q size = %d, manifest = %d", publicPath, asset.Path, len(responseBytes), asset.Size)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(responseBytes)); got != asset.SHA256 {
			t.Fatalf("GET %s for %q SHA-256 = %s, manifest = %s", publicPath, asset.Path, got, asset.SHA256)
		}
	}

	t.Logf("verified manifest closure and production handler bytes for %d embedded UI assets", len(manifest.Files))
}
