package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

func TestChannelRouter(t *testing.T) {
	tests := []struct {
		name         string
		values       []string
		legacyActive bool
		legacyErr    error
		want         release.Channel
		wantErr      error
	}{
		{name: "legacy active", values: []string{"bilibili-live-gift-panel/0.4.7"}, legacyActive: true, want: release.ChannelLegacyRushRush},
		{name: "legacy inactive", values: []string{"bilibili-live-gift-panel/0.4.7"}, wantErr: service.ErrLegacyChannelUnavailable},
		{name: "legacy activation unavailable", values: []string{"bilibili-live-gift-panel/0.4.7"}, legacyErr: errors.New("private activation backend"), wantErr: service.ErrLegacyChannelUnavailable},
		{name: "stable 0.4.9", values: []string{"bilibili-live-gift-panel/0.4.9"}, want: release.ChannelStable},
		{name: "stable 0.4.10", values: []string{"bilibili-live-gift-panel/0.4.10"}, want: release.ChannelStable},
		{name: "stable 0.4.11", values: []string{"bilibili-live-gift-panel/0.4.11"}, want: release.ChannelStable},
		{name: "stable 0.4.12", values: []string{"bilibili-live-gift-panel/0.4.12"}, want: release.ChannelStable},
		{name: "missing", wantErr: service.ErrClientVersionInvalid},
		{name: "duplicate", values: []string{"bilibili-live-gift-panel/0.4.12", "bilibili-live-gift-panel/0.4.12"}, wantErr: service.ErrClientVersionInvalid},
		{name: "wrong product", values: []string{"other/0.4.12"}, wantErr: service.ErrClientVersionInvalid},
		{name: "leading whitespace", values: []string{" bilibili-live-gift-panel/0.4.12"}, wantErr: service.ErrClientVersionInvalid},
		{name: "trailing whitespace", values: []string{"bilibili-live-gift-panel/0.4.12 "}, wantErr: service.ErrClientVersionInvalid},
		{name: "oversized", values: []string{"bilibili-live-gift-panel/" + strings.Repeat("9", 1024)}, wantErr: service.ErrClientVersionInvalid},
		{name: "prerelease", values: []string{"bilibili-live-gift-panel/0.4.12-rc.1"}, wantErr: service.ErrClientVersionInvalid},
		{name: "development", values: []string{"bilibili-live-gift-panel/dev"}, wantErr: service.ErrClientVersionInvalid},
		{name: "unreviewed gap", values: []string{"bilibili-live-gift-panel/0.4.8"}, wantErr: service.ErrClientVersionInvalid},
		{name: "later unreviewed", values: []string{"bilibili-live-gift-panel/0.4.13"}, wantErr: service.ErrClientVersionInvalid},
		{name: "unknown major", values: []string{"bilibili-live-gift-panel/1.0.0"}, wantErr: service.ErrClientVersionInvalid},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callbackCalls := 0
			router := service.ChannelRouter{LegacyActive: func(context.Context) (bool, error) {
				callbackCalls++
				return test.legacyActive, test.legacyErr
			}}
			got, err := router.Select(context.Background(), test.values)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Select() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("Select() = %q, want %q", got, test.want)
			}
			wantCalls := 0
			if len(test.values) == 1 && test.values[0] == "bilibili-live-gift-panel/0.4.7" {
				wantCalls = 1
			}
			if callbackCalls != wantCalls {
				t.Fatalf("LegacyActive calls = %d, want %d", callbackCalls, wantCalls)
			}
			if err != nil && strings.Contains(err.Error(), "private activation backend") {
				t.Fatalf("Select() leaked activation detail: %v", err)
			}
		})
	}
}
