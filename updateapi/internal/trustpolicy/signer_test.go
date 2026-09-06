package trustpolicy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignPolicyUsesSHA256DigestAndProducesClientEnvelope(t *testing.T) {
	candidate := mustParseCandidate(t)
	fake := newFakeSigner(t)
	now := time.Date(2029, 1, 2, 3, 4, 5, 0, time.UTC)
	output, audit, err := Sign(context.Background(), fake, candidate, SignOptions{
		KeyID:                 "publisher-root-v1",
		ExpectedPreviousEpoch: 0,
		ExpectedSPKISHA256:    fake.SPKISHA256,
		Now:                   now,
		CIActor:               "release-approver",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(output.CanonicalSigned)
	if !bytes.Equal(fake.Digest, digest[:]) {
		t.Fatalf("signer digest = %x, want SHA-256 %x", fake.Digest, digest)
	}
	if fake.PublicKeyID != "publisher-root-v1" || fake.SignKeyID != "publisher-root-v1" {
		t.Fatalf("signer key IDs = %q/%q, want reviewed key ID", fake.PublicKeyID, fake.SignKeyID)
	}
	if !bytes.Contains(output.Policy, []byte(`"algorithm":"ecdsa-p256-sha256"`)) || !bytes.Contains(output.Policy, []byte(`"signature":`)) {
		t.Fatalf("output is not Task 1 client envelope: %s", output.Policy)
	}
	if bytes.Contains(output.Policy, []byte(`"keyId"`)) || bytes.Contains(output.Policy, []byte(`"value"`)) || bytes.Contains(output.Policy, []byte(`ECDSA_P256_SHA256`)) {
		t.Fatalf("output silently adopted incompatible illustrative schema: %s", output.Policy)
	}
	policyDigest := sha256.Sum256(output.Policy)
	if audit.KeyID != "publisher-root-v1" || audit.Epoch != 1 || audit.PolicySHA256 != hex.EncodeToString(policyDigest[:]) || audit.RequestID != fake.SignRequestID || audit.UTC != "2029-01-02T03:04:05Z" || audit.CIActor != "release-approver" {
		t.Fatalf("audit = %+v, want only reviewed non-secret fields", audit)
	}
}

func TestSignPolicyRejectsSPKIAndSignatureMismatches(t *testing.T) {
	candidate := mustParseCandidate(t)
	tests := []struct {
		name   string
		mutate func(*fakeSigner)
		opts   func(*fakeSigner) SignOptions
	}{
		{
			name: "expected SPKI digest mismatch",
			opts: func(fake *fakeSigner) SignOptions { return validSignOptions(fake, strings.Repeat("0", 64)) },
		},
		{
			name: "non P256 SPKI",
			mutate: func(fake *fakeSigner) {
				key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				fake.PublicDER, err = x509.MarshalPKIXPublicKey(&key.PublicKey)
				if err != nil {
					t.Fatal(err)
				}
				got := sha256.Sum256(fake.PublicDER)
				fake.SPKISHA256 = hex.EncodeToString(got[:])
			},
			opts: func(fake *fakeSigner) SignOptions { return validSignOptions(fake, fake.SPKISHA256) },
		},
		{
			name:   "malformed SPKI DER",
			mutate: func(fake *fakeSigner) { fake.PublicDER = []byte("not DER") },
			opts: func(fake *fakeSigner) SignOptions {
				digest := sha256.Sum256(fake.PublicDER)
				return validSignOptions(fake, hex.EncodeToString(digest[:]))
			},
		},
		{
			name: "signature from other key",
			mutate: func(fake *fakeSigner) {
				other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				fake.SigningKey = other
			},
			opts: func(fake *fakeSigner) SignOptions { return validSignOptions(fake, fake.SPKISHA256) },
		},
		{
			name:   "malformed DER signature",
			mutate: func(fake *fakeSigner) { fake.Signature = []byte("not DER") },
			opts:   func(fake *fakeSigner) SignOptions { return validSignOptions(fake, fake.SPKISHA256) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeSigner(t)
			if test.mutate != nil {
				test.mutate(fake)
			}
			if _, _, err := Sign(context.Background(), fake, candidate, test.opts(fake)); err == nil {
				t.Fatal("Sign() error = nil, want rejection")
			}
		})
	}
}

func TestSignPolicyRedactsSignerAndCandidateDetails(t *testing.T) {
	candidate := mustParseCandidate(t)
	fake := newFakeSigner(t)
	fake.Err = errors.New("credential-secret candidate-contents raw-public-key signature-value")
	_, _, err := Sign(context.Background(), fake, candidate, validSignOptions(fake, fake.SPKISHA256))
	if err == nil {
		t.Fatal("Sign() error = nil, want signer failure")
	}
	for _, secret := range []string{"credential-secret", "candidate-contents", "raw-public-key", "signature-value", "NaisNet"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %q", secret, err)
		}
	}
}

func TestSignPolicyRejectsUnsafeReviewedInputsBeforeSigner(t *testing.T) {
	candidate := mustParseCandidate(t)
	tests := []struct {
		name   string
		mutate func(*SignOptions)
	}{
		{name: "blank key ID", mutate: func(options *SignOptions) { options.KeyID = "" }},
		{name: "key ID whitespace", mutate: func(options *SignOptions) { options.KeyID = " publisher-root-v1" }},
		{name: "uppercase SPKI digest", mutate: func(options *SignOptions) { options.ExpectedSPKISHA256 = strings.ToUpper(options.ExpectedSPKISHA256) }},
		{name: "malformed SPKI digest", mutate: func(options *SignOptions) { options.ExpectedSPKISHA256 = "abc" }},
		{name: "blank CI actor", mutate: func(options *SignOptions) { options.CIActor = "" }},
		{name: "unsafe CI actor", mutate: func(options *SignOptions) { options.CIActor = "actor\nsecret" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeSigner(t)
			options := validSignOptions(fake, fake.SPKISHA256)
			test.mutate(&options)
			if _, _, err := Sign(context.Background(), fake, candidate, options); err == nil {
				t.Fatal("Sign() error = nil, want reviewed input rejection")
			}
			if fake.PublicKeyID != "" || fake.SignKeyID != "" {
				t.Fatal("Sign() called signer before validating reviewed inputs")
			}
		})
	}
}

func TestSignPolicyAllowsGitHubBotActor(t *testing.T) {
	candidate := mustParseCandidate(t)
	fake := newFakeSigner(t)
	options := validSignOptions(fake, fake.SPKISHA256)
	options.CIActor = "github-actions[bot]"
	_, audit, err := Sign(context.Background(), fake, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if audit.CIActor != "github-actions[bot]" {
		t.Fatalf("CI actor = %q", audit.CIActor)
	}
}

func TestSignPolicyRejectsCompleteEnvelopeAboveClientMaximum(t *testing.T) {
	candidate := mustParseCandidate(t)
	candidate.Publishers = candidate.Publishers[:1]
	candidate.Publishers[0].AllowedTags = []string{"v1.0.0+a"}
	base, err := CanonicalSigned(candidate)
	if err != nil {
		t.Fatal(err)
	}
	padding := maxCandidateBytes - len(base) - 32
	if padding <= 0 {
		t.Fatalf("test candidate base unexpectedly large: %d", len(base))
	}
	candidate.Publishers[0].AllowedTags[0] += strings.Repeat("a", padding)
	canonical, err := CanonicalSigned(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != maxCandidateBytes-32 {
		t.Fatalf("canonical signed size = %d, want %d", len(canonical), maxCandidateBytes-32)
	}
	parsed, err := ParseCandidate(canonical, CandidateOptions{ExpectedPreviousEpoch: 0, Now: candidateValidationTime})
	if err != nil {
		t.Fatalf("near-boundary signed object was rejected before envelope construction: %v", err)
	}
	fake := newFakeSigner(t)
	output, audit, err := Sign(context.Background(), fake, parsed, validSignOptions(fake, fake.SPKISHA256))
	if err == nil {
		if len(output.Policy) <= maxCandidateBytes {
			t.Fatalf("test did not cross complete-envelope boundary: signed=%d envelope=%d", len(canonical), len(output.Policy))
		}
		t.Fatalf("Sign() accepted complete envelope of %d bytes above client maximum", len(output.Policy))
	}
	if len(output.Policy) != 0 || len(output.CanonicalSigned) != 0 || audit != (Audit{}) {
		t.Fatalf("oversized envelope returned output or audit: output=%d/%d audit=%+v", len(output.Policy), len(output.CanonicalSigned), audit)
	}
}

func validSignOptions(fake *fakeSigner, expectedDigest string) SignOptions {
	return SignOptions{
		KeyID:                 "publisher-root-v1",
		ExpectedPreviousEpoch: 0,
		ExpectedSPKISHA256:    expectedDigest,
		Now:                   candidateValidationTime,
		CIActor:               "release-approver",
	}
}

func mustParseCandidate(t *testing.T) Candidate {
	t.Helper()
	candidate, err := ParseCandidate(readCandidateFixture(t), CandidateOptions{ExpectedPreviousEpoch: 0, Now: candidateValidationTime})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

type fakeSigner struct {
	PublicDER       []byte
	SPKISHA256      string
	SigningKey      *ecdsa.PrivateKey
	Signature       []byte
	Digest          []byte
	PublicKeyID     string
	SignKeyID       string
	PublicRequestID string
	SignRequestID   string
	Err             error
}

func newFakeSigner(t testing.TB) *fakeSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(der)
	return &fakeSigner{
		PublicDER: der, SPKISHA256: hex.EncodeToString(digest[:]), SigningKey: key,
		PublicRequestID: "public-request-id", SignRequestID: "sign-request-id",
	}
}

func (fake *fakeSigner) PublicKey(_ context.Context, keyID string) ([]byte, string, error) {
	fake.PublicKeyID = keyID
	if fake.Err != nil {
		return nil, "", fake.Err
	}
	return append([]byte(nil), fake.PublicDER...), fake.PublicRequestID, nil
}

func (fake *fakeSigner) SignDigest(_ context.Context, keyID string, digest []byte) ([]byte, string, error) {
	fake.SignKeyID = keyID
	fake.Digest = append([]byte(nil), digest...)
	if fake.Err != nil {
		return nil, "", fake.Err
	}
	if fake.Signature != nil {
		return append([]byte(nil), fake.Signature...), fake.SignRequestID, nil
	}
	signature, err := ecdsa.SignASN1(rand.Reader, fake.SigningKey, digest)
	if err != nil {
		return nil, "", err
	}
	return signature, fake.SignRequestID, nil
}
