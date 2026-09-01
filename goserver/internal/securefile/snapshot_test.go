package securefile

import (
	"bytes"
	"os"
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
