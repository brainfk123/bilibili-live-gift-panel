package trustpolicy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"strings"
)

// PrivateKeySigner signs publisher-policy digests with one reviewed PKCS#8
// P-256 key supplied by the protected release environment.
type PrivateKeySigner struct {
	privateKey *ecdsa.PrivateKey
	publicDER  []byte
	requestID  string
}

// NewPrivateKeySigner accepts exactly one unencrypted PKCS#8 P-256 private key.
// The corresponding SPKI must match the independently reviewed digest.
func NewPrivateKeySigner(privateKeyPEM []byte, expectedSPKISHA256, requestIDValue string) (Signer, error) {
	if len(privateKeyPEM) == 0 || !sha256Hex.MatchString(expectedSPKISHA256) || !requestID.MatchString(requestIDValue) {
		return nil, errReviewedInput
	}
	block, remainder := pem.Decode(privateKeyPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || strings.TrimSpace(string(remainder)) != "" {
		return nil, errReviewedInput
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errReviewedInput
	}
	privateKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || privateKey.Curve != elliptic.P256() || privateKey.D == nil || privateKey.D.Sign() <= 0 ||
		privateKey.X == nil || privateKey.Y == nil || !privateKey.Curve.IsOnCurve(privateKey.X, privateKey.Y) {
		return nil, errReviewedInput
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, errReviewedInput
	}
	if _, err := parseReviewedPublicKey(publicDER, expectedSPKISHA256); err != nil {
		return nil, errReviewedInput
	}
	return &PrivateKeySigner{
		privateKey: privateKey,
		publicDER:  append([]byte(nil), publicDER...),
		requestID:  requestIDValue,
	}, nil
}

func (signer *PrivateKeySigner) PublicKey(ctx context.Context, keyID string) ([]byte, string, error) {
	if signer == nil || signer.privateKey == nil || ctx == nil || ctx.Err() != nil || !keyIDValue.MatchString(keyID) {
		return nil, "", errPublicKey
	}
	return append([]byte(nil), signer.publicDER...), signer.requestID, nil
}

func (signer *PrivateKeySigner) SignDigest(ctx context.Context, keyID string, digest []byte) ([]byte, string, error) {
	if signer == nil || signer.privateKey == nil || ctx == nil || ctx.Err() != nil || !keyIDValue.MatchString(keyID) || len(digest) != sha256.Size {
		return nil, "", errSigning
	}
	signature, err := ecdsa.SignASN1(rand.Reader, signer.privateKey, digest)
	if err != nil {
		return nil, "", errSigning
	}
	return signature, signer.requestID, nil
}
