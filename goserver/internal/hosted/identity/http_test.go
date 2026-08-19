package identity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostedapp "bilibili-live-gift-panel/internal/hosted/app"
)

const (
	testOrigin = "https://panel.example.test"
	testCSRF   = "csrf-bootstrap-value"
)

func TestHTTPExposesAuthRoutesWithoutUIDOrCookieLeakage(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	service := &fakeHTTPService{
		challenge: Challenge{
			ID: "challenge-http", QRImage: "data:image/png;base64,public-qr",
			VerificationURL: "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=public-key", ExpiresAt: now.Add(5 * time.Minute),
		},
		pollResult:  PollResult{Status: RegistrationRequired, RegistrationIntent: "registration-intent", ExpiresAt: now.Add(5 * time.Minute)},
		loginResult: SiteSession{Token: "site-token", AccountID: 72, ExpiresAt: now.Add(time.Hour)},
		session:     Session{AccountID: 72, ExpiresAt: now.Add(time.Hour)},
	}
	handler := newTestHTTPHandler(t, service, allowLimiter{})

	challengeResponse := serveAuthRequest(handler, http.MethodPost, "/api/auth/bili/challenges", "", "203.0.113.9:4000", true)
	if challengeResponse.Code != http.StatusCreated {
		t.Fatalf("challenge status = %d body=%s", challengeResponse.Code, challengeResponse.Body.String())
	}
	if challengeResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("challenge Cache-Control = %q, want no-store", challengeResponse.Header().Get("Cache-Control"))
	}
	assertBodyOmitsSecrets(t, challengeResponse.Body.String(), "32249588", "SESSDATA", "qr-key")
	var challengeBody struct {
		VerificationURL string `json:"verificationUrl"`
	}
	if err := json.Unmarshal(challengeResponse.Body.Bytes(), &challengeBody); err != nil || challengeBody.VerificationURL != service.challenge.VerificationURL {
		t.Fatalf("challenge body = %s, verification URL = %q, error = %v", challengeResponse.Body.String(), challengeBody.VerificationURL, err)
	}

	pollResponse := serveAuthRequest(handler, http.MethodGet, "/api/auth/bili/challenges/challenge-http", "", "203.0.113.9:4000", false)
	if pollResponse.Code != http.StatusOK || !strings.Contains(pollResponse.Body.String(), "registration-intent") {
		t.Fatalf("poll status = %d body=%s", pollResponse.Code, pollResponse.Body.String())
	}
	if pollResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("registration Cache-Control = %q, want no-store", pollResponse.Header().Get("Cache-Control"))
	}
	assertBodyOmitsSecrets(t, pollResponse.Body.String(), "32249588", "SESSDATA")

	sessionResponse := serveAuthRequest(handler, http.MethodPost, "/api/auth/session", `{"challengeId":"challenge-http"}`, "203.0.113.9:4000", true)
	if sessionResponse.Code != http.StatusNoContent {
		t.Fatalf("create session status = %d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	getRequest.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "site-token"})
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"authenticated":true`) {
		t.Fatalf("get session status = %d body=%s", getResponse.Code, getResponse.Body.String())
	}
	assertBodyOmitsSecrets(t, getResponse.Body.String(), "site-token", "32249588")

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/auth/session", nil)
	deleteRequest.Header.Set("Origin", testOrigin)
	deleteRequest.Header.Set("X-CSRF-Token", testCSRF)
	deleteRequest.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "site-token"})
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent || service.logoutToken != "site-token" {
		t.Fatalf("delete session status=%d token=%q body=%s", deleteResponse.Code, service.logoutToken, deleteResponse.Body.String())
	}
}

func TestHTTPChallengeCancellationIsCSRFProtectedIdempotentAndNonEnumerating(t *testing.T) {
	service := &fakeHTTPService{}
	handler := newTestHTTPHandler(t, service, allowLimiter{})

	rejected := httptest.NewRequest(http.MethodDelete, "/api/auth/bili/challenges/unknown-challenge", nil)
	rejectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(rejectedResponse, rejected)
	if rejectedResponse.Code != http.StatusForbidden || len(service.cancelCalls) != 0 {
		t.Fatalf("unprotected cancel status=%d calls=%v", rejectedResponse.Code, service.cancelCalls)
	}

	for attempt := 0; attempt < 2; attempt++ {
		response := serveAuthRequest(handler, http.MethodDelete, "/api/auth/bili/challenges/unknown-challenge", "", "203.0.113.9:4000", true)
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
			t.Fatalf("cancel attempt %d status=%d body=%q", attempt+1, response.Code, response.Body.String())
		}
	}
	if len(service.cancelCalls) != 2 || service.cancelCalls[0] != "unknown-challenge" || service.cancelCalls[1] != "unknown-challenge" {
		t.Fatalf("Cancel calls=%v", service.cancelCalls)
	}
}

func TestHTTPChallengeLimiterChecksGlobalAndPerIP(t *testing.T) {
	tests := []struct {
		name      string
		denyScope LimitScope
		wantKeys  []limitCall
	}{
		{name: "global", denyScope: LimitGlobal, wantKeys: []limitCall{{scope: LimitGlobal, key: "auth_challenge"}}},
		{name: "per ip", denyScope: LimitPerIP, wantKeys: []limitCall{{scope: LimitGlobal, key: "auth_challenge"}, {scope: LimitPerIP, key: "198.51.100.7"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limiter := &recordingLimiter{denyScope: test.denyScope}
			handler := newTestHTTPHandler(t, &fakeHTTPService{}, limiter)
			response := serveAuthRequest(handler, http.MethodPost, "/api/auth/bili/challenges", "", "198.51.100.7:8123", true)
			if response.Code != http.StatusTooManyRequests || response.Body.String() != "{\"error\":\"rate_limited\"}\n" {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if !equalLimitCalls(limiter.calls, test.wantKeys) {
				t.Fatalf("limiter calls = %#v, want %#v", limiter.calls, test.wantKeys)
			}
		})
	}
}

func TestHTTPChallengePollLimiterChecksGlobalPerIPAndChallenge(t *testing.T) {
	tests := []struct {
		name      string
		denyScope LimitScope
		wantCalls []limitCall
	}{
		{name: "global", denyScope: LimitGlobal, wantCalls: []limitCall{{scope: LimitGlobal, key: "auth_challenge_poll"}}},
		{name: "per ip", denyScope: LimitPerIP, wantCalls: []limitCall{{scope: LimitGlobal, key: "auth_challenge_poll"}, {scope: LimitPerIP, key: "198.51.100.8"}}},
		{name: "per challenge", denyScope: LimitPerChallenge, wantCalls: []limitCall{{scope: LimitGlobal, key: "auth_challenge_poll"}, {scope: LimitPerIP, key: "198.51.100.8"}, {scope: LimitPerChallenge, key: "challenge-rate-limit"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{}
			limiter := &recordingLimiter{denyScope: test.denyScope}
			handler := newTestHTTPHandler(t, service, limiter)
			response := serveAuthRequest(handler, http.MethodGet, "/api/auth/bili/challenges/challenge-rate-limit", "", "198.51.100.8:8123", false)
			if response.Code != http.StatusTooManyRequests || response.Body.String() != "{\"error\":\"rate_limited\"}\n" {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if !equalLimitCalls(limiter.calls, test.wantCalls) {
				t.Fatalf("limiter calls = %#v, want %#v", limiter.calls, test.wantCalls)
			}
			if service.pollCalls != 0 {
				t.Fatalf("rate-limited request reached Bilibili poll service %d times", service.pollCalls)
			}
		})
	}
}

func TestHTTPClientIPResolverRejectsSpoofingAndWalksOnlyTrustedProxyChain(t *testing.T) {
	trustedResolver, err := NewTrustedProxyClientIPResolver([]string{"127.0.0.0/8", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		wantIP     string
	}{
		{name: "untrusted direct peer cannot spoof", remoteAddr: "203.0.113.7:4444", forwarded: "198.51.100.200", wantIP: "203.0.113.7"},
		{name: "trusted proxies expose nearest untrusted client", remoteAddr: "127.0.0.1:4444", forwarded: "192.0.2.66, 198.51.100.9, 10.0.0.5", wantIP: "198.51.100.9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limiter := &recordingLimiter{denyScope: LimitPerIP}
			handler, err := NewHTTPHandler(&fakeHTTPService{}, HTTPOptions{
				AllowedOrigin: testOrigin, CSRFToken: testCSRF, Limiter: limiter, ClientIP: trustedResolver,
			})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/auth/bili/challenges", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("Origin", testOrigin)
			request.Header.Set("X-CSRF-Token", testCSRF)
			request.Header.Set("X-Forwarded-For", test.forwarded)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			want := []limitCall{{scope: LimitGlobal, key: "auth_challenge"}, {scope: LimitPerIP, key: test.wantIP}}
			if !equalLimitCalls(limiter.calls, want) {
				t.Fatalf("limiter calls=%#v want=%#v", limiter.calls, want)
			}
		})
	}
}

func TestHTTPSessionCreationRequiresOriginAndCSRF(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		csrf   string
	}{
		{name: "missing origin", csrf: testCSRF},
		{name: "cross site origin", origin: "https://attacker.invalid", csrf: testCSRF},
		{name: "missing csrf", origin: testOrigin},
		{name: "wrong csrf", origin: testOrigin, csrf: "wrong"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{}
			handler := newTestHTTPHandler(t, service, allowLimiter{})
			request := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(`{"challengeId":"challenge"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-CSRF-Token", test.csrf)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":\"request_rejected\"}\n" {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if service.loginChallenge != "" {
				t.Fatalf("Login called with %q", service.loginChallenge)
			}
		})
	}
}

