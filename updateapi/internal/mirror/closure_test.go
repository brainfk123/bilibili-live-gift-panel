package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestValidateLocalReleaseClosureRunsByTagMirrorValidators(t *testing.T) {
	root, metadata := localClosureFixture(t)
	if err := ValidateLocalReleaseClosure(metadata, root, "v0.4.11"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLocalReleaseClosureRejectsMissingOrInconsistentTask7Assets(t *testing.T) {
	for _, name := range []string{AssetChecksum, AssetManifest, AssetChangelog} {
		t.Run(name, func(t *testing.T) {
			root, metadata := localClosureFixture(t)
			if err := os.Remove(filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			if err := ValidateLocalReleaseClosure(metadata, root, "v0.4.11"); err == nil {
				t.Fatal("incomplete closure accepted")
			}
		})
	}
	root, metadata := localClosureFixture(t)
	if err := os.WriteFile(filepath.Join(root, AssetManifest), []byte("{\"invalid\":\"closure\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLocalReleaseClosure(metadata, root, "v0.4.11"); err == nil {
		t.Fatal("invalid fallback manifest accepted")
	}
}

func localClosureFixture(t testing.TB) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	executable := []byte("rushrush-bridge-executable")
	digest := sha256.Sum256(executable)
	hexDigest := hex.EncodeToString(digest[:])
	checksum := []byte(hexDigest + "  " + AssetExecutable)
	manifest := []byte("{\"tag_name\":\"v0.4.11\",\"draft\":false,\"prerelease\":false,\"assets\":[{\"name\":\"gift-panel-windows-x64.exe\",\"browser_download_url\":\"https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v0.4.11/gift-panel-windows-x64.exe\",\"size\":" + integer(len(executable)) + ",\"digest\":\"sha256:" + hexDigest + "\"}]}")
	changelog := []byte("{\"schemaVersion\":1,\"releases\":[{\"version\":\"0.4.11\"}]}")
	assets := map[string][]byte{AssetExecutable: executable, AssetChecksum: checksum, AssetManifest: manifest, AssetChangelog: changelog}
	metadataAssets := make([]map[string]any, 0, len(assets))
	ids := map[string]int{AssetExecutable: 1, AssetChecksum: 2, AssetManifest: 3, AssetChangelog: 4}
	for _, name := range []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog} {
		body := assets[name]
		if err := os.WriteFile(filepath.Join(root, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(body)
		metadataAssets = append(metadataAssets, map[string]any{
			"url":                  "https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/" + integer(ids[name]),
			"name":                 name,
			"size":                 len(body),
			"digest":               "sha256:" + hex.EncodeToString(hash[:]),
			"browser_download_url": "https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v0.4.11/" + name,
		})
	}
	metadata, err := json.Marshal(map[string]any{"tag_name": "v0.4.11", "draft": false, "prerelease": false, "published_at": "2026-09-01T00:00:00Z", "assets": metadataAssets})
	if err != nil {
		t.Fatal(err)
	}
	return root, metadata
}

func integer(value int) string {
	return strconv.Itoa(value)
}
