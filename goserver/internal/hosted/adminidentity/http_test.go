package adminidentity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"
)

func TestHTTPOperationAuthorizationReturnsSingleUseTokenForBoundPurposeAndTarget(t *testing.T) {
	service := &adminHTTPService{operationToken: "operation-token"}
	handler := newTestHTTPHandler(t, service)
	request := mutationRequest(http.MethodPost, "/api/admin/operation-authorizations", `{"totp":"123456","purpose":"bili_service_replace","target":"global"}`)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "administrator-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || response.Body.String() != "{\"authorizationToken\":\"operation-token\"}\n" {
		t.Fatalf("authorize response = %d %q", response.Code, response.Body.String())
	}
	if service.operationSession != "administrator-session" || service.operationCode != "123456" || service.operationPurpose != security.OperationBiliServiceReplace || service.operationTarget != "global" {
		t.Fatalf("operation request = %#v", service)
	}
}

func TestHTTPSensitiveEndpointsRejectMissingCookieWithoutPanic(t *testing.T) {
	handler := newTestHTTPHandler(t, &adminHTTPService{})
	for _, path := range []string{"/api/admin/totp", "/api/admin/recovery/archive"} {
		request := mutationRequest(http.MethodPost, path, `{"totp":"123456"}`)
		if path != "/api/admin/totp" {
			request = mutationRequest(http.MethodPost, path, `{}`)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Body.String() != "{\"error\":\"authentication_failed\"}\n" {
			t.Fatalf("POST %s = %d %q", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("POST %s Cache-Control = %q", path, response.Header().Get("Cache-Control"))
		}
	}
}

func TestHTTPSensitiveEndpointRateLimitUsesHashedSessionBeforeService(t *testing.T) {
	for _, test := range []struct{ path, body, key string }{
		{path: "/api/admin/totp", body: `{"totp":"123456"}`, key: "plain-cookie-must-not-be-a-key"},
		{path: "/api/admin/recovery/archive", body: `{}`, key: "plain-cookie-must-not-be-a-key"},
		{path: "/api/admin/recovery/prepare", body: `{"recoveryCode":"code"}`, key: "admin:1"},
	} {
		limiter := &denySessionLimit{deniedKey: test.key}
		service := &adminHTTPService{}
		handler, err := NewHTTPHandler(service, HTTPOptions{AllowedOrigin: "https://panel.example.com", CSRFToken: "csrf-test-token", Limiter: limiter, ClientIP: identity.DirectClientIP})
		if err != nil {
			t.Fatal(err)
		}
		request := mutationRequest(http.MethodPost, test.path, test.body)
		if test.path != "/api/admin/recovery/prepare" {
			request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: test.key})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("POST %s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		if service.verifySession != "" || service.recoverySession != "" || service.prepareCode != "" {
			t.Fatalf("POST %s reached service", test.path)
		}
		if limiter.sawPlaintext {
			t.Fatalf("POST %s limiter received plaintext secret", test.path)
		}
	}
}

type denySessionLimit struct {
	sawPlaintext bool
	deniedKey    string
}

type adminLimitCall struct {
	scope identity.LimitScope
	key   string
}

type denyAdministratorEmailLoginLimit struct{ calls []adminLimitCall }

type countingAdminLimiter struct{ calls int }

func (limiter *countingAdminLimiter) Allow(context.Context, identity.LimitScope, string) bool {
	limiter.calls++
	return true
}

func (limiter *denyAdministratorEmailLoginLimit) Allow(_ context.Context, scope identity.LimitScope, key string) bool {
	limiter.calls = append(limiter.calls, adminLimitCall{scope: scope, key: key})
	return scope != identity.LimitPerChallenge || key != "admin:1"
}

func (limiter *denySessionLimit) Allow(_ context.Context, scope identity.LimitScope, key string) bool {
	if key == limiter.deniedKey && limiter.deniedKey != "admin:1" {
		limiter.sawPlaintext = true
	}
	want := sha256.Sum256([]byte(limiter.deniedKey))
	return scope != identity.LimitPerChallenge || (key != fmt.Sprintf("%x", want[:]) && key != limiter.deniedKey)
}

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

func TestHTTPAdministratorBiliRoutesAreAbsentForEveryMethodWithoutSideEffects(t *testing.T) {
	service := &adminHTTPService{}
	limiter := &countingAdminLimiter{}
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example.com", CSRFToken: "csrf-test-token",
		Limiter: limiter, ClientIP: identity.DirectClientIP,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/admin/auth/bili/challenges",
		"/api/admin/auth/bili/challenges/legacy-proof",
		"/api/admin/auth/bili/challenges/legacy-proof/child",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions, "BREW"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, mutationRequest(method, path, `{}`))
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s %s status=%d, want 404", method, path, response.Code)
			}
		}
	}
	if limiter.calls != 0 || service.wasCalled() {
		t.Fatalf("removed Bilibili routes had side effects: limiter=%d service=%#v", limiter.calls, service)
	}
}

