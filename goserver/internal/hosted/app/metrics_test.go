package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInternalMetricsReturnsAggregatePrometheusGaugesWithoutIdentityLabels(t *testing.T) {
	handler := New(Dependencies{Metrics: func() MetricsSnapshot {
		return MetricsSnapshot{
			BilibiliBreakerOpen: true,
			Room: RoomRuntimeStatus{
				WatchedRooms: 3, TransitionFailures: 2, GraceTransitions: 4,
				ReadinessSamples: 5, ReadinessWithin10: 3, ReadinessWithin30: 4, ReadinessOver30: 1,
				ReadinessTotal: 1500 * time.Millisecond, ReadinessMaximum: 2750 * time.Millisecond, ReadinessAlert: true,
				ProbeCapacityPerMinute: 120, ProbeAvailable: 117, ProbeBacklog: 2, ProbeCapacityAlert: true,
			},
		}
	}})
	request := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("metrics Content-Type=%q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("metrics Cache-Control=%q", got)
	}
	want := "" +
		"# TYPE hosted_up gauge\n" +
		"hosted_up 1\n" +
		"# TYPE hosted_bilibili_breaker_open gauge\n" +
		"hosted_bilibili_breaker_open 1\n" +
		"# TYPE hosted_room_watched gauge\n" +
		"hosted_room_watched 3\n" +
		"# TYPE hosted_room_transition_failures_total counter\n" +
		"hosted_room_transition_failures_total 2\n" +
		"# TYPE hosted_room_grace_transitions_total counter\n" +
		"hosted_room_grace_transitions_total 4\n" +
		"# TYPE hosted_room_confirmed_to_ready_samples_total counter\n" +
		"hosted_room_confirmed_to_ready_samples_total 5\n" +
		"# TYPE hosted_room_confirmed_to_ready_within_10_seconds_total counter\n" +
		"hosted_room_confirmed_to_ready_within_10_seconds_total 3\n" +
		"# TYPE hosted_room_confirmed_to_ready_within_30_seconds_total counter\n" +
		"hosted_room_confirmed_to_ready_within_30_seconds_total 4\n" +
		"# TYPE hosted_room_confirmed_to_ready_over_30_seconds_total counter\n" +
		"hosted_room_confirmed_to_ready_over_30_seconds_total 1\n" +
		"# TYPE hosted_room_confirmed_to_ready_seconds_total counter\n" +
		"hosted_room_confirmed_to_ready_seconds_total 1.5\n" +
		"# TYPE hosted_room_confirmed_to_ready_seconds_max gauge\n" +
		"hosted_room_confirmed_to_ready_seconds_max 2.75\n" +
		"# TYPE hosted_room_readiness_alert gauge\n" +
		"hosted_room_readiness_alert 1\n" +
		"# TYPE hosted_room_probe_capacity_per_minute gauge\n" +
		"hosted_room_probe_capacity_per_minute 120\n" +
		"# TYPE hosted_room_probe_available gauge\n" +
		"hosted_room_probe_available 117\n" +
		"# TYPE hosted_room_probe_backlog gauge\n" +
		"hosted_room_probe_backlog 2\n" +
		"# TYPE hosted_room_probe_capacity_alert gauge\n" +
		"hosted_room_probe_capacity_alert 1\n"
	if got := response.Body.String(); got != want {
		t.Fatalf("metrics body mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	for _, forbidden := range []string{"{", "account", "room_id", "uid", "cookie", "invitation", "challenge"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("metrics body contains forbidden identity marker %q", forbidden)
		}
	}
}

func TestInternalMetricsRejectsQueriesAndUnsupportedMethodsBeforeSampling(t *testing.T) {
	samples := 0
	handler := New(Dependencies{Metrics: func() MetricsSnapshot {
		samples++
		return MetricsSnapshot{}
	}})
	for _, test := range []struct {
		name   string
		method string
		target string
		status int
		allow  string
	}{
		{name: "query", method: http.MethodGet, target: "/internal/metrics?account=41", status: http.StatusBadRequest},
		{name: "empty query delimiter", method: http.MethodGet, target: "/internal/metrics?", status: http.StatusBadRequest},
		{name: "post", method: http.MethodPost, target: "/internal/metrics", status: http.StatusMethodNotAllowed, allow: http.MethodGet},
		{name: "head", method: http.MethodHead, target: "/internal/metrics", status: http.StatusMethodNotAllowed, allow: http.MethodGet},
		{name: "options", method: http.MethodOptions, target: "/internal/metrics", status: http.StatusMethodNotAllowed, allow: http.MethodGet},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
			if response.Code != test.status {
				t.Fatalf("status=%d body=%q want=%d", response.Code, response.Body.String(), test.status)
			}
			if got := response.Header().Get("Allow"); got != test.allow {
				t.Fatalf("Allow=%q want=%q", got, test.allow)
			}
		})
	}
	if samples != 0 {
		t.Fatalf("rejected requests sampled metrics %d times", samples)
	}
}
