package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestEmbeddedPageHandlerServesNestedUIAssets(t *testing.T) {
	pageFS := fstest.MapFS{
		"index.html":                 &fstest.MapFile{Data: []byte("<!doctype html>")},
		"chunks/config-entry-abc.js": &fstest.MapFile{Data: []byte("export const config = true;")},
		"assets/app.css":             &fstest.MapFile{Data: []byte(".app { color: red; }")},
	}
	handler := newEmbeddedPageHandler(pageFS)

	tests := []struct {
		name string
		path string
		want int
		body string
	}{
		{name: "index", path: "/", want: http.StatusOK, body: "<!doctype html>"},
		{name: "nested chunk", path: "/chunks/config-entry-abc.js", want: http.StatusOK, body: "export const config = true;"},
		{name: "nested asset", path: "/assets/app.css", want: http.StatusOK, body: ".app { color: red; }"},
		{name: "missing asset", path: "/chunks/missing.js", want: http.StatusNotFound},
		{name: "traversal", path: "/chunks/../index.html", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if test.body != "" && response.Body.String() != test.body {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.body)
			}
		})
	}
}

func TestEmbeddedUIAssetManifestMatchesEmbeddedFS(t *testing.T) {
	manifestBytes, err := embeddedFS.ReadFile("dist/ui-assets.json")
	if err != nil {
		t.Fatalf("read embedded UI asset manifest: %v", err)
	}
	var manifest struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode embedded UI asset manifest: %v", err)
	}
	if len(manifest.Files) == 0 {
		t.Fatal("embedded UI asset manifest is empty")
	}
	for _, asset := range manifest.Files {
		if _, err := embeddedFS.ReadFile("dist/" + asset.Path); err != nil {
			t.Errorf("manifest asset %q is not embedded: %v", asset.Path, err)
		}
	}
}

func TestNewMainGiftClipJobsStopsOnPayloadFailure(t *testing.T) {
	want := errors.New("payload unavailable")
	called := false
	jobs, err := newMainGiftClipJobs(nil, nil, nil,
		func(string) (*giftClipPayload, error) { return nil, want },
		func(string, giftClipSourceResolver, giftClipEncoder, *diagnosticLogger) *giftClipJobManager {
			called = true
			return nil
		},
	)
	if !errors.Is(err, want) || jobs != nil || called {
		t.Fatalf("jobs=%v err=%v managerCalled=%v", jobs, err, called)
	}
}

func TestNewMainGiftClipJobsSharesGiftMediaWithoutStartingEncoder(t *testing.T) {
	media := &giftReceiptAPI{}
	payload := &giftClipPayload{}
	called := false
	jobs, err := newMainGiftClipJobs(&configStore{}, media, nil,
		func(string) (*giftClipPayload, error) { return payload, nil },
		func(_ string, resolver giftClipSourceResolver, encoder giftClipEncoder, _ *diagnosticLogger) *giftClipJobManager {
			called = true
			resolved, ok := resolver.(*receiptGiftClipSourceResolver)
			if !ok || resolved.media != media {
				t.Fatalf("resolver=%T media=%p want=%p", resolver, resolved.media, media)
			}
			ffmpeg, ok := encoder.(*giftClipFFmpegEncoder)
			if !ok || ffmpeg.payload != payload {
				t.Fatalf("encoder=%T payload=%p want=%p", encoder, ffmpeg.payload, payload)
			}
			return nil
		},
	)
	if err != nil || jobs != nil || !called {
		t.Fatalf("jobs=%v err=%v managerCalled=%v", jobs, err, called)
	}
}

func TestMainGiftClipShutdownOrdersRuntimeJobsServerInstallOnce(t *testing.T) {
	order := []string{}
	closeCount := 0
	closeJobs := newMainGiftClipCloser(func() {
		closeCount++
		order = append(order, "jobs")
	})
	runMainGiftClipShutdown(func() { order = append(order, "runtime") }, closeJobs, func() { order = append(order, "server") }, func() { order = append(order, "install") })
	closeJobs() // mirrors the deferred close after normal shutdown.
	if got := strings.Join(order, ","); got != "runtime,jobs,server,install" || closeCount != 1 {
		t.Fatalf("shutdown order=%q closeCount=%d", got, closeCount)
	}
}

func TestMainGiftClipCloserRunsOnlyOnce(t *testing.T) {
	count := 0
	closeJobs := newMainGiftClipCloser(func() { count++ })
	closeJobs()
	closeJobs()
	if count != 1 {
		t.Fatalf("close count=%d", count)
	}
}

type runtimeStatusTestInbox struct {
	health giftInboxHealth
}

