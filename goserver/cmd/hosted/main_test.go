package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/adminconsole"
	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/migration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
	hostedruntime "bilibili-live-gift-panel/internal/hosted/runtime"
	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/DATA-DOG/go-sqlmock"
)

const recoveryArchiveFixture = "R1BSQQEQDCAAAIAAAAAACAAAAAEAAAEdsLGys7S1tre4ubq7vL2+v8DBwsPExcbHyMnKy/OsZfWF1Ni/wJbLtaXhn3L2O7UBZh/umY584J8IxZQ+GUUnl/8Nh+dwlW3G4KjyUbDlP2vFi3PsyML32ProgId7mHDRyuhqypPGF36mEh81bubIw9oUqbRDCLXlH7+vOA4AGOfANiolmP1ODOAo65GMpTEd6XzXrCs1Lggs3Suw7aP3Rl6Uc3vxoiHvMtVTqU0qrLPlOfzrZrQNOA573Wn473x7Fw6asWQ56+8jRwCJEiZ9JudESX7gu2uLbcRUC5NZWg+49dRWCzZ3G5aYMZm80zWBlER9ZJUoEgz2pN8ZNf1m5q8uu0y4Oz+2oKpitpoUpNLvbAxa15gNiyuQGG5xQ11uUnhX3gTI7GYQthIpy9/koeG3cr45a8uCQQ=="

func TestNewHTTPServerConfiguresHostedTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	server := newHTTPServer("127.0.0.1:12500", handler)

	if server.Addr != "127.0.0.1:12500" {
		t.Fatalf("Addr = %q, want loopback address", server.Addr)
	}
	if server.Handler != handler {
		t.Fatal("Handler does not match the provided hosted handler")
	}
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 5s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout = %v, want 15s", server.ReadTimeout)
	}
	if server.WriteTimeout != 15*time.Second {
		t.Fatalf("WriteTimeout = %v, want 15s", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %v, want 60s", server.IdleTimeout)
	}
	if server.ErrorLog == nil {
		t.Fatal("ErrorLog = nil, want hosted structured logger bridge")
	}
}

func TestLoadHostedStaticFailsFastWhenBundleIsMissing(t *testing.T) {
	if _, err := loadHostedStatic(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("loadHostedStatic() accepted a missing production bundle")
	}
}

func TestOpenHostedLogRequiresSafeExistingRegularFileAndAppends(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "app.log")
	if _, err := openHostedLog(filepath.Join(directory, "missing.log")); err == nil {
		t.Fatal("openHostedLog() accepted a missing file")
	}
	if _, err := openHostedLog(directory); err == nil {
		t.Fatal("openHostedLog() accepted a directory")
	}
	if err := os.WriteFile(path, []byte("existing\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.log")
	if err := os.Symlink(path, link); err == nil {
		if _, err := openHostedLog(link); err == nil {
			t.Fatal("openHostedLog() accepted a symlink")
		}
	}

	file, err := openHostedLog(path)
	if err != nil {
		t.Fatalf("openHostedLog(): %v", err)
	}
	var stderr bytes.Buffer
	logger := newHostedLogger(&stderr, file)
	logger.Info("synthetic hosted event")
	if err := file.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), "existing\n") || !strings.Contains(string(contents), "synthetic hosted event") {
		t.Fatalf("log file was truncated or not written: %q", contents)
	}
	if !strings.Contains(stderr.String(), "synthetic hosted event") {
		t.Fatalf("stderr did not receive hosted log: %q", stderr.String())
	}
}

func TestHostedLogWriterFailsClosedAtCapacityAndRecoversAfterCopytruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("12345678"), 0o640); err != nil {
		t.Fatal(err)
	}
	fullSignals := 0
	file, err := openHostedLogWithLimit(path, 10, func() { fullSignals++ })
	if err != nil {
		t.Fatalf("openHostedLogWithLimit(): %v", err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("abc")); !errors.Is(err, errHostedLogCapacity) {
		t.Fatalf("Write() error = %v, want capacity failure", err)
	}
	if fullSignals != 1 {
		t.Fatalf("capacity signals = %d, want 1", fullSignals)
	}
	if _, err := file.Write([]byte("abc")); !errors.Is(err, errHostedLogCapacity) || fullSignals != 1 {
		t.Fatalf("second Write() error/signals = %v/%d, want same failure once", err, fullSignals)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("fresh")); err != nil {
		t.Fatalf("Write() after copytruncate: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "fresh" {
		t.Fatalf("contents after copytruncate = %q", contents)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := openHostedLogWithLimit(path, 10, func() {}); !errors.Is(err, errHostedLogCapacity) {
		t.Fatalf("open at capacity error = %v", err)
	}
}

func TestHostedLogWriterCancelsOnceOnEveryPersistentFileFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "info.log")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		backend hostedLogBackend
	}{
		{name: "stat", backend: &hostedLogBackendStub{statInfo: info, statErr: errors.New("synthetic stat failure")}},
		{name: "write", backend: &hostedLogBackendStub{statInfo: info, writeErr: errors.New("synthetic write failure")}},
		{name: "short_write", backend: &hostedLogBackendStub{statInfo: info, writeN: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			signals := 0
			writer := &hostedLogFile{file: test.backend, maxBytes: 10, onCapacity: func() { signals++ }}
			if _, err := writer.Write([]byte("payload")); err == nil {
				t.Fatal("Write() error = nil")
			}
			_, _ = writer.Write([]byte("payload"))
			if signals != 1 {
				t.Fatalf("failure signals = %d, want 1", signals)
			}
		})
	}
}

