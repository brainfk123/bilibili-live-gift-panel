package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hostedapp "bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/identity"
	hostedruntime "bilibili-live-gift-panel/internal/hosted/runtime"
)

func TestCredentialRouteRequiresAdministratorSessionAndRecentTOTP(t *testing.T) {
	service := &fakeHTTPService{issued: IssuedCredential{PublicID: testPublicID, URL: "https://host.example/obs/" + testPublicID + "#token=secret"}}
	handler := newTestHTTPHandler(t, service, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/41/obs-credential", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://host.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if service.issueAccountID != 41 || service.issueAdminToken != "admin-token" {
		t.Fatalf("Issue() account=%d token=%q", service.issueAccountID, service.issueAdminToken)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["publicId"] != testPublicID || body["url"] != service.issued.URL {
		t.Fatalf("body = %#v", body)
	}
}

func TestExchangeAcceptsTokenOnlyInBodyAndSetsExactScopedCookie(t *testing.T) {
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	service := &fakeHTTPService{session: ShortSession{Token: "short-secret", AccountID: 41, ExpiresAt: now.Add(12 * time.Hour)}}
	handler := newTestHTTPHandler(t, service, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{Now: func() time.Time { return now }})
	request := httptest.NewRequest(http.MethodPost, "/obs/"+testPublicID+"/exchange", strings.NewReader(`{"token":"long-secret-value-that-is-long-enough"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://host.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if service.exchangePublicID != testPublicID || service.exchangeToken != "long-secret-value-that-is-long-enough" {
		t.Fatalf("Exchange() publicID=%q token=%q", service.exchangePublicID, service.exchangeToken)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != OBSSessionCookie || cookie.Value != "short-secret" || cookie.Path != "/obs/"+testPublicID || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Domain != "" || cookie.MaxAge != 12*60*60 {
		t.Fatalf("cookie = %#v", cookie)
	}
	if strings.Contains(request.URL.RequestURI(), "long-secret") {
		t.Fatalf("request target leaked long token: %q", request.URL.RequestURI())
	}
}

func TestExchangeRateLimitRunsBeforeReadingSecretBody(t *testing.T) {
	service := &fakeHTTPService{}
	handler := newTestHTTPHandler(t, service, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{Limiter: denyAllLimiter{}})
	request := httptest.NewRequest(http.MethodPost, "/obs/"+testPublicID+"/exchange", nil)
	request.Body = panicReadCloser{}
	request.ContentLength = 32
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://host.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if service.exchangeCalls != 0 {
		t.Fatalf("Exchange() calls = %d", service.exchangeCalls)
	}
}

func TestCredentialAllLimiterScopesRunBeforeBodyDecodeAndAuthorization(t *testing.T) {
	service := &fakeHTTPService{}
	limiter := &denyNthLimiter{denyAt: 4}
	handler := newTestHTTPHandler(t, service, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{Limiter: limiter})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/41/obs-credential", nil)
	request.Body = panicReadCloser{}
	request.ContentLength = 2
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://host.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests || limiter.calls != 4 || service.issueCalls != 0 {
		t.Fatalf("status=%d limiter calls=%d Issue calls=%d", response.Code, limiter.calls, service.issueCalls)
	}
}

func TestCredentialCheapGuardsRejectBeforeLimitersOrAuthorization(t *testing.T) {
	service := &fakeHTTPService{}
	limiter := &denyNthLimiter{denyAt: 99}
	handler := newTestHTTPHandler(t, service, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{Limiter: limiter})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/41/obs-credential", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || limiter.calls != 0 || service.issueCalls != 0 {
		t.Fatalf("status=%d limiter calls=%d Issue calls=%d", response.Code, limiter.calls, service.issueCalls)
	}
}

func TestCredentialCreateResetAcceptsOnlyAnExactEmptyObjectDTO(t *testing.T) {
	for _, body := range []string{"null", `{"unexpected":true}`, "[]"} {
		service := &fakeHTTPService{}
		handler := newTestHTTPHandler(t, service, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{})
		request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/41/obs-credential", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://host.example")
		request.Header.Set("X-CSRF-Token", "csrf")
		request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || service.issueCalls != 0 {
			t.Fatalf("body %s status=%d Issue calls=%d", body, response.Code, service.issueCalls)
		}
	}
}

func TestServiceErrorsNeverExposeCredentialOrDatabaseDetails(t *testing.T) {
	secret := "long-token-must-stay-private"
	service := &fakeHTTPService{issueErr: errors.New("database failed near " + secret)}
	handler := newTestHTTPHandler(t, service, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/41/obs-credential", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://host.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "database") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestEventsStreamPublisherWithoutOwningRuntimeLease(t *testing.T) {
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{
		acquired: make(chan struct{}),
		status:   hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70},
		snapshot: configuration.RuntimeState{AttributeValues: map[string]float64{"hp": 10}},
	}
	service := &fakeHTTPService{authenticatedAccountID: 41}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{})
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	waitOBSSignal(t, response.flushed, "initial display")
	publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 70, Revision: 2, Runtime: configuration.RuntimeState{AttributeValues: map[string]float64{"hp": 12}}})
	waitOBSSignal(t, response.flushed, "increment display")
	cancel()
	waitOBSSignal(t, done, "OBS stream shutdown")

	if runtimeService.acquireAccountID != 0 || runtimeService.lease.ReleaseCalls() != 0 || runtimeService.lease.RenewCalls() != 0 {
		t.Fatalf("OBS stream touched runtime lease: %#v", runtimeService)
	}
	body := response.String()
	if !strings.Contains(body, "event: display\ndata: {\"accountId\":41,\"liveSessionId\":70,\"revision\":0") || !strings.Contains(body, "\"hp\":10") {
		t.Fatalf("missing initial display snapshot: %s", body)
	}
	if !strings.Contains(body, "event: display\ndata: {\"accountId\":41,\"liveSessionId\":70,\"revision\":2") || !strings.Contains(body, "\"hp\":12") {
		t.Fatalf("missing increment: %s", body)
	}
	if response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %#v", response.Header())
	}
}

func TestEventsAppliesRateLimitsBeforeReadingBody(t *testing.T) {
	handler := newTestHTTPHandler(t, &fakeHTTPService{}, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{Limiter: denyAllLimiter{}})
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	request.Body = panicReadCloser{}
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestEventsCoalescesPublisherOverflowToLatestSnapshot(t *testing.T) {
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{acquired: make(chan struct{}), status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70}}
	service := &fakeHTTPService{
		authenticatedAccountID: 41,
		authenticateBlockAfter: 2,
		authenticateStarted:    make(chan struct{}),
		authenticateRelease:    make(chan struct{}),
	}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "initial display")
	publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 70, Revision: 1})
	waitOBSSignal(t, service.authenticateStarted, "increment frame authentication")
	for revision := uint64(2); revision <= 25; revision++ {
		publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 70, Revision: revision})
	}
	close(service.authenticateRelease)
	for !strings.Contains(response.String(), `"revision":17`) && !strings.Contains(response.String(), `"revision":25`) {
		waitOBSSignal(t, response.flushed, "coalesced display")
	}
	cancel()
	waitOBSSignal(t, done, "coalesced OBS stream shutdown")
	if !strings.Contains(response.String(), `"revision":25`) {
		t.Fatalf("stream did not reconcile to publisher latest: %q", response.String())
	}
}

func TestEventsRetriesInitialSnapshotAcrossSessionSwitchAndRejectsStaleSessionEvents(t *testing.T) {
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{
		acquired: make(chan struct{}),
		status:   hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 72},
		statusSequence: []hostedruntime.Status{
			{State: hostedruntime.StateActive, SessionID: 70},
			{State: hostedruntime.StateActive, SessionID: 71},
			{State: hostedruntime.StateActive, SessionID: 71},
			{State: hostedruntime.StateActive, SessionID: 71},
		},
		snapshot: configuration.RuntimeState{AttributeValues: map[string]float64{"hp": 10}},
	}
	service := &fakeHTTPService{authenticatedAccountID: 41}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "stable initial display")
	publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 70, Revision: 999})
	publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 72, Revision: 1})
	for !strings.Contains(response.String(), `"liveSessionId":72`) {
		waitOBSSignal(t, response.flushed, "current-session display")
	}
	cancel()
	waitOBSSignal(t, done, "session-switch OBS stream shutdown")

	body := response.String()
	if !strings.Contains(body, `"liveSessionId":71`) || strings.Contains(body, `"liveSessionId":70`) {
		t.Fatalf("stream crossed session boundary inconsistently: %q", body)
	}
}

func TestEventsDiscardsInFlightSnapshotWhenItsSessionEndsDuringAuthentication(t *testing.T) {
	timer := &manualOBSTimer{channel: make(chan time.Time, 1)}
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{
		acquired: make(chan struct{}),
		status:   hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 71},
	}
	service := &fakeHTTPService{
		authenticatedAccountID: 41,
		authenticateBlockAfter: 2,
		authenticateStarted:    make(chan struct{}),
		authenticateRelease:    make(chan struct{}),
	}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{NewTimer: func(time.Duration) hostedruntime.Timer { return timer }})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "initial display")
	publisher.Publish(hostedruntime.DisplaySnapshot{
		AccountID: 41, LiveSessionID: 71, Revision: 1,
		Viewers: []hostedruntime.ViewerRow{{UID: 9, Name: "ended-session-secret", Avatar: "secret-avatar"}},
	})
	waitOBSSignal(t, service.authenticateStarted, "in-flight display authentication")
	runtimeService.SetStatus(hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 72})
	publisher.Clear(41, 71)
	close(service.authenticateRelease)
	timer.channel <- time.Now()
	for !strings.Contains(response.String(), ": keepalive") {
		waitOBSSignal(t, response.flushed, "session reset or keepalive")
	}
	cancel()
	waitOBSSignal(t, done, "session-reset OBS stream shutdown")

	body := response.String()
	if strings.Contains(body, "ended-session-secret") || strings.Count(body, `"liveSessionId":71`) != 1 {
		t.Fatalf("ended-session snapshot crossed the session boundary: %q", body)
	}
}

func TestEventsDoesNotRepeatSnapshotPublishedWhileInitialStateLoads(t *testing.T) {
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{
		acquired:      make(chan struct{}),
		statusStarted: make(chan struct{}),
		statusRelease: make(chan struct{}),
		status:        hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70},
	}
	service := &fakeHTTPService{authenticatedAccountID: 41}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()

	waitOBSSignal(t, runtimeService.statusStarted, "initial runtime status")
	publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 70, Revision: 2})
	close(runtimeService.statusRelease)
	waitOBSSignal(t, response.flushed, "initial published display")
	publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 70, Revision: 3})
	for !strings.Contains(response.String(), `"revision":3`) {
		waitOBSSignal(t, response.flushed, "next published display")
	}
	cancel()
	waitOBSSignal(t, done, "initial-load OBS stream shutdown")

	if count := strings.Count(response.String(), `"revision":2`); count != 1 {
		t.Fatalf("revision 2 event count = %d, body = %q", count, response.String())
	}
}

func TestEventsUsesInjectedTwentySecondKeepaliveWithoutSleeping(t *testing.T) {
	timer := &manualOBSTimer{channel: make(chan time.Time, 1)}
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{acquired: make(chan struct{}), status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70}}
	service := &fakeHTTPService{authenticatedAccountID: 41}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{NewTimer: func(delay time.Duration) hostedruntime.Timer {
		if delay != 20*time.Second {
			t.Fatalf("keepalive delay = %s", delay)
		}
		return timer
	}})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "initial display")
	timer.channel <- time.Now()
	waitOBSSignal(t, response.flushed, "keepalive")
	cancel()
	waitOBSSignal(t, done, "keepalive OBS stream shutdown")
	if !strings.Contains(response.String(), ": keepalive\n\n") {
		t.Fatalf("body = %q", response.String())
	}
}

func TestEventsKeepaliveSendsIdentityFreeResetWhenSessionChangesWithoutPublishedEvent(t *testing.T) {
	timer := &manualOBSTimer{channel: make(chan time.Time, 1)}
	publisher := hostedruntime.NewPublisher()
	publisher.Publish(hostedruntime.DisplaySnapshot{
		AccountID: 41, LiveSessionID: 70, Revision: 4,
		Viewers: []hostedruntime.ViewerRow{{UID: 9, Name: "old-viewer", Avatar: "old-avatar"}},
	})
	runtimeService := &fakeOBSRuntime{acquired: make(chan struct{}), status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70}}
	service := &fakeHTTPService{authenticatedAccountID: 41}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{NewTimer: func(time.Duration) hostedruntime.Timer { return timer }})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "viewer-bearing initial display")
	runtimeService.SetStatus(hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 71})
	runtimeService.SetSnapshot(configuration.RuntimeState{AttributeValues: map[string]float64{"hp": 20}})
	publisher.Clear(41, 70)
	timer.channel <- time.Now()
	waitOBSSignal(t, response.flushed, "identity-free reset display")
	cancel()
	waitOBSSignal(t, done, "identity-free reset OBS stream shutdown")

	body := response.String()
	reset := body[strings.LastIndex(body, "event: display"):]
	if !strings.Contains(reset, `"liveSessionId":71`) || !strings.Contains(reset, `"hp":20`) || strings.Contains(reset, `"viewers"`) || strings.Contains(reset, `"effects"`) {
		t.Fatalf("missing identity-free session reset: %q", body)
	}
}

func TestEventsUsesRollingWriteDeadlines(t *testing.T) {
	timer := &manualOBSTimer{channel: make(chan time.Time, 1)}
	runtimeService := &fakeOBSRuntime{acquired: make(chan struct{}), status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70}}
	service := &fakeHTTPService{authenticatedAccountID: 41}
	response := newControlledStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	base := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	var nowCalls atomic.Int64
	handler := newTestHTTPHandler(t, service, runtimeService, hostedruntime.NewPublisher(), HTTPOptions{
		Now:      func() time.Time { return base.Add(time.Duration(nowCalls.Add(1)) * time.Second) },
		NewTimer: func(time.Duration) hostedruntime.Timer { return timer },
	})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "initial display deadline")
	timer.channel <- time.Now()
	waitOBSSignal(t, response.flushed, "keepalive deadline")
	cancel()
	waitOBSSignal(t, done, "deadline OBS stream shutdown")

	deadlines := response.Deadlines()
	if len(deadlines) < 2 || deadlines[0].IsZero() || !deadlines[1].After(deadlines[0]) {
		t.Fatalf("write deadlines = %v", deadlines)
	}
}

func TestEventsWriteOrFlushFailureDoesNotTouchRuntimeLease(t *testing.T) {
	for _, test := range []struct {
		name     string
		writeErr error
		flushErr error
	}{
		{name: "write", writeErr: errors.New("write failed")},
		{name: "flush", flushErr: errors.New("flush failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeService := &fakeOBSRuntime{acquired: make(chan struct{}), status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70}}
			service := &fakeHTTPService{authenticatedAccountID: 41}
			response := newControlledStreamingRecorder()
			response.writeErr, response.flushErr = test.writeErr, test.flushErr
			request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
			ctx, cancel := context.WithCancel(request.Context())
			request = request.WithContext(ctx)
			defer cancel()
			request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
			timerStarted := make(chan struct{})
			handler := newTestHTTPHandler(t, service, runtimeService, hostedruntime.NewPublisher(), HTTPOptions{NewTimer: func(time.Duration) hostedruntime.Timer {
				close(timerStarted)
				return &manualOBSTimer{channel: make(chan time.Time)}
			}})
			done := make(chan struct{})
			go func() { handler.ServeHTTP(response, request); close(done) }()
			waitTimer := time.NewTimer(5 * time.Second)
			select {
			case <-done:
			case <-timerStarted:
				cancel()
				waitOBSSignal(t, done, "failed-output OBS stream shutdown")
				t.Fatal("stream entered its event loop after output failure")
			case <-waitTimer.C:
				cancel()
				waitOBSSignal(t, done, "timed-out output-failure OBS stream shutdown")
				t.Fatal("timed out waiting for OBS stream output failure")
			}
			waitTimer.Stop()
			if runtimeService.acquireAccountID != 0 || runtimeService.lease.ReleaseCalls() != 0 || runtimeService.lease.RenewCalls() != 0 {
				t.Fatal("output failure touched runtime lifecycle lease")
			}
		})
	}
}

func TestEventsRevalidatesShortSessionBeforeKeepaliveWithoutRuntimeLease(t *testing.T) {
	timer := &manualOBSTimer{channel: make(chan time.Time, 1)}
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{acquired: make(chan struct{}), status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70}}
	service := &fakeHTTPService{authenticatedAccountID: 41, authenticateErrAfter: 2}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{NewTimer: func(time.Duration) hostedruntime.Timer { return timer }})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "initial display")
	timer.channel <- time.Now()
	waitTimer := time.NewTimer(5 * time.Second)
	defer waitTimer.Stop()
	select {
	case <-done:
		if runtimeService.acquireAccountID != 0 || runtimeService.lease.ReleaseCalls() != 0 || runtimeService.lease.RenewCalls() != 0 || service.authenticateCalls != 3 {
			t.Fatalf("lease/auth calls = %d/%d/%d/%d", runtimeService.acquireAccountID, runtimeService.lease.ReleaseCalls(), runtimeService.lease.RenewCalls(), service.authenticateCalls)
		}
		if strings.Contains(response.String(), ": keepalive") {
			t.Fatalf("revoked session received keepalive: %q", response.String())
		}
	case <-response.flushed:
		cancel()
		waitOBSSignal(t, done, "revoked OBS stream shutdown")
		t.Fatal("revoked short session remained connected")
	case <-waitTimer.C:
		cancel()
		waitOBSSignal(t, done, "timed-out revoked OBS stream shutdown")
		t.Fatal("timed out waiting for revoked short session disconnect")
	}
}

func TestEventsAuthenticatesInitialIncrementResetAndKeepaliveFramesWithoutRuntimeLease(t *testing.T) {
	timer := &manualOBSTimer{channel: make(chan time.Time, 1)}
	publisher := hostedruntime.NewPublisher()
	runtimeService := &fakeOBSRuntime{
		acquired: make(chan struct{}), status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70},
		snapshot: configuration.RuntimeState{AttributeValues: map[string]float64{"hp": 10}},
	}
	service := &fakeHTTPService{authenticatedAccountID: 41}
	response := newStreamingRecorder()
	request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})
	handler := newTestHTTPHandler(t, service, runtimeService, publisher, HTTPOptions{NewTimer: func(time.Duration) hostedruntime.Timer { return timer }})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitOBSSignal(t, response.flushed, "initial display")
	if runtimeService.lease.RenewCalls() != 0 || service.authenticateCalls != 2 {
		t.Fatalf("initial frame renew/auth calls = %d/%d, want 0/2", runtimeService.lease.RenewCalls(), service.authenticateCalls)
	}

	publisher.Publish(hostedruntime.DisplaySnapshot{AccountID: 41, LiveSessionID: 70, Revision: 2})
	waitOBSSignal(t, response.flushed, "increment display")
	runtimeService.SetStatus(hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 71})
	runtimeService.SetSnapshot(configuration.RuntimeState{AttributeValues: map[string]float64{"hp": 20}})
	publisher.Clear(41, 70)
	timer.channel <- time.Now()
	waitOBSSignal(t, response.flushed, "identity-free reset display")
	waitOBSSignal(t, response.flushed, "renewed keepalive")
	cancel()
	waitOBSSignal(t, done, "renewed OBS stream shutdown")

	if runtimeService.acquireAccountID != 0 || runtimeService.lease.RenewCalls() != 0 || runtimeService.lease.ReleaseCalls() != 0 || service.authenticateCalls != 7 {
		t.Fatalf("account/renew/release/auth = %d/%d/%d/%d, want 0/0/0/7", runtimeService.acquireAccountID, runtimeService.lease.RenewCalls(), runtimeService.lease.ReleaseCalls(), service.authenticateCalls)
	}
	body := response.String()
	for _, required := range []string{`"revision":2`, `"liveSessionId":71`, `"hp":20`, ": keepalive"} {
		if !strings.Contains(body, required) {
			t.Fatalf("renewed OBS stream missing %q: %q", required, body)
		}
	}
}

func TestEventsRejectsInvalidDisabledOrDifferentAccountBeforeInitialFrame(t *testing.T) {
	for _, test := range []struct {
		name     string
		accounts []int64
		authErr  error
	}{
		{name: "invalid short session", accounts: []int64{41}, authErr: ErrAuthenticationFailed},
		{name: "disabled account", accounts: []int64{41}, authErr: ErrAccountDisabled},
		{name: "different account", accounts: []int64{41, 42}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeService := &fakeOBSRuntime{status: hostedruntime.Status{State: hostedruntime.StateActive, SessionID: 70}}
			authenticateErrAfter := 0
			if test.authErr != nil {
				authenticateErrAfter = 1
			}
			service := &fakeHTTPService{
				authenticatedAccountIDs: test.accounts,
				authenticateErrAfter:    authenticateErrAfter,
				authenticateErr:         test.authErr,
			}
			response := newStreamingRecorder()
			request := httptest.NewRequest(http.MethodGet, "/obs/"+testPublicID+"/events", nil)
			ctx, cancel := context.WithCancel(request.Context())
			defer cancel()
			request = request.WithContext(ctx)
			request.AddCookie(&http.Cookie{Name: OBSSessionCookie, Value: "short-secret"})

			handler := newTestHTTPHandler(t, service, runtimeService, hostedruntime.NewPublisher(), HTTPOptions{})
			done := make(chan struct{})
			go func() { handler.ServeHTTP(response, request); close(done) }()
			waitTimer := time.NewTimer(5 * time.Second)
			select {
			case <-done:
			case <-response.flushed:
				cancel()
				waitOBSSignal(t, done, "unauthorized OBS stream shutdown")
				t.Fatal("failed frame authorization entered the OBS stream loop")
			case <-waitTimer.C:
				cancel()
				waitOBSSignal(t, done, "timed-out unauthorized OBS stream shutdown")
				t.Fatal("timed out waiting for failed OBS frame authorization")
			}
			waitTimer.Stop()

			if response.String() != "" {
				t.Fatalf("failed frame authorization wrote an initial display: %q", response.String())
			}
			if runtimeService.acquireAccountID != 0 || runtimeService.lease.RenewCalls() != 0 || runtimeService.lease.ReleaseCalls() != 0 || service.authenticateCalls != 2 {
				t.Fatalf("account/renew/release/auth = %d/%d/%d/%d, want 0/0/0/2", runtimeService.acquireAccountID, runtimeService.lease.RenewCalls(), runtimeService.lease.ReleaseCalls(), service.authenticateCalls)
			}
		})
	}
}

func TestAllOBSPathsAreReadOnlyOutsideTheirExactMethods(t *testing.T) {
	obsHandler := newTestHTTPHandler(t, &fakeHTTPService{}, &fakeOBSRuntime{}, hostedruntime.NewPublisher(), HTTPOptions{})
	broad := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusTeapot) })
	handler := hostedapp.New(hostedapp.Dependencies{OBS: obsHandler, Auth: broad, Admin: broad})
	tests := []struct{ method, path, allow string }{
		{http.MethodPut, "/api/admin/accounts/41/obs-credential", http.MethodPost},
		{http.MethodPatch, "/obs/" + testPublicID + "/exchange", http.MethodPost},
		{http.MethodDelete, "/obs/" + testPublicID + "/events", http.MethodGet},
		{http.MethodPost, "/obs/" + testPublicID + "/events", http.MethodGet},
		{http.MethodHead, "/obs/" + testPublicID + "/events", http.MethodGet},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != test.allow {
			t.Fatalf("%s %s => status %d allow %q", test.method, test.path, response.Code, response.Header().Get("Allow"))
		}
	}
	for _, path := range []string{
		"/obs/" + testPublicID + "/configuration",
		"/obs/" + testPublicID + "/room",
		"/obs/" + testPublicID + "/reset",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status=%d, want no command route", path, response.Code)
		}
	}
}

type fakeHTTPService struct {
	issued                  IssuedCredential
	session                 ShortSession
	authenticatedAccountID  int64
	authenticatedAccountIDs []int64
	issueAccountID          int64
	issueAdminToken         string
	exchangePublicID        string
	exchangeToken           string
	exchangeCalls           int
	issueCalls              int
	issueErr                error
	authenticateCalls       int
	authenticateErrAfter    int
	authenticateErr         error
	authenticateBlockAfter  int
	authenticateStarted     chan struct{}
	authenticateRelease     chan struct{}
	authenticateBlockOnce   sync.Once
}

func (service *fakeHTTPService) Issue(_ context.Context, token string, accountID int64) (IssuedCredential, error) {
	service.issueCalls++
	service.issueAdminToken, service.issueAccountID = token, accountID
	return service.issued, service.issueErr
}
func (service *fakeHTTPService) Exchange(_ context.Context, publicID, token string) (ShortSession, error) {
	service.exchangeCalls++
	service.exchangePublicID, service.exchangeToken = publicID, token
	return service.session, nil
}
func (service *fakeHTTPService) Authenticate(_ context.Context, _, _ string) (int64, error) {
	service.authenticateCalls++
	if service.authenticateBlockAfter > 0 && service.authenticateCalls > service.authenticateBlockAfter {
		service.authenticateBlockOnce.Do(func() { close(service.authenticateStarted) })
		<-service.authenticateRelease
	}
	if service.authenticateErrAfter > 0 && service.authenticateCalls > service.authenticateErrAfter {
		if service.authenticateErr != nil {
			return 0, service.authenticateErr
		}
		return 0, ErrAuthenticationFailed
	}
	if len(service.authenticatedAccountIDs) > 0 {
		accountID := service.authenticatedAccountIDs[0]
		service.authenticatedAccountIDs = service.authenticatedAccountIDs[1:]
		return accountID, nil
	}
	if service.authenticatedAccountID <= 0 {
		return 0, ErrAuthenticationFailed
	}
	return service.authenticatedAccountID, nil
}

type fakeOBSRuntime struct {
	acquired          chan struct{}
	acquiredOnce      sync.Once
	acquireAccountID  int64
	acquireKind       hostedruntime.LeaseKind
	lease             fakeOBSLease
	status            hostedruntime.Status
	snapshot          configuration.RuntimeState
	statusStarted     chan struct{}
	statusRelease     chan struct{}
	statusStartedOnce sync.Once
	statusSequence    []hostedruntime.Status
	statusMu          sync.Mutex
	snapshotMu        sync.Mutex
}

func (runtimeService *fakeOBSRuntime) Acquire(_ context.Context, accountID int64, kind hostedruntime.LeaseKind) (hostedruntime.ConnectionLease, error) {
	runtimeService.acquireAccountID, runtimeService.acquireKind = accountID, kind
	if runtimeService.acquired != nil {
		runtimeService.acquiredOnce.Do(func() { close(runtimeService.acquired) })
	}
	return &runtimeService.lease, nil
}
func (runtimeService *fakeOBSRuntime) Status(context.Context, int64) (hostedruntime.Status, error) {
	if runtimeService.statusStarted != nil {
		runtimeService.statusStartedOnce.Do(func() { close(runtimeService.statusStarted) })
	}
	if runtimeService.statusRelease != nil {
		<-runtimeService.statusRelease
	}
	runtimeService.statusMu.Lock()
	defer runtimeService.statusMu.Unlock()
	if len(runtimeService.statusSequence) > 0 {
		status := runtimeService.statusSequence[0]
		runtimeService.statusSequence = runtimeService.statusSequence[1:]
		return status, nil
	}
	return runtimeService.status, nil
}
func (runtimeService *fakeOBSRuntime) Snapshot(context.Context, int64) (configuration.RuntimeState, error) {
	runtimeService.snapshotMu.Lock()
	defer runtimeService.snapshotMu.Unlock()
	return runtimeService.snapshot, nil
}

func (runtimeService *fakeOBSRuntime) SetStatus(status hostedruntime.Status) {
	runtimeService.statusMu.Lock()
	runtimeService.status = status
	runtimeService.statusMu.Unlock()
}

func (runtimeService *fakeOBSRuntime) SetSnapshot(snapshot configuration.RuntimeState) {
	runtimeService.snapshotMu.Lock()
	runtimeService.snapshot = snapshot
	runtimeService.snapshotMu.Unlock()
}

type fakeOBSLease struct {
	mu           sync.Mutex
	released     bool
	releaseCalls int
	renewCalls   int
	renewErr     error
}

func (*fakeOBSLease) Kind() hostedruntime.LeaseKind { return hostedruntime.LeaseOBS }
func (lease *fakeOBSLease) Renew(context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.renewCalls++
	return lease.renewErr
}
func (lease *fakeOBSLease) Release() {
	lease.mu.Lock()
	lease.released = true
	lease.releaseCalls++
	lease.mu.Unlock()
}
func (lease *fakeOBSLease) SetRenewError(err error) {
	lease.mu.Lock()
	lease.renewErr = err
	lease.mu.Unlock()
}
func (lease *fakeOBSLease) RenewCalls() int {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.renewCalls
}
func (lease *fakeOBSLease) ReleaseCalls() int {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.releaseCalls
}

type allowAllLimiter struct{}

func (allowAllLimiter) Allow(context.Context, identity.LimitScope, string) bool { return true }

type denyAllLimiter struct{}

func (denyAllLimiter) Allow(context.Context, identity.LimitScope, string) bool { return false }

type denyNthLimiter struct {
	calls  int
	denyAt int
}

func (limiter *denyNthLimiter) Allow(context.Context, identity.LimitScope, string) bool {
	limiter.calls++
	return limiter.calls != limiter.denyAt
}

func newTestHTTPHandler(t *testing.T, service httpService, runtimeService obsRuntime, publisher *hostedruntime.Publisher, overrides HTTPOptions) *HTTPHandler {
	t.Helper()
	options := HTTPOptions{
		AllowedOrigin: "https://host.example", CSRFToken: "csrf", Limiter: allowAllLimiter{},
		ClientIP: func(*http.Request) string { return "127.0.0.1" }, Runtime: runtimeService, Publisher: publisher,
	}
	if overrides.Now != nil {
		options.Now = overrides.Now
	}
	if overrides.NewTimer != nil {
		options.NewTimer = overrides.NewTimer
	}
	if overrides.Limiter != nil {
		options.Limiter = overrides.Limiter
	}
	handler, err := NewHTTPHandler(service, options)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type panicReadCloser struct{}

func (panicReadCloser) Read([]byte) (int, error) { panic("body read before limiter") }
func (panicReadCloser) Close() error             { return nil }

type manualOBSTimer struct {
	channel chan time.Time
	stopped bool
}

func (timer *manualOBSTimer) C() <-chan time.Time { return timer.channel }
func (timer *manualOBSTimer) Stop() bool          { timer.stopped = true; return true }

type streamingRecorder struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed chan struct{}
}

func newStreamingRecorder() *streamingRecorder {
	return &streamingRecorder{header: make(http.Header), flushed: make(chan struct{}, 8)}
}
func (recorder *streamingRecorder) Header() http.Header { return recorder.header }
func (recorder *streamingRecorder) WriteHeader(status int) {
	recorder.mu.Lock()
	if recorder.status == 0 {
		recorder.status = status
	}
	recorder.mu.Unlock()
}
func (recorder *streamingRecorder) Write(value []byte) (int, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.status == 0 {
		recorder.status = 200
	}
	return recorder.body.Write(value)
}
func (recorder *streamingRecorder) Flush() { recorder.flushed <- struct{}{} }
func (recorder *streamingRecorder) String() string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.body.String()
}

type controlledStreamingRecorder struct {
	*streamingRecorder
	controlMu sync.Mutex
	writeErr  error
	flushErr  error
	deadlines []time.Time
}

func newControlledStreamingRecorder() *controlledStreamingRecorder {
	return &controlledStreamingRecorder{streamingRecorder: newStreamingRecorder()}
}

func (recorder *controlledStreamingRecorder) Write(value []byte) (int, error) {
	if recorder.writeErr != nil {
		return 0, recorder.writeErr
	}
	return recorder.streamingRecorder.Write(value)
}

func (recorder *controlledStreamingRecorder) FlushError() error {
	if recorder.flushErr != nil {
		return recorder.flushErr
	}
	recorder.streamingRecorder.Flush()
	return nil
}

func (recorder *controlledStreamingRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.controlMu.Lock()
	recorder.deadlines = append(recorder.deadlines, deadline)
	recorder.controlMu.Unlock()
	return nil
}

func (recorder *controlledStreamingRecorder) Deadlines() []time.Time {
	recorder.controlMu.Lock()
	defer recorder.controlMu.Unlock()
	return append([]time.Time(nil), recorder.deadlines...)
}

func waitOBSSignal[T any](t *testing.T, channel <-chan T, label string) T {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case value := <-channel:
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}

var _ http.ResponseWriter = (*streamingRecorder)(nil)
var _ http.Flusher = (*streamingRecorder)(nil)
var _ io.ReadCloser = panicReadCloser{}
