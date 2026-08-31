package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	maxUpdateTrustPolicyBytes = 256 << 10
	maxUpdateTrustPolicyDepth = 16
)

type updateChannel string

const (
	updateChannelStable         updateChannel = "stable"
	updateChannelLegacyRushRush updateChannel = "legacy-rushrush"
)

type updateCertificateIdentity struct {
	Country        string
	Organization   string
	OrganizationID string
}

type updateArtifactIdentity struct {
	Tag         string
	Channel     updateChannel
	SHA256      string
	Certificate updateCertificateIdentity
}

type verifiedUpdateTrustPolicy struct {
	Epoch     uint64
	ExpiresAt time.Time
	SignedRaw []byte
	Rules     []updatePublisherRule
}

type publisherPolicyDocument struct {
	Signed     publisherPolicySigned      `json:"signed"`
	Signatures []publisherPolicySignature `json:"signatures"`
}

// publisherPolicySigned has deliberate field ordering: its JSON encoding is the
// byte sequence covered by the policy signature.
type publisherPolicySigned struct {
	Epoch      uint64                `json:"epoch"`
	ExpiresAt  string                `json:"expiresAt"`
	Publishers []updatePublisherRule `json:"publishers"`
}

type updatePublisherRule struct {
	ID             string        `json:"id"`
	Role           string        `json:"role"`
	Country        string        `json:"country"`
	Organization   string        `json:"organization"`
	OrganizationID string        `json:"organizationId"`
	AllowedChannel updateChannel `json:"allowedChannel"`
	AllowedTags    []string      `json:"allowedTags"`
	ManifestSHA256 string        `json:"manifestSha256,omitempty"`
}

