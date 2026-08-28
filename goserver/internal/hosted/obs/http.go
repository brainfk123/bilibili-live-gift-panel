package obs

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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

	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/obsselector"
	hostedruntime "bilibili-live-gift-panel/internal/hosted/runtime"
)

const (
	OBSSessionCookie = "__Secure-gift_panel_obs"
	maximumOBSBody   = 1024
	obsKeepalive     = 20 * time.Second
	obsWriteTimeout  = 5 * time.Second
)

type httpService interface {
	Issue(context.Context, string, int64) (IssuedCredential, error)
	Exchange(context.Context, string, string) (ShortSession, error)
	Authenticate(context.Context, string, string) (int64, error)
}

type obsRuntime interface {
	Status(context.Context, int64) (hostedruntime.Status, error)
	Snapshot(context.Context, int64) (configuration.RuntimeState, error)
}

type HTTPOptions struct {
	AllowedOrigin string
	CSRFToken     string
	Limiter       identity.ChallengeLimiter
	ClientIP      identity.ClientIPResolver
	Runtime       obsRuntime
	Publisher     *hostedruntime.Publisher
	Now           func() time.Time
	NewTimer      func(time.Duration) hostedruntime.Timer
}

type HTTPHandler struct {
	service       httpService
	allowedOrigin string
	csrfToken     string
	limiter       identity.ChallengeLimiter
	clientIP      identity.ClientIPResolver
	runtime       obsRuntime
	publisher     *hostedruntime.Publisher
	now           func() time.Time
	newTimer      func(time.Duration) hostedruntime.Timer
	mux           *http.ServeMux
}

