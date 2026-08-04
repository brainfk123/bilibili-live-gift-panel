package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticLoggerWritesPlainTextAndRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	logger, err := newDiagnosticLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	logger.now = func() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)) }
	logger.Info("gift_received", "gift_id", 35801, "blind_source", "catalog", "cookie", "SESSDATA=secret", "message", "line\nbreak")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"INFO gift_received", "gift_id=35801", `blind_source="catalog"`, `cookie="[REDACTED]"`, `message="line break"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("runtime log missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "SESSDATA=secret") || strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Fatalf("runtime log leaked a secret or used JSON: %s", text)
	}
}

func TestDiagnosticLogExportReturnsDownloadableText(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.now = func() time.Time { return time.Date(2026, 8, 4, 12, 34, 56, 0, time.FixedZone("CST", 8*60*60)) }
	logger.Info("service_start", "version", "0.1.1")

	response := httptest.NewRecorder()
	logger.handleExport(response, httptest.NewRequest(http.MethodGet, "/api/diagnostics/log", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, "gift-panel-runtime-20260804-123456.log") {
		t.Fatalf("content disposition = %q", disposition)
	}
	for _, expected := range []string{"运行日志", "service_start", "runtime.log"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("export missing %q: %s", expected, response.Body.String())
		}
	}
}
