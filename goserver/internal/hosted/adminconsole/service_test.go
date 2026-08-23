package adminconsole

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCursorRoundTripAndValidation(t *testing.T) {
	want := Cursor{CreatedAt: time.Date(2026, 8, 23, 8, 30, 0, 123, time.UTC), ID: 41}
	encoded := encodeCursor(want)
	got, err := decodeCursor(encoded)
	if err != nil || got != want {
		t.Fatalf("decodeCursor(%q) = %#v, %v; want %#v", encoded, got, err, want)
	}
	for _, value := range []string{"", "not-base64", "e30", encodeCursor(Cursor{})} {
		if _, err := decodeCursor(value); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("decodeCursor(%q) error = %v, want ErrInvalidCursor", value, err)
		}
	}
}

func TestNormalizeAccountQuery(t *testing.T) {
	query, err := normalizeAccountQuery(AccountQuery{Query: " 123456 ", Status: AccountStatusActive, Attention: AttentionMissingRoom, Limit: 250})
	if err != nil {
		t.Fatal(err)
	}
	if query.Query != "123456" || query.Limit != 100 {
		t.Fatalf("normalized query = %#v", query)
	}
	for _, invalid := range []AccountQuery{{Status: "unknown"}, {Attention: "cookie"}, {Limit: -1}} {
		if _, err := normalizeAccountQuery(invalid); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("normalizeAccountQuery(%#v) error = %v", invalid, err)
		}
	}
}

func TestAccountProjectionContainsNoCredentialFields(t *testing.T) {
	account := AccountDetail{AccountSummary: AccountSummary{ID: 41, Status: AccountStatusActive, RoomID: "123456"}}
	if account.ID != 41 || account.RoomID != "123456" {
		t.Fatalf("projection = %#v", account)
	}
	encoded, err := json.Marshal(account)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"uid", "cookie", "eventData", "credential"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("projection leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestAccountsUsesStablePaginationAndCapsLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 23, 8, 30, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT a.id.*FROM streamer_accounts").WithArgs(101).
		WillReturnRows(sqlmock.NewRows([]string{"id", "active", "room", "quota", "obs", "created", "updated"}).
			AddRow(52, true, "123456", 8, true, now, now).
			AddRow(41, false, "", 0, false, now.Add(-time.Minute), now).
			AddRow(30, true, "789", 1, true, now.Add(-2*time.Minute), now))
	service, _ := NewService(db, "https://panel.example.com")
	page, err := service.Accounts(context.Background(), AccountQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0].Status != AccountStatusActive || page.Items[1].Status != AccountStatusDisabled {
		t.Fatalf("page = %#v", page)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBatchValidationAndStableMixedResults(t *testing.T) {
	calls := []int64{}
	service := &Service{mutations: MutationServices{Disable: func(_ context.Context, _ string, id int64, _ string) error {
		calls = append(calls, id)
		if id == 52 {
			return errors.New("already disabled")
		}
		return nil
	}}}
	result, err := service.Batch(context.Background(), "admin-session", BatchRequest{AccountIDs: []int64{41, 52, 68}, Action: BatchDisable, Reason: "maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 3 || result.Results[0].Status != BatchSucceeded || result.Results[1].Status != BatchFailed || result.Results[2].AccountID != 68 {
		t.Fatalf("result = %#v", result)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v", calls)
	}
	for _, invalid := range []BatchRequest{
		{Action: BatchDisable, AccountIDs: nil, Reason: "reason"},
		{Action: BatchDisable, AccountIDs: []int64{41, 41}, Reason: "reason"},
		{Action: BatchQuota, AccountIDs: []int64{41}, Reason: "reason"},
		{Action: BatchQuota, AccountIDs: []int64{41}, RemainingQuota: int64Pointer(-1), Reason: "reason"},
		{Action: BatchEnable, AccountIDs: []int64{41}, Reason: "   "},
	} {
		if _, err := service.Batch(context.Background(), "admin-session", invalid); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("Batch(%#v) error=%v", invalid, err)
		}
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestUpdateRoomAuditsAndReturnsRefreshedDetail(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE.*FROM streamer_accounts").WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"room"}).AddRow("111"))
	mock.ExpectExec("INSERT INTO account_runtime_rooms").WithArgs(int64(41), "123456", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO audit_events").WithArgs(int64(41), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT a.id.*FROM streamer_accounts").WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "active", "room", "quota", "obs", "public", "created", "updated"}).AddRow(41, true, "123456", 8, false, "", now, now))
	mock.ExpectQuery("SELECT event_type").WithArgs(int64(41), 20).
		WillReturnRows(sqlmock.NewRows([]string{"type", "account", "created"}).AddRow("admin_room_updated", 41, now))
	service, _ := NewService(db, "https://panel.example.com")
	detail, err := service.UpdateRoom(context.Background(), 41, "123456")
	if err != nil || detail.RoomID != "123456" || len(detail.RecentEvents) != 1 {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}
	if _, err := service.UpdateRoom(context.Background(), 41, "0123"); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid room error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
