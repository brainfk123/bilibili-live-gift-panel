package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/httpapi"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

const (
	latestPath    = "/api/v1/releases/latest"
	changelogPath = "/api/v1/changelog"
	validRequest  = "0123456789abcdef0123456789abcdef"
	generatedID   = "fedcba9876543210fedcba9876543210"
)

type fakeReleaseService struct {
	latest    release.PublicRelease
	latestErr error
	document  service.Document
	changeErr error
}

func (service *fakeReleaseService) Latest(context.Context) (release.PublicRelease, error) {
	return service.latest, service.latestErr
}

func (service *fakeReleaseService) Changelog(context.Context) (service.Document, error) {
	return service.document, service.changeErr
}

type loggedError struct {
	requestID string
	code      string
	cause     error
}

type captureLogger struct{ entries []loggedError }

func (logger *captureLogger) Error(requestID, code string, cause error) {
	logger.entries = append(logger.entries, loggedError{requestID: requestID, code: code, cause: cause})
}

func TestLatestServesReleaseForGetAndHead(t *testing.T) {
	service := &fakeReleaseService{latest: testRelease()}
	handler := httpapi.New(service, func() string { return generatedID }, &captureLogger{})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			request := httptest.NewRequest(method, latestPath, nil)
			request.Header.Set("X-Request-ID", validRequest)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q, want private no-store", got)
			}
			if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := response.Header().Get("X-Request-ID"); got != validRequest {
				t.Fatalf("X-Request-ID = %q, want inbound ID", got)
			}
			if method == http.MethodHead {
				if got := response.Body.String(); got != "" {
					t.Fatalf("HEAD body = %q, want empty", got)
				}
				return
			}
			if got := response.Body.String(); got != `{"tag_name":"v0.4.4","draft":false,"prerelease":false,"assets":[{"name":"gift-panel-windows-x64.exe","browser_download_url":"https://cos.example.invalid/releases/v0.4.4/gift-panel-windows-x64.exe?signature=secret","size":42,"digest":"sha256:abc"}]}` {
				t.Fatalf("body = %s, want compact public release JSON", got)
			}
		})
	}
}

func TestChangelogServesDocumentForGetAndHead(t *testing.T) {
	service := &fakeReleaseService{document: service.Document{
		Body: []byte(`{"schemaVersion":1,"releases":[{"version":"0.4.4"}]}`),
		ETag: `"changelog-etag"`,
	}}
	handler := httpapi.New(service, func() string { return generatedID }, &captureLogger{})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, changelogPath, nil))

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Header().Get("Cache-Control"); got != "public, max-age=300" {
				t.Fatalf("Cache-Control = %q, want public max-age", got)
			}
			if got := response.Header().Get("ETag"); got != `"changelog-etag"` {
				t.Fatalf("ETag = %q, want document ETag", got)
			}
			if method == http.MethodHead {
				if got := response.Body.String(); got != "" {
					t.Fatalf("HEAD body = %q, want empty", got)
				}
				return
			}
			if got := response.Body.String(); got != `{"schemaVersion":1,"releases":[{"version":"0.4.4"}]}` {
				t.Fatalf("body = %s, want original changelog document", got)
			}
		})
	}
}

func TestHeadResponsesDeclareTheSameContentLengthAsGet(t *testing.T) {
	for _, test := range []struct {
		name    string
		path    string
		service *fakeReleaseService
	}{
		{"latest", latestPath, &fakeReleaseService{latest: testRelease()}},
		{"changelog", changelogPath, &fakeReleaseService{document: service.Document{Body: []byte(`{"schemaVersion":1,"releases":[{"version":"0.4.4"}]}`)}}},
		{"service error", latestPath, &fakeReleaseService{latestErr: service.ErrReleaseUnavailable}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := httpapi.New(test.service, func() string { return generatedID }, &captureLogger{})
			get := httptest.NewRecorder()
			handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, test.path, nil))
			head := httptest.NewRecorder()
			handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, test.path, nil))

			want := strconv.Itoa(get.Body.Len())
			if got := get.Header().Get("Content-Length"); got != want {
				t.Fatalf("GET Content-Length = %q, want %q", got, want)
			}
			if got := head.Header().Get("Content-Length"); got != want {
				t.Fatalf("HEAD Content-Length = %q, want matching GET length %q", got, want)
			}
			if got := head.Body.String(); got != "" {
				t.Fatalf("HEAD body = %q, want empty", got)
			}
		})
	}
}

