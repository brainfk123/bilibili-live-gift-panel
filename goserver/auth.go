package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	defaultQRCodeGenerateEndpoint = "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	defaultQRCodePollEndpoint     = "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"
	defaultBilibiliNavEndpoint    = "https://api.bilibili.com/x/web-interface/nav"
	defaultBilibiliRoomEndpoint   = "https://api.live.bilibili.com/room/v1/Room/get_info"
	qrCodeLifetime                = 3 * time.Minute
)

var errLoginCredentialsNotFound = errors.New("登录凭证不存在")

type loginCredentials struct {
	UID          int64             `json:"uid"`
	Uname        string            `json:"uname"`
	Avatar       string            `json:"avatar"`
	Cookies      map[string]string `json:"cookies"`
	RefreshToken string            `json:"refreshToken,omitempty"`
	SavedAt      int64             `json:"savedAt"`
}

type loginCredentialStore interface {
	Load() (loginCredentials, error)
	Save(loginCredentials) error
	Delete() error
}

type biliSession struct {
	UID          int64
	CookieHeader string
	Buvid        string
}

type loginPublicState struct {
	State       string `json:"state"`
	UID         int64  `json:"uid,omitempty"`
	Uname       string `json:"uname,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	RoomID      string `json:"roomId,omitempty"`
	IsRoomOwner *bool  `json:"isRoomOwner,omitempty"`
	QRImage     string `json:"qrImage,omitempty"`
	QRExpiresAt int64  `json:"expiresAt,omitempty"`
	Message     string `json:"message,omitempty"`
}

type qrImageEncoder func(string) (string, error)

type loginManager struct {
	client           *http.Client
	store            loginCredentialStore
	encodeQR         qrImageEncoder
	generateEndpoint string
	pollEndpoint     string
	navEndpoint      string
	roomInfoEndpoint string
	now              func() time.Time

	mu               sync.RWMutex
	pendingKey       string
	pendingExpiresAt time.Time
	onChange         func()
	cachedStatus     loginPublicState
	statusCheckedAt  time.Time
	cachedRoomID     string
	cachedRoomOwner  int64
	roomCheckedAt    time.Time
}

func newLoginManager(client *http.Client, store loginCredentialStore, encoder qrImageEncoder) *loginManager {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	if encoder == nil {
		encoder = encodeLoginQRCode
	}
	return &loginManager{
		client:           client,
		store:            store,
		encodeQR:         encoder,
		generateEndpoint: defaultQRCodeGenerateEndpoint,
		pollEndpoint:     defaultQRCodePollEndpoint,
		navEndpoint:      defaultBilibiliNavEndpoint,
		roomInfoEndpoint: defaultBilibiliRoomEndpoint,
		now:              time.Now,
	}
}

func encodeLoginQRCode(value string) (string, error) {
	png, err := qrcode.Encode(value, qrcode.Medium, 280)
	if err != nil {
		return "", fmt.Errorf("生成登录二维码失败：%w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func (manager *loginManager) SetOnChange(callback func()) {
	manager.mu.Lock()
	manager.onChange = callback
	manager.mu.Unlock()
}

func (manager *loginManager) notifyChanged() {
	manager.mu.RLock()
	callback := manager.onChange
	manager.mu.RUnlock()
	if callback != nil {
		callback()
	}
}

func (manager *loginManager) StartQRCode(ctx context.Context) (loginPublicState, error) {
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			URL string `json:"url"`
			Key string `json:"qrcode_key"`
		} `json:"data"`
	}
	if _, err := manager.getJSON(ctx, manager.generateEndpoint, nil, &payload); err != nil {
		return loginPublicState{}, err
	}
	if payload.Code != 0 || payload.Data.Key == "" || payload.Data.URL == "" {
		return loginPublicState{}, fmt.Errorf("创建登录二维码失败：%s", firstNonEmpty(payload.Message, "B 站响应无效"))
	}
	image, err := manager.encodeQR(payload.Data.URL)
	if err != nil {
		return loginPublicState{}, err
	}
	expiresAt := manager.now().Add(qrCodeLifetime)
	manager.mu.Lock()
	manager.pendingKey = payload.Data.Key
	manager.pendingExpiresAt = expiresAt
	manager.mu.Unlock()
	return loginPublicState{State: "waiting", QRImage: image, QRExpiresAt: expiresAt.Unix()}, nil
}

func (manager *loginManager) PollQRCode(ctx context.Context) (loginPublicState, error) {
	manager.mu.RLock()
	key := manager.pendingKey
	expiresAt := manager.pendingExpiresAt
	manager.mu.RUnlock()
	if key == "" {
		return loginPublicState{}, fmt.Errorf("请先生成登录二维码")
	}
	if !manager.now().Before(expiresAt) {
		manager.clearPending()
		return loginPublicState{State: "expired", Message: "二维码已过期，请重新生成"}, nil
	}

	endpoint, err := url.Parse(manager.pollEndpoint)
	if err != nil {
		return loginPublicState{}, err
	}
	query := endpoint.Query()
	query.Set("qrcode_key", key)
	endpoint.RawQuery = query.Encode()
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			URL          string `json:"url"`
			RefreshToken string `json:"refresh_token"`
			Code         int    `json:"code"`
			Message      string `json:"message"`
		} `json:"data"`
	}
	response, err := manager.getJSON(ctx, endpoint.String(), nil, &payload)
	if err != nil {
		return loginPublicState{}, err
	}
	if payload.Code != 0 {
		return loginPublicState{}, fmt.Errorf("登录轮询失败：%s", firstNonEmpty(payload.Message, "B 站响应无效"))
	}
	switch payload.Data.Code {
	case 86101:
		return loginPublicState{State: "waiting", QRExpiresAt: expiresAt.Unix(), Message: "等待扫码"}, nil
	case 86090:
		return loginPublicState{State: "scanned", QRExpiresAt: expiresAt.Unix(), Message: "已扫码，请在手机上确认"}, nil
	case 86038:
		manager.clearPending()
		return loginPublicState{State: "expired", Message: "二维码已过期，请重新生成"}, nil
	case 0:
		credentials, err := manager.credentialsFromLoginResponse(response, payload.Data.URL, payload.Data.RefreshToken)
		if err != nil {
			return loginPublicState{}, err
		}
		identity, err := manager.fetchIdentity(ctx, credentials.Cookies)
		if err != nil {
			return loginPublicState{}, err
		}
		credentials.UID = identity.UID
		credentials.Uname = identity.Uname
		credentials.Avatar = identity.Avatar
		credentials.SavedAt = manager.now().Unix()
		if err := manager.store.Save(credentials); err != nil {
			return loginPublicState{}, fmt.Errorf("保存登录状态失败：%w", err)
		}
		state := publicStateFromCredentials(credentials)
		manager.mu.Lock()
		manager.pendingKey = ""
		manager.pendingExpiresAt = time.Time{}
		manager.cachedStatus = state
		manager.statusCheckedAt = manager.now()
		manager.cachedRoomID = ""
		manager.cachedRoomOwner = 0
		manager.roomCheckedAt = time.Time{}
		manager.mu.Unlock()
		manager.notifyChanged()
		return state, nil
	default:
		return loginPublicState{State: "error", Message: firstNonEmpty(payload.Data.Message, "登录状态未知")}, nil
	}
}

func (manager *loginManager) Status(ctx context.Context, roomID string) loginPublicState {
	credentials, err := manager.store.Load()
	if errors.Is(err, errLoginCredentialsNotFound) {
		return loginPublicState{State: "anonymous"}
	}
	if err != nil {
		return loginPublicState{State: "error", Message: err.Error()}
	}
	manager.mu.RLock()
	cached := manager.cachedStatus
	checkedAt := manager.statusCheckedAt
	manager.mu.RUnlock()
	if cached.State != "" && manager.now().Sub(checkedAt) < 30*time.Second {
		return manager.withRoomOwnership(ctx, cached, roomID)
	}
	identity, err := manager.fetchIdentity(ctx, credentials.Cookies)
	if err != nil {
		expired := loginPublicState{State: "expired", Message: err.Error()}
		manager.mu.Lock()
		manager.cachedStatus = expired
		manager.statusCheckedAt = manager.now()
		manager.mu.Unlock()
		manager.notifyChanged()
		return expired
	}
	credentials.UID = identity.UID
	credentials.Uname = identity.Uname
	credentials.Avatar = identity.Avatar
	_ = manager.store.Save(credentials)
	state := publicStateFromCredentials(credentials)
	manager.mu.Lock()
	manager.cachedStatus = state
	manager.statusCheckedAt = manager.now()
	manager.mu.Unlock()
	return manager.withRoomOwnership(ctx, state, roomID)
}

func (manager *loginManager) Session(ctx context.Context) (biliSession, bool) {
	if state := manager.Status(ctx, ""); state.State != "logged_in" {
		return biliSession{}, false
	}
	credentials, err := manager.store.Load()
	if err != nil || credentials.UID <= 0 || credentials.Cookies["SESSDATA"] == "" {
		return biliSession{}, false
	}
	return biliSession{
		UID:          credentials.UID,
		CookieHeader: loginCookieHeader(credentials.Cookies),
		Buvid:        credentials.Cookies["buvid3"],
	}, true
}

func (manager *loginManager) Logout() error {
	if err := manager.store.Delete(); err != nil && !errors.Is(err, errLoginCredentialsNotFound) {
		return fmt.Errorf("清除登录状态失败：%w", err)
	}
	manager.mu.Lock()
	manager.cachedStatus = loginPublicState{}
	manager.statusCheckedAt = time.Time{}
	manager.cachedRoomID = ""
	manager.cachedRoomOwner = 0
	manager.roomCheckedAt = time.Time{}
	manager.pendingKey = ""
	manager.pendingExpiresAt = time.Time{}
	manager.mu.Unlock()
	manager.notifyChanged()
	return nil
}

func (manager *loginManager) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/api/auth/status":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "auth": manager.Status(r.Context(), r.URL.Query().Get("room_id"))})
	case "/api/auth/qrcode":
		var (
			state loginPublicState
			err   error
		)
		switch r.Method {
		case http.MethodPost:
			state, err = manager.StartQRCode(r.Context())
		case http.MethodGet:
			state, err = manager.PollQRCode(r.Context())
		default:
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		if state.State == "logged_in" {
			state = manager.withRoomOwnership(r.Context(), state, r.URL.Query().Get("room_id"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "auth": state})
	case "/api/auth/session":
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", http.MethodDelete)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		if err := manager.Logout(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "auth": loginPublicState{State: "anonymous"}})
	default:
		http.NotFound(w, r)
	}
}

func (manager *loginManager) withRoomOwnership(ctx context.Context, state loginPublicState, roomID string) loginPublicState {
	roomID = strings.TrimSpace(roomID)
	if state.State != "logged_in" || roomID == "" {
		return state
	}
	ownerUID, err := manager.roomOwnerUID(ctx, roomID)
	state.RoomID = roomID
	if err != nil {
		state.Message = "主播身份暂未验证：" + err.Error()
		return state
	}
	isOwner := ownerUID > 0 && ownerUID == state.UID
	state.IsRoomOwner = &isOwner
	if !isOwner {
		state.Message = "已使用普通登录身份连接；完整匿名资料仍取决于 B 站房间权限"
	}
	return state
}

func (manager *loginManager) roomOwnerUID(ctx context.Context, roomID string) (int64, error) {
	manager.mu.RLock()
	cachedRoomID := manager.cachedRoomID
	cachedOwner := manager.cachedRoomOwner
	checkedAt := manager.roomCheckedAt
	manager.mu.RUnlock()
	if cachedRoomID == roomID && manager.now().Sub(checkedAt) < 30*time.Second {
		return cachedOwner, nil
	}
	endpoint, err := url.Parse(manager.roomInfoEndpoint)
	if err != nil {
		return 0, err
	}
	query := endpoint.Query()
	query.Set("room_id", roomID)
	endpoint.RawQuery = query.Encode()
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			UID int64 `json:"uid"`
		} `json:"data"`
	}
	if _, err := manager.getJSON(ctx, endpoint.String(), nil, &payload); err != nil {
		return 0, err
	}
	if payload.Code != 0 || payload.Data.UID <= 0 {
		return 0, fmt.Errorf("读取房间主播失败：%s", firstNonEmpty(payload.Message, "B 站响应无效"))
	}
	manager.mu.Lock()
	manager.cachedRoomID = roomID
	manager.cachedRoomOwner = payload.Data.UID
	manager.roomCheckedAt = manager.now()
	manager.mu.Unlock()
	return payload.Data.UID, nil
}

func (manager *loginManager) clearPending() {
	manager.mu.Lock()
	manager.pendingKey = ""
	manager.pendingExpiresAt = time.Time{}
	manager.mu.Unlock()
}

func (manager *loginManager) credentialsFromLoginResponse(response *http.Response, callbackURL, refreshToken string) (loginCredentials, error) {
	cookies := map[string]string{}
	for _, cookie := range response.Cookies() {
		if isLoginCookie(cookie.Name) && cookie.Value != "" {
			cookies[cookie.Name] = cookie.Value
		}
	}
	if parsed, err := url.Parse(callbackURL); err == nil {
		for _, name := range loginCookieNames {
			if value := parsed.Query().Get(name); value != "" {
				cookies[name] = value
			}
		}
	}
	if cookies["SESSDATA"] == "" {
		return loginCredentials{}, fmt.Errorf("扫码已确认，但 B 站没有返回登录凭证")
	}
	return loginCredentials{Cookies: cookies, RefreshToken: refreshToken}, nil
}

type loginIdentity struct {
	UID    int64
	Uname  string
	Avatar string
}

func (manager *loginManager) fetchIdentity(ctx context.Context, cookies map[string]string) (loginIdentity, error) {
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			IsLogin bool   `json:"isLogin"`
			Mid     int64  `json:"mid"`
			Uname   string `json:"uname"`
			Face    string `json:"face"`
		} `json:"data"`
	}
	_, err := manager.getJSON(ctx, manager.navEndpoint, cookies, &payload)
	if err != nil {
		return loginIdentity{}, err
	}
	if payload.Code != 0 || !payload.Data.IsLogin || payload.Data.Mid <= 0 {
		return loginIdentity{}, fmt.Errorf("B 站登录已失效：%s", firstNonEmpty(payload.Message, "请重新扫码"))
	}
	return loginIdentity{UID: payload.Data.Mid, Uname: strings.TrimSpace(payload.Data.Uname), Avatar: strings.TrimSpace(payload.Data.Face)}, nil
}

func (manager *loginManager) getJSON(ctx context.Context, endpoint string, cookies map[string]string, target any) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Referer", "https://www.bilibili.com/")
	if len(cookies) > 0 {
		request.Header.Set("Cookie", loginCookieHeader(cookies))
	}
	response, err := manager.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("请求 B 站登录接口失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return response, fmt.Errorf("B 站登录接口返回 HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return response, fmt.Errorf("解析 B 站登录响应失败：%w", err)
	}
	return response, nil
}

var loginCookieNames = []string{"SESSDATA", "bili_jct", "DedeUserID", "DedeUserID__ckMd5", "sid", "buvid3", "buvid4", "b_nut"}

func isLoginCookie(name string) bool {
	for _, allowed := range loginCookieNames {
		if name == allowed {
			return true
		}
	}
	return false
}

func loginCookieHeader(cookies map[string]string) string {
	names := make([]string, 0, len(cookies))
	for name, value := range cookies {
		if isLoginCookie(name) && value != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+cookies[name])
	}
	return strings.Join(parts, "; ")
}

func publicStateFromCredentials(credentials loginCredentials) loginPublicState {
	return loginPublicState{State: "logged_in", UID: credentials.UID, Uname: credentials.Uname, Avatar: credentials.Avatar}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