func NewHTTPHandler(service httpService, options HTTPOptions) (*HTTPHandler, error) {
	if service == nil || options.Limiter == nil || options.ClientIP == nil || options.Runtime == nil || options.Publisher == nil || options.CSRFToken == "" || len(options.CSRFToken) > 512 {
		return nil, ErrInvalidInput
	}
	origin, err := url.Parse(options.AllowedOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewTimer == nil {
		options.NewTimer = func(delay time.Duration) hostedruntime.Timer { return obsSystemTimer{timer: time.NewTimer(delay)} }
	}
	handler := &HTTPHandler{
		service: service, allowedOrigin: options.AllowedOrigin, csrfToken: options.CSRFToken,
		limiter: options.Limiter, clientIP: options.ClientIP, runtime: options.Runtime, publisher: options.Publisher,
		now: options.Now, newTimer: options.NewTimer, mux: http.NewServeMux(),
	}
	// Complete-path patterns intentionally own every method. Each endpoint
	// performs its own exact method check, including HEAD on the SSE route.
	handler.mux.HandleFunc("/api/admin/accounts/{id}/obs-credential", handler.issueCredential)
	handler.mux.HandleFunc("/obs/{publicID}/exchange", handler.exchange)
	handler.mux.HandleFunc("/obs/{publicID}/events", handler.events)
	return handler, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) issueCredential(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeOBSError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if request.URL.RawQuery != "" || !jsonContentType(request) || request.ContentLength > maximumOBSBody {
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.acceptAdminMutation(request) {
		writeOBSError(response, http.StatusForbidden, "request_rejected")
		return
	}
	accountID, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	operation := "obs_credential_issue"
	if !handler.allowPublic(request, operation, strconv.FormatInt(accountID, 10)) {
		writeOBSError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	cookie, err := request.Cookie(identity.SiteSessionCookie)
	if err != nil || cookie.Value == "" {
		writeOBSError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	if !handler.allowSecret(request, operation, cookie.Value) {
		writeOBSError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var body map[string]json.RawMessage
	if !decodeStrictJSON(response, request, &body) || body == nil || len(body) != 0 {
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	issued, err := handler.service.Issue(request.Context(), cookie.Value, accountID)
	if err != nil {
		handler.writeServiceError(response, err)
		return
	}
	writeOBSJSON(response, http.StatusCreated, issued)
}

func (handler *HTTPHandler) exchange(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeOBSError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	publicID := request.PathValue("publicID")
	if !validPublicID(publicID) || request.URL.RawQuery != "" || !jsonContentType(request) || request.ContentLength > maximumOBSBody {
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.acceptOrigin(request) {
		writeOBSError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if !handler.allowPublic(request, "obs_exchange", publicID) {
		writeOBSError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeStrictJSON(response, request, &body) || !validToken(body.Token) {
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	session, err := handler.service.Exchange(request.Context(), publicID, body.Token)
	if err != nil {
		handler.writeServiceError(response, err)
		return
	}
	maxAge := int(time.Until(session.ExpiresAt).Seconds())
	if handler.now != nil {
		maxAge = int(session.ExpiresAt.Sub(handler.now().UTC()).Seconds())
	}
	if maxAge <= 0 || session.Token == "" {
		writeOBSError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name: OBSSessionCookie, Value: session.Token, Path: "/obs/" + publicID,
		Expires: session.ExpiresAt, MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	response.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) events(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		writeOBSError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	publicID := request.PathValue("publicID")
	if !validPublicID(publicID) || !validOBSOutputQuery(request.URL.Query()) || request.ContentLength > 0 {
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allowPublic(request, "obs_events", publicID) {
		writeOBSError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if !emptyOBSBody(response, request) {
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	_, ok := response.(http.Flusher)
	if !ok {
		writeOBSError(response, http.StatusInternalServerError, "stream_unavailable")
		return
	}
	cookie, err := request.Cookie(OBSSessionCookie)
	if err != nil || cookie.Value == "" {
		writeOBSError(response, http.StatusUnauthorized, "authentication_failed")
		return
	}
	accountID, err := handler.service.Authenticate(request.Context(), publicID, cookie.Value)
	if err != nil {
		handler.writeServiceError(response, err)
		return
	}
	subscription, err := handler.publisher.Subscribe(accountID)
	if err != nil {
		writeOBSError(response, http.StatusServiceUnavailable, "stream_unavailable")
		return
	}
	defer subscription.Cancel()
	initial, err := handler.initialSnapshot(request.Context(), accountID)
	if err != nil {
		handler.writeRuntimeError(response, err)
		return
	}
	controller := http.NewResponseController(response)
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Accel-Buffering", "no")
	if handler.writeDisplay(response, controller, request, publicID, cookie.Value, accountID, initial) != nil {
		return
	}
	lastSessionID, lastRevision := initial.LiveSessionID, initial.Revision
	for {
		timer := handler.newTimer(obsKeepalive)
		select {
		case <-request.Context().Done():
			timer.Stop()
			return
		case snapshot, open := <-subscription.Events():
			timer.Stop()
			if !open {
				return
			}
			if currentAccountID, authErr := handler.service.Authenticate(request.Context(), publicID, cookie.Value); authErr != nil || currentAccountID != accountID {
				return
			}
			if latest, ok := handler.publisher.Latest(accountID); ok && displaySnapshotAfter(latest, snapshot) {
				snapshot = latest
			}
			status, statusErr := handler.runtime.Status(request.Context(), accountID)
			if statusErr != nil {
				return
			}
			if snapshot.LiveSessionID != status.SessionID {
				continue
			}
			if snapshot.LiveSessionID < lastSessionID {
				continue
			}
			if snapshot.LiveSessionID == lastSessionID && snapshot.Revision <= lastRevision {
				continue
			}
			if handler.writeDisplay(response, controller, request, publicID, cookie.Value, accountID, snapshot) != nil {
				return
			}
			lastSessionID, lastRevision = snapshot.LiveSessionID, snapshot.Revision
		case <-timer.C():
			timer.Stop()
			if currentAccountID, authErr := handler.service.Authenticate(request.Context(), publicID, cookie.Value); authErr != nil || currentAccountID != accountID {
				return
			}
			status, statusErr := handler.runtime.Status(request.Context(), accountID)
			if statusErr != nil {
				return
			}
			latest, latestOK := handler.publisher.Latest(accountID)
			if latestOK && latest.AccountID == accountID && latest.LiveSessionID == status.SessionID &&
				(status.SessionID != lastSessionID || latest.Revision > lastRevision) {
				if handler.writeDisplay(response, controller, request, publicID, cookie.Value, accountID, latest) != nil {
					return
				}
				lastSessionID, lastRevision = latest.LiveSessionID, latest.Revision
			} else if status.SessionID != lastSessionID {
				reset, resetErr := handler.identityFreeSnapshot(request.Context(), accountID)
				if resetErr != nil {
					return
				}
				if reset.LiveSessionID != lastSessionID {
					if handler.writeDisplay(response, controller, request, publicID, cookie.Value, accountID, reset) != nil {
						return
					}
					lastSessionID, lastRevision = reset.LiveSessionID, reset.Revision
				}
			}
			if handler.writeKeepalive(response, controller, request, publicID, cookie.Value, accountID) != nil {
				return
			}
		}
	}
}

func validOBSOutputQuery(values url.Values) bool {
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	outputs, ok := values["output"]
	if !ok || len(outputs) != 1 {
		return false
	}
	_, err := obsselector.Decode(outputs[0])
	return err == nil
}

func (handler *HTTPHandler) initialSnapshot(ctx context.Context, accountID int64) (hostedruntime.DisplaySnapshot, error) {
	return handler.stableSnapshot(ctx, accountID, true)
}

func (handler *HTTPHandler) identityFreeSnapshot(ctx context.Context, accountID int64) (hostedruntime.DisplaySnapshot, error) {
	return handler.stableSnapshot(ctx, accountID, false)
}

func (handler *HTTPHandler) stableSnapshot(ctx context.Context, accountID int64, includePublished bool) (hostedruntime.DisplaySnapshot, error) {
	const maximumAttempts = 3
	for attempt := 0; attempt < maximumAttempts; attempt++ {
		before, err := handler.runtime.Status(ctx, accountID)
		if err != nil {
			return hostedruntime.DisplaySnapshot{}, err
		}
		initial, ok := hostedruntime.DisplaySnapshot{}, false
		if includePublished {
			initial, ok = handler.publisher.Latest(accountID)
		}
		if !ok || initial.AccountID != accountID || initial.LiveSessionID != before.SessionID {
			runtimeState, snapshotErr := handler.runtime.Snapshot(ctx, accountID)
			if snapshotErr != nil {
				return hostedruntime.DisplaySnapshot{}, snapshotErr
			}
			initial = hostedruntime.DisplaySnapshot{AccountID: accountID, LiveSessionID: before.SessionID, Runtime: runtimeState}
		}
		after, err := handler.runtime.Status(ctx, accountID)
		if err != nil {
			return hostedruntime.DisplaySnapshot{}, err
		}
		if before.SessionID == after.SessionID {
			return initial, nil
		}
	}
	return hostedruntime.DisplaySnapshot{}, hostedruntime.ErrUnavailable
}

func (handler *HTTPHandler) writeDisplay(response http.ResponseWriter, controller *http.ResponseController, request *http.Request, publicID, shortToken string, accountID int64, snapshot hostedruntime.DisplaySnapshot) error {
	if err := handler.authorizeOBSFrame(request, publicID, shortToken, accountID); err != nil {
		return err
	}
	if err := handler.setWriteDeadline(controller); err != nil {
		return err
	}
	if err := writeOBSEvent(response, "display", snapshot); err != nil {
		return err
	}
	return controller.Flush()
}

func (handler *HTTPHandler) writeKeepalive(response io.Writer, controller *http.ResponseController, request *http.Request, publicID, shortToken string, accountID int64) error {
	if err := handler.authorizeOBSFrame(request, publicID, shortToken, accountID); err != nil {
		return err
	}
	if err := handler.setWriteDeadline(controller); err != nil {
		return err
	}
	if _, err := io.WriteString(response, ": keepalive\n\n"); err != nil {
		return err
	}
	return controller.Flush()
}

func (handler *HTTPHandler) authorizeOBSFrame(request *http.Request, publicID, shortToken string, accountID int64) error {
	if request == nil || !validPublicID(publicID) || shortToken == "" || accountID <= 0 {
		return hostedruntime.ErrInvalidInput
	}
	authenticatedAccountID, err := handler.service.Authenticate(request.Context(), publicID, shortToken)
	if err != nil {
		return err
	}
	if authenticatedAccountID != accountID {
		return ErrAuthenticationFailed
	}
	return nil
}

func (handler *HTTPHandler) setWriteDeadline(controller *http.ResponseController) error {
	err := controller.SetWriteDeadline(handler.now().Add(obsWriteTimeout))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func displaySnapshotAfter(candidate, current hostedruntime.DisplaySnapshot) bool {
	return candidate.LiveSessionID > current.LiveSessionID ||
		(candidate.LiveSessionID == current.LiveSessionID && candidate.Revision > current.Revision)
}

func (handler *HTTPHandler) allowPublic(request *http.Request, operation, publicKey string) bool {
	return handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) &&
		handler.limiter.Allow(request.Context(), identity.LimitPerIP, operation+"\x00"+handler.clientIP(request)) &&
		handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, operation+"\x00"+publicKey)
}

func (handler *HTTPHandler) allowSecret(request *http.Request, operation, secret string) bool {
	digest := sha256.Sum256([]byte(secret))
	return handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, operation+"\x00"+hex.EncodeToString(digest[:]))
}

func (handler *HTTPHandler) acceptOrigin(request *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(request.Header.Get("Origin")), []byte(handler.allowedOrigin)) == 1
}

func (handler *HTTPHandler) acceptAdminMutation(request *http.Request) bool {
	return handler.acceptOrigin(request) && subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(handler.csrfToken)) == 1
}

func (handler *HTTPHandler) writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrAuthenticationFailed):
		writeOBSError(response, http.StatusUnauthorized, "authentication_failed")
	case errors.Is(err, ErrRecentTOTPRequired):
		writeOBSError(response, http.StatusForbidden, "recent_totp_required")
	case errors.Is(err, ErrAccountDisabled):
		writeOBSError(response, http.StatusForbidden, "account_disabled")
	default:
		writeOBSError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func (handler *HTTPHandler) writeRuntimeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hostedruntime.ErrAccountDisabled):
		writeOBSError(response, http.StatusForbidden, "account_disabled")
	case errors.Is(err, hostedruntime.ErrInvalidInput), errors.Is(err, hostedruntime.ErrInvalidLease):
		writeOBSError(response, http.StatusBadRequest, "invalid_request")
	default:
		writeOBSError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func jsonContentType(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func decodeStrictJSON(response http.ResponseWriter, request *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maximumOBSBody))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func emptyOBSBody(response http.ResponseWriter, request *http.Request) bool {
	value, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1))
	return err == nil && len(value) == 0
}

func writeOBSEvent(response io.Writer, event string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, payload)
	return err
}

func writeOBSError(response http.ResponseWriter, status int, code string) {
	writeOBSJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeOBSJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

type obsSystemTimer struct{ timer *time.Timer }

func (timer obsSystemTimer) C() <-chan time.Time { return timer.timer.C }
func (timer obsSystemTimer) Stop() bool          { return timer.timer.Stop() }
