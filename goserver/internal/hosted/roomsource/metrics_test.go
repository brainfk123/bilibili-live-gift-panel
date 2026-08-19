package roomsource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestManagerMetricsCountsDistinctRoomsReconnectsAndHealth(t *testing.T) {
	gateway := newFakeGateway(map[string]string{"7": "42", "8": "84"})
	manager := NewManager(gateway, Options{})
	t.Cleanup(manager.Close)
	first, err := manager.Subscribe(context.Background(), "7", 1, newRecordingSink())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Cancel()
	second, err := manager.Subscribe(context.Background(), "8", 2, newRecordingSink())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Cancel()

	metrics := manager.Metrics()
	if metrics.DistinctRooms != 2 || !metrics.Healthy || metrics.Reconnects != 0 {
		t.Fatalf("initial metrics = %#v", metrics)
	}

	clock := newManualClock(time.Unix(1_000, 0))
	timers := newManualTimerFactory(clock)
	reconnect := newReconnectGateway()
	reconnecting := NewManager(reconnect, Options{Now: clock.Now, NewTimer: timers.NewTimer, Jitter: func() float64 { return .5 }})
	t.Cleanup(reconnecting.Close)
	subscription, err := reconnecting.Subscribe(context.Background(), "42", 7, newRecordingSink())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	reconnect.connection(t, 0).fail(errors.New("disconnected"))
	timers.next(t).fire()
	reconnect.waitForOpens(t, 2)
	metrics = reconnecting.Metrics()
	if metrics.Reconnects != 1 || metrics.DistinctRooms != 1 {
		t.Fatalf("reconnect metrics = %#v", metrics)
	}
	rendered := fmt.Sprintf("%#v", metrics)
	for _, forbidden := range []string{"account", "cookie", "42-room"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Fatalf("metrics exposed %q: %s", forbidden, rendered)
		}
	}
}
