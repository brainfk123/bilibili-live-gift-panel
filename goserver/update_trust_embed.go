package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
)

var updateTrustRootSPKIBase64 string
var updateTrustBootstrapPolicyBase64 string

func embeddedUpdateTrust() (*ecdsa.PublicKey, []byte, error) {
	spkiDER, err := base64.StdEncoding.DecodeString(updateTrustRootSPKIBase64)
	if err != nil {
		return nil, nil, fmt.Errorf("decode update trust root: %w", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(spkiDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse update trust root: %w", err)
	}
	root, ok := parsed.(*ecdsa.PublicKey)
	if !ok || root.Curve != elliptic.P256() {
		return nil, nil, errors.New("update trust root must be ECDSA P-256")
	}
	policy, err := base64.StdEncoding.DecodeString(updateTrustBootstrapPolicyBase64)
	if err != nil {
		return nil, nil, fmt.Errorf("decode update trust bootstrap policy: %w", err)
	}
	if len(policy) == 0 {
		return nil, nil, errors.New("update trust bootstrap policy is empty")
	}
	return root, policy, nil
}
