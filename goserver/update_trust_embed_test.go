package main

import (
	"bytes"
	"crypto/elliptic"
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
