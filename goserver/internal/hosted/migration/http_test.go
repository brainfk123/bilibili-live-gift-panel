package migration

import (
	"context"
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

type httpMigrationService struct {
	job           Job
	gets, applies int
	lastAccountID int64
	keepRoom      bool
}

func (service *httpMigrationService) Preview(context.Context, int64, Envelope) (Preview, error) {
	return Preview{}, ErrUnavailable
}
func (service *httpMigrationService) Get(_ context.Context, accountID, _ int64) (Job, error) {
	service.gets++
	service.lastAccountID = accountID
	return service.job, nil
}
func (service *httpMigrationService) Apply(_ context.Context, accountID, _ int64, keepRoom bool) (Job, error) {
	service.applies++
	service.lastAccountID = accountID
	service.keepRoom = keepRoom
	service.job.Status = jobApplied
	return service.job, nil
}
func (service *httpMigrationService) Cancel(context.Context, int64, int64) (Job, error) {
	return Job{}, ErrUnavailable
}
func (service *httpMigrationService) Rollback(context.Context, int64, int64) (Job, error) {
	return Job{}, ErrUnavailable
}

type httpProof struct{ calls int }

func (proof *httpProof) ConsumeAccountProof(context.Context, string, int64, time.Duration) error {
	proof.calls++
	return nil
}

type allowAllLimiter struct{}

func (allowAllLimiter) Allow(context.Context, identity.LimitScope, string) bool { return true }
func testAuthenticate() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}