func (inbox *runtimeStatusTestInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (inbox *runtimeStatusTestInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (inbox *runtimeStatusTestInbox) Acknowledge(string) error { return nil }
func (inbox *runtimeStatusTestInbox) Release(string) error     { return nil }
func (inbox *runtimeStatusTestInbox) Close() error             { return nil }
func (inbox *runtimeStatusTestInbox) Health() giftInboxHealth  { return inbox.health }

func TestRuntimeStatusIncludesIngestionHealth(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := os.WriteFile(store.stateTransactionPath(), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	background := newBackgroundRuntime(store, nil)
	background.status = runtimeStatus{
		State: "connected", RoomID: "31567150", LastFrameAt: 3000, LastError: "https://connection-secret.example.test/read failure",
		ConnectionGaps: []connectionGap{{StartedAt: 1000, EndedAt: 4000, DurationMS: 3000, Attempts: 2, ErrorKind: "read_timeout"}},
	}
	store.transactionPending = true
	background.recordIngestionFailureFrom("accept", errors.New("https://secret.example.test/inbox write failed"))
	background.installInbox(&runtimeStatusTestInbox{health: giftInboxHealth{PendingCount: 3, OldestPendingAt: 2000}}, giftInboxHealth{PendingCount: 3, OldestPendingAt: 2000})

	handler := handleRuntimeStatus(background)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()

	var payload struct {
		Runtime map[string]json.RawMessage `json:"runtime"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"state":              `"connected"`,
		"lastFrameAt":        `3000`,
		"reconnectAttempts":  `2`,
		"gaps":               `[{"startedAt":1000,"endedAt":4000,"durationMs":3000,"attempts":2,"errorKind":"read_timeout"}]`,
		"inbox":              `{"pendingCount":3,"oldestPendingAt":2000}`,
		"transactionPending": `true`,
		"ingestionErrorKind": `"inbox_persist"`,
	} {
		if got := string(payload.Runtime[field]); got != want {
			t.Errorf("runtime.%s = %s, want %s (body = %s)", field, got, want, body)
		}
	}
	if strings.Contains(body, "secret.example.test") || strings.Contains(body, "connection-secret.example.test") || strings.Contains(body, "IngestionError") {
		t.Fatalf("runtime response exposed unsafe ingestion details: %s", body)
	}
}

func TestRuntimeIngestionErrorKindsAreStable(t *testing.T) {
	for _, test := range []struct {
		source string
		err    error
		want   string
	}{
		{source: "accept", err: errGiftInboxCapacity, want: "inbox_capacity"},
		{source: "accept", err: errors.New("write failed"), want: "inbox_persist"},
		{source: "next", err: errors.New("read failed"), want: "inbox_recovery"},
		{source: "consumer", err: errors.New("transaction failed"), want: "transaction"},
	} {
		t.Run(test.want, func(t *testing.T) {
			runtime := newBackgroundRuntime(nil, nil)
			runtime.recordIngestionFailureFrom(test.source, test.err)
			if got := runtime.Status().IngestionErrorKind; got != test.want {
				t.Fatalf("error kind = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRuntimeStatusReadsStoreTransactionSnapshotAfterRecovery(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := os.WriteFile(store.stateTransactionPath(), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	store.transactionPending = true
	if err := os.Remove(store.stateTransactionPath()); err != nil {
		t.Fatal(err)
	}
	if !runtime.Status().TransactionPending {
		t.Fatal("runtime did not read the store-owned pending transaction snapshot")
	}
	if _, err := store.readState(); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if runtime.Status().TransactionPending {
			t.Fatal("runtime did not reflect successful store recovery")
		}
	}
}

func TestRuntimeStatusWithNilStoreDoesNotUseCachedTransactionState(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	if runtime.Status().TransactionPending {
		t.Fatal("nil-store runtime reported a pending transaction")
	}
}

func TestApplicationLifecycleStartsDiagnosticsWithUnrecoverableTransactionEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	seed := &configStore{path: path}
	state := defaultAppState()
	state.RoomID = "31567150"
	if err := seed.replaceState(state); err != nil {
		t.Fatal(err)
	}
	evidence := []byte(`{"schemaVersion":999,"raw":"RAW-PREPARE-SECRET"}` + "\n")
	if err := os.WriteFile(seed.stateTransactionPath(), evidence, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := newConfigStoreAtPath(path)
	if err != nil {
		t.Fatalf("application store construction failed: %v", err)
	}
	logger, err := newDiagnosticLogger(filepath.Join(dir, "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	sourceStarted := make(chan struct{}, 1)
	background := newBackgroundRuntime(store, func() giftEventSource {
		sourceStarted <- struct{}{}
		return &stableConnectedSource{}
	})
	background.setDiagnosticLogger(logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		background.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	configResponse := httptest.NewRecorder()
	store.handle(configResponse, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if configResponse.Code != http.StatusOK || !strings.Contains(configResponse.Body.String(), `"roomId":"31567150"`) {
		t.Fatalf("diagnostic config GET status = %d, body = %s", configResponse.Code, configResponse.Body.String())
	}
	runtimeResponse := httptest.NewRecorder()
	handleRuntimeStatus(background).ServeHTTP(runtimeResponse, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	if runtimeResponse.Code != http.StatusOK || !strings.Contains(runtimeResponse.Body.String(), `"ingestionErrorKind":"transaction_recovery"`) || !strings.Contains(runtimeResponse.Body.String(), `"transactionPending":true`) {
		t.Fatalf("runtime health status = %d, body = %s", runtimeResponse.Code, runtimeResponse.Body.String())
	}
	for _, secret := range []string{"RAW-PREPARE-SECRET", "schemaVersion", "invalid character", "not supported"} {
		if strings.Contains(runtimeResponse.Body.String(), secret) || strings.Contains(configResponse.Body.String(), secret) {
			t.Fatalf("application API exposed recovery detail %q", secret)
		}
	}

	putResponse := httptest.NewRecorder()
	store.handle(putResponse, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"roomId":"1"}`)))
	if putResponse.Code != http.StatusConflict || strings.Contains(putResponse.Body.String(), "RAW-PREPARE-SECRET") {
		t.Fatalf("blocked mutation status = %d, body = %s", putResponse.Code, putResponse.Body.String())
	}
	if after, readErr := os.ReadFile(seed.stateTransactionPath()); readErr != nil || string(after) != string(evidence) {
		t.Fatalf("transaction evidence changed: data=%q err=%v", after, readErr)
	}

	deadline := time.After(2 * time.Second)
	for {
		export := string(logger.exportBytes())
		if strings.Contains(export, `error_kind="transaction_recovery"`) {
			if strings.Contains(export, "RAW-PREPARE-SECRET") || strings.Contains(export, "schemaVersion") {
				t.Fatalf("diagnostic export exposed transaction evidence: %s", export)
			}
			break
		}
		select {
		case <-sourceStarted:
			t.Fatal("gift source started while transaction evidence was unrecoverable")
		case <-deadline:
			t.Fatalf("diagnostic export did not report stable recovery kind: %s", export)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestConfigResetClearsCorruptTransactionAndRuntimeArtifactsThroughProductionHandler(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	seed := &configStore{path: path}
	state := defaultAppState()
	state.RoomID = "room-a"
	if err := seed.replaceState(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seed.stateTransactionPath(), []byte(`{"schemaVersion":999,"raw":"RESETTABLE-EVIDENCE"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStoreAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Accept("room-a", "SEND_GIFT", giftEvent{GiftID: 1}); err != nil {
		t.Fatal(err)
	}
	background := newBackgroundRuntime(store, nil)
	background.installInbox(inbox, inbox.SnapshotHealth())
	if err := background.savePendingGiftAnimationFile(pendingGiftAnimationFile{SchemaVersion: pendingGiftAnimationsSchemaVersion, PreparedRoomID: "room-a", Records: []pendingGiftAnimation{{RoomID: "room-a", Gift: giftEvent{GiftID: 1}}}}); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "user-owned.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.setResetCoordinator(background.ResetWithOutcome)

	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("reset status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.MutationBlockKind() != "" || store.TransactionPending() {
		t.Fatalf("reset did not clear store health: kind=%q pending=%v", store.MutationBlockKind(), store.TransactionPending())
	}
	if health := inbox.Health(); health.PendingCount != 0 {
		t.Fatalf("reset inbox health = %#v", health)
	}
	for _, artifact := range append(store.statePaths(), store.stateTransactionPath(), background.pendingGiftAnimationsPath()) {
		if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact %s survived reset: %v", filepath.Base(artifact), err)
		}
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "keep" {
		t.Fatalf("reset changed unrelated user file: data=%q err=%v", data, err)
	}
}

func TestConfigResetFailureIsSafeAndLeavesMutationsBlocked(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	inbox := &resetBarrierInbox{
		acceptStarted: make(chan struct{}), resetCalled: make(chan struct{}),
		resetErr: errors.New("RAW-RESET-SECRET https://private.example/reset"),
	}
	background := newBackgroundRuntime(store, nil)
	background.installInbox(inbox, inbox.Health())
	store.setResetCoordinator(background.ResetWithOutcome)

	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "RAW-RESET-SECRET") || strings.Contains(response.Body.String(), "private.example") {
		t.Fatalf("unsafe reset failure response status = %d, body = %s", response.Code, response.Body.String())
	}
	status := background.Status()
	if status.IngestionErrorKind != "reset_failure" {
		t.Fatalf("reset failure kind = %q, want reset_failure", status.IngestionErrorKind)
	}
	put := httptest.NewRecorder()
	store.handle(put, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"roomId":"2"}`)))
	if put.Code != http.StatusConflict || strings.Contains(put.Body.String(), "RAW-RESET-SECRET") {
		t.Fatalf("post-failure mutation status = %d, body = %s", put.Code, put.Body.String())
	}
}

func TestConfigResetFailureAfterMarkerFailsClosedAndRetryClearsCandidate(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	initial := defaultAppState()
	initial.RoomID = "old-room"
	initial.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 1}}
	initial.GiftCatalog = []giftInfo{{ID: 1, Name: "旧礼物"}}
	initial.Log = []logEntry{{EventID: "old-event", GiftName: "旧记录", ValueAfter: 1}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}
	next, err := cloneAppState(initial)
	if err != nil {
		t.Fatal(err)
	}
	next.RoomID = "candidate-room"
	next.Attributes[0].Value = 9
	next.GiftCatalog = []giftInfo{{ID: 9, Name: "候选礼物"}}
	next.Log = []logEntry{{EventID: "candidate-event", GiftName: "候选记录", ValueAfter: 9}}
	injectedReplay := errors.New("persistent injected config replay failure")
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "config.json" {
			return injectedReplay
		}
		return writeFileAtomically(path, data)
	}
	store.mu.Lock()
	outcome := store.persistPreparedStateWithOutcomeLocked(next, "")
	if !outcome.Committed || !errors.Is(outcome.Err, injectedReplay) {
		store.mu.Unlock()
		t.Fatalf("candidate persistence outcome = %+v", outcome)
	}
	if err := store.recoverPendingStateTransactionLocked(); !errors.Is(err, injectedReplay) {
		store.mu.Unlock()
		t.Fatalf("candidate replay error = %v", err)
	}
	if store.committedTransactionState == nil {
		store.mu.Unlock()
		t.Fatal("valid pending transaction did not install an authoritative candidate")
	}
	store.mu.Unlock()

	inbox := &resetBarrierInbox{
		acceptStarted: make(chan struct{}),
		resetCalled:   make(chan struct{}),
		resetErr:      errors.New("injected inbox reset failure"),
	}
	background := newBackgroundRuntime(store, nil)
	background.installInbox(inbox, inbox.Health())
	store.setResetCoordinator(background.ResetWithOutcome)

	failedReset := httptest.NewRecorder()
	store.handle(failedReset, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if failedReset.Code != http.StatusInternalServerError {
		t.Fatalf("failed reset status=%d body=%s", failedReset.Code, failedReset.Body.String())
	}
	get := httptest.NewRecorder()
	store.handle(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusInternalServerError {
		t.Fatalf("post-marker candidate GET status=%d body=%s, want fail closed", get.Code, get.Body.String())
	}

	genericRan := false
	if _, err := store.updateState(func(*appState) error {
		genericRan = true
		return nil
	}); err == nil || genericRan {
		t.Fatalf("reset-blocked generic mutation ran=%v err=%v", genericRan, err)
	}
	attributeIDRan := false
	command := existingAttributeEdit("", "阻止的属性", 3)
	command.Target = attributeEditTarget{Kind: "new"}
	command.GiftRules = nil
	command.TimerRules = nil
	command.GiftCatalogUpserts = nil
	if _, err := store.applyAttributeEdit(command, func() (string, error) {
		attributeIDRan = true
		return "blocked-attribute", nil
	}); err == nil || attributeIDRan {
		t.Fatalf("reset-blocked attribute mutation generated ID=%v err=%v", attributeIDRan, err)
	}
	store.mu.Lock()
	candidatePresent := store.committedTransactionState != nil
	store.mu.Unlock()
	if !candidatePresent || !store.TransactionPending() {
		t.Fatalf("failed reset lost candidate or pending status: candidate=%v pending=%v", candidatePresent, store.TransactionPending())
	}
	if _, err := os.Stat(store.stateTransactionPath()); err != nil {
		t.Fatalf("failed reset lost WAL evidence: %v", err)
	}
	if _, err := os.Stat(store.resetIntentPath()); err != nil {
		t.Fatalf("failed reset lost marker evidence: %v", err)
	}

	inbox.resetErr = nil
	successfulReset := httptest.NewRecorder()
	store.handle(successfulReset, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if successfulReset.Code != http.StatusNoContent {
		t.Fatalf("reset retry status=%d body=%s", successfulReset.Code, successfulReset.Body.String())
	}
	store.mu.Lock()
	candidatePresent = store.committedTransactionState != nil
	store.mu.Unlock()
	if candidatePresent || store.TransactionPending() || store.MutationBlockKind() != "" {
		t.Fatalf("reset retry did not clear block: candidate=%v pending=%v kind=%q", candidatePresent, store.TransactionPending(), store.MutationBlockKind())
	}
	for _, path := range append(store.statePaths(), store.stateTransactionPath()) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reset retry left %s: %v", filepath.Base(path), err)
		}
	}

	store.writeAtomically = nil
	genericRan = false
	if _, err := store.updateState(func(state *appState) error {
		genericRan = true
		state.RoomID = "after-reset"
		return nil
	}); err != nil || !genericRan {
		t.Fatalf("ordinary generic mutation after reset ran=%v err=%v", genericRan, err)
	}
	attributeIDRan = false
	if _, err := store.applyAttributeEdit(command, func() (string, error) {
		attributeIDRan = true
		return "after-reset-attribute", nil
	}); err != nil || !attributeIDRan {
		t.Fatalf("ordinary attribute mutation after reset generated ID=%v err=%v", attributeIDRan, err)
	}
}

type markerCheckingResetInbox struct {
	markerPath string
	resetErr   error
	resetCalls int
}

type durableMarkerCheckingResetInbox struct {
	durable    *bool
	resetCalls int
}

func (*durableMarkerCheckingResetInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (*durableMarkerCheckingResetInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (*durableMarkerCheckingResetInbox) Acknowledge(string) error { return nil }
func (*durableMarkerCheckingResetInbox) Release(string) error     { return nil }
func (*durableMarkerCheckingResetInbox) Close() error             { return nil }
func (*durableMarkerCheckingResetInbox) Health() giftInboxHealth  { return giftInboxHealth{} }
func (inbox *durableMarkerCheckingResetInbox) Reset() error {
	inbox.resetCalls++
	if !*inbox.durable {
		return errors.New("inbox reset ran before startup marker republication became durable")
	}
	return nil
}

func (*markerCheckingResetInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, nil
}
func (*markerCheckingResetInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}
func (*markerCheckingResetInbox) Acknowledge(string) error { return nil }
func (*markerCheckingResetInbox) Release(string) error     { return nil }
func (*markerCheckingResetInbox) Close() error             { return nil }
func (*markerCheckingResetInbox) Health() giftInboxHealth  { return giftInboxHealth{} }
func (inbox *markerCheckingResetInbox) Reset() error {
	inbox.resetCalls++
	data, err := os.ReadFile(inbox.markerPath)
	if err != nil {
		return fmt.Errorf("reset intent was not published before inbox reset: %w", err)
	}
	if _, err := decodeResetIntentRecord(data); err != nil {
		return fmt.Errorf("reset intent is not canonical: %q: %w", data, err)
	}
	return inbox.resetErr
}

func TestBackgroundRuntimeResetPublishesIntentBeforeFailureAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	state := defaultAppState()
	state.RoomID = "candidate-before-marker"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected post-marker inbox failure")
	inbox := &markerCheckingResetInbox{markerPath: filepath.Join(dir, "reset-intent.json"), resetErr: injected}
	background := newBackgroundRuntime(store, nil)
	background.installInbox(inbox, inbox.Health())
	store.setResetCoordinator(background.ResetWithOutcome)

	failed := httptest.NewRecorder()
	store.handle(failed, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if failed.Code != http.StatusInternalServerError || inbox.resetCalls != 1 {
		t.Fatalf("failed reset status=%d calls=%d body=%s", failed.Code, inbox.resetCalls, failed.Body.String())
	}
	if _, err := os.Stat(inbox.markerPath); err != nil {
		t.Fatalf("post-marker failure lost reset intent: %v", err)
	}
	get := httptest.NewRecorder()
	store.handle(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusInternalServerError {
		t.Fatalf("post-marker GET status=%d body=%s, want fail closed", get.Code, get.Body.String())
	}
	callbackRan := false
	if _, err := store.updateState(func(*appState) error {
		callbackRan = true
		return nil
	}); err == nil || callbackRan {
		t.Fatalf("post-marker mutation ran=%v err=%v", callbackRan, err)
	}

	inbox.resetErr = nil
	retried := httptest.NewRecorder()
	store.handle(retried, httptest.NewRequest(http.MethodDelete, "/api/config", nil))
	if retried.Code != http.StatusNoContent || inbox.resetCalls != 2 {
		t.Fatalf("reset retry status=%d calls=%d body=%s", retried.Code, inbox.resetCalls, retried.Body.String())
	}
	for _, path := range append(store.statePaths(), store.stateTransactionPath(), inbox.markerPath) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful reset left %s: %v", filepath.Base(path), err)
		}
	}
}

func TestBackgroundRuntimeResetRepublishesNonDurableIntentBeforeRetirement(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	state := defaultAppState()
	state.RoomID = "preserve-until-durable-marker"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	markerPath := store.resetIntentPath()
	injected := errors.New("injected reset-intent directory sync failure")
	publicationAttempts := 0
	markerSyncAttempts := 0
	markerDurable := false
	publishedMarkers := make([][]byte, 0, 3)
	store.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if path != markerPath {
			return writeFileAtomicallyOutcome(path, data)
		}
		publicationAttempts++
		publishedMarkers = append(publishedMarkers, append([]byte(nil), data...))
		attempt := publicationAttempts
		return writeFileAtomicallyOutcomeWith(path, data, func(directory string) error {
			markerSyncAttempts++
			if attempt <= 2 {
				return injected
			}
			if err := syncStateDirectory(directory); err != nil {
				return err
			}
			markerDurable = true
			return nil
		})
	}
	inbox := &markerCheckingResetInbox{markerPath: markerPath}
	background := newBackgroundRuntime(store, nil)
	background.installInbox(inbox, inbox.Health())
	retired := make([]string, 0)
	retire := func(path string) error {
		retired = append(retired, path)
		if !markerDurable {
			return errors.New("artifact retirement started before durable reset intent")
		}
		return retireFileDurably(path)
	}
	background.retireResetArtifact = retire
	store.retireResetArtifact = retire

	if err := background.Reset(); !errors.Is(err, injected) {
		t.Fatalf("first reset error=%v, want injected marker sync failure", err)
	}
	if publicationAttempts != 1 || markerSyncAttempts != 1 {
		t.Fatalf("first reset marker publication attempts=%d sync attempts=%d, want 1/1", publicationAttempts, markerSyncAttempts)
	}
	if inbox.resetCalls != 0 || len(retired) != 0 {
		t.Fatalf("first reset ran inbox/retirement before durable marker: inbox=%d retired=%v", inbox.resetCalls, retired)
	}
	if data, err := os.ReadFile(markerPath); err != nil || !bytes.Equal(data, publishedMarkers[0]) {
		t.Fatalf("rename-visible marker data=%q err=%v", data, err)
	}
	baseline, err := decodeResetIntentRecord(publishedMarkers[0])
	if err != nil || baseline == nil || !baseline.RoomConfigured || !baseline.AutoUpdateEnabled {
		t.Fatalf("rename-visible marker baseline=%+v err=%v", baseline, err)
	}
	get := httptest.NewRecorder()
	store.handle(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusInternalServerError {
		t.Fatalf("non-durable marker GET status=%d body=%s, want fail closed", get.Code, get.Body.String())
	}

	if err := background.Reset(); !errors.Is(err, injected) {
		t.Fatalf("second reset error=%v, want durable marker republication before retirement", err)
	}
	if publicationAttempts != 2 || markerSyncAttempts != 2 {
		t.Fatalf("second reset marker publication attempts=%d sync attempts=%d, want 2/2", publicationAttempts, markerSyncAttempts)
	}
	if !bytes.Equal(publishedMarkers[0], publishedMarkers[1]) {
		t.Fatalf("non-durable marker retry changed exact bytes: first=%q second=%q", publishedMarkers[0], publishedMarkers[1])
	}
	if inbox.resetCalls != 0 || len(retired) != 0 {
		t.Fatalf("second reset ran inbox/retirement before durable marker: inbox=%d retired=%v", inbox.resetCalls, retired)
	}

	if err := background.Reset(); err != nil {
		t.Fatal(err)
	}
	if publicationAttempts != 3 || markerSyncAttempts != 3 || !markerDurable {
		t.Fatalf("durable retry marker publication attempts=%d sync attempts=%d durable=%v", publicationAttempts, markerSyncAttempts, markerDurable)
	}
	if !bytes.Equal(publishedMarkers[0], publishedMarkers[2]) {
		t.Fatalf("durable marker retry changed exact bytes: first=%q third=%q", publishedMarkers[0], publishedMarkers[2])
	}
	if inbox.resetCalls != 1 {
		t.Fatalf("durable retry inbox reset calls=%d, want 1", inbox.resetCalls)
	}
	if len(retired) == 0 || retired[len(retired)-1] != markerPath {
		t.Fatalf("durable retry retirement order=%v, want marker last", retired)
	}
	if store.resetIntentStatus != resetIntentNone || store.resetIntentDurable {
		t.Fatalf("successful reset intent status=%q durable=%v, want cleared", store.resetIntentStatus, store.resetIntentDurable)
	}
	for _, path := range append(store.statePaths(), markerPath) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("durable retry left %s: %v", filepath.Base(path), err)
		}
	}
}

