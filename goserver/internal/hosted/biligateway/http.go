package biligateway

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/identity"

	"github.com/gorilla/websocket"
)

var (
	ErrRecentTOTPRequired   = errors.New("recent_totp_required")
	ErrAuthenticationFailed = errors.New("authentication_failed")
)

type credentialVerifier interface {
	Begin(context.Context) (identity.Challenge, error)
	ConsumeCredential(context.Context, string, func([]byte) error) error
}

type credentialReplacer interface {
	Replace(context.Context, []byte) (Credential, error)
}
type credentialStatusReader interface {
	Status(context.Context) CredentialStatus
}
type recentTOTPAuthorizer interface {
	RequireRecentTOTP(context.Context, string) error
	RequireSession(context.Context, string) error
}

// Service composes the already-authenticated administrator session with the
// QR adapter. It intentionally accepts no Cookie from HTTP JSON.
type Service struct {
	verifier    credentialVerifier
	credentials credentialReplacer
	admin       recentTOTPAuthorizer
}

func NewService(verifier credentialVerifier, credentials credentialReplacer, admin recentTOTPAuthorizer) (*Service, error) {
	if verifier == nil || credentials == nil || admin == nil {
		return nil, errors.New("invalid_bili_service")
	}
	return &Service{verifier: verifier, credentials: credentials, admin: admin}, nil
}

