package adminidentity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"
)

type adminHTTPServicePort interface {
	BeginEmailLogin(context.Context) (EmailLoginChallenge, error)
	VerifyEmailLogin(context.Context, string, string) (LoginResult, error)
	RequireSession(context.Context, string) error
	Logout(context.Context, string) error
	VerifyRecentTOTP(context.Context, string, string) error
	AuthorizeOperation(context.Context, string, string, security.OperationPurpose, string) (string, error)
	SendRecovery(context.Context, string) (RecoveryResult, error)
	PrepareRecovery(context.Context, string) (RecoveryPreparationResult, error)
	ConfirmHandoff(context.Context, string, string) error
}

type HTTPOptions struct {
	AllowedOrigin string
	CSRFToken     string
	Limiter       identity.ChallengeLimiter
	ClientIP      identity.ClientIPResolver
	Now           func() time.Time
}

type HTTPHandler struct {
	service       adminHTTPServicePort
	allowedOrigin string
	csrfToken     string
	limiter       identity.ChallengeLimiter
	clientIP      identity.ClientIPResolver
	now           func() time.Time
	mux           *http.ServeMux
}

func NewHTTPHandler(service adminHTTPServicePort, options HTTPOptions) (*HTTPHandler, error) {
	if service == nil || options.Limiter == nil || options.ClientIP == nil || options.CSRFToken == "" || len(options.CSRFToken) > 512 {
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
	handler.mux.HandleFunc("POST /api/admin/auth/email/challenges", handler.beginEmailLogin)
	handler.mux.HandleFunc("GET /api/admin/session", handler.getSession)
	handler.mux.HandleFunc("DELETE /api/admin/session", handler.deleteSession)
	handler.mux.HandleFunc("POST /api/admin/session/email", handler.verifyEmailLogin)
	handler.mux.HandleFunc("POST /api/admin/totp", handler.verifyRecentTOTP)
	handler.mux.HandleFunc("POST /api/admin/operation-authorizations", handler.authorizeOperation)
	handler.mux.HandleFunc("POST /api/admin/recovery/archive", handler.sendRecovery)
	handler.mux.HandleFunc("POST /api/admin/recovery/prepare", handler.prepareRecovery)
	handler.mux.HandleFunc("POST /api/admin/recovery/confirm", handler.confirmRecovery)
	return handler, nil
}

func (handler *HTTPHandler) authorizeOperation(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		TOTP    string `json:"totp"`
		Purpose string `json:"purpose"`
		Target  string `json:"target"`
	}
	if !decodeAdminJSON(response, request, &body) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	purpose, validPurpose := security.ParseOperationPurpose(body.Purpose)
	if !validTOTPCode(body.TOTP) || !validPurpose || !security.ValidOperationTarget(body.Target) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := adminSessionToken(request)
	if !ok {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	if !handler.allowSensitive(request, "admin_operation_authorization", token) {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	authorizationToken, err := handler.service.AuthorizeOperation(request.Context(), token, body.TOTP, purpose, body.Target)
	switch {
	case err == nil && authorizationToken != "":
		writeAdminJSON(response, http.StatusCreated, struct {
			AuthorizationToken string `json:"authorizationToken"`
		}{AuthorizationToken: authorizationToken})
	case errors.Is(err, ErrAuthenticationFailed):
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
	default:
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (handler *HTTPHandler) beginEmailLogin(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct{}
	if !decodeAdminJSON(response, request, &body) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allowAdministrator(request, "admin_email_begin") {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	challenge, err := handler.service.BeginEmailLogin(request.Context())
	if err != nil || challenge.ChallengeID == "" || !challenge.ExpiresAt.After(handler.now()) {
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeAdminJSON(response, http.StatusCreated, challenge)
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) getSession(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL.RawQuery != "" || !emptyAdminBody(request) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := adminSessionToken(request)
	if !ok {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	switch err := handler.service.RequireSession(request.Context(), token); {
	case err == nil:
		response.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrAuthenticationFailed):
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
	default:
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (handler *HTTPHandler) deleteSession(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if request.URL.RawQuery != "" || !emptyAdminBody(request) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := adminSessionToken(request)
	if !ok || handler.service.Logout(request.Context(), token) != nil {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	http.SetCookie(response, adminCookie("", time.Unix(1, 0)))
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) verifyEmailLogin(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
		EmailCode   string `json:"emailCode"`
	}
	if !decodeAdminJSON(response, request, &body) || body.ChallengeID == "" || len(body.ChallengeID) > 256 || !validEmailLoginCode(body.EmailCode) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "admin_email_login", body.ChallengeID) || !handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, "admin:1") {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.VerifyEmailLogin(request.Context(), body.ChallengeID, body.EmailCode)
	if errors.Is(err, ErrUnavailable) {
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	if err != nil || result.Token == "" || !result.ExpiresAt.After(handler.now()) {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	http.SetCookie(response, adminCookie(result.Token, result.ExpiresAt))
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) verifyRecentTOTP(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		TOTP string `json:"totp"`
	}
	if !decodeAdminJSON(response, request, &body) || !validTOTPCode(body.TOTP) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := adminSessionToken(request)
	if !ok {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	if !handler.allowSensitive(request, "admin_totp", token) {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if handler.service.VerifyRecentTOTP(request.Context(), token, body.TOTP) != nil {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) sendRecovery(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct{}
	if !decodeAdminJSON(response, request, &body) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := adminSessionToken(request)
	if !ok {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	if !handler.allowSensitive(request, "admin_recovery_archive", token) {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.SendRecovery(request.Context(), token)
	switch {
	case err == nil && len(result.RecoveryPassword) == 20:
		writeAdminJSON(response, http.StatusOK, result)
	case errors.Is(err, ErrRecentTOTPRequired):
		writeAdminError(response, http.StatusForbidden, "recent_totp_required")
	case errors.Is(err, ErrAuthenticationFailed):
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
	default:
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (handler *HTTPHandler) allowSensitive(request *http.Request, operation, sessionToken string) bool {
	if !handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) ||
		!handler.limiter.Allow(request.Context(), identity.LimitPerIP, operation+"\x00"+handler.clientIP(request)) {
		return false
	}
	digest := sha256.Sum256([]byte(sessionToken))
	return handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, fmt.Sprintf("%x", digest[:]))
}

func (handler *HTTPHandler) prepareRecovery(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		RecoveryCode string `json:"recoveryCode"`
	}
	if !decodeAdminJSON(response, request, &body) || body.RecoveryCode == "" || len(body.RecoveryCode) > 256 {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allowAdministrator(request, "admin_recovery_prepare") {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.PrepareRecovery(request.Context(), body.RecoveryCode)
	switch {
	case err == nil && result.TOTPURI != "" && len(result.RecoveryPassword) == 20 && result.HandoffToken != "":
		writeAdminJSON(response, http.StatusOK, result)
	case errors.Is(err, ErrUnavailable):
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
	}
}

func (handler *HTTPHandler) allowAdministrator(request *http.Request, operation string) bool {
	return handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) &&
		handler.limiter.Allow(request.Context(), identity.LimitPerIP, operation+"\x00"+handler.clientIP(request)) &&
		handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, "admin:1")
}

func (handler *HTTPHandler) confirmRecovery(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		HandoffToken string `json:"handoffToken"`
		TOTP         string `json:"totp"`
	}
	if !decodeAdminJSON(response, request, &body) || body.HandoffToken == "" || len(body.HandoffToken) > 512 || !validTOTPCode(body.TOTP) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allowSensitive(request, "admin_recovery_confirm", body.HandoffToken) {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if err := handler.service.ConfirmHandoff(request.Context(), body.HandoffToken, body.TOTP); err != nil {
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	http.SetCookie(response, adminCookie("", time.Unix(1, 0)))
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) allow(request *http.Request, operation, challengeID string) bool {
	if !handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) || !handler.limiter.Allow(request.Context(), identity.LimitPerIP, handler.clientIP(request)) {
		return false
	}
	return challengeID == "" || handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, challengeID)
}

func (handler *HTTPHandler) acceptJSONMutation(request *http.Request) bool {
	return handler.acceptMutation(request) && request.URL.RawQuery == "" && strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json")
}

func (handler *HTTPHandler) acceptMutation(request *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(request.Header.Get("Origin")), []byte(handler.allowedOrigin)) == 1 && subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(handler.csrfToken)) == 1
}

func decodeAdminJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func emptyAdminBody(request *http.Request) bool {
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	var one [1]byte
	read, err := request.Body.Read(one[:])
	return read == 0 && errors.Is(err, io.EOF)
}

func adminSessionToken(request *http.Request) (string, bool) {
	cookie, err := request.Cookie(identity.SiteSessionCookie)
	if err != nil || cookie == nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

func adminCookie(value string, expiresAt time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name: identity.SiteSessionCookie, Value: value, Path: "/", Expires: expiresAt.UTC(),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	if value == "" {
		cookie.MaxAge = -1
	} else {
		cookie.MaxAge = int(DefaultAdministratorSessionTTL / time.Second)
	}
	return cookie
}

func writeAdminError(response http.ResponseWriter, status int, code string) {
	writeAdminJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeAdminJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
