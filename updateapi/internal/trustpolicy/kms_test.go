package trustpolicy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	kms "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/kms/v20190118"
)

func TestSignPolicyUsesSHA256DigestAndProducesClientEnvelope(t *testing.T) {
	candidate := mustParseCandidate(t)
	fake := newFakeKMSSigner(t)
	now := time.Date(2029, 1, 2, 3, 4, 5, 0, time.UTC)
	output, audit, err := Sign(context.Background(), fake, candidate, SignOptions{
		KeyID:                 "kms-key-id",
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
		t.Fatalf("KMS digest = %x, want SHA-256 %x", fake.Digest, digest)
	}
	if fake.PublicKeyID != "kms-key-id" || fake.SignKeyID != "kms-key-id" {
		t.Fatalf("KMS key IDs = %q/%q, want reviewed key ID", fake.PublicKeyID, fake.SignKeyID)
	}
	if !bytes.Contains(output.Policy, []byte(`"algorithm":"ecdsa-p256-sha256"`)) || !bytes.Contains(output.Policy, []byte(`"signature":`)) {
		t.Fatalf("output is not Task 1 client envelope: %s", output.Policy)
	}
	if bytes.Contains(output.Policy, []byte(`"keyId"`)) || bytes.Contains(output.Policy, []byte(`"value"`)) || bytes.Contains(output.Policy, []byte(`ECDSA_P256_SHA256`)) {
		t.Fatalf("output silently adopted incompatible illustrative schema: %s", output.Policy)
	}
	policyDigest := sha256.Sum256(output.Policy)
	if audit.KeyID != "kms-key-id" || audit.Epoch != 1 || audit.PolicySHA256 != hex.EncodeToString(policyDigest[:]) || audit.RequestID != fake.SignRequestID || audit.UTC != "2029-01-02T03:04:05Z" || audit.CIActor != "release-approver" {
		t.Fatalf("audit = %+v, want only reviewed non-secret fields", audit)
	}
}

func TestSignPolicyRejectsSPKIAndSignatureMismatches(t *testing.T) {
	candidate := mustParseCandidate(t)
	tests := []struct {
		name   string
		mutate func(*fakeKMSSigner)
		opts   func(*fakeKMSSigner) SignOptions
	}{
		{
			name: "expected SPKI digest mismatch",
			opts: func(fake *fakeKMSSigner) SignOptions { return validSignOptions(fake, strings.Repeat("0", 64)) },
		},
		{
			name: "non P256 SPKI",
			mutate: func(fake *fakeKMSSigner) {
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
			opts: func(fake *fakeKMSSigner) SignOptions { return validSignOptions(fake, fake.SPKISHA256) },
		},
		{
			name:   "malformed SPKI DER",
			mutate: func(fake *fakeKMSSigner) { fake.PublicDER = []byte("not DER") },
			opts: func(fake *fakeKMSSigner) SignOptions {
				digest := sha256.Sum256(fake.PublicDER)
				return validSignOptions(fake, hex.EncodeToString(digest[:]))
			},
		},
		{
			name: "signature from other key",
			mutate: func(fake *fakeKMSSigner) {
				other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				fake.SigningKey = other
			},
			opts: func(fake *fakeKMSSigner) SignOptions { return validSignOptions(fake, fake.SPKISHA256) },
		},
		{
			name:   "malformed DER signature",
			mutate: func(fake *fakeKMSSigner) { fake.Signature = []byte("not DER") },
			opts:   func(fake *fakeKMSSigner) SignOptions { return validSignOptions(fake, fake.SPKISHA256) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeKMSSigner(t)
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
	fake := newFakeKMSSigner(t)
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

func TestSignPolicyRejectsUnsafeReviewedInputsBeforeKMS(t *testing.T) {
	candidate := mustParseCandidate(t)
	tests := []struct {
		name   string
		mutate func(*SignOptions)
	}{
		{name: "blank key ID", mutate: func(options *SignOptions) { options.KeyID = "" }},
		{name: "key ID whitespace", mutate: func(options *SignOptions) { options.KeyID = " kms-key-id" }},
		{name: "uppercase SPKI digest", mutate: func(options *SignOptions) { options.ExpectedSPKISHA256 = strings.ToUpper(options.ExpectedSPKISHA256) }},
		{name: "malformed SPKI digest", mutate: func(options *SignOptions) { options.ExpectedSPKISHA256 = "abc" }},
		{name: "blank CI actor", mutate: func(options *SignOptions) { options.CIActor = "" }},
		{name: "unsafe CI actor", mutate: func(options *SignOptions) { options.CIActor = "actor\nsecret" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeKMSSigner(t)
			options := validSignOptions(fake, fake.SPKISHA256)
			test.mutate(&options)
			if _, _, err := Sign(context.Background(), fake, candidate, options); err == nil {
				t.Fatal("Sign() error = nil, want reviewed input rejection")
			}
			if fake.PublicKeyID != "" || fake.SignKeyID != "" {
				t.Fatal("Sign() called KMS before validating reviewed inputs")
			}
		})
	}
}

func TestSignPolicyAllowsGitHubBotActor(t *testing.T) {
	candidate := mustParseCandidate(t)
	fake := newFakeKMSSigner(t)
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
	fake := newFakeKMSSigner(t)
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

func TestKMSSignerUsesReviewedGetPublicKeyAndDigestRequests(t *testing.T) {
	private, der, expectedDigest := kmsTestKey(t, elliptic.P256())
	client := &fakeKMSClient{
		publicResponse: publicKeyResponse("kms-key-id", der, "public-request-id"),
		sign: func(request *kms.SignByAsymmetricKeyRequest) (*kms.SignByAsymmetricKeyResponse, error) {
			if request.KeyId == nil || *request.KeyId != "kms-key-id" || request.Algorithm == nil || *request.Algorithm != "ECC_P256_R1" || request.MessageType == nil || *request.MessageType != "DIGEST" || request.Message == nil {
				t.Fatalf("unexpected SignByAsymmetricKey request: %+v", request)
			}
			digest, err := base64.StdEncoding.Strict().DecodeString(*request.Message)
			if err != nil || len(digest) != sha256.Size {
				t.Fatalf("request digest = %q, %v", *request.Message, err)
			}
			signature, err := ecdsa.SignASN1(rand.Reader, private, digest)
			if err != nil {
				t.Fatal(err)
			}
			return signatureResponse(signature, "sign-request-id"), nil
		},
	}
	signer, err := NewKMSSigner(client, expectedDigest)
	if err != nil {
		t.Fatal(err)
	}
	gotDER, requestID, err := signer.PublicKey(context.Background(), "kms-key-id")
	if err != nil {
		t.Fatal(err)
	}
	if client.publicRequest == nil || client.publicRequest.KeyId == nil || *client.publicRequest.KeyId != "kms-key-id" || !bytes.Equal(gotDER, der) || requestID != "public-request-id" {
		t.Fatalf("GetPublicKey result/request mismatch: request=%+v requestID=%q", client.publicRequest, requestID)
	}
	digest := sha256.Sum256([]byte("canonical signed policy"))
	signature, requestID, err := signer.SignDigest(context.Background(), "kms-key-id", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "sign-request-id" || !ecdsa.VerifyASN1(&private.PublicKey, digest[:], signature) {
		t.Fatal("SignDigest returned an unverifiable signature or wrong request ID")
	}
}

func TestKMSSignerRejectsKeyIDSPKIBase64PEMAndDERMismatches(t *testing.T) {
	_, validDER, validDigest := kmsTestKey(t, elliptic.P256())
	_, otherDER, _ := kmsTestKey(t, elliptic.P256())
	_, p384DER, p384Digest := kmsTestKey(t, elliptic.P384())
	tests := []struct {
		name     string
		response *kms.GetPublicKeyResponse
		expected string
	}{
		{name: "response key ID mismatch", response: publicKeyResponse("other-key", validDER, "request-id"), expected: validDigest},
		{name: "malformed base64", response: publicKeyResponseWithValues("kms-key-id", "%%%", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: validDER})), "request-id"), expected: validDigest},
		{name: "malformed PEM", response: publicKeyResponseWithValues("kms-key-id", base64.StdEncoding.EncodeToString(validDER), "not PEM", "request-id"), expected: validDigest},
		{name: "PEM and DER mismatch", response: publicKeyResponseWithValues("kms-key-id", base64.StdEncoding.EncodeToString(validDER), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: otherDER})), "request-id"), expected: validDigest},
		{name: "wrong PEM type", response: publicKeyResponseWithValues("kms-key-id", base64.StdEncoding.EncodeToString(validDER), string(pem.EncodeToMemory(&pem.Block{Type: "EC PUBLIC KEY", Bytes: validDER})), "request-id"), expected: validDigest},
		{name: "malformed DER", response: publicKeyResponse("kms-key-id", []byte("not DER"), "request-id"), expected: sha256String([]byte("not DER"))},
		{name: "non P256 DER", response: publicKeyResponse("kms-key-id", p384DER, "request-id"), expected: p384Digest},
		{name: "expected digest mismatch", response: publicKeyResponse("kms-key-id", validDER, "request-id"), expected: strings.Repeat("0", 64)},
		{name: "missing request ID", response: publicKeyResponse("kms-key-id", validDER, ""), expected: validDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeKMSClient{publicResponse: test.response}
			signer, err := NewKMSSigner(client, test.expected)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := signer.PublicKey(context.Background(), "kms-key-id"); err == nil {
				t.Fatal("PublicKey() error = nil, want strict response rejection")
			}
		})
	}
}

func TestKMSSignerRejectsMalformedSignatureAndNonDigestInput(t *testing.T) {
	_, der, expectedDigest := kmsTestKey(t, elliptic.P256())
	tests := []struct {
		name      string
		digest    []byte
		response  *kms.SignByAsymmetricKeyResponse
		wantCalls int
	}{
		{name: "digest length", digest: []byte("raw message"), response: signatureResponse([]byte{1}, "request-id"), wantCalls: 0},
		{name: "malformed base64", digest: make([]byte, sha256.Size), response: &kms.SignByAsymmetricKeyResponse{Response: &kms.SignByAsymmetricKeyResponseParams{Signature: common.StringPtr("%%%"), RequestId: common.StringPtr("request-id")}}, wantCalls: 1},
		{name: "malformed DER", digest: make([]byte, sha256.Size), response: signatureResponse([]byte("not DER"), "request-id"), wantCalls: 1},
		{name: "missing request ID", digest: make([]byte, sha256.Size), response: signatureResponse([]byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01}, ""), wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeKMSClient{publicResponse: publicKeyResponse("kms-key-id", der, "public-request-id"), signResponse: test.response}
			signer, err := NewKMSSigner(client, expectedDigest)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := signer.SignDigest(context.Background(), "kms-key-id", test.digest); err == nil {
				t.Fatal("SignDigest() error = nil, want strict rejection")
			}
			if client.signCalls != test.wantCalls {
				t.Fatalf("KMS sign calls = %d, want %d", client.signCalls, test.wantCalls)
			}
		})
	}
}

