package migration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

func TestHTTPApplyAuthorizesOwnerBeforeConsumingProof(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed, ExpiresAt: time.Now().Add(time.Hour)}}
	proof := &httpProof{}
	handler, err := NewHTTPHandler(service, proof, HTTPOptions{
		AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP,
		Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/migrations/9/apply", strings.NewReader(`{"challengeId":"recent-proof","keepRoomSuggestion":true}`))
	request.Header.Set("Origin", "https://hosted.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proof.calls != 1 || service.applies != 1 {
		t.Fatalf("status=%d proof=%d applies=%d body=%s", response.Code, proof.calls, service.applies, response.Body.String())
	}
	if service.gets != 1 || service.lastAccountID != 7 || !service.keepRoom {
		t.Fatalf("service preauthorization/apply = %#v", service)
	}
}

func TestHTTPApplyRejectsMalformedBodyBeforeProofConsumption(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed, ExpiresAt: time.Now().Add(time.Hour)}}
	proof := &httpProof{}
	handler, err := NewHTTPHandler(service, proof, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/migrations/9/apply", strings.NewReader(`{"challengeId":"proof","unknown":true}`))
	request.Header.Set("Origin", "https://hosted.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || proof.calls != 0 || service.applies != 0 {
		t.Fatalf("status=%d proof=%d applies=%d", response.Code, proof.calls, service.applies)
	}
}

func TestHTTPApplyRequiresExplicitRoomDecisionBeforeProofConsumption(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed, ExpiresAt: time.Now().Add(time.Hour)}}
	proof := &httpProof{}
	handler, err := NewHTTPHandler(service, proof, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/migrations/9/apply", strings.NewReader(`{"challengeId":"proof"}`))
	request.Header.Set("Origin", "https://hosted.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || proof.calls != 0 || service.applies != 0 {
		t.Fatalf("status=%d proof=%d applies=%d", response.Code, proof.calls, service.applies)
	}
}

func TestHTTPPreviewUsesRawJSONAndRejectsMultipart(t *testing.T) {
	service := &httpMigrationService{preview: Preview{ID: 9, ExpiresAt: time.Now().Add(time.Hour)}}
	handler, err := newMigrationHTTPTestHandler(service, &httpProof{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(validEnvelopeWire())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/preview", string(raw)))
	if response.Code != http.StatusCreated || service.previews != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d previews=%d headers=%v", response.Code, service.previews, response.Header())
	}
	request := migrationRequest(http.MethodPost, "/api/migrations/preview", string(raw))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.previews != 1 {
		t.Fatalf("multipart status=%d previews=%d", response.Code, service.previews)
	}
}

func TestHTTPGetAndCancelRejectBodiesBeforeService(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed, ExpiresAt: time.Now().Add(time.Hour)}}
	handler, err := newMigrationHTTPTestHandler(service, &httpProof{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/migrations/9", nil))
	if response.Code != http.StatusOK || service.gets != 1 {
		t.Fatalf("get status=%d gets=%d", response.Code, service.gets)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/migrations/9", strings.NewReader("x")))
	if response.Code != http.StatusBadRequest || service.gets != 1 {
		t.Fatalf("body get status=%d gets=%d", response.Code, service.gets)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodDelete, "/api/migrations/9", ""))
	if response.Code != http.StatusOK || service.cancels != 1 {
		t.Fatalf("cancel status=%d calls=%d", response.Code, service.cancels)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodDelete, "/api/migrations/9", "x"))
	if response.Code != http.StatusBadRequest || service.cancels != 1 {
		t.Fatalf("body cancel status=%d calls=%d", response.Code, service.cancels)
	}
}

func TestHTTPRollbackPreservesRetryableProofAndSkipsDuplicateProof(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobApplied, ExpiresAt: time.Now().Add(time.Hour)}}
	proof := &httpProof{err: identity.ErrVerificationPending}
	handler, err := newMigrationHTTPTestHandler(service, proof)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/rollback", `{"challengeId":"proof"}`))
	if response.Code != http.StatusAccepted || proof.calls != 1 || service.rollbacks != 0 {
		t.Fatalf("pending status=%d proof=%d rollback=%d", response.Code, proof.calls, service.rollbacks)
	}
	service.job.Status = jobRolledBack
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/rollback", `{"challengeId":"proof"}`))
	if response.Code != http.StatusOK || proof.calls != 1 || service.rollbacks != 0 {
		t.Fatalf("duplicate status=%d proof=%d rollback=%d", response.Code, proof.calls, service.rollbacks)
	}
}

func TestHTTPMutationRejectsQueryOriginAndContentTypeBeforeProof(t *testing.T) {
	for _, test := range []struct {
		name, path, origin, contentType string
		want                            int
	}{
		{name: "query", path: "/api/migrations/9/apply?x=1", origin: "https://hosted.example", contentType: "application/json", want: http.StatusBadRequest},
		{name: "origin", path: "/api/migrations/9/apply", origin: "https://attacker.example", contentType: "application/json", want: http.StatusForbidden},
		{name: "content type", path: "/api/migrations/9/apply", origin: "https://hosted.example", contentType: "text/plain", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed, ExpiresAt: time.Now().Add(time.Hour)}}
			proof := &httpProof{}
			handler, err := newMigrationHTTPTestHandler(service, proof)
			if err != nil {
				t.Fatal(err)
			}
			request := migrationRequest(http.MethodPost, test.path, `{"challengeId":"proof","keepRoomSuggestion":false}`)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || proof.calls != 0 || service.gets != 0 || service.applies != 0 {
				t.Fatalf("status=%d proof=%d get=%d apply=%d", response.Code, proof.calls, service.gets, service.applies)
			}
		})
	}
}

