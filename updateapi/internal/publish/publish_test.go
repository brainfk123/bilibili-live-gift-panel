package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
)

func TestRunPublishesAndVerifiesVersionedObjectsBeforeStablePointer(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()

	if err := Run(context.Background(), store, input); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe",
		"PUT releases/v1.2.3/gift-panel-windows-x64.exe",
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe",
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe.sha256",
		"PUT releases/v1.2.3/gift-panel-windows-x64.exe.sha256",
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe.sha256",
		"HEAD releases/v1.2.3/gift-panel-changelog.json",
		"PUT releases/v1.2.3/gift-panel-changelog.json",
		"HEAD releases/v1.2.3/gift-panel-changelog.json",
		"HEAD releases/v1.2.3/release.json",
		"PUT releases/v1.2.3/release.json",
		"HEAD releases/v1.2.3/release.json",
		"PUT channels/stable/latest.json",
		"GET channels/stable/latest.json",
	}
	if got := strings.Join(store.operations, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("operations =\n%s\nwant\n%s", got, strings.Join(want, "\n"))
	}
	if _, ok := store.objects["channels/stable/latest.json"]; !ok {
		t.Fatal("stable pointer was not written")
	}
}

func TestRunDoesNotRewriteMatchingVersionedObjects(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.seedVersioned(t, input)

	if err := Run(context.Background(), store, input); err != nil {
		t.Fatal(err)
	}
	for _, operation := range store.operations {
		if strings.HasPrefix(operation, "PUT releases/") {
			t.Fatalf("operation %q rewrote an immutable versioned object", operation)
		}
	}
	if got := store.operations[len(store.operations)-2:]; strings.Join(got, ",") != "PUT channels/stable/latest.json,GET channels/stable/latest.json" {
		t.Fatalf("final operations = %v, want stable put then readback", got)
	}
}

func TestRunRejectsMismatchedVersionedObjectBeforeStablePointer(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.objects["releases/v1.2.3/gift-panel-windows-x64.exe"] = storedObject{body: []byte("different"), digest: strings.Repeat("a", 64)}

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want immutable object mismatch rejection")
	}
	if store.hasStablePut() {
		t.Fatal("stable pointer was modified after immutable object mismatch")
	}
}

func TestRunRejectsBadChecksumBeforeStablePointer(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	if err := os.WriteFile(input.ChecksumPath, []byte(strings.Repeat("0", 64)+"  gift-panel-windows-x64.exe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want checksum mismatch rejection")
	}
	if len(store.operations) != 0 {
		t.Fatalf("operations = %v, want no COS access before validation", store.operations)
	}
}

func TestRunRejectsMalformedChangelogBeforeStablePointer(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[]}`)
	store := newMemoryStore()

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want malformed changelog rejection")
	}
	if len(store.operations) != 0 {
		t.Fatalf("operations = %v, want no COS access before validation", store.operations)
	}
}

func TestRunLeavesStableUntouchedWhenVersionedUploadFails(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.putError = errors.New("network interrupted")

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want versioned upload failure")
	}
	if store.hasStablePut() {
		t.Fatal("stable pointer was modified after versioned upload failure")
	}
}

func TestRunRejectsStableReadbackMismatch(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.stableReadback = []byte(`{"schemaVersion":1,"tagName":"v9.9.9","publishedAt":"2026-08-14T12:00:00Z","asset":{"name":"gift-panel-windows-x64.exe","objectKey":"releases/v9.9.9/gift-panel-windows-x64.exe","size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"changelogObjectKey":"releases/v9.9.9/gift-panel-changelog.json"}`)

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want stable readback mismatch")
	}
	if !store.hasStablePut() {
		t.Fatal("stable pointer was not written before its readback check")
	}
}

func writeInput(t *testing.T, asset, changelog string) Input {
	t.Helper()
	directory := t.TempDir()
	assetPath := filepath.Join(directory, "gift-panel-windows-x64.exe")
	checksumPath := filepath.Join(directory, "gift-panel-windows-x64.exe.sha256")
	changelogPath := filepath.Join(directory, "gift-panel-changelog.json")
	if err := os.WriteFile(assetPath, []byte(asset), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(asset))
	if err := os.WriteFile(checksumPath, []byte(hex.EncodeToString(digest[:])+"  gift-panel-windows-x64.exe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changelogPath, []byte(changelog), 0o600); err != nil {
		t.Fatal(err)
	}
	return Input{Tag: "v1.2.3", AssetPath: assetPath, ChecksumPath: checksumPath, ChangelogPath: changelogPath, PublishedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)}
}

type storedObject struct {
	body   []byte
	digest string
}

type memoryStore struct {
	objects        map[string]storedObject
	operations     []string
	putError       error
	stableReadback []byte
}

func newMemoryStore() *memoryStore {
	return &memoryStore{objects: make(map[string]storedObject)}
}

func (store *memoryStore) Head(_ context.Context, key string) (cosstore.ObjectInfo, error) {
	store.operations = append(store.operations, "HEAD "+key)
	object, ok := store.objects[key]
	if !ok {
		return cosstore.ObjectInfo{}, cosstore.ErrNotFound
	}
	return cosstore.ObjectInfo{Size: int64(len(object.body)), SHA256: object.digest}, nil
}

func (store *memoryStore) Put(_ context.Context, key string, body io.Reader, size int64, _ string, digest string) error {
	store.operations = append(store.operations, "PUT "+key)
	if store.putError != nil && key != "channels/stable/latest.json" {
		return store.putError
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("incorrect object size")
	}
	store.objects[key] = storedObject{body: data, digest: digest}
	return nil
}

func (store *memoryStore) Get(_ context.Context, key string, _ int64) ([]byte, string, error) {
	store.operations = append(store.operations, "GET "+key)
	if key == "channels/stable/latest.json" && store.stableReadback != nil {
		return append([]byte(nil), store.stableReadback...), "", nil
	}
	object, ok := store.objects[key]
	if !ok {
		return nil, "", cosstore.ErrNotFound
	}
	return append([]byte(nil), object.body...), "", nil
}

func (store *memoryStore) hasStablePut() bool {
	for _, operation := range store.operations {
		if operation == "PUT channels/stable/latest.json" {
			return true
		}
	}
	return false
}

func (store *memoryStore) seedVersioned(t *testing.T, input Input) {
	t.Helper()
	asset, err := os.ReadFile(input.AssetPath)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := os.ReadFile(input.ChecksumPath)
	if err != nil {
		t.Fatal(err)
	}
	changelog, err := os.ReadFile(input.ChangelogPath)
	if err != nil {
		t.Fatal(err)
	}
	assetDigest := sha256.Sum256(asset)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":1,"tagName":"v1.2.3","publishedAt":"2026-08-14T12:00:00Z","asset":{"name":"gift-panel-windows-x64.exe","objectKey":"releases/v1.2.3/gift-panel-windows-x64.exe","size":%d,"sha256":"%x"},"changelogObjectKey":"releases/v1.2.3/gift-panel-changelog.json"}`, len(asset), assetDigest))
	for key, body := range map[string][]byte{
		"releases/v1.2.3/gift-panel-windows-x64.exe":        asset,
		"releases/v1.2.3/gift-panel-windows-x64.exe.sha256": checksum,
		"releases/v1.2.3/gift-panel-changelog.json":         changelog,
		"releases/v1.2.3/release.json":                      manifest,
	} {
		digest := sha256.Sum256(body)
		store.objects[key] = storedObject{body: body, digest: hex.EncodeToString(digest[:])}
	}
}
