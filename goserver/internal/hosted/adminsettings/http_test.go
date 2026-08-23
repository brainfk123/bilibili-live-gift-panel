package adminsettings

import (
	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/identity"
	"encoding/json"
	"github.com/DATA-DOG/go-sqlmock"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPSettingsRequiresSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _ := NewService(db, testKeys(t), &sessions{}, nil)
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
	mock.ExpectExec("UPDATE site_sessions SET revoked_at").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 2))
	request = httptest.NewRequest(http.MethodPost, "/api/admin/sessions/revoke-others", nil)
	request.Header.Set("Origin", "https://panel.example.test")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authorized revoke-others status=%d body=%q", response.Code, response.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionInventoryRoutesRequireCurrentAdminAndRejectSecrets(t *testing.T) {
	now := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	validator := &sessions{inventory: []adminidentity.AdministratorSession{{
		PublicID: "00112233445566778899aabbccddeeff", DeviceLabel: "iPhone · Safari",
		ClientNetwork: "203.0.113.*", CreatedAt: now.Add(-time.Hour), LastSeenAt: now,
		ExpiresAt: now.Add(30 * 24 * time.Hour), Current: true,
	}}, loginEvents: []adminidentity.AdministratorLoginEvent{{
		Result: "failure", DeviceLabel: "Windows · Edge", ClientNetwork: "198.51.100.*", OccurredAt: now,
	}}}
	handler := newInventoryHTTPHandler(t, validator)

	for _, target := range []string{"/api/admin/sessions", "/api/admin/login-events?limit=20"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "current"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%q", target, response.Code, response.Body.String())
		}
		for _, forbidden := range []string{"token_hash", "raw_user_agent", "email_code", "203.0.113.45", "cookie"} {
			if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
				t.Fatalf("GET %s leaked %s: %s", target, forbidden, response.Body.String())
			}
		}
		var envelope map[string][]map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		items := envelope["sessions"]
		wantKeys := []string{"id", "deviceLabel", "clientNetwork", "createdAt", "lastSeenAt", "expiresAt", "current"}
		if strings.Contains(target, "login-events") {
			items = envelope["events"]
			wantKeys = []string{"result", "deviceLabel", "clientNetwork", "occurredAt"}
		}
		if len(items) != 1 || !hasExactKeys(items[0], wantKeys) {
			t.Fatalf("GET %s payload=%#v, want exact keys %v", target, envelope, wantKeys)
		}
	}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/admin/sessions", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.Code)
	}
}

func TestSessionInventoryDeleteEnforcesMethodCurrentMissingIDAndCSRF(t *testing.T) {
	validID := "00112233445566778899aabbccddeeff"
	tests := []struct {
		name      string
		method    string
		target    string
		origin    string
		csrf      string
		revokeErr error
		want      int
		code      string
	}{
		{name: "method", method: http.MethodPost, target: validID, origin: "https://panel.example.test", csrf: "csrf", want: http.StatusMethodNotAllowed},
		{name: "success", method: http.MethodDelete, target: validID, origin: "https://panel.example.test", csrf: "csrf", want: http.StatusNoContent},
		{name: "current", method: http.MethodDelete, target: validID, origin: "https://panel.example.test", csrf: "csrf", revokeErr: adminidentity.ErrCurrentAdminSession, want: http.StatusConflict, code: "current_session"},
		{name: "missing", method: http.MethodDelete, target: validID, origin: "https://panel.example.test", csrf: "csrf", revokeErr: adminidentity.ErrAdminSessionNotFound, want: http.StatusNotFound, code: "session_not_found"},
		{name: "invalid public id", method: http.MethodDelete, target: "ABC", origin: "https://panel.example.test", csrf: "csrf", want: http.StatusBadRequest, code: "invalid_request"},
		{name: "uppercase public id", method: http.MethodDelete, target: "00112233445566778899AABBCCDDEEFF", origin: "https://panel.example.test", csrf: "csrf", want: http.StatusBadRequest, code: "invalid_request"},
		{name: "wrong origin", method: http.MethodDelete, target: validID, origin: "https://evil.example", csrf: "csrf", want: http.StatusForbidden, code: "request_rejected"},
		{name: "wrong csrf", method: http.MethodDelete, target: validID, origin: "https://panel.example.test", csrf: "wrong", want: http.StatusForbidden, code: "request_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := &sessions{revokeErr: test.revokeErr}
			handler := newInventoryHTTPHandler(t, validator)
			request := httptest.NewRequest(test.method, "/api/admin/sessions/"+test.target, nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-CSRF-Token", test.csrf)
			request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "current"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || (test.code != "" && !strings.Contains(response.Body.String(), `"error":"`+test.code+`"`)) {
				t.Fatalf("response=%d %q", response.Code, response.Body.String())
			}
			if test.want == http.StatusNoContent && validator.revokedID != validID {
				t.Fatalf("revoked id=%q", validator.revokedID)
			}
		})
	}
}

func TestLoginEventLimitMustBeWithinOneAndFifty(t *testing.T) {
	handler := newInventoryHTTPHandler(t, &sessions{})
	for _, target := range []string{"/api/admin/login-events?limit=0", "/api/admin/login-events?limit=51", "/api/admin/login-events?limit=twenty"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "current"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status=%d body=%q", target, response.Code, response.Body.String())
		}
	}
}

func newInventoryHTTPHandler(t *testing.T, validator *sessions) *HTTPHandler {
	t.Helper()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := NewService(db, testKeys(t), validator, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, "https://panel.example.test", "csrf")
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func hasExactKeys(value map[string]any, keys []string) bool {
	if len(value) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}
