package artifactinspect

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

func TestVerifyEnrollmentArtifactBindsBootstrapAndFinalHashAuthorization(t *testing.T) {
	fixture := enrollmentArtifactFixture(t)
	evidence, err := VerifyEnrollmentArtifact(fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 1 || evidence.Version != "0.4.12" || evidence.Tag != "v0.4.12" || evidence.Commit != strings.Repeat("c", 40) ||
		evidence.SignedFileSHA256 != fixture.artifactSHA256 || evidence.PEContentSHA256 != fixture.peContentSHA256 || evidence.RootSPKISHA256 != fixture.options.ExpectedRootSHA256 ||
		evidence.BootstrapPolicyEpoch != 1 || evidence.AuthorizationPolicyEpoch != 2 || evidence.AuthorizationScope != AuthorizationScopeArtifactSHA256 || evidence.AuthorizedArtifactSHA256 != fixture.artifactSHA256 ||
		evidence.OuterIdentity != naisNetIdentity || evidence.AuthorizedIdentity != naisNetIdentity || evidence.FFmpegIdentity != naisNetIdentity ||
		evidence.AuthenticodeStatus != "Valid" || evidence.BootstrapSignatureStatus != "Valid" || evidence.AuthorizationSignatureStatus != "Valid" || evidence.FFmpegSignatureStatus != "Valid" {
		t.Fatalf("enrollment evidence = %#v", evidence)
	}
}

func TestVerifyEnrollmentArtifactAcceptsPostEnrollmentPublisherIdentityScope(t *testing.T) {
	fixture := enrollmentArtifactFixtureForVersion(t, "0.4.13", false)
	evidence, err := VerifyEnrollmentArtifact(fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.AuthorizationScope != AuthorizationScopePublisherIdentity || evidence.AuthorizedArtifactSHA256 != "" || evidence.SignedFileSHA256 != fixture.artifactSHA256 {
		t.Fatalf("post-enrollment evidence = %#v", evidence)
	}
}

func TestVerifyEnrollmentArtifactAcceptsExplicitlyAuthorizedFuturePrimary(t *testing.T) {
	fixture := enrollmentArtifactFixtureForVersion(t, "0.4.33", false)
	future := certidentity.Identity{Country: "CN", Organization: "FutureCo Technology Co., Ltd.", OrganizationID: "91110000EXAMPLE01"}
	authorization := signedEnrollmentPolicyForIdentity(t, fixture.key, 2, fixture.options.Tag, "", future)
	fixture.options.AuthorizationPolicyPath = writeFixture(t, fixture.root, "future-authorization.json", authorization)
	fixture.options.ExpectedAuthorizationPolicySHA256 = sha256Hex(authorization)
	fixture.options.InspectAuthenticode = func(path string) (certidentity.Identity, error) {
		if filepath.Base(path) == "ffmpeg.exe" {
			return naisNetIdentity, nil
		}
		return future, nil
	}
	evidence, err := VerifyEnrollmentArtifact(fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.OuterIdentity != future || evidence.AuthorizedIdentity != future || evidence.FFmpegIdentity != naisNetIdentity {
		t.Fatalf("future primary evidence = %#v", evidence)
	}
}

func TestVerifyEnrollmentArtifactKeepsV0412ExactHashOnly(t *testing.T) {
	fixture := enrollmentArtifactFixtureForVersion(t, "0.4.12", false)
	if _, err := VerifyEnrollmentArtifact(fixture.options); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authorization") {
		t.Fatalf("hashless v0.4.12 authorization error = %v", err)
	}
}

func TestVerifyEnrollmentArtifactRejectsPostEnrollmentWrongHashOrIdentity(t *testing.T) {
	t.Run("wrong exact hash", func(t *testing.T) {
		fixture := enrollmentArtifactFixtureForVersion(t, "0.4.13", true)
		wrong := signedEnrollmentPolicy(t, fixture.key, 2, fixture.options.Tag, strings.Repeat("0", 64))
		fixture.options.AuthorizationPolicyPath = writeFixture(t, fixture.root, "wrong-post-enrollment-authorization.json", wrong)
		fixture.options.ExpectedAuthorizationPolicySHA256 = sha256Hex(wrong)
		if _, err := VerifyEnrollmentArtifact(fixture.options); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authorization") {
			t.Fatalf("wrong v0.4.13 hash authorization error = %v", err)
		}
	})
	t.Run("wrong legal identity", func(t *testing.T) {
		fixture := enrollmentArtifactFixtureForVersion(t, "0.4.13", false)
		fixture.options.InspectAuthenticode = func(string) (certidentity.Identity, error) {
			return certidentity.Identity{Country: "CN", Organization: "Other Technology Co., Ltd.", OrganizationID: "91110000OTHER001"}, nil
		}
		if _, err := VerifyEnrollmentArtifact(fixture.options); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authorization") {
			t.Fatalf("wrong v0.4.13 identity error = %v", err)
		}
	})
}

