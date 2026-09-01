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

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "update-trust", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
