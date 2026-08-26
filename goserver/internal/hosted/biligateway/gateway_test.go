package biligateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/roomwatcher"
)

// This test fails if the production probe exposes room_init's numeric status
// instead of the normalized roomwatcher contract, treats looping as a real
// broadcast, or lets an upstream body escape through its error.
func TestControlledGatewayWatcherProbeNormalizesLiveStateWithoutRawPayload(t *testing.T) {
	var status atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("id") != "12" {
			t.Fatalf("probe room query = %q, want canonical room 12", request.URL.RawQuery)
		}
		if got := request.Header.Get("Cookie"); got != "SESSDATA=only-in-memory" {
			t.Fatalf("probe credential = %q", got)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"live_status": status.Load()},
		})
	}))
	defer server.Close()
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{Client: server.Client(), RoomInfoEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewControlledGateway(upstream, fakeCredentialLoader{}, GatewayOptions{})

	for _, test := range []struct {
		name   string
		status int
		want   roomwatcher.ObservedState
	}{
		{name: "offline", status: 0, want: roomwatcher.ObservedOffline},
		{name: "live", status: 1, want: roomwatcher.ObservedLive},
		{name: "looping is not a real broadcast", status: 2, want: roomwatcher.ObservedOffline},
	} {
		t.Run(test.name, func(t *testing.T) {
			status.Store(int64(test.status))
			got, err := gateway.Probe(context.Background(), "00012")
			if err != nil || got != test.want {
				t.Fatalf("Probe() = %q, %v; want %q", got, err, test.want)
			}
		})
	}

	status.Store(99)
	_, err = gateway.Probe(context.Background(), "12")
	if !errors.Is(err, ErrEgressUnavailable) {
		t.Fatalf("unknown live status error = %v", err)
	}
	if strings.Contains(err.Error(), "99") || strings.Contains(err.Error(), "live_status") {
		t.Fatalf("probe error exposed raw upstream payload: %v", err)
	}
}

func TestControlledGatewayNormalizesRoomCacheKeysAndCoalescesMisses(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	upstream := &fakeGatewayUpstream{roomInfo: func(_ context.Context, roomID string, _ []byte) (RoomInfo, error) {
		return RoomInfo{RoomID: roomID, CanonicalRoomID: "12"}, nil
	}}
	gateway := NewControlledGateway(upstream, fakeCredentialLoader{}, GatewayOptions{Now: func() time.Time { return clock }})
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := gateway.RoomInfo(WithAccount(context.Background(), 7), "00012"); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if upstream.roomInfoCalls != 1 {
		t.Fatalf("upstream room info calls = %d, want one coalesced normalized miss", upstream.roomInfoCalls)
	}
	if _, err := gateway.RoomInfo(WithAccount(context.Background(), 7), "12"); err != nil {
		t.Fatal(err)
	}
	if upstream.roomInfoCalls != 1 {
		t.Fatalf("normalized cache calls = %d, want 1", upstream.roomInfoCalls)
	}
	clock = clock.Add(5*time.Minute + time.Nanosecond)
	if _, err := gateway.RoomInfo(WithAccount(context.Background(), 7), "12"); err != nil {
		t.Fatal(err)
	}
	if upstream.roomInfoCalls != 2 {
		t.Fatalf("expired cache calls = %d, want 2", upstream.roomInfoCalls)
	}
}

func TestControlledGatewayFailsClosedWithoutTrustedAccountScope(t *testing.T) {
	upstream := &fakeGatewayUpstream{roomInfo: func(_ context.Context, roomID string, _ []byte) (RoomInfo, error) {
		return RoomInfo{RoomID: roomID, CanonicalRoomID: roomID}, nil
	}}
	gateway := NewControlledGateway(upstream, fakeCredentialLoader{}, GatewayOptions{})
	if _, err := gateway.RoomInfo(context.Background(), "12"); !errors.Is(err, ErrAccountScopeRequired) {
		t.Fatalf("unscoped request error = %v", err)
	}
	if upstream.roomInfoCalls != 0 {
		t.Fatalf("unscoped request reached upstream %d times", upstream.roomInfoCalls)
	}
}

