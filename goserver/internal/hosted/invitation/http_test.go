package invitation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

const (
	testOrigin = "https://panel.example.test"
	testCSRF   = "csrf-test"
)

func TestHTTPExposesSixInvitationMethodRoutesWithoutSecretLeakage(t *testing.T) {
	now := time.Date(2026, 8, 16, 20, 0, 0, 0, time.UTC)
	service := &fakeHTTPService{
		generated: GeneratedInvitation{Invitation: Invitation{ID: 71, CodeHint: "****Ab_9", Status: StatusActive, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, Code: "complete-code-once", RemainingQuota: 2},
		listed:    InvitationList{RemainingQuota: 2, Invitations: []Invitation{{ID: 70, CodeHint: "****old4", Status: StatusRevoked, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}}},
		quota:     Quota{AccountID: 41, RemainingQuota: 5},
		session:   identity.SiteSession{Token: "new-site-cookie", AccountID: 51, ExpiresAt: now.Add(24 * time.Hour)},
	}
	handler := newTestHTTPHandler(t, service, allowLimits{}, now)

	registration := invitationRequest(http.MethodPost, "/api/auth/registration", `{"code":"invite-secret","registrationIntent":"intent-secret"}`, "")
	registrationResponse := serveInvitation(handler, registration)
	if registrationResponse.Code != http.StatusNoContent || service.redeemCode != "invite-secret" || service.redeemIntent != "intent-secret" {
		t.Fatalf("registration=%d %q args=%q %q", registrationResponse.Code, registrationResponse.Body.String(), service.redeemCode, service.redeemIntent)
	}
	cookies := registrationResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != identity.SiteSessionCookie || cookies[0].Value != "new-site-cookie" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Path != "/" || cookies[0].Domain != "" || cookies[0].SameSite != http.SameSiteLaxMode || !cookies[0].Expires.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("registration cookie=%#v", cookies)
	}

	listResponse := serveInvitation(handler, invitationRequest(http.MethodGet, "/api/invitations", "", "streamer-cookie"))
	if listResponse.Code != http.StatusOK || strings.Contains(listResponse.Body.String(), "complete-code-once") || !strings.Contains(listResponse.Body.String(), "****old4") {
		t.Fatalf("list=%d %q", listResponse.Code, listResponse.Body.String())
	}

	streamerGenerate := serveInvitation(handler, invitationRequest(http.MethodPost, "/api/invitations", `{}`, "streamer-cookie"))
	if streamerGenerate.Code != http.StatusCreated || strings.Count(streamerGenerate.Body.String(), "complete-code-once") != 1 || service.generateActor != ActorStreamer || service.generateSession != "streamer-cookie" {
		t.Fatalf("streamer generate=%d %q actor=%q token=%q", streamerGenerate.Code, streamerGenerate.Body.String(), service.generateActor, service.generateSession)
	}

	revokeResponse := serveInvitation(handler, invitationRequest(http.MethodDelete, "/api/invitations/71", "", "streamer-cookie"))
	if revokeResponse.Code != http.StatusNoContent || service.revokeID != 71 || service.revokeSession != "streamer-cookie" {
		t.Fatalf("revoke=%d %q id=%d token=%q", revokeResponse.Code, revokeResponse.Body.String(), service.revokeID, service.revokeSession)
	}

	adminGenerate := serveInvitation(handler, invitationRequest(http.MethodPost, "/api/admin/invitations", `{}`, "admin-cookie"))
	if adminGenerate.Code != http.StatusCreated || service.generateActor != ActorAdministrator || service.generateSession != "admin-cookie" {
		t.Fatalf("admin generate=%d %q actor=%q token=%q", adminGenerate.Code, adminGenerate.Body.String(), service.generateActor, service.generateSession)
	}

	quotaResponse := serveInvitation(handler, invitationRequest(http.MethodPost, "/api/admin/accounts/41/invitation-quota", `{"remainingQuota":5,"reason":"support grant"}`, "admin-cookie"))
	if quotaResponse.Code != http.StatusOK || service.quotaAccountID != 41 || service.quotaRemaining != 5 || service.quotaReason != "support grant" || service.quotaSession != "admin-cookie" {
		t.Fatalf("quota=%d %q args=%d %d %q %q", quotaResponse.Code, quotaResponse.Body.String(), service.quotaAccountID, service.quotaRemaining, service.quotaReason, service.quotaSession)
	}

	for _, response := range []*httptest.ResponseRecorder{registrationResponse, listResponse, streamerGenerate, revokeResponse, adminGenerate, quotaResponse} {
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
		}
		for _, forbidden := range []string{"32249588", "new-site-cookie", "intent-secret", "streamer-cookie", "admin-cookie"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("response exposed %q: %q", forbidden, response.Body.String())
			}
		}
	}
}

