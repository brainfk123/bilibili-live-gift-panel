package updatepolicy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
)

const maxPolicyBytes = 256 << 10

type Channel string

const (
	ChannelStable         Channel = "stable"
	ChannelLegacyRushRush Channel = "legacy-rushrush"
)

type ArtifactIdentity struct {
	Tag         string
	Channel     Channel
	SHA256      string
	Certificate certidentity.Identity
}
type Verified struct {
	Epoch     uint64
	ExpiresAt time.Time
	SignedRaw []byte
	Rules     []PublisherRule
}
type Document struct {
	Signed     Signed      `json:"signed"`
	Signatures []Signature `json:"signatures"`
}
type Signed struct {
	Epoch      uint64          `json:"epoch"`
	ExpiresAt  string          `json:"expiresAt"`
	Publishers []PublisherRule `json:"publishers"`
}
type PublisherRule struct {
	ID             string   `json:"id"`
	Role           string   `json:"role"`
	Country        string   `json:"country"`
	Organization   string   `json:"organization"`
	OrganizationID string   `json:"organizationId"`
	AllowedChannel Channel  `json:"allowedChannel"`
	AllowedTags    []string `json:"allowedTags"`
	ManifestSHA256 string   `json:"manifestSha256,omitempty"`
}
type Signature struct {
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}
type policyError struct{ code string }

func (e *policyError) Error() string { return e.code }
func failure(code string) error      { return &policyError{code: code} }

const numericPrerelease = `(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)`

