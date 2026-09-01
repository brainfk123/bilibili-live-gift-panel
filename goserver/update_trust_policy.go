package main

import (
	"crypto/ecdsa"
	"regexp"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/updatepolicy"
)

const (
	maxUpdateTrustPolicyBytes = 256 << 10
	maxUpdateTrustPolicyDepth = 16
)

type updateChannel = updatepolicy.Channel

const (
	updateChannelStable         = updatepolicy.ChannelStable
	updateChannelLegacyRushRush = updatepolicy.ChannelLegacyRushRush
)

type updateArtifactIdentity = updatepolicy.ArtifactIdentity
type verifiedUpdateTrustPolicy struct {
	Epoch     uint64
	ExpiresAt time.Time
	SignedRaw []byte
	Rules     []updatePublisherRule
}
type publisherPolicyDocument = updatepolicy.Document
type publisherPolicySigned = updatepolicy.Signed
type updatePublisherRule = updatepolicy.PublisherRule
type publisherPolicySignature = updatepolicy.Signature

type updateTrustPolicyError struct{ code string }

func (e *updateTrustPolicyError) Error() string { return e.code }
func policyError(code string) error             { return &updateTrustPolicyError{code: code} }

func parseAndVerifyUpdateTrustPolicy(data []byte, root *ecdsa.PublicKey, now time.Time) (verifiedUpdateTrustPolicy, error) {
	verified, err := updatepolicy.ParseAndVerify(data, root, now)
	if err != nil {
		return verifiedUpdateTrustPolicy{}, policyError(err.Error())
	}
	return verifiedUpdateTrustPolicy{Epoch: verified.Epoch, ExpiresAt: verified.ExpiresAt, SignedRaw: verified.SignedRaw, Rules: verified.Rules}, nil
}
func (p verifiedUpdateTrustPolicy) Authorize(input updateArtifactIdentity) error {
	err := (updatepolicy.Verified{Epoch: p.Epoch, ExpiresAt: p.ExpiresAt, SignedRaw: p.SignedRaw, Rules: p.Rules}).Authorize(input)
	if err != nil {
		return policyError(err.Error())
	}
	return nil
}
func (p verifiedUpdateTrustPolicy) AuthorizeAt(input updateArtifactIdentity, at time.Time) error {
	err := (updatepolicy.Verified{Epoch: p.Epoch, ExpiresAt: p.ExpiresAt, SignedRaw: p.SignedRaw, Rules: p.Rules}).AuthorizeAt(input, at)
	if err != nil {
		return policyError(err.Error())
	}
	return nil
}
func canonicalizePublisherPolicySigned(s publisherPolicySigned) ([]byte, error) {
	return updatepolicy.CanonicalSigned(s)
}
func validatePublisherPolicyJSON(data []byte) error {
	if err := updatepolicy.ValidateJSON(data); err != nil {
		return policyError(err.Error())
	}
	return nil
}

type canonicalTagMatcher struct{}

func (canonicalTagMatcher) MatchString(value string) bool { return updatepolicy.IsCanonicalTag(value) }

var canonicalPolicyTag canonicalTagMatcher
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func normalizeUpdateCertificateIdentity(input updateCertificateIdentity) updateCertificateIdentity {
	return updateCertificateIdentity{Country: strings.ToUpper(strings.TrimSpace(input.Country)), Organization: strings.TrimSpace(input.Organization), OrganizationID: strings.ToUpper(strings.TrimSpace(input.OrganizationID))}
}
