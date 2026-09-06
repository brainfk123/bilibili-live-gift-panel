package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

const (
	latestPath    = "/api/v1/releases/latest"
	changelogPath = "/api/v1/changelog"
	policyPath    = "/api/v1/trust/publisher-policy"
	healthPath    = "/healthz"
)

// ReleaseService supplies routed public release metadata, the publisher policy,
// and the stable-only changelog. Changelog is not a release discovery endpoint.
type ReleaseService interface {
	Latest(context.Context, release.Channel) (release.PublicRelease, error)
	PublisherPolicy(context.Context) ([]byte, error)
	Changelog(context.Context) (service.Document, error)
}

// Logger records a stable failure classification without client response data.
type Logger interface {
	Error(requestID, code string, cause error)
}

type handler struct {
	service   ReleaseService
	router    service.ChannelRouter
	requestID func() (string, error)
	logger    Logger
	metrics   Metrics
}

type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type discardLogger struct{}

func (discardLogger) Error(string, string, error) {}

type VersionBucket = service.VersionBucket

const (
	VersionInvalid = service.VersionInvalid
	Version047     = service.Version047
	Version049     = service.Version049
	Version0410    = service.Version0410
	Version0411    = service.Version0411
	Version0412    = service.Version0412
)

type Outcome string

const (
	OutcomeOK                  Outcome = "ok"
	OutcomeClientInvalid       Outcome = "client_invalid"
	OutcomeLegacyUnavailable   Outcome = "legacy_unavailable"
	OutcomeMethodNotAllowed    Outcome = "method_not_allowed"
	OutcomeReleaseInvalid      Outcome = "release_invalid"
	OutcomeReleaseUnavailable  Outcome = "release_unavailable"
	OutcomeDownloadUnavailable Outcome = "download_unavailable"
	OutcomeEncodingFailed      Outcome = "encoding_failed"
)

type LatencyBucket string

const (
	LatencyUnder100ms LatencyBucket = "lt_100ms"
	LatencyUnder500ms LatencyBucket = "lt_500ms"
	LatencyUnder2s    LatencyBucket = "lt_2s"
	LatencyOver2s     LatencyBucket = "gte_2s"
)

type Observation struct {
	Version VersionBucket
	Channel release.Channel
	Outcome Outcome
	Latency LatencyBucket
}

type Metrics interface {
	Observe(Observation)
}

type discardMetrics struct{}

func (discardMetrics) Observe(Observation) {}

// New creates the exact public update API routes.
func New(releaseService ReleaseService, router service.ChannelRouter, requestID func() string, logger Logger, metrics Metrics) http.Handler {
	if requestID == nil {
		return newHandler(releaseService, router, newRequestID, logger, metrics)
	}
	return newHandler(releaseService, router, func() (string, error) {
		generated := requestID()
		if !validRequestID(generated) {
			return "", errors.New("request ID generator returned an invalid value")
		}
		return generated, nil
	}, logger, metrics)
}

func newHandler(releaseService ReleaseService, router service.ChannelRouter, requestID func() (string, error), logger Logger, metrics Metrics) http.Handler {
	if logger == nil {
		logger = discardLogger{}
	}
	if metrics == nil {
		metrics = discardMetrics{}
	}
	return handler{service: releaseService, router: router, requestID: requestID, logger: logger, metrics: metrics}
}

func (handler handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := request.Header.Get("X-Request-ID")
	if len(request.Header.Values("X-Request-ID")) != 1 || !validRequestID(requestID) {
		var err error
		requestID, err = handler.requestID()
		if err != nil {
			writer.Header().Set("X-Content-Type-Options", "nosniff")
			handler.logger.Error("", "request_id_unavailable", errors.New("request ID generation failed"))
			handler.writeError(writer, request, "", http.StatusServiceUnavailable, "request_id_unavailable", "请求标识暂时不可用")
			return
		}
	}
	writer.Header().Set("X-Request-ID", requestID)
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	switch request.URL.Path {
	case latestPath:
		handler.latest(writer, request, requestID)
	case changelogPath:
		handler.changelog(writer, request, requestID)
	case policyPath:
		handler.publisherPolicy(writer, request, requestID)
	case healthPath:
		handler.health(writer, request, requestID)
	default:
		handler.writeError(writer, request, requestID, http.StatusNotFound, "not_found", "资源不存在")
	}
}