func TestBackgroundRuntimeResetRepublishesStartupObservedIntentBeforeRetirement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	markerPath := filepath.Join(dir, "reset-intent.json")
	injected := errors.New("injected pre-restart reset-intent directory sync failure")
	seed := &configStore{path: path}
	seed.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		return writeFileAtomicallyOutcomeWith(path, data, func(string) error { return injected })
	}
	if err := seed.beginResetIntent(); !errors.Is(err, injected) {
		t.Fatalf("pre-restart marker publication error=%v, want injected sync failure", err)
	}
	preRestartMarker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read pre-restart rename-visible marker: %v", err)
	}

	restarted, err := initializeConfigStore(&configStore{path: path})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.resetIntentStatus != resetIntentValid || restarted.resetIntentDurable {
		t.Fatalf("startup marker status=%q durable=%v, want valid/non-durable until republished", restarted.resetIntentStatus, restarted.resetIntentDurable)
	}
	republicationAttempts := 0
	markerSyncAttempts := 0
	markerDurable := false
	restarted.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if path != markerPath {
			return writeFileAtomicallyOutcome(path, data)
		}
		republicationAttempts++
		if !bytes.Equal(data, preRestartMarker) {
			t.Fatalf("startup marker republication changed exact bytes: before=%q after=%q", preRestartMarker, data)
		}
		outcome := writeFileAtomicallyOutcomeWith(path, data, func(directory string) error {
			markerSyncAttempts++
			if err := syncStateDirectory(directory); err != nil {
				return err
			}
			markerDurable = true
			return nil
		})
		return outcome
	}
	inbox := &durableMarkerCheckingResetInbox{durable: &markerDurable}
	background := newBackgroundRuntime(restarted, nil)
	background.installInbox(inbox, inbox.Health())
	prematureRetirement := false
	retire := func(path string) error {
		if !markerDurable {
			prematureRetirement = true
			return errors.New("artifact retirement ran before startup marker republication became durable")
		}
		return retireFileDurably(path)
	}
	background.retireResetArtifact = retire
	restarted.retireResetArtifact = retire

	if err := background.Reset(); err != nil {
		t.Fatal(err)
	}
	if republicationAttempts != 1 || markerSyncAttempts != 1 || !markerDurable {
		t.Fatalf("startup marker republication attempts=%d sync attempts=%d durable=%v", republicationAttempts, markerSyncAttempts, markerDurable)
	}
	if inbox.resetCalls != 1 || prematureRetirement {
		t.Fatalf("startup reset inbox calls=%d premature retirement=%v", inbox.resetCalls, prematureRetirement)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup reset left marker: %v", err)
	}
}

