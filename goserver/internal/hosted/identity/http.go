package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const SiteSessionCookie = "__Host-gift_panel_session"

// LimitScope separates the global and source-address rate-limit buckets.
type LimitScope string

const (
	LimitGlobal       LimitScope = "global"
	LimitPerIP        LimitScope = "per_ip"
	LimitPerChallenge LimitScope = "per_challenge"
)

// ChallengeLimiter is injected so deployment can choose an in-process or
// external policy without changing authentication handlers.
type ChallengeLimiter interface {
	Allow(context.Context, LimitScope, string) bool
}

// ClientIPResolver derives the rate-limit identity from the direct peer and,
// when explicitly configured, a trusted reverse-proxy chain.
type ClientIPResolver func(*http.Request) string

type sessionService interface {
	Begin(context.Context) (Challenge, error)
	Poll(context.Context, string) (PollResult, error)
	Cancel(string)
	Login(context.Context, string) (SiteSession, error)
	Logout(context.Context, string) error
	RequireSession(context.Context, string) (Session, error)
}

type accountAdminService interface {
	DisableAccount(context.Context, string, int64, string) (ManagedAccount, error)
	EnableAccount(context.Context, string, int64, string) (ManagedAccount, error)
	RebindVerifiedUID(context.Context, string, int64, string, string) (ManagedAccount, error)
}

type identityHTTPService interface {
	sessionService
	accountAdminService
}

// HTTPOptions contains public deployment policy; CSRFToken is a non-secret
// bootstrap value but is still compared without data-dependent early exits.
type HTTPOptions struct {
	AllowedOrigin string
	CSRFToken     string
	Limiter       ChallengeLimiter
	ClientIP      ClientIPResolver
	Now           func() time.Time
}

// HTTPHandler owns hosted identity, session, and account-management routes.
type HTTPHandler struct {
	service       identityHTTPService
	allowedOrigin string
	csrfToken     string
	limiter       ChallengeLimiter
	clientIP      ClientIPResolver
	now           func() time.Time
	mux           *http.ServeMux
}

// NewHTTPHandler builds the stable hosted identity method-routes.
func NewHTTPHandler(service identityHTTPService, options HTTPOptions) (*HTTPHandler, error) {
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
		service: service, allowedOrigin: options.AllowedOrigin, csrfToken: options.CSRFToken, limiter: options.Limiter,
		clientIP: options.ClientIP,
		now:      options.Now,
		mux:      http.NewServeMux(),
	}
	handler.mux.HandleFunc("POST /api/auth/bili/challenges", handler.beginChallenge)
	handler.mux.HandleFunc("GET /api/auth/bili/challenges/{id}", handler.pollChallenge)
	handler.mux.HandleFunc("DELETE /api/auth/bili/challenges/{id}", handler.cancelChallenge)
	handler.mux.HandleFunc("POST /api/auth/session", handler.createSession)
	handler.mux.HandleFunc("DELETE /api/auth/session", handler.deleteSession)
	handler.mux.Handle("GET /api/auth/session", handler.Authenticate(http.HandlerFunc(handler.getSession)))
	handler.mux.HandleFunc("POST /api/admin/accounts/{id}/disable", handler.disableAccount)
	handler.mux.HandleFunc("POST /api/admin/accounts/{id}/enable", handler.enableAccount)
	handler.mux.HandleFunc("POST /api/admin/accounts/{id}/rebind", handler.rebindAccount)
	return handler, nil
}