func TestEgressBreakerRequiresCorrelatedRiskAcrossAccounts(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	breaker := newEgressBreaker(func() time.Time { return clock })
	for index := 0; index < 9; index++ {
		breaker.RecordRisk(int64(index%3 + 1))
	}
	if !breaker.Allow(int64(4)) {
		t.Fatal("breaker opened before ten correlated responses")
	}
	breaker.RecordRisk(1)
	if breaker.Allow(4) {
		t.Fatal("breaker remained closed after ten risks across three accounts")
	}
	clock = clock.Add(2 * time.Minute)
	if !breaker.Allow(4) {
		t.Fatal("breaker did not allow its one half-open probe")
	}
	if breaker.Allow(5) {
		t.Fatal("breaker allowed more than one half-open probe")
	}
	breaker.RecordSuccess()
	breaker.RecordSuccess()
	breaker.RecordSuccess()
	if !breaker.Allow(5) {
		t.Fatal("breaker did not close after three successful probes")
	}
}

func TestControlledGatewayAppliesBreakerToEveryOperationAndClosesAfterThreeHalfOpenSuccesses(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	upstream := &fakeGatewayUpstream{roomInfo: func(context.Context, string, []byte) (RoomInfo, error) { return RoomInfo{}, ErrRiskRejected }}
	gateway := NewControlledGateway(upstream, fakeCredentialLoader{}, GatewayOptions{Now: func() time.Time { return clock }})
	for index := 0; index < 10; index++ {
		_, err := gateway.RoomInfo(WithAccount(context.Background(), int64(index%3+1)), strconv.Itoa(index+1))
		if !errors.Is(err, ErrRiskRejected) {
			t.Fatalf("risk request %d error=%v", index, err)
		}
	}
	upstream.roomInfo = func(_ context.Context, roomID string, _ []byte) (RoomInfo, error) {
		return RoomInfo{RoomID: roomID, CanonicalRoomID: roomID}, nil
	}
	for _, operation := range []func() error{
		func() error { _, err := gateway.RoomInfo(WithAccount(context.Background(), 4), "99"); return err },
		func() error { _, err := gateway.GiftCatalog(WithAccount(context.Background(), 4), "99"); return err },
		func() error {
			_, err := gateway.OpenRoom(WithAccount(context.Background(), 4), "99", func(Event) {})
			return err
		},
	} {
		if err := operation(); !errors.Is(err, ErrEgressUnavailable) {
			t.Fatalf("open breaker operation error=%v", err)
		}
	}
	clock = clock.Add(2 * time.Minute)
	for _, operation := range []func() error{
		func() error { _, err := gateway.RoomInfo(WithAccount(context.Background(), 4), "99"); return err },
		func() error { _, err := gateway.GiftCatalog(WithAccount(context.Background(), 4), "99"); return err },
		func() error {
			_, err := gateway.OpenRoom(WithAccount(context.Background(), 4), "99", func(Event) {})
			return err
		},
	} {
		if err := operation(); err != nil {
			t.Fatalf("half-open success error=%v", err)
		}
	}
	if _, err := gateway.RoomInfo(WithAccount(context.Background(), 4), "100"); err != nil {
		t.Fatalf("breaker did not close after three successful probes: %v", err)
	}
}

func TestControlledGatewayCacheHitsDoNotConsumeHalfOpenBreakerProbe(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	upstream := &fakeGatewayUpstream{roomInfo: func(_ context.Context, roomID string, _ []byte) (RoomInfo, error) {
		return RoomInfo{RoomID: roomID, CanonicalRoomID: roomID}, nil
	}}
	gateway := NewControlledGateway(upstream, fakeCredentialLoader{}, GatewayOptions{Now: func() time.Time { return clock }})
	if _, err := gateway.RoomInfo(WithAccount(context.Background(), 1), "9"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		gateway.breaker.RecordRisk(int64(index%3 + 1))
	}
	clock = clock.Add(2 * time.Minute)
	if _, err := gateway.RoomInfo(WithAccount(context.Background(), 4), "9"); err != nil {
		t.Fatalf("cached room error=%v", err)
	}
	if !gateway.breaker.Allow(4) {
		t.Fatal("cache hit consumed the only half-open probe")
	}
}