func TestKMSSignerRedactsProviderErrors(t *testing.T) {
	_, der, expectedDigest := kmsTestKey(t, elliptic.P256())
	const secret = "provider credential and raw response secret"
	client := &fakeKMSClient{publicResponse: publicKeyResponse("kms-key-id", der, "request-id"), publicErr: errors.New(secret)}
	signer, err := NewKMSSigner(client, expectedDigest)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = signer.PublicKey(context.Background(), "kms-key-id")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("PublicKey error = %q, want redacted failure", err)
	}
}

func TestKMSSignerSelectsExactlyReviewedCredentialProviderMode(t *testing.T) {
	_, _, expectedDigest := kmsTestKey(t, elliptic.P256())
	for _, mode := range []string{"environment-session", "tke-oidc", "cvm-role"} {
		t.Run(mode, func(t *testing.T) {
			calls := map[string]int{}
			provider := &fakeCredentialProvider{credential: common.NewTokenCredential("temporary-id", "temporary-key", "temporary-token")}
			factories := kmsProviderFactories{
				environmentSession: func() (common.Provider, error) { calls["environment-session"]++; return provider, nil },
				tkeOIDC:            func() (common.Provider, error) { calls["tke-oidc"]++; return provider, nil },
				cvmRole:            func() (common.Provider, error) { calls["cvm-role"]++; return provider, nil },
				newClient: func(credential common.CredentialIface, region string) (kmsAPI, error) {
					calls["client"]++
					if region != "ap-shanghai" || credential.GetToken() != "temporary-token" {
						t.Fatal("client received wrong region or non-session credential")
					}
					return &fakeKMSClient{}, nil
				},
			}
			if _, err := newTencentKMSSignerWithProviders("ap-shanghai", expectedDigest, mode, factories); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []string{"environment-session", "tke-oidc", "cvm-role"} {
				want := 0
				if candidate == mode {
					want = 1
				}
				if calls[candidate] != want {
					t.Fatalf("provider %s calls = %d, want %d", candidate, calls[candidate], want)
				}
			}
			if calls["client"] != 1 || provider.calls != 1 {
				t.Fatalf("client/provider calls = %d/%d, want 1/1", calls["client"], provider.calls)
			}
		})
	}
}

