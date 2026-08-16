package security

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestNewKeyringValidatesAndCopiesKeys(t *testing.T) {
	tests := []struct {
		name    string
		aeadKey []byte
		hmacKey []byte
	}{
		{name: "short AEAD key", aeadKey: bytes.Repeat([]byte{1}, 31), hmacKey: bytes.Repeat([]byte{2}, 32)},
		{name: "long AEAD key", aeadKey: bytes.Repeat([]byte{1}, 33), hmacKey: bytes.Repeat([]byte{2}, 32)},
		{name: "short HMAC key", aeadKey: bytes.Repeat([]byte{1}, 32), hmacKey: bytes.Repeat([]byte{2}, 31)},
		{name: "long HMAC key", aeadKey: bytes.Repeat([]byte{1}, 32), hmacKey: bytes.Repeat([]byte{2}, 33)},
		{name: "reused key material", aeadKey: bytes.Repeat([]byte{1}, 32), hmacKey: bytes.Repeat([]byte{1}, 32)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewKeyring(1, test.aeadKey, test.hmacKey); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("NewKeyring() error = %v, want ErrInvalidInput", err)
			}
		})
	}

	aeadKey := bytes.Repeat([]byte{1}, 32)
	hmacKey := bytes.Repeat([]byte{2}, 32)
	keys, err := NewKeyring(1, aeadKey, hmacKey)
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	aeadKey[0] = 9
	hmacKey[0] = 9

	sealed, err := keys.Seal("bili_uid", []byte("123456"))
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	opened, err := keys.Open("bili_uid", sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if string(opened) != "123456" {
		t.Fatalf("Open() = %q, want original plaintext after caller key mutation", opened)
	}
}

func TestZeroKeyringCannotUseUnvalidatedKeyMaterial(t *testing.T) {
	var keys Keyring
	if _, err := keys.Seal("bili_uid", []byte("123456")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero Keyring Seal() error = %v, want ErrInvalidInput", err)
	}
	if _, err := keys.Lookup("bili_uid", []byte("123456")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero Keyring Lookup() error = %v, want ErrInvalidInput", err)
	}
}

func TestKeyringSealRoundTripsWithFreshNonceAndPurposeBinding(t *testing.T) {
	keys := fixedKeyring(t)
	plaintext := []byte("123456")

	first, err := keys.Seal("bili_uid", plaintext)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	second, err := keys.Seal("bili_uid", plaintext)
	if err != nil {
		t.Fatalf("Seal() second error = %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("Seal() reused nonce: identical inputs produced identical ciphertext")
	}
	if len(first) == 0 || first[0] != 7 {
		t.Fatalf("Seal() version prefix = %v, want 7", first)
	}
	if bytes.Contains(first, plaintext) {
		t.Fatal("Seal() ciphertext contains plaintext")
	}

	opened, err := keys.Open("bili_uid", first)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q, want %q", opened, plaintext)
	}
	if _, err := keys.Open("admin_totp", first); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("Open() wrong-purpose error = %v, want ErrAuthentication", err)
	}

	unknown := append([]byte(nil), first...)
	unknown[0] = 8
	if _, err := keys.Open("bili_uid", unknown); !errors.Is(err, ErrUnknownKeyVersion) {
		t.Fatalf("Open() unknown-version error = %v, want ErrUnknownKeyVersion", err)
	}
}

func TestKeyringRejectsInvalidInputsWithoutExposingSecrets(t *testing.T) {
	keys := fixedKeyring(t)
	secret := "plaintext-that-must-not-leak"

	if _, err := keys.Seal("", []byte(secret)); !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), secret) {
		t.Fatalf("Seal() error = %q, want safe ErrInvalidInput", err)
	}
	if _, err := keys.Open("bili_uid", []byte{7}); !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), secret) {
		t.Fatalf("Open() short-ciphertext error = %q, want safe ErrInvalidInput", err)
	}
	sealed, err := keys.Seal("bili_uid", []byte(secret))
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := keys.Open("bili_uid", sealed); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), secret) {
		t.Fatalf("Open() authentication error = %q, want safe ErrAuthentication", err)
	}
	if _, err := keys.Lookup("", []byte(secret)); !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), secret) {
		t.Fatalf("Lookup() error = %q, want safe ErrInvalidInput", err)
	}
	if _, err := keys.HashToken("", []byte(secret)); !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), secret) {
		t.Fatalf("HashToken() error = %q, want safe ErrInvalidInput", err)
	}
}

func TestKeyringLookupAndTokenHashArePurposeSeparated(t *testing.T) {
	keys := fixedKeyring(t)
	value := []byte("same-value")

	uidLookup, err := keys.Lookup("bili_uid", value)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	uidLookupAgain, err := keys.Lookup("bili_uid", value)
	if err != nil {
		t.Fatalf("Lookup() second error = %v", err)
	}
	totpLookup, err := keys.Lookup("admin_totp", value)
	if err != nil {
		t.Fatalf("Lookup() different purpose error = %v", err)
	}
	if len(uidLookup) != 32 || !bytes.Equal(uidLookup, uidLookupAgain) {
		t.Fatalf("Lookup() must return a deterministic 32-byte digest")
	}
	if bytes.Equal(uidLookup, totpLookup) {
		t.Fatal("Lookup() silently shared a domain across purposes")
	}

	siteHash, err := keys.HashToken("site_session", value)
	if err != nil {
		t.Fatalf("HashToken() error = %v", err)
	}
	inviteHash, err := keys.HashToken("invitation", value)
	if err != nil {
		t.Fatalf("HashToken() different purpose error = %v", err)
	}
	if len(siteHash) != 32 || bytes.Equal(siteHash, inviteHash) {
		t.Fatal("HashToken() must return purpose-separated 32-byte digests")
	}
	if bytes.Equal(siteHash, uidLookup) {
		t.Fatal("HashToken() and Lookup() silently shared an operation domain")
	}
}

func TestKeyringNewTokenUsesThirtyTwoRandomBytesAndRawURLAlphabet(t *testing.T) {
	keys := fixedKeyring(t)
	first, err := keys.NewToken()
	if err != nil {
		t.Fatalf("NewToken() error = %v", err)
	}
	second, err := keys.NewToken()
	if err != nil {
		t.Fatalf("NewToken() second error = %v", err)
	}
	if first == second {
		t.Fatal("NewToken() repeated a token")
	}
	if strings.Contains(first, "=") {
		t.Fatalf("NewToken() = %q, want unpadded base64url", first)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("NewToken() returned invalid raw base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("NewToken() decoded length = %d, want 32", len(decoded))
	}
}

func fixedKeyring(t *testing.T) Keyring {
	t.Helper()
	keys, err := NewKeyring(7, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	return keys
}
