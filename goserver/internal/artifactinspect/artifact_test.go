package artifactinspect

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

func TestVerifyBoundArtifactAcceptsOnlyTheBuiltSignedEnrollmentClosure(t *testing.T) {
	fixture := boundArtifactFixture(t)
	evidence, err := VerifyBoundArtifact(fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Version != "0.4.11" || evidence.Tag != "v0.4.11" || evidence.Commit != strings.Repeat("a", 40) || evidence.PolicyEpoch != 1 {
		t.Fatalf("evidence = %#v", evidence)
	}
	if evidence.OuterIdentity.Organization != "RushRush Network Technology Ltd" || evidence.FFmpegIdentity.Organization != "NaisNet Technology Co., Ltd." {
		t.Fatalf("identities = %#v / %#v", evidence.OuterIdentity, evidence.FFmpegIdentity)
	}
}

func TestVerifyBoundArtifactRejectsSignedBinarySubstitutionAndModifiedPESection(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*boundArtifactTestFixture)
	}{
		{name: "different RushRush signed binary", mutate: func(f *boundArtifactTestFixture) {
			f.options.SignedPath = writeSignedPE(t, f.root, syntheticPE(t, 0x52))
		}},
		{name: "modified PE section", mutate: func(f *boundArtifactTestFixture) {
			contents, _ := os.ReadFile(f.options.SignedPath)
			contents[400] ^= 0xff
			if err := os.WriteFile(f.options.SignedPath, contents, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := boundArtifactFixture(t)
			test.mutate(&fixture)
			if _, err := VerifyBoundArtifact(fixture.options); err == nil || !strings.Contains(err.Error(), "unsigned PE content") {
				t.Fatalf("substitution error = %v", err)
			}
		})
	}
}

func TestVerifyBoundArtifactRejectsReplacedEmbeddedFFmpegAndTrustBytes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*boundArtifactTestFixture)
		want   string
	}{
		{name: "replaced FFmpeg", want: "embedded FFmpeg", mutate: func(f *boundArtifactTestFixture) {
			replacement, _ := testFFmpegArchive(t, []byte("different-ffmpeg"))
			f.options.FFmpegArchivePath = writeFixture(t, f.root, "replacement-ffmpeg.zip", replacement)
		}},
		{name: "changed root", want: "embedded trust", mutate: func(f *boundArtifactTestFixture) {
			f.options.RootSPKIPath = writeFixture(t, f.root, "other-root.der", []byte("changed-root"))
			f.options.ExpectedRootSHA256 = sha256Hex([]byte("changed-root"))
		}},
		{name: "changed policy", want: "embedded trust", mutate: func(f *boundArtifactTestFixture) {
			f.options.PolicyPath = writeFixture(t, f.root, "other-policy.json", []byte("changed-policy"))
			f.options.ExpectedPolicySHA256 = sha256Hex([]byte("changed-policy"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := boundArtifactFixture(t)
			test.mutate(&fixture)
			if _, err := VerifyBoundArtifact(fixture.options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("replacement error = %v", err)
			}
		})
	}
}

func TestVerifyBoundArtifactRejectsUnscopedStableConvergencePolicy(t *testing.T) {
	fixture := boundArtifactFixtureWithScope(t, false)
	if _, err := VerifyBoundArtifact(fixture.options); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("unscoped convergence policy error = %v", err)
	}
}

func TestVerifyStaticArtifactBindsStableOuterAndRejectsArbitrarySignerOutputBytes(t *testing.T) {
	fixture := boundArtifactFixture(t)
	options := VerifyStaticOptions{UnsignedPath: fixture.options.UnsignedPath, SignedPath: fixture.options.SignedPath, SealedDirectory: t.TempDir(), Version: "0.4.11", Commit: strings.Repeat("a", 40), ExpectedIdentity: naisNetIdentity, FFmpegArchivePath: fixture.options.FFmpegArchivePath, FFmpegManifestPath: fixture.options.FFmpegManifestPath, InspectAuthenticode: func(string) (certidentity.Identity, error) { return naisNetIdentity, nil }}
	if _, err := VerifyStaticArtifact(options); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(options.SignedPath)
	contents[600] ^= 0xff
	_ = os.WriteFile(options.SignedPath, contents, 0o600)
	if _, err := VerifyStaticArtifact(options); err == nil {
		t.Fatal("arbitrary signed response bytes accepted")
	}
}

