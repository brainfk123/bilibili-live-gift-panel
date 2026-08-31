package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

func TestBridgePublishCannotMutateStable(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"0.4.11"}]}`)
	input.Channel = release.ChannelLegacyRushRush
	input.Tag = "v0.4.11"
	store := newMemoryStore()

	outcome, err := Publish(context.Background(), store, input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeLegacyPromoted {
		t.Fatalf("Publish() outcome = %q, want %q", outcome, OutcomeLegacyPromoted)
	}
	if store.hasStablePut() {
		t.Fatal("bridge wrote stable pointer")
	}
	if !containsOperation(store.operations, "PUT channels/legacy-rushrush/latest.json") {
		t.Fatal("legacy pointer was not written")
	}
	var manifest release.ChannelManifest
	if err := json.Unmarshal(store.objects["channels/legacy-rushrush/latest.json"].body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 || manifest.Channel != release.ChannelLegacyRushRush {
		t.Fatalf("legacy manifest = %#v, want schema 2 legacy channel", manifest)
	}
}

func TestStablePublishCannotMutateLegacyOrUseBridgeTag(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"0.4.11"}]}`)
	input.Tag = "v0.4.11"
	store := newMemoryStore()

	if _, err := Publish(context.Background(), store, input); err == nil {
		t.Fatal("stable publisher accepted reserved bridge tag")
	}
	if len(store.operations) != 0 {
		t.Fatalf("stable bridge rejection touched COS: %v", store.operations)
	}
	if _, exists := store.objects["channels/legacy-rushrush/latest.json"]; exists {
		t.Fatal("stable publisher wrote legacy pointer")
	}
}

func TestPublishRejectsUnknownChannelBeforeCOS(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	input.Channel = release.Channel("preview")
	store := newMemoryStore()
	if _, err := Publish(context.Background(), store, input); err == nil {
		t.Fatal("Publish() accepted unknown channel")
	}
	if len(store.operations) != 0 {
		t.Fatalf("unknown channel touched COS: %v", store.operations)
	}
}

