package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostedChangelogHandlerDomesticSuccessSkipsGitHub(t *testing.T) {
	var domesticRequests atomic.Int32
	domestic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		domesticRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.2.0"}]}`))
	}))
	defer domestic.Close()
	var githubRequests atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubRequests.Add(1)
		t.Fatal("GitHub must not be requested after domestic success")
	}))
	defer github.Close()

	response := httptest.NewRecorder()
	newHostedChangelogHandler(domestic.Client(), []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))

	assertHostedChangelogVersion(t, response, http.StatusOK, "0.2.0")
	if domesticRequests.Load() != 1 || githubRequests.Load() != 0 {
		t.Fatalf("requests: domestic=%d github=%d, want 1 and 0", domesticRequests.Load(), githubRequests.Load())
	}
}

func TestHostedChangelogHandlerFallsBackAfterDomestic503(t *testing.T) {
	var domesticRequests atomic.Int32
	domestic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		domesticRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer domestic.Close()
	var githubRequests atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubRequests.Add(1)
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.2.0"}]}`))
	}))
	defer github.Close()

	response := httptest.NewRecorder()
	newHostedChangelogHandler(domestic.Client(), []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))

	assertHostedChangelogVersion(t, response, http.StatusOK, "0.2.0")
	if domesticRequests.Load() != 1 || githubRequests.Load() != 1 {
		t.Fatalf("requests: domestic=%d github=%d, want 1 and 1", domesticRequests.Load(), githubRequests.Load())
	}
}

func TestHostedChangelogHandlerFallsBackAfterInvalidDomesticJSON(t *testing.T) {
	var domesticRequests atomic.Int32
	domestic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		domesticRequests.Add(1)
		_, _ = w.Write([]byte(`{"schemaVersion":`))
	}))
	defer domestic.Close()
	var githubRequests atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubRequests.Add(1)
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.2.0"}]}`))
	}))
	defer github.Close()

	response := httptest.NewRecorder()
	newHostedChangelogHandler(domestic.Client(), []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))

	assertHostedChangelogVersion(t, response, http.StatusOK, "0.2.0")
	if domesticRequests.Load() != 1 || githubRequests.Load() != 1 {
		t.Fatalf("requests: domestic=%d github=%d, want 1 and 1", domesticRequests.Load(), githubRequests.Load())
	}
}

func TestHostedChangelogHandlerFallsBackAfterInvalidDomesticDocument(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unsupported schema", body: `{"schemaVersion":2,"releases":[{"version":"0.2.0"}]}`},
		{name: "empty releases", body: `{"schemaVersion":1,"releases":[]}`},
		{name: "oversized body", body: strings.Repeat("x", int(hostedChangelogMaxBytes+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var domesticRequests atomic.Int32
			domestic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				domesticRequests.Add(1)
				_, _ = w.Write([]byte(test.body))
			}))
			defer domestic.Close()
			var githubRequests atomic.Int32
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				githubRequests.Add(1)
				_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.2.0"}]}`))
			}))
			defer github.Close()

			response := httptest.NewRecorder()
			newHostedChangelogHandler(domestic.Client(), []hostedChangelogSource{
				{Name: "国内镜像", URL: domestic.URL},
				{Name: "GitHub", URL: github.URL},
			})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))

			assertHostedChangelogVersion(t, response, http.StatusOK, "0.2.0")
			if domesticRequests.Load() != 1 || githubRequests.Load() != 1 {
				t.Fatalf("requests: domestic=%d github=%d, want 1 and 1", domesticRequests.Load(), githubRequests.Load())
			}
		})
	}
}

func TestHostedChangelogHandlerFallsBackAfterDomesticTimeout(t *testing.T) {
	var domesticRequests atomic.Int32
	domestic := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		domesticRequests.Add(1)
		<-r.Context().Done()
	}))
	defer domestic.Close()
	var githubRequests atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubRequests.Add(1)
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.2.0"}]}`))
	}))
	defer github.Close()

	client := &http.Client{Timeout: 75 * time.Millisecond}
	response := httptest.NewRecorder()
	startedAt := time.Now()
	newHostedChangelogHandler(client, []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))
	elapsed := time.Since(startedAt)

	assertHostedChangelogVersion(t, response, http.StatusOK, "0.2.0")
	if elapsed > time.Second {
		t.Fatalf("request took %s, want fallback within 1s", elapsed)
	}
	if domesticRequests.Load() != 1 || githubRequests.Load() != 1 {
		t.Fatalf("requests: domestic=%d github=%d, want 1 and 1", domesticRequests.Load(), githubRequests.Load())
	}
}