var canonicalTag = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-` + numericPrerelease + `(?:\.` + numericPrerelease + `)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ParseAndVerify(data []byte, root *ecdsa.PublicKey, now time.Time) (Verified, error) {
	if len(data) == 0 || len(data) > maxPolicyBytes {
		return Verified{}, failure("policy_size_invalid")
	}
	if root == nil || root.Curve != elliptic.P256() || !root.Curve.IsOnCurve(root.X, root.Y) {
		return Verified{}, failure("policy_root_invalid")
	}
	if err := ValidateJSON(data); err != nil {
		return Verified{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return Verified{}, failure("policy_unknown_field")
		}
		return Verified{}, failure("policy_invalid")
	}
	if err := validate(document.Signed, document.Signatures, now); err != nil {
		return Verified{}, err
	}
	canonical, err := CanonicalSigned(document.Signed)
	if err != nil {
		return Verified{}, failure("policy_invalid")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(document.Signatures[0].Signature)
	digest := sha256.Sum256(canonical)
	if err != nil || len(signature) == 0 || !ecdsa.VerifyASN1(root, digest[:], signature) {
		return Verified{}, failure("policy_signature_invalid")
	}
	expiresAt, _ := time.Parse(time.RFC3339, document.Signed.ExpiresAt)
	return Verified{Epoch: document.Signed.Epoch, ExpiresAt: expiresAt, SignedRaw: canonical, Rules: append([]PublisherRule(nil), document.Signed.Publishers...)}, nil
}

func CanonicalSigned(s Signed) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func (p Verified) Authorize(input ArtifactIdentity) error {
	if p.Epoch == 0 || p.ExpiresAt.IsZero() || !p.ExpiresAt.After(time.Now().UTC()) {
		return failure("policy_expired")
	}
	if !canonicalTag.MatchString(input.Tag) {
		return failure("publisher_not_authorized")
	}
	inputHash := strings.ToLower(strings.TrimSpace(input.SHA256))
	certificate := normalize(input.Certificate)
	for _, rule := range p.Rules {
		if input.Channel != rule.AllowedChannel || !allows(rule.AllowedTags, input.Tag) || certificate.Country != rule.Country || certificate.Organization != rule.Organization || certificate.OrganizationID != rule.OrganizationID {
			continue
		}
		if rule.ManifestSHA256 != "" && inputHash != rule.ManifestSHA256 {
			continue
		}
		return nil
	}
	return failure("publisher_not_authorized")
}

func (p Verified) AuthorizeExactManifest(input ArtifactIdentity) error {
	if !sha256Hex.MatchString(input.SHA256) {
		return failure("publisher_not_authorized")
	}
	if err := p.Authorize(input); err != nil {
		return err
	}
	certificate := normalize(input.Certificate)
	for _, rule := range p.Rules {
		if rule.ManifestSHA256 == input.SHA256 && input.Channel == rule.AllowedChannel && allows(rule.AllowedTags, input.Tag) && certificate.Country == rule.Country && certificate.Organization == rule.Organization && certificate.OrganizationID == rule.OrganizationID {
			return nil
		}
	}
	return failure("publisher_not_authorized")
}

func validate(s Signed, signatures []Signature, now time.Time) error {
	if s.Epoch == 0 || len(s.Publishers) == 0 || len(signatures) != 1 || signatures[0].Algorithm != "ecdsa-p256-sha256" {
		return failure("policy_invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339, s.ExpiresAt)
	if err != nil || !strings.HasSuffix(s.ExpiresAt, "Z") || expiresAt.Format(time.RFC3339) != s.ExpiresAt {
		return failure("policy_invalid")
	}
	if !expiresAt.After(now.UTC()) {
		return failure("policy_expired")
	}
	ids := map[string]struct{}{}
	scopes := map[string]struct{}{}
	for _, rule := range s.Publishers {
		if _, ok := ids[rule.ID]; ok {
			return failure("policy_invalid")
		}
		ids[rule.ID] = struct{}{}
		if err := validateRule(rule); err != nil {
			return err
		}
		for _, tag := range rule.AllowedTags {
			scope := rule.Country + "\x00" + rule.Organization + "\x00" + rule.OrganizationID + "\x00" + string(rule.AllowedChannel) + "\x00" + tag
			if _, ok := scopes[scope]; ok {
				return failure("policy_invalid")
			}
			scopes[scope] = struct{}{}
		}
	}
	return nil
}

func validateRule(rule PublisherRule) error {
	if strings.TrimSpace(rule.ID) == "" || rule.ID != strings.TrimSpace(rule.ID) || strings.TrimSpace(rule.Organization) == "" || rule.Organization != strings.TrimSpace(rule.Organization) || strings.TrimSpace(rule.OrganizationID) == "" || rule.OrganizationID != strings.ToUpper(strings.TrimSpace(rule.OrganizationID)) || rule.Country != "CN" || len(rule.AllowedTags) == 0 || (rule.ManifestSHA256 != "" && !sha256Hex.MatchString(rule.ManifestSHA256)) {
		return failure("policy_invalid")
	}
	seen := map[string]struct{}{}
	for _, tag := range rule.AllowedTags {
		if !canonicalTag.MatchString(tag) {
			return failure("policy_invalid")
		}
		if _, ok := seen[tag]; ok {
			return failure("policy_invalid")
		}
		seen[tag] = struct{}{}
	}
	if rule.Country == "CN" && rule.Organization == "RushRush Network Technology Ltd" && rule.OrganizationID == "91450900MADM3GLG5P" {
		if rule.Role != "bridge" || rule.AllowedChannel != ChannelLegacyRushRush || len(rule.AllowedTags) != 1 || rule.AllowedTags[0] != "v0.4.11" {
			return failure("policy_invalid")
		}
		return nil
	}
	if rule.Role != "primary" || rule.AllowedChannel != ChannelStable {
		return failure("policy_invalid")
	}
	return nil
}

func ValidateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateValue(decoder, 1); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return failure("policy_trailing_json")
	}
	return nil
}
func validateValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return failure("policy_depth_invalid")
	}
	token, err := decoder.Token()
	if err != nil {
		return failure("policy_invalid")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return failure("policy_invalid")
			}
			if _, exists := keys[name]; exists {
				return failure("policy_duplicate_key")
			}
			keys[name] = struct{}{}
			if err := validateValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return failure("policy_invalid")
		}
	case '[':
		for decoder.More() {
			if err := validateValue(decoder, depth+1); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return failure("policy_invalid")
		}
	default:
		return failure("policy_invalid")
	}
	return nil
}
func IsCanonicalTag(tag string) bool { return canonicalTag.MatchString(tag) }
func normalize(input certidentity.Identity) certidentity.Identity {
	return certidentity.Identity{Country: strings.ToUpper(strings.TrimSpace(input.Country)), Organization: strings.TrimSpace(input.Organization), OrganizationID: strings.ToUpper(strings.TrimSpace(input.OrganizationID))}
}
func allows(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
func Hash(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