type publisherPolicySignature struct {
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

type updateTrustPolicyError struct{ code string }

func (e *updateTrustPolicyError) Error() string { return e.code }

func policyError(code string) error { return &updateTrustPolicyError{code: code} }

func parseAndVerifyUpdateTrustPolicy(data []byte, root *ecdsa.PublicKey, now time.Time) (verifiedUpdateTrustPolicy, error) {
	if len(data) == 0 || len(data) > maxUpdateTrustPolicyBytes {
		return verifiedUpdateTrustPolicy{}, policyError("policy_size_invalid")
	}
	if root == nil || root.Curve != elliptic.P256() || !root.Curve.IsOnCurve(root.X, root.Y) {
		return verifiedUpdateTrustPolicy{}, policyError("policy_root_invalid")
	}
	if err := validatePublisherPolicyJSON(data); err != nil {
		return verifiedUpdateTrustPolicy{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document publisherPolicyDocument
	if err := decoder.Decode(&document); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return verifiedUpdateTrustPolicy{}, policyError("policy_unknown_field")
		}
		return verifiedUpdateTrustPolicy{}, policyError("policy_invalid")
	}
	if err := validatePublisherPolicy(document.Signed, document.Signatures, now); err != nil {
		return verifiedUpdateTrustPolicy{}, err
	}
	signedRaw, err := canonicalizePublisherPolicySigned(document.Signed)
	if err != nil {
		return verifiedUpdateTrustPolicy{}, policyError("policy_invalid")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(document.Signatures[0].Signature)
	digest := sha256.Sum256(signedRaw)
	if err != nil || len(signature) == 0 || !ecdsa.VerifyASN1(root, digest[:], signature) {
		return verifiedUpdateTrustPolicy{}, policyError("policy_signature_invalid")
	}
	expiresAt, _ := time.Parse(time.RFC3339, document.Signed.ExpiresAt)
	return verifiedUpdateTrustPolicy{
		Epoch: document.Signed.Epoch, ExpiresAt: expiresAt, SignedRaw: signedRaw, Rules: append([]updatePublisherRule(nil), document.Signed.Publishers...),
	}, nil
}

func canonicalizePublisherPolicySigned(s publisherPolicySigned) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func (p verifiedUpdateTrustPolicy) Authorize(input updateArtifactIdentity) error {
	if p.Epoch == 0 || p.ExpiresAt.IsZero() || !p.ExpiresAt.After(time.Now().UTC()) {
		return policyError("policy_expired")
	}
	if !canonicalPolicyTag.MatchString(input.Tag) {
		return policyError("publisher_not_authorized")
	}
	inputTag := input.Tag
	inputHash := strings.ToLower(strings.TrimSpace(input.SHA256))
	certificate := normalizeUpdateCertificateIdentity(input.Certificate)
	for _, rule := range p.Rules {
		if input.Channel != rule.AllowedChannel || !rule.allowsTag(inputTag) ||
			certificate.Country != rule.Country || certificate.Organization != rule.Organization || certificate.OrganizationID != rule.OrganizationID {
			continue
		}
		if rule.ManifestSHA256 != "" && inputHash != rule.ManifestSHA256 {
			continue
		}
		return nil
	}
	return policyError("publisher_not_authorized")
}

func (r updatePublisherRule) allowsTag(tag string) bool {
	for _, allowed := range r.AllowedTags {
		if tag == allowed {
			return true
		}
	}
	return false
}

func validatePublisherPolicy(signed publisherPolicySigned, signatures []publisherPolicySignature, now time.Time) error {
	if signed.Epoch == 0 || len(signed.Publishers) == 0 || len(signatures) != 1 || signatures[0].Algorithm != "ecdsa-p256-sha256" {
		return policyError("policy_invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339, signed.ExpiresAt)
	if err != nil || !strings.HasSuffix(signed.ExpiresAt, "Z") || expiresAt.Format(time.RFC3339) != signed.ExpiresAt {
		return policyError("policy_invalid")
	}
	if !expiresAt.After(now.UTC()) {
		return policyError("policy_expired")
	}
	ids := make(map[string]struct{}, len(signed.Publishers))
	tagScopes := make(map[publisherPolicyTagScope]struct{})
	for _, rule := range signed.Publishers {
		if _, exists := ids[rule.ID]; exists {
			return policyError("policy_invalid")
		}
		ids[rule.ID] = struct{}{}
		if err := validatePublisherRule(rule); err != nil {
			return err
		}
		for _, tag := range rule.AllowedTags {
			scope := publisherPolicyTagScope{
				country: rule.Country, organization: rule.Organization, organizationID: rule.OrganizationID, channel: rule.AllowedChannel, tag: tag,
			}
			if _, exists := tagScopes[scope]; exists {
				return policyError("policy_invalid")
			}
			tagScopes[scope] = struct{}{}
		}
	}
	return nil
}

type publisherPolicyTagScope struct {
	country        string
	organization   string
	organizationID string
	channel        updateChannel
	tag            string
}

var canonicalPolicyTag = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validatePublisherRule(rule updatePublisherRule) error {
	if strings.TrimSpace(rule.ID) == "" || rule.ID != strings.TrimSpace(rule.ID) ||
		strings.TrimSpace(rule.Organization) == "" || rule.Organization != strings.TrimSpace(rule.Organization) ||
		strings.TrimSpace(rule.OrganizationID) == "" || rule.OrganizationID != strings.ToUpper(strings.TrimSpace(rule.OrganizationID)) ||
		rule.Country != "CN" || len(rule.AllowedTags) == 0 ||
		(rule.ManifestSHA256 != "" && !sha256Hex.MatchString(rule.ManifestSHA256)) {
		return policyError("policy_invalid")
	}
	seenTags := make(map[string]struct{}, len(rule.AllowedTags))
	for _, tag := range rule.AllowedTags {
		if !canonicalPolicyTag.MatchString(tag) {
			return policyError("policy_invalid")
		}
		if _, exists := seenTags[tag]; exists {
			return policyError("policy_invalid")
		}
		seenTags[tag] = struct{}{}
	}
	if isRushRushPublisher(rule) {
		if rule.Role != "bridge" || rule.AllowedChannel != updateChannelLegacyRushRush || len(rule.AllowedTags) != 1 || rule.AllowedTags[0] != "v0.4.11" {
			return policyError("policy_invalid")
		}
		return nil
	}
	if rule.Role != "primary" || rule.AllowedChannel != updateChannelStable {
		return policyError("policy_invalid")
	}
	return nil
}

func isRushRushPublisher(rule updatePublisherRule) bool {
	return rule.Country == "CN" && rule.Organization == "RushRush Network Technology Ltd" && rule.OrganizationID == "91450900MADM3GLG5P"
}

func normalizeUpdateCertificateIdentity(input updateCertificateIdentity) updateCertificateIdentity {
	return updateCertificateIdentity{
		Country: strings.ToUpper(strings.TrimSpace(input.Country)), Organization: strings.TrimSpace(input.Organization), OrganizationID: strings.ToUpper(strings.TrimSpace(input.OrganizationID)),
	}
}

func validatePublisherPolicyJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateJSONValue(decoder, 1); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return policyError("policy_trailing_json")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxUpdateTrustPolicyDepth {
		return policyError("policy_depth_invalid")
	}
	token, err := decoder.Token()
	if err != nil {
		return policyError("policy_invalid")
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return policyError("policy_invalid")
				}
				name, ok := key.(string)
				if !ok {
					return policyError("policy_invalid")
				}
				if _, exists := keys[name]; exists {
					return policyError("policy_duplicate_key")
				}
				keys[name] = struct{}{}
				if err := validateJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return policyError("policy_invalid")
			}
		case '[':
			for decoder.More() {
				if err := validateJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return policyError("policy_invalid")
			}
		default:
			return policyError("policy_invalid")
		}
	}
	return nil
}
