package biligateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestControlledGatewayMetricsCountsRiskRateLimitFailureAndBreaker(t *testing.T) {
	failures := &fakeGatewayUpstream{roomInfo: func(context.Context, string, []byte) (RoomInfo, error) {
		return RoomInfo{}, errors.New("upstream-timeout")
	}}
	gateway := NewControlledGateway(failures, fakeCredentialLoader{}, GatewayOptions{})
	if _, err := gateway.RoomInfo(WithAccount(context.Background(), 7), "12"); err == nil {
		t.Fatal("expected upstream failure")
	}
	metrics := gateway.Metrics()
	if metrics.Failures != 1 || metrics.RateLimited != 0 || metrics.RiskEvents != 0 || metrics.BreakerOpen {
		t.Fatalf("failure metrics = %#v", metrics)
	}

	limited := NewControlledGateway(&fakeGatewayUpstream{roomInfo: func(_ context.Context, roomID string, _ []byte) (RoomInfo, error) {
		return RoomInfo{RoomID: roomID, CanonicalRoomID: roomID}, nil
	}}, fakeCredentialLoader{}, GatewayOptions{})
	for index := 0; index < 21; index++ {
		_, _ = limited.RoomInfo(WithAccount(context.Background(), 7), fmt.Sprintf("%d", index+1))
	}
	metrics = limited.Metrics()
	if metrics.RateLimited == 0 {
		t.Fatalf("rate-limit metrics = %#v", metrics)
	}

	upstreamLimited := NewControlledGateway(&fakeGatewayUpstream{roomInfo: func(context.Context, string, []byte) (RoomInfo, error) {
		return RoomInfo{}, ErrRateLimited
	}}, fakeCredentialLoader{}, GatewayOptions{})
	if _, err := upstreamLimited.RoomInfo(WithAccount(context.Background(), 7), "12"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("RoomInfo() error = %v, want rate limited", err)
	}
	metrics = upstreamLimited.Metrics()
	if metrics.RateLimited != 1 || metrics.RiskEvents != 1 {
		t.Fatalf("upstream rate-limit metrics = %#v", metrics)
	}

	risky := NewControlledGateway(&fakeGatewayUpstream{roomInfo: func(context.Context, string, []byte) (RoomInfo, error) {
		return RoomInfo{}, ErrRiskRejected
	}}, fakeCredentialLoader{}, GatewayOptions{})
	for index := 0; index < 10; index++ {
		_, _ = risky.RoomInfo(WithAccount(context.Background(), int64(index%3+1)), "12")
	}
	metrics = risky.Metrics()
	if metrics.RiskEvents < 10 || !metrics.BreakerOpen {
		t.Fatalf("risk metrics = %#v", metrics)
	}
	rendered := fmt.Sprintf("%#v", metrics)
	for _, forbidden := range []string{"sessdata", "cookie", "account"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Fatalf("metrics exposed %q: %s", forbidden, rendered)
		}
	}
}