func TestHTTPAdministratorSessionOwnsOnlyGetAndDelete(t *testing.T) {
	service := &adminHTTPService{}
	handler := newTestHTTPHandler(t, service)

	get := httptest.NewRequest(http.MethodGet, "/api/admin/session", nil)
	get.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, get)
	if response.Code != http.StatusNoContent || service.requireSessionCalls != 1 {
		t.Fatalf("GET status=%d require calls=%d", response.Code, service.requireSessionCalls)
	}

	remove := mutationRequest(http.MethodDelete, "/api/admin/session", ``)
	remove.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-session"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, remove)
	if response.Code != http.StatusNoContent || service.logoutCalls != 1 || service.logoutToken != "admin-session" {
		t.Fatalf("DELETE status=%d logout calls=%d token=%q", response.Code, service.logoutCalls, service.logoutToken)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != "" || cookies[0].MaxAge != -1 {
		t.Fatalf("DELETE cookies=%#v", cookies)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/api/admin/session", `{"challengeId":"legacy-proof","totp":"123456"}`))
	if response.Code != http.StatusMethodNotAllowed || service.requireSessionCalls != 1 || service.logoutCalls != 1 {
		t.Fatalf("POST status=%d service=%#v", response.Code, service)
	}
}

func TestHTTPAdminSessionProbeIsEmptyAndDistinguishesUnavailable(t *testing.T) {
	tests := []struct {
		name       string
		cookie     string
		body       string
		serviceErr error
		wantStatus int
		wantCode   string
		wantCalls  int
	}{
		{name: "valid", cookie: "admin-session", wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "missing cookie", wantStatus: http.StatusUnauthorized, wantCode: "authentication_failed"},
		{name: "expired", cookie: "expired-session", serviceErr: ErrAuthenticationFailed, wantStatus: http.StatusUnauthorized, wantCode: "authentication_failed", wantCalls: 1},
		{name: "repository unavailable", cookie: "admin-session", serviceErr: ErrUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "temporarily_unavailable", wantCalls: 1},
		{name: "query rejected", cookie: "admin-session", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "body rejected", cookie: "admin-session", body: `{}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &adminHTTPService{requireSessionErr: test.serviceErr}
			handler := newTestHTTPHandler(t, service)
			path := "/api/admin/session"
			if test.name == "query rejected" {
				path += "?unexpected=1"
			}
			request := httptest.NewRequest(http.MethodGet, path, strings.NewReader(test.body))
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: test.cookie})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || service.requireSessionCalls != test.wantCalls {
				t.Fatalf("status=%d calls=%d body=%q", response.Code, service.requireSessionCalls, response.Body.String())
			}
			if test.wantCode == "" {
				if response.Body.Len() != 0 {
					t.Fatalf("body=%q, want empty", response.Body.String())
				}
			} else if response.Body.String() != `{"error":"`+test.wantCode+`"}`+"\n" {
				t.Fatalf("body=%q", response.Body.String())
			}
		})
	}
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch} {
		response := httptest.NewRecorder()
		newTestHTTPHandler(t, &adminHTTPService{}).ServeHTTP(response, httptest.NewRequest(method, "/api/admin/session", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status=%d, want 405", method, response.Code)
		}
	}
}

func TestHTTPEmailLoginReturnsOnlyChallengeThenSetsSevenDayCookie(t *testing.T) {
	now := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	service := &adminHTTPService{emailChallenge: EmailLoginChallenge{ChallengeID: "email-proof", ExpiresAt: now.Add(5 * time.Minute)}, emailLogin: LoginResult{Token: "email-session", ExpiresAt: now.Add(7 * 24 * time.Hour)}}
	handler := newTestHTTPHandlerAt(t, service, now)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/api/admin/auth/email/challenges", `{}`))
	if response.Code != http.StatusCreated || response.Body.String() != "{\"challengeId\":\"email-proof\",\"expiresAt\":\"2026-08-16T13:05:00Z\"}\n" || service.emailBeginCalls != 1 {
		t.Fatalf("begin response=%d %q calls=%d", response.Code, response.Body.String(), service.emailBeginCalls)
	}
	if strings.Contains(response.Body.String(), "owner@") || strings.Contains(response.Body.String(), "123456") {
		t.Fatalf("begin response exposed email or code: %q", response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/api/admin/session/email", `{"challengeId":"email-proof","emailCode":"654321"}`))
	if response.Code != http.StatusNoContent || service.emailLoginChallenge != "email-proof" || service.emailLoginCode != "654321" {
		t.Fatalf("login response=%d %q args=%q %q", response.Code, response.Body.String(), service.emailLoginChallenge, service.emailLoginCode)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != "email-session" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || !cookies[0].Expires.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func TestHTTPEmailLoginRejectsTOTPAndNeverReturnsEmailOrCode(t *testing.T) {
	now := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	service := &adminHTTPService{emailLogin: LoginResult{Token: "email-session", ExpiresAt: now.Add(7 * 24 * time.Hour)}}
	handler := newTestHTTPHandlerAt(t, service, now)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/api/admin/session/email", `{"challengeId":"email-proof","emailCode":"654321","totp":"123456"}`))
	if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_request\"}\n" || service.emailLoginChallenge != "" {
		t.Fatalf("response=%d %q service=%#v", response.Code, response.Body.String(), service)
	}
	for _, secret := range []string{"owner@example.com", "654321", "123456", "email-proof"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response exposed %q: %q", secret, response.Body.String())
		}
	}
}

func TestHTTPEmailLoginMapsUnavailableSessionCreationToServiceUnavailable(t *testing.T) {
	service := &adminHTTPService{emailLoginErr: ErrUnavailable}
	handler := newTestHTTPHandler(t, service)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/api/admin/session/email", `{"challengeId":"email-proof","emailCode":"654321"}`))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"error\":\"temporarily_unavailable\"}\n" {
		t.Fatalf("response=%d %q", response.Code, response.Body.String())
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatalf("cookies=%#v", response.Result().Cookies())
	}
}

func TestHTTPEmailLoginAdministratorLimitRunsAfterGlobalIPAndChallengeBeforeService(t *testing.T) {
	limiter := &denyAdministratorEmailLoginLimit{}
	service := &adminHTTPService{}
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example.com", CSRFToken: "csrf-test-token",
		Limiter: limiter, ClientIP: identity.DirectClientIP,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mutationRequest(http.MethodPost, "/api/admin/session/email", `{"challengeId":"email-proof","emailCode":"654321"}`))
	if response.Code != http.StatusTooManyRequests || response.Body.String() != "{\"error\":\"rate_limited\"}\n" {
		t.Fatalf("response=%d %q", response.Code, response.Body.String())
	}
	if service.emailLoginChallenge != "" {
		t.Fatalf("rate-limited request reached service: %#v", service)
	}
	want := []adminLimitCall{
		{scope: identity.LimitGlobal, key: "admin_email_login"},
		{scope: identity.LimitPerIP, key: "192.0.2.1"},
		{scope: identity.LimitPerChallenge, key: "email-proof"},
		{scope: identity.LimitPerChallenge, key: "admin:1"},
	}
	if len(limiter.calls) != len(want) {
		t.Fatalf("limiter calls=%#v, want %#v", limiter.calls, want)
	}
	for index := range want {
		if limiter.calls[index] != want[index] {
			t.Fatalf("limiter call %d=%#v, want %#v", index, limiter.calls[index], want[index])
		}
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

func TestHTTPRecoveryPrepareThenConfirmClearsOldCookie(t *testing.T) {
	service := &adminHTTPService{preparation: RecoveryPreparationResult{TOTPURI: "otpauth://new", RecoveryPassword: "abcdefghijklmnopqrst", HandoffToken: "opaque-handoff"}}
	handler := newTestHTTPHandler(t, service)
	request := mutationRequest(http.MethodPost, "/api/admin/recovery/prepare", `{"recoveryCode":"one-time-code"}`)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "old-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.prepareCode != "one-time-code" {
		t.Fatalf("response=%d %q code=%q", response.Code, response.Body.String(), service.prepareCode)
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("prepare cleared an old session before confirmation")
	}
	request = mutationRequest(http.MethodPost, "/api/admin/recovery/confirm", `{"handoffToken":"opaque-handoff","totp":"123456"}`)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "old-session"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || service.confirmToken != "opaque-handoff" || service.confirmCode != "123456" {
		t.Fatalf("confirm=%d %q args=%q %q", response.Code, response.Body.String(), service.confirmToken, service.confirmCode)
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
		httptest.NewRequest(http.MethodPost, "/api/admin/recovery/prepare", strings.NewReader(`{"recoveryCode":"code"}`)),
		mutationRequest(http.MethodPost, "/api/admin/recovery/prepare", `{"recoveryCode":"code","challengeId":"legacy-proof"}`),
		mutationRequest(http.MethodPost, "/api/admin/recovery/prepare?accountId=1", `{"recoveryCode":"code"}`),
	}
	for index, request := range tests {
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden && response.Code != http.StatusBadRequest {
			t.Fatalf("case %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	if service.prepareCode != "" {
		t.Fatal("rejected mutation reached service")
	}
}

func TestHTTPForbiddenMalformedQueryNeverReachesService(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "recent totp", method: http.MethodPost, path: "/api/admin/totp?x;y", body: `{"totp":"123456"}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
		{name: "recovery archive", method: http.MethodPost, path: "/api/admin/recovery/archive?x;y", body: `{}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
		{name: "recovery prepare", method: http.MethodPost, path: "/api/admin/recovery/prepare?x;y", body: `{"recoveryCode":"code"}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
		{name: "recovery confirm", method: http.MethodPost, path: "/api/admin/recovery/confirm?x;y", body: `{"handoffToken":"handoff","totp":"123456"}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &adminHTTPService{}
			handler := newTestHTTPHandler(t, service)
			request := mutationRequest(test.method, test.path, test.body)
			request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-session"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus || response.Body.String() != fmt.Sprintf("{\"error\":%q}\n", test.wantCode) {
				t.Fatalf("response=%d %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
			if service.wasCalled() {
				t.Fatalf("malformed query reached service: %#v", service)
			}
		})
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

func TestAppCompositionKeepsRemovedAdminBiliPathsOutOfOtherHandlers(t *testing.T) {
	service := &adminHTTPService{}
	limiter := &countingAdminLimiter{}
	administrator, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example.com", CSRFToken: "csrf-test-token",
		Limiter: limiter, ClientIP: identity.DirectClientIP,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherCalls := 0
	other := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		otherCalls++
		response.WriteHeader(http.StatusTeapot)
	})
	handler := app.New(app.Dependencies{DB: healthyDatabase{}, Auth: other, Admin: administrator, BiliService: other})

	for _, path := range []string{"/api/admin/auth/bili/challenges", "/api/admin/auth/bili/challenges/legacy-proof"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions, "BREW"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, mutationRequest(method, path, `{}`))
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s %s status=%d, want 404", method, path, response.Code)
			}
		}
	}
	if otherCalls != 0 || limiter.calls != 0 || service.wasCalled() {
		t.Fatalf("removed route escaped ownership boundary: other=%d limiter=%d service=%#v", otherCalls, limiter.calls, service)
	}
}

type adminHTTPService struct {
	emailBeginCalls     int
	emailChallenge      EmailLoginChallenge
	emailChallengeErr   error
	emailLogin          LoginResult
	emailLoginErr       error
	emailLoginChallenge string
	emailLoginCode      string
	verifyErr           error
	verifySession       string
	verifyCode          string
	recovery            RecoveryResult
	recoveryErr         error
	recoverySession     string
	preparation         RecoveryPreparationResult
	preparationErr      error
	prepareCode         string
	confirmErr          error
	confirmToken        string
	confirmCode         string
	requireSessionErr   error
	requireSessionToken string
	requireSessionCalls int
	logoutCalls         int
	logoutToken         string
	operationToken      string
	operationErr        error
	operationSession    string
	operationCode       string
	operationPurpose    security.OperationPurpose
	operationTarget     string
}

func (service *adminHTTPService) BeginEmailLogin(context.Context) (EmailLoginChallenge, error) {
	service.emailBeginCalls++
	return service.emailChallenge, service.emailChallengeErr
}

func (service *adminHTTPService) VerifyEmailLogin(_ context.Context, challengeID, emailCode string) (LoginResult, error) {
	service.emailLoginChallenge, service.emailLoginCode = challengeID, emailCode
	return service.emailLogin, service.emailLoginErr
}

func (service *adminHTTPService) RequireSession(_ context.Context, token string) error {
	service.requireSessionCalls++
	service.requireSessionToken = token
	return service.requireSessionErr
}

func (service *adminHTTPService) Logout(_ context.Context, token string) error {
	service.logoutCalls++
	service.logoutToken = token
	return nil
}

func (service *adminHTTPService) VerifyRecentTOTP(_ context.Context, sessionToken, code string) error {
	service.verifySession, service.verifyCode = sessionToken, code
	return service.verifyErr
}

func (service *adminHTTPService) AuthorizeOperation(_ context.Context, sessionToken, code string, purpose security.OperationPurpose, target string) (string, error) {
	service.operationSession, service.operationCode, service.operationPurpose, service.operationTarget = sessionToken, code, purpose, target
	return service.operationToken, service.operationErr
}

func (service *adminHTTPService) SendRecovery(_ context.Context, sessionToken string) (RecoveryResult, error) {
	service.recoverySession = sessionToken
	return service.recovery, service.recoveryErr
}

func (service *adminHTTPService) PrepareRecovery(_ context.Context, code string) (RecoveryPreparationResult, error) {
	service.prepareCode = code
	return service.preparation, service.preparationErr
}

func (service *adminHTTPService) ConfirmHandoff(_ context.Context, token, code string) error {
	service.confirmToken, service.confirmCode = token, code
	return service.confirmErr
}

func (service *adminHTTPService) wasCalled() bool {
	return service.emailBeginCalls != 0 || service.emailLoginChallenge != "" || service.verifySession != "" || service.verifyCode != "" ||
		service.recoverySession != "" || service.prepareCode != "" || service.confirmToken != "" || service.confirmCode != "" ||
		service.requireSessionCalls != 0 || service.logoutCalls != 0
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