func TestKMSSignerProviderModeFailureNeverFallsThrough(t *testing.T) {
	_, _, expectedDigest := kmsTestKey(t, elliptic.P256())
	for _, mode := range []string{"", "ambient", "environment-session", "tke-oidc", "cvm-role"} {
		t.Run(mode, func(t *testing.T) {
			calls := map[string]int{}
			secret := "provider-secret-must-not-leak"
			factories := kmsProviderFactories{
				environmentSession: func() (common.Provider, error) { calls["environment-session"]++; return nil, errors.New(secret) },
				tkeOIDC:            func() (common.Provider, error) { calls["tke-oidc"]++; return nil, errors.New(secret) },
				cvmRole:            func() (common.Provider, error) { calls["cvm-role"]++; return nil, errors.New(secret) },
				newClient: func(common.CredentialIface, string) (kmsAPI, error) {
					calls["client"]++
					return &fakeKMSClient{}, nil
				},
			}
			_, err := newTencentKMSSignerWithProviders("ap-shanghai", expectedDigest, mode, factories)
			if err == nil {
				t.Fatal("provider selection error = nil")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("provider error leaked detail: %q", err)
			}
			selectedCalls := calls["environment-session"] + calls["tke-oidc"] + calls["cvm-role"]
			if mode == "" || mode == "ambient" {
				if selectedCalls != 0 {
					t.Fatalf("invalid mode selected %d providers", selectedCalls)
				}
			} else if selectedCalls != 1 {
				t.Fatalf("selected provider failure fell through: calls=%v", calls)
			}
			if calls["client"] != 0 {
				t.Fatal("client constructed after provider failure")
			}
		})
	}
}

