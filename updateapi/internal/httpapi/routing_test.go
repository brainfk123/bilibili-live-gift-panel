package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/httpapi"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

type routedReleaseService struct {
	latest       release.PublicRelease
	latestErr    map[release.Channel]error
	latestCalls  []release.Channel
	policy       []byte
	policyErr    error
	policyCalls  int
	changelog    service.Document
	changelogErr error
}

func (stub *routedReleaseService) Latest(_ context.Context, channel release.Channel) (release.PublicRelease, error) {
	stub.latestCalls = append(stub.latestCalls, channel)
	return stub.latest, stub.latestErr[channel]
}

func (stub *routedReleaseService) PublisherPolicy(context.Context) ([]byte, error) {
	stub.policyCalls++
	return append([]byte(nil), stub.policy...), stub.policyErr
}

func (stub *routedReleaseService) Changelog(context.Context) (service.Document, error) {
	return stub.changelog, stub.changelogErr
}

type captureMetrics struct{ observations []httpapi.Observation }

func (metrics *captureMetrics) Observe(observation httpapi.Observation) {
	metrics.observations = append(metrics.observations, observation)
}

func TestLatestRoutesByUserAgent(t *testing.T) {
	tests := []struct {
		name         string
		values       []string
		legacyActive bool
		latestErr    map[release.Channel]error
		wantStatus   int
		wantChannel  release.Channel
		wantCode     string
		wantCall     bool
	}{
		{name: "legacy active", values: []string{"bilibili-live-gift-panel/0.4.7"}, legacyActive: true, wantStatus: 200, wantChannel: release.ChannelLegacyRushRush, wantCall: true},
		{name: "legacy inactive", values: []string{"bilibili-live-gift-panel/0.4.7"}, wantStatus: 503, wantCode: "legacy_channel_unavailable"},
		{name: "legacy pointer unavailable", values: []string{"bilibili-live-gift-panel/0.4.7"}, legacyActive: true, latestErr: map[release.Channel]error{release.ChannelLegacyRushRush: service.ErrReleaseUnavailable}, wantStatus: 503, wantChannel: release.ChannelLegacyRushRush, wantCode: "release_unavailable", wantCall: true},
		{name: "stable 0.4.9", values: []string{"bilibili-live-gift-panel/0.4.9"}, wantStatus: 200, wantChannel: release.ChannelStable, wantCall: true},
		{name: "stable 0.4.10", values: []string{"bilibili-live-gift-panel/0.4.10"}, wantStatus: 200, wantChannel: release.ChannelStable, wantCall: true},
		{name: "stable 0.4.11", values: []string{"bilibili-live-gift-panel/0.4.11"}, wantStatus: 200, wantChannel: release.ChannelStable, wantCall: true},
		{name: "stable 0.4.12", values: []string{"bilibili-live-gift-panel/0.4.12"}, wantStatus: 200, wantChannel: release.ChannelStable, wantCall: true},
		{name: "missing", wantStatus: 400, wantCode: "client_version_invalid"},
		{name: "duplicate", values: []string{"bilibili-live-gift-panel/0.4.12", "bilibili-live-gift-panel/0.4.12"}, wantStatus: 400, wantCode: "client_version_invalid"},
		{name: "whitespace", values: []string{" bilibili-live-gift-panel/0.4.12"}, wantStatus: 400, wantCode: "client_version_invalid"},
		{name: "oversized", values: []string{"bilibili-live-gift-panel/" + strings.Repeat("9", 1024)}, wantStatus: 400, wantCode: "client_version_invalid"},
		{name: "prerelease", values: []string{"bilibili-live-gift-panel/0.4.12-rc.1"}, wantStatus: 400, wantCode: "client_version_invalid"},
		{name: "development", values: []string{"bilibili-live-gift-panel/dev"}, wantStatus: 400, wantCode: "client_version_invalid"},
		{name: "gap 0.4.8", values: []string{"bilibili-live-gift-panel/0.4.8"}, wantStatus: 400, wantCode: "client_version_invalid"},
		{name: "later unreviewed", values: []string{"bilibili-live-gift-panel/0.4.13"}, wantStatus: 400, wantCode: "client_version_invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &routedReleaseService{latest: testRelease(), latestErr: test.latestErr}
			router := service.ChannelRouter{LegacyActive: func(context.Context) (bool, error) { return test.legacyActive, nil }}
			handler := httpapi.New(stub, router, func() string { return generatedID }, &captureLogger{}, &captureMetrics{})
			request := httptest.NewRequest(http.MethodGet, latestPath, nil)
			request.Header.Del("User-Agent")
			for _, value := range test.values {
				request.Header.Add("User-Agent", value)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q, want private, no-store", got)
			}
			if got := response.Header().Get("Vary"); got != "User-Agent" {
				t.Fatalf("Vary = %q, want User-Agent", got)
			}
			if got := response.Header().Get("X-Gift-Panel-Update-Channel"); got != string(test.wantChannel) {
				t.Fatalf("channel header = %q, want %q", got, test.wantChannel)
			}
			if test.wantCode != "" && decodeError(t, response).Code != test.wantCode {
				t.Fatalf("error body = %s, want code %q", response.Body.String(), test.wantCode)
			}
			if len(stub.latestCalls) != boolCount(test.wantCall) {
				t.Fatalf("Latest calls = %#v, want call=%v", stub.latestCalls, test.wantCall)
			}
			if test.wantCall && stub.latestCalls[0] != test.wantChannel {
				t.Fatalf("Latest channel = %q, want %q", stub.latestCalls[0], test.wantChannel)
			}
		})
	}
}