func TestBackgroundRuntimeResetRetiresEveryAuthoritativeArtifactWithMarkerLast(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	state := defaultAppState()
	state.RoomID = "retire-all"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.stateTransactionPath(), []byte("reset-owned-wal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := inbox.Accept("retire-all", "SEND_GIFT", giftEvent{GiftID: 1})
	if err != nil {
		t.Fatal(err)
	}
	recordPath := inbox.recordPath(record.LocalSequence, record.IngestionID)
	background := newBackgroundRuntime(store, nil)
	background.installInbox(inbox, inbox.SnapshotHealth())
	if err := background.savePendingGiftAnimationFile(pendingGiftAnimationFile{SchemaVersion: pendingGiftAnimationsSchemaVersion, PreparedRoomID: "retire-all", Records: []pendingGiftAnimation{}}); err != nil {
		t.Fatal(err)
	}

	var retired []string
	recordRetirement := func(path string) error {
		retired = append(retired, filepath.Clean(path))
		return retireFileDurably(path)
	}
	store.retireResetArtifact = recordRetirement
	background.retireResetArtifact = recordRetirement
	inbox.shared.retireResetArtifact = recordRetirement
	if err := background.Reset(); err != nil {
		t.Fatal(err)
	}
	markerPath := store.resetIntentPath()
	markerIndex := -1
	for index, path := range retired {
		if path == markerPath {
			markerIndex = index
		}
	}
	if markerIndex != len(retired)-1 {
		t.Fatalf("reset marker retirement index=%d retirements=%#v, want last", markerIndex, retired)
	}
	for _, path := range append(append(store.statePaths(), store.stateTransactionPath(), markerPath), recordPath, inbox.sequencePath, background.pendingGiftAnimationsPath()) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("authoritative reset artifact %s remains: %v", path, err)
		}
	}
}

