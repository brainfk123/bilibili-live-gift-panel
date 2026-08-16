package mirror

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testTag         = "v0.4.4"
	testPublishedAt = "2026-08-16T12:00:00Z"
)

func TestGitHubReleaseSourceLatestSendsFixedRequestAndMapsTrustedRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/brainfk123/bilibili-live-gift-panel/releases/latest" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", request.Method)
		}
		if got := request.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Fatalf("Accept = %q", got)
		}
		if got := request.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Fatalf("X-GitHub-Api-Version = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != "bilibili-live-gift-panel-release-mirror/1.0" {
			t.Fatalf("User-Agent = %q", got)
		}
		if got := request.Header.Get("If-None-Match"); got != `"previous"` {
			t.Fatalf("If-None-Match = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}

		writer.Header().Set("ETag", `"current"`)
		writeReleaseJSON(t, writer, validReleaseJSON())
	}))
	defer server.Close()

	result, err := newGitHubReleaseSource(server.Client(), server.URL).Latest(context.Background(), `"previous"`)
	if err != nil {
		t.Fatal(err)
	}
	if result.NotModified {
		t.Fatal("NotModified = true, want false")
	}
	if result.ETag != `"current"` {
		t.Fatalf("ETag = %q", result.ETag)
	}
	if result.Release.Tag != testTag {
		t.Fatalf("Tag = %q", result.Release.Tag)
	}
	if result.Release.PublishedAt.Format("2006-01-02T15:04:05Z") != testPublishedAt {
		t.Fatalf("PublishedAt = %s", result.Release.PublishedAt)
	}
	if len(result.Release.Assets) != 4 {
		t.Fatalf("len(Assets) = %d, want 4", len(result.Release.Assets))
	}
	if result.Release.Assets[AssetExecutable].Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("executable digest = %q", result.Release.Assets[AssetExecutable].Digest)
	}
}