func TestVerifyStaticArtifactSealsTheExactSnapshotInspectedBeforeSourceSwap(t *testing.T) {
	fixture := boundArtifactFixture(t)
	original, err := os.ReadFile(fixture.options.SignedPath)
	if err != nil {
		t.Fatal(err)
	}
	sealedDirectory := t.TempDir()
	options := VerifyStaticOptions{
		UnsignedPath: fixture.options.UnsignedPath, SignedPath: fixture.options.SignedPath,
		SealedDirectory: sealedDirectory, Version: "0.4.11", Commit: strings.Repeat("a", 40), ExpectedIdentity: naisNetIdentity,
		FFmpegArchivePath: fixture.options.FFmpegArchivePath, FFmpegManifestPath: fixture.options.FFmpegManifestPath,
		InspectAuthenticode: func(path string) (certidentity.Identity, error) {
			if filepath.Base(path) == "signed.exe" {
				if err := os.WriteFile(fixture.options.SignedPath, syntheticPEWithPayload(t, []byte("attacker replacement")), 0o600); err != nil {
					return certidentity.Identity{}, err
				}
			}
			return naisNetIdentity, nil
		},
	}
	evidence, err := VerifyStaticArtifact(options)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256Hex(original)
	if evidence.SignedFileSHA256 != wantHash || evidence.SignedFileSize != int64(len(original)) {
		t.Fatalf("sealed evidence = %#v", evidence)
	}
	sealed, err := os.ReadFile(filepath.Join(sealedDirectory, wantHash+".exe"))
	if err != nil || !bytes.Equal(sealed, original) {
		t.Fatalf("sealed executable differs from inspected snapshot: error=%v", err)
	}
}

