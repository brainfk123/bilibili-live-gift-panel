package app

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// MetricsSnapshot contains only process-wide aggregates. It deliberately has
// no label, account, room, viewer, credential, or request dimensions.
type MetricsSnapshot struct {
	BilibiliBreakerOpen bool
	Room                RoomRuntimeStatus
}

// MetricsSnapshotFunc reads one detached aggregate snapshot per accepted
// metrics request.
type MetricsSnapshotFunc func() MetricsSnapshot

func internalMetricsHandler(snapshot MetricsSnapshotFunc) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", prometheusContentType)
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.URL.RawQuery != "" || request.URL.ForceQuery {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		current := snapshot()
		var body strings.Builder
		writeMetric(&body, "hosted_up", "gauge", "1")
		writeMetric(&body, "hosted_bilibili_breaker_open", "gauge", boolMetric(current.BilibiliBreakerOpen))
		writeMetric(&body, "hosted_room_watched", "gauge", strconv.Itoa(current.Room.WatchedRooms))
		writeMetric(&body, "hosted_room_transition_failures_total", "counter", strconv.FormatUint(current.Room.TransitionFailures, 10))
		writeMetric(&body, "hosted_room_grace_transitions_total", "counter", strconv.FormatUint(current.Room.GraceTransitions, 10))
		writeMetric(&body, "hosted_room_confirmed_to_ready_samples_total", "counter", strconv.FormatUint(current.Room.ReadinessSamples, 10))
		writeMetric(&body, "hosted_room_confirmed_to_ready_within_10_seconds_total", "counter", strconv.FormatUint(current.Room.ReadinessWithin10, 10))
		writeMetric(&body, "hosted_room_confirmed_to_ready_within_30_seconds_total", "counter", strconv.FormatUint(current.Room.ReadinessWithin30, 10))
		writeMetric(&body, "hosted_room_confirmed_to_ready_over_30_seconds_total", "counter", strconv.FormatUint(current.Room.ReadinessOver30, 10))
		writeMetric(&body, "hosted_room_confirmed_to_ready_seconds_total", "counter", durationMetric(current.Room.ReadinessTotal))
		writeMetric(&body, "hosted_room_confirmed_to_ready_seconds_max", "gauge", durationMetric(current.Room.ReadinessMaximum))
		writeMetric(&body, "hosted_room_readiness_alert", "gauge", boolMetric(current.Room.ReadinessAlert))
		writeMetric(&body, "hosted_room_probe_capacity_per_minute", "gauge", strconv.Itoa(current.Room.ProbeCapacityPerMinute))
		writeMetric(&body, "hosted_room_probe_available", "gauge", strconv.Itoa(current.Room.ProbeAvailable))
		writeMetric(&body, "hosted_room_probe_backlog", "gauge", strconv.Itoa(current.Room.ProbeBacklog))
		writeMetric(&body, "hosted_room_probe_capacity_alert", "gauge", boolMetric(current.Room.ProbeCapacityAlert))
		_, _ = io.WriteString(response, body.String())
	})
}

func writeMetric(body *strings.Builder, name, kind, value string) {
	_, _ = fmt.Fprintf(body, "# TYPE %s %s\n%s %s\n", name, kind, name, value)
}

func boolMetric(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func durationMetric(value time.Duration) string {
	return strconv.FormatFloat(value.Seconds(), 'f', -1, 64)
}
