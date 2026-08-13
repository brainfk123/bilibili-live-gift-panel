package main

import (
	"context"
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

type diagnosticSecretStringer string

func (value diagnosticSecretStringer) String() string { return string(value) }

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
	for _, expected := range []string{"INFO gift_received", "gift_id=35801", `blind_source="catalog"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("runtime log missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "SESSDATA=secret") || strings.Contains(text, "cookie") || strings.Contains(text, "message") || strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Fatalf("runtime log leaked a secret or used JSON: %s", text)
	}
}

func TestDiagnosticLoggerPreservesBlindBoxLeaderboardReadFailureEvent(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.Error("blind_box_leaderboard_read_failed", "error_kind", "config_decode")

	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, "blind_box_leaderboard_read_failed") || !strings.Contains(text, `error_kind="config_decode"`) {
		t.Fatalf("leaderboard failure event was not retained: %s", text)
	}
}

func TestDiagnosticLoggerOmitsUnknownAndWrongTypedFields(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("gift_received",
		"gift_id", 35801,
		"count", 2,
		"rnd_hash", "ba7816bf8f01",
		"reason", "duplicate",
		"error_kind", "read",
		"source_duplicate", true,
		"private-token-key", 1,
		"unknown_numeric", 7,
		"unknown_bool", false,
		"unknown_number", json.Number("17"),
		"gift_id", "35801",
		"count", 2.5,
		"timestamp", true,
		"source_duplicate", 1,
		"rnd_hash", 42,
		"reason", 1,
		"error_kind", "private-error",
		"state", "ba7816bf8f01",
		"version", "vprivate-token",
		"port", "12450",
	)

	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"gift_id=35801", "count=2", `rnd_hash="ba7816bf8f01"`, `reason="duplicate"`, `error_kind="read"`, "source_duplicate=true"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("diagnostics missing valid field %q: %s", expected, text)
		}
	}
	for _, forbidden := range []string{"private-token-key", "unknown_numeric", "unknown_bool", "unknown_number", "private-error", "vprivate-token", "port=", "[REDACTED]", `gift_id="35801"`, "count=2.5", "timestamp=true", "source_duplicate=1"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("diagnostics emitted forbidden field/value %q: %s", forbidden, text)
		}
	}
}

func TestDiagnosticLoggerFixesLevelAndEventToSafeCategories(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.write("private-level-secret", "private-event-secret", "gift_id", 1)

	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, " INFO diagnostic_event_omitted gift_id=1") {
		t.Fatalf("logger did not use fixed level/event categories: %s", text)
	}
	for _, secret := range []string{"private-level-secret", "private-event-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("logger leaked untrusted level/event %q: %s", secret, text)
		}
	}
}

func TestDiagnosticLoggerRetainsOnlyCanonicalPublicRoomIDs(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("connection_state", "state", "connected", "room_id", "31567150")
	for _, value := range []any{" 31567150", "+31567150", "-31567150", "031567150", "31567150gift", "https://private.example/31567150?token=private", "private-token", "123456789012345678901", 31567150} {
		logger.Info("connection_state", "state", "connected", "room_id", value)
	}

	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if count := strings.Count(text, `room_id="31567150"`); count != 1 {
		t.Fatalf("validated room_id entries = %d, want 1: %s", count, text)
	}
	for _, secret := range []string{" 31567150", "+31567150", "-31567150", "031567150", "31567150gift", "private.example", "private-token", "123456789012345678901"} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostics leaked rejected room identifier %q: %s", secret, text)
		}
	}
}

func TestDiagnosticLogRoundTripsAllSafeProductionFields(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("gift_received",
		"gift_id", 35801, "blind_parent_id", 35800, "count", 2, "timestamp", int64(1700000000),
		"rnd_hash", "ba7816bf8f01", "source_duplicate", false, "blind_source", "catalog",
		"blind_cost", 6000.0, "blind_value", 9000.0, "blind_priced", true,
	)
	logger.Info("gift_accepted", "accept_write_ms", int64(5), "inbox_depth", 3, "oldest_pending_age_ms", int64(9))
	logger.Info("connection_gap", "attempts", 2, "duration_ms", int64(123), "error_kind", "read")
	logger.Info("blind_box_catalog_ready", "mapped_children", 4)
	logger.Info("http_ready", "port", 12450)
	logger.Info("service_start", "version", "0.1.1")
	logger.Info("service_stop", "version", "dev")
	logger.Info("connection_state", "state", "connected", "room_id", "31567150")

	export := string(logger.exportBytes())
	for _, expected := range []string{"gift_id=35801", "blind_parent_id=35800", "count=2", "timestamp=1700000000", `rnd_hash="ba7816bf8f01"`, "source_duplicate=false", `blind_source="catalog"`, "blind_cost=6000", "blind_value=9000", "blind_priced=true", "accept_write_ms=5", "inbox_depth=3", "oldest_pending_age_ms=9", "attempts=2", "duration_ms=123", `error_kind="read"`, "mapped_children=4", "port=12450", `version="0.1.1"`, `version="dev"`, `room_id="31567150"`} {
		if !strings.Contains(export, expected) {
			t.Fatalf("export missing validated field %q: %s", expected, export)
		}
	}
}

func TestDiagnosticLoggerRejectsNestedAndNormalizedSensitiveValues(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("gift_received",
		"gift_id", 35801,
		"blind_parent_id", 35800,
		"count", 2,
		"timestamp", 1700000000,
		"rnd_hash", "ba7816bf8f01",
		"userUID", "private-user-uid",
		"viewer.uid", "private-viewer-uid",
		"rndValue", "private-rnd",
		"error_message", "private-error",
		"nested", map[string]any{"UserName": "private-name", "avatar": "https://private.example/avatar.png"},
		"batch", []any{"private-slice", map[string]any{"Token": "private-token"}},
		"formatter", diagnosticSecretStringer("private-stringer"),
		"quoted", `"private-json-string"`,
		"note", "credentials https://private.example/path?token=private-url-token",
	)

	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"gift_id=35801", "blind_parent_id=35800", "count=2", "timestamp=1700000000", `rnd_hash="ba7816bf8f01"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("diagnostics missing safe value %q: %s", expected, text)
		}
	}
	for _, secret := range []string{"private-user-uid", "private-viewer-uid", "private-rnd", "private-error", "private-name", "private.example/avatar.png", "private-slice", "private-token", "private-stringer", "private-json-string", "private-url-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, text)
		}
	}
}