func TestHTTPPreauthorizationRejectsBeforeProofConsumption(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		err    error
		route  string
	}{
		{name: "expired apply", err: ErrExpired, route: "/api/migrations/9/apply"},
		{name: "base drift", err: ErrConflict, route: "/api/migrations/9/apply"},
		{name: "anchor drift", err: ErrConflict, route: "/api/migrations/9/rollback"},
		{name: "open session", err: ErrConflict, route: "/api/migrations/9/rollback"},
		{name: "non owner", err: ErrNotFound, route: "/api/migrations/9/apply"},
		{name: "database unavailable", err: ErrUnavailable, route: "/api/migrations/9/apply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed}, preauthErr: test.err}
			proof := &httpProof{}
			handler, err := newMigrationHTTPTestHandler(service, proof)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"challengeId":"proof","keepRoomSuggestion":false}`
			if strings.HasSuffix(test.route, "/rollback") {
				body = `{"challengeId":"proof"}`
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, migrationRequest(http.MethodPost, test.route, body))
			if proof.calls != 0 || service.applies != 0 || service.rollbacks != 0 {
				t.Fatalf("proof=%d apply=%d rollback=%d", proof.calls, service.applies, service.rollbacks)
			}
		})
	}
}

func TestHTTPRollbackBodyAllowsOnlyChallengeID(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobApplied}}
	proof := &httpProof{}
	handler, err := newMigrationHTTPTestHandler(service, proof)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/rollback", `{"challengeId":"proof","keepRoomSuggestion":false}`))
	if response.Code != http.StatusBadRequest || proof.calls != 0 || service.gets != 0 {
		t.Fatalf("status=%d proof=%d get=%d", response.Code, proof.calls, service.gets)
	}
}

func TestHTTPPreviewDecodesBeforeAuthentication(t *testing.T) {
	service := &httpMigrationService{}
	var authenticated int
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			authenticated++
			next.ServeHTTP(response, request)
		})
	}
	handler, err := NewHTTPHandler(service, &httpProof{}, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: authenticate, AccountID: func(context.Context) (int64, bool) { return 7, true }})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/preview", `{`))
	if response.Code != http.StatusBadRequest || authenticated != 0 || service.previews != 0 {
		t.Fatalf("status=%d auth=%d preview=%d", response.Code, authenticated, service.previews)
	}
}

func TestHTTPPreviewRejectsOversizeAfterAllThreeLimitsBeforeAuthentication(t *testing.T) {
	service := &httpMigrationService{}
	limiter := &countingLimiter{}
	var authenticated int
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			authenticated++
			next.ServeHTTP(response, request)
		})
	}
	handler, err := NewHTTPHandler(service, &httpProof{}, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: limiter, ClientIP: identity.DirectClientIP, Authenticate: authenticate, AccountID: func(context.Context) (int64, bool) { return 7, true }})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/preview", strings.Repeat("x", maxMigrationBody+1)))
	if response.Code != http.StatusBadRequest || limiter.calls != 3 || authenticated != 0 || service.previews != 0 {
		t.Fatalf("status=%d limits=%d auth=%d preview=%d", response.Code, limiter.calls, authenticated, service.previews)
	}
}

func newMigrationHTTPTestHandler(service migrationHTTPService, proof accountProofConsumer) (*HTTPHandler, error) {
	return NewHTTPHandler(service, proof, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true }})
}
func migrationRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Origin", "https://hosted.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	return request
}

type httpMigrationService struct {
	job                     Job
	preview                 Preview
	gets, applies, previews int
	cancels, rollbacks      int
	lastAccountID           int64
	keepRoom                bool
	preauthErr              error
}

func (service *httpMigrationService) Preview(context.Context, int64, Envelope) (Preview, error) {
	service.previews++
	return service.preview, nil
}
func (service *httpMigrationService) Get(_ context.Context, accountID, _ int64) (Job, error) {
	service.gets++
	service.lastAccountID = accountID
	return service.job, nil
}
func (service *httpMigrationService) PreauthorizeApply(_ context.Context, accountID, _ int64) (Job, error) {
	service.gets++
	service.lastAccountID = accountID
	return service.job, service.preauthErr
}
func (service *httpMigrationService) PreauthorizeRollback(_ context.Context, accountID, _ int64) (Job, error) {
	service.gets++
	service.lastAccountID = accountID
	return service.job, service.preauthErr
}
func (service *httpMigrationService) Apply(_ context.Context, accountID, _ int64, keepRoom bool) (Job, error) {
	service.applies++
	service.lastAccountID = accountID
	service.keepRoom = keepRoom
	service.job.Status = jobApplied
	return service.job, nil
}
func (service *httpMigrationService) Cancel(context.Context, int64, int64) (Job, error) {
	service.cancels++
	service.job.Status = jobCancelled
	return service.job, nil
}
func (service *httpMigrationService) Rollback(context.Context, int64, int64) (Job, error) {
	service.rollbacks++
	service.job.Status = jobRolledBack
	return service.job, nil
}

type httpProof struct {
	calls int
	err   error
}

func (proof *httpProof) ConsumeAccountProof(context.Context, string, int64, time.Duration) error {
	proof.calls++
	return proof.err
}

type allowAllLimiter struct{}

func (allowAllLimiter) Allow(context.Context, identity.LimitScope, string) bool { return true }

type countingLimiter struct{ calls int }

func (limiter *countingLimiter) Allow(context.Context, identity.LimitScope, string) bool {
	limiter.calls++
	return true
}
func testAuthenticate() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}
