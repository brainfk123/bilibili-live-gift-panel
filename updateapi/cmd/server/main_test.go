package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

func TestValidateLoopbackAddressRejectsNonLoopbackHosts(t *testing.T) {
	for _, address := range []string{"0.0.0.0:12450", "192.0.2.1:12450", "localhost:12450", "127.0.0.1", "[::1]"} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Fatalf("validateLoopbackAddress(%q) error = nil, want rejection", address)
		}
	}
}

func TestValidateLoopbackAddressAcceptsIPv4AndIPv6Loopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:12450", "[::1]:12450"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Fatalf("validateLoopbackAddress(%q) = %v, want nil", address, err)
		}
	}
}

func TestServeReturnsNonzeroWhenListenFails(t *testing.T) {
	server := &http.Server{Addr: "not-a-listen-address"}
	if got := serve(server, nil, log.New(io.Discard, "", 0)); got == 0 {
		t.Fatal("serve() = 0, want non-zero for ListenAndServe failure")
	}
}

func TestLegacyRoutingConfigurationControlsRouterIndependently(t *testing.T) {
	setRequiredServerEnvironment(t)
	t.Setenv("UPDATE_LEGACY_ROUTING_ACTIVE", "false")
	configuration, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newChannelRouter(configuration.routing).Select(context.Background(), []string{"bilibili-live-gift-panel/0.4.7"}); err != service.ErrLegacyChannelUnavailable {
		t.Fatalf("inactive v0.4.7 route error = %v, want controlled unavailable", err)
	}

	t.Setenv("UPDATE_LEGACY_ROUTING_ACTIVE", "true")
	configuration, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	channel, err := newChannelRouter(configuration.routing).Select(context.Background(), []string{"bilibili-live-gift-panel/0.4.7"})
	if err != nil || channel != release.ChannelLegacyRushRush {
		t.Fatalf("active v0.4.7 route = %q, %v; want legacy-rushrush", channel, err)
	}
}

func TestLoadConfigIgnoresArbitraryChannelKey(t *testing.T) {
	setRequiredServerEnvironment(t)
	t.Setenv("COS_CHANNEL_KEY", "channels/beta/latest.json")

	configuration, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.bucket != "private-release-1250000000" {
		t.Fatalf("bucket = %q, want configured bucket", configuration.bucket)
	}
}

func TestLoadConfigRejectsInvalidClosedRoutingValue(t *testing.T) {
	setRequiredServerEnvironment(t)
	t.Setenv("UPDATE_STABLE_CHANNEL_KEY", "channels/preview/latest.json")
	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil, want invalid routing configuration rejected")
	}
}

func setRequiredServerEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("UPDATE_API_LISTEN", "127.0.0.1:12450")
	t.Setenv("COS_BUCKET", "private-release-1250000000")
	t.Setenv("COS_REGION", "ap-shanghai")
	t.Setenv("COS_SECRET_ID", "secret-id")
	t.Setenv("COS_SECRET_KEY", "secret-key")
	for _, name := range []string{
		"UPDATE_STABLE_CHANNEL_KEY",
		"UPDATE_LEGACY_CHANNEL_KEY",
		"UPDATE_LEGACY_ROUTING_ACTIVE",
		"UPDATE_PUBLISHER_POLICY_KEY",
	} {
		value, present := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(name, value)
				return
			}
			_ = os.Unsetenv(name)
		})
	}
}
