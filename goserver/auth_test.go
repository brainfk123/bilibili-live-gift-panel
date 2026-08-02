package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type memoryLoginCredentialStore struct {
	mu          sync.Mutex
	credentials *loginCredentials
}

func (store *memoryLoginCredentialStore) Load() (loginCredentials, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.credentials == nil {
		return loginCredentials{}, errLoginCredentialsNotFound
	}
	return cloneLoginCredentials(*store.credentials), nil
}

func (store *memoryLoginCredentialStore) Save(credentials loginCredentials) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	copy := cloneLoginCredentials(credentials)
	store.credentials = &copy
	return nil
}

func (store *memoryLoginCredentialStore) Delete() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.credentials = nil
	return nil
}

func cloneLoginCredentials(credentials loginCredentials) loginCredentials {
	copy := credentials
	copy.Cookies = make(map[string]string, len(credentials.Cookies))
	for name, value := range credentials.Cookies {
		copy.Cookies[name] = value
	}
	return copy
}

func TestLoginManagerQRCodeFlowStoresCredentialsServerSide(t *testing.T) {
	var pollCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/generate":
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0,
				"data": map[string]any{"url": "https://account.bilibili.com/scan?qrcode_key=qr-key", "qrcode_key": "qr-key"},
			})
		case "/poll":
			pollCount++
			if pollCount == 1 {
				writeJSON(w, http.StatusOK, map[string]any{
					"code": 0,
					"data": map[string]any{"code": 86101, "message": "未扫码", "url": ""},
				})
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "secret-session"})
			http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "csrf-token"})
			http.SetCookie(w, &http.Cookie{Name: "DedeUserID", Value: "32249588"})
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0,
				"data": map[string]any{"code": 0, "message": "", "url": "https://www.bilibili.com/", "refresh_token": "refresh-token"},
			})
		case "/nav":
			if !strings.Contains(r.Header.Get("Cookie"), "SESSDATA=secret-session") {
				t.Fatalf("nav cookie = %q", r.Header.Get("Cookie"))
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0,
				"data": map[string]any{"isLogin": true, "mid": 32249588, "uname": "反重力鱼", "face": "https://example.test/face.jpg"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	credentials := &memoryLoginCredentialStore{}
	manager := newLoginManager(server.Client(), credentials, func(value string) (string, error) {
		if !strings.Contains(value, "qrcode_key=qr-key") {
			t.Fatalf("QR value = %q", value)
		}
		return "data:image/png;base64,qr", nil
	})
	manager.generateEndpoint = server.URL + "/generate"
	manager.pollEndpoint = server.URL + "/poll"
	manager.navEndpoint = server.URL + "/nav"

	started, err := manager.StartQRCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if started.State != "waiting" || started.QRImage != "data:image/png;base64,qr" || started.QRExpiresAt == 0 {
		t.Fatalf("started state = %#v", started)
	}
	waiting, err := manager.PollQRCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State != "waiting" {
		t.Fatalf("waiting state = %#v", waiting)
	}
	loggedIn, err := manager.PollQRCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loggedIn.State != "logged_in" || loggedIn.UID != 32249588 || loggedIn.Uname != "反重力鱼" {
		t.Fatalf("logged in state = %#v", loggedIn)
	}
	encoded, _ := json.Marshal(loggedIn)
	if strings.Contains(string(encoded), "secret-session") || strings.Contains(string(encoded), "csrf-token") {
		t.Fatalf("public state exposed credentials: %s", encoded)
	}
	saved, err := credentials.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Cookies["SESSDATA"] != "secret-session" || saved.RefreshToken != "refresh-token" {
		t.Fatalf("saved credentials = %#v", saved)
	}
	session, ok := manager.Session(context.Background())
	if !ok || session.UID != 32249588 || !strings.Contains(session.CookieHeader, "SESSDATA=secret-session") {
		t.Fatalf("session = %#v, ok=%v", session, ok)
	}
}

func TestLoginManagerLogoutDeletesLocalSessionAndNotifies(t *testing.T) {
	credentials := &memoryLoginCredentialStore{credentials: &loginCredentials{
		UID: 1, Cookies: map[string]string{"SESSDATA": "secret"},
	}}
	manager := newLoginManager(http.DefaultClient, credentials, nil)
	notified := 0
	manager.SetOnChange(func() { notified++ })
	if err := manager.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Load(); err != errLoginCredentialsNotFound {
		t.Fatalf("load after logout error = %v", err)
	}
	if notified != 1 {
		t.Fatalf("change notifications = %d", notified)
	}
}

func TestLoginManagerStatusVerifiesCurrentRoomOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nav":
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0,
				"data": map[string]any{"isLogin": true, "mid": 32249588, "uname": "主播", "face": "face.jpg"},
			})
		case "/room":
			uid := int64(32249588)
			if r.URL.Query().Get("room_id") == "other-room" {
				uid = 999
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0,
				"data": map[string]any{"room_id": 31567150, "uid": uid},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	credentials := &memoryLoginCredentialStore{credentials: &loginCredentials{
		UID:     32249588,
		Cookies: map[string]string{"SESSDATA": "secret-session"},
	}}
	manager := newLoginManager(server.Client(), credentials, nil)
	manager.navEndpoint = server.URL + "/nav"
	manager.roomInfoEndpoint = server.URL + "/room"

	owner := manager.Status(context.Background(), "31567150")
	if owner.IsRoomOwner == nil || !*owner.IsRoomOwner || owner.RoomID != "31567150" {
		t.Fatalf("owner state = %#v", owner)
	}
	notOwner := manager.Status(context.Background(), "other-room")
	if notOwner.IsRoomOwner == nil || *notOwner.IsRoomOwner || !strings.Contains(notOwner.Message, "不是当前房间主播") {
		t.Fatalf("non-owner state = %#v", notOwner)
	}
}

func TestLoginManagerDoesNotUseExpiredCredentialsForBackgroundSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"code":    -101,
			"message": "账号未登录",
			"data":    map[string]any{"isLogin": false},
		})
	}))
	defer server.Close()

	credentials := &memoryLoginCredentialStore{credentials: &loginCredentials{
		UID:     32249588,
		Cookies: map[string]string{"SESSDATA": "expired-session"},
	}}
	manager := newLoginManager(server.Client(), credentials, nil)
	manager.navEndpoint = server.URL
	if session, ok := manager.Session(context.Background()); ok {
		t.Fatalf("expired credentials must not reach the background connection: %#v", session)
	}
}
