package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
)

func TestEmbeddedUpdateTrust(t *testing.T) {
	originalRoot, originalPolicy := updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64
	t.Cleanup(func() {
		updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64 = originalRoot, originalPolicy
	})

	rootDER := readFixture(t, "root-epoch-1-spki.der")
	policy := readFixture(t, "policy-epoch-1.json")
	updateTrustRootSPKIBase64 = base64.StdEncoding.EncodeToString(rootDER)
	updateTrustBootstrapPolicyBase64 = base64.StdEncoding.EncodeToString(policy)

	root, gotPolicy, err := embeddedUpdateTrust()
	if err != nil {
		t.Fatalf("embeddedUpdateTrust() error = %v", err)
	}
	if root.Curve != elliptic.P256() || !root.Curve.IsOnCurve(root.X, root.Y) {
		t.Fatal("embeddedUpdateTrust() did not return the test-only P-256 root")
	}
	if !bytes.Equal(gotPolicy, policy) {
		t.Fatal("embeddedUpdateTrust() bootstrap policy differs from the encoded fixture")
	}
}

func TestEmbeddedUpdateTrustRejectsInvalidInputs(t *testing.T) {
	originalRoot, originalPolicy := updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64
	t.Cleanup(func() {
		updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64 = originalRoot, originalPolicy
	})

	updateTrustRootSPKIBase64 = "not base64"
	updateTrustBootstrapPolicyBase64 = base64.StdEncoding.EncodeToString(readFixture(t, "policy-epoch-1.json"))
	if _, _, err := embeddedUpdateTrust(); err == nil || !strings.Contains(err.Error(), "decode update trust root") {
		t.Fatalf("invalid root error = %v, want decode update trust root", err)
	}

	updateTrustRootSPKIBase64 = base64.StdEncoding.EncodeToString(readFixture(t, "root-epoch-1-spki.der"))
	updateTrustBootstrapPolicyBase64 = "not base64"
	if _, _, err := embeddedUpdateTrust(); err == nil || !strings.Contains(err.Error(), "decode update trust bootstrap policy") {
		t.Fatalf("invalid bootstrap policy error = %v, want decode update trust bootstrap policy", err)
	}
}

func TestEmbeddedUpdateTrustRejectsNonP256Roots(t *testing.T) {
	originalRoot, originalPolicy := updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64
	t.Cleanup(func() {
		updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64 = originalRoot, originalPolicy
	})
	updateTrustBootstrapPolicyBase64 = base64.StdEncoding.EncodeToString(readFixture(t, "policy-epoch-1.json"))

	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-384 key: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	for name, public := range map[string]any{
		"ECDSA P-384": &p384.PublicKey,
		"RSA":         &rsaKey.PublicKey,
	} {
		t.Run(name, func(t *testing.T) {
			der, err := x509.MarshalPKIXPublicKey(public)
			if err != nil {
				t.Fatalf("marshal test SPKI: %v", err)
			}
			updateTrustRootSPKIBase64 = base64.StdEncoding.EncodeToString(der)
			if _, _, err := embeddedUpdateTrust(); err == nil || !strings.Contains(err.Error(), "update trust root must be ECDSA P-256") {
				t.Fatalf("non-P-256 root error = %v, want ECDSA P-256 rejection", err)
			}
		})
	}
}

func TestEmbeddedUpdateTrustRejectsEmptyBootstrapPolicy(t *testing.T) {
	originalRoot, originalPolicy := updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64
	t.Cleanup(func() {
		updateTrustRootSPKIBase64, updateTrustBootstrapPolicyBase64 = originalRoot, originalPolicy
	})

	updateTrustRootSPKIBase64 = base64.StdEncoding.EncodeToString(readFixture(t, "root-epoch-1-spki.der"))
	updateTrustBootstrapPolicyBase64 = ""
	if _, _, err := embeddedUpdateTrust(); err == nil || !strings.Contains(err.Error(), "update trust bootstrap policy is empty") {
		t.Fatalf("empty bootstrap policy error = %v, want empty policy rejection", err)
	}
}
