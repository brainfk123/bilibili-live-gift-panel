// Package trustpolicy validates and signs publisher-policy candidates without
// ever accepting policy or KMS secrets through its output surfaces.
package trustpolicy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxCandidateBytes = 256 << 10
	maxCandidateDepth = 16
	maxPolicyBytes    = 256 << 10
	clientAlgorithm   = "ecdsa-p256-sha256"

	stableChannel = "stable"
	bridgeChannel = "legacy-rushrush"
)

const canonicalNumericPrereleaseIdentifier = `(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)`

var (
	canonicalTag = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-` + canonicalNumericPrereleaseIdentifier + `(?:\.` + canonicalNumericPrereleaseIdentifier + `)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyIDValue   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	ciActor      = regexp.MustCompile(`^[A-Za-z0-9_.\[\]-]{1,100}$`)
	requestID    = regexp.MustCompile(`^[A-Za-z0-9_.:@/-]{1,256}$`)
)

// Candidate is exactly the signed object understood by the Task 1 client.
// Field order is deliberate because encoding this type defines signed bytes.
type Candidate struct {
	Epoch      uint64      `json:"epoch"`
	ExpiresAt  string      `json:"expiresAt"`
	Publishers []Publisher `json:"publishers"`
}

// Publisher is one closed publisher authorization in the client wire format.
type Publisher struct {
	ID             string   `json:"id"`
	Role           string   `json:"role"`
	Country        string   `json:"country"`
	Organization   string   `json:"organization"`
	OrganizationID string   `json:"organizationId"`
	AllowedChannel string   `json:"allowedChannel"`
	AllowedTags    []string `json:"allowedTags"`
	ManifestSHA256 string   `json:"manifestSha256,omitempty"`
}

// CandidateOptions binds a candidate to one exact anti-rollback transition.
type CandidateOptions struct {
	ExpectedPreviousEpoch uint64
	Now                   time.Time
}

// Signer is the narrow KMS boundary. Implementations return DER SPKI and DER
// ECDSA signature bytes plus non-secret provider request identifiers.
type Signer interface {
	PublicKey(context.Context, string) ([]byte, string, error)
	SignDigest(context.Context, string, []byte) ([]byte, string, error)
}

// SignOptions contains reviewed, non-secret binding inputs. KeyID and the SPKI
// digest are supplied by the CLI through named environment variables only.
type SignOptions struct {
	KeyID                 string
	ExpectedPreviousEpoch uint64
	ExpectedSPKISHA256    string
	Now                   time.Time
	CIActor               string
}

// Output is the complete client-readable policy and the exact signed bytes.
type Output struct {
	Policy          []byte
	CanonicalSigned []byte
}

// Audit is intentionally limited to the six reviewed non-secret fields.
type Audit struct {
	KeyID        string `json:"keyId"`
	Epoch        uint64 `json:"epoch"`
	PolicySHA256 string `json:"policySha256"`
	RequestID    string `json:"requestId"`
	UTC          string `json:"utc"`
	CIActor      string `json:"ciActor"`
}

