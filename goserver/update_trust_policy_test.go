package main

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pinnedPolicyTime = time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)

func TestPublisherPolicyAuthorize(t *testing.T) {
	policy := mustVerifyPolicyFixture(t, "policy-epoch-1.json")
	tests := []struct {
		name    string
		input   updateArtifactIdentity
		wantErr string
	}{
		{"stable NaisNet", updateArtifactIdentity{Tag: "v0.4.12", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}}, ""},
		{"exact bridge", updateArtifactIdentity{Tag: "v0.4.11", Channel: updateChannelLegacyRushRush, Certificate: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}, ""},
		{"RushRush cannot sign stable", updateArtifactIdentity{Tag: "v0.4.11", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}, "publisher_not_authorized"},
		{"RushRush tag is exact", updateArtifactIdentity{Tag: "v0.4.12", Channel: updateChannelLegacyRushRush, Certificate: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}, "publisher_not_authorized"},
		{"artifact tag whitespace is not canonical", updateArtifactIdentity{Tag: " v0.4.12 ", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}}, "publisher_not_authorized"},
		{"organization ID is exact", updateArtifactIdentity{Tag: "v0.4.12", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "different"}}, "publisher_not_authorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorCode(t, policy.Authorize(tt.input), tt.wantErr)
		})
	}
}

func TestPublisherPolicyRejectsInvalidDocuments(t *testing.T) {
	valid := string(readFixture(t, "policy-epoch-1.json"))
	tests := []struct {
		name string
		data string
		at   time.Time
		want string
	}{
		{"unknown fields", strings.Replace(valid, "\"epoch\":1", "\"unknown\":true,\"epoch\":1", 1), pinnedPolicyTime, "policy_unknown_field"},
		{"duplicate keys", strings.Replace(valid, "\"epoch\":1", "\"epoch\":1,\"epoch\":1", 1), pinnedPolicyTime, "policy_duplicate_key"},
		{"trailing JSON", valid + "{}", pinnedPolicyTime, "policy_trailing_json"},
		{"duplicate publisher IDs", strings.Replace(valid, "\"id\":\"rushrush-bridge\"", "\"id\":\"naisnet-primary\"", 1), pinnedPolicyTime, "policy_invalid"},
		{"zero epoch", strings.Replace(valid, "\"epoch\":1", "\"epoch\":0", 1), pinnedPolicyTime, "policy_invalid"},
		{"timestamp lacks UTC form", strings.Replace(valid, "2030-01-01T00:00:00Z", "2030-01-01T08:00:00+08:00", 1), pinnedPolicyTime, "policy_invalid"},
		{"noncanonical tag", strings.Replace(valid, "v0.4.12", "0.4.12", 1), pinnedPolicyTime, "policy_invalid"},
		{"trailing prerelease separator", strings.Replace(valid, "v0.4.12", "v0.4.12-", 1), pinnedPolicyTime, "policy_invalid"},
		{"trailing build separator", strings.Replace(valid, "v0.4.12", "v0.4.12+", 1), pinnedPolicyTime, "policy_invalid"},
		{"leading zero major", strings.Replace(valid, "v0.4.12", "v00.4.12", 1), pinnedPolicyTime, "policy_invalid"},
		{"leading zero minor", strings.Replace(valid, "v0.4.12", "v0.04.12", 1), pinnedPolicyTime, "policy_invalid"},
		{"leading zero patch", strings.Replace(valid, "v0.4.12", "v0.4.012", 1), pinnedPolicyTime, "policy_invalid"},
		{"conflicting publisher scope", conflictingPublisherPolicyFixture(), pinnedPolicyTime, "policy_invalid"},
		{"expired policy", valid, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), "policy_expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseAndVerifyUpdateTrustPolicy([]byte(tt.data), testRootPublicKey(t), tt.at)
			assertErrorCode(t, err, tt.want)
		})
	}
}

func conflictingPublisherPolicyFixture() string {
	return `{"signed":{"epoch":1,"expiresAt":"2030-01-01T00:00:00Z","publishers":[{"id":"naisnet-primary","role":"primary","country":"CN","organization":"NaisNet Technology Co., Ltd.","organizationId":"91210103MA7CJ3C094","allowedChannel":"stable","allowedTags":["v0.4.12"]},{"id":"naisnet-conflicting-hash","role":"primary","country":"CN","organization":"NaisNet Technology Co., Ltd.","organizationId":"91210103MA7CJ3C094","allowedChannel":"stable","allowedTags":["v0.4.12"],"manifestSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]},"signatures":[{"algorithm":"ecdsa-p256-sha256","signature":"AA=="}]}`
}

