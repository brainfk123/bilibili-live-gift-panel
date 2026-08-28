package migration

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

const maxMigrationBody = 2 << 20

type migrationHTTPService interface {
	Preview(context.Context, int64, Envelope) (Preview, error)
	Select(context.Context, int64, int64, SelectionCommand) (Preview, error)
	Get(context.Context, int64, int64) (Job, error)
	PreauthorizeApply(context.Context, int64, int64) (Job, error)
	PreauthorizeRollback(context.Context, int64, int64) (Job, error)
	Apply(context.Context, int64, int64, SelectionCommand) (Job, error)
	Cancel(context.Context, int64, int64) (Job, error)
	Rollback(context.Context, int64, int64) (Job, error)
}

type accountProofConsumer interface {
	ConsumeAccountProof(context.Context, string, int64, time.Duration) error
}

// HTTPOptions carries the shared authenticated hosted HTTP policy.
type HTTPOptions struct {
	AllowedOrigin string
	CSRFToken     string
	Limiter       identity.ChallengeLimiter
	ClientIP      identity.ClientIPResolver
	Authenticate  func(http.Handler) http.Handler
	AccountID     func(context.Context) (int64, bool)
}

type HTTPHandler struct {
	service                  migrationHTTPService
	proofConsumer            accountProofConsumer
	allowedOrigin, csrfToken string
	limiter                  identity.ChallengeLimiter
	clientIP                 identity.ClientIPResolver
	authenticate             func(http.Handler) http.Handler
	accountID                func(context.Context) (int64, bool)
	mux                      *http.ServeMux
}

func NewHTTPHandler(service migrationHTTPService, proof accountProofConsumer, options HTTPOptions) (*HTTPHandler, error) {
	if service == nil || proof == nil || options.Limiter == nil || options.ClientIP == nil || options.Authenticate == nil || options.CSRFToken == "" || len(options.CSRFToken) > 512 {
		return nil, ErrInvalidInput
	}
	origin, err := url.Parse(options.AllowedOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrInvalidInput
	}
	if options.AccountID == nil {
		options.AccountID = identity.AccountIDFromContext
	}
	handler := &HTTPHandler{service: service, proofConsumer: proof, allowedOrigin: options.AllowedOrigin, csrfToken: options.CSRFToken, limiter: options.Limiter, clientIP: options.ClientIP, authenticate: options.Authenticate, accountID: options.AccountID, mux: http.NewServeMux()}
	handler.mux.HandleFunc("POST /api/migrations/preview", handler.preview)
	handler.mux.HandleFunc("PUT /api/migrations/{id}/selection", handler.selectUnits)
	handler.mux.HandleFunc("POST /api/migrations/{id}/apply", handler.apply)
	handler.mux.HandleFunc("DELETE /api/migrations/{id}", handler.cancel)
	handler.mux.HandleFunc("POST /api/migrations/{id}/rollback", handler.rollback)
	handler.mux.HandleFunc("GET /api/migrations/{id}", handler.get)
	return handler, nil
}

