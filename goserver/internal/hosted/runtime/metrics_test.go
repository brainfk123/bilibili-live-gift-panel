package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

func TestManagerMetricsCountsAggregatesWithoutIdentities(t *testing.T) {
	log := &operationLog{}
	sessions := &orderedSessions{enabled: true, target: "999888777", log: log}
	sources := newOrderedRoomSources(log, map[string]string{"999888777": "999888777"})
	factory := &recordingProcessorFactory{
		log:      log,
		accepted: make(chan roomsource.Event, 1),
		status:   ProcessorStatus{Degraded: true, Buffered: 4, Rejecting: true, ConnectionHealthy: true},
	}
	manager, err := NewManager(Dependencies{Sessions: sessions, Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{ProcessorFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if _, err := manager.Acquire(context.Background(), 123456789, LeaseConfig); err != nil {
		t.Fatal(err)
	}

	metrics := manager.Metrics()
	if metrics.ActiveAccounts != 1 || metrics.QueueDepth != 4 || metrics.QueueDepthMax != 4 {
		t.Fatalf("queue metrics = %#v", metrics)
	}
	if metrics.DegradedAccounts != 1 || metrics.RejectingAccounts != 1 {
		t.Fatalf("account gauges = %#v", metrics)
	}
	rendered := fmt.Sprintf("%#v", metrics)
	for _, forbidden := range []string{"123456789", "999888777"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("metrics exposed %q: %s", forbidden, rendered)
		}
	}
}