func TestCanonicalSignedPolicy(t *testing.T) {
	policy := mustVerifyPolicyFixture(t, "policy-epoch-1.json")
	want := `{"epoch":1,"expiresAt":"2030-01-01T00:00:00Z","publishers":[{"id":"naisnet-primary","role":"primary","country":"CN","organization":"NaisNet Technology Co., Ltd.","organizationId":"91210103MA7CJ3C094","allowedChannel":"stable","allowedTags":["v0.4.12"]},{"id":"rushrush-bridge","role":"bridge","country":"CN","organization":"RushRush Network Technology Ltd","organizationId":"91450900MADM3GLG5P","allowedChannel":"legacy-rushrush","allowedTags":["v0.4.11"]}]}`
	if got := string(policy.SignedRaw); got != want {
		t.Fatalf("canonical signed policy = %q, want %q", got, want)
	}

	fixture := string(readFixture(t, "policy-epoch-1.json"))
	signature := extractFixtureSignature(t, fixture)
	digest := sha256.Sum256(policy.SignedRaw)
	if !ecdsa.VerifyASN1(testRootPublicKey(t), digest[:], signature) {
		t.Fatal("test-only fixture signature does not verify canonical signed policy")
	}
}

func TestPublisherPolicyAuthorizeRequiresManifestHashWhenScoped(t *testing.T) {
	policy := verifiedUpdateTrustPolicy{
		Epoch:     1,
		ExpiresAt: time.Now().Add(time.Hour),
		Rules: []updatePublisherRule{{
			ID: "naisnet-hash-scoped", Role: "primary", Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094", AllowedChannel: updateChannelStable, AllowedTags: []string{"v0.4.12"}, ManifestSHA256: strings.Repeat("a", 64),
		}},
	}
	identity := updateArtifactIdentity{Tag: "v0.4.12", Channel: updateChannelStable, SHA256: strings.Repeat("a", 64), Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}}
	assertErrorCode(t, policy.Authorize(identity), "")
	identity.SHA256 = strings.Repeat("b", 64)
	assertErrorCode(t, policy.Authorize(identity), "publisher_not_authorized")
}

func FuzzParsePublisherPolicy(f *testing.F) {
	valid := readFixture(f, "policy-epoch-1.json")
	f.Add(valid)
	f.Add([]byte(`{"signed":{"epoch":1,"epoch":1},"signatures":[]}`))
	f.Add([]byte(strings.Repeat(`{"nested":`, maxUpdateTrustPolicyDepth+1) + `0` + strings.Repeat(`}`, maxUpdateTrustPolicyDepth+1)))
	f.Add(make([]byte, maxUpdateTrustPolicyBytes+1))
	f.Add([]byte(strings.Replace(string(valid), "v0.4.12", "0.4.12", 1)))
	f.Add([]byte(strings.Replace(string(valid), `"signature":"MEUC`, `"signature":"AAAA`, 1)))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 300<<10 {
			t.Skip()
		}
		_, _ = parseAndVerifyUpdateTrustPolicy(data, testRootPublicKey(t), pinnedPolicyTime)
	})
}

func mustVerifyPolicyFixture(t testing.TB, name string) verifiedUpdateTrustPolicy {
	t.Helper()
	policy, err := parseAndVerifyUpdateTrustPolicy(readFixture(t, name), testRootPublicKey(t), pinnedPolicyTime)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return policy
}

func testRootPublicKey(t testing.TB) *ecdsa.PublicKey {
	t.Helper()
	der := readFixture(t, "root-epoch-1-spki.der")
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("parse test-only root SPKI: %v", err)
	}
	public, ok := key.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("root key type = %T, want ECDSA", key)
	}
	return public
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "update-trust", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func extractFixtureSignature(t testing.TB, policy string) []byte {
	t.Helper()
	const marker = `"signature":"`
	start := strings.Index(policy, marker)
	if start < 0 {
		t.Fatal("fixture has no signature")
	}
	start += len(marker)
	end := strings.Index(policy[start:], `"`)
	if end < 0 {
		t.Fatal("fixture signature is unterminated")
	}
	signature, err := base64.StdEncoding.DecodeString(policy[start : start+end])
	if err != nil {
		t.Fatalf("decode fixture signature: %v", err)
	}
	return signature
}

func assertErrorCode(t testing.TB, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want code %q", err, want)
	}
}
