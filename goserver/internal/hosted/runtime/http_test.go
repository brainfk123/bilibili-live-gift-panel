package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
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

func TestRuntimeHTTPEventsOwnsConfigLeaseAndUsesInjectedTwentySecondKeepalive(t *testing.T) {
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
	timer := <-timerCreated
	if timer.delay != 20*time.Second {
		t.Fatalf("keepalive delay = %v, want 20s", timer.delay)
	}
	timer.Fire()
	<-timerCreated
	cancel()
	<-done

	body := response.Body.String()
	for _, required := range []string{"event: status", "event: snapshot", `"health":9`, "event: degraded", ": keepalive"} {
		if !strings.Contains(body, required) {
			t.Fatalf("SSE body missing %q: %q", required, body)
		}
	}
	if response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SSE headers = %#v", response.Header())
	}
	if service.acquireKind != LeaseConfig || service.accountID != 7 || lease.releaseCalls != 1 {
		t.Fatalf("lease ownership service=%#v lease=%#v", service, lease)
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
func (service *recordingHTTPRuntime) Snapshot(context.Context, int64) (configuration.RuntimeState, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.snapshot, nil
}

type recordingHTTPLease struct {
	kind         LeaseKind
	releaseCalls int
}

func (lease *recordingHTTPLease) Kind() LeaseKind             { return lease.kind }
func (lease *recordingHTTPLease) Renew(context.Context) error { return nil }
func (lease *recordingHTTPLease) Release()                    { lease.releaseCalls++ }

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

var _ = errors.Is
