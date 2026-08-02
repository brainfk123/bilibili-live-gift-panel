//go:build windows

package main

import (
	"bytes"
	"testing"
)

func TestWindowsLoginCredentialProtectionRoundTrip(t *testing.T) {
	plain := []byte(`{"SESSDATA":"secret-session"}`)
	encrypted, err := protectLoginCredentialData(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("secret-session")) {
		t.Fatal("DPAPI output contains the plaintext credential")
	}
	decrypted, err := unprotectLoginCredentialData(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plain) {
		t.Fatalf("decrypted data = %q", decrypted)
	}
}
