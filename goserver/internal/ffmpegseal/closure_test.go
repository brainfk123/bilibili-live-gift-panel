package ffmpegseal

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"bilibili-live-gift-panel/internal/certidentity"
)

var testNaisNet = certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}

func TestVerifyAndSealEmitsExactArchiveAndManifestBytes(t *testing.T) {
	archive, manifest, binaryBytes := closureFixture(t)
	root := t.TempDir()
	archivePath := writeClosureFile(t, root, "source.zip", archive)
	manifestPath := writeClosureFile(t, root, "source.json", manifest)
	sealedDirectory := filepath.Join(root, "sealed")
	if err := os.Mkdir(sealedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	evidence, err := VerifyAndSeal(Options{
		ArchivePath: archivePath, ManifestPath: manifestPath, SealedDirectory: sealedDirectory,
		InspectAuthenticode: exactNaisNetInspector(t, binaryBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Version != "9.0" || evidence.ArchiveSHA256 != testHashHex(archive) || evidence.ManifestSHA256 != testHashHex(manifest) || evidence.FFmpegSHA256 != testHashHex(binaryBytes) || evidence.FFmpegIdentity != testNaisNet {
		t.Fatalf("evidence = %#v", evidence)
	}
	for name, want := range map[string][]byte{"ffmpeg.zip": archive, "manifest.json": manifest} {
		got, readErr := os.ReadFile(filepath.Join(sealedDirectory, name))
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("sealed %s differs: error=%v", name, readErr)
		}
	}
}

func TestVerifyAndSealUsesRetainedSnapshotsWhenSourcesAreSwappedAfterSnapshot(t *testing.T) {
	archive, manifest, binaryBytes := closureFixture(t)
	root := t.TempDir()
	archivePath := writeClosureFile(t, root, "source.zip", archive)
	manifestPath := writeClosureFile(t, root, "source.json", manifest)
	sealedDirectory := filepath.Join(root, "sealed")
	if err := os.Mkdir(sealedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyAndSeal(Options{
		ArchivePath: archivePath, ManifestPath: manifestPath, SealedDirectory: sealedDirectory,
		InspectAuthenticode: exactNaisNetInspector(t, binaryBytes),
		Hooks: &Hooks{AfterSnapshots: func() error {
			if err := os.WriteFile(archivePath, []byte("attacker archive"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(manifestPath, []byte("attacker manifest"), 0o600)
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(sealedDirectory, "ffmpeg.zip")); !bytes.Equal(got, archive) {
		t.Fatal("sealed archive was reread from the swapped source")
	}
	if got, _ := os.ReadFile(filepath.Join(sealedDirectory, "manifest.json")); !bytes.Equal(got, manifest) {
		t.Fatal("sealed manifest was reread from the swapped source")
	}
}

func TestVerifyAndSealAcceptsExactImmutableNaisNetDisplaySubjectWithDERIdentity(t *testing.T) {
	archive, manifest, binaryBytes := closureFixture(t)
	var document map[string]any
	if err := json.Unmarshal(manifest, &document); err != nil {
		t.Fatal(err)
	}
	document["signer_subject"] = naisNetFixedSignerSubject
	manifest, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sealedDirectory := filepath.Join(root, "sealed")
	if err := os.Mkdir(sealedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndSeal(Options{
		ArchivePath: writeClosureFile(t, root, "source.zip", archive), ManifestPath: writeClosureFile(t, root, "source.json", manifest), SealedDirectory: sealedDirectory,
		InspectAuthenticode: exactNaisNetInspector(t, binaryBytes),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAndSealRejectsZipBombBeforeInflation(t *testing.T) {
	archive, manifest, binaryBytes := closureFixture(t)
	central := bytes.Index(archive, []byte{'P', 'K', 1, 2})
	local := bytes.Index(archive, []byte{'P', 'K', 3, 4})
	if central < 0 || local < 0 {
		t.Fatal("fixture ZIP headers missing")
	}
	bomb := append([]byte(nil), archive...)
	binary.LittleEndian.PutUint32(bomb[central+24:central+28], uint32(MaximumUncompressedBytes+1))
	binary.LittleEndian.PutUint32(bomb[local+22:local+26], uint32(MaximumUncompressedBytes+1))
	var document map[string]any
	if err := json.Unmarshal(manifest, &document); err != nil {
		t.Fatal(err)
	}
	document["archive_sha256"] = testHashHex(bomb)
	bombManifest, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sealedDirectory := filepath.Join(root, "sealed")
	if err := os.Mkdir(sealedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = VerifyAndSeal(Options{
		ArchivePath: writeClosureFile(t, root, "bomb.zip", bomb), ManifestPath: writeClosureFile(t, root, "bomb.json", bombManifest), SealedDirectory: sealedDirectory,
		InspectAuthenticode: exactNaisNetInspector(t, binaryBytes),
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "uncompressed") {
		t.Fatalf("ZIP bomb error = %v", err)
	}
}

func TestPublishSealedInstallsExactPairThroughRetainedDestination(t *testing.T) {
	archive, manifest, binaryBytes := closureFixture(t)
	root := t.TempDir()
	sealedDirectory := filepath.Join(root, "sealed")
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(sealedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	archivePath := writeClosureFile(t, sealedDirectory, "ffmpeg.zip", archive)
	manifestPath := writeClosureFile(t, sealedDirectory, "manifest.json", manifest)
	if _, err := PublishSealed(PublishOptions{
		ArchivePath: archivePath, ManifestPath: manifestPath, Destination: destination,
		InspectAuthenticode: exactNaisNetInspector(t, binaryBytes),
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"ffmpeg.zip": archive, "manifest.json": manifest} {
		got, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("installed %s differs: error=%v", name, err)
		}
	}
}

func TestPublishSealedRejectsDestinationJunctionIntroducedAfterOpen(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("production junction semantics are Windows-specific")
	}
	archive, manifest, binaryBytes := closureFixture(t)
	root := t.TempDir()
	sealedDirectory := filepath.Join(root, "sealed")
	destination := filepath.Join(root, "destination")
	outside := filepath.Join(root, "outside")
	for _, directory := range []string{sealedDirectory, destination, outside} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	archivePath := writeClosureFile(t, sealedDirectory, "ffmpeg.zip", archive)
	manifestPath := writeClosureFile(t, sealedDirectory, "manifest.json", manifest)
	_, err := PublishSealed(PublishOptions{
		ArchivePath: archivePath, ManifestPath: manifestPath, Destination: destination,
		InspectAuthenticode: exactNaisNetInspector(t, binaryBytes),
		Hooks: &PublishHooks{AfterOpenDestination: func() error {
			parked := destination + ".parked"
			if renameErr := os.Rename(destination, parked); renameErr != nil {
				return renameErr
			}
			if linkErr := os.Symlink(outside, destination); linkErr != nil {
				_ = os.Rename(parked, destination)
				return linkErr
			}
			return nil
		}},
	})
	if err == nil {
		t.Fatal("destination junction-at-write was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "ffmpeg.zip")); !os.IsNotExist(statErr) {
		t.Fatalf("outside destination was modified: %v", statErr)
	}
}

func closureFixture(t testing.TB) ([]byte, []byte, []byte) {
	t.Helper()
	binaryBytes := []byte("synthetic NaisNet signed FFmpeg executable")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	header := &zip.FileHeader{Name: "ffmpeg.exe", Method: zip.Deflate}
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(binaryBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	descriptor := "schema=2\nfixture=true\n"
	gate := "binary fixture gate\n"
	document := map[string]any{
		"schema": 1, "component_fingerprint": testHashHex([]byte(descriptor)), "descriptor": descriptor,
		"descriptor_sha256": testHashHex([]byte(descriptor)), "version": "9.0", "sha256": testHashHex(binaryBytes),
		"archive_sha256": testHashHex(archive.Bytes()), "component_gate": gate, "component_gate_sha256": testHashHex([]byte(gate)),
		"size": len(binaryBytes), "authenticode": true,
		"signer_subject":        "C=CN;O=NaisNet Technology Co., Ltd.;SERIALNUMBER=91210103MA7CJ3C094",
		"source_release_commit": strings.Repeat("a", 40),
	}
	manifest, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), archive.Bytes()...), manifest, binaryBytes
}

func exactNaisNetInspector(t testing.TB, want []byte) func(string) (certidentity.Identity, error) {
	t.Helper()
	return func(path string) (certidentity.Identity, error) {
		got, err := os.ReadFile(path)
		if err != nil {
			return certidentity.Identity{}, err
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("inspected FFmpeg differs from archived entry")
		}
		return testNaisNet, nil
	}
}

func writeClosureFile(t testing.TB, root, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testHashHex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
