package biligateway

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"

	"github.com/gorilla/websocket"
)

func TestHTTPReplaceUsesAdminCookieAndNeverReturnsServiceCookie(t *testing.T) {
	service := &fakeHTTPService{}
	handler, err := NewHTTPHandler(service, testHTTPOptions())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bili-service/replace", strings.NewReader(`{"challengeId":"service-challenge"}`))
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "administrator-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("replace status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.token != "administrator-session" || service.challengeID != "service-challenge" {
		t.Fatalf("replace arguments token=%q challenge=%q", service.token, service.challengeID)
	}
	for _, forbidden := range []string{"SESSDATA", "service-cookie", "private"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("replace response exposed %q: %q", forbidden, response.Body.String())
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
}

func TestHTTPChallengeAndReplaceRejectWrongMethods(t *testing.T) {
	handler, err := NewHTTPHandler(&fakeHTTPService{}, testHTTPOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/admin/bili-service/challenge", "/api/admin/bili-service/replace"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s status = %d, want 405", path, response.Code)
		}
	}
}

func TestHTTPBiliServiceRateLimitsBeforeAdministratorAuthenticationWithoutRawToken(t *testing.T) {
	options := testHTTPOptions()
	options.Limiter = denyHTTPRequests{}
	handler, err := NewHTTPHandler(&fakeHTTPService{}, options)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bili-service/challenge", nil)
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "administrator-session-private"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || strings.Contains(response.Body.String(), "administrator-session-private") {
		t.Fatalf("rate limit status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestHTTPStatusRequiresAdministratorSessionAndReturnsOnlyStatusShape(t *testing.T) {
	handler, err := NewHTTPHandler(&fakeHTTPService{}, testHTTPOptions())
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/admin/bili-service/status", nil))
	if unauthenticated.Code != http.StatusUnauthorized || strings.Contains(unauthenticated.Body.String(), "private") {
		t.Fatalf("unauthenticated status=%d body=%q", unauthenticated.Code, unauthenticated.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bili-service/status", nil)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "administrator-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 3 || body["version"] != float64(1) || body["health"] != "healthy" || body["lastVerifiedAt"] == nil {
		t.Fatalf("status body=%v", body)
	}
}

func TestHTTPStatusKeepsExactShapeForMissingAndUnavailableCredentials(t *testing.T) {
	for _, health := range []string{"missing", "unavailable"} {
		t.Run(health, func(t *testing.T) {
			handler, err := NewHTTPHandler(&fakeHTTPService{status: CredentialStatus{Health: health}, hasStatus: true}, testHTTPOptions())
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/admin/bili-service/status", nil)
			request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "administrator-session"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 2 || body["version"] != float64(0) || body["health"] != health || body["lastVerifiedAt"] != nil {
				t.Fatalf("status body=%v", body)
			}
		})
	}
}

func TestHTTPUpstreamMapsRetryAfterWithoutExposingResponseOrCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Cookie"); got != "SESSDATA=private-cookie" {
			t.Fatalf("Cookie = %q", got)
		}
		response.Header().Set("Retry-After", "17")
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte("private upstream body must not escape"))
	}))
	defer server.Close()
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{Client: server.Client(), RoomInfoEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upstream.RoomInfo(context.Background(), "12", []byte("SESSDATA=private-cookie"))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("RoomInfo() error = %v", err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "cookie") {
		t.Fatalf("error exposed secret response: %v", err)
	}
	if retry, ok := RetryAfter(err); !ok || retry != 17*time.Second {
		t.Fatalf("RetryAfter = %v, %v", retry, ok)
	}
}

func TestBiliPacketDecoderExpandsBoundedCompressedApplicationBodies(t *testing.T) {
	packet := encodeDanmakuPacket(danmakuMessageOperation, []byte(`{"cmd":"SEND_GIFT","data":{"giftId":1}}`))
	bodies, err := decodeDanmakuApplicationBodies(packet)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || !strings.Contains(string(bodies[0]), "SEND_GIFT") {
		t.Fatalf("bodies = %q", bodies)
	}
}

func TestBiliPacketDecoderRejectsCompressedExpansionBeyondBound(t *testing.T) {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), maximumDanmakuPayload+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := encodeDanmakuPacket(danmakuMessageOperation, compressed.Bytes())
	payload[6], payload[7] = 0, 2
	if _, err := decodeDanmakuApplicationBodies(payload); !errors.Is(err, ErrEgressUnavailable) {
		t.Fatalf("oversized compressed packet error = %v", err)
	}
}

