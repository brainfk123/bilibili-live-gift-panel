package adminconsole

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"github.com/DATA-DOG/go-sqlmock"
)

type testSessions struct {
	err   error
	token string
}

func (sessions *testSessions) RequireSession(_ context.Context, token string) error {
	sessions.token = token
	return sessions.err
}

func TestHTTPRequiresAdministratorSession(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _ := NewService(db, "https://panel.example.com")
	sessions := &testSessions{err: errors.New("revoked")}
	handler, _ := NewHTTPHandler(service, sessions)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "revoked-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || sessions.token != "revoked-token" {
		t.Fatalf("status=%d token=%q", response.Code, sessions.token)
	}
}

func TestHTTPRejectsMalformedCursorBeforeRepository(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _ := NewService(db, "https://panel.example.com")
	handler, _ := NewHTTPHandler(service, &testSessions{})
	request := httptest.NewRequest(http.MethodGet, "/api/admin/accounts?cursor=not-base64", nil)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
