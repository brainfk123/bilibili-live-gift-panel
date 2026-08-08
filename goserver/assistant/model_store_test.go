package assistant

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func fakeGGUF(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	buffer.WriteString("GGUF")
	for _, value := range []any{uint32(3), uint64(0), uint64(2)} {
		if err := binary.Write(&buffer, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	writeString := func(value string) {
		if err := binary.Write(&buffer, binary.LittleEndian, uint64(len(value))); err != nil {
			t.Fatal(err)
		}
		buffer.WriteString(value)
	}
	writeString("general.architecture")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(ggufTypeString))
	writeString("qwen3")
	writeString("general.file_type")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(ggufTypeUint32))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(ggufMostlyQ8_0))
	buffer.Write(bytes.Repeat([]byte{0x5a}, 1024))
	return buffer.Bytes()
}

func manifestFor(data []byte) ModelManifest {
	sum := sha256.Sum256(data)
	return ModelManifest{
		SchemaVersion: 1, ModelID: "Qwen/Qwen3-0.6B-GGUF", Version: "test-1",
		Repository: "Qwen/Qwen3-0.6B-GGUF", Revision: "abc1234", File: "model.gguf",
		SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Architecture: "qwen3", Quantization: "Q8_0",
	}
}

func TestOfficialQwenArtifact(t *testing.T) {
	modelPath := os.Getenv("ASSISTANT_TEST_MODEL")
	if modelPath == "" {
		t.Skip("set ASSISTANT_TEST_MODEL to the official Qwen3-0.6B Q8_0 GGUF")
	}
	manifest := ModelManifest{
		SchemaVersion: 1,
		ModelID:       "Qwen3-0.6B-GGUF-Q8_0",
		Version:       "qwen3-0.6b-q8_0-official",
		Repository:    "Qwen/Qwen3-0.6B-GGUF",
		Revision:      "6abe20cd0aed577f4d0b267935868ecae190aee9",
		File:          "Qwen3-0.6B-Q8_0.gguf",
		SizeBytes:     639446688,
		SHA256:        "9465e63a22add5354d9bb4b99e90117043c7124007664907259bd16d043bb031",
		Architecture:  "qwen3",
		Quantization:  "Q8_0",
	}
	if err := validateModelFile(modelPath, manifest); err != nil {
		t.Fatalf("validate official Qwen artifact: %v", err)
	}
}

func TestFetchManifestVerifiesSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := manifestFor(fakeGGUF(t))
	manifest.DownloadURL = "http://127.0.0.1/model.gguf"
	payload, _ := json.Marshal(manifest)
	envelope, _ := json.Marshal(SignedManifest{Payload: payload, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(envelope) }))
	defer server.Close()
	store, err := NewModelStore(ModelStoreOptions{Root: t.TempDir(), ManifestURL: server.URL, PublicKey: publicKey, AllowedHosts: []string{"127.0.0.1"}, AllowHTTPTest: true})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := store.FetchManifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if actual.Version != manifest.Version {
		t.Fatalf("version = %q", actual.Version)
	}
	store.publicKey[0] ^= 0xff
	if _, err := store.FetchManifest(context.Background()); err == nil || !strings.Contains(err.Error(), "签名") {
		t.Fatalf("bad signature error = %v", err)
	}
}

func TestPrepareResumesOnlyFromMatchingContentRange(t *testing.T) {
	data := fakeGGUF(t)
	manifest := manifestFor(data)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if value := request.Header.Get("Range"); value != "" {
			var start int
			_, _ = fmt.Sscanf(value, "bytes=%d-", &start)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start:])
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	manifest.DownloadURL = server.URL + "/model.gguf"
	store, _ := NewModelStore(ModelStoreOptions{Root: t.TempDir(), ManifestURL: server.URL, AllowedHosts: []string{"127.0.0.1"}, AllowHTTPTest: true})
	partial := filepath.Join(store.root, "models", manifest.Version, "model.gguf.partial")
	if err := os.MkdirAll(filepath.Dir(partial), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, data[:100], 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := store.Prepare(context.Background(), manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d", requests.Load())
	}
	if err := store.Validate(path, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestActivateRestoresBackupRecordAfterInterruptedReplace(t *testing.T) {
	data := fakeGGUF(t)
	manifest := manifestFor(data)
	root := t.TempDir()
	store, _ := NewModelStore(ModelStoreOptions{Root: root})
	modelPath := filepath.Join(root, "models", manifest.Version, "model.gguf")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(manifest, modelPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "active.json"), filepath.Join(root, "active.previous.json")); err != nil {
		t.Fatal(err)
	}
	actual, path, err := store.Active()
	if err != nil {
		t.Fatal(err)
	}
	if actual.Version != manifest.Version || path != modelPath {
		t.Fatalf("active = %#v, %s", actual, path)
	}
}

func TestContentRangeStartValidation(t *testing.T) {
	if !contentRangeStartsAt("bytes 100-199/200", 100) {
		t.Fatal("valid range rejected")
	}
	if contentRangeStartsAt("bytes 99-199/200", 100) {
		t.Fatal("wrong range accepted")
	}
}