func TestHostedLoggerWritesPrimaryFileBeforeBestEffortStderr(t *testing.T) {
	var primary bytes.Buffer
	logger := newHostedLogger(errorWriter{}, &primary)
	logger.Info("primary survives stderr failure")
	if !strings.Contains(primary.String(), "primary survives stderr failure") {
		t.Fatalf("primary log = %q", primary.String())
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic secondary failure") }

type hostedLogBackendStub struct {
	statInfo os.FileInfo
	statErr  error
	writeN   int
	writeErr error
}

func (backend *hostedLogBackendStub) Stat() (os.FileInfo, error) {
	return backend.statInfo, backend.statErr
}

func (backend *hostedLogBackendStub) Write(payload []byte) (int, error) {
	if backend.writeErr != nil {
		return 0, backend.writeErr
	}
	if backend.writeN != 0 {
		return backend.writeN, nil
	}
	return len(payload), nil
}

func (*hostedLogBackendStub) Close() error { return nil }

func TestStaticCompositionDoesNotStealOBSOrAPIRoutes(t *testing.T) {
	static := statusHandler(http.StatusNonAuthoritativeInfo)
	obs := statusHandler(http.StatusTeapot)
	handler := composeHostedHTTPWithRuntimeOBSAndStatic(
		healthyHostedDatabase{}, statusHandler(http.StatusAccepted), statusHandler(http.StatusAccepted),
		statusHandler(http.StatusAccepted), nil, nil, nil, nil, obs, nil, nil, static, "runtime-csrf",
	)
	publicID := strings.Repeat("A", 43)
	for _, test := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/", http.StatusNonAuthoritativeInfo},
		{http.MethodGet, "/hosted.html", http.StatusNonAuthoritativeInfo},
		{http.MethodGet, "/assets/app.js", http.StatusNonAuthoritativeInfo},
		{http.MethodGet, "/obs/" + publicID, http.StatusNonAuthoritativeInfo},
		{http.MethodGet, "/obs/" + publicID + "/events", http.StatusTeapot},
		{http.MethodPost, "/obs/" + publicID + "/exchange", http.StatusTeapot},
		{http.MethodGet, "/api/bootstrap", http.StatusOK},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, response.Code, test.want)
		}
	}
}

func TestProductionHTTPServerContextsFollowProcessShutdown(t *testing.T) {
	processContext, cancel := context.WithCancel(context.Background())
	server := newHTTPServerWithContext(processContext, "127.0.0.1:12500", http.NewServeMux())
	if server.BaseContext == nil {
		t.Fatal("BaseContext is nil")
	}
	requestContext := server.BaseContext(nil)
	cancel()
	select {
	case <-requestContext.Done():
	default:
		t.Fatal("process cancellation did not cancel active request base context")
	}
}

func TestComposeHostedHTTPMakesAllInvitationRoutesReachableWithSpecificity(t *testing.T) {
	auth := statusHandler(http.StatusAccepted)
	admin := statusHandler(http.StatusNonAuthoritativeInfo)
	invitation := statusHandler(http.StatusTeapot)
	handler := composeHostedHTTP(healthyHostedDatabase{}, auth, admin, invitation, nil, nil, nil, "runtime-csrf")

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/api/auth/registration", http.StatusTeapot},
		{http.MethodGet, "/api/invitations", http.StatusTeapot},
		{http.MethodPost, "/api/invitations", http.StatusTeapot},
		{http.MethodDelete, "/api/invitations/71", http.StatusTeapot},
		{http.MethodPost, "/api/admin/invitations", http.StatusTeapot},
		{http.MethodGet, "/api/admin/invitations?sort=status&direction=asc", http.StatusTeapot},
		{http.MethodDelete, "/api/admin/invitations/71", http.StatusTeapot},
		{http.MethodPost, "/api/admin/accounts/41/invitation-quota", http.StatusTeapot},
		{http.MethodPost, "/api/auth/session", http.StatusAccepted},
		{http.MethodPost, "/api/admin/accounts/41/disable", http.StatusAccepted},
		{http.MethodPost, "/api/admin/totp", http.StatusNonAuthoritativeInfo},
		{http.MethodGet, "/api/bootstrap", http.StatusOK},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s status=%d want=%d", test.method, test.path, response.Code, test.want)
		}
	}
}

func TestComposeHostedHTTPMakesAllConfigurationRoutesReachableWithSpecificity(t *testing.T) {
	auth := statusHandler(http.StatusAccepted)
	admin := statusHandler(http.StatusNonAuthoritativeInfo)
	invitation := statusHandler(http.StatusTeapot)
	configuration := statusHandler(http.StatusCreated)
	handler := composeHostedHTTP(healthyHostedDatabase{}, auth, admin, invitation, configuration, nil, nil, "runtime-csrf")

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/configuration"},
		{http.MethodPut, "/api/configuration/definition"},
		{http.MethodPut, "/api/configuration/state"},
		{http.MethodPut, "/api/configuration/room-suggestion"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusCreated {
			t.Fatalf("%s %s status=%d want configuration handler", route.method, route.path, response.Code)
		}
	}
}

func TestComposeHostedHTTPMakesMigrationRoutesReachableWithSpecificity(t *testing.T) {
	migration := statusHandler(http.StatusCreated)
	handler := composeHostedHTTP(healthyHostedDatabase{}, statusHandler(http.StatusAccepted), statusHandler(http.StatusNonAuthoritativeInfo), statusHandler(http.StatusTeapot), nil, migration, nil, "runtime-csrf")
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/migrations/preview"},
		{http.MethodPost, "/api/migrations/9/apply"},
		{http.MethodDelete, "/api/migrations/9"},
		{http.MethodPost, "/api/migrations/9/rollback"},
		{http.MethodGet, "/api/migrations/9"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusCreated {
			t.Fatalf("%s %s status=%d", route.method, route.path, response.Code)
		}
	}
}

func TestComposeHostedHTTPMakesBiliServiceRoutesReachableWithSpecificity(t *testing.T) {
	biliService := statusHandler(http.StatusCreated)
	handler := composeHostedHTTP(healthyHostedDatabase{}, statusHandler(http.StatusAccepted), statusHandler(http.StatusNonAuthoritativeInfo), statusHandler(http.StatusTeapot), nil, nil, biliService, "runtime-csrf")
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/bili-service/status"},
		{http.MethodPost, "/api/admin/bili-service/challenge"},
		{http.MethodPost, "/api/admin/bili-service/replace"},
		{http.MethodPost, "/api/admin/bili-service/check"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusCreated {
			t.Fatalf("%s %s status=%d", route.method, route.path, response.Code)
		}
	}
}

