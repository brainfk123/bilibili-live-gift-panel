package adminidentity

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

type adminHTTPServicePort interface {
	BeginVerification(context.Context) (identity.Challenge, error)
	CancelVerification(string)
	VerifyLogin(context.Context, string, string) (LoginResult, error)
	VerifyRecentTOTP(context.Context, string, string) error
	SendRecovery(context.Context, string) (RecoveryResult, error)
	CompleteRecovery(context.Context, string, string) (RecoveryCompletionResult, error)
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
	handler.mux.HandleFunc("POST /api/admin/auth/bili/challenges", handler.beginVerification)
	handler.mux.HandleFunc("DELETE /api/admin/auth/bili/challenges/{id}", handler.cancelVerification)
	handler.mux.HandleFunc("POST /api/admin/session", handler.verifyLogin)
	handler.mux.HandleFunc("POST /api/admin/totp", handler.verifyRecentTOTP)
	handler.mux.HandleFunc("POST /api/admin/recovery/archive", handler.sendRecovery)
	handler.mux.HandleFunc("POST /api/admin/recovery/complete", handler.completeRecovery)
	return handler, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) beginVerification(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if len(request.URL.Query()) != 0 || !handler.allow(request, "admin_proof_begin", "") {
		if len(request.URL.Query()) != 0 {
			writeAdminError(response, http.StatusBadRequest, "invalid_request")
		} else {
			writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		}
		return
	}
	challenge, err := handler.service.BeginVerification(request.Context())
	if err != nil {
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeAdminJSON(response, http.StatusCreated, challenge)
}

func (handler *HTTPHandler) cancelVerification(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	challengeID := request.PathValue("id")
	if challengeID == "" || len(challengeID) > 256 || len(request.URL.Query()) != 0 {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.service.CancelVerification(challengeID)
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) verifyLogin(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
		TOTP        string `json:"totp"`
	}
	if !decodeAdminJSON(response, request, &body) || body.ChallengeID == "" || len(body.ChallengeID) > 256 || !validTOTPCode(body.TOTP) {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "admin_login", body.ChallengeID) {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.VerifyLogin(request.Context(), body.ChallengeID, body.TOTP)
	switch {
	case err == nil && result.Token != "" && result.ExpiresAt.After(handler.now()):
		http.SetCookie(response, adminCookie(result.Token, result.ExpiresAt))
		response.WriteHeader(http.StatusNoContent)
	case errors.Is(err, identity.ErrVerificationPending):
		writeAdminError(response, http.StatusAccepted, "verification_pending")
	case errors.Is(err, identity.ErrVerificationUnavailable):
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
	}
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
	if !ok || handler.service.VerifyRecentTOTP(request.Context(), token, body.TOTP) != nil {
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
		writeAdminError(response, http.StatusUnauthorized, "authentication_required")
		return
	}
	result, err := handler.service.SendRecovery(request.Context(), token)
	switch {
	case err == nil && len(result.RecoveryPassword) == 20:
		writeAdminJSON(response, http.StatusOK, result)
	case errors.Is(err, ErrRecentTOTPRequired):
		writeAdminError(response, http.StatusForbidden, "recent_totp_required")
	case errors.Is(err, ErrAuthenticationFailed):
		writeAdminError(response, http.StatusUnauthorized, "authentication_required")
	default:
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (handler *HTTPHandler) completeRecovery(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeAdminError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		ChallengeID  string `json:"challengeId"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if !decodeAdminJSON(response, request, &body) || body.ChallengeID == "" || len(body.ChallengeID) > 256 || body.RecoveryCode == "" || len(body.RecoveryCode) > 256 {
		writeAdminError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "admin_recovery", body.ChallengeID) {
		writeAdminError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.CompleteRecovery(request.Context(), body.ChallengeID, body.RecoveryCode)
	switch {
	case err == nil && result.TOTPURI != "" && len(result.RecoveryPassword) == 20:
		http.SetCookie(response, adminCookie("", time.Unix(1, 0)))
		writeAdminJSON(response, http.StatusOK, result)
	case errors.Is(err, identity.ErrVerificationPending):
		writeAdminError(response, http.StatusAccepted, "verification_pending")
	case errors.Is(err, identity.ErrVerificationUnavailable):
		writeAdminError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeAdminError(response, http.StatusUnauthorized, "authentication_failed")
	}
}

func (handler *HTTPHandler) allow(request *http.Request, operation, challengeID string) bool {
	if !handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) || !handler.limiter.Allow(request.Context(), identity.LimitPerIP, handler.clientIP(request)) {
		return false
	}
	return challengeID == "" || handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, challengeID)
}

func (handler *HTTPHandler) acceptJSONMutation(request *http.Request) bool {
	return handler.acceptMutation(request) && len(request.URL.Query()) == 0 && strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json")
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

func adminSessionToken(request *http.Request) (string, bool) {
	cookie, err := request.Cookie(identity.SiteSessionCookie)
	return cookie.Value, err == nil && cookie.Value != ""
}

func adminCookie(value string, expiresAt time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name: identity.SiteSessionCookie, Value: value, Path: "/", Expires: expiresAt.UTC(),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	if value == "" {
		cookie.MaxAge = -1
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
