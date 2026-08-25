package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/identity"
)

func TestRuntimeHTTPRoomMutationUsesAuthOriginCSRFQueryBodyAndSizeGuards(t *testing.T) {
	service := &recordingHTTPRuntime{status: Status{State: StateIdle}}
	handler := newHTTPTestHandler(t, service, nil)

	tests := []struct {
		name, target, origin, csrf, contentType, body string
		want                                          int
	}{
		{name: "origin", target: "/api/runtime/room", origin: "https://evil.example", csrf: "csrf", contentType: "application/json", body: `{"roomId":"42"}`, want: http.StatusForbidden},
		{name: "csrf", target: "/api/runtime/room", origin: "https://panel.example", csrf: "wrong", contentType: "application/json", body: `{"roomId":"42"}`, want: http.StatusForbidden},
		{name: "query", target: "/api/runtime/room?account=8", origin: "https://panel.example", csrf: "csrf", contentType: "application/json", body: `{"roomId":"42"}`, want: http.StatusBadRequest},
		{name: "unknown", target: "/api/runtime/room", origin: "https://panel.example", csrf: "csrf", contentType: "application/json", body: `{"roomId":"42","stop":true}`, want: http.StatusBadRequest},
		{name: "blank", target: "/api/runtime/room", origin: "https://panel.example", csrf: "csrf", contentType: "application/json", body: `{"roomId":" "}`, want: http.StatusBadRequest},
		{name: "content type", target: "/api/runtime/room", origin: "https://panel.example", csrf: "csrf", contentType: "text/plain", body: `{"roomId":"42"}`, want: http.StatusBadRequest},
		{name: "large", target: "/api/runtime/room", origin: "https://panel.example", csrf: "csrf", contentType: "application/json", body: `{"roomId":"` + strings.Repeat("1", 5000) + `"}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, test.target, strings.NewReader(test.body))
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-CSRF-Token", test.csrf)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status/cache = %d/%q, want %d/no-store", response.Code, response.Header().Get("Cache-Control"), test.want)
			}
		})
	}
	if service.setRoomCalls != 0 {
		t.Fatalf("rejected requests called SetRoom %d times", service.setRoomCalls)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/runtime/room", strings.NewReader(`{"roomId":"42"}`))
	request.Header.Set("Origin", "https://panel.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || service.setRoomCalls != 1 || service.accountID != 7 || service.roomID != "42" {
		t.Fatalf("successful room response=%d service=%#v", response.Code, service)
	}
}

func TestRuntimeHTTPRoomAppliesAllLimitScopesBeforeReadingBodyOrAuthenticating(t *testing.T) {
	service := &recordingHTTPRuntime{}
	limiter := &recordingRuntimeLimiter{denyScope: identity.LimitPerChallenge}
	authCalls := 0
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example", CSRFToken: "csrf", Limiter: limiter, ClientIP: func(*http.Request) string { return "127.0.0.1" },
		Authenticate: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				authCalls++
				next.ServeHTTP(response, request)
			})
		},
		AccountID: func(context.Context) (int64, bool) { return 7, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := &trackingReadCloser{reader: strings.NewReader(`{"roomId":"42"}`)}
	request := httptest.NewRequest(http.MethodPut, "/api/runtime/room", nil)
	request.Body = body
	request.ContentLength = -1
	request.Header.Set("Origin", "https://panel.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "opaque"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", response.Code)
	}
	wantScopes := []identity.LimitScope{identity.LimitGlobal, identity.LimitPerIP, identity.LimitPerChallenge}
	if !reflect.DeepEqual(limiter.scopes, wantScopes) {
		t.Fatalf("limiter scopes = %v, want %v", limiter.scopes, wantScopes)
	}
	if body.reads != 0 || authCalls != 0 || service.setRoomCalls != 0 {
		t.Fatalf("limited request reads/auth/service = %d/%d/%d, want zero", body.reads, authCalls, service.setRoomCalls)
	}
}

func TestRuntimeHTTPRoomRejectsDeclaredOversizeBeforeLimiterOrBodyRead(t *testing.T) {
	service := &recordingHTTPRuntime{}
	limiter := &recordingRuntimeLimiter{}
	handler := newHTTPTestHandlerWithLimiter(t, service, limiter)
	body := &trackingReadCloser{reader: strings.NewReader(`{"roomId":"42"}`)}
	request := httptest.NewRequest(http.MethodPut, "/api/runtime/room", nil)
	request.Body = body
	request.ContentLength = maximumRuntimeBody + 1
	request.Header.Set("Origin", "https://panel.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || len(limiter.scopes) != 0 || body.reads != 0 {
		t.Fatalf("oversize status/limits/reads = %d/%d/%d, want 400/0/0", response.Code, len(limiter.scopes), body.reads)
	}
}

func TestRuntimeHTTPExactMethodMatrixRejectsWithoutAuthenticationOrService(t *testing.T) {
	for _, route := range []struct {
		path    string
		allowed string
	}{
		{path: "/api/runtime/room", allowed: http.MethodPut},
		{path: "/api/runtime/status", allowed: http.MethodGet},
		{path: "/api/runtime/events", allowed: http.MethodGet},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
			if method == route.allowed {
				continue
			}
			t.Run(method+" "+route.path, func(t *testing.T) {
				service := &recordingHTTPRuntime{}
				authCalls := 0
				handler, err := NewHTTPHandler(service, HTTPOptions{
					AllowedOrigin: "https://panel.example", CSRFToken: "csrf", Limiter: allowRuntimeRequests{}, ClientIP: func(*http.Request) string { return "127.0.0.1" },
					Authenticate: func(next http.Handler) http.Handler {
						return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
							authCalls++
							next.ServeHTTP(response, request)
						})
					},
					AccountID: func(context.Context) (int64, bool) { return 7, true },
				})
				if err != nil {
					t.Fatal(err)
				}
				requestContext, cancelRequest := context.WithCancel(context.Background())
				cancelRequest()
				request := httptest.NewRequest(method, route.path, nil).WithContext(requestContext)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusMethodNotAllowed {
					t.Fatalf("status = %d, want 405", response.Code)
				}
				if authCalls != 0 || service.acquireCalls != 0 || service.statusCalls != 0 || service.setRoomCalls != 0 {
					t.Fatalf("rejected method auth/acquire/status/set = %d/%d/%d/%d", authCalls, service.acquireCalls, service.statusCalls, service.setRoomCalls)
				}
			})
		}
	}
}

func TestRuntimeHTTPStatusRejectsQueryAndBodyBeforeAuthentication(t *testing.T) {
	authCalls := 0
	service := &recordingHTTPRuntime{status: Status{State: StateActive, RoomID: "42"}}
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example", CSRFToken: "csrf", Limiter: allowRuntimeRequests{}, ClientIP: func(*http.Request) string { return "127.0.0.1" },
		Authenticate: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { authCalls++; next.ServeHTTP(w, r) })
		},
		AccountID: func(context.Context) (int64, bool) { return 7, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/runtime/status?extra=1", nil),
		httptest.NewRequest(http.MethodGet, "/api/runtime/status", strings.NewReader("x")),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.Code)
		}
	}
	if authCalls != 0 || service.statusCalls != 0 {
		t.Fatalf("invalid status requests auth/service calls = %d/%d", authCalls, service.statusCalls)
	}
}

func TestRuntimeHTTPEventsStayConnectedWithoutOwningExecutionLease(t *testing.T) {
	lease := &recordingHTTPLease{kind: LeaseConfig}
	service := &recordingHTTPRuntime{status: Status{State: StateDegraded, RoomID: "42", Degraded: true}, snapshot: configuration.RuntimeState{AttributeValues: map[string]float64{"health": 9}}, lease: lease}
	timerCreated := make(chan *manualTimer, 2)
	handler := newHTTPTestHandler(t, service, func(delay time.Duration) Timer {
		timer := &manualTimer{delay: delay, ch: make(chan time.Time, 1)}
		timerCreated <- timer
		return timer
	})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/runtime/events", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	timer := receiveRuntimeSignal(t, timerCreated, "initial keepalive timer")
	if timer.delay != 20*time.Second {
		t.Fatalf("keepalive delay = %v, want 20s", timer.delay)
	}
	timer.Fire()
	receiveRuntimeSignal(t, timerCreated, "next keepalive timer")
	cancel()
	receiveRuntimeSignal(t, done, "config stream shutdown")

	body := response.Body.String()
	for _, required := range []string{"event: status", "event: snapshot", `"health":9`, "event: degraded", ": keepalive"} {
		if !strings.Contains(body, required) {
			t.Fatalf("SSE body missing %q: %q", required, body)
		}
	}
	if response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SSE headers = %#v", response.Header())
	}
	if service.acquireCalls != 0 {
		t.Fatalf("status stream acquired execution lease %d times", service.acquireCalls)
	}
}

func TestRuntimeHTTPEventsStreamsChangedAuthoritativeStatusWithRollingDeadlines(t *testing.T) {
	lease := &recordingHTTPLease{kind: LeaseConfig}
	service := &recordingHTTPRuntime{status: Status{State: StateIdle, ConnectionHealthy: true}, snapshot: configuration.RuntimeState{}, lease: lease}
	timers := make(chan *manualTimer, 8)
	base := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	var nowCalls atomic.Int64
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example", CSRFToken: "csrf", Limiter: allowRuntimeRequests{}, ClientIP: func(*http.Request) string { return "127.0.0.1" },
		Authenticate: func(next http.Handler) http.Handler { return next }, AccountID: func(context.Context) (int64, bool) { return 7, true },
		Now: func() time.Time { return base.Add(time.Duration(nowCalls.Add(1)) * time.Second) },
		NewTimer: func(delay time.Duration) Timer {
			timer := &manualTimer{delay: delay, ch: make(chan time.Time, 1)}
			timers <- timer
			return timer
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := newRuntimeStreamingRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/runtime/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitRuntimeFlushes(t, response, 3)

	transitions := []Status{
		{State: StateDegraded, RoomID: "42", SessionID: 70, Degraded: true, ConnectionHealthy: false},
		{State: StateActive, RoomID: "42", SessionID: 70, ConnectionHealthy: true},
		{State: StateActive, RoomID: "84", SessionID: 71, ConnectionHealthy: true},
		{State: StateDisabled, RoomID: "84", SessionID: 71},
		{State: StateShuttingDown, RoomID: "84", SessionID: 71},
	}
	for index, status := range transitions {
		service.setStatus(status)
		timer := receiveRuntimeSignal(t, timers, fmt.Sprintf("transition %d timer", index))
		if timer.delay != runtimeKeepalive {
			t.Fatalf("transition %d timer = %v", index, timer.delay)
		}
		timer.Fire()
		flushes := 2
		if index < 2 {
			flushes = 3
		}
		waitRuntimeFlushes(t, response, flushes)
	}
	unchanged := receiveRuntimeSignal(t, timers, "unchanged-status timer")
	unchanged.Fire()
	waitRuntimeFlushes(t, response, 1)
	receiveRuntimeSignal(t, timers, "post-keepalive timer")
	cancel()
	receiveRuntimeSignal(t, done, "status stream shutdown")

	body := response.String()
	if got := strings.Count(body, "event: status"); got != 1+len(transitions) {
		t.Fatalf("status frames = %d, want %d: %q", got, 1+len(transitions), body)
	}
	if got := strings.Count(body, "event: degraded"); got != 3 {
		t.Fatalf("degraded/recovery frames = %d, want 3: %q", got, body)
	}
	for _, required := range []string{`"state":"degraded"`, `"connectionHealthy":false`, `"roomId":"84"`, `"state":"disabled"`, `"state":"shutting_down"`} {
		if !strings.Contains(body, required) {
			t.Fatalf("status stream missing %q: %q", required, body)
		}
	}
	deadlines := response.Deadlines()
	if len(deadlines) < 4 {
		t.Fatalf("rolling write deadlines = %v", deadlines)
	}
	for index := range deadlines {
		if deadlines[index].IsZero() || (index > 0 && !deadlines[index].After(deadlines[index-1])) {
			t.Fatalf("write deadlines are not rolling and bounded: %v", deadlines)
		}
	}
}

func TestRuntimeHTTPEventsOutputFailureDoesNotTouchExecutionLease(t *testing.T) {
	for _, test := range []struct {
		name       string
		phase      string
		writeError bool
	}{
		{name: "initial write", phase: "initial", writeError: true},
		{name: "initial flush", phase: "initial"},
		{name: "keepalive write", phase: "keepalive", writeError: true},
		{name: "keepalive flush", phase: "keepalive"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := &recordingHTTPLease{kind: LeaseConfig}
			service := &recordingHTTPRuntime{status: Status{State: StateIdle}, snapshot: configuration.RuntimeState{}, lease: lease}
			timers := make(chan *manualTimer, 1)
			handler := newHTTPTestHandler(t, service, func(delay time.Duration) Timer {
				timer := &manualTimer{delay: delay, ch: make(chan time.Time, 1)}
				timers <- timer
				return timer
			})
			response := newRuntimeStreamingRecorder()
			failure := errors.New("stream output failed")
			if test.phase == "initial" {
				response.setFailure(test.writeError, failure)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/api/runtime/events", nil).WithContext(ctx)
			done := make(chan struct{})
			go func() { handler.ServeHTTP(response, request); close(done) }()
			if test.phase == "keepalive" {
				waitRuntimeFlushes(t, response, 3)
				response.setFailure(test.writeError, failure)
				receiveRuntimeSignal(t, timers, "keepalive failure timer").Fire()
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			select {
			case <-done:
			case <-timers:
				cancel()
				receiveRuntimeSignal(t, done, "stream shutdown after unexpected timer")
				t.Fatal("config stream remained open after output failure")
			case <-deadline.C:
				cancel()
				t.Fatal("config stream did not exit after output failure")
			}
			if service.acquireCalls != 0 || lease.ReleaseCalls() != 0 {
				t.Fatalf("stream output touched execution lease: acquire/release = %d/%d", service.acquireCalls, lease.ReleaseCalls())
			}
		})
	}
}

func TestRuntimeHTTPEventsReauthenticatesEveryFrameWithoutRenewingExecution(t *testing.T) {
	lease := &recordingHTTPLease{kind: LeaseConfig}
	service := &recordingHTTPRuntime{status: Status{State: StateIdle, ConnectionHealthy: true}, snapshot: configuration.RuntimeState{}, lease: lease}
	authentication := &rotatingRuntimeAuthentication{accountID: 7}
	timers := make(chan *manualTimer, 3)
	handler := newReauthHTTPTestHandler(t, service, authentication, func(delay time.Duration) Timer {
		timer := &manualTimer{delay: delay, ch: make(chan time.Time, 1)}
		timers <- timer
		return timer
	})
	response := newRuntimeStreamingRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/runtime/events", nil).WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "current-cookie"})
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	waitRuntimeFlushes(t, response, 3)
	if calls, renewals := authentication.Calls(), lease.RenewCalls(); calls != 4 || renewals != 0 {
		t.Fatalf("handshake+initial auth/renew calls = %d/%d, want 4/0", calls, renewals)
	}

	receiveRuntimeSignal(t, timers, "unchanged keepalive timer").Fire()
	waitRuntimeFlushes(t, response, 1)
	service.setStatus(Status{State: StateDegraded, RoomID: "42", SessionID: 70, Degraded: true})
	receiveRuntimeSignal(t, timers, "changed-status timer").Fire()
	waitRuntimeFlushes(t, response, 3)
	receiveRuntimeSignal(t, timers, "post-status timer")
	cancel()
	receiveRuntimeSignal(t, done, "renewed config stream shutdown")

	if calls, renewals := authentication.Calls(), lease.RenewCalls(); calls != 8 || renewals != 0 {
		t.Fatalf("all-frame auth/renew calls = %d/%d, want 8/0", calls, renewals)
	}
	if strings.Count(response.String(), ": keepalive") != 2 || lease.ReleaseCalls() != 0 || service.acquireCalls != 0 {
		t.Fatalf("keepalives/release/acquire = %d/%d/%d, want 2/0/0", strings.Count(response.String(), ": keepalive"), lease.ReleaseCalls(), service.acquireCalls)
	}
}

func TestRuntimeHTTPEventsStopsOnRevokedOrChangedAccountWithoutExecutionLease(t *testing.T) {
	for _, test := range []struct {
		name      string
		accountID int64
	}{
		{name: "cookie revoked", accountID: 0},
		{name: "cookie changed account", accountID: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := &recordingHTTPLease{kind: LeaseConfig}
			service := &recordingHTTPRuntime{status: Status{State: StateIdle}, snapshot: configuration.RuntimeState{}, lease: lease}
			authentication := &rotatingRuntimeAuthentication{accountID: 7}
			timers := make(chan *manualTimer, 1)
			handler := newReauthHTTPTestHandler(t, service, authentication, func(delay time.Duration) Timer {
				timer := &manualTimer{delay: delay, ch: make(chan time.Time, 1)}
				timers <- timer
				return timer
			})
			response := newRuntimeStreamingRecorder()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/api/runtime/events", nil).WithContext(ctx)
			request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "current-cookie"})
			done := make(chan struct{})
			go func() { handler.ServeHTTP(response, request); close(done) }()
			waitRuntimeFlushes(t, response, 3)
			authentication.SetAccount(test.accountID)
			receiveRuntimeSignal(t, timers, "revocation keepalive timer").Fire()
			receiveRuntimeSignal(t, done, "revoked config stream shutdown")

			if strings.Contains(response.String(), ": keepalive") {
				t.Fatalf("revoked stream received keepalive: %q", response.String())
			}
			if lease.RenewCalls() != 0 || lease.ReleaseCalls() != 0 || service.acquireCalls != 0 {
				t.Fatalf("renew/release/acquire calls = %d/%d/%d, want 0/0/0", lease.RenewCalls(), lease.ReleaseCalls(), service.acquireCalls)
			}
		})
	}
}

type recordingHTTPRuntime struct {
	mu           sync.Mutex
	status       Status
	snapshot     configuration.RuntimeState
	lease        ConnectionLease
	setRoomCalls int
	statusCalls  int
	accountID    int64
	roomID       string
	acquireKind  LeaseKind
	acquireCalls int
	acquireError error
	setRoomError error
	statusError  error
}

func (service *recordingHTTPRuntime) Acquire(_ context.Context, accountID int64, kind LeaseKind) (ConnectionLease, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.acquireCalls++
	service.accountID, service.acquireKind = accountID, kind
	if service.acquireError != nil {
		return nil, service.acquireError
	}
	if service.lease == nil {
		service.lease = &recordingHTTPLease{kind: kind}
	}
	return service.lease, nil
}
func (service *recordingHTTPRuntime) SetRoom(_ context.Context, accountID int64, roomID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.setRoomCalls++
	service.accountID, service.roomID = accountID, roomID
	return service.setRoomError
}
func (service *recordingHTTPRuntime) Status(context.Context, int64) (Status, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.statusCalls++
	return service.status, service.statusError
}
func (service *recordingHTTPRuntime) setStatus(status Status) {
	service.mu.Lock()
	service.status = status
	service.mu.Unlock()
}
func (service *recordingHTTPRuntime) Snapshot(context.Context, int64) (configuration.RuntimeState, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.snapshot, nil
}

type recordingHTTPLease struct {
	mu           sync.Mutex
	kind         LeaseKind
	releaseCalls int
	renewCalls   int
	renewError   error
}

func (lease *recordingHTTPLease) Kind() LeaseKind { return lease.kind }
func (lease *recordingHTTPLease) Renew(context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.renewCalls++
	return lease.renewError
}
func (lease *recordingHTTPLease) Release() {
	lease.mu.Lock()
	lease.releaseCalls++
	lease.mu.Unlock()
}
func (lease *recordingHTTPLease) SetRenewError(err error) {
	lease.mu.Lock()
	lease.renewError = err
	lease.mu.Unlock()
}
func (lease *recordingHTTPLease) RenewCalls() int {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.renewCalls
}
func (lease *recordingHTTPLease) ReleaseCalls() int {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.releaseCalls
}

type allowRuntimeRequests struct{}

func (allowRuntimeRequests) Allow(context.Context, identity.LimitScope, string) bool { return true }

type recordingRuntimeLimiter struct {
	denyScope identity.LimitScope
	scopes    []identity.LimitScope
}

func (limiter *recordingRuntimeLimiter) Allow(_ context.Context, scope identity.LimitScope, _ string) bool {
	limiter.scopes = append(limiter.scopes, scope)
	return scope != limiter.denyScope
}

type trackingReadCloser struct {
	reader io.Reader
	reads  int
}

func (body *trackingReadCloser) Read(buffer []byte) (int, error) {
	body.reads++
	return body.reader.Read(buffer)
}
func (*trackingReadCloser) Close() error { return nil }

func newHTTPTestHandler(t *testing.T, service runtimeHTTPService, newTimer func(time.Duration) Timer) *HTTPHandler {
	t.Helper()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example", CSRFToken: "csrf", Limiter: allowRuntimeRequests{}, ClientIP: func(*http.Request) string { return "127.0.0.1" },
		Authenticate: func(next http.Handler) http.Handler { return next }, AccountID: func(context.Context) (int64, bool) { return 7, true }, NewTimer: newTimer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func newHTTPTestHandlerWithLimiter(t *testing.T, service runtimeHTTPService, limiter identity.ChallengeLimiter) *HTTPHandler {
	t.Helper()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example", CSRFToken: "csrf", Limiter: limiter, ClientIP: func(*http.Request) string { return "127.0.0.1" },
		Authenticate: func(next http.Handler) http.Handler { return next }, AccountID: func(context.Context) (int64, bool) { return 7, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type runtimeAuthenticationAccountKey struct{}

type rotatingRuntimeAuthentication struct {
	mu        sync.Mutex
	accountID int64
	calls     int
}

func (authentication *rotatingRuntimeAuthentication) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authentication.mu.Lock()
		authentication.calls++
		accountID := authentication.accountID
		authentication.mu.Unlock()
		if accountID <= 0 {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(request.Context(), runtimeAuthenticationAccountKey{}, accountID)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}
func (authentication *rotatingRuntimeAuthentication) SetAccount(accountID int64) {
	authentication.mu.Lock()
	authentication.accountID = accountID
	authentication.mu.Unlock()
}
func (authentication *rotatingRuntimeAuthentication) Calls() int {
	authentication.mu.Lock()
	defer authentication.mu.Unlock()
	return authentication.calls
}

func newReauthHTTPTestHandler(t *testing.T, service runtimeHTTPService, authentication *rotatingRuntimeAuthentication, newTimer func(time.Duration) Timer) *HTTPHandler {
	t.Helper()
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://panel.example", CSRFToken: "csrf", Limiter: allowRuntimeRequests{}, ClientIP: func(*http.Request) string { return "127.0.0.1" },
		Authenticate: authentication.Middleware,
		AccountID: func(ctx context.Context) (int64, bool) {
			accountID, ok := ctx.Value(runtimeAuthenticationAccountKey{}).(int64)
			return accountID, ok && accountID > 0
		},
		NewTimer: newTimer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type runtimeStreamingRecorder struct {
	mu        sync.Mutex
	header    http.Header
	body      strings.Builder
	status    int
	flushed   chan struct{}
	writeErr  error
	flushErr  error
	deadlines []time.Time
}

func newRuntimeStreamingRecorder() *runtimeStreamingRecorder {
	return &runtimeStreamingRecorder{header: make(http.Header), flushed: make(chan struct{}, 64)}
}
func (recorder *runtimeStreamingRecorder) Header() http.Header { return recorder.header }
func (recorder *runtimeStreamingRecorder) WriteHeader(status int) {
	recorder.mu.Lock()
	if recorder.status == 0 {
		recorder.status = status
	}
	recorder.mu.Unlock()
}
func (recorder *runtimeStreamingRecorder) Write(value []byte) (int, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.writeErr != nil {
		return 0, recorder.writeErr
	}
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.body.Write(value)
}
func (recorder *runtimeStreamingRecorder) Flush() { _ = recorder.FlushError() }
func (recorder *runtimeStreamingRecorder) FlushError() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.flushErr != nil {
		return recorder.flushErr
	}
	recorder.flushed <- struct{}{}
	return nil
}
func (recorder *runtimeStreamingRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.mu.Lock()
	recorder.deadlines = append(recorder.deadlines, deadline)
	recorder.mu.Unlock()
	return nil
}
func (recorder *runtimeStreamingRecorder) setFailure(write bool, failure error) {
	recorder.mu.Lock()
	if write {
		recorder.writeErr = failure
	} else {
		recorder.flushErr = failure
	}
	recorder.mu.Unlock()
}
func (recorder *runtimeStreamingRecorder) String() string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.body.String()
}
func (recorder *runtimeStreamingRecorder) Deadlines() []time.Time {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]time.Time(nil), recorder.deadlines...)
}

func waitRuntimeFlushes(t *testing.T, recorder *runtimeStreamingRecorder, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		receiveRuntimeSignal(t, recorder.flushed, fmt.Sprintf("flush %d of %d", index+1, count))
	}
}

func receiveRuntimeSignal[T any](t *testing.T, channel <-chan T, label string) T {
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

var _ = errors.Is
