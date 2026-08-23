package adminsettings

import (
	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/security"
	"bytes"
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
	"time"
)

type sessions struct {
	err         error
	inventory   []adminidentity.AdministratorSession
	loginEvents []adminidentity.AdministratorLoginEvent
	revokeErr   error
	revokedID   string
	loginLimit  int
}

func (s sessions) RequireSession(context.Context, string) error { return s.err }
func (s sessions) AdministratorSessions(context.Context, string) ([]adminidentity.AdministratorSession, error) {
	return append([]adminidentity.AdministratorSession(nil), s.inventory...), s.err
}
func (s *sessions) RevokeAdministratorSession(_ context.Context, _ string, publicID string) error {
	s.revokedID = publicID
	return s.revokeErr
}
func (s *sessions) AdministratorLoginEvents(_ context.Context, _ string, limit int) ([]adminidentity.AdministratorLoginEvent, error) {
	s.loginLimit = limit
	return append([]adminidentity.AdministratorLoginEvent(nil), s.loginEvents...), s.err
}
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
	service, _ := NewService(db, keys, &sessions{}, func(context.Context) string { return "healthy" })
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
	service, _ := NewService(db, keys, &sessions{}, nil)
	if err := service.RevokeOtherSessions(context.Background(), "current-token"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionInventoryProjectionSortsCurrentFirstAndFormatsUTC(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	location := time.FixedZone("client", 8*60*60)
	base := time.Date(2026, 8, 23, 16, 0, 0, 0, location)
	validator := &sessions{inventory: []adminidentity.AdministratorSession{
		{PublicID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", DeviceLabel: "Windows · Edge", ClientNetwork: "198.51.100.*", CreatedAt: base.Add(-2 * time.Hour), LastSeenAt: base.Add(-time.Minute), ExpiresAt: base.Add(time.Hour)},
		{PublicID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DeviceLabel: "iPhone · Safari", ClientNetwork: "203.0.113.*", CreatedAt: base.Add(-time.Hour), LastSeenAt: base.Add(-2 * time.Minute), ExpiresAt: base.Add(2 * time.Hour), Current: true},
	}}
	service, _ := NewService(db, testKeys(t), validator, nil)
	got, err := service.Sessions(context.Background(), "current")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || !got[0].Current || got[1].ID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("sessions=%#v", got)
	}
	if got[0].CreatedAt != "2026-08-23T07:00:00Z" || got[0].LastSeenAt != "2026-08-23T07:58:00Z" || got[0].ExpiresAt != "2026-08-23T10:00:00Z" {
		t.Fatalf("UTC timestamps=%#v", got[0])
	}
}

func TestLoginEventProjectionSortsNewestAndForwardsBoundedLimit(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 23, 8, 30, 0, 0, time.UTC)
	validator := &sessions{loginEvents: []adminidentity.AdministratorLoginEvent{
		{Result: "failure", DeviceLabel: "Android · Chrome", ClientNetwork: "2001:db8:abcd:1234::*", OccurredAt: now.Add(-time.Minute)},
		{Result: "success", DeviceLabel: "iPhone · Safari", ClientNetwork: "203.0.113.*", OccurredAt: now},
	}}
	service, _ := NewService(db, testKeys(t), validator, nil)
	got, err := service.LoginEvents(context.Background(), "current", 20)
	if err != nil {
		t.Fatal(err)
	}
	if validator.loginLimit != 20 || len(got) != 2 || got[0].Result != "success" || got[0].OccurredAt != "2026-08-23T08:30:00Z" {
		t.Fatalf("limit=%d events=%#v", validator.loginLimit, got)
	}
}