func TestWebsocketConnectionTerminatesOnCompressedBoundsFailure(t *testing.T) {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), maximumDanmakuPayload+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := encodeDanmakuPacket(danmakuMessageOperation, compressed.Bytes())
	payload[6], payload[7] = 0, 2
	read := make(chan socketRead, 1)
	read <- socketRead{kind: websocket.BinaryMessage, payload: payload}
	connection := &websocketConnection{connection: &injectedDanmakuSocket{reads: read, writes: make(chan []byte, 1)}, done: make(chan struct{}), now: time.Now, newTicker: func(time.Duration) danmakuTicker { return &injectedTicker{ticks: make(chan time.Time)} }}
	go connection.forward(func(Event) {})
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("compressed bounds failure did not close Done")
	}
	if !errors.Is(connection.Err(), ErrEgressUnavailable) {
		t.Fatalf("terminal error=%v", connection.Err())
	}
}

func TestWebsocketConnectionDoesNotRecordFailureAfterExplicitClose(t *testing.T) {
	connection := &websocketConnection{connection: &injectedDanmakuSocket{reads: make(chan socketRead), writes: make(chan []byte, 1)}, done: make(chan struct{}), now: time.Now, newTicker: func(time.Duration) danmakuTicker { return &injectedTicker{ticks: make(chan time.Time)} }}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	connection.fail(ErrEgressUnavailable)
	if connection.Err() != nil {
		t.Fatalf("explicit Close recorded terminal error=%v", connection.Err())
	}
}

func TestHTTPUpstreamOpenRoomUsesInjectedDanmakuTransportAndRedactsTerminalError(t *testing.T) {
	read := make(chan socketRead, 3)
	write := make(chan []byte, 3)
	socket := &injectedDanmakuSocket{reads: read, writes: write}
	ticker := &injectedTicker{ticks: make(chan time.Time, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/danmaku" || request.URL.Query().Get("id") != "12" || request.URL.Query().Get("type") != "0" || request.URL.Query().Get("room_id") != "" {
			t.Fatalf("danmaku request path=%q query=%q", request.URL.Path, request.URL.RawQuery)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"code": 0, "data": map[string]any{"token": "room-token", "host_list": []any{map[string]any{"host": "danmaku.example.test", "wss_port": 443}}}})
	}))
	defer server.Close()
	read <- socketRead{kind: websocket.BinaryMessage, payload: encodeDanmakuPacket(danmakuAuthReplyOperation, []byte(`{"code":0}`))}
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{
		Client: server.Client(), RoomInfoEndpoint: server.URL + "/room", DanmakuInfoEndpoint: server.URL + "/danmaku",
		Dial: func(_ context.Context, target string, header http.Header) (danmakuSocket, error) {
			if target != "wss://danmaku.example.test:443/sub" || header.Get("Cookie") != "SESSDATA=private; DedeUserID=32249588; buvid3=buvid-private" {
				t.Fatalf("dial target=%q cookie=%q", target, header.Get("Cookie"))
			}
			return socket, nil
		},
		Now:       func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) },
		NewTicker: func(time.Duration) danmakuTicker { return ticker },
	})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan Event, 1)
	connection, err := upstream.OpenRoom(context.Background(), "12", []byte("SESSDATA=private; DedeUserID=32249588; buvid3=buvid-private"), func(event Event) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	auth := <-write
	packets, err := decodeDanmakuPackets(auth)
	if err != nil || len(packets) != 1 || packets[0].operation != danmakuAuthOperation {
		t.Fatalf("auth packet=%#v error=%v", packets, err)
	}
	var body map[string]any
	if err := json.Unmarshal(packets[0].body, &body); err != nil || body["key"] != "room-token" || body["roomid"] != float64(12) || body["uid"] != float64(32249588) || body["buvid"] != "buvid-private" {
		t.Fatalf("auth body=%v error=%v", body, err)
	}
	read <- socketRead{kind: websocket.BinaryMessage, payload: encodeDanmakuPacket(danmakuMessageOperation, []byte(`{"cmd":"SEND_GIFT"}`))}
	if event := <-events; event.Type != "application" || string(event.Data) != `{"cmd":"SEND_GIFT"}` {
		t.Fatalf("event=%#v", event)
	}
	ticker.ticks <- time.Now()
	heartbeat := <-write
	if packets, err := decodeDanmakuPackets(heartbeat); err != nil || len(packets) != 1 || packets[0].operation != danmakuHeartbeatOperation {
		t.Fatalf("heartbeat packets=%#v error=%v", packets, err)
	}
	read <- socketRead{err: errors.New("private upstream read failure")}
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("connection did not terminate after read failure")
	}
	if !errors.Is(connection.Err(), ErrEgressUnavailable) || strings.Contains(connection.Err().Error(), "private") {
		t.Fatalf("terminal error=%v", connection.Err())
	}
}