func TestHTTPSessionCreationRejectsAccountIDInjection(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "json", path: "/api/auth/session", body: `{"challengeId":"challenge","accountId":999}`},
		{name: "query", path: "/api/auth/session?accountId=999", body: `{"challengeId":"challenge"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{}
			handler := newTestHTTPHandler(t, service, allowLimiter{})
			response := serveAuthRequest(handler, http.MethodPost, test.path, test.body, "203.0.113.5:1", true)
			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_request\"}\n" {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if service.loginChallenge != "" {
				t.Fatalf("injected account ID reached Login: challenge=%q", service.loginChallenge)
			}
		})
	}
}

func TestHTTPForbiddenMalformedQueryNeverReachesService(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		cookie string
	}{
		{name: "begin challenge", method: http.MethodPost, path: "/api/auth/bili/challenges?x;y"},
		{name: "poll challenge", method: http.MethodGet, path: "/api/auth/bili/challenges/proof?x;y"},
		{name: "cancel challenge", method: http.MethodDelete, path: "/api/auth/bili/challenges/proof?x;y"},
		{name: "create session", method: http.MethodPost, path: "/api/auth/session?x;y", body: `{"challengeId":"proof"}`},
		{name: "delete session", method: http.MethodDelete, path: "/api/auth/session?x;y", cookie: "site-session"},
		{name: "disable account", method: http.MethodPost, path: "/api/admin/accounts/41/disable?x;y", body: `{"reason":"security"}`, cookie: "admin-session"},
		{name: "enable account", method: http.MethodPost, path: "/api/admin/accounts/41/enable?x;y", body: `{"reason":"appeal"}`, cookie: "admin-session"},
		{name: "rebind account", method: http.MethodPost, path: "/api/admin/accounts/41/rebind?x;y", body: `{"challengeId":"proof","reason":"support"}`, cookie: "admin-session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{}
			handler := newTestHTTPHandler(t, service, allowLimiter{})
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Origin", testOrigin)
			request.Header.Set("X-CSRF-Token", testCSRF)
			request.Header.Set("Content-Type", "application/json")
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: test.cookie})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_request\"}\n" {
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

func TestHTTPSessionCookieHasHostPrefixAndCompleteSecurityAttributes(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	service := &fakeHTTPService{loginResult: SiteSession{Token: "site-token-secret", AccountID: 88, ExpiresAt: now.Add(24 * time.Hour)}}
	handler := newTestHTTPHandler(t, service, allowLimiter{})
	response := serveAuthRequest(handler, http.MethodPost, "/api/auth/session", `{"challengeId":"challenge"}`, "203.0.113.5:1", true)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v, want exactly one", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != SiteSessionCookie || cookie.Value != "site-token-secret" || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode || !cookie.Expires.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("session cookie = %#v", cookie)
	}
}

func TestHTTPAuthenticationMiddlewareInjectsRepositoryAccountOnly(t *testing.T) {
	service := &fakeHTTPService{session: Session{AccountID: 314, ExpiresAt: time.Now().Add(time.Hour)}}
	handler := newTestHTTPHandler(t, service, allowLimiter{})
	var gotAccountID int64
	next := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var ok bool
		gotAccountID, ok = AccountIDFromContext(request.Context())
		if !ok {
			http.Error(response, "missing account", http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/protected?accountId=999", nil)
	request.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "hashed-at-service"})
	response := httptest.NewRecorder()
	handler.Authenticate(next).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || gotAccountID != 314 {
		t.Fatalf("status=%d accountID=%d", response.Code, gotAccountID)
	}
	if service.requiredToken != "hashed-at-service" {
		t.Fatalf("RequireSession token = %q", service.requiredToken)
	}
}

func TestHTTPMapsAuthenticationFailuresToGenericErrors(t *testing.T) {
	service := &fakeHTTPService{pollErr: errors.New("UID 32249588 Cookie SESSDATA=secret"), loginErr: errors.New("account 99 database private")}
	handler := newTestHTTPHandler(t, service, allowLimiter{})

	pollResponse := serveAuthRequest(handler, http.MethodGet, "/api/auth/bili/challenges/challenge", "", "203.0.113.2:1", false)
	if pollResponse.Code != http.StatusUnauthorized || pollResponse.Body.String() != "{\"error\":\"authentication_failed\"}\n" {
		t.Fatalf("poll status=%d body=%q", pollResponse.Code, pollResponse.Body.String())
	}
	assertBodyOmitsSecrets(t, pollResponse.Body.String(), "32249588", "SESSDATA", "secret")

	loginResponse := serveAuthRequest(handler, http.MethodPost, "/api/auth/session", `{"challengeId":"challenge"}`, "203.0.113.2:1", true)
	if loginResponse.Code != http.StatusUnauthorized || loginResponse.Body.String() != "{\"error\":\"authentication_failed\"}\n" {
		t.Fatalf("login status=%d body=%q", loginResponse.Code, loginResponse.Body.String())
	}
	assertBodyOmitsSecrets(t, loginResponse.Body.String(), "account 99", "database private")
}

func TestHTTPAuthRoutesMountThroughHostedAppRouter(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 30, 0, 0, time.UTC)
	auth := newTestHTTPHandler(t, &fakeHTTPService{
		challenge: Challenge{ID: "mounted-challenge", QRImage: "qr", ExpiresAt: now.Add(time.Minute)},
	}, allowLimiter{})
	application := hostedapp.New(hostedapp.Dependencies{DB: healthyAppDatabase{}, Auth: auth})
	response := serveAuthRequest(application, http.MethodPost, "/api/auth/bili/challenges", "", "203.0.113.8:1", true)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "mounted-challenge") {
		t.Fatalf("mounted auth route status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestHTTPAdministratorAccountRoutesExposeOnlyInternalStatus(t *testing.T) {
	service := &fakeHTTPService{}
	handler := newTestHTTPHandler(t, service, allowLimiter{})
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus string
		challenge  string
		reason     string
	}{
		{name: "disable", path: "/api/admin/accounts/71/disable", body: `{"reason":" policy violation "}`, wantStatus: AccountStatusDisabled, reason: "policy violation"},
		{name: "enable", path: "/api/admin/accounts/71/enable", body: `{"reason":" appeal accepted "}`, wantStatus: AccountStatusActive, reason: "appeal accepted"},
		{name: "rebind", path: "/api/admin/accounts/71/rebind", body: `{"challengeId":"fresh-proof","reason":" ownership exception "}`, wantStatus: AccountStatusActive, challenge: "fresh-proof", reason: "ownership exception"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service.adminResult = ManagedAccount{AccountID: 71, Status: test.wantStatus}
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.RemoteAddr = "203.0.113.21:9000"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", testOrigin)
			request.Header.Set("X-CSRF-Token", testCSRF)
			request.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "administrator-cookie"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
			if response.Body.String() != `{"accountId":71,"status":"`+test.wantStatus+`"}`+"\n" {
				t.Fatalf("response = %q", response.Body.String())
			}
			assertBodyOmitsSecrets(t, response.Body.String(), "987654321", "administrator-cookie", "fresh-proof")
			if service.adminSession != "administrator-cookie" || service.adminAccountID != 71 || service.adminChallenge != test.challenge {
				t.Fatalf("service args session=%q account=%d challenge=%q", service.adminSession, service.adminAccountID, service.adminChallenge)
			}
			if service.adminReason != test.reason {
				t.Fatalf("service reason = %q, want %q", service.adminReason, test.reason)
			}
		})
	}
}

func TestHTTPAdministratorAccountRoutesApplyGlobalIPAndHashedSessionLimits(t *testing.T) {
	routes := []struct {
		operation string
		path      string
		body      string
	}{
		{operation: "admin_account_disable", path: "/api/admin/accounts/71/disable", body: `{"reason":"security"}`},
		{operation: "admin_account_enable", path: "/api/admin/accounts/71/enable", body: `{"reason":"appeal"}`},
		{operation: "admin_account_rebind", path: "/api/admin/accounts/71/rebind", body: `{"challengeId":"proof","reason":"ownership"}`},
	}
	for _, route := range routes {
		for _, deniedScope := range []LimitScope{LimitGlobal, LimitPerIP, LimitPerChallenge} {
			t.Run(route.operation+"/"+string(deniedScope), func(t *testing.T) {
				service := &fakeHTTPService{adminResult: ManagedAccount{AccountID: 71, Status: AccountStatusActive}}
				limiter := &recordingLimiter{denyScope: deniedScope}
				handler := newTestHTTPHandler(t, service, limiter)
				request := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body))
				request.RemoteAddr = "203.0.113.22:9000"
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Origin", testOrigin)
				request.Header.Set("X-CSRF-Token", testCSRF)
				request.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "administrator-cookie-secret"})
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)

				if response.Code != http.StatusTooManyRequests || response.Body.String() != `{"error":"rate_limited"}`+"\n" {
					t.Fatalf("status=%d body=%q calls=%#v", response.Code, response.Body.String(), limiter.calls)
				}
				if service.adminSession != "" {
					t.Fatal("rate-limited request reached account service")
				}
				for _, call := range limiter.calls {
					if strings.Contains(call.key, "administrator-cookie-secret") {
						t.Fatalf("limiter key exposed cookie: %#v", limiter.calls)
					}
				}
				digest := sha256.Sum256([]byte("administrator-cookie-secret"))
				want := []limitCall{{scope: LimitGlobal, key: route.operation}}
				if deniedScope != LimitGlobal {
					want = append(want, limitCall{scope: LimitPerIP, key: route.operation + "\x00" + "203.0.113.22"})
				}
				if deniedScope == LimitPerChallenge {
					want = append(want, limitCall{scope: LimitPerChallenge, key: route.operation + "\x00" + fmt.Sprintf("%x", digest[:])})
				}
				if !equalLimitCalls(limiter.calls, want) {
					t.Fatalf("limiter calls=%#v want=%#v", limiter.calls, want)
				}
			})
		}
	}
}

func TestHTTPAdministratorAccountRoutesRejectUntrustedOrInjectedInputBeforeService(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		body        string
		origin      string
		csrf        string
		contentType string
		withCookie  bool
		wantStatus  int
	}{
		{name: "missing origin", path: "/api/admin/accounts/71/disable", body: `{"reason":"security"}`, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusForbidden},
		{name: "wrong csrf", path: "/api/admin/accounts/71/enable", body: `{"reason":"appeal"}`, origin: testOrigin, csrf: "wrong", contentType: "application/json", withCookie: true, wantStatus: http.StatusForbidden},
		{name: "missing cookie", path: "/api/admin/accounts/71/disable", body: `{"reason":"security"}`, origin: testOrigin, csrf: testCSRF, contentType: "application/json", wantStatus: http.StatusUnauthorized},
		{name: "query account injection", path: "/api/admin/accounts/71/disable?accountId=99", body: `{"reason":"security"}`, origin: testOrigin, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusBadRequest},
		{name: "body account injection", path: "/api/admin/accounts/71/disable", body: `{"accountId":99,"reason":"security"}`, origin: testOrigin, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusBadRequest},
		{name: "uid injection", path: "/api/admin/accounts/71/rebind", body: `{"uid":"987654321","challengeId":"proof","reason":"ownership"}`, origin: testOrigin, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusBadRequest},
		{name: "noncanonical path id", path: "/api/admin/accounts/071/disable", body: `{"reason":"security"}`, origin: testOrigin, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusBadRequest},
		{name: "empty reason", path: "/api/admin/accounts/71/disable", body: `{"reason":"   "}`, origin: testOrigin, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusBadRequest},
		{name: "control reason", path: "/api/admin/accounts/71/enable", body: "{\"reason\":\"line one\\nline two\"}", origin: testOrigin, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusBadRequest},
		{name: "fake json media type", path: "/api/admin/accounts/71/enable", body: `{"reason":"appeal"}`, origin: testOrigin, csrf: testCSRF, contentType: "application/jsonx", withCookie: true, wantStatus: http.StatusBadRequest},
		{name: "oversized body", path: "/api/admin/accounts/71/disable", body: `{"reason":"` + strings.Repeat("a", 5000) + `"}`, origin: testOrigin, csrf: testCSRF, contentType: "application/json", withCookie: true, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{adminResult: ManagedAccount{AccountID: 71, Status: AccountStatusActive}}
			handler := newTestHTTPHandler(t, service, allowLimiter{})
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-CSRF-Token", test.csrf)
			request.Header.Set("Content-Type", test.contentType)
			if test.withCookie {
				request.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "administrator-cookie"})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if service.adminSession != "" {
				t.Fatalf("rejected request reached service with session %q", service.adminSession)
			}
			assertBodyOmitsSecrets(t, response.Body.String(), "987654321", "administrator-cookie", `"accountId":99`)
		})
	}
}

func TestHTTPAdministratorAccountRoutesMountAheadOfGeneralAdminHandler(t *testing.T) {
	service := &fakeHTTPService{adminResult: ManagedAccount{AccountID: 71, Status: AccountStatusDisabled}}
	identityHandler := newTestHTTPHandler(t, service, allowLimiter{})
	generalAdmin := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	})
	application := hostedapp.New(hostedapp.Dependencies{DB: healthyAppDatabase{}, Auth: identityHandler, Admin: generalAdmin})

	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/71/disable", strings.NewReader(`{"reason":"security"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("X-CSRF-Token", testCSRF)
	request.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "administrator-cookie"})
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.adminAccountID != 71 {
		t.Fatalf("account route status=%d body=%q target=%d", response.Code, response.Body.String(), service.adminAccountID)
	}

	response = httptest.NewRecorder()
	application.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/admin/session", nil))
	if response.Code != http.StatusTeapot {
		t.Fatalf("general admin route status=%d, want %d", response.Code, http.StatusTeapot)
	}
}

