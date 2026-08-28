package migration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/obsselector"
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
	request := httptest.NewRequest(http.MethodPost, "/api/migrations/9/apply", strings.NewReader(`{"challengeId":"recent-proof","selection":{"includeRoomSuggestion":true}}`))
	request.Header.Set("Origin", "https://hosted.example")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proof.calls != 1 || service.applies != 1 {
		t.Fatalf("status=%d proof=%d applies=%d body=%s", response.Code, proof.calls, service.applies, response.Body.String())
	}
	if service.gets != 1 || service.selects != 1 || service.lastAccountID != 7 || !service.selection.IncludeRoomSuggestion {
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

func TestHTTPApplyRequiresExplicitSelectionBeforeProofConsumption(t *testing.T) {
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

func TestHTTPSelectionUpdateIsAccountOwnedReadOnlyAndReturnsBlockingConflict(t *testing.T) {
	service := &httpMigrationService{selectionPreview: Preview{ID: 9, Conflicts: []SelectionConflict{{ID: "attribute:exe"}}, CanConfirm: false}}
	proof := &httpProof{}
	handler, err := newMigrationHTTPTestHandler(service, proof)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPut, "/api/migrations/9/selection", `{"unitIds":["attribute:exe"]}`))
	if response.Code != http.StatusOK || service.selects != 1 || service.lastAccountID != 7 || service.applies != 0 || proof.calls != 0 {
		t.Fatalf("status=%d selects=%d account=%d applies=%d proof=%d body=%s", response.Code, service.selects, service.lastAccountID, service.applies, proof.calls, response.Body.String())
	}
}

func TestHTTPComposeApplyRejectsUnresolvedConflictBeforeProofConsumption(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed}, selectionPreview: Preview{ID: 9, Conflicts: []SelectionConflict{{ID: "attribute:exe"}}, CanConfirm: false}}
	proof := &httpProof{}
	handler, err := newMigrationHTTPTestHandler(service, proof)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/apply", `{"challengeId":"proof","selection":{"unitIds":["attribute:exe"]}}`))
	if response.Code != http.StatusConflict || service.selects != 1 || service.gets != 1 || proof.calls != 0 || service.applies != 0 {
		t.Fatalf("status=%d selects=%d gets=%d proof=%d applies=%d", response.Code, service.selects, service.gets, proof.calls, service.applies)
	}
}

func TestHTTPComposeApplyPendingRetrySkipsSelectionAndProof(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPending}}
	proof := &httpProof{}
	handler, err := newMigrationHTTPTestHandler(service, proof)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/apply", `{"challengeId":"proof","selection":{"unitIds":["stale-client-value"]}}`))
	if response.Code != http.StatusOK || service.gets != 1 || service.selects != 0 || proof.calls != 0 || service.applies != 0 {
		t.Fatalf("status=%d gets=%d selects=%d proof=%d applies=%d", response.Code, service.gets, service.selects, proof.calls, service.applies)
	}
}

