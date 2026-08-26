package adminconsole

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"github.com/DATA-DOG/go-sqlmock"
)

type testSessions struct {
	err   error
	token string
}

func (sessions *testSessions) RequireSession(_ context.Context, token string) error {
	sessions.token = token
	return sessions.err
}

func TestHTTPRequiresAdministratorSession(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _ := NewService(db, "https://panel.example.com")
	sessions := &testSessions{err: errors.New("revoked")}
	handler, _ := NewHTTPHandler(service, sessions)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "revoked-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || sessions.token != "revoked-token" {
		t.Fatalf("status=%d token=%q", response.Code, sessions.token)
	}
}

func TestHTTPRejectsMalformedCursorBeforeRepository(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _ := NewService(db, "https://panel.example.com")
	handler, _ := NewHTTPHandler(service, &testSessions{})
	request := httptest.NewRequest(http.MethodGet, "/api/admin/accounts?cursor=not-base64", nil)
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPBatchReturnsEveryTargetResult(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, _ := NewService(db, "https://panel.example.com", MutationServices{Disable: func(_ context.Context, _ string, id int64, _ string) error {
		if id == 52 {
			return errors.New("disabled")
		}
		return nil
	}})
	handler, _ := NewHTTPHandler(service, &testSessions{})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/batch", strings.NewReader(`{"accountIds":[41,52],"action":"disable","reason":"maintenance"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accountId":41`) || !strings.Contains(response.Body.String(), `"accountId":52`) || !strings.Contains(response.Body.String(), `"failed"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// This test fails if the administrator PUT rejects a disabled account before
// reaching the deep room mutation or reports its canonical saved target as a
// failed operation.
func TestHTTPUpdateRoomPersistsCanonicalTargetForDisabledAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	called := false
	mutation := roomMutationFunc(func(_ context.Context, accountID int64, roomID string) (RoomMutationResult, error) {
		called = true
		if accountID != 41 || roomID != "7" {
			t.Fatalf("SetRoom(%d, %q)", accountID, roomID)
		}
		return RoomMutationResult{OldCanonical: "111", NewCanonical: "42"}, nil
	})
	mock.ExpectExec("INSERT INTO audit_events").WithArgs(int64(41), []byte(`{"newRoomId":"42","oldRoomId":"111"}`), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT a.id.*FROM streamer_accounts").WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "active", "room", "quota", "obs", "public", "created", "updated"}).AddRow(41, false, "42", 0, false, "", now, now))
	mock.ExpectQuery("SELECT event_type").WithArgs(int64(41), 20).
		WillReturnRows(sqlmock.NewRows([]string{"type", "account", "created"}).AddRow("admin_room_updated", 41, now))
	service, _ := NewService(db, "https://panel.example.com", MutationServices{Room: mutation})
	handler, _ := NewHTTPHandler(service, &testSessions{})
	request := httptest.NewRequest(http.MethodPut, "/api/admin/accounts/41/room", strings.NewReader(`{"roomId":"7"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !called || !strings.Contains(response.Body.String(), `"status":"disabled"`) || !strings.Contains(response.Body.String(), `"roomId":"42"`) {
		t.Fatalf("status=%d called=%v body=%s", response.Code, called, response.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// This test fails if concurrent PUT audits reconstruct old/new room IDs with
// before/after queries instead of using the immutable result returned by each
// serialized deep mutation.
func TestHTTPConcurrentRoomUpdatesAuditEachMutationResult(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	now := time.Date(2026, 8, 26, 12, 30, 0, 0, time.UTC)
	firstStarted := make(chan struct{})
	allowFirst := make(chan struct{})
	secondReturned := make(chan struct{})
	mutation := roomMutationFunc(func(_ context.Context, _ int64, roomID string) (RoomMutationResult, error) {
		switch roomID {
		case "20":
			close(firstStarted)
			<-allowFirst
			return RoomMutationResult{OldCanonical: "10", NewCanonical: "20"}, nil
		case "30":
			close(secondReturned)
			return RoomMutationResult{OldCanonical: "20", NewCanonical: "30"}, nil
		default:
			return RoomMutationResult{}, errors.New("unexpected room")
		}
	})
	for _, payload := range [][]byte{
		[]byte(`{"newRoomId":"20","oldRoomId":"10"}`),
		[]byte(`{"newRoomId":"30","oldRoomId":"20"}`),
	} {
		mock.ExpectExec("INSERT INTO audit_events").WithArgs(int64(41), payload, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	}
	for range 2 {
		mock.ExpectQuery("SELECT a.id.*FROM streamer_accounts").WithArgs(int64(41)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "active", "room", "quota", "obs", "public", "created", "updated"}).AddRow(41, false, "30", 0, false, "", now, now))
		mock.ExpectQuery("SELECT event_type").WithArgs(int64(41), 20).
			WillReturnRows(sqlmock.NewRows([]string{"type", "account", "created"}))
	}
	service, _ := NewService(db, "https://panel.example.com", MutationServices{Room: mutation})
	handler, _ := NewHTTPHandler(service, &testSessions{})
	put := func(roomID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, "/api/admin/accounts/41/room", strings.NewReader(`{"roomId":"`+roomID+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- put("20") }()
	<-firstStarted
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { secondDone <- put("30") }()
	<-secondReturned
	close(allowFirst)
	if first, second := <-firstDone, <-secondDone; first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("concurrent PUT status = %d/%d body=%s / %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