func TestHTTPAdministratorAccountResultCannotOverridePathIdentityOrExposeErrors(t *testing.T) {
	tests := []struct {
		name   string
		result ManagedAccount
		err    error
		forbid string
	}{
		{name: "mismatched result", result: ManagedAccount{AccountID: 99, Status: AccountStatusActive}, forbid: `"accountId":99`},
		{name: "private database text", err: errors.New("duplicate UID 987654321 database-private"), forbid: "987654321"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{adminResult: test.result, adminErr: test.err}
			handler := newTestHTTPHandler(t, service, allowLimiter{})
			request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/71/enable", strings.NewReader(`{"reason":"appeal"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", testOrigin)
			request.Header.Set("X-CSRF-Token", testCSRF)
			request.AddCookie(&http.Cookie{Name: SiteSessionCookie, Value: "administrator-cookie"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusConflict || response.Body.String() != `{"error":"operation_failed"}`+"\n" {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			assertBodyOmitsSecrets(t, response.Body.String(), test.forbid, "database-private")
		})
	}
}

type healthyAppDatabase struct{}

func (healthyAppDatabase) Health(context.Context) error { return nil }

type fakeHTTPService struct {
	beginCalls     int
	challenge      Challenge
	beginErr       error
	pollResult     PollResult
	pollErr        error
	pollCalls      int
	loginResult    SiteSession
	loginErr       error
	loginChallenge string
	session        Session
	requireErr     error
	requiredToken  string
	logoutErr      error
	logoutToken    string
	cancelCalls    []string
	adminResult    ManagedAccount
	adminErr       error
	adminSession   string
	adminAccountID int64
	adminChallenge string
	adminReason    string
}

func (service *fakeHTTPService) Begin(context.Context) (Challenge, error) {
	service.beginCalls++
	return service.challenge, service.beginErr
}

func (service *fakeHTTPService) Poll(_ context.Context, _ string) (PollResult, error) {
	service.pollCalls++
	return service.pollResult, service.pollErr
}

func (service *fakeHTTPService) Login(_ context.Context, challengeID string) (SiteSession, error) {
	service.loginChallenge = challengeID
	return service.loginResult, service.loginErr
}

func (service *fakeHTTPService) Logout(_ context.Context, token string) error {
	service.logoutToken = token
	return service.logoutErr
}

func (service *fakeHTTPService) RequireSession(_ context.Context, token string) (Session, error) {
	service.requiredToken = token
	return service.session, service.requireErr
}

func (service *fakeHTTPService) Cancel(challengeID string) {
	service.cancelCalls = append(service.cancelCalls, challengeID)
}

func (service *fakeHTTPService) DisableAccount(_ context.Context, session string, accountID int64, reason string) (ManagedAccount, error) {
	service.adminSession, service.adminAccountID, service.adminReason, service.adminChallenge = session, accountID, reason, ""
	return service.adminResult, service.adminErr
}

func (service *fakeHTTPService) EnableAccount(_ context.Context, session string, accountID int64, reason string) (ManagedAccount, error) {
	service.adminSession, service.adminAccountID, service.adminReason, service.adminChallenge = session, accountID, reason, ""
	return service.adminResult, service.adminErr
}

func (service *fakeHTTPService) RebindVerifiedUID(_ context.Context, session string, accountID int64, challengeID, reason string) (ManagedAccount, error) {
	service.adminSession, service.adminAccountID, service.adminReason, service.adminChallenge = session, accountID, reason, challengeID
	return service.adminResult, service.adminErr
}

func (service *fakeHTTPService) wasCalled() bool {
	return service.beginCalls != 0 || service.pollCalls != 0 || service.loginChallenge != "" || service.requiredToken != "" ||
		service.logoutToken != "" || len(service.cancelCalls) != 0 || service.adminSession != "" || service.adminAccountID != 0 ||
		service.adminChallenge != "" || service.adminReason != ""
}

type allowLimiter struct{}

func (allowLimiter) Allow(context.Context, LimitScope, string) bool { return true }

type limitCall struct {
	scope LimitScope
	key   string
}

type recordingLimiter struct {
	denyScope LimitScope
	calls     []limitCall
}

func (limiter *recordingLimiter) Allow(_ context.Context, scope LimitScope, key string) bool {
	limiter.calls = append(limiter.calls, limitCall{scope: scope, key: key})
	return scope != limiter.denyScope
}

func newTestHTTPHandler(t *testing.T, service identityHTTPService, limiter ChallengeLimiter) *HTTPHandler {
	t.Helper()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: testOrigin,
		CSRFToken:     testCSRF,
		Limiter:       limiter,
		ClientIP:      DirectClientIP,
		Now:           func() time.Time { return time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler() error = %v", err)
	}
	return handler
}

func serveAuthRequest(handler http.Handler, method, path, body, remoteAddr string, mutating bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remoteAddr
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if mutating {
		request.Header.Set("Origin", testOrigin)
		request.Header.Set("X-CSRF-Token", testCSRF)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertBodyOmitsSecrets(t *testing.T, body string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(body, value) {
			t.Fatalf("response exposed %q: %q", value, body)
		}
	}
}

func equalLimitCalls(got, want []limitCall) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func decodeJSONBody(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