func TestComposeHostedHTTPMountsOnlyAutomaticRuntimeRoutes(t *testing.T) {
	runtimeHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		allowed := map[string]string{
			"/api/runtime/room": http.MethodPut, "/api/runtime/events": http.MethodGet, "/api/runtime/status": http.MethodGet,
		}
		if request.Method != allowed[request.URL.Path] {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusCreated)
	})
	handler := composeHostedHTTPWithRuntime(healthyHostedDatabase{}, statusHandler(http.StatusAccepted), statusHandler(http.StatusNonAuthoritativeInfo), statusHandler(http.StatusTeapot), nil, nil, nil, runtimeHandler, "runtime-csrf")
	for _, route := range []struct{ method, path string }{
		{http.MethodPut, "/api/runtime/room"}, {http.MethodGet, "/api/runtime/events"}, {http.MethodGet, "/api/runtime/status"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusCreated {
			t.Fatalf("%s %s status=%d, want runtime handler", route.method, route.path, response.Code)
		}
	}
	for _, path := range []string{"/api/runtime/start", "/api/runtime/stop"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status=%d, want no route", path, response.Code)
		}
	}
}

func TestComposeHostedHTTPMountsOBSPathsAheadOfBroadAccountAndAdminHandlers(t *testing.T) {
	obsHandler := statusHandler(http.StatusPartialContent)
	handler := composeHostedHTTPWithRuntimeAndOBS(
		healthyHostedDatabase{},
		statusHandler(http.StatusAccepted),
		statusHandler(http.StatusNonAuthoritativeInfo),
		statusHandler(http.StatusTeapot),
		nil, nil, nil, nil, obsHandler, "runtime-csrf",
	)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/accounts/41/obs-credential"},
		{http.MethodPut, "/api/admin/accounts/41/obs-credential"},
		{http.MethodPost, "/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/exchange"},
		{http.MethodGet, "/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/events"},
		{http.MethodDelete, "/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/events"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusPartialContent {
			t.Fatalf("%s %s status=%d, want OBS handler", route.method, route.path, response.Code)
		}
	}
}

func TestComposeHostedHTTPKeepsWrongBiliServiceMethodsOutOfBroadAdmin(t *testing.T) {
	allowed := map[string]string{
		"/api/admin/bili-service/status":    http.MethodGet,
		"/api/admin/bili-service/challenge": http.MethodPost,
		"/api/admin/bili-service/replace":   http.MethodPost,
		"/api/admin/bili-service/check":     http.MethodPost,
	}
	biliService := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != allowed[request.URL.Path] {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusCreated)
	})
	handler := composeHostedHTTP(healthyHostedDatabase{}, statusHandler(http.StatusAccepted), statusHandler(http.StatusNonAuthoritativeInfo), statusHandler(http.StatusTeapot), nil, nil, biliService, "runtime-csrf")

	for path, allowedMethod := range allowed {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodHead, "BREW"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			want := http.StatusMethodNotAllowed
			if method == allowedMethod {
				want = http.StatusCreated
			}
			if response.Code != want {
				t.Fatalf("%s %s status=%d want=%d from Bili service handler", method, path, response.Code, want)
			}
		}
	}
}

func TestNewProductionBiliGatewayUsesCanonicalEndpointsAndIsRetainedByHTTP(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var captured biligateway.HTTPUpstreamOptions
	dependencies, err := newProductionBiliGateway(database, keys, func(options biligateway.HTTPUpstreamOptions) (*biligateway.HTTPUpstream, error) {
		captured = options
		return biligateway.NewHTTPUpstream(options)
	})
	if err != nil {
		t.Fatalf("newProductionBiliGateway() error = %v", err)
	}
	if dependencies.Credentials == nil || dependencies.Gateway == nil {
		t.Fatalf("production Bili dependencies = %#v", dependencies)
	}
	mock.ExpectBegin()
	transaction, err := dependencies.Credentials.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("production credential transaction: %v", err)
	}
	mock.ExpectRollback()
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("rollback production credential transaction: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("production credential transaction expectations: %v", err)
	}
	if captured.RoomInfoEndpoint != "https://api.live.bilibili.com/room/v1/Room/room_init" ||
		captured.GiftCatalogEndpoint != "https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig" ||
		captured.DanmakuInfoEndpoint != "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo" {
		t.Fatalf("production Bili endpoints = %#v", captured)
	}

	base := statusHandler(http.StatusNoContent)
	retained := retainBiliGateway(base, dependencies.Gateway)
	owner, ok := retained.(*biliGatewayOwner)
	if !ok || owner.Gateway != dependencies.Gateway {
		t.Fatalf("HTTP lifecycle does not retain production gateway: %#v", retained)
	}
	response := httptest.NewRecorder()
	owner.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("retaining wrapper changed HTTP behavior: status=%d", response.Code)
	}
}

func TestNewProductionBiliGatewayFailsBeforeBindOnInvalidDependencies(t *testing.T) {
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	factoryCalled := false
	if _, err := newProductionBiliGateway(nil, keys, func(options biligateway.HTTPUpstreamOptions) (*biligateway.HTTPUpstream, error) {
		factoryCalled = true
		return biligateway.NewHTTPUpstream(options)
	}); err == nil || factoryCalled {
		t.Fatalf("nil database error=%v factoryCalled=%v", err, factoryCalled)
	}

	database, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	want := errors.New("upstream construction failed")
	if _, err := newProductionBiliGateway(database, keys, func(biligateway.HTTPUpstreamOptions) (*biligateway.HTTPUpstream, error) {
		return nil, want
	}); !errors.Is(err, want) {
		t.Fatalf("upstream construction error=%v, want wrapped %v", err, want)
	}
}