func TestDiagnosticLogExportSanitizesLegacyUnsafeLines(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON := `{"timestamp":"2026-08-04T12:00:00Z","level":"INFO","event":"gift_received","gift_id":35801,"blind_parent_id":35800,"count":2,"reason":"packet_bounds","rnd":"legacy-rnd","userUID":987654321,"nested":{"UserName":"legacy-name","avatar":"https://legacy.example/avatar.png"},"token":"legacy-token"}`
	legacyText := `2026-08-04T12:01:00Z ERROR gift_received gift_id=35801 viewer.uid=legacy-viewer-uid error_message="legacy raw error" cookie="SESSDATA=legacy-cookie" avatar="https://legacy.example/avatar.png"`
	malformed := `legacy malformed line token=legacy-malformed-token`
	if err := os.WriteFile(logger.path+".1", []byte(legacyJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logger.path, []byte(legacyText+"\n"+malformed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	export := string(logger.exportBytes())
	for _, expected := range []string{"2026-08-04T12:00:00Z", "gift_received", "gift_id=35801", "blind_parent_id=35800", "count=2", `reason="packet_bounds"`, "malformed_legacy_line"} {
		if !strings.Contains(export, expected) {
			t.Fatalf("export missing safe legacy data %q: %s", expected, export)
		}
	}
	for _, secret := range []string{"legacy-rnd", "987654321", "legacy-name", "legacy.example/avatar.png", "legacy-token", "legacy-viewer-uid", "legacy raw error", "legacy-cookie", "legacy-malformed-token"} {
		if strings.Contains(export, secret) {
			t.Fatalf("export leaked legacy value %q: %s", secret, export)
		}
	}
}

func TestDiagnosticLogExportsNormalizedRuntimeIngestionLifecycle(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.RoomID = "room"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Close()
	logger, err := newDiagnosticLogger(filepath.Join(root, "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.installInbox(inbox, inbox.SnapshotHealth())
	runtime.setDiagnosticLogger(logger)
	gift := giftEvent{
		GiftID: 35801, BlindGiftID: 35800, Num: 2, Timestamp: 1700000000, Rnd: "runtime-private-rnd",
		UID: 987654321, Uname: "runtime-private-name", Avatar: "https://runtime.private/avatar.png",
	}
	for range 2 {
		runtime.acceptGift(context.Background(), "room", "SEND_GIFT", gift)
	}
	var first giftInboxRecord
	for index := 0; index < 2; index++ {
		record, ok, err := inbox.Next()
		if err != nil || !ok {
			t.Fatalf("next record %d = %#v, %v, %v", index, record, ok, err)
		}
		if index == 0 {
			first = record
		}
		if err := runtime.processInboxRecord(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if err := inbox.Acknowledge(record.IngestionID); err != nil {
			t.Fatal(err)
		}
	}
	if err := runtime.processInboxRecord(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	runtime.setConnectionGaps([]connectionGap{{StartedAt: 1, EndedAt: 2, DurationMS: 123, Attempts: 2, ErrorKind: "read"}})

	export := string(logger.exportBytes())
	for _, expected := range []string{"gift_accepted", "gift_transaction_prepare", "gift_transaction_complete", "gift_transaction_recovery", "gift_received", `reason="duplicate"`, "source_duplicate=true", "connection_gap", "duration_ms=123", "gift_id=35801", "blind_parent_id=35800", "count=2", "timestamp=1700000000", `rnd_hash="` + diagnosticHash(gift.Rnd) + `"`, "accept_write_ms=", "inbox_depth=", "oldest_pending_age_ms="} {
		if !strings.Contains(export, expected) {
			t.Fatalf("export missing %q: %s", expected, export)
		}
	}
	for _, secret := range []string{gift.Rnd, "987654321", gift.Uname, gift.Avatar} {
		if strings.Contains(export, secret) {
			t.Fatalf("export leaked runtime value %q: %s", secret, export)
		}
	}
}

func TestDiagnosticLogRedactsHostileIngestionValues(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("gift_received",
		"rnd", "raw-rnd-secret",
		"viewer_uid", 987654321,
		"username", "private-viewer",
		"avatar", "https://private.example/avatar.png",
		"error", errors.New("token=private-token"),
		"payload", `{"cookie":"SESSDATA=private-cookie"}`,
		"rnd_hash", "ba7816bf8f01",
	)

	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `rnd_hash="ba7816bf8f01"`) {
		t.Fatalf("diagnostics did not retain the safe hash: %s", text)
	}
	for _, secret := range []string{"raw-rnd-secret", "987654321", "private-viewer", "private.example/avatar.png", "private-token", "private-cookie"} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, text)
		}
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