func TestPublisherPolicyEndpoint(t *testing.T) {
	policy := []byte(`{"signed":{"epoch":7},"signatures":[{"signature":"complete-envelope"}]}`)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			stub := &routedReleaseService{policy: policy}
			handler := httpapi.New(stub, service.ChannelRouter{}, func() string { return generatedID }, &captureLogger{}, &captureMetrics{})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/trust/publisher-policy", nil))

			if response.Code != http.StatusOK || stub.policyCalls != 1 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.policyCalls, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if got := response.Header().Get("Content-Length"); got != "71" {
				t.Fatalf("Content-Length = %q, want complete envelope length", got)
			}
			if method == http.MethodGet && response.Body.String() != string(policy) {
				t.Fatalf("body = %s, want %s", response.Body.String(), policy)
			}
			if method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatalf("HEAD body = %q", response.Body.String())
			}
		})
	}
}

func TestLatestMetricsReceiveOnlyBoundedAggregateDimensions(t *testing.T) {
	metrics := &captureMetrics{}
	handler := httpapi.New(&routedReleaseService{latest: testRelease()}, service.ChannelRouter{}, func() string { return generatedID }, &captureLogger{}, metrics)
	request := httptest.NewRequest(http.MethodGet, latestPath+"?token=private-query", nil)
	request.RemoteAddr = "203.0.113.42:54321"
	request.Header.Set("User-Agent", "private-unknown-client/recognizable-user")
	request.Header.Set("X-Private-Identifier", "recognizable-header")
	request.AddCookie(&http.Cookie{Name: "session", Value: "recognizable-cookie"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if len(metrics.observations) != 1 {
		t.Fatalf("observations = %#v, want one", metrics.observations)
	}
	want := httpapi.Observation{
		Version: httpapi.VersionInvalid,
		Outcome: httpapi.OutcomeClientInvalid,
		Latency: metrics.observations[0].Latency,
	}
	if metrics.observations[0] != want {
		t.Fatalf("observation = %#v, want %#v", metrics.observations[0], want)
	}
	if metrics.observations[0].Latency != httpapi.LatencyUnder100ms &&
		metrics.observations[0].Latency != httpapi.LatencyUnder500ms &&
		metrics.observations[0].Latency != httpapi.LatencyUnder2s &&
		metrics.observations[0].Latency != httpapi.LatencyOver2s {
		t.Fatalf("latency bucket = %q, want closed bucket", metrics.observations[0].Latency)
	}
}

func TestPublisherPolicyEndpointMapsStorageFailureToControlled503(t *testing.T) {
	stub := &routedReleaseService{policyErr: errors.New("private trust object path")}
	handler := httpapi.New(stub, service.ChannelRouter{}, func() string { return generatedID }, &captureLogger{}, &captureMetrics{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/trust/publisher-policy", nil))
	if response.Code != http.StatusServiceUnavailable || decodeError(t, response).Code != "release_unavailable" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