func TestHTTPHistoryIsAuthenticatedBoundedAndPrivacySafe(t *testing.T) {
	created := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	applied := created.Add(time.Minute)
	expires := created.Add(24 * time.Hour)
	rollback := applied.Add(7 * 24 * time.Hour)
	service := &httpMigrationService{history: []HistoryJob{{ID: 9, Status: jobPending, CreatedAt: created, ExpiresAt: &expires}, {ID: 8, Status: jobApplied, CreatedAt: created.Add(-time.Hour), AppliedAt: &applied, RollbackExpiresAt: &rollback}}}
	handler, err := newMigrationHTTPTestHandler(service, &httpProof{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/migrations", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.histories != 1 || service.lastAccountID != 7 {
		t.Fatalf("status=%d histories=%d account=%d body=%s", response.Code, service.histories, service.lastAccountID, response.Body.String())
	}
	if got := response.Body.String(); strings.Contains(got, "accountId") || strings.Contains(got, "definition") || strings.Contains(got, "runtime") || strings.Contains(got, "hash") || strings.Contains(got, "token") {
		t.Fatalf("history leaked private fields: %s", got)
	}
}

func TestHTTPAppliedMigrationIssuesRealOutputLinksAndProofGatesReissue(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed}, selectionPreview: Preview{ID: 9, CanConfirm: true}, obsOutputs: []OBSOutput{{Selector: obsselector.Selector{Kind: "attribute", ID: "score"}, Name: "积分"}}}
	proof := &httpProof{}
	issued := 0
	handler, err := NewHTTPHandler(service, proof, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true }, IssueOBS: func(context.Context, int64) (string, error) {
		issued++
		return "https://hosted.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA#token=secret", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	apply := migrationRequest(http.MethodPost, "/api/migrations/9/apply", `{"challengeId":"proof","selection":{"unitIds":[],"includeGeneralSettings":false,"includeRoomSuggestion":false}}`)
	applyResponse := httptest.NewRecorder()
	handler.ServeHTTP(applyResponse, apply)
	if applyResponse.Code != http.StatusOK || !strings.Contains(applyResponse.Body.String(), `"url":"https://hosted.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiJzY29yZSJ9#token=secret"`) || !strings.Contains(applyResponse.Body.String(), `"outputId":"eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiJzY29yZSJ9"`) || issued != 1 || proof.calls != 1 {
		t.Fatalf("apply status=%d issued=%d proof=%d body=%s", applyResponse.Code, issued, proof.calls, applyResponse.Body.String())
	}
	reissue := migrationRequest(http.MethodPost, "/api/migrations/9/obs-links", `{"challengeId":"proof-2"}`)
	reissueResponse := httptest.NewRecorder()
	handler.ServeHTTP(reissueResponse, reissue)
	if reissueResponse.Code != http.StatusOK || issued != 2 || proof.calls != 2 {
		t.Fatalf("reissue status=%d issued=%d proof=%d body=%s", reissueResponse.Code, issued, proof.calls, reissueResponse.Body.String())
	}
	proof.err = identity.ErrVerificationPending
	blockedResponse := httptest.NewRecorder()
	handler.ServeHTTP(blockedResponse, migrationRequest(http.MethodPost, "/api/migrations/9/obs-links", `{"challengeId":"unverified-proof"}`))
	if blockedResponse.Code != http.StatusAccepted || issued != 2 || proof.calls != 3 {
		t.Fatalf("unverified reissue status=%d issued=%d proof=%d body=%s", blockedResponse.Code, issued, proof.calls, blockedResponse.Body.String())
	}
}

