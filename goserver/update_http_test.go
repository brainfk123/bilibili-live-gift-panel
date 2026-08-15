package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestValidateUpdateURLRejectsUnsafeDestinations(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "public HTTPS", rawURL: "https://github.com/example/release?token=secret"},
		{name: "HTTP downgrade", rawURL: "http://github.com/example/release", wantErr: true},
		{name: "userinfo", rawURL: "https://user:pass@github.com/example/release", wantErr: true},
		{name: "localhost", rawURL: "https://localhost/release", wantErr: true},
		{name: "localhost subdomain", rawURL: "https://metadata.localhost/release", wantErr: true},
		{name: "loopback IPv4", rawURL: "https://127.0.0.1/release", wantErr: true},
		{name: "private IPv4", rawURL: "https://10.0.0.1/release", wantErr: true},
		{name: "link local IPv4", rawURL: "https://169.254.169.254/release", wantErr: true},
		{name: "loopback IPv6", rawURL: "https://[::1]/release", wantErr: true},
		{name: "fragment", rawURL: "https://github.com/release#fragment", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			err = validateUpdateURL(parsed)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateUpdateURL(%q) error = %v, wantErr = %v", test.rawURL, err, test.wantErr)
			}
		})
	}
}

func TestUpdateAddressPolicyRejectsNonPublicResolvedIPs(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "8.8.8.8", want: true},
		{address: "2606:4700:4700::1111", want: true},
		{address: "10.0.0.1"},
		{address: "100.64.0.1"},
		{address: "127.0.0.1"},
		{address: "169.254.169.254"},
		{address: "192.168.1.1"},
		{address: "198.18.0.1"},
		{address: "::1"},
		{address: "fd00::1"},
		{address: "fe80::1"},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := updateAddressIsPublic(net.ParseIP(test.address)); got != test.want {
				t.Fatalf("updateAddressIsPublic(%q) = %v, want %v", test.address, got, test.want)
			}
		})
	}
}

func TestUpdateRedirectPolicyRejectsUnsafeAndExcessiveRedirects(t *testing.T) {
	publicRequest := func(rawURL string) *http.Request {
		request, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		return request
	}

	if err := checkUpdateRedirect(publicRequest("http://example.com/file"), nil); err == nil {
		t.Fatal("HTTP redirect accepted, want rejection")
	}
	if err := checkUpdateRedirect(publicRequest("https://127.0.0.1/file"), nil); err == nil {
		t.Fatal("loopback redirect accepted, want rejection")
	}
	via := make([]*http.Request, 5)
	if err := checkUpdateRedirect(publicRequest("https://example.com/file"), via); err == nil {
		t.Fatal("sixth request accepted, want redirect limit")
	}
	if err := checkUpdateRedirect(publicRequest("https://example.com/file"), via[:4]); err != nil {
		t.Fatalf("fifth request rejected: %v", err)
	}
}

func TestSafeUpdateNetworkErrorDoesNotExposeURL(t *testing.T) {
	raw := &url.Error{
		Op:  "Get",
		URL: "https://bucket.example/file.exe?q-signature=TOP-SECRET",
		Err: errors.New("connection reset"),
	}
	err := safeUpdateNetworkError(raw)
	if err == nil {
		t.Fatal("safeUpdateNetworkError() = nil")
	}
	message := err.Error()
	for _, forbidden := range []string{"TOP-SECRET", "q-signature", "bucket.example", "/file.exe"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("sanitized error %q contains %q", message, forbidden)
		}
	}
}

func TestDefaultUpdaterRejectsUnsafeReleaseURLBeforeNetworkAccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"tag_name":"v9.9.9","assets":[]}`))
	}))
	defer server.Close()

	updater := newAutoUpdater(autoUpdaterOptions{CurrentVersion: "0.1.0"})
	_, err := updater.fetchReleaseFromSource(t.Context(), updateReleaseSource{Name: "unsafe", URL: server.URL})
	if err == nil {
		t.Fatal("unsafe HTTP release URL accepted")
	}
	if requests.Load() != 0 {
		t.Fatalf("unsafe release source received %d requests, want zero", requests.Load())
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error %q exposed release URL", err)
	}
}

func TestDefaultChangelogClientRejectsUnsafeSourceBeforeNetworkAccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte(`{"schemaVersion":1,"releases":[{}]}`))
	}))
	defer server.Close()

	response := httptest.NewRecorder()
	newHostedChangelogHandler(nil, []hostedChangelogSource{{Name: "unsafe", URL: server.URL}})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.Code)
	}
	if requests.Load() != 0 {
		t.Fatalf("unsafe changelog source received %d requests, want zero", requests.Load())
	}
}

func TestUpdaterSanitizesClientErrorsContainingSignedURLs(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: request.URL.String() + "?q-signature=TOP-SECRET", Err: errors.New("reset")}
	})}
	updater := newAutoUpdater(autoUpdaterOptions{Client: client, CurrentVersion: "0.1.0"})
	_, err := updater.fetchReleaseFromSource(t.Context(), updateReleaseSource{Name: "signed", URL: "https://updates.example.test/release"})
	if err == nil {
		t.Fatal("client error = nil")
	}
	for _, forbidden := range []string{"TOP-SECRET", "q-signature", "updates.example.test"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error %q contains %q", err, forbidden)
		}
	}
}

func TestUpdaterSanitizesMalformedSignedURLsBeforeNetworkAccess(t *testing.T) {
	malformed := "https://updates.example.test/%zz?q-signature=TOP-SECRET"
	updater := newAutoUpdater(autoUpdaterOptions{Client: &http.Client{}, CurrentVersion: "0.1.0", UpdatesDir: t.TempDir()})

	checks := []struct {
		name string
		run  func() error
	}{
		{name: "release", run: func() error {
			_, err := updater.fetchReleaseFromSource(t.Context(), updateReleaseSource{Name: "signed", URL: malformed})
			return err
		}},
		{name: "checksum", run: func() error {
			_, err := updater.fetchChecksum(t.Context(), malformed)
			return err
		}},
		{name: "asset", run: func() error {
			_, err := updater.downloadAsset(t.Context(), "1.2.3", githubAsset{
				Name: updateAssetName, DownloadURL: malformed, Size: 1,
				Digest: "sha256:" + strings.Repeat("a", 64),
			})
			return err
		}},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := check.run()
			if err == nil {
				t.Fatal("malformed URL error = nil")
			}
			for _, forbidden := range []string{"TOP-SECRET", "q-signature", "%zz", "updates.example.test"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error %q contains %q", err, forbidden)
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
