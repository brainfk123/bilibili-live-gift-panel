package assistant

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func signedModelServer(t *testing.T, manifest ModelManifest, data []byte) (*httptest.Server, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var envelope []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(envelope)
		case "/model.gguf":
			_, _ = w.Write(data)
		default:
			http.NotFound(w, request)
		}
	}))
	manifest.DownloadURL = server.URL + "/model.gguf"
	payload, err := json.Marshal(manifest)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	envelope, err = json.Marshal(SignedManifest{Payload: payload, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return server, publicKey
}

func waitForAssistantState(t *testing.T, service *Service, states ...string) AssistantStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		for _, state := range states {
			if status.State == state {
				return status
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("assistant did not reach states %v; latest = %#v", states, service.Status())
	return AssistantStatus{}
}

func TestInstallCheckAndDeleteLifecycle(t *testing.T) {
	data := fakeGGUF(t)
	manifest := manifestFor(data)
	server, publicKey := signedModelServer(t, manifest, data)
	defer server.Close()
	store, err := NewModelStore(ModelStoreOptions{
		Root: t.TempDir(), ManifestURL: server.URL + "/manifest.json", PublicKey: publicKey,
		AllowedHosts: []string{"127.0.0.1"}, AllowHTTPTest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Options{Knowledge: EmbeddedKnowledge(), Store: store, Engine: &fakeEngine{}, AppVersion: "0.2.4"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	status, err := service.CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.LatestVersion != manifest.Version {
		t.Fatalf("check status = %#v", status)
	}
	if err := service.StartInstall(); err != nil {
		t.Fatal(err)
	}
	status = waitForAssistantState(t, service, "installed", "error")
	if status.State == "error" {
		t.Fatal(status.Message)
	}
	if status.ModelVersion != manifest.Version || status.Progress != 1 {
		t.Fatalf("install status = %#v", status)
	}
	if err := service.DeleteModel(); err != nil {
		t.Fatal(err)
	}
	if status := service.Status(); status.State != "missing" {
		t.Fatalf("delete status = %#v", status)
	}
}

func TestFailedUpdateKeepsOldActiveModel(t *testing.T) {
	data := fakeGGUF(t)
	oldManifest := manifestFor(data)
	oldManifest.Version = "old-1"
	root := t.TempDir()
	oldPath := filepath.Join(root, "models", oldManifest.Version, "model.gguf")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	newManifest := manifestFor(data)
	newManifest.Version = "new-2"
	server, publicKey := signedModelServer(t, newManifest, data)
	defer server.Close()
	store, _ := NewModelStore(ModelStoreOptions{
		Root: root, ManifestURL: server.URL + "/manifest.json", PublicKey: publicKey,
		AllowedHosts: []string{"127.0.0.1"}, AllowHTTPTest: true,
	})
	if err := store.Activate(oldManifest, oldPath); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{loadErr: errors.New("native load failed")}
	service, err := NewService(Options{Knowledge: EmbeddedKnowledge(), Store: store, Engine: engine, AppVersion: "0.2.4"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.StartUpdate(); err != nil {
		t.Fatal(err)
	}
	status := waitForAssistantState(t, service, "installed", "ready")
	if !strings.Contains(status.Message, "加载验证失败") {
		t.Fatalf("status = %#v", status)
	}
	active, path, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != oldManifest.Version || path != oldPath {
		t.Fatalf("old active model was replaced: %#v %s", active, path)
	}
}

func TestPrepareRejectsHashMismatchAndRemovesPartial(t *testing.T) {
	data := fakeGGUF(t)
	manifest := manifestFor(data)
	manifest.SHA256 = strings.Repeat("0", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	manifest.DownloadURL = server.URL
	store, _ := NewModelStore(ModelStoreOptions{Root: t.TempDir(), AllowedHosts: []string{"127.0.0.1"}, AllowHTTPTest: true})
	_, err := store.Prepare(context.Background(), manifest, nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("hash error = %v", err)
	}
	partial := filepath.Join(store.root, "models", manifest.Version, "model.gguf.partial")
	if _, statErr := os.Stat(partial); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial still exists: %v", statErr)
	}
}

func TestManifestRejectsTraversalAndFloatingRevision(t *testing.T) {
	data := fakeGGUF(t)
	manifest := manifestFor(data)
	store, _ := NewModelStore(ModelStoreOptions{Root: t.TempDir()})
	manifest.File = "../model.gguf"
	if err := store.validateManifest(manifest); err == nil {
		t.Fatal("traversal filename accepted")
	}
	manifest = manifestFor(data)
	manifest.Revision = "main"
	if err := store.validateManifest(manifest); err == nil {
		t.Fatal("floating revision accepted")
	}
}