func TestHostedChangelogHandlerPreservesReleaseOrder(t *testing.T) {
	var githubRequests atomic.Int32
	domestic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.3.0"},{"version":"0.2.0"}]}`))
	}))
	defer domestic.Close()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubRequests.Add(1)
		t.Fatal("GitHub must not be requested after a valid domestic document")
	}))
	defer github.Close()

	response := httptest.NewRecorder()
	newHostedChangelogHandler(domestic.Client(), []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))

	assertHostedChangelogVersions(t, response, http.StatusOK, "0.3.0", "0.2.0")
	if githubRequests.Load() != 0 {
		t.Fatalf("GitHub requests = %d, want 0", githubRequests.Load())
	}
}

func TestHostedChangelogHandlerUsesStaleCacheWhenAllSourcesFail(t *testing.T) {
	var available atomic.Bool
	available.Store(true)
	var domesticRequests atomic.Int32
	var githubRequests atomic.Int32
	newServer := func(version string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if version == "0.2.0" {
				domesticRequests.Add(1)
			} else {
				githubRequests.Add(1)
			}
			if !available.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"` + version + `"}]}`))
		}))
	}
	domestic := newServer("0.2.0")
	defer domestic.Close()
	github := newServer("0.1.0")
	defer github.Close()
	now := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
	handler := newHostedChangelogHandlerWithNow(domestic.Client(), []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	}, func() time.Time { return now })

	first := httptest.NewRecorder()
	handler(first, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))
	assertHostedChangelogVersion(t, first, http.StatusOK, "0.2.0")
	available.Store(false)
	now = now.Add(hostedChangelogCacheTTL + time.Second)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))

	assertHostedChangelogVersion(t, response, http.StatusOK, "0.2.0")
	if domesticRequests.Load() != 2 || githubRequests.Load() != 1 {
		t.Fatalf("requests: domestic=%d github=%d, want 2 and 1", domesticRequests.Load(), githubRequests.Load())
	}
}

func TestHostedChangelogHandlerReturnsBadGatewayWhenAllSourcesFailWithoutCache(t *testing.T) {
	var domesticRequests atomic.Int32
	domestic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		domesticRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer domestic.Close()
	var githubRequests atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer github.Close()

	response := httptest.NewRecorder()
	newHostedChangelogHandler(domestic.Client(), []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	})(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message != "在线更新日志暂时不可用" {
		t.Fatalf("message = %q, want generic error", payload.Message)
	}
	if domesticRequests.Load() != 1 || githubRequests.Load() != 1 {
		t.Fatalf("requests: domestic=%d github=%d, want 1 and 1", domesticRequests.Load(), githubRequests.Load())
	}
}

func TestHostedChangelogHandlerCachesForThirtyMinutes(t *testing.T) {
	var domesticRequests atomic.Int32
	domestic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		domesticRequests.Add(1)
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.2.0"}]}`))
	}))
	defer domestic.Close()
	var githubRequests atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubRequests.Add(1)
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.1.0"}]}`))
	}))
	defer github.Close()
	now := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC)
	handler := newHostedChangelogHandlerWithNow(domestic.Client(), []hostedChangelogSource{
		{Name: "国内镜像", URL: domestic.URL},
		{Name: "GitHub", URL: github.URL},
	}, func() time.Time { return now })

	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/api/changelog", nil)
		response := httptest.NewRecorder()
		handler(response, request)
		assertHostedChangelogVersion(t, response, http.StatusOK, "0.2.0")
	}
	if domesticRequests.Load() != 1 || githubRequests.Load() != 0 {
		t.Fatalf("requests: domestic=%d github=%d, want 1 and 0", domesticRequests.Load(), githubRequests.Load())
	}
}

func TestHostedChangelogDefaultSourcesPreferDomesticMirror(t *testing.T) {
	previous := updateAPIBaseURLHex
	updateAPIBaseURLHex = hex.EncodeToString([]byte("https://updates.example.test"))
	t.Cleanup(func() { updateAPIBaseURLHex = previous })

	sources := defaultHostedChangelogSources()
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	if sources[0] != (hostedChangelogSource{Name: "国内镜像", URL: "https://updates.example.test/api/v1/changelog"}) {
		t.Fatalf("domestic source = %#v", sources[0])
	}
	if sources[1] != (hostedChangelogSource{Name: "GitHub", URL: hostedChangelogURL}) {
		t.Fatalf("GitHub source = %#v", sources[1])
	}
}

func assertHostedChangelogVersion(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantVersion string) {
	t.Helper()
	assertHostedChangelogVersions(t, response, wantStatus, wantVersion)
}

func assertHostedChangelogVersions(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantVersions ...string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d", response.Code, wantStatus)
	}
	var payload struct {
		Code     int `json:"code"`
		Releases []struct {
			Version string `json:"version"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != 0 || len(payload.Releases) != len(wantVersions) {
		t.Fatalf("payload = %+v, want releases %q", payload, wantVersions)
	}
	for index, wantVersion := range wantVersions {
		if payload.Releases[index].Version != wantVersion {
			t.Fatalf("release %d = %q, want %q", index, payload.Releases[index].Version, wantVersion)
		}
	}
}
