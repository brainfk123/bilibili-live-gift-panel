package main

import (
	"io"
	"log"
	"net/http"
	"testing"
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

func TestLegacyChannelRemainsInactiveByDefault(t *testing.T) {
	t.Setenv("UPDATE_LEGACY_RUSHRUSH_ACTIVE", "true")
	active, err := legacyChannelActive(nil)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("legacy channel activated before the deployment task")
	}
}

func TestLoadConfigIgnoresArbitraryChannelKey(t *testing.T) {
	t.Setenv("UPDATE_API_LISTEN", "127.0.0.1:12450")
	t.Setenv("COS_BUCKET", "private-release-1250000000")
	t.Setenv("COS_REGION", "ap-shanghai")
	t.Setenv("COS_SECRET_ID", "secret-id")
	t.Setenv("COS_SECRET_KEY", "secret-key")
	t.Setenv("COS_CHANNEL_KEY", "channels/beta/latest.json")

	configuration, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.bucket != "private-release-1250000000" {
		t.Fatalf("bucket = %q, want configured bucket", configuration.bucket)
	}
}