type policySignature struct {
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

type safeError string

func (err safeError) Error() string { return string(err) }

const (
	errCandidateInvalid  safeError = "publisher policy candidate is invalid"
	errReviewedInput     safeError = "reviewed signing input is invalid"
	errPublicKey         safeError = "KMS public key validation failed"
	errSigning           safeError = "KMS signing failed"
	errSignatureInvalid  safeError = "KMS signature verification failed"
	errCanonicalEncoding safeError = "publisher policy encoding failed"
	errPolicySize        safeError = "publisher policy size is invalid"
)

// ParseCandidate rejects envelopes and ambiguous JSON before returning the
// complete signed object.
func ParseCandidate(data []byte, options CandidateOptions) (Candidate, error) {
	if len(data) == 0 || len(data) > maxCandidateBytes {
		return Candidate{}, errCandidateInvalid
	}
	if err := validateCandidateJSON(data); err != nil {
		return Candidate{}, errCandidateInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var candidate Candidate
	if err := decoder.Decode(&candidate); err != nil {
		return Candidate{}, errCandidateInvalid
	}
	if err := validateCandidate(candidate, options.ExpectedPreviousEpoch, resolvedNow(options.Now)); err != nil {
		return Candidate{}, errCandidateInvalid
	}
	return candidate, nil
}

// CanonicalSigned returns the same deterministic bytes used by the Task 1
// client: fixed struct field order, compact JSON, and no HTML escaping.
func CanonicalSigned(candidate Candidate) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(candidate); err != nil {
		return nil, errCanonicalEncoding
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

// Sign validates all local bindings before KMS access, submits only the SHA-256
// digest, verifies the returned signature locally, and then creates the client
// envelope in memory.
func Sign(ctx context.Context, signer Signer, candidate Candidate, options SignOptions) (Output, Audit, error) {
	if ctx == nil || signer == nil {
		return Output{}, Audit{}, errReviewedInput
	}
	if err := ValidateSignOptions(candidate, options); err != nil {
		return Output{}, Audit{}, err
	}
	now := resolvedNow(options.Now)
	canonical, err := CanonicalSigned(candidate)
	if err != nil {
		return Output{}, Audit{}, errCanonicalEncoding
	}
	digest := sha256.Sum256(canonical)

	publicDER, publicRequestID, err := signer.PublicKey(ctx, options.KeyID)
	if err != nil || !requestID.MatchString(publicRequestID) {
		return Output{}, Audit{}, errPublicKey
	}
	publicKey, err := parseReviewedPublicKey(publicDER, options.ExpectedSPKISHA256)
	if err != nil {
		return Output{}, Audit{}, errPublicKey
	}

	signature, signRequestID, err := signer.SignDigest(ctx, options.KeyID, digest[:])
	if err != nil || !requestID.MatchString(signRequestID) {
		return Output{}, Audit{}, errSigning
	}
	if len(signature) == 0 || !ecdsa.VerifyASN1(publicKey, digest[:], signature) {
		return Output{}, Audit{}, errSignatureInvalid
	}

	signatures, err := json.Marshal([]policySignature{{Algorithm: clientAlgorithm, Signature: base64.StdEncoding.EncodeToString(signature)}})
	if err != nil {
		return Output{}, Audit{}, errCanonicalEncoding
	}
	policy := make([]byte, 0, len(canonical)+len(signatures)+28)
	policy = append(policy, `{"signed":`...)
	policy = append(policy, canonical...)
	policy = append(policy, `,"signatures":`...)
	policy = append(policy, signatures...)
	policy = append(policy, '}')
	if len(policy) > maxPolicyBytes {
		return Output{}, Audit{}, errPolicySize
	}
	policyDigest := sha256.Sum256(policy)
	audit := Audit{
		KeyID:        options.KeyID,
		Epoch:        candidate.Epoch,
		PolicySHA256: hex.EncodeToString(policyDigest[:]),
		RequestID:    signRequestID,
		UTC:          now.UTC().Format(time.RFC3339),
		CIActor:      options.CIActor,
	}
	return Output{Policy: append([]byte(nil), policy...), CanonicalSigned: append([]byte(nil), canonical...)}, audit, nil
}

// ValidateSignOptions performs every local reviewed-input check without KMS
// access so command frontends can reject unsafe input before signer creation.
func ValidateSignOptions(candidate Candidate, options SignOptions) error {
	if !keyIDValue.MatchString(options.KeyID) || !sha256Hex.MatchString(options.ExpectedSPKISHA256) || !ciActor.MatchString(options.CIActor) {
		return errReviewedInput
	}
	if err := validateCandidate(candidate, options.ExpectedPreviousEpoch, resolvedNow(options.Now)); err != nil {
		return errCandidateInvalid
	}
	return nil
}

func parseReviewedPublicKey(der []byte, expectedHex string) (*ecdsa.PublicKey, error) {
	expected, err := hex.DecodeString(expectedHex)
	if err != nil || len(expected) != sha256.Size {
		return nil, errPublicKey
	}
	digest := sha256.Sum256(der)
	if subtle.ConstantTimeCompare(digest[:], expected) != 1 {
		return nil, errPublicKey
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errPublicKey
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() || publicKey.X == nil || publicKey.Y == nil || !publicKey.Curve.IsOnCurve(publicKey.X, publicKey.Y) {
		return nil, errPublicKey
	}
	return publicKey, nil
}

func validateCandidate(candidate Candidate, expectedPrevious uint64, now time.Time) error {
	if expectedPrevious == math.MaxUint64 || candidate.Epoch == 0 || candidate.Epoch != expectedPrevious+1 {
		return errCandidateInvalid
	}
	expiresAt, err := time.Parse(time.RFC3339, candidate.ExpiresAt)
	if err != nil || !strings.HasSuffix(candidate.ExpiresAt, "Z") || expiresAt.Format(time.RFC3339) != candidate.ExpiresAt || !expiresAt.After(now.UTC()) {
		return errCandidateInvalid
	}
	if len(candidate.Publishers) < 1 || len(candidate.Publishers) > 2 {
		return errCandidateInvalid
	}
	if err := validateNaisNetPublisher(candidate.Publishers[0]); err != nil {
		return errCandidateInvalid
	}
	if len(candidate.Publishers) == 2 {
		if err := validateRushRushPublisher(candidate.Publishers[1]); err != nil {
			return errCandidateInvalid
		}
	}
	return nil
}

func validateNaisNetPublisher(publisher Publisher) error {
	if publisher.ID != "naisnet-primary" || publisher.Role != "primary" || publisher.Country != "CN" ||
		publisher.Organization != "NaisNet Technology Co., Ltd." || publisher.OrganizationID != "91210103MA7CJ3C094" ||
		publisher.AllowedChannel != stableChannel || len(publisher.AllowedTags) == 0 || !validManifestHash(publisher.ManifestSHA256) {
		return errCandidateInvalid
	}
	if !sort.StringsAreSorted(publisher.AllowedTags) {
		return errCandidateInvalid
	}
	previous := ""
	for _, tag := range publisher.AllowedTags {
		if !canonicalTag.MatchString(tag) || tag == "v0.4.11" || tag == previous {
			return errCandidateInvalid
		}
		previous = tag
	}
	return nil
}

func validateRushRushPublisher(publisher Publisher) error {
	if publisher.ID != "rushrush-bridge" || publisher.Role != "bridge" || publisher.Country != "CN" ||
		publisher.Organization != "RushRush Network Technology Ltd" || publisher.OrganizationID != "91450900MADM3GLG5P" ||
		publisher.AllowedChannel != bridgeChannel || len(publisher.AllowedTags) != 1 || publisher.AllowedTags[0] != "v0.4.11" ||
		!validManifestHash(publisher.ManifestSHA256) {
		return errCandidateInvalid
	}
	return nil
}

func validManifestHash(value string) bool {
	return value == "" || sha256Hex.MatchString(value)
}

func resolvedNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func validateCandidateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateJSONValue(decoder, 1); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errCandidateInvalid
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxCandidateDepth {
		return errCandidateInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return errCandidateInvalid
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return errCandidateInvalid
			}
			name, ok := key.(string)
			if !ok {
				return errCandidateInvalid
			}
			if _, exists := keys[name]; exists {
				return errCandidateInvalid
			}
			keys[name] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return errCandidateInvalid
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return errCandidateInvalid
		}
	default:
		return errCandidateInvalid
	}
	return nil
}
