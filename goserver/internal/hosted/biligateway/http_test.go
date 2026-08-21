package biligateway

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	hostedapp "bilibili-live-gift-panel/internal/hosted/app"
	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
)

func TestSensitiveServiceAccountReplacementRenewsOnlyAfterCredentialAudit(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x62}, 32))
	if err != nil {
		t.Fatal(err)
	}
	authorizedAt := time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)
	completedAt := authorizedAt.Add(2 * time.Second)
	clock := &timeSequence{values: []time.Time{authorizedAt, completedAt}}
	authorizer := &recordingSensitiveAuthorizer{writeMarkers: true}
	store := NewCredentialStore(database, keys)
	service, err := NewService(successfulCredentialVerifier{}, store, authorizer, ServiceOptions{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("sensitive_authorize").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(activeCredentialQuery)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta(insertCredentialQuery)).
		WithArgs(int64(1), sqlmock.AnyArg(), authorizedAt).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertCredentialAuditQuery)).
		WithArgs([]byte(`{"credentialVersion":1}`), authorizedAt).
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectExec("sensitive_renew").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := service.Replace(context.Background(), "administrator-session", "challenge"); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if authorizer.authorizeCalls != 1 || authorizer.renewCalls != 1 || authorizer.authorizedToken != "administrator-session" {
		t.Fatalf("sensitive calls authorize=%d renew=%d token=%q", authorizer.authorizeCalls, authorizer.renewCalls, authorizer.authorizedToken)
	}
	if authorizer.authorizeTx == nil || authorizer.authorizeTx != authorizer.renewTx {
		t.Fatal("authorization and renewal did not use the same caller-owned transaction")
	}
	if !authorizer.authorizedAt.Equal(authorizedAt) || !authorizer.renewedAt.Equal(completedAt) {
		t.Fatalf("sensitive timestamps authorize=%s renew=%s", authorizer.authorizedAt, authorizer.renewedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveServiceAccountReplacementFailureNeverRenewsAndRenewalFailureRollsBack(t *testing.T) {
	authorizedAt := time.Date(2026, 8, 21, 11, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		name           string
		auditError     error
		renewError     error
		wantRenewCalls int
	}{
		{name: "audit failure", auditError: errors.New("private audit failure"), wantRenewCalls: 0},
		{name: "renewal failure", renewError: errors.New("private renewal failure"), wantRenewCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x62}, 32))
			if err != nil {
				t.Fatal(err)
			}
			authorizer := &recordingSensitiveAuthorizer{writeMarkers: true, renewErr: test.renewError}
			store := NewCredentialStore(database, keys)
			service, err := NewService(successfulCredentialVerifier{}, store, authorizer, ServiceOptions{Now: func() time.Time { return authorizedAt }})
			if err != nil {
				t.Fatal(err)
			}

			mock.ExpectBegin()
			mock.ExpectExec("sensitive_authorize").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta(activeCredentialQuery)).WillReturnError(sql.ErrNoRows)
			mock.ExpectExec(regexp.QuoteMeta(insertCredentialQuery)).WillReturnResult(sqlmock.NewResult(9, 1))
			audit := mock.ExpectExec(regexp.QuoteMeta(insertCredentialAuditQuery))
			if test.auditError != nil {
				audit.WillReturnError(test.auditError)
			} else {
				audit.WillReturnResult(sqlmock.NewResult(10, 1))
			}
			if test.renewError != nil {
				mock.ExpectExec("sensitive_renew").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectRollback()

			if err := service.Replace(context.Background(), "administrator-session", "challenge"); err == nil {
				t.Fatal("Replace() unexpectedly succeeded")
			}
			if authorizer.renewCalls != test.wantRenewCalls {
				t.Fatalf("RenewRecentTOTP calls = %d, want %d", authorizer.renewCalls, test.wantRenewCalls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceReplaceUsesCredentialConsumerContextForTransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	replacer := &contextGuardedCredentialReplacer{database: database}
	service, err := NewService(canceledCredentialVerifier{}, replacer, allowingRecentTOTP{}, ServiceOptions{Now: func() time.Time {
		return time.Date(2026, 8, 21, 11, 20, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectRollback()
	err = service.Replace(context.Background(), "administrator-session", "challenge")
	if !errors.Is(err, identity.ErrChallengeExpired) {
		t.Fatalf("Replace error=%v", err)
	}
	if replacer.sideEffect {
		t.Fatal("canceled credential consumer context allowed replacement side effect")
	}
	if !errors.Is(replacer.cause, identity.ErrChallengeExpired) {
		t.Fatalf("replacement context cause=%v", replacer.cause)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

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

func TestHTTPBiliServiceWrongMethodsRemain405ThroughRealAppComposition(t *testing.T) {
	biliService, err := NewHTTPHandler(&fakeHTTPService{}, testHTTPOptions())
	if err != nil {
		t.Fatal(err)
	}
	broadAdmin := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	})
	handler := hostedapp.New(hostedapp.Dependencies{BiliService: biliService, Admin: broadAdmin})

	for _, route := range []struct{ method, path string }{
		{http.MethodHead, "/api/admin/bili-service/status"},
		{http.MethodPost, "/api/admin/bili-service/status"},
		{http.MethodGet, "/api/admin/bili-service/challenge"},
		{http.MethodHead, "/api/admin/bili-service/challenge"},
		{http.MethodGet, "/api/admin/bili-service/replace"},
		{http.MethodHead, "/api/admin/bili-service/replace"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status=%d body=%q, want 405 from real Bili handler", route.method, route.path, response.Code, response.Body.String())
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

func TestHTTPReplaceRejectsJSONLikeContentTypesBeforeRateLimits(t *testing.T) {
	limiter := &recordingHTTPRateLimiter{}
	service := &fakeHTTPService{}
	options := testHTTPOptions()
	options.Limiter = limiter
	handler, err := NewHTTPHandler(service, options)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bili-service/replace", strings.NewReader(`{"challengeId":"service-challenge"}`))
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/jsonp")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "administrator-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%q, want cheap structural rejection", response.Code, response.Body.String())
	}
	if len(limiter.calls) != 0 || service.requireSessionCalls != 0 || service.replaceCalls != 0 {
		t.Fatalf("rejected structure reached limiter/service: calls=%v session=%d replace=%d", limiter.calls, service.requireSessionCalls, service.replaceCalls)
	}
}

func TestHTTPReplaceLimitsBeforeDecodeAndSession(t *testing.T) {
	for _, test := range []struct {
		name             string
		cookie           string
		body             string
		wantStatus       int
		wantScopes       []identity.LimitScope
		wantSessionCalls int
	}{
		{name: "missing cookie still consumes non-secret scopes", body: `{"challengeId":"service-challenge"}`, wantStatus: http.StatusUnauthorized, wantScopes: []identity.LimitScope{identity.LimitGlobal, identity.LimitPerIP}},
		{name: "malformed JSON consumes all scopes before bounded decode", cookie: "administrator-session", body: `{`, wantStatus: http.StatusBadRequest, wantScopes: []identity.LimitScope{identity.LimitGlobal, identity.LimitPerIP, identity.LimitPerChallenge}},
		{name: "valid body authenticates only after every scope", cookie: "administrator-session", body: `{"challengeId":"service-challenge"}`, wantStatus: http.StatusNoContent, wantScopes: []identity.LimitScope{identity.LimitGlobal, identity.LimitPerIP, identity.LimitPerChallenge}, wantSessionCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			limiter := &recordingHTTPRateLimiter{}
			service := &fakeHTTPService{}
			options := testHTTPOptions()
			options.Limiter = limiter
			handler, err := NewHTTPHandler(service, options)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/admin/bili-service/replace", strings.NewReader(test.body))
			request.Header.Set("Origin", "https://admin.example.test")
			request.Header.Set("X-CSRF-Token", "csrf")
			request.Header.Set("Content-Type", "application/json; charset=utf-8")
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: test.cookie})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%q want=%d", response.Code, response.Body.String(), test.wantStatus)
			}
			if len(limiter.calls) != len(test.wantScopes) {
				t.Fatalf("limiter calls=%v want scopes=%v", limiter.calls, test.wantScopes)
			}
			for index, scope := range test.wantScopes {
				if limiter.calls[index].scope != scope {
					t.Fatalf("limiter call %d=%v want scope=%s", index, limiter.calls[index], scope)
				}
			}
			if service.requireSessionCalls != test.wantSessionCalls {
				t.Fatalf("RequireSession calls=%d want=%d", service.requireSessionCalls, test.wantSessionCalls)
			}
			if test.cookie != "" && len(limiter.calls) == 3 {
				digest := sha256.Sum256([]byte(test.cookie))
				if strings.Contains(limiter.calls[2].key, test.cookie) || !strings.HasSuffix(limiter.calls[2].key, fmt.Sprintf("%x", digest[:])) {
					t.Fatalf("per-cookie limiter key is not a secret-free digest: %q", limiter.calls[2].key)
				}
			}
		})
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

func TestRetryBackoffUsesDeterministicCappedTwentyPercentJitter(t *testing.T) {
	for _, test := range []struct {
		name    string
		attempt int
		sample  float64
		want    time.Duration
	}{
		{name: "first low", attempt: 0, sample: 0, want: 800 * time.Millisecond},
		{name: "first midpoint", attempt: 0, sample: 0.5, want: time.Second},
		{name: "first high", attempt: 0, sample: 1, want: 1200 * time.Millisecond},
		{name: "fifth midpoint", attempt: 4, sample: 0.5, want: 16 * time.Second},
		{name: "negative attempt clamps", attempt: -1, sample: 0.5, want: time.Second},
		{name: "final delay caps", attempt: 20, sample: 1, want: 60 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			backoff := NewRetryBackoff(func() float64 { return test.sample })
			if got := backoff(test.attempt); got != test.want {
				t.Fatalf("backoff(%d)=%v want=%v", test.attempt, got, test.want)
			}
		})
	}
}

func TestHTTPUpstreamParsesRetryAfterHTTPDateFromInjectedClock(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Retry-After", now.Add(37*time.Second).Format(http.TimeFormat))
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{Client: server.Client(), RoomInfoEndpoint: server.URL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upstream.RoomInfo(context.Background(), "12", []byte("SESSDATA=private-cookie"))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("RoomInfo() error=%v", err)
	}
	if retry, ok := RetryAfter(err); !ok || retry != 37*time.Second {
		t.Fatalf("RetryAfter=%v, %v", retry, ok)
	}
}

func TestHTTPUpstreamHTTP200ApplicationRateLimitRetainsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Retry-After", "11")
		_ = json.NewEncoder(response).Encode(map[string]any{"code": -509, "message": "private upstream detail"})
	}))
	defer server.Close()
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{Client: server.Client(), RoomInfoEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upstream.RoomInfo(context.Background(), "12", []byte("SESSDATA=private-cookie"))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("RoomInfo() error=%v", err)
	}
	if retry, ok := RetryAfter(err); !ok || retry != 11*time.Second {
		t.Fatalf("RetryAfter=%v, %v", retry, ok)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("application rate limit leaked response: %v", err)
	}
}

func TestHTTPUpstreamMapsHTTP412ToStableRisk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer server.Close()
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{Client: server.Client(), RoomInfoEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = upstream.RoomInfo(context.Background(), "12", []byte("SESSDATA=private-cookie"))
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("HTTP 412 error=%v", err)
	}
}

func TestBiliApplicationRiskCodesMapToStableRisk(t *testing.T) {
	if !errors.Is(mapBiliApplicationCode(-352), ErrRiskRejected) {
		t.Fatalf("-352 = %v", mapBiliApplicationCode(-352))
	}
	if !errors.Is(mapBiliApplicationCode(1), ErrEgressUnavailable) {
		t.Fatalf("unknown = %v", mapBiliApplicationCode(1))
	}
}

func TestHTTPUpstreamCentralizesHTTP200ApplicationRiskMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{"code": -352, "message": "private upstream detail"})
	}))
	defer server.Close()
	upstream, err := NewHTTPUpstream(HTTPUpstreamOptions{Client: server.Client(), RoomInfoEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Code int `json:"code"`
	}
	err = upstream.getJSON(context.Background(), server.URL, "12", []byte("SESSDATA=private-cookie"), &payload)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("HTTP-200 application risk error=%v", err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "cookie") {
		t.Fatalf("application risk error exposed upstream detail: %v", err)
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

func TestBiliPacketDecoderRecursesThroughBrotliAndZlibPackets(t *testing.T) {
	leaf := encodeDanmakuPacket(danmakuMessageOperation, []byte(`{"cmd":"SEND_GIFT"}`))
	zlibPacket := encodeCompressedDanmakuPacket(t, 2, leaf)
	brotliPacket := encodeCompressedDanmakuPacket(t, 3, zlibPacket)
	bodies, err := decodeDanmakuApplicationBodies(brotliPacket)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || string(bodies[0]) != `{"cmd":"SEND_GIFT"}` {
		t.Fatalf("recursively decoded bodies=%q", bodies)
	}
}

func TestBiliPacketDecoderRejectsFrameBeyondBoundAcrossPackets(t *testing.T) {
	body := bytes.Repeat([]byte("x"), maximumDanmakuPayload/2)
	payload := append(encodeDanmakuPacket(danmakuMessageOperation, body), encodeDanmakuPacket(danmakuMessageOperation, body)...)
	if _, err := decodeDanmakuApplicationBodies(payload); !errors.Is(err, ErrEgressUnavailable) {
		t.Fatalf("oversized multi-packet frame error=%v", err)
	}
}

func TestBiliPacketDecoderRejectsCumulativeRecursiveExpansion(t *testing.T) {
	leaf := encodeDanmakuPacket(danmakuMessageOperation, bytes.Repeat([]byte("x"), maximumDanmakuPayload/3))
	children := make([]byte, 0)
	for range 4 {
		children = append(children, encodeCompressedDanmakuPacket(t, 2, leaf)...)
	}
	payload := encodeCompressedDanmakuPacket(t, 3, children)
	if _, err := decodeDanmakuApplicationBodies(payload); !errors.Is(err, ErrEgressUnavailable) {
		t.Fatalf("cumulative recursive expansion error=%v", err)
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

func TestWebsocketConnectionTerminatesOnCorruptBrotliWithGenericError(t *testing.T) {
	payload := encodeDanmakuPacket(danmakuMessageOperation, []byte("not-brotli-private-detail"))
	binary.BigEndian.PutUint16(payload[6:8], 3)
	read := make(chan socketRead, 1)
	read <- socketRead{kind: websocket.BinaryMessage, payload: payload}
	connection := &websocketConnection{connection: &injectedDanmakuSocket{reads: read, writes: make(chan []byte, 1)}, done: make(chan struct{}), now: time.Now, newTicker: func(time.Duration) danmakuTicker { return &injectedTicker{ticks: make(chan time.Time)} }}
	go connection.forward(func(Event) {})
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("corrupt Brotli packet did not close Done")
	}
	if !errors.Is(connection.Err(), ErrEgressUnavailable) || strings.Contains(connection.Err().Error(), "private") || strings.Contains(connection.Err().Error(), "brotli") {
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

func TestWebsocketConnectionFailureBeforeExplicitCloseRemainsTerminalError(t *testing.T) {
	connection := &websocketConnection{connection: &injectedDanmakuSocket{reads: make(chan socketRead), writes: make(chan []byte, 1)}, done: make(chan struct{}), now: time.Now, newTicker: func(time.Duration) danmakuTicker { return &injectedTicker{ticks: make(chan time.Time)} }}
	connection.fail(ErrEgressUnavailable)
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(connection.Err(), ErrEgressUnavailable) {
		t.Fatalf("failure-first terminal error=%v", connection.Err())
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
	readLimit, readBeforeLimit := socket.readLimitState()
	if readBeforeLimit || readLimit != maximumDanmakuPayload {
		t.Fatalf("socket read limit=%d readBeforeLimit=%v", readLimit, readBeforeLimit)
	}
	auth := <-write
	packets, err := decodeDanmakuPackets(auth)
	if err != nil || len(packets) != 1 || packets[0].operation != danmakuAuthOperation {
		t.Fatalf("auth packet=%#v error=%v", packets, err)
	}
	var body map[string]any
	if err := json.Unmarshal(packets[0].body, &body); err != nil || body["key"] != "room-token" || body["roomid"] != float64(12) || body["uid"] != float64(32249588) || body["buvid"] != "buvid-private" || body["protover"] != float64(3) {
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
	reads           chan socketRead
	writes          chan []byte
	mu              sync.Mutex
	readLimit       int64
	readBeforeLimit bool
}

func (socket *injectedDanmakuSocket) SetReadLimit(limit int64) {
	socket.mu.Lock()
	socket.readLimit = limit
	socket.mu.Unlock()
}

func (socket *injectedDanmakuSocket) readLimitState() (int64, bool) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	return socket.readLimit, socket.readBeforeLimit
}

func (socket *injectedDanmakuSocket) ReadMessage() (int, []byte, error) {
	socket.mu.Lock()
	if socket.readLimit == 0 {
		socket.readBeforeLimit = true
	}
	socket.mu.Unlock()
	value := <-socket.reads
	return value.kind, value.payload, value.err
}
func (socket *injectedDanmakuSocket) WriteMessage(_ int, payload []byte) error {
	socket.writes <- append([]byte(nil), payload...)
	return nil
}
func (*injectedDanmakuSocket) SetReadDeadline(time.Time) error { return nil }
func (*injectedDanmakuSocket) Close() error                    { return nil }

func encodeCompressedDanmakuPacket(t *testing.T, protocol uint16, nested []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	switch protocol {
	case 2:
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write(nested); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	case 3:
		writer := brotli.NewWriter(&compressed)
		if _, err := writer.Write(nested); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported compression protocol %d", protocol)
	}
	packet := encodeDanmakuPacket(danmakuMessageOperation, compressed.Bytes())
	binary.BigEndian.PutUint16(packet[6:8], protocol)
	return packet
}

type injectedTicker struct{ ticks chan time.Time }

func (ticker *injectedTicker) C() <-chan time.Time { return ticker.ticks }
func (*injectedTicker) Stop()                      {}

type fakeHTTPService struct {
	token, challengeID  string
	status              CredentialStatus
	hasStatus           bool
	requireSessionCalls int
	replaceCalls        int
}

type canceledCredentialVerifier struct{}

func (canceledCredentialVerifier) Begin(context.Context) (identity.Challenge, error) {
	return identity.Challenge{}, nil
}

type successfulCredentialVerifier struct{}

func (successfulCredentialVerifier) Begin(context.Context) (identity.Challenge, error) {
	return identity.Challenge{}, nil
}

func (successfulCredentialVerifier) ConsumeCredential(ctx context.Context, _ string, consumer func(context.Context, []byte) error) error {
	return consumer(ctx, []byte("SESSDATA=private"))
}

type timeSequence struct {
	values []time.Time
	index  int
}

func (sequence *timeSequence) Now() time.Time {
	if sequence.index >= len(sequence.values) {
		return sequence.values[len(sequence.values)-1]
	}
	value := sequence.values[sequence.index]
	sequence.index++
	return value
}

type recordingSensitiveAuthorizer struct {
	writeMarkers    bool
	renewErr        error
	authorizeCalls  int
	renewCalls      int
	authorizedToken string
	authorizedAt    time.Time
	renewedAt       time.Time
	authorizeTx     *sql.Tx
	renewTx         *sql.Tx
}

func (authorizer *recordingSensitiveAuthorizer) AuthorizeRecentTOTP(ctx context.Context, transaction *sql.Tx, token string, now time.Time) (security.SensitiveSession, error) {
	authorizer.authorizeCalls++
	authorizer.authorizedToken = token
	authorizer.authorizedAt = now
	authorizer.authorizeTx = transaction
	if authorizer.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_authorize"); err != nil {
			return security.SensitiveSession{}, err
		}
	}
	return security.SensitiveSession{}, nil
}

func (authorizer *recordingSensitiveAuthorizer) RenewRecentTOTP(ctx context.Context, transaction *sql.Tx, session security.SensitiveSession, now time.Time) error {
	authorizer.renewCalls++
	authorizer.renewedAt = now
	authorizer.renewTx = transaction
	if authorizer.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_renew"); err != nil {
			return err
		}
	}
	return authorizer.renewErr
}

func (*recordingSensitiveAuthorizer) RequireSession(context.Context, string) error { return nil }
func (canceledCredentialVerifier) ConsumeCredential(ctx context.Context, _ string, consumer func(context.Context, []byte) error) error {
	consumerContext, cancel := context.WithCancelCause(ctx)
	cancel(identity.ErrChallengeExpired)
	return consumer(consumerContext, []byte("SESSDATA=private"))
}

type contextGuardedCredentialReplacer struct {
	database   *sql.DB
	sideEffect bool
	cause      error
}

func (replacer *contextGuardedCredentialReplacer) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	return replacer.database.BeginTx(ctx, options)
}

func (replacer *contextGuardedCredentialReplacer) Replace(ctx context.Context, _ *sql.Tx, _ []byte, _ time.Time) (Credential, error) {
	select {
	case <-ctx.Done():
		replacer.cause = context.Cause(ctx)
		return Credential{}, replacer.cause
	default:
		replacer.sideEffect = true
		return Credential{}, nil
	}
}

type allowingRecentTOTP struct{}

func (allowingRecentTOTP) RequireRecentTOTP(context.Context, string) error { return nil }
func (allowingRecentTOTP) RequireSession(context.Context, string) error    { return nil }
func (allowingRecentTOTP) AuthorizeRecentTOTP(context.Context, *sql.Tx, string, time.Time) (security.SensitiveSession, error) {
	return security.SensitiveSession{}, nil
}
func (allowingRecentTOTP) RenewRecentTOTP(context.Context, *sql.Tx, security.SensitiveSession, time.Time) error {
	return nil
}

type allowHTTPRequests struct{}

func (allowHTTPRequests) Allow(context.Context, identity.LimitScope, string) bool { return true }

type denyHTTPRequests struct{}

func (denyHTTPRequests) Allow(context.Context, identity.LimitScope, string) bool { return false }

type httpRateLimitCall struct {
	scope identity.LimitScope
	key   string
}
type recordingHTTPRateLimiter struct{ calls []httpRateLimitCall }

func (limiter *recordingHTTPRateLimiter) Allow(_ context.Context, scope identity.LimitScope, key string) bool {
	limiter.calls = append(limiter.calls, httpRateLimitCall{scope: scope, key: key})
	return true
}
func testHTTPOptions() HTTPOptions {
	return HTTPOptions{AllowedOrigin: "https://admin.example.test", CSRFToken: "csrf", Limiter: allowHTTPRequests{}, ClientIP: func(*http.Request) string { return "127.0.0.1" }}
}

func (service *fakeHTTPService) Begin(context.Context) (identity.Challenge, error) {
	return identity.Challenge{ID: "service-challenge", QRImage: "data:image/png;base64,qr", ExpiresAt: time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)}, nil
}
func (service *fakeHTTPService) Replace(_ context.Context, token, challengeID string) error {
	service.replaceCalls++
	service.token, service.challengeID = token, challengeID
	return nil
}
func (service *fakeHTTPService) RequireSession(context.Context, string) error {
	service.requireSessionCalls++
	return nil
}
func (service *fakeHTTPService) Status(context.Context) CredentialStatus {
	if service.hasStatus {
		return service.status
	}
	verifiedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	return CredentialStatus{Version: 1, Health: "healthy", LastVerifiedAt: &verifiedAt}
}