// This test fails if main bypasses runtime.Manager.SetRoom, refreshes before
// the canonical target is persisted, or hides a durable reference-sync error.
func TestProductionRoomMutationCanonicalizesThenRefreshesAndRetries(t *testing.T) {
	sessions := &mainRoomMutationSessions{}
	sources := &mainRoomMutationSources{}
	manager, err := hostedruntime.NewManager(hostedruntime.Dependencies{
		Sessions: sessions, Configuration: mainRuntimeConfiguration{}, Migration: mainRuntimeMigration{}, RoomSources: sources,
	}, hostedruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	wantSyncErr := errors.New("reference sync unavailable")
	refresher := &mainRoomReferenceRefresher{sessions: sessions, failures: 1, err: wantSyncErr}
	mutation := productionRoomMutation{runtime: manager, references: refresher}

	if _, err := mutation.SetRoom(context.Background(), 7, "7"); !errors.Is(err, wantSyncErr) {
		t.Fatalf("first SetRoom error = %v, want reference sync failure", err)
	}
	result, err := mutation.SetRoom(context.Background(), 7, "7")
	if err != nil {
		t.Fatalf("retry SetRoom: %v", err)
	}
	if result.OldCanonical != "42" || result.NewCanonical != "42" {
		t.Fatalf("retry mutation result = %#v, want exact committed 42 -> 42", result)
	}
	if targets := sessions.persistedTargets(); len(targets) != 2 || targets[0] != "42" || targets[1] != "42" {
		t.Fatalf("persisted canonical targets = %#v, want [42 42]", targets)
	}
	if observed := refresher.observedTargets(); len(observed) != 2 || observed[0] != "42" || observed[1] != "42" {
		t.Fatalf("refresh observed targets = %#v, want canonical persistence before each refresh", observed)
	}
}

func TestProductionRoomMutationStopsBeforeRefreshWhenRuntimeFenceFails(t *testing.T) {
	want := errors.New("fence mismatch")
	setter := roomSetterFunc(func(context.Context, int64, string) (hostedruntime.RoomMutationResult, error) {
		return hostedruntime.RoomMutationResult{}, want
	})
	refresher := &mainRoomReferenceRefresher{}
	mutation := productionRoomMutation{runtime: setter, references: refresher}
	if _, err := mutation.SetRoom(context.Background(), 7, "7"); !errors.Is(err, want) {
		t.Fatalf("SetRoom error = %v", err)
	}
	if refresher.calls != 0 {
		t.Fatalf("runtime failure still refreshed references %d times", refresher.calls)
	}
}

func TestProductionRoomMutationPreservesAdministratorNotFoundContract(t *testing.T) {
	setter := roomSetterFunc(func(context.Context, int64, string) (hostedruntime.RoomMutationResult, error) {
		return hostedruntime.RoomMutationResult{}, hostedruntime.ErrAccountNotFound
	})
	mutation := productionRoomMutation{runtime: setter, references: &mainRoomReferenceRefresher{}}
	if _, err := mutation.SetRoom(context.Background(), 404, "7"); !errors.Is(err, adminconsole.ErrNotFound) {
		t.Fatalf("missing-account SetRoom error = %v, want administrator not found", err)
	}
}

// This table fails if any post-Manager initialization return escapes without
// the one production cleanup path, or if ownership is cleared before the
// watcher/runtime goroutines and room sources have quiesced.
func TestProductionRuntimeLifecycleCleansEveryPostManagerInitializationFailure(t *testing.T) {
	cases := []struct {
		name       string
		standalone bool
		room       bool
		want       []string
	}{
		{name: "runtime HTTP", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "OBS service", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "OBS HTTP", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "administrator HTTP", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "administrator settings", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "administrator settings HTTP", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "probe cadence", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "room watcher", want: []string{"runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "room runtime", standalone: true, want: []string{"watcher-close", "watcher-wait", "runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "administrator console", room: true, want: []string{"room-shutdown", "room-wait", "runtime-shutdown", "runtime-wait", "owner-clear"}},
		{name: "administrator console HTTP", room: true, want: []string{"room-shutdown", "room-wait", "runtime-shutdown", "runtime-wait", "owner-clear"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			log := &runtimeStartupLog{}
			runtime := &runtimeStartupResource{name: "runtime", log: log}
			want := errors.New("fail " + test.name)
			err := withProductionRuntimeLifecycle(context.Background(), time.Second, runtime, func() { log.add("owner-clear") }, func(lifecycle *productionRuntimeLifecycle) error {
				if test.standalone {
					lifecycle.TrackWatcher(&watcherStartupResource{log: log})
				}
				if test.room {
					lifecycle.TrackWatcher(&watcherStartupResource{log: log})
					lifecycle.TrackRoomRuntime(&runtimeStartupResource{name: "room", log: log})
				}
				return want
			})
			if !errors.Is(err, want) {
				t.Fatalf("initialization error = %v, want %v", err, want)
			}
			if got := log.snapshot(); !slices.Equal(got, test.want) {
				t.Fatalf("cleanup order = %v, want %v", got, test.want)
			}
		})
	}
}

func TestServeHTTPReturnsBindErrorBeforeServingOrAnnouncing(t *testing.T) {
	bindErr := errors.New("bind failed")
	var serveCalls atomic.Int32
	server := lifecycleStub{
		serve: func(net.Listener) error {
			serveCalls.Add(1)
			return nil
		},
	}
	var announceCalls int
	var gotNetwork, gotAddress string

	err := serveHTTP(
		context.Background(),
		server,
		"127.0.0.1:12500",
		func(network, address string) (net.Listener, error) {
			gotNetwork, gotAddress = network, address
			return nil, bindErr
		},
		30*time.Second,
		func() { announceCalls++ },
	)

	if !errors.Is(err, bindErr) {
		t.Fatalf("serveHTTP() error = %v, want bind failure", err)
	}
	if gotNetwork != "tcp" || gotAddress != "127.0.0.1:12500" {
		t.Fatalf("listen called with %q %q, want tcp and configured address", gotNetwork, gotAddress)
	}
	if serveCalls.Load() != 0 {
		t.Fatalf("Serve called %d times after bind failure", serveCalls.Load())
	}
	if announceCalls != 0 {
		t.Fatalf("onListening called %d times after bind failure", announceCalls)
	}
}

func TestServeHTTPReturnsUnexpectedServeErrorAndClosesListener(t *testing.T) {
	listener := newTrackedListener()
	serveErr := errors.New("serve failed")
	announced := false
	server := lifecycleStub{
		serve: func(got net.Listener) error {
			if got != listener {
				t.Errorf("Serve listener = %T, want tracked listener", got)
			}
			return serveErr
		},
	}

	err := serveHTTP(
		context.Background(),
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {
			if listener.isClosed() {
				t.Error("listener was closed before listening announcement")
			}
			announced = true
		},
	)

	if !errors.Is(err, serveErr) {
		t.Fatalf("serveHTTP() error = %v, want Serve failure", err)
	}
	if !announced {
		t.Fatal("onListening was not called after successful bind")
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after Serve failure")
	}
}

func TestServeHTTPShutdownUsesConfiguredDeadlineAndWaitsForServerClosed(t *testing.T) {
	processContext, cancelProcess := context.WithCancel(context.Background())
	cancelProcess()
	listener := newTrackedListener()
	releaseServe := make(chan struct{})
	deadlineRemaining := make(chan time.Duration, 1)
	server := lifecycleStub{
		serve: func(net.Listener) error {
			<-releaseServe
			return http.ErrServerClosed
		},
		shutdown: func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("Shutdown context has no deadline")
				deadlineRemaining <- 0
			} else {
				deadlineRemaining <- time.Until(deadline)
			}
			close(releaseServe)
			return nil
		},
		close: func() error {
			t.Error("Close called after successful graceful shutdown")
			return nil
		},
	}

	err := serveHTTP(
		processContext,
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {},
	)
	if err != nil {
		t.Fatalf("serveHTTP() error = %v", err)
	}

	remaining := <-deadlineRemaining
	if remaining < 29*time.Second || remaining > 30*time.Second {
		t.Fatalf("Shutdown deadline remaining = %v, want approximately 30s", remaining)
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after graceful shutdown")
	}
}