func (service *Service) Begin(ctx context.Context) (identity.Challenge, error) {
	if service == nil {
		return identity.Challenge{}, identity.ErrVerificationUnavailable
	}
	return service.verifier.Begin(ctx)
}
func (service *Service) Status(ctx context.Context) CredentialStatus {
	if store, ok := service.credentials.(credentialStatusReader); ok {
		return store.Status(ctx)
	}
	return CredentialStatus{Health: "unavailable"}
}
func (service *Service) RequireSession(ctx context.Context, token string) error {
	if service == nil {
		return ErrAuthenticationFailed
	}
	if err := service.admin.RequireSession(ctx, token); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

func (service *Service) Replace(ctx context.Context, sessionToken, challengeID string) error {
	if service == nil || sessionToken == "" || challengeID == "" {
		return ErrAuthenticationFailed
	}
	if err := service.admin.RequireRecentTOTP(ctx, sessionToken); err != nil {
		if errors.Is(err, adminidentity.ErrRecentTOTPRequired) {
			return ErrRecentTOTPRequired
		}
		return ErrAuthenticationFailed
	}
	if err := service.verifier.ConsumeCredential(ctx, challengeID, func(cookie []byte) error {
		_, replaceErr := service.credentials.Replace(ctx, cookie)
		return replaceErr
	}); err != nil {
		if errors.Is(err, ErrCredentialUnavailable) {
			return ErrCredentialUnavailable
		}
		if errors.Is(err, identity.ErrVerificationPending) {
			return identity.ErrVerificationPending
		}
		if errors.Is(err, identity.ErrChallengeExpired) {
			return identity.ErrChallengeExpired
		}
		return ErrAuthenticationFailed
	}
	return nil
}

type httpService interface {
	Begin(context.Context) (identity.Challenge, error)
	Replace(context.Context, string, string) error
	RequireSession(context.Context, string) error
	Status(context.Context) CredentialStatus
}

type HTTPOptions struct {
	AllowedOrigin, CSRFToken string
	Limiter                  identity.ChallengeLimiter
	ClientIP                 identity.ClientIPResolver
}

type HTTPHandler struct {
	service       httpService
	allowedOrigin string
	csrfToken     string
	limiter       identity.ChallengeLimiter
	clientIP      identity.ClientIPResolver
	mux           *http.ServeMux
}

func NewHTTPHandler(service httpService, options HTTPOptions) (*HTTPHandler, error) {
	if service == nil || options.Limiter == nil || options.ClientIP == nil || options.CSRFToken == "" || len(options.CSRFToken) > 512 {
		return nil, errors.New("invalid_bili_service_http")
	}
	origin, err := url.Parse(options.AllowedOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("invalid_bili_service_http")
	}
	handler := &HTTPHandler{service: service, allowedOrigin: options.AllowedOrigin, csrfToken: options.CSRFToken, limiter: options.Limiter, clientIP: options.ClientIP, mux: http.NewServeMux()}
	handler.mux.HandleFunc("POST /api/admin/bili-service/challenge", handler.begin)
	handler.mux.HandleFunc("POST /api/admin/bili-service/replace", handler.replace)
	handler.mux.HandleFunc("GET /api/admin/bili-service/status", handler.status)
	return handler, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	handler.mux.ServeHTTP(response, request)
}

func (handler *HTTPHandler) begin(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptEmptyMutation(request) {
		writeHTTPError(response, http.StatusForbidden, "request_rejected")
		return
	}
	if _, ok := handler.requireLimitedSession(response, request, "bili_service_challenge"); !ok {
		return
	}
	challenge, err := handler.service.Begin(request.Context())
	if err != nil {
		writeHTTPError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeHTTPJSON(response, http.StatusCreated, challenge)
}

func (handler *HTTPHandler) replace(response http.ResponseWriter, request *http.Request) {
	if !handler.acceptJSONMutation(request) {
		writeHTTPError(response, http.StatusForbidden, "request_rejected")
		return
	}
	var body struct {
		ChallengeID string `json:"challengeId"`
	}
	if !decodeJSON(response, request, &body) || body.ChallengeID == "" || len(body.ChallengeID) > 256 {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	cookie, ok := handler.requireLimitedSession(response, request, "bili_service_replace")
	if !ok {
		return
	}
	err := handler.service.Replace(request.Context(), cookie, body.ChallengeID)
	switch {
	case err == nil:
		response.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrRecentTOTPRequired):
		writeHTTPError(response, http.StatusForbidden, "recent_totp_required")
	case errors.Is(err, identity.ErrVerificationPending):
		writeHTTPError(response, http.StatusAccepted, "verification_pending")
	case errors.Is(err, identity.ErrChallengeExpired):
		writeHTTPError(response, http.StatusGone, "expired")
	case errors.Is(err, ErrCredentialUnavailable):
		writeHTTPError(response, http.StatusServiceUnavailable, "credential_unavailable")
	default:
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
	}
}
func (handler *HTTPHandler) status(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	if _, ok := handler.requireLimitedSession(response, request, "bili_service_status"); !ok {
		return
	}
	status := handler.service.Status(request.Context())
	if status.Health != "healthy" || status.Version <= 0 || status.LastVerifiedAt == nil || status.LastVerifiedAt.IsZero() {
		if status.Health != "missing" {
			status.Health = "unavailable"
		}
		status.Version = 0
		status.LastVerifiedAt = nil
	}
	writeHTTPJSON(response, http.StatusOK, struct {
		Version        int64      `json:"version"`
		Health         string     `json:"health"`
		LastVerifiedAt *time.Time `json:"lastVerifiedAt,omitempty"`
	}{Version: status.Version, Health: status.Health, LastVerifiedAt: status.LastVerifiedAt})
}
func (handler *HTTPHandler) requireLimitedSession(response http.ResponseWriter, request *http.Request, operation string) (string, bool) {
	cookie, err := request.Cookie(identity.SiteSessionCookie)
	if err != nil || cookie == nil || cookie.Value == "" {
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
		return "", false
	}
	if !handler.allow(request, operation, cookie.Value) {
		writeHTTPError(response, http.StatusTooManyRequests, "rate_limited")
		return "", false
	}
	if handler.service.RequireSession(request.Context(), cookie.Value) != nil {
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
		return "", false
	}
	return cookie.Value, true
}
func (handler *HTTPHandler) allow(request *http.Request, operation, sessionToken string) bool {
	if !handler.limiter.Allow(request.Context(), identity.LimitGlobal, operation) || !handler.limiter.Allow(request.Context(), identity.LimitPerIP, operation+"\x00"+handler.clientIP(request)) {
		return false
	}
	digest := sha256.Sum256([]byte(sessionToken))
	return handler.limiter.Allow(request.Context(), identity.LimitPerChallenge, operation+"\x00"+hex.EncodeToString(digest[:]))
}
func (handler *HTTPHandler) requireSession(response http.ResponseWriter, request *http.Request) (string, bool) {
	cookie, err := request.Cookie(identity.SiteSessionCookie)
	if err != nil || cookie == nil || cookie.Value == "" {
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
		return "", false
	}
	if handler.service.RequireSession(request.Context(), cookie.Value) != nil {
		writeHTTPError(response, http.StatusUnauthorized, "authentication_failed")
		return "", false
	}
	return cookie.Value, true
}

func (handler *HTTPHandler) acceptEmptyMutation(request *http.Request) bool {
	return handler.acceptMutation(request) && request.URL.RawQuery == "" && request.Body != nil && request.ContentLength == 0
}
func (handler *HTTPHandler) acceptJSONMutation(request *http.Request) bool {
	return handler.acceptMutation(request) && request.URL.RawQuery == "" && strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json")
}
func (handler *HTTPHandler) acceptMutation(request *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(request.Header.Get("Origin")), []byte(handler.allowedOrigin)) == 1 && subtle.ConstantTimeCompare([]byte(request.Header.Get("X-CSRF-Token")), []byte(handler.csrfToken)) == 1
}
func decodeJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
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

var _ = time.Second

const (
	danmakuHeaderLength              = 16
	danmakuHeartbeatOperation uint32 = 2
	danmakuMessageOperation   uint32 = 5
	danmakuAuthOperation      uint32 = 7
	danmakuAuthReplyOperation uint32 = 8
	maximumDanmakuPayload            = 1 << 20
)

type danmakuPacket struct {
	protocol  uint16
	operation uint32
	body      []byte
}

func encodeDanmakuPacket(operation uint32, body []byte) []byte {
	output := make([]byte, danmakuHeaderLength+len(body))
	binary.BigEndian.PutUint32(output[:4], uint32(len(output)))
	binary.BigEndian.PutUint16(output[4:6], danmakuHeaderLength)
	binary.BigEndian.PutUint32(output[8:12], operation)
	binary.BigEndian.PutUint32(output[12:16], 1)
	copy(output[danmakuHeaderLength:], body)
	return output
}
func decodeDanmakuPackets(payload []byte) ([]danmakuPacket, error) {
	var packets []danmakuPacket
	for offset := 0; offset < len(payload); {
		if offset+danmakuHeaderLength > len(payload) {
			return nil, ErrEgressUnavailable
		}
		total, header := int(binary.BigEndian.Uint32(payload[offset:offset+4])), int(binary.BigEndian.Uint16(payload[offset+4:offset+6]))
		if total < header || header < danmakuHeaderLength || total > maximumDanmakuPayload || offset+total > len(payload) {
			return nil, ErrEgressUnavailable
		}
		packets = append(packets, danmakuPacket{protocol: binary.BigEndian.Uint16(payload[offset+6 : offset+8]), operation: binary.BigEndian.Uint32(payload[offset+8 : offset+12]), body: append([]byte(nil), payload[offset+header:offset+total]...)})
		offset += total
	}
	return packets, nil
}
func decodeDanmakuApplicationBodies(payload []byte) ([][]byte, error) {
	packets, err := decodeDanmakuPackets(payload)
	if err != nil {
		return nil, err
	}
	var bodies [][]byte
	for _, packet := range packets {
		if packet.operation != danmakuMessageOperation {
			continue
		}
		if packet.protocol == 2 {
			inflated, err := inflateDanmaku(packet.body)
			if err != nil {
				return nil, err
			}
			nested, err := decodeDanmakuPackets(inflated)
			if err != nil {
				return nil, err
			}
			for _, child := range nested {
				if child.operation == danmakuMessageOperation {
					bodies = append(bodies, child.body)
				}
			}
			continue
		}
		bodies = append(bodies, packet.body)
	}
	return bodies, nil
}
func inflateDanmaku(payload []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, ErrEgressUnavailable
	}
	defer reader.Close()
	result, err := io.ReadAll(io.LimitReader(reader, maximumDanmakuPayload+1))
	if err != nil || len(result) > maximumDanmakuPayload {
		return nil, ErrEgressUnavailable
	}
	return result, nil
}

