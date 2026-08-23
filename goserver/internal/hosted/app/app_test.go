package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeHealth struct {
	err error
}

func (health fakeHealth) Health(context.Context) error {
	return health.err
}

func TestHealthDoesNotExposeConfiguration(t *testing.T) {
	handler := New(Dependencies{DB: fakeHealth{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q, want minimal status", got)
	}
	for _, forbidden := range []string{"MYSQL", "DSN", "KEY", "configuration"} {
		if strings.Contains(strings.ToUpper(response.Body.String()), forbidden) {
			t.Fatalf("health response exposed %q: %q", forbidden, response.Body.String())
		}
	}
}

func TestHealthReturnsServiceUnavailableWhenDatabaseFails(t *testing.T) {
	handler := New(Dependencies{DB: fakeHealth{err: errors.New("database-secret-details")}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got := response.Body.String(); got != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("body = %q, want minimal unavailable status", got)
	}
	if strings.Contains(response.Body.String(), "database-secret-details") {
		t.Fatalf("health response exposed database error: %q", response.Body.String())
	}
}

func TestBootstrapReturnsOnlyRuntimeCSRFAndRejectsQueries(t *testing.T) {
	handler := New(Dependencies{DB: fakeHealth{}, CSRFToken: "runtime-csrf"})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Body.String(); got != "{\"csrfToken\":\"runtime-csrf\"}\n" {
		t.Fatalf("body = %q", got)
	}

	for _, target := range []string{"/api/bootstrap?extra=1", "/api/bootstrap?x;y"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400", target, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q", target, got)
		}
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(`{}`)))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("POST Cache-Control = %q", got)
	}
	if strings.Contains(response.Body.String(), "runtime-csrf") {
		t.Fatalf("POST exposed bootstrap value: %q", response.Body.String())
	}
}

func TestConfigurationMethodRoutesWinOverBroaderPrefixes(t *testing.T) {
	configuration := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	auth := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: auth, Configuration: configuration})

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/configuration"},
		{http.MethodPut, "/api/configuration/definition"},
		{http.MethodPut, "/api/configuration/state"},
		{http.MethodPut, "/api/configuration/room-suggestion"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want configuration handler", route.method, route.path, response.Code)
		}
	}
}

func TestMigrationMethodRoutesWinOverBroaderAuthenticationPrefix(t *testing.T) {
	migration := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	auth := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: auth, Migration: migration})
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/migrations/preview"},
		{http.MethodPost, "/api/migrations/9/apply"},
		{http.MethodDelete, "/api/migrations/9"},
		{http.MethodPost, "/api/migrations/9/rollback"},
		{http.MethodGet, "/api/migrations/9"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want migration handler", route.method, route.path, response.Code)
		}
	}
}

func TestBiliServiceRoutesWinOverBroaderAdministratorPrefix(t *testing.T) {
	biliService := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	administrator := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Admin: administrator, BiliService: biliService})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/bili-service/status"},
		{http.MethodPost, "/api/admin/bili-service/challenge"},
		{http.MethodPost, "/api/admin/bili-service/replace"},
		{http.MethodPost, "/api/admin/bili-service/check"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want Bili service handler", route.method, route.path, response.Code)
		}
	}
}

func TestAdministratorConsoleQueriesWinOverBroadAccountHandler(t *testing.T) {
	console := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	broad := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: broad, Admin: broad, AdminConsole: console})
	for _, path := range []string{"/api/admin/overview", "/api/admin/accounts", "/api/admin/accounts/41"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("GET %s status=%d, want administrator console", path, response.Code)
		}
	}
	for _, route := range []struct{ method, path string }{{http.MethodPost, "/api/admin/accounts/batch"}, {http.MethodPut, "/api/admin/accounts/41/room"}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status=%d, want administrator console", route.method, route.path, response.Code)
		}
	}
}

func TestEveryMethodForExactBiliServicePathsStaysOutOfBroadAdministratorHandler(t *testing.T) {
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
		response.WriteHeader(http.StatusTeapot)
	})
	administrator := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	})
	handler := New(Dependencies{DB: fakeHealth{}, Admin: administrator, BiliService: biliService})

	for path, allowedMethod := range allowed {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodHead, "BREW"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			want := http.StatusMethodNotAllowed
			if method == allowedMethod {
				want = http.StatusTeapot
			}
			if response.Code != want {
				t.Fatalf("%s %s status=%d, want %d from Bili service handler", method, path, response.Code, want)
			}
		}
	}
}