func TestServeHTTPWithRuntimeStartsRuntimeShutdownBeforeWaitingForBlockedHTTPAndJoinsErrors(t *testing.T) {
	processContext, cancelProcess := context.WithCancel(context.Background())
	listener := newTrackedListener()
	runtimeStarted := make(chan struct{})
	releaseRuntime := make(chan struct{})
	releaseServe := make(chan struct{})
	runtimeErr := errors.New("runtime shutdown failed")
	httpErr := errors.New("HTTP shutdown failed")
	server := lifecycleStub{
		serve: func(net.Listener) error {
			<-releaseServe
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error {
			select {
			case <-runtimeStarted:
			case <-time.After(time.Second):
				return errors.New("HTTP shutdown waited before runtime shutdown was initiated")
			}
			close(releaseRuntime)
			return httpErr
		},
		close: func() error {
			close(releaseServe)
			return nil
		},
	}
	runtimeShutdown := func(context.Context) error {
		close(runtimeStarted)
		<-releaseRuntime
		return runtimeErr
	}
	done := make(chan error, 1)
	go func() {
		done <- serveHTTPWithRuntime(
			processContext,
			server,
			"127.0.0.1:12500",
			func(string, string) (net.Listener, error) { return listener, nil },
			30*time.Second,
			func() {},
			runtimeShutdown,
		)
	}()
	cancelProcess()
	select {
	case err := <-done:
		if !errors.Is(err, httpErr) || !errors.Is(err, runtimeErr) {
			t.Fatalf("serveHTTPWithRuntime error = %v, want joined HTTP and runtime errors", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent runtime/HTTP shutdown deadlocked")
	}
}

func TestShutdownAndJoinRuntimeNeverWaitsBeyondGraceContext(t *testing.T) {
	graceContext, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	var waitContextErr error
	err := shutdownAndJoinRuntime(
		graceContext,
		func(ctx context.Context) error { return ctx.Err() },
		func(ctx context.Context) error {
			waitContextErr = ctx.Err()
			return ctx.Err()
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want original grace timeout", err)
	}
	if !errors.Is(waitContextErr, context.DeadlineExceeded) {
		t.Fatalf("runtime Wait context error = %v, want same expired grace context", waitContextErr)
	}
}

func TestProcessShutdownCancelsBlockedSetRoomBeforeHTTPShutdownWaits(t *testing.T) {
	sessions := &blockedSwitchSessions{pendingStarted: make(chan struct{})}
	sources := &mainRuntimeSources{}
	manager, err := hostedruntime.NewManager(hostedruntime.Dependencies{
		Sessions: sessions, Configuration: mainRuntimeConfiguration{}, Migration: mainRuntimeMigration{}, RoomSources: sources,
	}, hostedruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(context.Background(), 7, hostedruntime.LeaseConfig); err != nil {
		t.Fatal(err)
	}
	switchDone := make(chan error, 1)
	go func() { switchDone <- manager.SetRoom(context.Background(), 7, "84") }()
	<-sessions.pendingStarted

	processContext, cancelProcess := context.WithCancel(context.Background())
	releaseServe := make(chan struct{})
	listener := newTrackedListener()
	server := lifecycleStub{
		serve: func(net.Listener) error {
			<-releaseServe
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error {
			if err := <-switchDone; !errors.Is(err, hostedruntime.ErrUnavailable) {
				t.Errorf("blocked SetRoom error = %v, want unavailable", err)
			}
			close(releaseServe)
			return nil
		},
		close: func() error { return nil },
	}
	done := make(chan error, 1)
	go func() {
		done <- serveHTTPWithRuntime(processContext, server, "127.0.0.1:12500", func(string, string) (net.Listener, error) { return listener, nil }, 30*time.Second, func() {}, manager.Shutdown)
	}()
	cancelProcess()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked SetRoom prevented process shutdown")
	}
}

func TestServeHTTPClosesServerWhenShutdownFails(t *testing.T) {
	processContext, cancelProcess := context.WithCancel(context.Background())
	cancelProcess()
	listener := newTrackedListener()
	shutdownErr := errors.New("shutdown failed")
	releaseServe := make(chan struct{})
	serveExited := make(chan struct{})
	closeCalled := make(chan struct{}, 1)
	server := lifecycleStub{
		serve: func(net.Listener) error {
			<-releaseServe
			close(serveExited)
			return http.ErrServerClosed
		},
		shutdown: func(context.Context) error { return shutdownErr },
		close: func() error {
			closeCalled <- struct{}{}
			close(releaseServe)
			return nil
		},
	}

	err := serveHTTP(
		processContext,
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {},
	)
	if !errors.Is(err, shutdownErr) {
		t.Fatalf("serveHTTP() error = %v, want Shutdown failure", err)
	}
	select {
	case <-closeCalled:
	default:
		t.Fatal("Close was not called after Shutdown failure")
	}
	select {
	case <-serveExited:
	case <-time.After(time.Second):
		t.Fatal("Serve did not exit after Close")
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after Shutdown failure")
	}
}

func TestServeHTTPTreatsServerClosedAsNormalAndClosesListener(t *testing.T) {
	listener := newTrackedListener()
	server := lifecycleStub{
		serve: func(net.Listener) error { return http.ErrServerClosed },
	}

	err := serveHTTP(
		context.Background(),
		server,
		"127.0.0.1:12500",
		func(string, string) (net.Listener, error) { return listener, nil },
		30*time.Second,
		func() {},
	)
	if err != nil {
		t.Fatalf("serveHTTP() error = %v, want normal ServerClosed result", err)
	}
	if !listener.isClosed() {
		t.Fatal("listener remained open after ServerClosed")
	}
}

func TestRunModeAdminInitPrintsOneTimeSecretsAndNeverStartsHTTP(t *testing.T) {
	initializer := &initializerStub{result: adminidentity.InitializeResult{
		TOTPURI:          "otpauth://totp/GiftPanel:owner?secret=ONCE",
		RecoveryPassword: "12345678901234567890",
		HandoffToken:     "one-time-confirmation-token",
	}}
	var output bytes.Buffer
	serveCalls := 0

	err := runMode(
		context.Background(),
		[]string{"admin", "init", "--email", "owner@example.com"},
		initializer,
		&output,
		func() error { serveCalls++; return nil },
	)
	if err != nil {
		t.Fatalf("runMode() error = %v", err)
	}
	if serveCalls != 0 {
		t.Fatalf("HTTP serve called %d times during local admin init", serveCalls)
	}
	if initializer.email != "owner@example.com" {
		t.Fatalf("Initialize email=%q", initializer.email)
	}
	for _, secret := range []string{initializer.result.TOTPURI, initializer.result.RecoveryPassword, initializer.result.HandoffToken} {
		if got := bytes.Count(output.Bytes(), []byte(secret)); got != 1 {
			t.Fatalf("secret %q appeared %d times in output %q", secret, got, output.String())
		}
	}
	for _, forbidden := range []string{"32249588", "owner@example.com", "MYSQL", "HOSTED_"} {
		if bytes.Contains(output.Bytes(), []byte(forbidden)) {
			t.Fatalf("CLI output exposed %q: %q", forbidden, output.String())
		}
	}
}

func TestRunModeDecryptsGeneratedRecoveryArchiveFromStdinWithoutPasswordArgument(t *testing.T) {
	archive, err := base64.StdEncoding.DecodeString(recoveryArchiveFixture)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "admin-recovery.gpra")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runModeWithInput(context.Background(), []string{"admin", "recovery", "decrypt", "--archive", archivePath, "--password-stdin"}, nil, strings.NewReader("oaKjpKWmp6ipqqusra6v\n"), &output, nil)
	if err != nil {
		t.Fatalf("decrypt command error = %v", err)
	}
	lines := strings.Fields(output.String())
	if len(lines) != adminidentity.RecoveryCodeCount {
		t.Fatalf("code lines=%d output=%q", len(lines), output.String())
	}
	for index, line := range lines {
		raw := make([]byte, adminidentity.RecoveryCodeBytes)
		for offset := range raw {
			raw[offset] = byte(index*adminidentity.RecoveryCodeBytes + offset + 1)
		}
		if want := base64.RawURLEncoding.EncodeToString(raw); line != want {
			t.Fatalf("code %d=%q want=%q", index, line, want)
		}
	}
	if err := runModeWithInput(context.Background(), []string{"admin", "recovery", "decrypt", "--archive", archivePath, "--password", "oaKjpKWmp6ipqqusra6v"}, nil, strings.NewReader(""), &bytes.Buffer{}, nil); !errors.Is(err, errInvalidCommand) {
		t.Fatalf("password argv error=%v, want invalid command", err)
	}
}

func TestRunDecryptsRecoveryArchiveBeforeConfigurationOrNetworkInitialization(t *testing.T) {
	archive, err := base64.StdEncoding.DecodeString(recoveryArchiveFixture)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "admin-recovery.gpra")
	passwordPath := filepath.Join(directory, "password.txt")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordPath, []byte("oaKjpKWmp6ipqqusra6v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HOSTED_LISTEN_ADDR", "HOSTED_MYSQL_DSN", "HOSTED_ENCRYPTION_KEY_FILE", "HOSTED_HMAC_KEY_FILE", "HOSTED_SMTP_ADDRESS"} {
		t.Setenv(name, "")
	}
	oldArgs, oldStdout := os.Args, os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"gift-panel-hosted", "admin", "recovery", "decrypt", "--archive", archivePath, "--password-file", passwordPath}
	os.Stdout = writer
	t.Cleanup(func() { os.Args = oldArgs; os.Stdout = oldStdout; _ = reader.Close(); _ = writer.Close() })
	err = run()
	_ = writer.Close()
	os.Stdout = oldStdout
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil {
		t.Fatalf("offline run decrypt error=%v", err)
	}
	if lines := strings.Fields(string(output)); len(lines) != adminidentity.RecoveryCodeCount {
		t.Fatalf("offline run output lines=%d output=%q", len(lines), output)
	}
}

func TestRunModeRepeatedAdminInitFailsClosedWithoutPrintingOrListening(t *testing.T) {
	initializer := &initializerStub{err: adminidentity.ErrAlreadyInitialized}
	var output bytes.Buffer
	serveCalls := 0
	err := runMode(
		context.Background(),
		[]string{"admin", "init", "--email", "owner@example.com"},
		initializer,
		&output,
		func() error { serveCalls++; return nil },
	)
	if !errors.Is(err, adminidentity.ErrAlreadyInitialized) {
		t.Fatalf("runMode() error = %v", err)
	}
	if output.Len() != 0 || serveCalls != 0 {
		t.Fatalf("failed init output=%q serveCalls=%d", output.String(), serveCalls)
	}
}

func TestRunModeNormalServiceAndInvalidCommandLifecycle(t *testing.T) {
	initializer := &initializerStub{}
	serveCalls := 0
	if err := runMode(context.Background(), nil, initializer, &bytes.Buffer{}, func() error { serveCalls++; return nil }); err != nil {
		t.Fatalf("normal runMode() error = %v", err)
	}
	if serveCalls != 1 || initializer.calls != 0 {
		t.Fatalf("normal mode serveCalls=%d initCalls=%d", serveCalls, initializer.calls)
	}

	serveCalls = 0
	var output bytes.Buffer
	err := runMode(context.Background(), []string{"admin", "init", "--email", "owner@example.com", "unexpected"}, initializer, &output, func() error { serveCalls++; return nil })
	if !errors.Is(err, errInvalidCommand) || serveCalls != 0 || output.Len() != 0 {
		t.Fatalf("invalid command error=%v serveCalls=%d output=%q", err, serveCalls, output.String())
	}
	err = runMode(context.Background(), []string{"admin", "init", "--uid", "32249588", "--email", "owner@example.com"}, initializer, &output, func() error { serveCalls++; return nil })
	if !errors.Is(err, errInvalidCommand) || serveCalls != 0 || output.Len() != 0 || initializer.calls != 0 {
		t.Fatalf("legacy UID command error=%v initCalls=%d serveCalls=%d output=%q", err, initializer.calls, serveCalls, output.String())
	}
}

func TestRunModeWithCleanupDoesNotStartCleanupForAdministratorCLI(t *testing.T) {
	initializer := &initializerStub{result: adminidentity.InitializeResult{TOTPURI: "otpauth://pending", RecoveryPassword: "12345678901234567890", HandoffToken: "pending-handoff"}}
	cleanupCalls, serveCalls := 0, 0
	err := runModeWithCleanup(context.Background(), []string{"admin", "init", "--email", "owner@example.com"}, initializer, strings.NewReader(""), &bytes.Buffer{}, func(context.Context) { cleanupCalls++ }, func() error { serveCalls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 0 || serveCalls != 0 {
		t.Fatalf("admin CLI cleanupCalls=%d serveCalls=%d", cleanupCalls, serveCalls)
	}
}

func TestRunModeWithCleanupJoinsCleanupBeforeRepositoryClose(t *testing.T) {
	started := make(chan struct{})
	var mu sync.Mutex
	order := make([]string, 0, 2)
	cleanup := func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		mu.Lock()
		order = append(order, "cleanup-exit")
		mu.Unlock()
	}
	err := runModeWithCleanup(context.Background(), nil, &initializerStub{}, strings.NewReader(""), &bytes.Buffer{}, cleanup, func() error { <-started; return nil })
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	order = append(order, "repository-close")
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "cleanup-exit" || got[1] != "repository-close" {
		t.Fatalf("shutdown order=%v", got)
	}
}

type initializerStub struct {
	result adminidentity.InitializeResult
	err    error
	email  string
	calls  int
}

type blockedSwitchSessions struct {
	mu             sync.Mutex
	nextID         int64
	pendingOnce    sync.Once
	pendingStarted chan struct{}
	owner          hostedruntime.OwnerFence
}

func (*blockedSwitchSessions) AccountEnabled(context.Context, int64) (bool, error) {
	return true, nil
}
func (sessions *blockedSwitchSessions) ClaimOwnership(_ context.Context, accountID int64, token hostedruntime.OwnerToken, _ time.Duration) (hostedruntime.OwnerClaim, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.owner == (hostedruntime.OwnerFence{}) {
		sessions.owner = hostedruntime.OwnerFence{AccountID: accountID, Token: token, Epoch: 1}
	}
	return hostedruntime.OwnerClaim{Fence: sessions.owner, Reconcile: false}, nil
}
func (*blockedSwitchSessions) RenewOwnership(context.Context, hostedruntime.OwnerFence, time.Duration) error {
	return nil
}
func (*blockedSwitchSessions) ReleaseOwnership(context.Context, hostedruntime.OwnerFence) error {
	return nil
}
func (*blockedSwitchSessions) TargetRoom(context.Context, int64) (string, error) {
	return "42", nil
}
func (*blockedSwitchSessions) PersistTargetRoom(context.Context, hostedruntime.PersistTargetRoomCommand) error {
	return nil
}
func (sessions *blockedSwitchSessions) StartSession(_ context.Context, command hostedruntime.StartSessionCommand) (hostedruntime.Session, error) {
	sessions.mu.Lock()
	sessions.nextID++
	id := sessions.nextID
	sessions.mu.Unlock()
	return hostedruntime.Session{ID: id, AccountID: command.AccountID, RoomID: command.RoomID, ConfigVersionID: command.ConfigVersionID, StartedAt: command.StartedAt}, nil
}
func (*blockedSwitchSessions) EndSession(context.Context, hostedruntime.EndSessionCommand) error {
	return nil
}
func (sessions *blockedSwitchSessions) PendingMigration(ctx context.Context, _ int64) (int64, bool, error) {
	sessions.pendingOnce.Do(func() { close(sessions.pendingStarted) })
	<-ctx.Done()
	return 0, false, ctx.Err()
}

type mainRuntimeConfiguration struct{}

func (mainRuntimeConfiguration) LoadActive(context.Context, int64) (configuration.Version, configuration.State, error) {
	return configuration.Version{ID: 1}, configuration.State{}, nil
}

type mainRuntimeMigration struct{}

func (mainRuntimeMigration) ApplyPendingAfterSession(context.Context, migration.OwnerFence, int64) (migration.Job, error) {
	return migration.Job{}, nil
}

type mainRoomMutationSessions struct {
	mu        sync.Mutex
	target    string
	persisted []string
}

func (*mainRoomMutationSessions) ClaimOwnership(_ context.Context, accountID int64, token hostedruntime.OwnerToken, _ time.Duration) (hostedruntime.OwnerClaim, error) {
	return hostedruntime.OwnerClaim{Fence: hostedruntime.OwnerFence{AccountID: accountID, Token: token, Epoch: 1}}, nil
}
func (*mainRoomMutationSessions) RenewOwnership(context.Context, hostedruntime.OwnerFence, time.Duration) error {
	return nil
}
func (*mainRoomMutationSessions) ReleaseOwnership(context.Context, hostedruntime.OwnerFence) error {
	return nil
}
func (sessions *mainRoomMutationSessions) TargetRoom(context.Context, int64) (string, error) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.target, nil
}
func (sessions *mainRoomMutationSessions) PersistTargetRoom(_ context.Context, command hostedruntime.PersistTargetRoomCommand) error {
	sessions.mu.Lock()
	sessions.target = command.RoomID
	sessions.persisted = append(sessions.persisted, command.RoomID)
	sessions.mu.Unlock()
	return nil
}
func (*mainRoomMutationSessions) StartSession(context.Context, hostedruntime.StartSessionCommand) (hostedruntime.Session, error) {
	return hostedruntime.Session{}, errors.New("unexpected session start")
}
func (*mainRoomMutationSessions) EndSession(context.Context, hostedruntime.EndSessionCommand) error {
	return errors.New("unexpected session end")
}
func (*mainRoomMutationSessions) PendingMigration(context.Context, int64) (int64, bool, error) {
	return 0, false, nil
}
func (sessions *mainRoomMutationSessions) persistedTargets() []string {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return append([]string(nil), sessions.persisted...)
}

type mainRoomMutationSources struct{}

func (*mainRoomMutationSources) Resolve(_ context.Context, roomID string, _ int64) (string, error) {
	if roomID == "7" {
		return "42", nil
	}
	return roomID, nil
}
func (*mainRoomMutationSources) SubscribeCanonical(context.Context, string, int64, roomsource.Sink) (roomsource.Subscription, error) {
	return nil, errors.New("unexpected room subscription")
}
func (*mainRoomMutationSources) Close()                     {}
func (*mainRoomMutationSources) Wait(context.Context) error { return nil }

type mainRoomReferenceRefresher struct {
	sessions *mainRoomMutationSessions
	failures int
	err      error
	calls    int
	observed []string
}

func (refresher *mainRoomReferenceRefresher) RefreshReferences(context.Context) error {
	refresher.calls++
	if refresher.sessions != nil {
		refresher.sessions.mu.Lock()
		refresher.observed = append(refresher.observed, refresher.sessions.target)
		refresher.sessions.mu.Unlock()
	}
	if refresher.failures > 0 {
		refresher.failures--
		return refresher.err
	}
	return nil
}
func (refresher *mainRoomReferenceRefresher) observedTargets() []string {
	return append([]string(nil), refresher.observed...)
}

type roomSetterFunc func(context.Context, int64, string) (hostedruntime.RoomMutationResult, error)

func (setter roomSetterFunc) MutateRoom(ctx context.Context, accountID int64, roomID string) (hostedruntime.RoomMutationResult, error) {
	return setter(ctx, accountID, roomID)
}

type runtimeStartupLog struct {
	mu    sync.Mutex
	items []string
}

func (log *runtimeStartupLog) add(item string) {
	log.mu.Lock()
	log.items = append(log.items, item)
	log.mu.Unlock()
}

func (log *runtimeStartupLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.items...)
}

type runtimeStartupResource struct {
	name string
	log  *runtimeStartupLog
}

func (resource *runtimeStartupResource) Shutdown(context.Context) error {
	resource.log.add(resource.name + "-shutdown")
	return nil
}
func (resource *runtimeStartupResource) Wait(context.Context) error {
	resource.log.add(resource.name + "-wait")
	return nil
}

type watcherStartupResource struct{ log *runtimeStartupLog }

func (resource *watcherStartupResource) Close() { resource.log.add("watcher-close") }
func (resource *watcherStartupResource) Wait(context.Context) error {
	resource.log.add("watcher-wait")
	return nil
}

type mainRuntimeSources struct {
	mu     sync.Mutex
	closed bool
}

func (*mainRuntimeSources) Resolve(_ context.Context, roomID string, _ int64) (string, error) {
	return roomID, nil
}
func (*mainRuntimeSources) SubscribeCanonical(_ context.Context, roomID string, _ int64, _ roomsource.Sink) (roomsource.Subscription, error) {
	return &mainRuntimeSubscription{roomID: roomID, done: make(chan struct{})}, nil
}
func (sources *mainRuntimeSources) Close() {
	sources.mu.Lock()
	sources.closed = true
	sources.mu.Unlock()
}
func (*mainRuntimeSources) Wait(context.Context) error { return nil }

type mainRuntimeSubscription struct {
	roomID string
	done   chan struct{}
	once   sync.Once
}

func (subscription *mainRuntimeSubscription) RoomID() string { return subscription.roomID }
func (subscription *mainRuntimeSubscription) Cancel() {
	subscription.once.Do(func() { close(subscription.done) })
}
func (subscription *mainRuntimeSubscription) Done() <-chan struct{} { return subscription.done }
func (subscription *mainRuntimeSubscription) Wait(ctx context.Context) error {
	select {
	case <-subscription.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type healthyHostedDatabase struct{}

func (healthyHostedDatabase) Health(context.Context) error { return nil }

func statusHandler(status int) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(status)
	})
}