func TestBackgroundRuntimeResetIntentSurvivesPartialRetirementAndRestart(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	state := defaultAppState()
	state.RoomID = "ordinary-no-wal"
	state.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 7}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	stateBytes := make(map[string][]byte)
	for _, path := range store.statePaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		stateBytes[path] = data
	}
	inbox, err := openGiftInbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := inbox.Accept("ordinary-no-wal", "SEND_GIFT", giftEvent{GiftID: 1})
	if err != nil {
		t.Fatal(err)
	}
	recordPath := inbox.recordPath(record.LocalSequence, record.IngestionID)
	recordBytes, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	sequenceBytes, err := os.ReadFile(inbox.sequencePath)
	if err != nil {
		t.Fatal(err)
	}
	background := newBackgroundRuntime(store, nil)
	background.installInbox(inbox, inbox.SnapshotHealth())
	if err := background.savePendingGiftAnimationFile(pendingGiftAnimationFile{SchemaVersion: pendingGiftAnimationsSchemaVersion, PreparedRoomID: "ordinary-no-wal", Records: []pendingGiftAnimation{{RoomID: "ordinary-no-wal", Gift: giftEvent{GiftID: 1}}}}); err != nil {
		t.Fatal(err)
	}
	animationPath := background.pendingGiftAnimationsPath()
	animationBytes, err := os.ReadFile(animationPath)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected partial state retirement failure")
	failed := false
	store.retireResetArtifact = func(path string) error {
		if filepath.Base(path) == "cache.json" && !failed {
			failed = true
			return injected
		}
		return retireFileDurably(path)
	}
	if err := background.Reset(); !errors.Is(err, injected) {
		t.Fatalf("partial reset error=%v, want injected", err)
	}
	if !failed {
		t.Fatal("partial state retirement failure was not injected")
	}
	markerPath := store.resetIntentPath()
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("partial retirement lost reset marker: %v", err)
	}
	get := httptest.NewRecorder()
	store.handle(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusInternalServerError {
		t.Fatalf("partial-retirement GET status=%d body=%s", get.Code, get.Body.String())
	}
	mutationRan := false
	if _, err := store.updateState(func(*appState) error {
		mutationRan = true
		return nil
	}); err == nil || mutationRan {
		t.Fatalf("partial-retirement mutation ran=%v err=%v", mutationRan, err)
	}
	if err := inbox.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a power loss choosing arbitrary pre-reset versions for every
	// independently cached runtime artifact while the durable marker survives.
	for path, data := range stateBytes {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{recordPath: recordBytes, inbox.sequencePath: sequenceBytes, animationPath: animationBytes} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := initializeConfigStore(&configStore{path: store.path})
	if err != nil {
		t.Fatal(err)
	}
	preRecovery := httptest.NewRecorder()
	restarted.handle(preRecovery, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if preRecovery.Code != http.StatusInternalServerError {
		t.Fatalf("restart exposed resurrected state status=%d body=%s", preRecovery.Code, preRecovery.Body.String())
	}
	restartedRuntime := newBackgroundRuntime(restarted, nil)
	if err := restartedRuntime.Reset(); err != nil {
		t.Fatal(err)
	}
	completed := httptest.NewRecorder()
	restarted.handle(completed, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if completed.Code != http.StatusNoContent {
		t.Fatalf("completed restart reset GET status=%d body=%s", completed.Code, completed.Body.String())
	}
	for _, path := range append(append(restarted.statePaths(), restarted.stateTransactionPath(), markerPath), recordPath, inbox.sequencePath, animationPath) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restart reset left %s: %v", path, err)
		}
	}
}

func TestFormulaPreviewUsesSelectedGiftPrice(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"加班时间+price/1000*60",
		"attributeName":"加班时间",
		"attributeValue":0,
		"context":"gift",
		"giftPrice":5200
	}`))
	response := httptest.NewRecorder()

	handleFormulaPreview(store)(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"result":312`) {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestFormulaPreviewUsesGiftRuleIdentity(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"积分+10","condition":"用户身份>=舰长","attributeName":"积分","attributeValue":0,
		"context":"gift","userIdentity":2
	}`))
	response := httptest.NewRecorder()

	handleFormulaPreview(store)(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"triggered":true`) || !strings.Contains(response.Body.String(), `"result":10`) {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestFormulaPreviewReturnsUnchangedValueForFalseGiftCondition(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"积分+10","condition":"用户身份>=舰长","attributeName":"积分","attributeValue":4,
		"context":"gift","userIdentity":1
	}`))
	response := httptest.NewRecorder()

	handleFormulaPreview(store)(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"triggered":false`) || !strings.Contains(response.Body.String(), `"result":4`) {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestFormulaPreviewRejectsInvalidUserIdentity(t *testing.T) {
	for _, identity := range []string{"-1", "5", "1.5", `"captain"`} {
		t.Run(identity, func(t *testing.T) {
			store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
			request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
				"formula":"积分+1","attributeName":"积分","attributeValue":0,"userIdentity":`+identity+`
			}`))
			response := httptest.NewRecorder()
			handleFormulaPreview(store)(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "用户身份必须是 0 到 4 的整数") {
				t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFormulaPreviewRejectsInvalidGiftCondition(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"积分+10","condition":"用户身份>=不存在身份","attributeName":"积分","attributeValue":0,"userIdentity":2
	}`))
	response := httptest.NewRecorder()
	handleFormulaPreview(store)(response, request)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), `"triggered":false`) || !strings.Contains(response.Body.String(), "不存在身份") {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestFormulaPreviewKeepsOmittedGiftConditionCompatible(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"积分+price/1000","attributeName":"积分","attributeValue":4,"giftPrice":2000
	}`))
	response := httptest.NewRecorder()
	handleFormulaPreview(store)(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"triggered":true`) || !strings.Contains(response.Body.String(), `"result":6`) {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestFormulaPreviewKeepsUserIdentityOutOfTimerContext(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"用户身份+1","attributeName":"积分","attributeValue":0,"context":"timer","userIdentity":4
	}`))
	response := httptest.NewRecorder()
	handleFormulaPreview(store)(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "用户身份") {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestFormulaPreviewValidateOnlyChecksGiftRuleWithoutEvaluation(t *testing.T) {
	originalRandomIntn := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = originalRandomIntn })
	formulaRandomIntn = func(int) int {
		t.Fatal("validation-only request drew randomness")
		return 0
	}
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"RANDOMCHOICE(积分+10,1/0)","condition":"RANDOMCHOICE(1,1/0)",
		"attributeName":"积分","attributeValue":0,"context":"gift","validateOnly":true
	}`))
	response := httptest.NewRecorder()
	handleFormulaPreview(store)(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":0`) {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestFormulaPreviewValidateOnlyEnforcesContextAndArity(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		message string
	}{
		{name: "gift unknown variable", payload: `{"formula":"missing+1","attributeName":"积分","validateOnly":true}`, message: "missing"},
		{name: "gift wrong arity", payload: `{"formula":"IF(1,2)","attributeName":"积分","validateOnly":true}`, message: "IF 需要 3 个参数"},
		{name: "timer gift variable", payload: `{"formula":"price+1","attributeName":"积分","context":"timer","validateOnly":true}`, message: "price"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
			response := httptest.NewRecorder()
			handleFormulaPreview(store)(response, httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(test.payload)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.message) {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFormulaPreviewValidateOnlyRejectsGuaranteedRuntimeErrors(t *testing.T) {
	overflow := "1" + strings.Repeat("0", 307) + "*100"
	tests := map[string]string{
		"1/0":                         "除数为零",
		overflow:                      "规则结果不是有效数字",
		"RANDBETWEEN(10,1)":           "最小值不能大于最大值",
		"积分*(" + overflow + ")":       "规则结果不是有效数字",
		"积分/ROUND(1,309)":             "规则结果不是有效数字",
		"MAX(积分," + overflow + ")":    "规则结果不是有效数字",
		"MIN(积分,-(" + overflow + "))": "规则结果不是有效数字",
		"ROUND(积分,309)":               "规则结果不是有效数字",
		"ROUND(" + overflow + ",积分)":  "规则结果不是有效数字",
	}
	for formula, message := range tests {
		t.Run(message, func(t *testing.T) {
			store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
			payload := fmt.Sprintf(`{"formula":%q,"attributeName":"积分","validateOnly":true}`, formula)
			response := httptest.NewRecorder()
			handleFormulaPreview(store)(response, httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(payload)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), message) {
				t.Fatalf("formula %s: status = %d, body = %s", formula, response.Code, response.Body.String())
			}
		})
	}
}

func TestListenWithFallbackSkipsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	port := occupied.Addr().(*net.TCPAddr).Port
	listener, selected, err := listenWithFallback(port, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if selected == port {
		t.Fatalf("selected occupied port %d", selected)
	}
}

func TestUpdatedStartupNotifiesWithoutOpeningConfig(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 1)
	center.AttachSink(func(notification desktopNotification) { received <- notification })
	if openConfig := announceStartup(center, "1.2.3"); openConfig {
		t.Fatal("updated startup requested the configuration page")
	}
	select {
	case notification := <-received:
		if notification.Title != "直播礼物面板已更新" {
			t.Fatalf("startup notification = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("updated startup did not notify")
	}
}

func TestNormalStartupStillOpensConfig(t *testing.T) {
	if openConfig := announceStartup(newNotificationCenter(), ""); !openConfig {
		t.Fatal("normal startup no longer requests the configuration page")
	}
}

func TestListenWithFallbackUsesRequestedPortWhenAvailable(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	listener, selected, err := listenWithFallback(port, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if selected != port {
		t.Fatalf("selected port %d, want %d", selected, port)
	}
}

func TestUpdateReadyRequestsInstallWithoutConsultingPagePresence(t *testing.T) {
	updateExit := make(chan struct{}, 1)
	handler := updateReadyExitHandler(updateExit)

	handler("0.4.5")
	select {
	case <-updateExit:
		t.Fatal("ready update requested installation before the visible countdown")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-updateExit:
	case <-time.After(4 * time.Second):
		t.Fatal("ready update did not request installation after the countdown")
	}
	// Repeated readiness notifications must remain non-blocking and coalesce.
	handler("0.4.5")
}