func TestVerifyEnrollmentArtifactRejectsWrongFinalHashAuthorization(t *testing.T) {
	fixture := enrollmentArtifactFixture(t)
	wrong := signedEnrollmentPolicy(t, fixture.key, 2, fixture.options.Tag, strings.Repeat("0", 64))
	fixture.options.AuthorizationPolicyPath = writeFixture(t, fixture.root, "wrong-authorization.json", wrong)
	fixture.options.ExpectedAuthorizationPolicySHA256 = sha256Hex(wrong)
	if _, err := VerifyEnrollmentArtifact(fixture.options); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authorization") {
		t.Fatalf("wrong final hash authorization error = %v", err)
	}
}

func TestVerifyEnrollmentArtifactRejectsNonContentAddressedOrSwappedArtifact(t *testing.T) {
	fixture := enrollmentArtifactFixture(t)
	nonAddressed := filepath.Join(fixture.root, "stable.exe")
	contents, err := os.ReadFile(fixture.options.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nonAddressed, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.options.ArtifactPath = nonAddressed
	if _, err := VerifyEnrollmentArtifact(fixture.options); err == nil || !strings.Contains(strings.ToLower(err.Error()), "content-addressed") {
		t.Fatalf("non-content-addressed artifact error = %v", err)
	}
}

func TestVerifyEnrollmentArtifactRejectsPreEnrollmentVersionAndNonAdvancingPolicy(t *testing.T) {
	for _, mutate := range []func(*VerifyEnrollmentOptions){
		func(options *VerifyEnrollmentOptions) { options.Version, options.Tag = "0.4.11", "v0.4.11" },
		func(options *VerifyEnrollmentOptions) {
			options.ExpectedAuthorizationPolicyEpoch = options.ExpectedBootstrapPolicyEpoch
		},
	} {
		fixture := enrollmentArtifactFixture(t)
		mutate(&fixture.options)
		if _, err := VerifyEnrollmentArtifact(fixture.options); err == nil {
			t.Fatal("invalid enrollment version or policy epoch was accepted")
		}
	}
}

func TestParseEnrollmentFFmpegManifestRejectsDuplicateFields(t *testing.T) {
	manifest := []byte(`{"version":"9.0","sha256":"` + strings.Repeat("a", 64) + `","size":1,"authenticode":true,"authenticode":true}`)
	if _, err := parseEnrollmentFFmpegManifest(manifest); err == nil {
		t.Fatal("duplicate enrollment FFmpeg manifest field was accepted")
	}
}

func TestEnrollmentVersionStartsAtV0412(t *testing.T) {
	if isEnrollmentVersion("0.4.11") || !isEnrollmentVersion("0.4.12") || !isEnrollmentVersion("1.0.0") || isEnrollmentVersion("01.0.0") {
		t.Fatal("enrollment version boundary is invalid")
	}
}

func TestVerifyEnrollmentPoliciesProvesExactAuthorizationBeforeBuild(t *testing.T) {
	fixture := enrollmentArtifactFixture(t)
	evidence, err := VerifyEnrollmentPolicies(VerifyEnrollmentPoliciesOptions{
		RootSPKIPath: fixture.options.RootSPKIPath, ExpectedRootSHA256: fixture.options.ExpectedRootSHA256,
		BootstrapPolicyPath: fixture.options.BootstrapPolicyPath, ExpectedBootstrapPolicySHA256: fixture.options.ExpectedBootstrapPolicySHA256, ExpectedBootstrapPolicyEpoch: fixture.options.ExpectedBootstrapPolicyEpoch,
		AuthorizationPolicyPath: fixture.options.AuthorizationPolicyPath, ExpectedAuthorizationPolicySHA256: fixture.options.ExpectedAuthorizationPolicySHA256, ExpectedAuthorizationPolicyEpoch: fixture.options.ExpectedAuthorizationPolicyEpoch,
		Tag: fixture.options.Tag, Now: fixture.options.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 1 || evidence.AuthorizationScope != AuthorizationScopeArtifactSHA256 || evidence.AuthorizedArtifactSHA256 != fixture.artifactSHA256 || evidence.AuthorizedIdentity != naisNetIdentity || evidence.BootstrapSignatureStatus != "Valid" || evidence.AuthorizationSignatureStatus != "Valid" {
		t.Fatalf("policy preflight evidence = %#v", evidence)
	}
	corrupt, err := os.ReadFile(fixture.options.AuthorizationPolicyPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt[len(corrupt)-2] ^= 1
	path := writeFixture(t, fixture.root, "corrupt-authorization.json", corrupt)
	if _, err := VerifyEnrollmentPolicies(VerifyEnrollmentPoliciesOptions{
		RootSPKIPath: fixture.options.RootSPKIPath, ExpectedRootSHA256: fixture.options.ExpectedRootSHA256,
		BootstrapPolicyPath: fixture.options.BootstrapPolicyPath, ExpectedBootstrapPolicySHA256: fixture.options.ExpectedBootstrapPolicySHA256, ExpectedBootstrapPolicyEpoch: fixture.options.ExpectedBootstrapPolicyEpoch,
		AuthorizationPolicyPath: path, ExpectedAuthorizationPolicySHA256: sha256Hex(corrupt), ExpectedAuthorizationPolicyEpoch: fixture.options.ExpectedAuthorizationPolicyEpoch,
		Tag: fixture.options.Tag, Now: fixture.options.Now,
	}); err == nil {
		t.Fatal("corrupt authorization policy passed pre-build verification")
	}
}

func TestVerifyEnrollmentPoliciesAcceptsPostEnrollmentPublisherIdentityScope(t *testing.T) {
	fixture := enrollmentArtifactFixtureForVersion(t, "0.4.13", false)
	evidence, err := VerifyEnrollmentPolicies(VerifyEnrollmentPoliciesOptions{
		RootSPKIPath: fixture.options.RootSPKIPath, ExpectedRootSHA256: fixture.options.ExpectedRootSHA256,
		BootstrapPolicyPath: fixture.options.BootstrapPolicyPath, ExpectedBootstrapPolicySHA256: fixture.options.ExpectedBootstrapPolicySHA256, ExpectedBootstrapPolicyEpoch: fixture.options.ExpectedBootstrapPolicyEpoch,
		AuthorizationPolicyPath: fixture.options.AuthorizationPolicyPath, ExpectedAuthorizationPolicySHA256: fixture.options.ExpectedAuthorizationPolicySHA256, ExpectedAuthorizationPolicyEpoch: fixture.options.ExpectedAuthorizationPolicyEpoch,
		Tag: fixture.options.Tag, Now: fixture.options.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.AuthorizationScope != AuthorizationScopePublisherIdentity || evidence.AuthorizedArtifactSHA256 != "" || evidence.AuthorizedIdentity != naisNetIdentity {
		t.Fatalf("post-enrollment policy evidence = %#v", evidence)
	}
}

func TestVerifyEnrollmentCandidateBeforeAuthorizationPolicyExists(t *testing.T) {
	fixture := enrollmentArtifactFixture(t)
	evidence, err := VerifyEnrollmentCandidate(VerifyEnrollmentCandidateOptions{
		ArtifactPath: fixture.options.ArtifactPath, ExpectedPEContentSHA256: fixture.options.ExpectedPEContentSHA256,
		Version: fixture.options.Version, Tag: fixture.options.Tag, Commit: fixture.options.Commit,
		RootSPKIPath: fixture.options.RootSPKIPath, ExpectedRootSHA256: fixture.options.ExpectedRootSHA256,
		BootstrapPolicyPath: fixture.options.BootstrapPolicyPath, ExpectedBootstrapPolicySHA256: fixture.options.ExpectedBootstrapPolicySHA256, ExpectedBootstrapPolicyEpoch: fixture.options.ExpectedBootstrapPolicyEpoch,
		FFmpegArchivePath: fixture.options.FFmpegArchivePath, FFmpegManifestPath: fixture.options.FFmpegManifestPath,
		Now: fixture.options.Now, InspectAuthenticode: fixture.options.InspectAuthenticode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 1 || evidence.SignedFileSHA256 != fixture.artifactSHA256 || evidence.RootKeyID != "sha256:"+fixture.options.ExpectedRootSHA256 || evidence.BootstrapSignatureStatus != "Valid" || evidence.OuterIdentity != naisNetIdentity || evidence.FFmpegIdentity != naisNetIdentity {
		t.Fatalf("candidate evidence = %#v", evidence)
	}
}

func TestVerifyEnrollmentCandidateBindsReviewedFuturePrimary(t *testing.T) {
	fixture := enrollmentArtifactFixtureForVersion(t, "0.4.33", false)
	future := certidentity.Identity{Country: "CN", Organization: "FutureCo Technology Co., Ltd.", OrganizationID: "91110000EXAMPLE01"}
	evidence, err := VerifyEnrollmentCandidate(VerifyEnrollmentCandidateOptions{
		ArtifactPath: fixture.options.ArtifactPath, ExpectedPEContentSHA256: fixture.options.ExpectedPEContentSHA256,
		Version: fixture.options.Version, Tag: fixture.options.Tag, Commit: fixture.options.Commit,
		RootSPKIPath: fixture.options.RootSPKIPath, ExpectedRootSHA256: fixture.options.ExpectedRootSHA256,
		BootstrapPolicyPath: fixture.options.BootstrapPolicyPath, ExpectedBootstrapPolicySHA256: fixture.options.ExpectedBootstrapPolicySHA256, ExpectedBootstrapPolicyEpoch: fixture.options.ExpectedBootstrapPolicyEpoch,
		FFmpegArchivePath: fixture.options.FFmpegArchivePath, FFmpegManifestPath: fixture.options.FFmpegManifestPath,
		ExpectedPrimaryIdentity: future, Now: fixture.options.Now,
		InspectAuthenticode: func(path string) (certidentity.Identity, error) {
			if filepath.Base(path) == "ffmpeg.exe" {
				return naisNetIdentity, nil
			}
			return future, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.OuterIdentity != future || evidence.FFmpegIdentity != naisNetIdentity {
		t.Fatalf("future candidate evidence = %#v", evidence)
	}
}

type enrollmentArtifactTestFixture struct {
	root                            string
	key                             *ecdsa.PrivateKey
	artifactSHA256, peContentSHA256 string
	options                         VerifyEnrollmentOptions
}

func enrollmentArtifactFixture(t testing.TB) enrollmentArtifactTestFixture {
	return enrollmentArtifactFixtureForVersion(t, "0.4.12", true)
}

func enrollmentArtifactFixtureForVersion(t testing.TB, version string, exactHash bool) enrollmentArtifactTestFixture {
	t.Helper()
	root := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootSPKI, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := signedEnrollmentPolicy(t, key, 1, "v0.4.12", "")
	ffmpeg := []byte("synthetic NaisNet FFmpeg enrollment fixture")
	archive, manifest := testFFmpegArchive(t, ffmpeg)
	commit := strings.Repeat("c", 40)
	payload := bytes.Join([][]byte{
		[]byte(version), []byte(commit),
		[]byte(base64.StdEncoding.EncodeToString(rootSPKI)),
		[]byte(base64.StdEncoding.EncodeToString(bootstrap)),
		archive, manifest,
	}, []byte{0})
	unsigned := syntheticPEWithPayload(t, payload)
	signedPath := writeSignedPE(t, root, unsigned)
	signed, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}
	artifactSHA256 := sha256Hex(signed)
	contentAddressedPath := filepath.Join(root, artifactSHA256+".exe")
	if err := os.Rename(signedPath, contentAddressedPath); err != nil {
		t.Fatal(err)
	}
	peContentSHA256, err := AuthenticodeContentSHA256(signed)
	if err != nil {
		t.Fatal(err)
	}
	authorizationHash := ""
	if exactHash {
		authorizationHash = artifactSHA256
	}
	authorization := signedEnrollmentPolicy(t, key, 2, "v"+version, authorizationHash)
	return enrollmentArtifactTestFixture{
		root: root, key: key, artifactSHA256: artifactSHA256, peContentSHA256: peContentSHA256,
		options: VerifyEnrollmentOptions{
			ArtifactPath: contentAddressedPath, ExpectedPEContentSHA256: peContentSHA256,
			Version: version, Tag: "v" + version, Commit: commit,
			RootSPKIPath: writeFixture(t, root, "root.der", rootSPKI), ExpectedRootSHA256: sha256Hex(rootSPKI),
			BootstrapPolicyPath: writeFixture(t, root, "bootstrap.json", bootstrap), ExpectedBootstrapPolicySHA256: sha256Hex(bootstrap), ExpectedBootstrapPolicyEpoch: 1,
			AuthorizationPolicyPath: writeFixture(t, root, "authorization.json", authorization), ExpectedAuthorizationPolicySHA256: sha256Hex(authorization), ExpectedAuthorizationPolicyEpoch: 2,
			FFmpegArchivePath: writeFixture(t, root, "ffmpeg.zip", archive), FFmpegManifestPath: writeFixture(t, root, "manifest.json", manifest),
			Now:                 time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			InspectAuthenticode: func(string) (certidentity.Identity, error) { return naisNetIdentity, nil },
		},
	}
}

func signedEnrollmentPolicy(t testing.TB, key *ecdsa.PrivateKey, epoch uint64, tag, manifestSHA256 string) []byte {
	return signedEnrollmentPolicyForIdentity(t, key, epoch, tag, manifestSHA256, naisNetIdentity)
}

func signedEnrollmentPolicyForIdentity(t testing.TB, key *ecdsa.PrivateKey, epoch uint64, tag, manifestSHA256 string, identity certidentity.Identity) []byte {
	t.Helper()
	rule := updatepolicy.PublisherRule{
		ID: "reviewed-stable", Role: "primary", Country: identity.Country, Organization: identity.Organization, OrganizationID: identity.OrganizationID,
		AllowedChannel: updatepolicy.ChannelStable, AllowedTags: []string{tag}, ManifestSHA256: manifestSHA256,
	}
	bridge := updatepolicy.PublisherRule{
		ID: "rushrush-bridge", Role: "bridge", Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P",
		AllowedChannel: updatepolicy.ChannelLegacyRushRush, AllowedTags: []string{"v0.4.11"},
	}
	signed := updatepolicy.Signed{Epoch: epoch, ExpiresAt: "2030-01-01T00:00:00Z", Publishers: []updatepolicy.PublisherRule{rule, bridge}}
	canonical, err := updatepolicy.CanonicalSigned(signed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(updatepolicy.Document{Signed: signed, Signatures: []updatepolicy.Signature{{Algorithm: "ecdsa-p256-sha256", Signature: base64.StdEncoding.EncodeToString(signature)}}})
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