func TestVerifyBoundArtifactSealsTheExactInspectedBridgeSnapshot(t *testing.T) {
	fixture := boundArtifactFixture(t)
	original, err := os.ReadFile(fixture.options.SignedPath)
	if err != nil {
		t.Fatal(err)
	}
	call := 0
	fixture.options.InspectAuthenticode = func(path string) (certidentity.Identity, error) {
		call++
		if filepath.Base(path) == "signed.exe" {
			if err := os.WriteFile(fixture.options.SignedPath, syntheticPEWithPayload(t, []byte("attacker bridge replacement")), 0o600); err != nil {
				return certidentity.Identity{}, err
			}
			return rushRushIdentity, nil
		}
		if call == 1 || filepath.Base(path) == "ffmpeg.exe" {
			return naisNetIdentity, nil
		}
		return certidentity.Identity{}, nil
	}
	evidence, err := VerifyBoundArtifact(fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256Hex(original)
	if evidence.SignedFileSHA256 != wantHash || evidence.SignedFileSize != int64(len(original)) {
		t.Fatalf("sealed evidence = %#v", evidence)
	}
	sealed, err := os.ReadFile(filepath.Join(fixture.options.SealedDirectory, wantHash+".exe"))
	if err != nil || !bytes.Equal(sealed, original) {
		t.Fatalf("sealed bridge executable differs from inspected snapshot: error=%v", err)
	}
}

func TestVerifyStablePolicyUsesActualSignerAndTheSharedAuthorizationMatcher(t *testing.T) {
	hash := strings.Repeat("1", 64)
	root, policy := scopedPolicyFixture(t, hash, true)
	actual := updatepolicy.ArtifactIdentity{
		Tag: "v0.4.12", Channel: updatepolicy.ChannelStable, SHA256: hash,
		Certificate: certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"},
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if epoch, err := VerifyStablePolicy(root, policy, 1, actual, now); err != nil || epoch != 1 {
		t.Fatalf("actual stable artifact was not authorized: epoch=%d error=%v", epoch, err)
	}
	mutations := map[string]func(*updatepolicy.ArtifactIdentity){
		"organization": func(value *updatepolicy.ArtifactIdentity) { value.Certificate.Organization = "Wrong Organization" },
		"hash":         func(value *updatepolicy.ArtifactIdentity) { value.SHA256 = strings.Repeat("2", 64) },
		"channel":      func(value *updatepolicy.ArtifactIdentity) { value.Channel = updatepolicy.ChannelLegacyRushRush },
		"tag":          func(value *updatepolicy.ArtifactIdentity) { value.Tag = "v0.4.13" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := actual
			mutate(&candidate)
			if _, err := VerifyStablePolicy(root, policy, 1, candidate, now); err == nil {
				t.Fatal("mutated actual artifact was authorized")
			}
		})
	}
}

func TestVerifyStableArtifactPolicyRejectsMutationDuringAuthenticodeInspection(t *testing.T) {
	stable := []byte("actual immutable stable artifact")
	hash := sha256Hex(stable)
	root, policy := scopedPolicyFixture(t, hash, true)
	artifactPath := writeFixture(t, t.TempDir(), "stable.exe", stable)
	identity := certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	_, err := VerifyStableArtifactPolicy(StablePolicyOptions{
		RootDER: root, PolicyBytes: policy, ExpectedEpoch: 1, ArtifactPath: artifactPath,
		Tag: "v0.4.12", Channel: updatepolicy.ChannelStable, Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		InspectAuthenticode: func(path string) (certidentity.Identity, error) {
			if err := os.WriteFile(path, []byte("substituted during inspection"), 0o600); err != nil {
				return certidentity.Identity{}, err
			}
			return identity, nil
		},
	})
	if err == nil {
		t.Fatal("stable artifact mutation during Authenticode inspection was accepted")
	}
}

func TestVerifyStableArtifactPolicyRejectsSignerFromSwapAndRestoredArtifact(t *testing.T) {
	stable := []byte("policy-authorized stable artifact")
	substitute := []byte("different NaisNet-signed artifact")
	hash := sha256Hex(stable)
	root, policy := scopedPolicyFixture(t, hash, true)
	artifactPath := writeFixture(t, t.TempDir(), "stable.exe", stable)
	naisnet := certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	wrong := certidentity.Identity{Country: "CN", Organization: "Wrong signer", OrganizationID: "wrong"}

	_, err := VerifyStableArtifactPolicy(StablePolicyOptions{
		RootDER: root, PolicyBytes: policy, ExpectedEpoch: 1, ArtifactPath: artifactPath,
		Tag: "v0.4.12", Channel: updatepolicy.ChannelStable, Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		InspectAuthenticode: func(inspectedPath string) (certidentity.Identity, error) {
			inspected := inspectWhileOriginalIsSwapped(t, artifactPath, inspectedPath, substitute)
			if bytes.Equal(inspected, substitute) {
				return naisnet, nil
			}
			return wrong, nil
		},
	})
	if err == nil {
		t.Fatal("hash from the stable artifact was combined with a signer from a swap-and-restored file")
	}
}

func TestVerifyBoundArtifactRejectsOuterSignerFromSwapAndRestoredArtifact(t *testing.T) {
	fixture := boundArtifactFixture(t)
	substitute := syntheticPEWithPayload(t, []byte("different RushRush-signed bridge"))
	call := 0
	fixture.options.InspectAuthenticode = func(inspectedPath string) (certidentity.Identity, error) {
		call++
		switch call {
		case 1:
			return naisNetIdentity, nil // stable convergence snapshot
		case 2:
			inspected := inspectWhileOriginalIsSwapped(t, fixture.options.SignedPath, inspectedPath, substitute)
			if bytes.Equal(inspected, substitute) {
				return rushRushIdentity, nil
			}
			return naisNetIdentity, nil
		default:
			return naisNetIdentity, nil // embedded FFmpeg snapshot
		}
	}
	if _, err := VerifyBoundArtifact(fixture.options); err == nil {
		t.Fatal("outer bridge hash/content was combined with a signer from a swap-and-restored file")
	}
}

func inspectWhileOriginalIsSwapped(t testing.TB, originalPath, inspectedPath string, substitute []byte) []byte {
	t.Helper()
	parked := originalPath + ".parked"
	if err := os.Rename(originalPath, parked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, substitute, 0o600); err != nil {
		_ = os.Rename(parked, originalPath)
		t.Fatal(err)
	}
	inspected, readErr := os.ReadFile(inspectedPath)
	removeErr := os.Remove(originalPath)
	restoreErr := os.Rename(parked, originalPath)
	if readErr != nil || removeErr != nil || restoreErr != nil {
		t.Fatalf("swap/restore inspection failed: read=%v remove=%v restore=%v", readErr, removeErr, restoreErr)
	}
	return inspected
}

type boundArtifactTestFixture struct {
	root    string
	options VerifyArtifactOptions
}

func boundArtifactFixture(t testing.TB) boundArtifactTestFixture {
	return boundArtifactFixtureWithScope(t, true)
}

func boundArtifactFixtureWithScope(t testing.TB, scoped bool) boundArtifactTestFixture {
	t.Helper()
	root := t.TempDir()
	sealedDirectory := filepath.Join(root, "sealed")
	if err := os.Mkdir(sealedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stableArtifact := []byte("actual immutable stable artifact fixture")
	stableHash := sha256Hex(stableArtifact)
	rootSPKI, policy := scopedPolicyFixture(t, stableHash, scoped)
	ffmpeg := []byte("synthetic-naisnet-ffmpeg")
	archive, manifest := testFFmpegArchive(t, ffmpeg)
	version := "0.4.11"
	commit := strings.Repeat("a", 40)
	payload := bytes.Join([][]byte{
		[]byte(version), []byte(commit),
		[]byte(base64.StdEncoding.EncodeToString(rootSPKI)), []byte(base64.StdEncoding.EncodeToString(policy)),
		archive, manifest,
	}, []byte{0})
	unsigned := syntheticPEWithPayload(t, payload)
	unsignedPath := writeFixture(t, root, "unsigned.exe", unsigned)
	signedPath := writeSignedPE(t, root, unsigned)
	return boundArtifactTestFixture{root: root, options: VerifyArtifactOptions{
		UnsignedPath:         unsignedPath,
		SignedPath:           signedPath,
		SealedDirectory:      sealedDirectory,
		Version:              version,
		Tag:                  "v" + version,
		Commit:               commit,
		RootSPKIPath:         writeFixture(t, root, "root.der", rootSPKI),
		ExpectedRootSHA256:   sha256Hex(rootSPKI),
		PolicyPath:           writeFixture(t, root, "policy.json", policy),
		ExpectedPolicySHA256: sha256Hex(policy),
		ExpectedPolicyEpoch:  1,
		StableArtifactPath:   writeFixture(t, root, "stable.exe", stableArtifact),
		StableTag:            "v0.4.12",
		StableChannel:        updatepolicy.ChannelStable,
		FFmpegArchivePath:    writeFixture(t, root, "ffmpeg.zip", archive),
		FFmpegManifestPath:   writeFixture(t, root, "manifest.json", manifest),
		Now:                  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		InspectAuthenticode: func(path string) (certidentity.Identity, error) {
			if strings.HasSuffix(path, "signed.exe") {
				return certidentity.Identity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}, nil
			}
			return certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}, nil
		},
	}}
}

