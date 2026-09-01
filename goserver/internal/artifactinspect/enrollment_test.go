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
		evidence.BootstrapPolicyEpoch != 1 || evidence.AuthorizationPolicyEpoch != 2 || evidence.AuthorizedArtifactSHA256 != fixture.artifactSHA256 ||
		evidence.OuterIdentity != naisNetIdentity || evidence.AuthorizedIdentity != naisNetIdentity || evidence.FFmpegIdentity != naisNetIdentity ||
		evidence.AuthenticodeStatus != "Valid" || evidence.BootstrapSignatureStatus != "Valid" || evidence.AuthorizationSignatureStatus != "Valid" || evidence.FFmpegSignatureStatus != "Valid" {
		t.Fatalf("enrollment evidence = %#v", evidence)
	}
}

func TestVerifyEnrollmentArtifactRejectsWrongFinalHashAuthorization(t *testing.T) {
	fixture := enrollmentArtifactFixture(t)
	wrong := signedEnrollmentPolicy(t, fixture.key, 2, strings.Repeat("0", 64))
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
	if evidence.SchemaVersion != 1 || evidence.AuthorizedArtifactSHA256 != fixture.artifactSHA256 || evidence.AuthorizedIdentity != naisNetIdentity || evidence.BootstrapSignatureStatus != "Valid" || evidence.AuthorizationSignatureStatus != "Valid" {
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

type enrollmentArtifactTestFixture struct {
	root                            string
	key                             *ecdsa.PrivateKey
	artifactSHA256, peContentSHA256 string
	options                         VerifyEnrollmentOptions
}

func enrollmentArtifactFixture(t testing.TB) enrollmentArtifactTestFixture {
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
	bootstrap := signedEnrollmentPolicy(t, key, 1, "")
	ffmpeg := []byte("synthetic NaisNet FFmpeg enrollment fixture")
	archive, manifest := testFFmpegArchive(t, ffmpeg)
	version := "0.4.12"
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
	authorization := signedEnrollmentPolicy(t, key, 2, artifactSHA256)
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

func signedEnrollmentPolicy(t testing.TB, key *ecdsa.PrivateKey, epoch uint64, manifestSHA256 string) []byte {
	t.Helper()
	rule := updatepolicy.PublisherRule{
		ID: "naisnet-stable", Role: "primary", Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094",
		AllowedChannel: updatepolicy.ChannelStable, AllowedTags: []string{"v0.4.12"}, ManifestSHA256: manifestSHA256,
	}
	signed := updatepolicy.Signed{Epoch: epoch, ExpiresAt: "2030-01-01T00:00:00Z", Publishers: []updatepolicy.PublisherRule{rule}}
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
