package securefile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapshotBytesRetainsExactPrivateRegularFile(t *testing.T) {
	want := []byte("exact signed executable snapshot")
	snapshot, err := SnapshotBytes(want, "securefile-test-", "signed.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	got, err := os.ReadFile(snapshot.Path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("snapshot bytes = %q, %v", got, err)
	}
	if err := snapshot.Revalidate(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotBytesDeniesReplacementWhileInspectorCanRead(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows share mode provides the production Authenticode lock")
	}
	snapshot, err := SnapshotBytes([]byte("locked signed executable"), "securefile-lock-test-", "signed.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if _, err := os.ReadFile(snapshot.Path); err != nil {
		t.Fatalf("read-only inspector access failed: %v", err)
	}
	if err := os.WriteFile(snapshot.Path, []byte("replacement"), 0o600); err == nil {
		t.Fatal("snapshot allowed replacement while retained")
	}
	if err := snapshot.Revalidate(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotSealsExactContentAddressedBytesFromRetainedHandle(t *testing.T) {
	want := []byte("exact verified signed executable")
	snapshot, err := SnapshotBytes(want, "securefile-seal-test-", "signed.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	digest := sha256.Sum256(want)
	wantDigest := hex.EncodeToString(digest[:])
	sealed, err := snapshot.SealContentAddressed(t.TempDir(), ".exe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Name != wantDigest+".exe" || sealed.SHA256 != wantDigest || sealed.Size != int64(len(want)) {
		t.Fatalf("sealed metadata = %#v", sealed)
	}
	got, err := os.ReadFile(filepath.Join(sealed.Directory, sealed.Name))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("sealed bytes = %q, %v", got, err)
	}
}

func TestExactDirectoryWriterRejectsLexicalTraversal(t *testing.T) {
	root := t.TempDir()
	destination := root + string(filepath.Separator) + "child" + string(filepath.Separator) + ".." + string(filepath.Separator) + "sealed"
	if err := os.MkdirAll(filepath.Clean(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteExactToDirectory(destination, "artifact.exe", []byte("verified"), nil); err == nil {
		t.Fatal("lexically traversing destination was accepted")
	}
}

func TestExactPairPublicationKeepsCommittedOutputsWhenBackupCleanupFails(t *testing.T) {
	directory := t.TempDir()
	for name, contents := range map[string][]byte{"ffmpeg.zip": []byte("old archive"), "manifest.json": []byte("old manifest")} {
		if err := os.WriteFile(filepath.Join(directory, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []ExactFile{{Name: "ffmpeg.zip", Bytes: []byte("new exact archive")}, {Name: "manifest.json", Bytes: []byte("new exact manifest")}}
	err := PublishExactFiles(directory, want, &SealHooks{BeforeBackupCleanup: func(index int) error {
		if index == 1 {
			return os.ErrPermission
		}
		return nil
	}})
	if err == nil {
		t.Fatal("injected backup cleanup failure was ignored")
	}
	for _, file := range want {
		got, readErr := os.ReadFile(filepath.Join(directory, file.Name))
		if readErr != nil || !bytes.Equal(got, file.Bytes) {
			t.Fatalf("committed %s rolled back after cleanup failure: bytes=%q error=%v", file.Name, got, readErr)
		}
	}
}