func TestHTTPMalformedRawQueryIsRejectedBeforeServiceOnEveryRoute(t *testing.T) {
	tests := []struct{ method, path, body, cookie string }{
		{http.MethodPost, "/api/auth/registration?x;y", `{"code":"code","registrationIntent":"intent"}`, ""},
		{http.MethodGet, "/api/invitations?x;y", "", "streamer"},
		{http.MethodPost, "/api/invitations?x;y", `{}`, "streamer"},
		{http.MethodDelete, "/api/invitations/1?x;y", "", "streamer"},
		{http.MethodPost, "/api/admin/invitations?x;y", `{}`, "admin"},
		{http.MethodPost, "/api/admin/accounts/41/invitation-quota?x;y", `{"remainingQuota":1,"reason":"support"}`, "admin"},
	}
	for _, test := range tests {
		service := &fakeHTTPService{}
		handler := newTestHTTPHandler(t, service, allowLimits{}, time.Now())
		response := serveInvitation(handler, invitationRequest(test.method, test.path, test.body, test.cookie))
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_request\"}\n" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s = %d %q cache=%q", test.method, test.path, response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
		}
		if service.calls != 0 {
			t.Fatalf("%s %s reached service %d times", test.method, test.path, service.calls)
		}
	}
}

func TestHTTPRejectsCallerAccountInjectionAndNonJSONBodies(t *testing.T) {
	tests := []struct{ path, body string }{
		{path: "/api/invitations", body: `{"accountId":999}`},
		{path: "/api/admin/accounts/41/invitation-quota", body: `{"accountId":999,"remainingQuota":1,"reason":"support"}`},
	}
	for _, test := range tests {
		service := &fakeHTTPService{}
		handler := newTestHTTPHandler(t, service, allowLimits{}, time.Now())
		request := invitationRequest(http.MethodPost, test.path, test.body, "session")
		response := serveInvitation(handler, request)
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_request\"}\n" || service.calls != 0 {
			t.Fatalf("POST %s = %d %q calls=%d", test.path, response.Code, response.Body.String(), service.calls)
		}
	}

	service := &fakeHTTPService{}
	handler := newTestHTTPHandler(t, service, allowLimits{}, time.Now())
	request := invitationRequest(http.MethodPost, "/api/invitations", `{}`, "session")
	request.Header.Set("Content-Type", "text/plain")
	response := serveInvitation(handler, request)
	if response.Code != http.StatusBadRequest || service.calls != 0 {
		t.Fatalf("non-json=%d %q calls=%d", response.Code, response.Body.String(), service.calls)
	}
}

func TestHTTPRejectsUnexpectedGETAndDELETEBodiesBeforeService(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/invitations"},
		{method: http.MethodDelete, path: "/api/invitations/1"},
	} {
		service := &fakeHTTPService{}
		handler := newTestHTTPHandler(t, service, allowLimits{}, time.Now())
		response := serveInvitation(handler, invitationRequest(test.method, test.path, `{"unexpected":true}`, "streamer"))
		if response.Code != http.StatusBadRequest || response.Body.String() != "{\"error\":\"invalid_request\"}\n" || service.calls != 0 {
			t.Fatalf("%s %s = %d %q calls=%d", test.method, test.path, response.Code, response.Body.String(), service.calls)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
		}
	}
}