func TestRunPublishesAndVerifiesVersionedObjectsBeforeStablePointer(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()

	outcome, err := Publish(context.Background(), store, input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeStablePromoted {
		t.Fatalf("Publish() outcome = %q, want %q", outcome, OutcomeStablePromoted)
	}

	want := []string{
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe",
		"PUT-IMMUTABLE releases/v1.2.3/gift-panel-windows-x64.exe",
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe",
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe.sha256",
		"PUT-IMMUTABLE releases/v1.2.3/gift-panel-windows-x64.exe.sha256",
		"HEAD releases/v1.2.3/gift-panel-windows-x64.exe.sha256",
		"HEAD releases/v1.2.3/gift-panel-changelog.json",
		"PUT-IMMUTABLE releases/v1.2.3/gift-panel-changelog.json",
		"HEAD releases/v1.2.3/gift-panel-changelog.json",
		"HEAD releases/v1.2.3/release.json",
		"PUT-IMMUTABLE releases/v1.2.3/release.json",
		"HEAD releases/v1.2.3/release.json",
		"GET channels/stable/latest.json",
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

func TestRunRejectsConcurrentImmutableWriteConflictBeforeStablePointer(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.immutablePutError = cosstore.ErrAlreadyExists

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want atomic immutable-write conflict")
	}
	if !containsOperation(store.operations, "PUT-IMMUTABLE releases/v1.2.3/gift-panel-windows-x64.exe") {
		t.Fatalf("operations = %v, want atomic immutable write", store.operations)
	}
	if store.hasStablePut() {
		t.Fatal("stable pointer was modified after immutable-write conflict")
	}
}

func TestRunRejectsNonCanonicalOrUnsafeTagBeforeCOSAccess(t *testing.T) {
	for _, tag := range []string{"1.2.3", "v01.2.3", "v1.2.3+build", "v1.2.3+extra/path", "v1.2.3/../../stable", "v1.2.3\\escape"} {
		t.Run(tag, func(t *testing.T) {
			input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
			input.Tag = tag
			store := newMemoryStore()

			if err := Run(context.Background(), store, input); err == nil {
				t.Fatal("Run() error = nil, want canonical safe tag rejection")
			}
			if len(store.operations) != 0 {
				t.Fatalf("operations = %v, want no COS access for rejected tag", store.operations)
			}
		})
	}
}

func TestRunDoesNotRewriteMatchingVersionedObjects(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.seedVersioned(t, input)

	outcome, err := Publish(context.Background(), store, input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeStablePromoted {
		t.Fatalf("Publish() outcome = %q, want %q", outcome, OutcomeStablePromoted)
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
	store.immutablePutError = errors.New("network interrupted")

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want versioned upload failure")
	}
	if store.hasStablePut() {
		t.Fatal("stable pointer was modified after versioned upload failure")
	}
}

func TestRunRestoresValidatedPriorStableAfterReadbackMismatch(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	prior := stableManifest("v1.1.0")
	mismatch := stableManifest("v9.9.9")
	store.stableGets = []stableGetResult{{body: prior}, {body: mismatch}, {body: prior}}
	store.objects[stableKey] = storedObjectFor(prior)

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want restored promotion failure")
	}
	if strings.Contains(err.Error(), "promotion outcome is indeterminate") {
		t.Fatalf("Run() error = %v, prior stable was restored and verified", err)
	}
	if got := store.objects[stableKey].body; string(got) != string(prior) {
		t.Fatalf("stable body = %s, want restored prior %s", got, prior)
	}
	wantTail := "GET channels/stable/latest.json,PUT channels/stable/latest.json,GET channels/stable/latest.json,PUT channels/stable/latest.json,GET channels/stable/latest.json"
	if got := strings.Join(store.operations[len(store.operations)-5:], ","); got != wantTail {
		t.Fatalf("final operations = %s, want %s", got, wantTail)
	}
}

func TestRunReturnsPromotionIndeterminateWhenRollbackFails(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	prior := stableManifest("v1.1.0")
	store.stableGets = []stableGetResult{{body: prior}, {body: stableManifest("v9.9.9")}}
	store.stablePutErrors = []error{nil, errors.New("restore denied")}
	store.objects[stableKey] = storedObjectFor(prior)

	err := Run(context.Background(), store, input)
	if err == nil || !errors.Is(err, ErrPromotionIndeterminate) || !strings.Contains(err.Error(), "promotion outcome is indeterminate") {
		t.Fatalf("Run() error = %v, want distinct promotion-indeterminate error", err)
	}
}

func TestRunRestoresValidatedPriorStableAfterReadbackError(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	prior := stableManifest("v1.1.0")
	store.stableGets = []stableGetResult{{body: prior}, {err: errors.New("read timeout")}, {body: prior}}
	store.objects[stableKey] = storedObjectFor(prior)

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want restored readback failure")
	}
	if errors.Is(err, ErrPromotionIndeterminate) {
		t.Fatalf("Run() error = %v, prior stable was restored and verified", err)
	}
	if got := store.objects[stableKey].body; string(got) != string(prior) {
		t.Fatalf("stable body = %s, want restored prior %s", got, prior)
	}
}

func TestRunReturnsPromotionIndeterminateWithoutPriorStable(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.stableGets = []stableGetResult{{err: cosstore.ErrNotFound}, {body: stableManifest("v9.9.9")}}

	err := Run(context.Background(), store, input)
	if err == nil || !errors.Is(err, ErrPromotionIndeterminate) || !strings.Contains(err.Error(), "promotion outcome is indeterminate") {
		t.Fatalf("Run() error = %v, want no-prior promotion-indeterminate error", err)
	}
}

func TestRunRejectsInvalidPriorStableBeforePromotion(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	store.stableGets = []stableGetResult{{body: []byte(`{"schemaVersion":99}`)}}

	err := Run(context.Background(), store, input)
	if err == nil {
		t.Fatal("Run() error = nil, want invalid prior stable rejection")
	}
	if store.hasStablePut() {
		t.Fatal("stable pointer was modified after prior stable validation failed")
	}
}

func TestPublishDoesNotDowngradeNewerStableAfterMirroringImmutableObjects(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	prior := stableManifest("v1.3.0")
	store.objects[stableKey] = storedObjectFor(prior)

	outcome, err := Publish(context.Background(), store, input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeStableUnchanged {
		t.Fatalf("Publish() outcome = %q, want %q", outcome, OutcomeStableUnchanged)
	}
	if store.hasStablePut() {
		t.Fatal("older repair downgraded the stable pointer")
	}
	if got := store.objects[stableKey].body; !bytes.Equal(got, prior) {
		t.Fatalf("stable body = %s, want unchanged newer stable %s", got, prior)
	}
	for _, key := range []string{
		"releases/v1.2.3/gift-panel-windows-x64.exe",
		"releases/v1.2.3/gift-panel-windows-x64.exe.sha256",
		"releases/v1.2.3/gift-panel-changelog.json",
		"releases/v1.2.3/release.json",
	} {
		if _, ok := store.objects[key]; !ok {
			t.Fatalf("immutable object %q was not mirrored before the monotonic stable decision", key)
		}
	}
}

func TestPublishTreatsEqualStableTagAsIdempotentEvenWhenManifestBytesDiffer(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	prior := stableManifest("v1.2.3")
	store.objects[stableKey] = storedObjectFor(prior)

	outcome, err := Publish(context.Background(), store, input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeStableUnchanged {
		t.Fatalf("Publish() outcome = %q, want %q", outcome, OutcomeStableUnchanged)
	}
	if store.hasStablePut() {
		t.Fatal("equal-tag repair rewrote the stable pointer")
	}
	if got := store.objects[stableKey].body; !bytes.Equal(got, prior) {
		t.Fatalf("stable body = %s, want unchanged equal-tag stable %s", got, prior)
	}
}

func TestCompareTagsUsesNumericSemverOrdering(t *testing.T) {
	for _, test := range []struct {
		left  string
		right string
		want  int
	}{
		{left: "v1.10.0", right: "v1.9.99", want: 1},
		{left: "v0.10.0", right: "v0.9.9", want: 1},
		{left: "v2.0.0", right: "v10.0.0", want: -1},
		{left: "v3.4.5", right: "v3.4.5", want: 0},
	} {
		got, err := compareTags(test.left, test.right)
		if err != nil {
			t.Fatalf("compareTags(%q, %q): %v", test.left, test.right, err)
		}
		if got != test.want {
			t.Fatalf("compareTags(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

// Mutation caught: independently parsing the prior tag with strconv accepts forms rejected by the shared stable-tag boundary.
func TestCompareTagsRejectsNonCanonicalPriorStableTag(t *testing.T) {
	for _, prior := range []string{"1.2.3", "v01.2.3", "v1.2.3+build", "v18446744073709551616.0.0"} {
		if _, err := compareTags("v2.0.0", prior); err == nil {
			t.Fatalf("compareTags() accepted noncanonical prior tag %q", prior)
		}
	}
}

// Mutation caught: the orchestration adapter bypasses the existing immutable/stable-last transaction instead of delegating to it.
func TestPublisherObjectUsesExistingTransaction(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	store := newMemoryStore()
	publisher, err := NewPublisher(store)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := publisher.Publish(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeStablePromoted || !store.hasStablePut() {
		t.Fatalf("Publish() outcome = %q, stable put = %v", outcome, store.hasStablePut())
	}
}

// Mutation caught: retaining caller-owned slices lets bytes change after strict validation but before the immutable transaction consumes them.
func TestPublishPreparedReleaseUsesAnImmutableCopiedSnapshot(t *testing.T) {
	asset := []byte("validated executable")
	digest := sha256.Sum256(asset)
	checksum := []byte(hex.EncodeToString(digest[:]) + "  gift-panel-windows-x64.exe")
	changelog := []byte(`{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	prepared, err := NewPreparedRelease(asset, checksum, changelog)
	if err != nil {
		t.Fatal(err)
	}
	asset[0] = 'X'
	checksum[0] = '0'
	changelog[0] = 'X'
	store := newMemoryStore()

	outcome, err := Publish(context.Background(), store, Input{
		Channel:     release.ChannelStable,
		Tag:         "v1.2.3",
		PublishedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
		Prepared:    prepared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeStablePromoted {
		t.Fatalf("Publish() outcome = %q", outcome)
	}
	if got := store.objects["releases/v1.2.3/gift-panel-windows-x64.exe"].body; string(got) != "validated executable" {
		t.Fatalf("published asset = %q, want immutable validated snapshot", got)
	}
}

// Mutation caught: accepting both paths and a prepared snapshot makes the publisher's authoritative input ambiguous.
func TestPublishRejectsAmbiguousPreparedAndPathInputBeforeCOS(t *testing.T) {
	input := writeInput(t, "windows executable", `{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`)
	prepared, err := NewPreparedRelease([]byte("snapshot"), []byte(strings.Repeat("0", 64)+"  gift-panel-windows-x64.exe"), []byte(`{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	input.Prepared = prepared
	store := newMemoryStore()

	if _, err := Publish(context.Background(), store, input); err == nil {
		t.Fatal("Publish() accepted paths and a prepared snapshot")
	}
	if len(store.operations) != 0 {
		t.Fatalf("COS operations = %v, want none for ambiguous input", store.operations)
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
	return Input{Channel: release.ChannelStable, Tag: "v1.2.3", AssetPath: assetPath, ChecksumPath: checksumPath, ChangelogPath: changelogPath, PublishedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)}
}

type storedObject struct {
	body   []byte
	digest string
}

type stableGetResult struct {
	body []byte
	err  error
}

type memoryStore struct {
	objects           map[string]storedObject
	operations        []string
	putError          error
	immutablePutError error
	stableGets        []stableGetResult
	stablePutErrors   []error
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
	if key == stableKey && len(store.stablePutErrors) > 0 {
		err := store.stablePutErrors[0]
		store.stablePutErrors = store.stablePutErrors[1:]
		if err != nil {
			return err
		}
	}
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

func (store *memoryStore) PutImmutable(_ context.Context, key string, body io.Reader, size int64, _ string, digest string) error {
	store.operations = append(store.operations, "PUT-IMMUTABLE "+key)
	if store.immutablePutError != nil {
		return store.immutablePutError
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("incorrect object size")
	}
	if _, exists := store.objects[key]; exists {
		return errors.New("object already exists")
	}
	store.objects[key] = storedObject{body: data, digest: digest}
	return nil
}

func (store *memoryStore) Get(_ context.Context, key string, _ int64) ([]byte, string, error) {
	store.operations = append(store.operations, "GET "+key)
	if key == stableKey && len(store.stableGets) > 0 {
		result := store.stableGets[0]
		store.stableGets = store.stableGets[1:]
		return append([]byte(nil), result.body...), "", result.err
	}
	object, ok := store.objects[key]
	if !ok {
		return nil, "", cosstore.ErrNotFound
	}
	return append([]byte(nil), object.body...), "", nil
}

func stableManifest(tag string) []byte {
	return []byte(fmt.Sprintf(`{"schemaVersion":1,"tagName":%q,"publishedAt":"2026-08-14T12:00:00Z","asset":{"name":"gift-panel-windows-x64.exe","objectKey":%q,"size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"changelogObjectKey":%q}`,
		tag,
		"releases/"+tag+"/gift-panel-windows-x64.exe",
		"releases/"+tag+"/gift-panel-changelog.json",
	))
}

func storedObjectFor(body []byte) storedObject {
	digest := sha256.Sum256(body)
	return storedObject{body: append([]byte(nil), body...), digest: hex.EncodeToString(digest[:])}
}

func (store *memoryStore) hasStablePut() bool {
	for _, operation := range store.operations {
		if operation == "PUT channels/stable/latest.json" {
			return true
		}
	}
	return false
}

func containsOperation(operations []string, want string) bool {
	for _, operation := range operations {
		if operation == want {
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