func (handler *HTTPHandler) disableAccount(response http.ResponseWriter, request *http.Request) {
	accountID, sessionToken, ok := handler.acceptAccountMutation(response, request, "admin_account_disable")
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeHTTPJSON(response, request, &body) {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	normalizedReason, validReason := normalizeAdministratorReason(body.Reason)
	if !validReason {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := handler.service.DisableAccount(request.Context(), sessionToken, accountID, normalizedReason)
	handler.writeAccountMutation(response, accountID, result, err, AccountStatusDisabled)
}

func (handler *HTTPHandler) enableAccount(response http.ResponseWriter, request *http.Request) {
	accountID, sessionToken, ok := handler.acceptAccountMutation(response, request, "admin_account_enable")
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeHTTPJSON(response, request, &body) {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	normalizedReason, validReason := normalizeAdministratorReason(body.Reason)
	if !validReason {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := handler.service.EnableAccount(request.Context(), sessionToken, accountID, normalizedReason)
	handler.writeAccountMutation(response, accountID, result, err, AccountStatusActive)
}

func (handler *HTTPHandler) rebindAccount(response http.ResponseWriter, request *http.Request) {
	accountID, sessionToken, ok := handler.acceptAccountMutation(response, request, "admin_account_rebind")
	if !ok {
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
		Reason      string `json:"reason"`
	}
	if !decodeHTTPJSON(response, request, &body) || body.ChallengeID == "" || len(body.ChallengeID) > 256 {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	normalizedReason, validReason := normalizeAdministratorReason(body.Reason)
	if !validReason {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := handler.service.RebindVerifiedUID(request.Context(), sessionToken, accountID, body.ChallengeID, normalizedReason)
	handler.writeAccountMutation(response, accountID, result, err, "")
}

func (handler *HTTPHandler) acceptAccountMutation(response http.ResponseWriter, request *http.Request, operation string) (int64, string, bool) {
	if !handler.acceptMutation(request) {
		writeHTTPError(response, http.StatusForbidden, "request_rejected")
		return 0, "", false
	}
	if request.URL.RawQuery != "" || !isJSONContentType(request.Header.Get("Content-Type")) {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return 0, "", false
	}
	rawAccountID := request.PathValue("id")
	accountID, err := strconv.ParseInt(rawAccountID, 10, 64)
	if err != nil || accountID <= 0 || strconv.FormatInt(accountID, 10) != rawAccountID {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return 0, "", false
	}
	cookie, err := request.Cookie(SiteSessionCookie)
	if err != nil || cookie == nil || cookie.Value == "" {
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
		return 0, "", false
	}
	if !handler.allowAccountMutation(request, operation, cookie.Value) {
		writeHTTPError(response, http.StatusTooManyRequests, "rate_limited")
		return 0, "", false
	}
	return accountID, cookie.Value, true
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func (handler *HTTPHandler) allowAccountMutation(request *http.Request, operation, sessionToken string) bool {
	if !handler.limiter.Allow(request.Context(), LimitGlobal, operation) ||
		!handler.limiter.Allow(request.Context(), LimitPerIP, operation+"\x00"+handler.clientIP(request)) {
		return false
	}
	digest := sha256.Sum256([]byte(sessionToken))
	return handler.limiter.Allow(request.Context(), LimitPerChallenge, operation+"\x00"+fmt.Sprintf("%x", digest[:]))
}

func (handler *HTTPHandler) writeAccountMutation(response http.ResponseWriter, accountID int64, result ManagedAccount, err error, requiredStatus string) {
	if err == nil && result.AccountID == accountID && (requiredStatus == "" || result.Status == requiredStatus) && (result.Status == AccountStatusActive || result.Status == AccountStatusDisabled) {
		writeHTTPJSON(response, http.StatusOK, result)
		return
	}
	switch {
	case errors.Is(err, ErrRecentTOTPRequired):
		writeHTTPError(response, http.StatusForbidden, "recent_totp_required")
	case errors.Is(err, ErrVerificationPending):
		writeHTTPError(response, http.StatusAccepted, "verification_pending")
	case errors.Is(err, ErrVerificationUnavailable), errors.Is(err, ErrRepositoryUnavailable):
		writeHTTPError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	case errors.Is(err, ErrInvalidInput):
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrAuthenticationFailed):
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
	default:
		writeHTTPError(response, http.StatusConflict, "operation_failed")
	}
}

func decodeHTTPJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func (handler *HTTPHandler) cancelChallenge(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeHTTPError(response, http.StatusForbidden, "request_rejected")
		return
	}
	challengeID := request.PathValue("id")
	if challengeID == "" || len(challengeID) > 256 || request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.service.Cancel(challengeID)
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) beginChallenge(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeHTTPError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.limiter.Allow(request.Context(), LimitGlobal, "auth_challenge") {
		writeHTTPError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	ip := handler.clientIP(request)
	if !handler.limiter.Allow(request.Context(), LimitPerIP, ip) {
		writeHTTPError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	challenge, err := handler.service.Begin(request.Context())
	if err != nil {
		writeHTTPError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeHTTPJSON(response, http.StatusCreated, struct {
		ChallengeID string    `json:"challengeId"`
		QRImage     string    `json:"qrImage"`
		ExpiresAt   time.Time `json:"expiresAt"`
	}{ChallengeID: challenge.ID, QRImage: challenge.QRImage, ExpiresAt: challenge.ExpiresAt})
}

func (handler *HTTPHandler) pollChallenge(response http.ResponseWriter, request *http.Request) {
	challengeID := request.PathValue("id")
	if challengeID == "" || len(challengeID) > 256 || request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.limiter.Allow(request.Context(), LimitGlobal, "auth_challenge_poll") {
		writeHTTPError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if !handler.limiter.Allow(request.Context(), LimitPerIP, handler.clientIP(request)) {
		writeHTTPError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if !handler.limiter.Allow(request.Context(), LimitPerChallenge, challengeID) {
		writeHTTPError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	result, err := handler.service.Poll(request.Context(), challengeID)
	if err == nil {
		writeHTTPJSON(response, http.StatusOK, result)
		return
	}
	if errors.Is(err, ErrChallengeExpired) {
		writeHTTPJSON(response, http.StatusGone, struct {
			Status string `json:"status"`
		}{Status: "expired"})
		return
	}
	if errors.Is(err, ErrVerificationUnavailable) {
		writeHTTPError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
}

func (handler *HTTPHandler) createSession(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeHTTPError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if request.URL.RawQuery != "" || !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.ChallengeID == "" || len(body.ChallengeID) > 256 {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	session, err := handler.service.Login(request.Context(), body.ChallengeID)
	if err != nil || session.Token == "" || session.AccountID <= 0 || !session.ExpiresAt.After(handler.now()) {
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	http.SetCookie(response, siteCookie(session.Token, session.ExpiresAt))
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) deleteSession(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeHTTPError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	cookie, err := request.Cookie(SiteSessionCookie)
	if err != nil || cookie.Value == "" || handler.service.Logout(request.Context(), cookie.Value) != nil {
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	http.SetCookie(response, siteCookie("", time.Unix(1, 0)))
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) getSession(response http.ResponseWriter, _ *http.Request) {
	writeHTTPJSON(response, http.StatusOK, struct {
		Authenticated bool `json:"authenticated"`
	}{Authenticated: true})
}

type accountContextKey struct{}

// AccountIDFromContext returns only the repository-authenticated tenant ID.
func AccountIDFromContext(ctx context.Context) (int64, bool) {
	accountID, ok := ctx.Value(accountContextKey{}).(int64)
	return accountID, ok && accountID > 0
}

// Authenticate hashes and resolves the host-only Cookie through Service, then
// injects the repository account ID. Request JSON and query parameters are not
// consulted.
func (handler *HTTPHandler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(SiteSessionCookie)
		if err != nil || cookie.Value == "" {
			writeHTTPError(response, http.StatusUnauthorized, "authentication_required")
			return
		}
		session, err := handler.service.RequireSession(request.Context(), cookie.Value)
		if err != nil || session.AccountID <= 0 {
			writeHTTPError(response, http.StatusUnauthorized, "authentication_required")
			return
		}
		ctx := context.WithValue(request.Context(), accountContextKey{}, session.AccountID)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func (handler *HTTPHandler) acceptMutation(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	csrf := request.Header.Get("X-CSRF-Token")
	return subtle.ConstantTimeCompare([]byte(origin), []byte(handler.allowedOrigin)) == 1 &&
		subtle.ConstantTimeCompare([]byte(csrf), []byte(handler.csrfToken)) == 1
}

func siteCookie(value string, expiresAt time.Time) *http.Cookie {
	cookie := &http.Cookie{
		Name: SiteSessionCookie, Value: value, Path: "/", Expires: expiresAt.UTC(),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	if value == "" {
		cookie.MaxAge = -1
	}
	return cookie
}

func clientIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil && host != "" {
		return host
	}
	if parsed := net.ParseIP(remoteAddress); parsed != nil {
		return parsed.String()
	}
	return "unknown"
}

// DirectClientIP ignores forwarding headers and is safe for direct public
// listeners. Reverse-proxy deployments should use a trusted resolver below.
func DirectClientIP(request *http.Request) string {
	return clientIP(request.RemoteAddr)
}

// NewTrustedProxyClientIPResolver walks X-Forwarded-For from the nearest hop
// and accepts it only while every hop closer to this server is trusted. This
// prevents an untrusted direct peer or leftmost spoof entry from choosing a
// rate-limit bucket.
func NewTrustedProxyClientIPResolver(trustedCIDRs []string) (ClientIPResolver, error) {
	if len(trustedCIDRs) == 0 {
		return nil, ErrInvalidInput
	}
	trusted := make([]netip.Prefix, 0, len(trustedCIDRs))
	for _, raw := range trustedCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return nil, ErrInvalidInput
		}
		trusted = append(trusted, prefix.Masked())
	}
	isTrusted := func(address netip.Addr) bool {
		address = address.Unmap()
		for _, prefix := range trusted {
			if prefix.Contains(address) {
				return true
			}
		}
		return false
	}
	return func(request *http.Request) string {
		peer, ok := parseRemoteIP(request.RemoteAddr)
		if !ok {
			return "unknown"
		}
		peer = peer.Unmap()
		if !isTrusted(peer) {
			return peer.String()
		}
		chain := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
		current := peer
		for index := len(chain) - 1; index >= 0; index-- {
			raw := strings.TrimSpace(chain[index])
			if raw == "" || !isTrusted(current) {
				break
			}
			candidate, err := netip.ParseAddr(raw)
			if err != nil {
				break
			}
			current = candidate.Unmap()
		}
		return current.String()
	}, nil
}

func parseRemoteIP(remoteAddress string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	address, err := netip.ParseAddr(host)
	return address, err == nil
}

func writeHTTPError(response http.ResponseWriter, status int, code string) {
	writeHTTPJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeHTTPJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
