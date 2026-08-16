package invitation

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
	"strconv"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

type invitationHTTPService interface {
	Generate(context.Context, string, ActorKind) (GeneratedInvitation, error)
	List(context.Context, string) (InvitationList, error)
	Revoke(context.Context, string, int64) error
	AdjustQuota(context.Context, string, int64, uint64, string) (Quota, error)
	Redeem(context.Context, string, string) (identity.SiteSession, error)
}

type HTTPOptions struct {
	AllowedOrigin string
	CSRFToken     string
	Limiter       identity.ChallengeLimiter
	ClientIP      identity.ClientIPResolver
	Authenticate  func(http.Handler) http.Handler
	Now           func() time.Time
}

type HTTPHandler struct {
	service       invitationHTTPService
	allowedOrigin string
	csrfToken     string
	limiter       identity.ChallengeLimiter
	clientIP      identity.ClientIPResolver
	now           func() time.Time
	mux           *http.ServeMux
}

func NewHTTPHandler(service invitationHTTPService, options HTTPOptions) (*HTTPHandler, error) {
	if service == nil || options.Limiter == nil || options.ClientIP == nil || options.Authenticate == nil || options.CSRFToken == "" || len(options.CSRFToken) > 512 {
		return nil, ErrInvalidInput
	}
	origin, err := url.Parse(options.AllowedOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	handler := &HTTPHandler{
		service: service, allowedOrigin: options.AllowedOrigin, csrfToken: options.CSRFToken,
		limiter: options.Limiter, clientIP: options.ClientIP, now: options.Now, mux: http.NewServeMux(),
	}
	handler.mux.HandleFunc("POST /api/auth/registration", handler.redeem)
	handler.mux.Handle("GET /api/invitations", options.Authenticate(http.HandlerFunc(handler.list)))
	handler.mux.Handle("POST /api/invitations", options.Authenticate(http.HandlerFunc(handler.generateStreamer)))
	handler.mux.Handle("DELETE /api/invitations/{id}", options.Authenticate(http.HandlerFunc(handler.revoke)))
	handler.mux.HandleFunc("POST /api/admin/invitations", handler.generateAdministrator)
	handler.mux.HandleFunc("POST /api/admin/accounts/{id}/invitation-quota", handler.adjustQuota)
	return handler, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) redeem(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		handler.writeRequestRejection(response, request)
		return
	}
	var body struct {
		Code               string `json:"code"`
		RegistrationIntent string `json:"registrationIntent"`
	}
	if !decodeJSON(response, request, &body) || body.Code == "" || len(body.Code) > 128 || body.RegistrationIntent == "" || len(body.RegistrationIntent) > 512 {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "invitation_redeem", body.Code+"\x00"+body.RegistrationIntent) {
		writeError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	session, err := handler.service.Redeem(request.Context(), body.Code, body.RegistrationIntent)
	if err != nil || session.Token == "" || session.AccountID <= 0 || !session.ExpiresAt.After(handler.now()) {
		handler.writeServiceError(response, err)
		return
	}
	http.SetCookie(response, sessionCookie(session.Token, session.ExpiresAt))
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) list(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" || !hasEmptyBody(response, request) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := sessionToken(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	result, err := handler.service.List(request.Context(), token)
	if err != nil {
		handler.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *HTTPHandler) generateStreamer(response http.ResponseWriter, request *http.Request) {
	handler.generate(response, request, ActorStreamer, "invitation_generate_streamer")
}

func (handler *HTTPHandler) generateAdministrator(response http.ResponseWriter, request *http.Request) {
	handler.generate(response, request, ActorAdministrator, "invitation_generate_admin")
}

func (handler *HTTPHandler) generate(response http.ResponseWriter, request *http.Request, actor ActorKind, operation string) {
	if !handler.acceptJSONMutation(request) {
		handler.writeRequestRejection(response, request)
		return
	}
	var body struct{}
	if !decodeJSON(response, request, &body) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := sessionToken(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	if !handler.allow(request, operation, token) {
		writeError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.Generate(request.Context(), token, actor)
	if err != nil || result.ID <= 0 || result.Code == "" || len(result.CodeHint) != 8 || result.Status != StatusActive || !result.ExpiresAt.After(handler.now()) {
		handler.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, result)
}

func (handler *HTTPHandler) revoke(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if request.URL.RawQuery != "" {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !hasEmptyBody(response, request) {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	invitationID, ok := canonicalPathID(request.PathValue("id"))
	if !ok {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := sessionToken(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	if !handler.allow(request, "invitation_revoke", token) {
		writeError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if err := handler.service.Revoke(request.Context(), token, invitationID); err != nil {
		handler.writeServiceError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) adjustQuota(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		handler.writeRequestRejection(response, request)
		return
	}
	accountID, ok := canonicalPathID(request.PathValue("id"))
	if !ok {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var body struct {
		RemainingQuota uint64 `json:"remainingQuota"`
		Reason         string `json:"reason"`
	}
	if !decodeJSON(response, request, &body) || strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 64 {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := sessionToken(request)
	if !ok {
		writeError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	if !handler.allow(request, "invitation_quota_adjust", token) {
		writeError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.AdjustQuota(request.Context(), token, accountID, body.RemainingQuota, body.Reason)
	if err != nil || result.AccountID != accountID || result.RemainingQuota != body.RemainingQuota {
		handler.writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (handler *HTTPHandler) allow(request *http.Request, operation, secret string) bool {
	if !handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) ||
		!handler.limiter.Allow(request.Context(), identity.LimitPerIP, operation+"\x00"+handler.clientIP(request)) {
		return false
	}
	digest := sha256.Sum256([]byte(secret))
	return handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, operation+"\x00"+fmt.Sprintf("%x", digest[:]))
}

func (handler *HTTPHandler) acceptJSONMutation(request *http.Request) bool {
	return handler.acceptMutation(request) && request.URL.RawQuery == "" && isJSON(request.Header.Get("Content-Type"))
}

func (handler *HTTPHandler) acceptMutation(request *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(request.Header.Get("Origin")), []byte(handler.allowedOrigin)) == 1 &&
		subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(handler.csrfToken)) == 1
}

func (handler *HTTPHandler) writeRequestRejection(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeError(response, http.StatusForbidden, "request_rejected")
		return
	}
	writeError(response, http.StatusBadRequest, "invalid_request")
}

func (handler *HTTPHandler) writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrAuthentication):
		writeError(response, http.StatusUnauthorized, "authentication_failed")
	case errors.Is(err, ErrRecentTOTPRequired):
		writeError(response, http.StatusForbidden, "recent_totp_required")
	case errors.Is(err, ErrQuotaExhausted):
		writeError(response, http.StatusConflict, "quota_exhausted")
	case errors.Is(err, ErrUnavailable):
		writeError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeError(response, http.StatusConflict, "operation_failed")
	}
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func hasEmptyBody(response http.ResponseWriter, request *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1))
	return err == nil && len(body) == 0
}

func isJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func canonicalPathID(raw string) (int64, bool) {
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value > 0 && strconv.FormatInt(value, 10) == raw
}

func sessionToken(request *http.Request) (string, bool) {
	cookie, err := request.Cookie(identity.SiteSessionCookie)
	return cookieValue(cookie), err == nil && cookie != nil && cookie.Value != ""
}

func cookieValue(cookie *http.Cookie) string {
	if cookie == nil {
		return ""
	}
	return cookie.Value
}

func sessionCookie(value string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name: identity.SiteSessionCookie, Value: value, Path: "/", Expires: expiresAt.UTC(),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

func writeError(response http.ResponseWriter, status int, code string) {
	writeJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
