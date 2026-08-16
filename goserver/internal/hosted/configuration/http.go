package configuration

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

const maxConfigurationBody = 2 << 20

type configurationHTTPService interface {
	Load(context.Context, int64) (Version, State, error)
	SaveDefinition(context.Context, int64, SaveDefinitionCommand) (Version, State, error)
	SaveState(context.Context, int64, SaveStateCommand) (State, error)
	SuggestRoom(context.Context, int64, RoomSuggestionCommand) error
}

// HTTPOptions supplies the shared hosted security policy. Authenticate must
// inject an account ID that AccountID can read; callers never submit one.
type HTTPOptions struct {
	AllowedOrigin string
	CSRFToken     string
	Limiter       identity.ChallengeLimiter
	ClientIP      identity.ClientIPResolver
	Authenticate  func(http.Handler) http.Handler
	AccountID     func(context.Context) (int64, bool)
}

// HTTPHandler exposes only the hosted configuration method-routes.
type HTTPHandler struct {
	service       configurationHTTPService
	allowedOrigin string
	csrfToken     string
	limiter       identity.ChallengeLimiter
	clientIP      identity.ClientIPResolver
	authenticate  func(http.Handler) http.Handler
	accountID     func(context.Context) (int64, bool)
	mux           *http.ServeMux
}

func NewHTTPHandler(service configurationHTTPService, options HTTPOptions) (*HTTPHandler, error) {
	if service == nil || options.Limiter == nil || options.ClientIP == nil || options.Authenticate == nil || options.CSRFToken == "" || len(options.CSRFToken) > 512 {
		return nil, ErrInvalidInput
	}
	origin, err := url.Parse(options.AllowedOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrInvalidInput
	}
	if options.AccountID == nil {
		options.AccountID = identity.AccountIDFromContext
	}
	handler := &HTTPHandler{service: service, allowedOrigin: options.AllowedOrigin, csrfToken: options.CSRFToken, limiter: options.Limiter, clientIP: options.ClientIP, authenticate: options.Authenticate, accountID: options.AccountID, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /api/configuration", handler.load)
	handler.mux.HandleFunc("PUT /api/configuration/definition", handler.saveDefinition)
	handler.mux.HandleFunc("PUT /api/configuration/state", handler.saveState)
	handler.mux.HandleFunc("PUT /api/configuration/room-suggestion", handler.suggestRoom)
	return handler, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) load(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" || !emptyConfigurationBody(response, request) {
		writeConfigurationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "configuration_load") {
		writeConfigurationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	handler.authenticate(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		accountID, ok := handler.accountID(request.Context())
		if !ok || accountID <= 0 {
			writeConfigurationError(response, http.StatusUnauthorized, "authentication_required")
			return
		}
		version, state, err := handler.service.Load(request.Context(), accountID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeConfigurationJSON(response, http.StatusOK, struct {
			Version Version `json:"version"`
			State   State   `json:"state"`
		}{Version: version, State: state})
	})).ServeHTTP(response, request)
}

func (handler *HTTPHandler) saveDefinition(response http.ResponseWriter, request *http.Request) {
	var command SaveDefinitionCommand
	handler.mutate(response, request, "configuration_definition", &command, func(accountID int64) (any, error) {
		version, state, err := handler.service.SaveDefinition(request.Context(), accountID, command)
		return struct {
			Version Version `json:"version"`
			State   State   `json:"state"`
		}{Version: version, State: state}, err
	})
}

func (handler *HTTPHandler) saveState(response http.ResponseWriter, request *http.Request) {
	var command SaveStateCommand
	handler.mutate(response, request, "configuration_state", &command, func(accountID int64) (any, error) {
		return handler.service.SaveState(request.Context(), accountID, command)
	})
}

func (handler *HTTPHandler) suggestRoom(response http.ResponseWriter, request *http.Request) {
	var command RoomSuggestionCommand
	handler.mutate(response, request, "configuration_room_suggestion", &command, func(accountID int64) (any, error) {
		return struct {
			RoomID string `json:"roomId"`
		}{RoomID: command.RoomID}, handler.service.SuggestRoom(request.Context(), accountID, command)
	})
}

func (handler *HTTPHandler) mutate(response http.ResponseWriter, request *http.Request, operation string, body any, execute func(int64) (any, error)) {
	if !handler.acceptJSONMutation(request) {
		handler.writeRejection(response, request)
		return
	}
	if !decodeConfigurationJSON(response, request, body) {
		writeConfigurationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, operation) {
		writeConfigurationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	handler.authenticate(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		accountID, ok := handler.accountID(request.Context())
		if !ok || accountID <= 0 {
			writeConfigurationError(response, http.StatusUnauthorized, "authentication_required")
			return
		}
		result, err := execute(accountID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeConfigurationJSON(response, http.StatusOK, result)
	})).ServeHTTP(response, request)
}

func (handler *HTTPHandler) acceptJSONMutation(request *http.Request) bool {
	return handler.acceptMutation(request) && request.URL.RawQuery == "" && configurationJSON(request.Header.Get("Content-Type"))
}

func (handler *HTTPHandler) acceptMutation(request *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(request.Header.Get("Origin")), []byte(handler.allowedOrigin)) == 1 && subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(handler.csrfToken)) == 1
}

func (handler *HTTPHandler) writeRejection(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeConfigurationError(response, http.StatusForbidden, "request_rejected")
		return
	}
	writeConfigurationError(response, http.StatusBadRequest, "invalid_request")
}

func (handler *HTTPHandler) allow(request *http.Request, operation string) bool {
	if !handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) || !handler.limiter.Allow(request.Context(), identity.LimitPerIP, operation+"\x00"+handler.clientIP(request)) {
		return false
	}
	cookie, _ := request.Cookie(identity.SiteSessionCookie)
	value := ""
	if cookie != nil {
		value = cookie.Value
	}
	digest := sha256.Sum256([]byte(value))
	return handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, operation+"\x00"+fmt.Sprintf("%x", digest[:]))
}

func (handler *HTTPHandler) writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRevisionConflict):
		writeConfigurationError(response, http.StatusConflict, "revision_conflict")
	case errors.Is(err, ErrInvalidInput):
		writeConfigurationError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrNotFound):
		writeConfigurationError(response, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrUnavailable):
		writeConfigurationError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeConfigurationError(response, http.StatusConflict, "operation_failed")
	}
}

func decodeConfigurationJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maxConfigurationBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func emptyConfigurationBody(response http.ResponseWriter, request *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1))
	return err == nil && len(body) == 0
}

func configurationJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func writeConfigurationError(response http.ResponseWriter, status int, code string) {
	writeConfigurationJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeConfigurationJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