func TestKMSSignerRejectsSelectedCredentialWithoutSessionToken(t *testing.T) {
	_, _, expectedDigest := kmsTestKey(t, elliptic.P256())
	provider := &fakeCredentialProvider{credential: common.NewCredential("long-lived-id", "long-lived-key")}
	clientCalls := 0
	_, err := newTencentKMSSignerWithProviders("ap-shanghai", expectedDigest, "cvm-role", kmsProviderFactories{
		cvmRole: func() (common.Provider, error) { return provider, nil },
		newClient: func(common.CredentialIface, string) (kmsAPI, error) {
			clientCalls++
			return &fakeKMSClient{}, nil
		},
	})
	if err == nil {
		t.Fatal("constructor accepted selected credential without STS token")
	}
	if clientCalls != 0 {
		t.Fatal("client constructed with non-session credential")
	}
}

func TestKMSSignerEnvironmentSessionRequiresAllTemporaryValues(t *testing.T) {
	const secretID = "temporary-secret-id"
	const secretKey = "temporary-secret-key"
	const token = "temporary-sts-token"
	for _, missing := range []string{"", "TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY", "TENCENTCLOUD_SESSION_TOKEN"} {
		name := missing
		if name == "" {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			values := map[string]string{
				"TENCENTCLOUD_SECRET_ID":     secretID,
				"TENCENTCLOUD_SECRET_KEY":    secretKey,
				"TENCENTCLOUD_SESSION_TOKEN": token,
			}
			if missing != "" {
				values[missing] = ""
			}
			provider := newEnvironmentSessionProvider(func(name string) (string, bool) {
				value, ok := values[name]
				return value, ok
			})
			credential, err := provider.GetCredential()
			if missing != "" {
				if err == nil {
					t.Fatal("environment session accepted missing temporary value")
				}
				for _, secret := range []string{secretID, secretKey, token} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("environment provider error leaked temporary value: %q", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			gotID, gotKey, gotToken := credential.GetCredential()
			if gotID != secretID || gotKey != secretKey || gotToken != token {
				t.Fatal("environment provider did not preserve the complete temporary session")
			}
		})
	}
}

func validSignOptions(fake *fakeKMSSigner, expectedDigest string) SignOptions {
	return SignOptions{
		KeyID:                 "kms-key-id",
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

type fakeKMSSigner struct {
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

type fakeKMSClient struct {
	publicRequest  *kms.GetPublicKeyRequest
	publicResponse *kms.GetPublicKeyResponse
	publicErr      error
	signResponse   *kms.SignByAsymmetricKeyResponse
	signErr        error
	sign           func(*kms.SignByAsymmetricKeyRequest) (*kms.SignByAsymmetricKeyResponse, error)
	signCalls      int
}

type fakeCredentialProvider struct {
	credential common.CredentialIface
	err        error
	calls      int
}

func (provider *fakeCredentialProvider) GetCredential() (common.CredentialIface, error) {
	provider.calls++
	return provider.credential, provider.err
}

func (client *fakeKMSClient) GetPublicKeyWithContext(_ context.Context, request *kms.GetPublicKeyRequest) (*kms.GetPublicKeyResponse, error) {
	client.publicRequest = request
	return client.publicResponse, client.publicErr
}

func (client *fakeKMSClient) SignByAsymmetricKeyWithContext(_ context.Context, request *kms.SignByAsymmetricKeyRequest) (*kms.SignByAsymmetricKeyResponse, error) {
	client.signCalls++
	if client.sign != nil {
		return client.sign(request)
	}
	return client.signResponse, client.signErr
}

func kmsTestKey(t testing.TB, curve elliptic.Curve) (*ecdsa.PrivateKey, []byte, string) {
	t.Helper()
	private, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return private, der, sha256String(der)
}

func publicKeyResponse(keyID string, der []byte, requestID string) *kms.GetPublicKeyResponse {
	return publicKeyResponseWithValues(keyID, base64.StdEncoding.EncodeToString(der), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), requestID)
}

func publicKeyResponseWithValues(keyID, publicKey, publicKeyPEM, requestID string) *kms.GetPublicKeyResponse {
	return &kms.GetPublicKeyResponse{Response: &kms.GetPublicKeyResponseParams{
		KeyId: common.StringPtr(keyID), PublicKey: common.StringPtr(publicKey), PublicKeyPem: common.StringPtr(publicKeyPEM), RequestId: common.StringPtr(requestID),
	}}
}

func signatureResponse(signature []byte, requestID string) *kms.SignByAsymmetricKeyResponse {
	return &kms.SignByAsymmetricKeyResponse{Response: &kms.SignByAsymmetricKeyResponseParams{
		Signature: common.StringPtr(base64.StdEncoding.EncodeToString(signature)), RequestId: common.StringPtr(requestID),
	}}
}

func sha256String(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func newFakeKMSSigner(t testing.TB) *fakeKMSSigner {
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
	return &fakeKMSSigner{
		PublicDER: der, SPKISHA256: hex.EncodeToString(digest[:]), SigningKey: key,
		PublicRequestID: "public-request-id", SignRequestID: "sign-request-id",
	}
}

func (fake *fakeKMSSigner) PublicKey(_ context.Context, keyID string) ([]byte, string, error) {
	fake.PublicKeyID = keyID
	if fake.Err != nil {
		return nil, "", fake.Err
	}
	return append([]byte(nil), fake.PublicDER...), fake.PublicRequestID, nil
}

func (fake *fakeKMSSigner) SignDigest(_ context.Context, keyID string, digest []byte) ([]byte, string, error) {
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