func TestRuntimeRoutesAreExactAndExposeNoStartOrStop(t *testing.T) {
	runtimeHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		allowed := map[string]string{
			"/api/runtime/room":   http.MethodPut,
			"/api/runtime/events": http.MethodGet,
			"/api/runtime/status": http.MethodGet,
		}
		if request.Method != allowed[request.URL.Path] {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.WriteHeader(http.StatusTeapot)
	})
	auth := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: auth, Runtime: runtimeHandler})
	for _, route := range []struct{ method, path string }{
		{http.MethodPut, "/api/runtime/room"},
		{http.MethodGet, "/api/runtime/events"},
		{http.MethodGet, "/api/runtime/status"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusTeapot {
			t.Fatalf("%s %s status = %d, want runtime handler", route.method, route.path, response.Code)
		}
	}
	for _, path := range []string{"/api/runtime/start", "/api/runtime/stop"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want no route", path, response.Code)
		}
	}
}

func TestOBSOwnsEveryMethodOnCredentialExchangeAndEventPaths(t *testing.T) {
	obsHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	broad := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusAccepted) })
	handler := New(Dependencies{DB: fakeHealth{}, Auth: broad, Admin: broad, OBS: obsHandler})
	paths := []string{
		"/api/admin/accounts/41/obs-credential",
		"/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/exchange",
		"/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/events",
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusTeapot {
				t.Fatalf("%s %s status=%d, want OBS handler", method, path, response.Code)
			}
		}
	}
}

func TestStaticHandlerFailsFastWhenManifestAssetsAreMissing(t *testing.T) {
	root := writeStaticFixture(t)
	if err := os.Remove(filepath.Join(root, "assets", "obs.css")); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStaticHandler(root); err == nil {
		t.Fatal("NewStaticHandler() accepted a manifest with a missing asset")
	}
}

func TestStaticRoutesServeOnlyHostedPagesAndManifestAssets(t *testing.T) {
	staticHandler, err := NewStaticHandler(writeStaticFixture(t))
	if err != nil {
		t.Fatalf("NewStaticHandler() error = %v", err)
	}
	obsHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	})
	handler := New(Dependencies{DB: fakeHealth{}, OBS: obsHandler, Static: staticHandler})
	publicID := strings.Repeat("A", 43)
	for _, target := range []string{
		"/obs/" + publicID + "/",
		"/obs/" + publicID + "/?theme=glass",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || response.Body.String() != "obs-page" {
			t.Fatalf("GET %s = (%d, %q), want trailing-slash OBS page", target, response.Code, response.Body.String())
		}
	}
	for _, theme := range []string{"minimal", "glass", "rpg", "pixel", "neon", "kawaii"} {
		response := httptest.NewRecorder()
		target := "/obs/" + publicID + "?theme=" + theme
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || response.Body.String() != "obs-page" {
			t.Fatalf("GET %s = (%d, %q), want themed OBS page", target, response.Code, response.Body.String())
		}
	}

	for _, test := range []struct {
		method, target string
		want           int
		body           string
	}{
		{http.MethodGet, "/", http.StatusOK, "hosted-page"},
		{http.MethodGet, "/hosted.html", http.StatusOK, "hosted-page"},
		{http.MethodHead, "/hosted.html", http.StatusOK, ""},
		{http.MethodGet, "/obs/" + publicID, http.StatusOK, "obs-page"},
		{http.MethodGet, "/assets/hosted.js", http.StatusOK, "hosted-script"},
		{http.MethodGet, "/assets/obs.css", http.StatusOK, "obs-style"},
		{http.MethodGet, "/obs/short", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "/extra", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "/events/", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "/exchange/", http.StatusNotFound, ""},
		{http.MethodGet, "/assets/unlisted.js", http.StatusNotFound, ""},
		{http.MethodGet, "/.vite/manifest.json", http.StatusNotFound, ""},
		{http.MethodGet, "/secret.txt", http.StatusNotFound, ""},
		{http.MethodGet, "/assets/", http.StatusNotFound, ""},
		{http.MethodGet, "/hosted.html?theme=glass", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=unknown", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=glass&theme=neon", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=glass&file=secret.txt", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=%67lass", http.StatusNotFound, ""},
		{http.MethodGet, "/obs/" + publicID + "?theme=glass%2F..%2Fsecret", http.StatusNotFound, ""},
		{http.MethodPost, "/", http.StatusMethodNotAllowed, ""},
		{http.MethodGet, "/obs/" + publicID + "/events", http.StatusTeapot, ""},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.target, nil))
		if response.Code != test.want || (test.body != "" && response.Body.String() != test.body) {
			t.Fatalf("%s %s = (%d, %q), want (%d, %q)", test.method, test.target, response.Code, response.Body.String(), test.want, test.body)
		}
	}
}

func writeStaticFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"hosted.html":         "hosted-page",
		"obs.html":            "obs-page",
		"assets/hosted.js":    "hosted-script",
		"assets/obs.css":      "obs-style",
		"assets/unlisted.js":  "must-not-serve",
		"secret.txt":          "must-not-serve",
		".vite/manifest.json": `{"hosted.html":{"file":"assets/hosted.js"},"obs.html":{"file":"assets/hosted.js","css":["assets/obs.css"]}}`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
