package main

import "testing"

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
