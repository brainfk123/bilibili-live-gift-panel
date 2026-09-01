package artifactinspect

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
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

type boundArtifactTestFixture struct {
	root    string
	options VerifyArtifactOptions
}

func boundArtifactFixture(t testing.TB) boundArtifactTestFixture {
	t.Helper()
	root := t.TempDir()
	rootSPKI, err := os.ReadFile(filepath.Join("..", "..", "testdata", "update-trust", "root-epoch-1-spki.der"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "testdata", "update-trust", "policy-epoch-1.json"))
	if err != nil {
		t.Fatal(err)
	}
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
		Version:              version,
		Tag:                  "v" + version,
		Commit:               commit,
		RootSPKIPath:         writeFixture(t, root, "root.der", rootSPKI),
		ExpectedRootSHA256:   sha256Hex(rootSPKI),
		PolicyPath:           writeFixture(t, root, "policy.json", policy),
		ExpectedPolicySHA256: sha256Hex(policy),
		ExpectedPolicyEpoch:  1,
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

func syntheticPEWithPayload(t testing.TB, payload []byte) []byte {
	t.Helper()
	size := 1024 + len(payload)
	binary := make([]byte, size)
	binary[0], binary[1] = 'M', 'Z'
	putUint32(binary[0x3c:0x40], 0x80)
	copy(binary[0x80:0x84], []byte{'P', 'E', 0, 0})
	binary[0x98], binary[0x99] = 0x0b, 0x02
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