func scopedPolicyFixture(t testing.TB, stableHash string, scoped bool) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	rule := updatepolicy.PublisherRule{ID: "naisnet-primary", Role: "primary", Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094", AllowedChannel: updatepolicy.ChannelStable, AllowedTags: []string{"v0.4.12"}}
	if scoped {
		rule.ManifestSHA256 = stableHash
	}
	signed := updatepolicy.Signed{Epoch: 1, ExpiresAt: "2030-01-01T00:00:00Z", Publishers: []updatepolicy.PublisherRule{rule}}
	canonical, err := updatepolicy.CanonicalSigned(signed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(updatepolicy.Document{Signed: signed, Signatures: []updatepolicy.Signature{{Algorithm: "ecdsa-p256-sha256", Signature: base64.StdEncoding.EncodeToString(signature)}}})
	if err != nil {
		t.Fatal(err)
	}
	return root, policy
}

func syntheticPEWithPayload(t testing.TB, payload []byte) []byte {
	t.Helper()
	size := 1024 + len(payload)
	for size%8 != 0 {
		size++
	}
	binary := make([]byte, size)
	binary[0], binary[1] = 'M', 'Z'
	putUint32(binary[0x3c:0x40], 0x80)
	copy(binary[0x80:0x84], []byte{'P', 'E', 0, 0})
	binary[0x80+6], binary[0x80+7] = 1, 0
	binary[0x80+20], binary[0x80+21] = 0xf0, 0
	binary[0x98], binary[0x99] = 0x0b, 0x02
	putUint32(binary[0x98+60:0x98+64], 0x200)
	putUint32(binary[0x98+108:0x98+112], 16)
	section := 0x98 + 0xf0
	copy(binary[section:section+8], []byte(".text\x00\x00\x00"))
	putUint32(binary[section+8:section+12], uint32(len(binary)-0x200))
	putUint32(binary[section+12:section+16], 0x1000)
	putUint32(binary[section+16:section+20], uint32(len(binary)-0x200))
	putUint32(binary[section+20:section+24], 0x200)
	copy(binary[512:], payload)
	return binary
}

func writeSignedPE(t testing.TB, root string, unsigned []byte) string {
	t.Helper()
	signed := append(append([]byte(nil), unsigned...), bytes.Repeat([]byte{0xa5}, 32)...)
	securityDirectory := 0x98 + 112 + 8*4
	putUint32(signed[securityDirectory:securityDirectory+4], uint32(len(unsigned)))
	putUint32(signed[securityDirectory+4:securityDirectory+8], 32)
	return writeFixture(t, root, "signed.exe", signed)
}

func testFFmpegArchive(t testing.TB, ffmpeg []byte) ([]byte, []byte) {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("ffmpeg.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(ffmpeg); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("{\"version\":\"9.0\",\"sha256\":\"" + sha256Hex(ffmpeg) + "\",\"size\":" + strconv.Itoa(len(ffmpeg)) + ",\"authenticode\":true}")
	return archive.Bytes(), manifest
}

func writeFixture(t testing.TB, root, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sha256Hex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
