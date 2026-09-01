package trustpolicy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"
)

func TestPrivateKeySignerSignsDigestWithReviewedPKCS8P256Key(t *testing.T) {
	privateKey, privatePEM, publicDER, publicDigest := privateKeyFixture(t, elliptic.P256())
	signer, err := NewPrivateKeySigner(privatePEM, publicDigest, "github-run:1234:attempt:2")
	if err != nil {
		t.Fatal(err)
	}

	gotPublicDER, requestID, err := signer.PublicKey(context.Background(), "publisher-root-v1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPublicDER, publicDER) || requestID != "github-run:1234:attempt:2" {
		t.Fatalf("public key result = %x/%q, want reviewed DER and run request ID", gotPublicDER, requestID)
	}

	digest := sha256.Sum256([]byte("canonical publisher policy"))
	signature, requestID, err := signer.SignDigest(context.Background(), "publisher-root-v1", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "github-run:1234:attempt:2" || !ecdsa.VerifyASN1(&privateKey.PublicKey, digest[:], signature) {
		t.Fatal("private-key signer returned the wrong request ID or an unverifiable signature")
	}
	if _, _, err := signer.SignDigest(context.Background(), "publisher-root-v1", []byte("raw message")); err == nil {
		t.Fatal("private-key signer accepted non-SHA-256 input")
	}
}

func TestPrivateKeySignerRejectsUnreviewedOrAmbiguousPrivateKeys(t *testing.T) {
	_, validPEM, _, validDigest := privateKeyFixture(t, elliptic.P256())
	_, p384PEM, _, p384Digest := privateKeyFixture(t, elliptic.P384())
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER})

	tests := []struct {
		name       string
		privatePEM []byte
		digest     string
		requestID  string
	}{
		{name: "empty", digest: validDigest, requestID: "github-run:1"},
		{name: "malformed PEM", privatePEM: []byte("not a private key"), digest: validDigest, requestID: "github-run:1"},
		{name: "SEC1 PEM type", privatePEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1}}), digest: validDigest, requestID: "github-run:1"},
		{name: "multiple PEM blocks", privatePEM: append(append([]byte(nil), validPEM...), validPEM...), digest: validDigest, requestID: "github-run:1"},
		{name: "RSA PKCS8", privatePEM: rsaPEM, digest: validDigest, requestID: "github-run:1"},
		{name: "P384 PKCS8", privatePEM: p384PEM, digest: p384Digest, requestID: "github-run:1"},
		{name: "SPKI mismatch", privatePEM: validPEM, digest: strings.Repeat("0", 64), requestID: "github-run:1"},
		{name: "unsafe request ID", privatePEM: validPEM, digest: validDigest, requestID: "github run secret\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if signer, err := NewPrivateKeySigner(test.privatePEM, test.digest, test.requestID); err == nil || signer != nil {
				t.Fatalf("NewPrivateKeySigner() = (%T, %v), want strict rejection", signer, err)
			}
		})
	}
}

func privateKeyFixture(t testing.TB, curve elliptic.Curve) (*ecdsa.PrivateKey, []byte, []byte, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(publicDER)
	return privateKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), publicDER, hex.EncodeToString(digest[:])
}
