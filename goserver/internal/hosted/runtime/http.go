package runtime

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
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/identity"
)

const (
	maximumRuntimeBody = 4096
	runtimeKeepalive   = 20 * time.Second
)

type runtimeHTTPService interface {
	Acquire(context.Context, int64, LeaseKind) (ConnectionLease, error)
	SetRoom(context.Context, int64, string) error
	Status(context.Context, int64) (Status, error)
	Snapshot(context.Context, int64) (configuration.RuntimeState, error)
}

type HTTPOptions struct {
	AllowedOrigin string
	CSRFToken     string
	Limiter       identity.ChallengeLimiter
	ClientIP      identity.ClientIPResolver
	Authenticate  func(http.Handler) http.Handler
	AccountID     func(context.Context) (int64, bool)
	NewTimer      func(time.Duration) Timer
}

type HTTPHandler struct {
	service       runtimeHTTPService
	allowedOrigin string
	csrfToken     string
	limiter       identity.ChallengeLimiter
	clientIP      identity.ClientIPResolver
	authenticate  func(http.Handler) http.Handler
	accountID     func(context.Context) (int64, bool)
	newTimer      func(time.Duration) Timer
	mux           *http.ServeMux
}

func NewHTTPHandler(service runtimeHTTPService, options HTTPOptions) (*HTTPHandler, error) {
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
	if options.NewTimer == nil {
		options.NewTimer = newSystemTimer
	}
	handler := &HTTPHandler{
		service: service, allowedOrigin: options.AllowedOrigin, csrfToken: options.CSRFToken,
		limiter: options.Limiter, clientIP: options.ClientIP, authenticate: options.Authenticate,
		accountID: options.AccountID, newTimer: options.NewTimer, mux: http.NewServeMux(),
	}
	handler.mux.HandleFunc("PUT /api/runtime/room", handler.setRoom)
	handler.mux.HandleFunc("GET /api/runtime/status", handler.status)
	handler.mux.HandleFunc("GET /api/runtime/events", handler.events)
	return handler, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) setRoom(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut || request.URL.Path != "/api/runtime/room" {
		response.Header().Set("Allow", http.MethodPut)
		writeRuntimeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if request.URL.RawQuery != "" {
		writeRuntimeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("Origin")), []byte(handler.allowedOrigin)) != 1 || subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(handler.csrfToken)) != 1 {
		writeRuntimeError(response, http.StatusForbidden, "request_rejected")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") || request.ContentLength > maximumRuntimeBody {
		writeRuntimeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "runtime_room") {
		writeRuntimeError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var body struct {
		RoomID string `json:"roomId"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maximumRuntimeBody))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validRoomID(strings.TrimSpace(body.RoomID)) {
		writeRuntimeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.withAccount(response, request, func(accountID int64) {
		if err := handler.service.SetRoom(request.Context(), accountID, strings.TrimSpace(body.RoomID)); err != nil {
			handler.writeServiceError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
}

func (handler *HTTPHandler) status(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.URL.Path != "/api/runtime/status" {
		response.Header().Set("Allow", http.MethodGet)
		writeRuntimeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !emptyRuntimeRequest(response, request) {
		writeRuntimeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "runtime_status") {
		writeRuntimeError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	handler.withAccount(response, request, func(accountID int64) {
		status, err := handler.service.Status(request.Context(), accountID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeRuntimeJSON(response, http.StatusOK, status)
	})
}

func (handler *HTTPHandler) events(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.URL.Path != "/api/runtime/events" {
		response.Header().Set("Allow", http.MethodGet)
		writeRuntimeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !emptyRuntimeRequest(response, request) {
		writeRuntimeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "runtime_events") {
		writeRuntimeError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeRuntimeError(response, http.StatusInternalServerError, "stream_unavailable")
		return
	}
	handler.withAccount(response, request, func(accountID int64) {
		lease, err := handler.service.Acquire(request.Context(), accountID, LeaseConfig)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		defer lease.Release()
		snapshot, err := handler.service.Snapshot(request.Context(), accountID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		status, err := handler.service.Status(request.Context(), accountID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		// The shared server has a finite write timeout for ordinary JSON
		// requests. This authenticated streaming response owns its lifetime
		// through the request context and injected keepalive timer instead.
		_ = http.NewResponseController(response).SetWriteDeadline(time.Time{})
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Accel-Buffering", "no")
		writeRuntimeEvent(response, "status", status)
		writeRuntimeEvent(response, "snapshot", snapshot)
		writeRuntimeEvent(response, "degraded", struct {
			Degraded bool `json:"degraded"`
		}{Degraded: status.Degraded})
		flusher.Flush()
		for {
			timer := handler.newTimer(runtimeKeepalive)
			select {
			case <-request.Context().Done():
				timer.Stop()
				return
			case <-timer.C():
				timer.Stop()
				_, _ = io.WriteString(response, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	})
}

func (handler *HTTPHandler) withAccount(response http.ResponseWriter, request *http.Request, next func(int64)) {
	handler.authenticate(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		accountID, ok := handler.accountID(request.Context())
		if !ok || accountID <= 0 {
			writeRuntimeError(response, http.StatusUnauthorized, "authentication_required")
			return
		}
		next(accountID)
	})).ServeHTTP(response, request)
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

func emptyRuntimeRequest(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.RawQuery != "" {
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1))
	return err == nil && len(body) == 0
}

func (handler *HTTPHandler) writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrInvalidLease):
		writeRuntimeError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrAccountDisabled):
		writeRuntimeError(response, http.StatusForbidden, "account_disabled")
	case errors.Is(err, ErrClosed):
		writeRuntimeError(response, http.StatusServiceUnavailable, "shutting_down")
	case errors.Is(err, ErrUnavailable):
		writeRuntimeError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeRuntimeError(response, http.StatusConflict, "operation_failed")
	}
}

func writeRuntimeEvent(response io.Writer, event string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, payload)
}

func writeRuntimeError(response http.ResponseWriter, status int, code string) {
	writeRuntimeJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeRuntimeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