func (initializer *initializerStub) Initialize(_ context.Context, email string) (adminidentity.InitializeResult, error) {
	initializer.calls++
	initializer.email = email
	return initializer.result, initializer.err
}

type lifecycleStub struct {
	serve    func(net.Listener) error
	shutdown func(context.Context) error
	close    func() error
}

func (stub lifecycleStub) Serve(listener net.Listener) error {
	if stub.serve == nil {
		panic("unexpected Serve call")
	}
	return stub.serve(listener)
}

func (stub lifecycleStub) Shutdown(ctx context.Context) error {
	if stub.shutdown == nil {
		panic("unexpected Shutdown call")
	}
	return stub.shutdown(ctx)
}

func (stub lifecycleStub) Close() error {
	if stub.close == nil {
		panic("unexpected Close call")
	}
	return stub.close()
}

type trackedListener struct {
	closed    chan struct{}
	closeOnce sync.Once
}

func newTrackedListener() *trackedListener {
	return &trackedListener{closed: make(chan struct{})}
}

func (*trackedListener) Accept() (net.Conn, error) {
	return nil, errors.New("tracked listener does not accept connections")
}

func (listener *trackedListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (*trackedListener) Addr() net.Addr {
	return stubAddr("127.0.0.1:12500")
}

func (listener *trackedListener) isClosed() bool {
	select {
	case <-listener.closed:
		return true
	default:
		return false
	}
}

type stubAddr string

func (stubAddr) Network() string { return "tcp" }
func (address stubAddr) String() string {
	return string(address)
}
