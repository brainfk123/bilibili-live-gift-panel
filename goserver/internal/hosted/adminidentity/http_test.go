package adminidentity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/identity"
)

func TestHTTPHandlerExposesNoAdministratorInitializationRoute(t *testing.T) {
	handler := newTestHTTPHandler(t, &adminHTTPService{})
	request := mutationRequest(http.MethodPost, "/api/admin/init", `{}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("POST /api/admin/init status = %d, want 404", response.Code)
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "uid") || strings.Contains(strings.ToLower(response.Body.String()), "email") {
		t.Fatalf("unknown init route reflected administrator fields: %q", response.Body.String())
	}
}

func TestHTTPAdministratorLoginSetsHostOnlySessionCookie(t *testing.T) {
	now := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	service := &adminHTTPService{login: LoginResult{Token: "plain-admin-token", ExpiresAt: now.Add(time.Hour)}}
	handler := newTestHTTPHandlerAt(t, service, now)
	request := mutationRequest(http.MethodPost, "/api/admin/session", `{"challengeId":"proof-id","totp":"123456"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if service.loginChallenge != "proof-id" || service.loginCode != "123456" {
		t.Fatalf("login arguments challenge=%q code=%q", service.loginChallenge, service.loginCode)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != identity.SiteSessionCookie || cookie.Value != "plain-admin-token" || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode || !cookie.Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("administrator cookie = %#v", cookie)
	}
}

func TestHTTPLoginPendingAndFailuresAreGeneric(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "pending", err: identity.ErrVerificationPending, wantStatus: http.StatusAccepted, wantCode: "verification_pending"},
		{name: "wrong uid or totp", err: ErrAuthenticationFailed, wantStatus: http.StatusUnauthorized, wantCode: "authentication_failed"},
		{name: "upstream unavailable", err: identity.ErrVerificationUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "temporarily_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &adminHTTPService{loginErr: test.err}
			handler := newTestHTTPHandler(t, service)
			request := mutationRequest(http.MethodPost, "/api/admin/session", `{"challengeId":"proof-secret","totp":"123456"}`)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			for _, forbidden := range []string{"proof-secret", "123456", "32249588", test.err.Error()} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("response exposed %q: %q", forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestHTTPRecoveryReturnsPasswordOnceWithoutArchiveOrUID(t *testing.T) {
	service := &adminHTTPService{recovery: RecoveryResult{RecoveryPassword: "12345678901234567890"}}
	handler := newTestHTTPHandler(t, service)
	request := mutationRequest(http.MethodPost, "/api/admin/recovery/archive", `{}`)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-session-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.recoverySession != "admin-session-token" {
		t.Fatalf("SendRecovery token = %q", service.recoverySession)
	}
	if got := response.Body.String(); got != "{\"recoveryPassword\":\"12345678901234567890\"}\n" {
		t.Fatalf("response = %q", got)
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "archive") || strings.Contains(response.Body.String(), "32249588") {
		t.Fatalf("response exposed archive or UID: %q", response.Body.String())
	}
}

func TestHTTPCompleteRecoveryUsesFreshChallengeAndClearsOldCookie(t *testing.T) {
	service := &adminHTTPService{completion: RecoveryCompletionResult{TOTPURI: "otpauth://new", RecoveryPassword: "abcdefghijklmnopqrst"}}
	handler := newTestHTTPHandler(t, service)
	request := mutationRequest(http.MethodPost, "/api/admin/recovery/complete", `{"challengeId":"fresh-proof","recoveryCode":"one-time-code"}`)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "old-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.completeChallenge != "fresh-proof" || service.completeCode != "one-time-code" {
		t.Fatalf("response=%d %q args=%q %q", response.Code, response.Body.String(), service.completeChallenge, service.completeCode)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != identity.SiteSessionCookie || cookies[0].Value != "" || cookies[0].MaxAge != -1 {
		t.Fatalf("clearing cookies = %#v", cookies)
	}
}

func TestHTTPMutationsRejectOriginCSRFAndUnexpectedFields(t *testing.T) {
	service := &adminHTTPService{}
	handler := newTestHTTPHandler(t, service)
	tests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/admin/session", strings.NewReader(`{"challengeId":"proof","totp":"123456"}`)),
		mutationRequest(http.MethodPost, "/api/admin/session", `{"challengeId":"proof","totp":"123456","uid":"32249588"}`),
		mutationRequest(http.MethodPost, "/api/admin/session?accountId=1", `{"challengeId":"proof","totp":"123456"}`),
	}
	for index, request := range tests {
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden && response.Code != http.StatusBadRequest {
			t.Fatalf("case %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	if service.loginChallenge != "" {
		t.Fatal("rejected mutation reached service")
	}
}

func TestAppMountsAdministratorHandlerOnlyUnderAdminPrefix(t *testing.T) {
	admin := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	})
	handler := app.New(app.Dependencies{DB: healthyDatabase{}, Admin: admin})

	for _, test := range []struct {
		path string
		want int
	}{{path: "/api/admin/session", want: http.StatusTeapot}, {path: "/api/not-admin/session", want: http.StatusNotFound}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("POST %s status=%d want=%d", test.path, response.Code, test.want)
		}
	}
}

type adminHTTPService struct {
	challenge         identity.Challenge
	challengeErr      error
	login             LoginResult
	loginErr          error
	loginChallenge    string
	loginCode         string
	verifyErr         error
	verifySession     string
	verifyCode        string
	recovery          RecoveryResult
	recoveryErr       error
	recoverySession   string
	completion        RecoveryCompletionResult
	completionErr     error
	completeChallenge string
	completeCode      string
	cancelled         []string
}

func (service *adminHTTPService) BeginVerification(context.Context) (identity.Challenge, error) {
	return service.challenge, service.challengeErr
}

func (service *adminHTTPService) CancelVerification(challengeID string) {
	service.cancelled = append(service.cancelled, challengeID)
}

func (service *adminHTTPService) VerifyLogin(_ context.Context, challengeID, code string) (LoginResult, error) {
	service.loginChallenge, service.loginCode = challengeID, code
	return service.login, service.loginErr
}

func (service *adminHTTPService) VerifyRecentTOTP(_ context.Context, sessionToken, code string) error {
	service.verifySession, service.verifyCode = sessionToken, code
	return service.verifyErr
}

func (service *adminHTTPService) SendRecovery(_ context.Context, sessionToken string) (RecoveryResult, error) {
	service.recoverySession = sessionToken
	return service.recovery, service.recoveryErr
}

func (service *adminHTTPService) CompleteRecovery(_ context.Context, challengeID, code string) (RecoveryCompletionResult, error) {
	service.completeChallenge, service.completeCode = challengeID, code
	return service.completion, service.completionErr
}

type allowAdminLimits struct{}

func (allowAdminLimits) Allow(context.Context, identity.LimitScope, string) bool { return true }

func newTestHTTPHandler(t *testing.T, service adminHTTPServicePort) *HTTPHandler {
	t.Helper()
	return newTestHTTPHandlerAt(t, service, time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC))
}

func newTestHTTPHandlerAt(t *testing.T, service adminHTTPServicePort, now time.Time) *HTTPHandler {
	t.Helper()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example.com", CSRFToken: "csrf-test-token",
		Limiter: allowAdminLimits{}, ClientIP: identity.DirectClientIP, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler() error = %v", err)
	}
	return handler
}

func mutationRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Origin", "https://panel.example.com")
	request.Header.Set("X-CSRF-Token", "csrf-test-token")
	request.Header.Set("Content-Type", "application/json")
	return request
}

type healthyDatabase struct{}

func (healthyDatabase) Health(context.Context) error { return nil }
