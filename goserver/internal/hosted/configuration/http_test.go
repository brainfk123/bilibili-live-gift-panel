package configuration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

func TestConfigurationHTTPRejectsMalformedWriteBeforeAuthentication(t *testing.T) {
	service := &httpService{}
	authCalls := 0
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://admin.example.test", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP,
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
	request := httptest.NewRequest(http.MethodPut, "/api/configuration/state", strings.NewReader(`{"expectedRevision":1}{}`))
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "session-secret"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || authCalls != 0 || service.saveStateCalls != 0 {
		t.Fatalf("status=%d auth=%d save=%d", response.Code, authCalls, service.saveStateCalls)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestConfigurationHTTPStateUsesTrustedContextAccountAndThreeLimits(t *testing.T) {
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(SaveStateCommand{ExpectedRevision: 4, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	service := &httpService{state: State{AccountID: 7, ConfigVersionID: 31, Revision: 5, Runtime: runtime}, version: Version{ID: 31, AccountID: 7, Number: 2, Definition: definition}}
	limiter := &recordingLimiter{}
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://admin.example.test", CSRFToken: "csrf", Limiter: limiter, ClientIP: identity.DirectClientIP,
		Authenticate: passthroughAuthentication,
		AccountID:    func(context.Context) (int64, bool) { return 7, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/configuration/state", strings.NewReader(string(body)))
	request.RemoteAddr = "192.0.2.4:1234"
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "session-secret"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.accountID != 7 || service.stateCommand.ExpectedRevision != 4 {
		t.Fatalf("status=%d account=%d command=%#v", response.Code, service.accountID, service.stateCommand)
	}
	if len(limiter.calls) != 3 || limiter.calls[0].scope != identity.LimitGlobal || limiter.calls[1].scope != identity.LimitPerIP || limiter.calls[2].scope != identity.LimitPerChallenge || strings.Contains(limiter.calls[2].key, "session-secret") {
		t.Fatalf("limit calls=%#v", limiter.calls)
	}
}

func TestConfigurationHTTPRequiresAuthenticatedAccountAndMapsConflict(t *testing.T) {
	service := &httpService{loadErr: ErrRevisionConflict}
	handler, err := NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://admin.example.test", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP,
		Authenticate: passthroughAuthentication,
		AccountID:    func(context.Context) (int64, bool) { return 0, false },
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/configuration", nil))
	if response.Code != http.StatusUnauthorized || service.loadCalls != 0 {
		t.Fatalf("missing account status=%d load=%d", response.Code, service.loadCalls)
	}

	service = &httpService{stateErr: ErrRevisionConflict}
	handler, err = NewHTTPHandler(service, HTTPOptions{
		AllowedOrigin: "https://admin.example.test", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP,
		Authenticate: passthroughAuthentication,
		AccountID:    func(context.Context) (int64, bool) { return 7, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/configuration/state", strings.NewReader(`{"expectedRevision":4,"runtime":{"attributeValues":{},"giftTargetReceived":[],"activities":[],"ruleLimits":{"appliedCounts":{}}}}`))
	request.Header.Set("Origin", "https://admin.example.test")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "session-secret"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || strings.TrimSpace(response.Body.String()) != `{"error":"revision_conflict"}` {
		t.Fatalf("conflict status=%d body=%q", response.Code, response.Body.String())
	}
}

type httpService struct {
	version        Version
	state          State
	loadErr        error
	stateErr       error
	accountID      int64
	loadCalls      int
	saveStateCalls int
	stateCommand   SaveStateCommand
}

func (service *httpService) Load(_ context.Context, accountID int64) (Version, State, error) {
	service.accountID, service.loadCalls = accountID, service.loadCalls+1
	return service.version, service.state, service.loadErr
}
func (service *httpService) SaveDefinition(_ context.Context, accountID int64, _ SaveDefinitionCommand) (Version, State, error) {
	service.accountID = accountID
	return service.version, service.state, nil
}
func (service *httpService) SaveState(_ context.Context, accountID int64, command SaveStateCommand) (State, error) {
	service.accountID, service.saveStateCalls, service.stateCommand = accountID, service.saveStateCalls+1, command
	return service.state, service.stateErr
}
func (service *httpService) SuggestRoom(_ context.Context, accountID int64, _ RoomSuggestionCommand) error {
	service.accountID = accountID
	return nil
}

type limitCall struct {
	scope identity.LimitScope
	key   string
}
type recordingLimiter struct{ calls []limitCall }

func (limiter *recordingLimiter) Allow(_ context.Context, scope identity.LimitScope, key string) bool {
	limiter.calls = append(limiter.calls, limitCall{scope, key})
	return true
}

type allowAllLimiter struct{}

func (allowAllLimiter) Allow(context.Context, identity.LimitScope, string) bool { return true }
func passthroughAuthentication(next http.Handler) http.Handler                  { return next }

var _ configurationHTTPService = (*httpService)(nil)
