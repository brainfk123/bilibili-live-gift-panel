package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
