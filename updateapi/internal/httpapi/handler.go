package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

const (
	latestPath    = "/api/v1/releases/latest"
	changelogPath = "/api/v1/changelog"
	healthPath    = "/healthz"
)

// ReleaseService supplies public release metadata and the public changelog.
type ReleaseService interface {
	Latest(context.Context) (release.PublicRelease, error)
	Changelog(context.Context) (service.Document, error)
}

// Logger records a stable failure classification without client response data.
type Logger interface {
	Error(requestID, code string, cause error)
}

type handler struct {
	service   ReleaseService
	requestID func() string
	logger    Logger
}

type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type discardLogger struct{}

func (discardLogger) Error(string, string, error) {}

// New creates the exact public update API routes.
func New(service ReleaseService, requestID func() string, logger Logger) http.Handler {
	if requestID == nil {
		requestID = newRequestID
	}
	if logger == nil {
		logger = discardLogger{}
	}
	return handler{service: service, requestID: requestID, logger: logger}
}

func (handler handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := request.Header.Get("X-Request-ID")
	if len(request.Header.Values("X-Request-ID")) != 1 || !validRequestID(requestID) {
		requestID = handler.requestID()
	}
	writer.Header().Set("X-Request-ID", requestID)
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	switch request.URL.Path {
	case latestPath:
		handler.latest(writer, request, requestID)
	case changelogPath:
		handler.changelog(writer, request, requestID)
	case healthPath:
		handler.health(writer, request, requestID)
	default:
		handler.writeError(writer, request, requestID, http.StatusNotFound, "not_found", "资源不存在")
	}
}

func (handler handler) latest(writer http.ResponseWriter, request *http.Request, requestID string) {
	if !getOrHead(writer, request, requestID, handler) {
		return
	}
	release, err := handler.service.Latest(request.Context())
	if err != nil {
		handler.serviceError(writer, request, requestID, err)
		return
	}
	body, err := json.Marshal(release)
	if err != nil {
		handler.writeLoggedError(writer, request, requestID, "release_unavailable", errors.New("release encoding failed"))
		return
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	handler.writeBody(writer, request, http.StatusOK, "application/json", body)
}

func (handler handler) changelog(writer http.ResponseWriter, request *http.Request, requestID string) {
	if !getOrHead(writer, request, requestID, handler) {
		return
	}
	document, err := handler.service.Changelog(request.Context())
	if err != nil {
		handler.serviceError(writer, request, requestID, err)
		return
	}
	writer.Header().Set("Cache-Control", "public, max-age=300")
	if document.ETag != "" {
		writer.Header().Set("ETag", document.ETag)
	}
	handler.writeBody(writer, request, http.StatusOK, "application/json", document.Body)
}

func (handler handler) health(writer http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		handler.writeError(writer, request, requestID, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	handler.writeBody(writer, request, http.StatusOK, "text/plain; charset=utf-8", []byte("ok"))
}

func getOrHead(writer http.ResponseWriter, request *http.Request, requestID string, handler handler) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	writer.Header().Set("Allow", "GET, HEAD")
	handler.writeError(writer, request, requestID, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	return false
}

func (handler handler) serviceError(writer http.ResponseWriter, request *http.Request, requestID string, err error) {
	switch {
	case errors.Is(err, service.ErrReleaseInvalid):
		handler.writeLoggedError(writer, request, requestID, "release_invalid", service.ErrReleaseInvalid)
	case errors.Is(err, service.ErrDownloadUnavailable):
		handler.writeLoggedError(writer, request, requestID, "download_unavailable", service.ErrDownloadUnavailable)
	default:
		handler.writeLoggedError(writer, request, requestID, "release_unavailable", service.ErrReleaseUnavailable)
	}
}

func (handler handler) writeLoggedError(writer http.ResponseWriter, request *http.Request, requestID, code string, cause error) {
	handler.logger.Error(requestID, code, cause)
	handler.writeError(writer, request, requestID, http.StatusServiceUnavailable, code, "更新信息暂时不可用")
}

func (handler handler) writeError(writer http.ResponseWriter, request *http.Request, requestID string, status int, code, message string) {
	body, err := json.Marshal(errorResponse{Code: code, Message: message, RequestID: requestID})
	if err != nil {
		return
	}
	handler.writeBody(writer, request, status, "application/json", body)
}

func (handler handler) writeBody(writer http.ResponseWriter, request *http.Request, status int, contentType string, body []byte) {
	writer.Header().Set("Content-Type", contentType)
	writer.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(body)
	}
}

func validRequestID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func newRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(bytes)
}