func (handler *HTTPHandler) selectUnits(response http.ResponseWriter, request *http.Request) {
	var command SelectionCommand
	if !handler.acceptJSONMutation(request) {
		handler.writeRejection(response, request)
		return
	}
	if !handler.allow(request, "migration_selection") {
		writeMigrationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if !decodeMigrationJSON(response, request, &command) {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	jobID, ok := migrationPathID(request)
	if !ok {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.authenticated(response, request, func(accountID int64, request *http.Request) {
		preview, err := handler.service.Select(request.Context(), accountID, jobID, command)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeMigrationJSON(response, http.StatusOK, preview)
	})
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) preview(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		handler.writeRejection(response, request)
		return
	}
	if !handler.allow(request, "migration_preview") {
		writeMigrationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	envelope, _, err := Decode(http.MaxBytesReader(response, request.Body, maxMigrationBody), maxMigrationBody)
	if err != nil {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.authenticated(response, request, func(accountID int64, request *http.Request) {
		preview, err := handler.service.Preview(request.Context(), accountID, envelope)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeMigrationJSON(response, http.StatusCreated, preview)
	})
}

func (handler *HTTPHandler) get(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" || !emptyMigrationBody(response, request) {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "migration_get") {
		writeMigrationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	jobID, ok := migrationPathID(request)
	if !ok {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.authenticated(response, request, func(accountID int64, request *http.Request) {
		job, err := handler.service.Get(request.Context(), accountID, jobID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeMigrationJSON(response, http.StatusOK, job)
	})
}

func (handler *HTTPHandler) apply(response http.ResponseWriter, request *http.Request) {
	var body struct {
		ChallengeID string            `json:"challengeId"`
		Selection   *SelectionCommand `json:"selection"`
	}
	if !handler.acceptJSONMutation(request) {
		handler.writeRejection(response, request)
		return
	}
	if !handler.allow(request, "migration_apply") {
		writeMigrationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if !decodeMigrationJSON(response, request, &body) || body.ChallengeID == "" || len(body.ChallengeID) > 256 || body.Selection == nil {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	jobID, ok := migrationPathID(request)
	if !ok {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.authenticated(response, request, func(accountID int64, request *http.Request) {
		job, err := handler.service.PreauthorizeApply(request.Context(), accountID, jobID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		if job.Status == jobPending || job.Status == jobApplied {
			writeMigrationJSON(response, http.StatusOK, job)
			return
		}
		preview, err := handler.service.Select(request.Context(), accountID, jobID, *body.Selection)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		if !preview.CanConfirm {
			handler.writeServiceError(response, ErrConflict)
			return
		}
		if err := handler.proofConsumer.ConsumeAccountProof(request.Context(), body.ChallengeID, accountID, 15*time.Minute); err != nil {
			handler.writeProofError(response, err)
			return
		}
		job, err = handler.service.Apply(request.Context(), accountID, jobID, *body.Selection)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeMigrationJSON(response, http.StatusOK, job)
	})
}

func (handler *HTTPHandler) cancel(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		handler.writeRejection(response, request)
		return
	}
	if request.URL.RawQuery != "" || !emptyMigrationBody(response, request) {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if !handler.allow(request, "migration_cancel") {
		writeMigrationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	jobID, ok := migrationPathID(request)
	if !ok {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.authenticated(response, request, func(accountID int64, request *http.Request) {
		job, err := handler.service.Cancel(request.Context(), accountID, jobID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeMigrationJSON(response, http.StatusOK, job)
	})
}

func (handler *HTTPHandler) rollback(response http.ResponseWriter, request *http.Request) {
	var body struct {
		ChallengeID string `json:"challengeId"`
	}
	if !handler.acceptJSONMutation(request) {
		handler.writeRejection(response, request)
		return
	}
	if !handler.allow(request, "migration_rollback") {
		writeMigrationError(response, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if !decodeMigrationJSON(response, request, &body) || body.ChallengeID == "" || len(body.ChallengeID) > 256 {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	jobID, ok := migrationPathID(request)
	if !ok {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	handler.authenticated(response, request, func(accountID int64, request *http.Request) {
		job, err := handler.service.PreauthorizeRollback(request.Context(), accountID, jobID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		if job.Status == jobRolledBack {
			writeMigrationJSON(response, http.StatusOK, job)
			return
		}
		if err := handler.proofConsumer.ConsumeAccountProof(request.Context(), body.ChallengeID, accountID, 15*time.Minute); err != nil {
			handler.writeProofError(response, err)
			return
		}
		job, err = handler.service.Rollback(request.Context(), accountID, jobID)
		if err != nil {
			handler.writeServiceError(response, err)
			return
		}
		writeMigrationJSON(response, http.StatusOK, job)
	})
}

func (handler *HTTPHandler) authenticated(response http.ResponseWriter, request *http.Request, next func(int64, *http.Request)) {
	handler.authenticate(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		accountID, ok := handler.accountID(request.Context())
		if !ok || accountID <= 0 {
			writeMigrationError(response, http.StatusUnauthorized, "authentication_required")
			return
		}
		next(accountID, request)
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
func (handler *HTTPHandler) acceptJSONMutation(request *http.Request) bool {
	return handler.acceptMutation(request) && request.URL.RawQuery == "" && migrationJSON(request.Header.Get("Content-Type"))
}
func (handler *HTTPHandler) acceptMutation(request *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(request.Header.Get("Origin")), []byte(handler.allowedOrigin)) == 1 && subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(handler.csrfToken)) == 1
}
func (handler *HTTPHandler) writeRejection(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptMutation(request) {
		writeMigrationError(response, http.StatusForbidden, "request_rejected")
	} else {
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
	}
}
func (handler *HTTPHandler) writeProofError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrVerificationPending):
		writeMigrationError(response, http.StatusAccepted, "verification_pending")
	case errors.Is(err, identity.ErrVerificationUnavailable):
		writeMigrationError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeMigrationError(response, http.StatusUnauthorized, "proof_rejected")
	}
}
func (handler *HTTPHandler) writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeMigrationError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrNotFound):
		writeMigrationError(response, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrExpired):
		writeMigrationError(response, http.StatusGone, "expired")
	case errors.Is(err, ErrConflict):
		writeMigrationError(response, http.StatusConflict, "operation_conflict")
	case errors.Is(err, ErrUnavailable):
		writeMigrationError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	default:
		writeMigrationError(response, http.StatusConflict, "operation_failed")
	}
}
func decodeMigrationJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}
func emptyMigrationBody(response http.ResponseWriter, request *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1))
	return err == nil && len(body) == 0
}
func migrationJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}
func migrationPathID(request *http.Request) (int64, bool) {
	value, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	return value, err == nil && value > 0
}
func writeMigrationError(response http.ResponseWriter, status int, code string) {
	writeMigrationJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
func writeMigrationJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
