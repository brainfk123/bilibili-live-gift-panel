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
	mutationID := [16]byte{1, 1, 1, 1}
	called := false
	mutation := roomMutationFunc(func(_ context.Context, accountID int64, roomID string) (RoomMutationResult, error) {
		called = true
		if accountID != 41 || roomID != "7" {
			t.Fatalf("SetRoom(%d, %q)", accountID, roomID)
		}
		return RoomMutationResult{MutationID: mutationID, AccountID: 41, DesiredCanonical: "42", OldCanonical: "111", NewCanonical: "42", Phase: RoomMutationReferencesSynced}, nil
	})
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_id, desired_room_id, old_room_id, new_room_id, phase, audit_event_id FROM room_mutation_receipts").WithArgs(mutationID[:]).
		WillReturnRows(sqlmock.NewRows([]string{"account", "desired", "old", "new", "phase", "audit"}).AddRow(41, "42", "111", "42", string(RoomMutationReferencesSynced), nil))
	mock.ExpectExec("INSERT INTO audit_events").WithArgs(int64(41), []byte(`{"newRoomId":"42","oldRoomId":"111"}`), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE room_mutation_receipts SET phase").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
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

func TestHTTPRoomUpdateRetryCompletesSameReceiptAfterAuditFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mutationID := [16]byte{9, 9, 9, 9}
	receipt := RoomMutationResult{MutationID: mutationID, AccountID: 41, DesiredCanonical: "42", OldCanonical: "111", NewCanonical: "42", Phase: RoomMutationReferencesSynced}
	service, _ := NewService(db, "https://panel.example.com", MutationServices{Room: roomMutationFunc(func(context.Context, int64, string) (RoomMutationResult, error) { return receipt, nil })})
	handler, _ := NewHTTPHandler(service, &testSessions{})
	put := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, "/api/admin/accounts/41/room", strings.NewReader(`{"roomId":"7"}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "admin-token"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_id, desired_room_id, old_room_id, new_room_id, phase, audit_event_id FROM room_mutation_receipts").WithArgs(mutationID[:]).WillReturnRows(sqlmock.NewRows([]string{"account", "desired", "old", "new", "phase", "audit"}).AddRow(41, "42", "111", "42", string(RoomMutationReferencesSynced), nil))
	mock.ExpectExec("INSERT INTO audit_events").WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	if first := put(); first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first PUT status=%d body=%s", first.Code, first.Body.String())
	}
	expectHTTPRoomAudit(mock, mutationID, "111", "42", 21)
	now := time.Date(2026, 8, 26, 14, 40, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT a.id.*FROM streamer_accounts").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"id", "active", "room", "quota", "obs", "public", "created", "updated"}).AddRow(41, false, "42", 0, false, "", now, now))
	mock.ExpectQuery("SELECT event_type").WithArgs(int64(41), 20).WillReturnRows(sqlmock.NewRows([]string{"type", "account", "created"}))
	if second := put(); second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"roomId":"42"`) {
		t.Fatalf("retry PUT status=%d body=%s", second.Code, second.Body.String())
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
	now := time.Date(2026, 8, 26, 12, 30, 0, 0, time.UTC)
	firstStarted := make(chan struct{})
	allowFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	firstID := [16]byte{2, 2, 2, 2}
	secondID := [16]byte{3, 3, 3, 3}
	mutation := roomMutationFunc(func(_ context.Context, _ int64, roomID string) (RoomMutationResult, error) {
		switch roomID {
		case "20":
			close(firstStarted)
			<-allowFirst
			return RoomMutationResult{MutationID: firstID, AccountID: 41, DesiredCanonical: "20", OldCanonical: "10", NewCanonical: "20", Phase: RoomMutationReferencesSynced}, nil
		case "30":
			close(secondStarted)
			return RoomMutationResult{MutationID: secondID, AccountID: 41, DesiredCanonical: "30", OldCanonical: "20", NewCanonical: "30", Phase: RoomMutationReferencesSynced}, nil
		default:
			return RoomMutationResult{}, errors.New("unexpected room")
		}
	})
	expectHTTPRoomAudit(mock, firstID, "10", "20", 11)
	mock.ExpectQuery("SELECT a.id.*FROM streamer_accounts").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"id", "active", "room", "quota", "obs", "public", "created", "updated"}).AddRow(41, false, "20", 0, false, "", now, now))
	mock.ExpectQuery("SELECT event_type").WithArgs(int64(41), 20).WillReturnRows(sqlmock.NewRows([]string{"type", "account", "created"}))
	expectHTTPRoomAudit(mock, secondID, "20", "30", 12)
	mock.ExpectQuery("SELECT a.id.*FROM streamer_accounts").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"id", "active", "room", "quota", "obs", "public", "created", "updated"}).AddRow(41, false, "30", 0, false, "", now, now))
	mock.ExpectQuery("SELECT event_type").WithArgs(int64(41), 20).WillReturnRows(sqlmock.NewRows([]string{"type", "account", "created"}))
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
	select {
	case <-secondStarted:
		t.Fatal("second concurrent PUT entered mutation before first receipt was audited")
	case <-time.After(50 * time.Millisecond):
	}
	close(allowFirst)
	first := <-firstDone
	<-secondStarted
	second := <-secondDone
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("concurrent PUT status = %d/%d body=%s / %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectHTTPRoomAudit(mock sqlmock.Sqlmock, mutationID [16]byte, oldRoom, newRoom string, auditID int64) {
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_id, desired_room_id, old_room_id, new_room_id, phase, audit_event_id FROM room_mutation_receipts").WithArgs(mutationID[:]).
		WillReturnRows(sqlmock.NewRows([]string{"account", "desired", "old", "new", "phase", "audit"}).AddRow(41, newRoom, oldRoom, newRoom, string(RoomMutationReferencesSynced), nil))
	mock.ExpectExec("INSERT INTO audit_events").WithArgs(int64(41), []byte(`{"newRoomId":"`+newRoom+`","oldRoomId":"`+oldRoom+`"}`), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(auditID, 1))
	mock.ExpectExec("UPDATE room_mutation_receipts SET phase").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}
