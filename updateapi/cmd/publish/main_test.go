package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/publish"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

func TestPublishFailureMessageIncludesOnlySafeCOSClassification(t *testing.T) {
	request, err := http.NewRequest(http.MethodHead, "https://private.example.invalid/releases/secret?token=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	providerError := &cos.ErrorResponse{
		Response:  &http.Response{StatusCode: http.StatusForbidden, Request: request, Header: make(http.Header)},
		Code:      "AccessDenied",
		Message:   "sensitive provider detail",
		RequestID: "sensitive-request-id",
	}
	got := publishFailureMessage(fmt.Errorf("private object: %w", providerError))
	if got != "publish failed: Tencent COS AccessDenied (HTTP 403)" {
		t.Fatalf("message = %q", got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "private object") {
		t.Fatalf("message leaked private detail: %q", got)
	}
}

func TestPublishFailureMessageKeepsLocalFailuresGeneric(t *testing.T) {
	if got := publishFailureMessage(errors.New("private local path")); got != "publish failed" {
		t.Fatalf("message = %q", got)
	}
}

func TestRunRequiresAllPublishingFlagsBeforeCreatingStore(t *testing.T) {
	asset, checksum, changelog := commandInputs(t)
	called := false
	err := run([]string{"--tag", "v1.2.3", "--asset", asset, "--checksum", checksum, "--changelog", changelog}, func() (publish.Store, error) {
		called = true
		return nil, errors.New("must not create store")
	}, io.Discard)
	if err == nil {
		t.Fatal("run() error = nil, want required flag rejection")
	}
	if called {
		t.Fatal("run() created COS store before checking required flags")
	}
}

func TestRunRejectsInvalidPublishedTimestampBeforeCreatingStore(t *testing.T) {
	asset, checksum, changelog := commandInputs(t)
	called := false
	err := run([]string{"--tag", "v1.2.3", "--published-at", "yesterday", "--asset", asset, "--checksum", checksum, "--changelog", changelog}, func() (publish.Store, error) {
		called = true
		return nil, errors.New("must not create store")
	}, io.Discard)
	if err == nil {
		t.Fatal("run() error = nil, want published timestamp rejection")
	}
	if called {
		t.Fatal("run() created COS store before checking published timestamp")
	}
}

func TestRunRejectsInvalidTagWithoutPublishing(t *testing.T) {
	asset, checksum, changelog := commandInputs(t)
	store := newCommandStore()
	var output bytes.Buffer
	err := run([]string{"--tag", "v1.2.3+extra/path", "--published-at", "2026-08-14T10:30:00Z", "--asset", asset, "--checksum", checksum, "--changelog", changelog}, func() (publish.Store, error) {
		return store, nil
	}, &output)
	if err == nil {
		t.Fatal("run() error = nil, want invalid tag rejection")
	}
	if len(store.objects) != 0 {
		t.Fatalf("objects = %v, want no publishing", store.objects)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want no success output", output.String())
	}
}

func TestRunPrintsOnlyVerifiedIdentifiersAndOutcome(t *testing.T) {
	asset, checksum, changelog := commandInputs(t)
	store := newCommandStore()
	var output bytes.Buffer
	err := run([]string{"--tag", "v1.2.3", "--published-at", "2026-08-14T10:30:00Z", "--asset", asset, "--checksum", checksum, "--changelog", changelog}, func() (publish.Store, error) {
		return store, nil
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	want := "v1.2.3\nreleases/v1.2.3/gift-panel-windows-x64.exe\nreleases/v1.2.3/gift-panel-windows-x64.exe.sha256\nreleases/v1.2.3/gift-panel-changelog.json\nreleases/v1.2.3/release.json\nchannels/stable/latest.json\nstable promoted\n"
	if output.String() != want {
		t.Fatalf("output = %q, want only verified release identifiers", output.String())
	}
	stable := string(store.objects["channels/stable/latest.json"].body)
	if !bytes.Contains([]byte(stable), []byte(`"publishedAt":"2026-08-14T10:30:00Z"`)) {
		t.Fatalf("stable manifest = %s, want preserved GitHub release timestamp", stable)
	}
}

func TestRunReportsStableUnchangedWhenRepairingAnOlderRelease(t *testing.T) {
	asset, checksum, changelog := commandInputs(t)
	store := newCommandStore()
	prior := []byte(`{"schemaVersion":1,"tagName":"v1.3.0","publishedAt":"2026-08-14T11:30:00Z","asset":{"name":"gift-panel-windows-x64.exe","objectKey":"releases/v1.3.0/gift-panel-windows-x64.exe","size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"changelogObjectKey":"releases/v1.3.0/gift-panel-changelog.json"}`)
	digest := sha256.Sum256(prior)
	store.objects["channels/stable/latest.json"] = commandObject{body: prior, digest: hex.EncodeToString(digest[:])}
	var output bytes.Buffer

	err := run([]string{"--tag", "v1.2.3", "--published-at", "2026-08-14T10:30:00Z", "--asset", asset, "--checksum", checksum, "--changelog", changelog}, func() (publish.Store, error) {
		return store, nil
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "stable unchanged\n") {
		t.Fatalf("output = %q, want explicit stable unchanged outcome", output.String())
	}
	if got := store.objects["channels/stable/latest.json"].body; !bytes.Equal(got, prior) {
		t.Fatalf("stable body = %s, want unchanged newer stable %s", got, prior)
	}
}

func commandInputs(t *testing.T) (string, string, string) {
	t.Helper()
	directory := t.TempDir()
	asset := filepath.Join(directory, "gift-panel-windows-x64.exe")
	checksum := filepath.Join(directory, "gift-panel-windows-x64.exe.sha256")
	changelog := filepath.Join(directory, "gift-panel-changelog.json")
	body := []byte("windows executable")
	if err := os.WriteFile(asset, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if err := os.WriteFile(checksum, []byte(hex.EncodeToString(digest[:])+"  gift-panel-windows-x64.exe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changelog, []byte(`{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return asset, checksum, changelog
}

type commandObject struct {
	body   []byte
	digest string
}

type commandStore struct{ objects map[string]commandObject }

func newCommandStore() *commandStore { return &commandStore{objects: make(map[string]commandObject)} }

func (store *commandStore) Head(_ context.Context, key string) (cosstore.ObjectInfo, error) {
	object, ok := store.objects[key]
	if !ok {
		return cosstore.ObjectInfo{}, cosstore.ErrNotFound
	}
	return cosstore.ObjectInfo{Size: int64(len(object.body)), SHA256: object.digest}, nil
}

func (store *commandStore) PutImmutable(_ context.Context, key string, body io.Reader, size int64, _ string, digest string) error {
	if _, exists := store.objects[key]; exists {
		return cosstore.ErrAlreadyExists
	}
	data, err := io.ReadAll(body)
	if err != nil || int64(len(data)) != size {
		return errors.New("immutable write failed")
	}
	store.objects[key] = commandObject{body: data, digest: digest}
	return nil
}

func (store *commandStore) Put(_ context.Context, key string, body io.Reader, size int64, _ string, digest string) error {
	data, err := io.ReadAll(body)
	if err != nil || int64(len(data)) != size {
		return errors.New("stable write failed")
	}
	store.objects[key] = commandObject{body: data, digest: digest}
	return nil
}

func (store *commandStore) Get(_ context.Context, key string, _ int64) ([]byte, string, error) {
	object, ok := store.objects[key]
	if !ok {
		return nil, "", cosstore.ErrNotFound
	}
	return append([]byte(nil), object.body...), "", nil
}
