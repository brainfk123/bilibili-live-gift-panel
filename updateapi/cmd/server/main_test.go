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
