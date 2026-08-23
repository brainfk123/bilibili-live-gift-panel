package adminsettings

import (
	"bilibili-live-gift-panel/internal/hosted/identity"
	"github.com/DATA-DOG/go-sqlmock"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPSettingsRequiresSession(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _ := NewService(db, testKeys(t), sessions{}, nil)
	handler, _ := NewHTTPHandler(service, "https://panel.example.test", "csrf")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if response.Code != 401 {
		t.Fatalf("status=%d", response.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/sessions/revoke-others", nil)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatalf("mutation status=%d", response.Code)
	}
}
