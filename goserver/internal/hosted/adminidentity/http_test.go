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
)

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
		{path: "/api/admin/recovery/prepare", body: `{"challengeId":"proof","recoveryCode":"code"}`, key: "admin:1"},
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
		if service.verifySession != "" || service.recoverySession != "" || service.prepareChallenge != "" {
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
	for _, method := range []string{http.MethodHead, http.MethodDelete} {
		response := httptest.NewRecorder()
		newTestHTTPHandler(t, &adminHTTPService{}).ServeHTTP(response, httptest.NewRequest(method, "/api/admin/session", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status=%d, want 405", method, response.Code)
		}
	}
}

func TestHTTPAdminProofStatusExposesStateWithoutIdentity(t *testing.T) {
	now := time.Date(2026, 8, 16, 13, 5, 0, 0, time.UTC)
	tests := []struct {
		name       string
		status     AdminProofStatus
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "pending", status: AdminProofStatus{Status: AdminProofPending, ExpiresAt: now.Add(time.Minute)}, wantStatus: http.StatusOK, wantBody: `{"status":"pending","expiresAt":"2026-08-16T13:06:00Z"}` + "\n"},
		{name: "verified", status: AdminProofStatus{Status: AdminProofVerified, ExpiresAt: now.Add(time.Minute)}, wantStatus: http.StatusOK, wantBody: `{"status":"verified","expiresAt":"2026-08-16T13:06:00Z"}` + "\n"},
		{name: "expired", err: identity.ErrChallengeExpired, wantStatus: http.StatusGone, wantBody: `{"status":"expired"}` + "\n"},
		{name: "unavailable", err: ErrUnavailable, wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"temporarily_unavailable"}` + "\n"},
		{name: "invalid", err: ErrAuthenticationFailed, wantStatus: http.StatusUnauthorized, wantBody: `{"error":"authentication_failed"}` + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &adminHTTPService{proofStatus: test.status, proofStatusErr: test.err}
			handler := newTestHTTPHandler(t, service)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/auth/bili/challenges/proof-status", nil))
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody || service.proofStatusChallenge != "proof-status" {
				t.Fatalf("status=%d body=%q challenge=%q", response.Code, response.Body.String(), service.proofStatusChallenge)
			}
			for _, forbidden := range []string{"uid", "32249588", "SESSDATA"} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("response exposed %q: %s", forbidden, response.Body.String())
				}
			}
		})
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
	request := mutationRequest(http.MethodPost, "/api/admin/recovery/prepare", `{"challengeId":"fresh-proof","recoveryCode":"one-time-code"}`)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "old-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.prepareChallenge != "fresh-proof" || service.prepareCode != "one-time-code" {
		t.Fatalf("response=%d %q args=%q %q", response.Code, response.Body.String(), service.prepareChallenge, service.prepareCode)
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

func TestHTTPForbiddenMalformedQueryNeverReachesService(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "begin proof", method: http.MethodPost, path: "/api/admin/auth/bili/challenges?x;y", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "cancel proof", method: http.MethodDelete, path: "/api/admin/auth/bili/challenges/proof?x;y", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "login", method: http.MethodPost, path: "/api/admin/session?x;y", body: `{"challengeId":"proof","totp":"123456"}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
		{name: "recent totp", method: http.MethodPost, path: "/api/admin/totp?x;y", body: `{"totp":"123456"}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
		{name: "recovery archive", method: http.MethodPost, path: "/api/admin/recovery/archive?x;y", body: `{}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
		{name: "recovery prepare", method: http.MethodPost, path: "/api/admin/recovery/prepare?x;y", body: `{"challengeId":"proof","recoveryCode":"code"}`, wantStatus: http.StatusForbidden, wantCode: "request_rejected"},
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

type adminHTTPService struct {
	emailBeginCalls      int
	emailChallenge       EmailLoginChallenge
	emailChallengeErr    error
	emailLogin           LoginResult
	emailLoginErr        error
	emailLoginChallenge  string
	emailLoginCode       string
	beginCalls           int
	challenge            identity.Challenge
	challengeErr         error
	login                LoginResult
	loginErr             error
	loginChallenge       string
	loginCode            string
	verifyErr            error
	verifySession        string
	verifyCode           string
	recovery             RecoveryResult
	recoveryErr          error
	recoverySession      string
	preparation          RecoveryPreparationResult
	preparationErr       error
	prepareChallenge     string
	prepareCode          string
	confirmErr           error
	confirmToken         string
	confirmCode          string
	cancelled            []string
	requireSessionErr    error
	requireSessionToken  string
	requireSessionCalls  int
	proofStatus          AdminProofStatus
	proofStatusErr       error
	proofStatusChallenge string
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

func (service *adminHTTPService) PollVerification(_ context.Context, challengeID string) (AdminProofStatus, error) {
	service.proofStatusChallenge = challengeID
	return service.proofStatus, service.proofStatusErr
}

func (service *adminHTTPService) BeginVerification(context.Context) (identity.Challenge, error) {
	service.beginCalls++
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

func (service *adminHTTPService) PrepareRecovery(_ context.Context, challengeID, code string) (RecoveryPreparationResult, error) {
	service.prepareChallenge, service.prepareCode = challengeID, code
	return service.preparation, service.preparationErr
}

func (service *adminHTTPService) ConfirmRecovery(_ context.Context, token, code string) error {
	service.confirmToken, service.confirmCode = token, code
	return service.confirmErr
}

func (service *adminHTTPService) wasCalled() bool {
	return service.beginCalls != 0 || service.emailBeginCalls != 0 || service.emailLoginChallenge != "" || service.loginChallenge != "" || service.loginCode != "" || service.verifySession != "" ||
		service.verifyCode != "" || service.recoverySession != "" || service.prepareChallenge != "" || service.prepareCode != "" ||
		service.confirmToken != "" || service.confirmCode != "" || len(service.cancelled) != 0
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