func TestGitHubReleaseSourceLatest304ReturnsNoMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"untrusted"`)
		writer.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	result, err := newGitHubReleaseSource(server.Client(), server.URL).Latest(context.Background(), `"previous"`)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NotModified {
		t.Fatal("NotModified = false, want true")
	}
	if result.ETag != "" || result.Release.Tag != "" || !result.Release.PublishedAt.IsZero() || result.Release.Assets != nil {
		t.Fatalf("304 result contains untrusted metadata: %#v", result)
	}
}

func TestGitHubReleaseSourceLatestRejectsUntrustedRelease(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "draft", mutate: func(release map[string]any) { release["draft"] = true }},
		{name: "prerelease", mutate: func(release map[string]any) { release["prerelease"] = true }},
		{name: "noncanonical tag", mutate: func(release map[string]any) { release["tag_name"] = "v0.04.4" }},
		{name: "invalid publication time", mutate: func(release map[string]any) { release["published_at"] = "tomorrow" }},
		{name: "missing required asset", mutate: func(release map[string]any) { release["assets"] = validAssets()[:3] }},
		{name: "duplicate required asset", mutate: func(release map[string]any) { release["assets"] = append(validAssets(), validAssets()[0]) }},
		{name: "zero declared size", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "size", int64(0))
		}},
		{name: "negative declared size", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "size", int64(-1))
		}},
		{name: "oversize executable", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "size", int64(maxExecutableBytes+1))
		}},
		{name: "invalid digest algorithm", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "digest", "md5:deadbeef")
		}},
		{name: "short digest", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "digest", "sha256:abc")
		}},
		{name: "non hexadecimal digest", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "digest", "sha256:"+strings.Repeat("g", 64))
		}},
		{name: "uppercase hexadecimal digest", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "digest", "sha256:"+strings.Repeat("A", 64))
		}},
		{name: "non HTTPS asset URL", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "browser_download_url", "http://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v0.4.4/gift-panel-windows-x64.exe")
		}},
		{name: "wrong repository asset URL", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "browser_download_url", "https://github.com/other/repository/releases/download/v0.4.4/gift-panel-windows-x64.exe")
		}},
		{name: "wrong tag asset URL", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "browser_download_url", "https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v0.4.3/gift-panel-windows-x64.exe")
		}},
		{name: "wrong name asset URL", mutate: func(release map[string]any) {
			release["assets"] = mutateAsset(validAssets(), AssetExecutable, "browser_download_url", "https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v0.4.4/other.exe")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				release := validReleaseJSON()
				test.mutate(release)
				writer.Header().Set("ETag", `"current"`)
				writeReleaseJSON(t, writer, release)
			}))
			defer server.Close()

			if _, err := newGitHubReleaseSource(server.Client(), server.URL).Latest(context.Background(), ""); err == nil {
				t.Fatal("Latest() error = nil, want rejection")
			}
		})
	}
}

func TestGitHubReleaseSourceLatestRejectsDuplicateSecurityFields(t *testing.T) {
	valid := marshalReleaseJSON(t, validReleaseJSON())
	executableURL := `"browser_download_url":"https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v0.4.4/gift-panel-windows-x64.exe"`

	tests := []struct {
		name string
		body string
	}{
		{
			name: "duplicate draft",
			body: strings.Replace(valid, `"draft":false`, `"draft":false,"draft":false`, 1),
		},
		{
			name: "duplicate tag name",
			body: strings.Replace(valid, `"tag_name":"v0.4.4"`, `"tag_name":"v0.4.4","tag_name":"v0.4.4"`, 1),
		},
		{
			name: "duplicate assets",
			body: strings.Replace(valid, `"assets":`, `"assets":[],"assets":`, 1),
		},
		{
			name: "duplicate asset download URL",
			body: strings.Replace(valid, executableURL, executableURL+","+executableURL, 1),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("ETag", `"current"`)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			if _, err := newGitHubReleaseSource(server.Client(), server.URL).Latest(context.Background(), ""); err == nil {
				t.Fatal("Latest() error = nil, want duplicate-field rejection")
			}
		})
	}
}

func TestGitHubReleaseSourceLatestRejectsMissingETagAndMalformedJSONWithoutLeakingResponse(t *testing.T) {
	tests := []struct {
		name   string
		etag   string
		body   string
		secret string
	}{
		{name: "missing ETag", body: `{"tag_name":"v0.4.4"}`},
		{name: "malformed JSON", etag: `"current"`, body: `{"tag_name":`, secret: "private.example.invalid/asset?token=secret"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if test.etag != "" {
					writer.Header().Set("ETag", test.etag)
				}
				_, _ = writer.Write([]byte(test.body + test.secret))
			}))
			defer server.Close()

			_, err := newGitHubReleaseSource(server.Client(), server.URL).Latest(context.Background(), "")
			if err == nil {
				t.Fatal("Latest() error = nil, want rejection")
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("error leaked response value: %v", err)
			}
		})
	}
}

func validReleaseJSON() map[string]any {
	return map[string]any{
		"tag_name":     testTag,
		"draft":        false,
		"prerelease":   false,
		"published_at": testPublishedAt,
		"assets":       validAssets(),
	}
}

func validAssets() []map[string]any {
	return []map[string]any{
		assetJSON(AssetExecutable, 12_345_678, "sha256:"+strings.Repeat("a", 64)),
		assetJSON(AssetChecksum, 65, ""),
		assetJSON(AssetManifest, 256, ""),
		assetJSON(AssetChangelog, 512, ""),
	}
}

func assetJSON(name string, size int64, digest string) map[string]any {
	return map[string]any{
		"name":                 name,
		"size":                 size,
		"digest":               digest,
		"browser_download_url": "https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/" + testTag + "/" + name,
	}
}

func mutateAsset(assets []map[string]any, name, field string, value any) []map[string]any {
	cloned := make([]map[string]any, len(assets))
	for index, asset := range assets {
		cloned[index] = make(map[string]any, len(asset))
		for key, original := range asset {
			cloned[index][key] = original
		}
		if asset["name"] == name {
			cloned[index][field] = value
		}
	}
	return cloned
}

func writeReleaseJSON(t *testing.T, writer http.ResponseWriter, value map[string]any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func marshalReleaseJSON(t *testing.T, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
