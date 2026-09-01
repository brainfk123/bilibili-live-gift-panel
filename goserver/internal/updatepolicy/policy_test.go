package updatepolicy

import (
	"crypto/ecdsa"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
)

func TestProductionVerifierAuthorizesExactStableArtifactInput(t *testing.T) {
	rootDER := fixture(t, "root-epoch-1-spki.der")
	parsed, err := x509.ParsePKIXPublicKey(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	root := parsed.(*ecdsa.PublicKey)
	policy, err := ParseAndVerify(fixture(t, "policy-epoch-1.json"), root, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Authorize(ArtifactIdentity{
		Tag: "v0.4.12", Channel: ChannelStable, SHA256: strings.Repeat("a", 64),
		Certificate: certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProductionVerifierRejectsNoncanonicalAndWrongManifestHash(t *testing.T) {
	rootDER := fixture(t, "root-epoch-1-spki.der")
	parsed, _ := x509.ParsePKIXPublicKey(rootDER)
	policy, err := ParseAndVerify(fixture(t, "policy-epoch-1.json"), parsed.(*ecdsa.PublicKey), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	input := ArtifactIdentity{Tag: " v0.4.12 ", Channel: ChannelStable, SHA256: strings.Repeat("b", 64), Certificate: certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}}
	if err := policy.Authorize(input); err == nil {
		t.Fatal("noncanonical tag accepted")
	}
}

func TestAuthorizeAtIsSingleCurrentPolicyMutationBoundary(t *testing.T) {
	identity := certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	hash := strings.Repeat("a", 64)
	policy := Verified{Epoch: 1, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Rules: []PublisherRule{{ID: "naisnet-primary", Role: "primary", Country: identity.Country, Organization: identity.Organization, OrganizationID: identity.OrganizationID, AllowedChannel: ChannelStable, AllowedTags: []string{"v0.4.12"}, ManifestSHA256: hash}}}
	base := ArtifactIdentity{Tag: "v0.4.12", Channel: ChannelStable, SHA256: hash, Certificate: identity, RequireManifestSHA256: true}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := policy.AuthorizeAt(base, now); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*ArtifactIdentity){"tag": func(v *ArtifactIdentity) { v.Tag = "v0.4.13" }, "channel": func(v *ArtifactIdentity) { v.Channel = ChannelLegacyRushRush }, "hash": func(v *ArtifactIdentity) { v.SHA256 = strings.Repeat("b", 64) }, "organization": func(v *ArtifactIdentity) { v.Certificate.OrganizationID = "DIFFERENT" }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := policy.AuthorizeAt(candidate, now); err == nil {
				t.Fatal("mutation authorized")
			}
		})
	}
	unscoped := policy
	unscoped.Rules = append([]PublisherRule(nil), policy.Rules...)
	unscoped.Rules[0].ManifestSHA256 = ""
	if err := unscoped.AuthorizeAt(base, now); err == nil {
		t.Fatal("exact-manifest request matched an unscoped rule")
	}
}

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "update-trust", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