func TestHTTPStreamerMutationsRejectCheaplyAndRateLimitBeforeAuthentication(t *testing.T) {
	now := time.Date(2026, 8, 16, 20, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
		mutate func(*http.Request)
	}{
		{name: "missing origin", method: http.MethodPost, path: "/api/invitations", body: `{}`, mutate: func(request *http.Request) { request.Header.Del("Origin") }},
		{name: "malformed query", method: http.MethodPost, path: "/api/invitations?x;y", body: `{}`},
		{name: "unexpected delete body", method: http.MethodDelete, path: "/api/invitations/1", body: `{"unexpected":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeHTTPService{}
			steps := make([]string, 0, 4)
			authenticate := recordingAuthentication(&steps, false)
			handler := newTestHTTPHandlerWithAuth(t, service, &orderedLimits{steps: &steps}, now, authenticate)
			request := invitationRequest(test.method, test.path, test.body, "invalid-session")
			if test.mutate != nil {
				test.mutate(request)
			}
			response := serveInvitation(handler, request)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if service.calls != 0 || len(steps) != 0 {
				t.Fatalf("cheap rejection reached dependency: service=%d steps=%v", service.calls, steps)
			}
		})
	}

	steps := make([]string, 0, 4)
	service := &fakeHTTPService{}
	handler := newTestHTTPHandlerWithAuth(t, service, &orderedLimits{steps: &steps}, now, recordingAuthentication(&steps, false))
	response := serveInvitation(handler, invitationRequest(http.MethodPost, "/api/invitations", `{}`, "invalid-session"))
	if response.Code != http.StatusUnauthorized || service.calls != 0 {
		t.Fatalf("invalid cookie = %d %q service=%d", response.Code, response.Body.String(), service.calls)
	}
	wantSteps := []string{"limit:global", "limit:per_ip", "limit:per_challenge", "authenticate"}
	if strings.Join(steps, ",") != strings.Join(wantSteps, ",") {
		t.Fatalf("dependency order=%v want=%v", steps, wantSteps)
	}

	steps = make([]string, 0, 3)
	service = &fakeHTTPService{}
	handler = newTestHTTPHandlerWithAuth(t, service, &orderedLimits{steps: &steps}, now, recordingAuthentication(&steps, false))
	response = serveInvitation(handler, invitationRequest(http.MethodDelete, "/api/invitations/1", "", ""))
	if response.Code != http.StatusUnauthorized || service.calls != 0 {
		t.Fatalf("missing cookie = %d %q service=%d", response.Code, response.Body.String(), service.calls)
	}
	wantSteps = []string{"limit:global", "limit:per_ip", "limit:per_challenge"}
	if strings.Join(steps, ",") != strings.Join(wantSteps, ",") {
		t.Fatalf("missing-cookie limit order=%v want=%v", steps, wantSteps)
	}
}

func TestHTTPMutationsUseGlobalPerIPAndHashedCredentialLimits(t *testing.T) {
	tests := []struct{ method, path, body, cookie string }{
		{http.MethodPost, "/api/auth/registration", `{"code":"code-secret","registrationIntent":"intent-secret"}`, ""},
		{http.MethodPost, "/api/invitations", `{}`, "streamer-secret"},
		{http.MethodDelete, "/api/invitations/1", "", "streamer-secret"},
		{http.MethodPost, "/api/admin/invitations", `{}`, "admin-secret"},
		{http.MethodPost, "/api/admin/accounts/41/invitation-quota", `{"remainingQuota":1,"reason":"support"}`, "admin-secret"},
	}
	for _, test := range tests {
		service := &fakeHTTPService{}
		limiter := &recordingLimits{deny: identity.LimitPerChallenge}
		handler := newTestHTTPHandler(t, service, limiter, time.Now())
		request := invitationRequest(test.method, test.path, test.body, test.cookie)
		request.RemoteAddr = "198.51.100.7:1234"
		response := serveInvitation(handler, request)
		if response.Code != http.StatusTooManyRequests || response.Body.String() != "{\"error\":\"rate_limited\"}\n" || service.calls != 0 {
			t.Fatalf("%s %s = %d %q calls=%d", test.method, test.path, response.Code, response.Body.String(), service.calls)
		}
		if len(limiter.calls) != 3 || limiter.calls[0].scope != identity.LimitGlobal || limiter.calls[1].scope != identity.LimitPerIP || limiter.calls[2].scope != identity.LimitPerChallenge {
			t.Fatalf("limit calls=%#v", limiter.calls)
		}
		for _, call := range limiter.calls {
			for _, forbidden := range []string{"code-secret", "intent-secret", "streamer-secret", "admin-secret"} {
				if strings.Contains(call.key, forbidden) {
					t.Fatalf("limiter key exposed %q: %#v", forbidden, limiter.calls)
				}
			}
		}
	}
}

func TestHTTPOriginCSRFFailuresAndServiceErrorsAreGeneric(t *testing.T) {
	service := &fakeHTTPService{generateErr: errors.New("private UID 32249588 database detail")}
	handler := newTestHTTPHandler(t, service, allowLimits{}, time.Now())

	rejected := invitationRequest(http.MethodPost, "/api/invitations", `{}`, "streamer-cookie")
	rejected.Header.Del("Origin")
	rejectedResponse := serveInvitation(handler, rejected)
	if rejectedResponse.Code != http.StatusForbidden || rejectedResponse.Body.String() != "{\"error\":\"request_rejected\"}\n" || service.calls != 0 {
		t.Fatalf("origin rejection=%d %q calls=%d", rejectedResponse.Code, rejectedResponse.Body.String(), service.calls)
	}

	failure := serveInvitation(handler, invitationRequest(http.MethodPost, "/api/invitations", `{}`, "streamer-cookie"))
	if failure.Code != http.StatusConflict || failure.Body.String() != "{\"error\":\"operation_failed\"}\n" {
		t.Fatalf("generic failure=%d %q", failure.Code, failure.Body.String())
	}
	for _, forbidden := range []string{"32249588", "database", "streamer-cookie"} {
		if strings.Contains(failure.Body.String(), forbidden) {
			t.Fatalf("failure exposed %q: %q", forbidden, failure.Body.String())
		}
	}
}

type fakeHTTPService struct {
	calls           int
	generated       GeneratedInvitation
	generateErr     error
	generateSession string
	generateActor   ActorKind
	listed          InvitationList
	listErr         error
	listSession     string
	revokeErr       error
	revokeSession   string
	revokeID        int64
	quota           Quota
	quotaErr        error
	quotaSession    string
	quotaAccountID  int64
	quotaRemaining  uint64
	quotaReason     string
	session         identity.SiteSession
	redeemErr       error
	redeemCode      string
	redeemIntent    string
}

func (service *fakeHTTPService) Generate(_ context.Context, session string, actor ActorKind) (GeneratedInvitation, error) {
	service.calls++
	service.generateSession, service.generateActor = session, actor
	return service.generated, service.generateErr
}

func (service *fakeHTTPService) List(_ context.Context, session string) (InvitationList, error) {
	service.calls++
	service.listSession = session
	return service.listed, service.listErr
}

func (service *fakeHTTPService) Revoke(_ context.Context, session string, id int64) error {
	service.calls++
	service.revokeSession, service.revokeID = session, id
	return service.revokeErr
}

func (service *fakeHTTPService) AdjustQuota(_ context.Context, session string, accountID int64, remaining uint64, reason string) (Quota, error) {
	service.calls++
	service.quotaSession, service.quotaAccountID, service.quotaRemaining, service.quotaReason = session, accountID, remaining, reason
	return service.quota, service.quotaErr
}

func (service *fakeHTTPService) Redeem(_ context.Context, code, intent string) (identity.SiteSession, error) {
	service.calls++
	service.redeemCode, service.redeemIntent = code, intent
	return service.session, service.redeemErr
}

type allowLimits struct{}

func (allowLimits) Allow(context.Context, identity.LimitScope, string) bool { return true }

type limitCall struct {
	scope identity.LimitScope
	key   string
}

type recordingLimits struct {
	deny  identity.LimitScope
	calls []limitCall
}

func (limiter *recordingLimits) Allow(_ context.Context, scope identity.LimitScope, key string) bool {
	limiter.calls = append(limiter.calls, limitCall{scope: scope, key: key})
	return scope != limiter.deny
}

func newTestHTTPHandler(t *testing.T, service invitationHTTPService, limiter identity.ChallengeLimiter, now time.Time) *HTTPHandler {
	t.Helper()
	return newTestHTTPHandlerWithAuth(t, service, limiter, now, func(next http.Handler) http.Handler { return next })
}

func newTestHTTPHandlerWithAuth(t *testing.T, service invitationHTTPService, limiter identity.ChallengeLimiter, now time.Time, authenticate func(http.Handler) http.Handler) *HTTPHandler {
	t.Helper()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: testOrigin, CSRFToken: testCSRF, Limiter: limiter, ClientIP: identity.DirectClientIP,
		Authenticate: authenticate, Now: fixedNow(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type orderedLimits struct{ steps *[]string }

func (limiter *orderedLimits) Allow(_ context.Context, scope identity.LimitScope, _ string) bool {
	*limiter.steps = append(*limiter.steps, "limit:"+string(scope))
	return true
}

func recordingAuthentication(steps *[]string, allow bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			*steps = append(*steps, "authenticate")
			if !allow {
				writeError(response, http.StatusUnauthorized, "authentication_failed")
				return
			}
			next.ServeHTTP(response, request)
		})
	}
}

func invitationRequest(method, path, body, cookie string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("X-CSRF-Token", testCSRF)
	request.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: cookie})
	}
	return request
}

func serveInvitation(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