// HTTPUpstream is the production HTTP-only Bilibili adapter. It is deliberately
// unopinionated about logging: it never records response bodies or Cookies.
type HTTPUpstreamOptions struct {
	Client              *http.Client
	RoomInfoEndpoint    string
	GiftCatalogEndpoint string
	DanmakuInfoEndpoint string
	Dial                func(context.Context, string, http.Header) (danmakuSocket, error)
	Now                 func() time.Time
	NewTicker           func(time.Duration) danmakuTicker
}
type danmakuSocket interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	SetReadDeadline(time.Time) error
	Close() error
}
type danmakuTicker interface {
	C() <-chan time.Time
	Stop()
}
type systemDanmakuTicker struct{ ticker *time.Ticker }

func (ticker systemDanmakuTicker) C() <-chan time.Time { return ticker.ticker.C }
func (ticker systemDanmakuTicker) Stop()               { ticker.ticker.Stop() }

type HTTPUpstream struct {
	client                                                     *http.Client
	roomInfoEndpoint, giftCatalogEndpoint, danmakuInfoEndpoint string
	dial                                                       func(context.Context, string, http.Header) (danmakuSocket, error)
	now                                                        func() time.Time
	newTicker                                                  func(time.Duration) danmakuTicker
}
type upstreamFailure struct {
	cause      error
	retryAfter time.Duration
}