func TestHTTPAppliedMigrationRejectsCrossOriginOBSBaseWithoutUndoingApply(t *testing.T) {
	service := &httpMigrationService{job: Job{ID: 9, Status: jobPreviewed}, selectionPreview: Preview{ID: 9, CanConfirm: true}, obsOutputs: []OBSOutput{{Selector: obsselector.Selector{Kind: "attribute", ID: "score"}, Name: "积分"}}}
	handler, err := NewHTTPHandler(service, &httpProof{}, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true }, IssueOBS: func(context.Context, int64) (string, error) {
		return "https://attacker.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA#token=secret", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/apply", `{"challengeId":"proof","selection":{"unitIds":[],"includeGeneralSettings":false,"includeRoomSuggestion":false}}`))
	if response.Code != http.StatusOK || service.job.Status != jobApplied || !strings.Contains(response.Body.String(), `"obsReissueRequired":true`) || strings.Contains(response.Body.String(), "attacker.example") {
		t.Fatalf("status=%d job=%#v body=%s", response.Code, service.job, response.Body.String())
	}
}

func TestHTTPValidatesCompleteOBSOutputSetBeforeCredentialRotation(t *testing.T) {
	service := &httpMigrationService{
		job: Job{ID: 9, Status: jobPreviewed}, selectionPreview: Preview{ID: 9, CanConfirm: true},
		obsOutputs: []OBSOutput{
			{Selector: obsselector.Selector{Kind: "attribute", ID: "合法:积分🔥"}, Name: "合法输出"},
			{Selector: obsselector.Selector{Kind: "scene", ID: "重复", Attributes: []string{"积分", "积分"}}, Name: "非法场景"},
		},
	}
	issued := 0
	handler, err := NewHTTPHandler(service, &httpProof{}, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true }, IssueOBS: func(context.Context, int64) (string, error) {
		issued++
		return "https://hosted.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA#token=secret", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/apply", `{"challengeId":"proof","selection":{"unitIds":[],"includeGeneralSettings":false,"includeRoomSuggestion":false}}`))
	if response.Code != http.StatusOK || service.job.Status != jobApplied || issued != 0 || !strings.Contains(response.Body.String(), `"obsReissueRequired":true`) || strings.Contains(response.Body.String(), "obsLinks") {
		t.Fatalf("status=%d job=%#v issued=%d body=%s", response.Code, service.job, issued, response.Body.String())
	}
}

func TestHTTPOversizeOBSOutputDoesNotRotateCredential(t *testing.T) {
	attributes := make([]string, 200)
	for index := range attributes {
		attributes[index] = strconv.Itoa(index) + strings.Repeat("界", 100)
	}
	service := &httpMigrationService{
		job: Job{ID: 9, Status: jobPreviewed}, selectionPreview: Preview{ID: 9, CanConfirm: true},
		obsOutputs: []OBSOutput{{Selector: obsselector.Selector{Kind: "scene", ID: "二百属性场景", Attributes: attributes}, Name: "过长输出"}},
	}
	issued := 0
	handler, err := NewHTTPHandler(service, &httpProof{}, HTTPOptions{AllowedOrigin: "https://hosted.example", CSRFToken: "csrf", Limiter: allowAllLimiter{}, ClientIP: identity.DirectClientIP, Authenticate: testAuthenticate(), AccountID: func(context.Context) (int64, bool) { return 7, true }, IssueOBS: func(context.Context, int64) (string, error) {
		issued++
		return "https://hosted.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA#token=secret", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, migrationRequest(http.MethodPost, "/api/migrations/9/apply", `{"challengeId":"proof","selection":{"unitIds":[],"includeGeneralSettings":false,"includeRoomSuggestion":false}}`))
	if response.Code != http.StatusOK || issued != 0 || !strings.Contains(response.Body.String(), `"obsReissueRequired":true`) {
		t.Fatalf("status=%d issued=%d body=%s", response.Code, issued, response.Body.String())
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
			request := migrationRequest(http.MethodPost, test.path, `{"challengeId":"proof","selection":{}}`)
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
			body := `{"challengeId":"proof","selection":{}}`
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
	selectionPreview        Preview
	gets, applies, previews int
	selects                 int
	cancels, rollbacks      int
	lastAccountID           int64
	selection               SelectionCommand
	preauthErr              error
	history                 []HistoryJob
	histories               int
	obsOutputs              []OBSOutput
}

func (service *httpMigrationService) History(_ context.Context, accountID int64) ([]HistoryJob, error) {
	service.histories++
	service.lastAccountID = accountID
	return service.history, nil
}
func (service *httpMigrationService) OBSOutputs(_ context.Context, accountID, _ int64) ([]OBSOutput, error) {
	service.lastAccountID = accountID
	return service.obsOutputs, nil
}

func (service *httpMigrationService) Preview(context.Context, int64, Envelope) (Preview, error) {
	service.previews++
	return service.preview, nil
}
func (service *httpMigrationService) Select(_ context.Context, accountID, _ int64, selection SelectionCommand) (Preview, error) {
	service.selects++
	service.lastAccountID = accountID
	service.selection = selection
	if service.selectionPreview.ID != 0 || len(service.selectionPreview.Conflicts) != 0 {
		return service.selectionPreview, nil
	}
	return Preview{ID: 9, CanConfirm: true}, nil
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
func (service *httpMigrationService) Apply(_ context.Context, accountID, _ int64, selection SelectionCommand) (Job, error) {
	service.applies++
	service.lastAccountID = accountID
	service.selection = selection
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
