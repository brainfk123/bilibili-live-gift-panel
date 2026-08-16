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