func TestServiceErrorsAreTypedPrivateAndLoggedWithoutSensitiveData(t *testing.T) {
	privateCause := errors.New("COS key releases/v0.4.4/secret.json and https://cos.example.invalid/?signature=secret")
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{"unavailable", fmtWrapped(service.ErrReleaseUnavailable, privateCause), "release_unavailable"},
		{"invalid", fmtWrapped(service.ErrReleaseInvalid, privateCause), "release_invalid"},
		{"download", fmtWrapped(service.ErrDownloadUnavailable, privateCause), "download_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := &captureLogger{}
			handler := httpapi.New(&fakeReleaseService{latestErr: test.err}, func() string { return generatedID }, logger)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, latestPath+"?token=do-not-reflect", nil))

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", response.Code)
			}
			body := decodeError(t, response)
			if body.Code != test.code || body.RequestID != generatedID {
				t.Fatalf("error = %#v, want code %q and generated request ID", body, test.code)
			}
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "releases/") {
				t.Fatalf("response leaked private cause: %s", response.Body.String())
			}
			if len(logger.entries) != 1 || logger.entries[0].requestID != generatedID || logger.entries[0].code != test.code {
				t.Fatalf("logged entries = %#v, want stable code and request ID", logger.entries)
			}
			if strings.Contains(logger.entries[0].cause.Error(), "secret") || strings.Contains(logger.entries[0].cause.Error(), "releases/") {
				t.Fatalf("logger cause leaked sensitive service detail: %v", logger.entries[0].cause)
			}
		})
	}
}

func TestInvalidManifestLogsOnlyItsSanitizedReasonCode(t *testing.T) {
	logger := &captureLogger{}
	handler := httpapi.New(&fakeReleaseService{latestErr: reasonedReleaseInvalid{cause: errors.New("releases/v0.4.4/private.json?signature=secret")}}, func() string { return generatedID }, logger)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, latestPath, nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if len(logger.entries) != 1 {
		t.Fatalf("logged entries = %#v, want one", logger.entries)
	}
	if got := logger.entries[0].cause.Error(); got != "reason=manifest_tag" {
		t.Fatalf("logged cause = %q, want sanitized reason", got)
	}
}

type reasonedReleaseInvalid struct{ cause error }

func (reasoned reasonedReleaseInvalid) Error() string { return reasoned.cause.Error() }
func (reasoned reasonedReleaseInvalid) Is(target error) bool {
	return target == service.ErrReleaseInvalid
}
func (reasoned reasonedReleaseInvalid) InvalidReason() string { return "manifest_tag" }

func TestUnknownAndUnsupportedRequestsUseStableJSONErrors(t *testing.T) {
	handler := httpapi.New(&fakeReleaseService{}, func() string { return generatedID }, &captureLogger{})

	t.Run("unknown path", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/unrecognized?private=value", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", response.Code)
		}
		if body := decodeError(t, response); body.Code != "not_found" {
			t.Fatalf("error code = %q, want not_found", body.Code)
		}
	})

	t.Run("unsupported method", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, latestPath, nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", response.Code)
		}
		if got := response.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("Allow = %q, want GET, HEAD", got)
		}
		if body := decodeError(t, response); body.Code != "method_not_allowed" {
			t.Fatalf("error code = %q, want method_not_allowed", body.Code)
		}
	})
}

func TestRequestIDAcceptsOnlyLowercaseHex(t *testing.T) {
	handler := httpapi.New(&fakeReleaseService{latest: testRelease()}, func() string { return generatedID }, &captureLogger{})

	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{"valid", validRequest, validRequest},
		{"uppercase", strings.ToUpper(validRequest), generatedID},
		{"wrong length", validRequest[:31], generatedID},
		{"not hexadecimal", "gggggggggggggggggggggggggggggggg", generatedID},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, latestPath, nil)
			request.Header.Set("X-Request-ID", test.input)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if got := response.Header().Get("X-Request-ID"); got != test.want {
				t.Fatalf("X-Request-ID = %q, want %q", got, test.want)
			}
		})
	}

	t.Run("multiple values", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, latestPath, nil)
		request.Header.Add("X-Request-ID", validRequest)
		request.Header.Add("X-Request-ID", validRequest)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if got := response.Header().Get("X-Request-ID"); got != generatedID {
			t.Fatalf("X-Request-ID = %q, want generated ID for multiple inbound values", got)
		}
	})
}

func TestHealthzServesGetAndHead(t *testing.T) {
	handler := httpapi.New(&fakeReleaseService{}, func() string { return generatedID }, &captureLogger{})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "/healthz", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Header().Get("Content-Length"); got != "2" {
				t.Fatalf("Content-Length = %q, want 2", got)
			}
			if method == http.MethodGet && response.Body.String() != "ok" {
				t.Fatalf("GET body = %q, want ok", response.Body.String())
			}
			if method == http.MethodHead && response.Body.String() != "" {
				t.Fatalf("HEAD body = %q, want empty", response.Body.String())
			}
		})
	}
}

func TestHealthzRejectsUnsupportedMethodsWithGetAndHeadAllow(t *testing.T) {
	response := httptest.NewRecorder()
	httpapi.New(&fakeReleaseService{}, func() string { return generatedID }, &captureLogger{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
	if got := response.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q, want GET, HEAD", got)
	}
}

type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func decodeError(t *testing.T, response *httptest.ResponseRecorder) errorResponse {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body
}

func testRelease() release.PublicRelease {
	return release.PublicRelease{TagName: "v0.4.4", Assets: []release.PublicAsset{{
		Name:        "gift-panel-windows-x64.exe",
		DownloadURL: "https://cos.example.invalid/releases/v0.4.4/gift-panel-windows-x64.exe?signature=secret",
		Size:        42,
		Digest:      "sha256:abc",
	}}}
}

func fmtWrapped(category, cause error) error {
	return errors.Join(category, cause)
}