func TestHTTPUpstreamOpenRoomRejectsDanmakuAuthenticationWithoutLeakingReply(t *testing.T) {
	read := make(chan socketRead, 1)
	socket := &injectedDanmakuSocket{reads: read, writes: make(chan []byte, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"code": 0, "data": map[string]any{"token": "room-token", "host_list": []any{map[string]any{"host": "danmaku.example.test", "wss_port": 443}}}})
	}))
	defer server.Close()
	read <- socketRead{kind: websocket.BinaryMessage, payload: encodeDanmakuPacket(danmakuAuthReplyOperation, []byte(`{"code":-101,"message":"private rejection"}`))}
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{
		Client: server.Client(), RoomInfoEndpoint: server.URL + "/room", DanmakuInfoEndpoint: server.URL + "/danmaku",
		Dial: func(context.Context, string, http.Header) (danmakuSocket, error) { return socket, nil },
		Now:  time.Now, NewTicker: func(time.Duration) danmakuTicker { return &injectedTicker{ticks: make(chan time.Time)} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upstream.OpenRoom(context.Background(), "12", []byte("SESSDATA=private"), func(Event) {}); !errors.Is(err, ErrEgressUnavailable) || strings.Contains(err.Error(), "private") {
		t.Fatalf("authentication rejection error=%v", err)
	}
}

type socketRead struct {
	kind    int
	payload []byte
	err     error
}
type injectedDanmakuSocket struct {
	reads  chan socketRead
	writes chan []byte
}

func (socket *injectedDanmakuSocket) ReadMessage() (int, []byte, error) {
	value := <-socket.reads
	return value.kind, value.payload, value.err
}
func (socket *injectedDanmakuSocket) WriteMessage(_ int, payload []byte) error {
	socket.writes <- append([]byte(nil), payload...)
	return nil
}
func (*injectedDanmakuSocket) SetReadDeadline(time.Time) error { return nil }
func (*injectedDanmakuSocket) Close() error                    { return nil }

type injectedTicker struct{ ticks chan time.Time }

func (ticker *injectedTicker) C() <-chan time.Time { return ticker.ticks }
func (*injectedTicker) Stop()                      {}

type fakeHTTPService struct {
	token, challengeID string
	status             CredentialStatus
	hasStatus          bool
}
type allowHTTPRequests struct{}

func (allowHTTPRequests) Allow(context.Context, identity.LimitScope, string) bool { return true }

type denyHTTPRequests struct{}

func (denyHTTPRequests) Allow(context.Context, identity.LimitScope, string) bool { return false }
func testHTTPOptions() HTTPOptions {
	return HTTPOptions{AllowedOrigin: "https://admin.example.test", CSRFToken: "csrf", Limiter: allowHTTPRequests{}, ClientIP: func(*http.Request) string { return "127.0.0.1" }}
}

func (service *fakeHTTPService) Begin(context.Context) (identity.Challenge, error) {
	return identity.Challenge{ID: "service-challenge", QRImage: "data:image/png;base64,qr", ExpiresAt: time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)}, nil
}
func (service *fakeHTTPService) Replace(_ context.Context, token, challengeID string) error {
	service.token, service.challengeID = token, challengeID
	return nil
}
func (*fakeHTTPService) RequireSession(context.Context, string) error { return nil }
func (service *fakeHTTPService) Status(context.Context) CredentialStatus {
	if service.hasStatus {
		return service.status
	}
	verifiedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	return CredentialStatus{Version: 1, Health: "healthy", LastVerifiedAt: &verifiedAt}
}
