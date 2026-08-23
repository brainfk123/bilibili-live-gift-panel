package adminsettings

import (
	"bilibili-live-gift-panel/internal/hosted/security"
	"bytes"
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
	"time"
)

type sessions struct{ err error }

func (s sessions) RequireSession(context.Context, string) error { return s.err }
func testKeys(t *testing.T) security.Keyring {
	t.Helper()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}
func TestSettingsMasksEmailAndReturnsNoSecrets(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys := testKeys(t)
	cipher, _ := keys.Seal("admin_email", []byte("owner@example.com"))
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT a.email_ciphertext").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"email", "expiry", "totp", "recovery"}).AddRow(cipher, now.Add(30*24*time.Hour), true, now))
	service, _ := NewService(db, keys, sessions{}, func(context.Context) string { return "healthy" })
	value, err := service.Settings(context.Background(), "admin-token")
	if err != nil || value.MaskedEmail != "o***@example.com" || !value.TOTPEnabled || value.ServiceHealth != "healthy" {
		t.Fatalf("settings=%#v err=%v", value, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestRevokeOthersPreservesCurrentHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keys := testKeys(t)
	mock.ExpectExec("UPDATE site_sessions SET revoked_at").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 3))
	service, _ := NewService(db, keys, sessions{}, nil)
	if err := service.RevokeOtherSessions(context.Background(), "current-token"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
