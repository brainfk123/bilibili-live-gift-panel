package cosstore_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
)

func TestHeadReturnsMetadataAndTypedNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", request.Method)
		}
		if request.URL.Path == "/releases/v0.4.4/missing.exe" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Length", "123")
		writer.Header().Set("X-Cos-Meta-Sha256", strings.Repeat("a", 64))
		writer.Header().Set("ETag", `"metadata-etag"`)
	}))
	defer server.Close()

	store := newStore(t, server, time.Second)
	info, err := store.Head(context.Background(), "releases/v0.4.4/gift-panel-windows-x64.exe")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 123 || info.SHA256 != strings.Repeat("a", 64) || info.ETag != `"metadata-etag"` {
		t.Fatalf("Head() = %+v, want COS metadata", info)
	}
	_, err = store.Head(context.Background(), "releases/v0.4.4/missing.exe")
	if !errors.Is(err, cosstore.ErrNotFound) {
		t.Fatalf("Head() error = %v, want typed not found", err)
	}
}

func TestNewRejectsUnsafeBucketAndRegion(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		region string
	}{
		{name: "bucket path", bucket: "private-release-1250000000/redirect", region: "ap-shanghai"},
		{name: "bucket userinfo", bucket: "private-release-1250000000@evil.example", region: "ap-shanghai"},
		{name: "bucket without app id", bucket: "private-release", region: "ap-shanghai"},
		{name: "uppercase bucket", bucket: "Private-release-1250000000", region: "ap-shanghai"},
		{name: "oversized bucket label", bucket: strings.Repeat("a", 62) + "-1250000000", region: "ap-shanghai"},
		{name: "region path", bucket: "private-release-1250000000", region: "ap-shanghai/redirect"},
		{name: "region userinfo", bucket: "private-release-1250000000", region: "ap-shanghai@evil.example"},
		{name: "uppercase region", bucket: "private-release-1250000000", region: "AP-Shanghai"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := cosstore.New(test.bucket, test.region, "secret-id", "secret-key", nil)
			if err == nil || client != nil {
				t.Fatalf("New(%q, %q) = (%v, %v), want rejection", test.bucket, test.region, client, err)
			}
		})
	}
}

func TestPutWritesDigestMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", request.Method)
		}
		if request.URL.Path != "/channels/stable/latest.json" {
			t.Fatalf("path = %q, want stable channel key", request.URL.Path)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("X-Cos-Meta-Sha256") != strings.Repeat("b", 64) {
			t.Fatalf("x-cos-meta-sha256 = %q, want release digest", request.Header.Get("X-Cos-Meta-Sha256"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"schemaVersion":1}` {
			t.Fatalf("body = %q, want release JSON", body)
		}
		writer.Header().Set("X-Cos-Hash-Crc64ecma", "15068690071664149826")
	}))
	defer server.Close()

	err := newStore(t, server, time.Second).Put(context.Background(), "channels/stable/latest.json", strings.NewReader(`{"schemaVersion":1}`), int64(len(`{"schemaVersion":1}`)), "application/json", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPutImmutableForbidsOverwrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", request.Method)
		}
		if request.Header.Get("X-Cos-Forbid-Overwrite") != "true" {
			t.Fatalf("x-cos-forbid-overwrite = %q, want true", request.Header.Get("X-Cos-Forbid-Overwrite"))
		}
		writer.Header().Set("X-Cos-Hash-Crc64ecma", "15068690071664149826")
	}))
	defer server.Close()

	err := newStore(t, server, time.Second).PutImmutable(context.Background(), "releases/v0.4.4/release.json", strings.NewReader(`{"schemaVersion":1}`), int64(len(`{"schemaVersion":1}`)), "application/json", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPutImmutableMapsCOSConflictToAlreadyExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/xml")
		writer.WriteHeader(http.StatusConflict)
		_, _ = writer.Write([]byte(`<Error><Code>FileAlreadyExists</Code><Message>exists</Message></Error>`))
	}))
	defer server.Close()

	err := newStore(t, server, time.Second).PutImmutable(context.Background(), "releases/v0.4.4/release.json", strings.NewReader(`{"schemaVersion":1}`), int64(len(`{"schemaVersion":1}`)), "application/json", strings.Repeat("b", 64))
	if !errors.Is(err, cosstore.ErrAlreadyExists) {
		t.Fatalf("PutImmutable() error = %v, want typed conflict", err)
	}
}

func TestGetAuthorizesReadAndReturnsETag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", request.Method)
		}
		if request.URL.Path != "/releases/v0.4.4/gift-panel-windows-x64.exe" {
			t.Fatalf("path = %q, want exact object key", request.URL.Path)
		}
		if request.Header.Get("Authorization") == "" {
			t.Fatal("Authorization header is empty")
		}
		writer.Header().Set("ETag", `"upstream-etag"`)
		_, _ = writer.Write([]byte("release body"))
	}))
	defer server.Close()

	store := newStore(t, server, 50*time.Millisecond)
	body, etag, err := store.Get(context.Background(), "releases/v0.4.4/gift-panel-windows-x64.exe", 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "release body" {
		t.Fatalf("body = %q, want release body", body)
	}
	if etag != `"upstream-etag"` {
		t.Fatalf("ETag = %q, want upstream ETag", etag)
	}
}

func TestGetRejectsBodyLargerThanLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("five!"))
	}))
	defer server.Close()

	_, _, err := newStore(t, server, time.Second).Get(context.Background(), "releases/v0.4.4/gift-panel-windows-x64.exe", 4)
	if err == nil {
		t.Fatal("Get() error = nil, want oversized body rejection")
	}
}

func TestGetRejectsKeysOutsideChannelAndReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server received a request for a rejected key")
	}))
	defer server.Close()

	_, _, err := newStore(t, server, time.Second).Get(context.Background(), "private/credentials.json", 64)
	if err == nil {
		t.Fatal("Get() error = nil, want rejection for arbitrary key")
	}
}

func TestGetUsesSuppliedBoundedHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = writer.Write([]byte("late"))
	}))
	defer server.Close()

	started := time.Now()
	_, _, err := newStore(t, server, 25*time.Millisecond).Get(context.Background(), "releases/v0.4.4/gift-panel-windows-x64.exe", 64)
	if err == nil {
		t.Fatal("Get() error = nil, want supplied client timeout")
	}
	if elapsed := time.Since(started); elapsed >= 200*time.Millisecond {
		t.Fatalf("Get() took %s, want bounded timeout before server response", elapsed)
	}
}

func TestPresignGetSignsExactReleaseKeyForTenMinutes(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	urlString, err := newStore(t, server, time.Second).PresignGet(context.Background(), "releases/v0.4.4/gift-panel-windows-x64.exe", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := url.Parse(urlString)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Path != "/releases/v0.4.4/gift-panel-windows-x64.exe" {
		t.Fatalf("signed path = %q, want exact release key", signed.Path)
	}
	if signed.Query().Get("q-signature") == "" {
		t.Fatal("q-signature is empty, want signed GET URL")
	}
	times := strings.Split(signed.Query().Get("q-sign-time"), ";")
	if len(times) != 2 {
		t.Fatalf("q-sign-time = %q, want start;end", signed.Query().Get("q-sign-time"))
	}
	startSeconds, err := parseUnixSeconds(times[0])
	if err != nil {
		t.Fatal(err)
	}
	endSeconds, err := parseUnixSeconds(times[1])
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(endSeconds-startSeconds)*time.Second != 10*time.Minute {
		t.Fatalf("signed expiration = %s, want 10m", time.Duration(endSeconds-startSeconds)*time.Second)
	}
}

func TestPresignGetRejectsKeysOutsideReleases(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := newStore(t, server, time.Second).PresignGet(context.Background(), "channels/stable/latest.json", 10*time.Minute)
	if err == nil {
		t.Fatal("PresignGet() error = nil, want non-release key rejection")
	}
}

func TestPresignGetRejectsTTLThatIsNotTenMinutes(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := newStore(t, server, time.Second).PresignGet(context.Background(), "releases/v0.4.4/gift-panel-windows-x64.exe", 5*time.Minute)
	if err == nil {
		t.Fatal("PresignGet() error = nil, want non-10-minute TTL rejection")
	}
}

func newStore(t *testing.T, server *httptest.Server, timeout time.Duration) *cosstore.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := cosstore.New("private-release-1250000000", "ap-guangzhou", "secret-id", "secret-key", &http.Client{
		Timeout: timeout,
		Transport: rewriteTransport{
			target: target,
			base:   http.DefaultTransport,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func parseUnixSeconds(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.URL.Scheme = transport.target.Scheme
	cloned.URL.Host = transport.target.Host
	cloned.Host = ""
	return transport.base.RoundTrip(cloned)
}