func TestControlledGatewayCoalescesConcurrentHalfOpenMissIntoOneProbe(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	started, release := make(chan struct{}), make(chan struct{})
	upstream := &fakeGatewayUpstream{roomInfo: func(_ context.Context, roomID string, _ []byte) (RoomInfo, error) {
		close(started)
		<-release
		return RoomInfo{RoomID: roomID, CanonicalRoomID: roomID}, nil
	}}
	gateway := NewControlledGateway(upstream, fakeCredentialLoader{}, GatewayOptions{Now: func() time.Time { return clock }})
	for index := 0; index < 10; index++ {
		gateway.breaker.RecordRisk(int64(index%3 + 1))
	}
	clock = clock.Add(2 * time.Minute)
	errors := make(chan error, 2)
	for _, accountID := range []int64{4, 5} {
		go func(accountID int64) {
			_, err := gateway.RoomInfo(WithAccount(context.Background(), accountID), "9")
			errors <- err
		}(accountID)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("half-open probe did not reach upstream")
	}
	close(release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatalf("coalesced half-open request error=%v", err)
		}
	}
	if upstream.roomInfoCalls != 1 {
		t.Fatalf("half-open upstream probes=%d, want one", upstream.roomInfoCalls)
	}
}

func TestControlledGatewayOpenRoomLoadFailureReopensHalfOpenBreaker(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	gateway := NewControlledGateway(&fakeGatewayUpstream{}, failingCredentialLoader{}, GatewayOptions{Now: func() time.Time { return clock }})
	for index := 0; index < 10; index++ {
		gateway.breaker.RecordRisk(int64(index%3 + 1))
	}
	clock = clock.Add(2 * time.Minute)
	if _, err := gateway.OpenRoom(WithAccount(context.Background(), 4), "9", func(Event) {}); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("load error=%v", err)
	}
	if gateway.breaker.Allow(4) {
		t.Fatal("load failure released an immediate half-open probe")
	}
	clock = clock.Add(2 * time.Minute)
	if !gateway.breaker.Allow(4) {
		t.Fatal("load failure did not allow a probe after the two minute hold")
	}
}

func TestEgressBreakerRequiresThreeConsecutiveHalfOpenSuccesses(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	breaker := newEgressBreaker(func() time.Time { return clock })
	for index := 0; index < 10; index++ {
		breaker.RecordRisk(int64(index%3 + 1))
	}
	clock = clock.Add(2 * time.Minute)
	if !breaker.Allow(4) {
		t.Fatal("first half-open probe rejected")
	}
	breaker.RecordSuccess()
	if !breaker.Allow(4) {
		t.Fatal("second half-open probe rejected")
	}
	breaker.RecordFailure()
	clock = clock.Add(2 * time.Minute)
	for range 2 {
		if !breaker.Allow(4) {
			t.Fatal("half-open probe rejected")
		}
		breaker.RecordSuccess()
	}
	if breaker.openedUntil.IsZero() {
		t.Fatal("two successes after a failure closed breaker")
	}
}

func TestEgressBreakerHalfOpenFailureReopensTwoMinuteHold(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	breaker := newEgressBreaker(func() time.Time { return clock })
	for index := 0; index < 10; index++ {
		breaker.RecordRisk(int64(index%3 + 1))
	}
	clock = clock.Add(2 * time.Minute)
	if !breaker.Allow(4) {
		t.Fatal("half-open probe rejected")
	}
	breaker.RecordFailure()
	if breaker.Allow(4) {
		t.Fatal("failure released an immediate second probe")
	}
	clock = clock.Add(2 * time.Minute)
	if !breaker.Allow(4) {
		t.Fatal("breaker did not reopen after two minute hold")
	}
}

type fakeCredentialLoader struct{}

func (fakeCredentialLoader) Load(context.Context) (Credential, error) {
	return Credential{Version: 1, Cookie: []byte("SESSDATA=only-in-memory")}, nil
}

type failingCredentialLoader struct{}

func (failingCredentialLoader) Load(context.Context) (Credential, error) {
	return Credential{}, ErrCredentialUnavailable
}

type fakeGatewayUpstream struct {
	mu            sync.Mutex
	roomInfoCalls int
	roomInfo      func(context.Context, string, []byte) (RoomInfo, error)
}

func (upstream *fakeGatewayUpstream) RoomInfo(ctx context.Context, roomID string, cookie []byte) (RoomInfo, error) {
	upstream.mu.Lock()
	upstream.roomInfoCalls++
	upstream.mu.Unlock()
	return upstream.roomInfo(ctx, roomID, cookie)
}
func (*fakeGatewayUpstream) GiftCatalog(context.Context, string, []byte) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (*fakeGatewayUpstream) OpenRoom(context.Context, string, []byte, Sink) (Connection, error) {
	return fakeConnection{}, nil
}

type fakeConnection struct{}

func (fakeConnection) Close() error          { return nil }
func (fakeConnection) Done() <-chan struct{} { return nil }
func (fakeConnection) Err() error            { return nil }