func (handler handler) latest(writer http.ResponseWriter, request *http.Request, requestID string) {
	started := time.Now()
	version := service.VersionBucketForUserAgent(request.Header.Values("User-Agent"))
	channel := release.Channel("")
	outcome := OutcomeOK
	defer func() {
		handler.metrics.Observe(Observation{Version: version, Channel: channel, Outcome: outcome, Latency: latencyBucket(time.Since(started))})
	}()
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Vary", "User-Agent")
	if !getOrHead(writer, request, requestID, handler) {
		outcome = OutcomeMethodNotAllowed
		return
	}
	selected, err := handler.router.Select(request.Context(), request.Header.Values("User-Agent"))
	if err != nil {
		if errors.Is(err, service.ErrClientVersionInvalid) {
			outcome = OutcomeClientInvalid
			handler.writeError(writer, request, requestID, http.StatusBadRequest, "client_version_invalid", "客户端版本不受支持")
			return
		}
		outcome = OutcomeLegacyUnavailable
		handler.writeError(writer, request, requestID, http.StatusServiceUnavailable, "legacy_channel_unavailable", "更新通道暂时不可用")
		return
	}
	channel = selected
	writer.Header().Set("X-Gift-Panel-Update-Channel", string(channel))
	publicRelease, err := handler.service.Latest(request.Context(), channel)
	if err != nil {
		outcome = serviceOutcome(err)
		handler.serviceError(writer, request, requestID, err)
		return
	}
	body, err := json.Marshal(publicRelease)
	if err != nil {
		outcome = OutcomeEncodingFailed
		handler.writeLoggedError(writer, request, requestID, "release_unavailable", errors.New("release encoding failed"))
		return
	}
	handler.writeBody(writer, request, http.StatusOK, "application/json", body)
}

func (handler handler) publisherPolicy(writer http.ResponseWriter, request *http.Request, requestID string) {
	writer.Header().Set("Cache-Control", "private, no-store")
	if !getOrHead(writer, request, requestID, handler) {
		return
	}
	body, err := handler.service.PublisherPolicy(request.Context())
	if err != nil {
		handler.serviceError(writer, request, requestID, err)
		return
	}
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
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
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
		cause := service.ErrReleaseInvalid
		if reason := service.InvalidReason(err); reason != "" {
			cause = fmt.Errorf("reason=%s", reason)
		}
		handler.writeLoggedError(writer, request, requestID, "release_invalid", cause)
	case errors.Is(err, service.ErrDownloadUnavailable):
		handler.writeLoggedError(writer, request, requestID, "download_unavailable", service.ErrDownloadUnavailable)
	default:
		handler.writeLoggedError(writer, request, requestID, "release_unavailable", service.ErrReleaseUnavailable)
	}
}

func serviceOutcome(err error) Outcome {
	switch {
	case errors.Is(err, service.ErrReleaseInvalid):
		return OutcomeReleaseInvalid
	case errors.Is(err, service.ErrDownloadUnavailable):
		return OutcomeDownloadUnavailable
	default:
		return OutcomeReleaseUnavailable
	}
}

func latencyBucket(latency time.Duration) LatencyBucket {
	switch {
	case latency < 100*time.Millisecond:
		return LatencyUnder100ms
	case latency < 500*time.Millisecond:
		return LatencyUnder500ms
	case latency < 2*time.Second:
		return LatencyUnder2s
	default:
		return LatencyOver2s
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
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
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

func newRequestID() (string, error) {
	return requestIDFromReader(rand.Read)
}

func requestIDFromReader(readRandom func([]byte) (int, error)) (string, error) {
	bytes := make([]byte, 16)
	read, err := readRandom(bytes)
	if err != nil {
		return "", err
	}
	if read != len(bytes) {
		return "", errors.New("random source returned too few bytes")
	}
	return hex.EncodeToString(bytes), nil
}
