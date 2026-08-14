package main

import (
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

func TestMainPendingGiftClipUpdateClosesOnceBeforeInstall(t *testing.T) {
	order := []string{}
	closeCount := 0
	closeJobs := newMainGiftClipCloser(func() {
		closeCount++
		order = append(order, "jobs")
	})
	runMainPendingGiftClipUpdate(closeJobs, func() { order = append(order, "install") })
	closeJobs() // mirrors the deferred close on the pending-update return path.
	if got := strings.Join(order, ","); got != "jobs,install" || closeCount != 1 {
		t.Fatalf("pending update order=%q closeCount=%d", got, closeCount)
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
	store.setResetCoordinator(background.Reset)

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
	store.setResetCoordinator(background.Reset)

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