func (failure upstreamFailure) Error() string { return failure.cause.Error() }
func (failure upstreamFailure) Unwrap() error { return failure.cause }
func RetryAfter(err error) (time.Duration, bool) {
	var failure upstreamFailure
	if !errors.As(err, &failure) || failure.retryAfter <= 0 {
		return 0, false
	}
	return failure.retryAfter, true
}

func NewHTTPUpstream(options HTTPUpstreamOptions) (*HTTPUpstream, error) {
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 8 * time.Second}
	}
	for _, endpoint := range []string{options.RoomInfoEndpoint, options.GiftCatalogEndpoint, options.DanmakuInfoEndpoint} {
		if endpoint == "" {
			continue
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ws" && parsed.Scheme != "wss") {
			return nil, errors.New("invalid_bili_upstream")
		}
	}
	if options.RoomInfoEndpoint == "" {
		return nil, errors.New("invalid_bili_upstream")
	}
	if options.Dial == nil {
		options.Dial = func(ctx context.Context, target string, header http.Header) (danmakuSocket, error) {
			connection, _, err := websocket.DefaultDialer.DialContext(ctx, target, header)
			return connection, err
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewTicker == nil {
		options.NewTicker = func(interval time.Duration) danmakuTicker {
			return systemDanmakuTicker{ticker: time.NewTicker(interval)}
		}
	}
	return &HTTPUpstream{client: options.Client, roomInfoEndpoint: options.RoomInfoEndpoint, giftCatalogEndpoint: options.GiftCatalogEndpoint, danmakuInfoEndpoint: options.DanmakuInfoEndpoint, dial: options.Dial, now: options.Now, newTicker: options.NewTicker}, nil
}

func (upstream *HTTPUpstream) RoomInfo(ctx context.Context, roomID string, cookie []byte) (RoomInfo, error) {
	var payload struct {
		Code int `json:"code"`
		Data struct {
			RoomID  int64  `json:"room_id"`
			ShortID int64  `json:"short_id"`
			Title   string `json:"title"`
		} `json:"data"`
	}
	if err := upstream.getJSON(ctx, upstream.roomInfoEndpoint, roomID, cookie, &payload); err != nil {
		return RoomInfo{}, err
	}
	if payload.Code != 0 || payload.Data.RoomID <= 0 {
		return RoomInfo{}, ErrEgressUnavailable
	}
	return RoomInfo{RoomID: roomID, CanonicalRoomID: strconv.FormatInt(payload.Data.RoomID, 10), Title: payload.Data.Title}, nil
}
func (upstream *HTTPUpstream) GiftCatalog(ctx context.Context, roomID string, cookie []byte) ([]gameplay.GiftInfo, error) {
	if upstream.giftCatalogEndpoint == "" {
		return nil, ErrEgressUnavailable
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				ID       int     `json:"id"`
				Name     string  `json:"name"`
				Price    float64 `json:"price"`
				CoinType string  `json:"coin_type"`
				ImageURL string  `json:"webp"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := upstream.getJSON(ctx, upstream.giftCatalogEndpoint, roomID, cookie, &payload); err != nil {
		return nil, err
	}
	if payload.Code != 0 {
		return nil, ErrEgressUnavailable
	}
	result := make([]gameplay.GiftInfo, 0, len(payload.Data.List))
	for _, gift := range payload.Data.List {
		if gift.ID > 0 {
			result = append(result, gameplay.GiftInfo{ID: gift.ID, Name: gift.Name, Price: gift.Price, CoinType: gift.CoinType, ImageURL: gift.ImageURL})
		}
	}
	return result, nil
}
func (upstream *HTTPUpstream) OpenRoom(ctx context.Context, roomID string, cookie []byte, sink Sink) (Connection, error) {
	if upstream == nil || upstream.danmakuInfoEndpoint == "" || sink == nil {
		return nil, ErrEgressUnavailable
	}
	var info struct {
		Code int `json:"code"`
		Data struct {
			Token    string `json:"token"`
			HostList []struct {
				Host    string `json:"host"`
				WSSPort int    `json:"wss_port"`
			} `json:"host_list"`
		} `json:"data"`
	}
	if err := upstream.getJSON(ctx, upstream.danmakuInfoEndpoint, roomID, cookie, &info); err != nil {
		return nil, err
	}
	if info.Code != 0 || info.Data.Token == "" || len(info.Data.HostList) == 0 {
		return nil, ErrEgressUnavailable
	}
	host := info.Data.HostList[0]
	if host.Host == "" || host.WSSPort <= 0 {
		return nil, ErrEgressUnavailable
	}
	uid, buvid, ok := danmakuIdentity(cookie)
	if !ok {
		return nil, ErrEgressUnavailable
	}
	numericRoomID, err := strconv.ParseInt(roomID, 10, 64)
	if err != nil || numericRoomID <= 0 {
		return nil, ErrEgressUnavailable
	}
	header := http.Header{}
	header.Set("Cookie", string(cookie))
	header.Set("User-Agent", "gift-panel-hosted/1")
	header.Set("Origin", "https://live.bilibili.com")
	connection, err := upstream.dial(ctx, "wss://"+host.Host+":"+strconv.Itoa(host.WSSPort)+"/sub", header)
	if err != nil {
		return nil, ErrEgressUnavailable
	}
	auth, _ := json.Marshal(map[string]any{"uid": uid, "roomid": numericRoomID, "protover": 2, "platform": "web", "type": 2, "key": info.Data.Token, "buvid": buvid})
	if err := connection.WriteMessage(websocket.BinaryMessage, encodeDanmakuPacket(danmakuAuthOperation, auth)); err != nil {
		_ = connection.Close()
		return nil, ErrEgressUnavailable
	}
	if err := connection.SetReadDeadline(upstream.now().Add(45 * time.Second)); err != nil {
		_ = connection.Close()
		return nil, ErrEgressUnavailable
	}
	_, reply, err := connection.ReadMessage()
	if err != nil || !validDanmakuAuthReply(reply) {
		_ = connection.Close()
		return nil, ErrEgressUnavailable
	}
	result := &websocketConnection{connection: connection, done: make(chan struct{}), now: upstream.now, newTicker: upstream.newTicker}
	go result.forward(sink)
	go result.heartbeat()
	return result, nil
}

type websocketConnection struct {
	connection danmakuSocket
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	err        error
	closed     bool
	now        func() time.Time
	newTicker  func(time.Duration) danmakuTicker
}

func (connection *websocketConnection) Close() error {
	var result error
	connection.once.Do(func() {
		connection.mu.Lock()
		connection.closed = true
		connection.mu.Unlock()
		close(connection.done)
		result = connection.connection.Close()
	})
	return result
}
func (connection *websocketConnection) Done() <-chan struct{} { return connection.done }
func (connection *websocketConnection) Err() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.err
}
func (connection *websocketConnection) fail(err error) {
	connection.mu.Lock()
	if !connection.closed {
		connection.err = err
	}
	connection.mu.Unlock()
}
func (connection *websocketConnection) forward(sink Sink) {
	defer connection.Close()
	for {
		_, payload, err := connection.connection.ReadMessage()
		if err != nil {
			select {
			case <-connection.done:
				return
			default:
			}
			connection.fail(ErrEgressUnavailable)
			return
		}
		select {
		case <-connection.done:
			return
		default:
		}
		if err := connection.connection.SetReadDeadline(connection.now().Add(45 * time.Second)); err != nil {
			select {
			case <-connection.done:
				return
			default:
			}
			connection.fail(ErrEgressUnavailable)
			return
		}
		bodies, decodeErr := decodeDanmakuApplicationBodies(payload)
		if decodeErr != nil {
			select {
			case <-connection.done:
				return
			default:
			}
			connection.fail(ErrEgressUnavailable)
			return
		}
		for _, body := range bodies {
			select {
			case <-connection.done:
				return
			default:
				sink(Event{Type: "application", Data: append([]byte(nil), body...)})
			}
		}
	}
}
func (connection *websocketConnection) heartbeat() {
	ticker := connection.newTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-connection.done:
			return
		case <-ticker.C():
			if err := connection.connection.WriteMessage(websocket.BinaryMessage, encodeDanmakuPacket(danmakuHeartbeatOperation, nil)); err != nil {
				select {
				case <-connection.done:
					return
				default:
				}
				connection.fail(ErrEgressUnavailable)
				_ = connection.Close()
				return
			}
		}
	}
}
func validDanmakuAuthReply(payload []byte) bool {
	packets, err := decodeDanmakuPackets(payload)
	if err != nil {
		return false
	}
	for _, packet := range packets {
		if packet.operation == danmakuAuthReplyOperation {
			var reply struct {
				Code int `json:"code"`
			}
			return json.Unmarshal(packet.body, &reply) == nil && reply.Code == 0
		}
	}
	return false
}
func (upstream *HTTPUpstream) getJSON(ctx context.Context, endpoint, roomID string, cookie []byte, target any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ErrEgressUnavailable
	}
	query := parsed.Query()
	if endpoint == upstream.roomInfoEndpoint || endpoint == upstream.danmakuInfoEndpoint {
		query.Set("id", roomID)
		if endpoint == upstream.danmakuInfoEndpoint {
			query.Set("type", "0")
		}
	} else {
		query.Set("room_id", roomID)
	}
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return ErrEgressUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "gift-panel-hosted/1")
	request.Header.Set("Cookie", string(cookie))
	response, err := upstream.client.Do(request)
	if err != nil {
		return ErrEgressUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return mapUpstreamStatus(response)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return ErrEgressUnavailable
	}
	return nil
}

func danmakuIdentity(cookie []byte) (int64, string, bool) {
	var uid int64
	var buvid string
	for _, part := range strings.Split(string(cookie), ";") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || value == "" {
			continue
		}
		switch key {
		case "DedeUserID":
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed <= 0 {
				return 0, "", false
			}
			uid = parsed
		case "buvid3", "buvid4":
			if len(value) <= 256 && buvid == "" {
				buvid = value
			}
		}
	}
	return uid, buvid, uid > 0
}
func mapUpstreamStatus(response *http.Response) error {
	cause := ErrEgressUnavailable
	if response.StatusCode == http.StatusTooManyRequests {
		cause = ErrRateLimited
	}
	if response.StatusCode == http.StatusForbidden {
		cause = ErrRiskRejected
	}
	seconds, _ := strconv.Atoi(response.Header.Get("Retry-After"))
	if seconds > 0 {
		return upstreamFailure{cause: cause, retryAfter: time.Duration(seconds) * time.Second}
	}
	return cause
}
