package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestDisableAccountAuthorizesRecentAdministratorAndCommitsAtomicChanges(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedServiceKeyring(t)
	service, err := NewService(NewRepository(database), keys, &memoryVerifier{}, ServiceOptions{Now: nowFunc(now)})
	if err != nil {
		t.Fatal(err)
	}
	tokenHash, err := keys.HashToken("admin_session", []byte("administrator-session"))
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(7), nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET disabled_at = ?, credential_epoch = credential_epoch + 1 WHERE id = ? AND disabled_at IS NULL")).
		WithArgs(now, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?")).
		WithArgs(now, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("streamer_account_disabled", int64(1), int64(42), auditJSONArgument{
			wantReason: "security incident", forbidden: []string{"administrator-session"},
		}, now).
		WillReturnResult(sqlmock.NewResult(81, 1))
	mock.ExpectCommit()

	result, err := service.DisableAccount(context.Background(), "administrator-session", 42, "  security incident  ")
	if err != nil {
		t.Fatalf("DisableAccount() error = %v", err)
	}
	if result.AccountID != 42 || result.Status != AccountStatusDisabled {
		t.Fatalf("DisableAccount() = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnableAccountClearsDisabledStateWithoutRestoringCredentials(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 10, 0, 0, time.UTC)
	disabledAt := now.Add(-time.Hour)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedServiceKeyring(t)
	service, err := NewService(NewRepository(database), keys, &memoryVerifier{}, ServiceOptions{Now: nowFunc(now)})
	if err != nil {
		t.Fatal(err)
	}
	tokenHash, _ := keys.HashToken("admin_session", []byte("administrator-session"))

	mock.ExpectBegin()
	expectRecentAdministrator(mock, tokenHash, now, now.Add(-2*time.Minute))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(43)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(8), disabledAt))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET disabled_at = NULL WHERE id = ? AND disabled_at IS NOT NULL")).
		WithArgs(int64(43)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("streamer_account_enabled", int64(1), int64(43), auditJSONArgument{wantReason: "appeal accepted"}, now).
		WillReturnResult(sqlmock.NewResult(82, 1))
	mock.ExpectCommit()

	result, err := service.EnableAccount(context.Background(), "administrator-session", 43, " appeal accepted ")
	if err != nil {
		t.Fatalf("EnableAccount() error = %v", err)
	}
	if result.AccountID != 43 || result.Status != AccountStatusActive {
		t.Fatalf("EnableAccount() = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRebindVerifiedUIDForgetsProofAndCommitsEncryptedBindingAtomically(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 20, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedServiceKeyring(t)
	verifier := &memoryVerifier{verifications: []Verification{{UID: "987654321", CompletedAt: now}}}
	service, err := NewService(NewRepository(database), keys, verifier, ServiceOptions{Now: nowFunc(now), ChallengeTTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	tokenHash, _ := keys.HashToken("admin_session", []byte("administrator-session"))
	oldLookup := bytes.Repeat([]byte{0x44}, sha256.Size)
	newLookup, err := keys.Lookup("bili_uid", []byte("987654321"))
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(44)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(9), nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT uid_lookup FROM bili_uid_bindings WHERE account_id = ? AND unbound_at IS NULL FOR UPDATE")).
		WithArgs(int64(44)).
		WillReturnRows(sqlmock.NewRows([]string{"uid_lookup"}).AddRow(oldLookup))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
		WithArgs(newLookup).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE bili_uid_bindings SET unbound_at = ? WHERE account_id = ? AND unbound_at IS NULL")).
		WithArgs(now, int64(44)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)")).
		WithArgs(int64(44), encryptedUIDArgument{plaintext: "987654321"}, newLookup, now).
		WillReturnResult(sqlmock.NewResult(92, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET credential_epoch = credential_epoch + 1 WHERE id = ?")).
		WithArgs(int64(44)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?")).
		WithArgs(now, int64(44)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("streamer_account_uid_rebound", int64(1), int64(44), auditJSONArgument{
			wantReason: "verified ownership exception", wantOldLookup: oldLookup, wantNewLookup: newLookup,
			forbidden: []string{"987654321", "administrator-session"},
		}, now).
		WillReturnResult(sqlmock.NewResult(93, 1))
	mock.ExpectCommit()

	result, err := service.RebindVerifiedUID(context.Background(), "administrator-session", 44, "rebind-proof", " verified ownership exception ")
	if err != nil {
		t.Fatalf("RebindVerifiedUID() error = %v", err)
	}
	if result.AccountID != 44 || result.Status != AccountStatusActive {
		t.Fatalf("RebindVerifiedUID() = %#v", result)
	}
	assertForgottenExactly(t, verifier, "rebind-proof")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRebindRejectsNonAdministratorBeforeConsumingBilibiliProof(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 25, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedServiceKeyring(t)
	verifier := &memoryVerifier{verifications: []Verification{{UID: "987654321", CompletedAt: now}}}
	service, err := NewService(NewRepository(database), keys, verifier, ServiceOptions{Now: nowFunc(now)})
	if err != nil {
		t.Fatal(err)
	}
	tokenHash, _ := keys.HashToken("admin_session", []byte("streamer-or-invalid-session"))

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = service.RebindVerifiedUID(context.Background(), "streamer-or-invalid-session", 44, "victim-proof", "support exception")
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("RebindVerifiedUID() error = %v", err)
	}
	if verifier.polls != 0 || len(verifier.forgotten()) != 0 {
		t.Fatalf("unauthorized rebind consumed proof: polls=%d forget=%v", verifier.polls, verifier.forgotten())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdministratorAuthorizationUsesPersistedRecentTOTPBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 30, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Minute)
	tests := []struct {
		name         string
		expiresAt    time.Time
		revokedAt    any
		verifiedAt   any
		sessionEpoch int64
		adminEpoch   int64
		sessionError error
		wantError    error
	}{
		{name: "exactly five minutes is recent", expiresAt: now.Add(time.Hour), verifiedAt: now.Add(-5 * time.Minute), sessionEpoch: 4, adminEpoch: 4},
		{name: "future skew boundary accepted", expiresAt: now.Add(time.Hour), verifiedAt: now.Add(30 * time.Second), sessionEpoch: 4, adminEpoch: 4},
		{name: "older than five minutes", expiresAt: now.Add(time.Hour), verifiedAt: now.Add(-5*time.Minute - time.Nanosecond), sessionEpoch: 4, adminEpoch: 4, wantError: ErrRecentTOTPRequired},
		{name: "future beyond skew", expiresAt: now.Add(time.Hour), verifiedAt: now.Add(30*time.Second + time.Nanosecond), sessionEpoch: 4, adminEpoch: 4, wantError: ErrRecentTOTPRequired},
		{name: "missing recent totp", expiresAt: now.Add(time.Hour), sessionEpoch: 4, adminEpoch: 4, wantError: ErrRecentTOTPRequired},
		{name: "revoked admin session", expiresAt: now.Add(time.Hour), revokedAt: revokedAt, verifiedAt: now, sessionEpoch: 4, adminEpoch: 4, wantError: ErrAuthenticationFailed},
		{name: "expired admin session", expiresAt: now, verifiedAt: now, sessionEpoch: 4, adminEpoch: 4, wantError: ErrAuthenticationFailed},
		{name: "stale admin epoch", expiresAt: now.Add(time.Hour), verifiedAt: now, sessionEpoch: 3, adminEpoch: 4, wantError: ErrAuthenticationFailed},
		{name: "streamer or unknown session", adminEpoch: 4, sessionError: sql.ErrNoRows, wantError: ErrAuthenticationFailed},
		{name: "second query database failure rolls back generically", adminEpoch: 4, sessionError: errors.New("private administrator database detail"), wantError: ErrRepositoryUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			repository := NewRepository(database).(*sqlRepository)
			tokenHash := bytes.Repeat([]byte{0x51}, sha256.Size)
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
				WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(test.adminEpoch))
			expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).WithArgs(tokenHash)
			if test.sessionError != nil {
				expectation.WillReturnError(test.sessionError)
			} else {
				expectation.WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at", "totp_verified_at"}).
					AddRow(int64(9), test.sessionEpoch, test.expiresAt, test.revokedAt, test.verifiedAt))
			}
			if test.wantError == nil {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}

			err = repository.authorizeAccountAdministrator(context.Background(), tokenHash, now)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("authorizeAccountAdministrator() error = %v, want %v", err, test.wantError)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatalf("authorization error leaked database text: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDisableAccountRollsBackStatusEpochWhenRevokeOrAuditFails(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 40, 0, 0, time.UTC)
	for _, failure := range []string{"session revocation", "audit"} {
		t.Run(failure, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			keys := fixedServiceKeyring(t)
			service, err := NewService(NewRepository(database), keys, &memoryVerifier{}, ServiceOptions{Now: nowFunc(now)})
			if err != nil {
				t.Fatal(err)
			}
			tokenHash, _ := keys.HashToken("admin_session", []byte("administrator-session"))
			mock.ExpectBegin()
			expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
				WithArgs(int64(52)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(2), nil))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET disabled_at = ?, credential_epoch = credential_epoch + 1 WHERE id = ? AND disabled_at IS NULL")).
				WithArgs(now, int64(52)).WillReturnResult(sqlmock.NewResult(0, 1))
			revoke := mock.ExpectExec(regexp.QuoteMeta("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?")).WithArgs(now, int64(52))
			if failure == "session revocation" {
				revoke.WillReturnError(errors.New("private revoke failure"))
			} else {
				revoke.WillReturnResult(sqlmock.NewResult(0, 2))
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
					WithArgs("streamer_account_disabled", int64(1), int64(52), sqlmock.AnyArg(), now).
					WillReturnError(errors.New("private audit failure"))
			}
			mock.ExpectRollback()

			_, err = service.DisableAccount(context.Background(), "administrator-session", 52, "security")
			if !errors.Is(err, ErrRepositoryUnavailable) || strings.Contains(err.Error(), "private") {
				t.Fatalf("DisableAccount() error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnableAccountRollsBackWhenAuditWriteFails(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 45, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedServiceKeyring(t)
	service, err := NewService(NewRepository(database), keys, &memoryVerifier{}, ServiceOptions{Now: nowFunc(now)})
	if err != nil {
		t.Fatal(err)
	}
	tokenHash, _ := keys.HashToken("admin_session", []byte("administrator-session"))
	mock.ExpectBegin()
	expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(53)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(4), now.Add(-time.Hour)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET disabled_at = NULL WHERE id = ? AND disabled_at IS NOT NULL")).WithArgs(int64(53)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("streamer_account_enabled", int64(1), int64(53), sqlmock.AnyArg(), now).
		WillReturnError(errors.New("private enable audit failure"))
	mock.ExpectRollback()

	_, err = service.EnableAccount(context.Background(), "administrator-session", 53, "appeal")
	if !errors.Is(err, ErrRepositoryUnavailable) || strings.Contains(err.Error(), "private") {
		t.Fatalf("EnableAccount() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAccountMutationsClassifyInvalidCredentialEpochAsRepositoryUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 46, 0, 0, time.UTC)
	for _, operation := range []string{"disable", "enable", "rebind"} {
		t.Run(operation, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			repository := NewRepository(database).(*sqlRepository)
			tokenHash := bytes.Repeat([]byte{0x54}, sha256.Size)
			mock.ExpectBegin()
			expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
			disabledAt := any(nil)
			if operation == "enable" {
				disabledAt = now.Add(-time.Hour)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
				WithArgs(int64(54)).
				WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(0), disabledAt))
			mock.ExpectRollback()

			switch operation {
			case "disable":
				_, err = repository.disableAccount(context.Background(), tokenHash, 54, "security", now)
			case "enable":
				_, err = repository.enableAccount(context.Background(), tokenHash, 54, "appeal", now)
			case "rebind":
				_, err = repository.rebindAccount(context.Background(), tokenHash, 54, EncryptedUID{
					Ciphertext: []byte("encrypted-new-uid"), Lookup: bytes.Repeat([]byte{0x55}, sha256.Size),
				}, "support", now)
			}
			if !errors.Is(err, ErrRepositoryUnavailable) {
				t.Fatalf("%s invalid credential epoch error = %v", operation, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDisabledAccountCannotCreateNewSiteSession(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 47, 0, 0, time.UTC)
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	mock.ExpectExec("(?s)INSERT INTO site_sessions .*FROM streamer_accounts.*credential_epoch = .*disabled_at IS NULL").
		WithArgs(bytes.Repeat([]byte{0x55}, sha256.Size), now, now.Add(time.Hour), int64(54), int64(6)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repository.CreateSession(context.Background(), Session{
		AccountID: 54, TokenHash: bytes.Repeat([]byte{0x55}, sha256.Size), CredentialEpoch: 6,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateSession(disabled account) error = %v, want ErrNotFound", err)
	}
	assertSQLExpectations(t, mock)
}

func TestRebindRejectsSameOrPreviouslyBoundUIDAndMapsDuplicateInsertGenerically(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 50, 0, 0, time.UTC)
	newLookup := bytes.Repeat([]byte{0x62}, sha256.Size)
	tests := []struct {
		name       string
		oldLookup  []byte
		boundOwner any
		duplicate  bool
	}{
		{name: "same current uid", oldLookup: newLookup},
		{name: "historically bound uid", oldLookup: bytes.Repeat([]byte{0x61}, sha256.Size), boundOwner: int64(77)},
		{name: "duplicate insert race", oldLookup: bytes.Repeat([]byte{0x61}, sha256.Size), duplicate: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			repository := NewRepository(database).(*sqlRepository)
			tokenHash := bytes.Repeat([]byte{0x60}, sha256.Size)
			mock.ExpectBegin()
			expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(61)).
				WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(3), nil))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT uid_lookup FROM bili_uid_bindings WHERE account_id = ? AND unbound_at IS NULL FOR UPDATE")).WithArgs(int64(61)).
				WillReturnRows(sqlmock.NewRows([]string{"uid_lookup"}).AddRow(test.oldLookup))
			if !bytes.Equal(test.oldLookup, newLookup) {
				duplicateQuery := mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).WithArgs(newLookup)
				if test.boundOwner != nil {
					duplicateQuery.WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(test.boundOwner))
				} else {
					duplicateQuery.WillReturnError(sql.ErrNoRows)
					mock.ExpectExec(regexp.QuoteMeta("UPDATE bili_uid_bindings SET unbound_at = ? WHERE account_id = ? AND unbound_at IS NULL")).WithArgs(now, int64(61)).
						WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)")).
						WithArgs(int64(61), []byte("encrypted-new-uid"), newLookup, now).
						WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry UID 987654321 private"})
				}
			}
			mock.ExpectRollback()

			_, err = repository.rebindAccount(context.Background(), tokenHash, 61, EncryptedUID{Ciphertext: []byte("encrypted-new-uid"), Lookup: newLookup}, "exception", now)
			if !errors.Is(err, ErrAccountManagementFailed) || strings.Contains(err.Error(), "987654321") || strings.Contains(err.Error(), "Duplicate") {
				t.Fatalf("rebindAccount() error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRebindRollsBackBindingEpochWhenSessionRevocationOrAuditFails(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 55, 0, 0, time.UTC)
	oldLookup := bytes.Repeat([]byte{0x71}, sha256.Size)
	newLookup := bytes.Repeat([]byte{0x72}, sha256.Size)
	for _, failure := range []string{"session revocation", "audit"} {
		t.Run(failure, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			repository := NewRepository(database).(*sqlRepository)
			tokenHash := bytes.Repeat([]byte{0x70}, sha256.Size)
			mock.ExpectBegin()
			expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(70)).
				WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(5), nil))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT uid_lookup FROM bili_uid_bindings WHERE account_id = ? AND unbound_at IS NULL FOR UPDATE")).WithArgs(int64(70)).
				WillReturnRows(sqlmock.NewRows([]string{"uid_lookup"}).AddRow(oldLookup))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).WithArgs(newLookup).
				WillReturnError(sql.ErrNoRows)
			mock.ExpectExec(regexp.QuoteMeta("UPDATE bili_uid_bindings SET unbound_at = ? WHERE account_id = ? AND unbound_at IS NULL")).WithArgs(now, int64(70)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)")).
				WithArgs(int64(70), []byte("encrypted-new-uid"), newLookup, now).
				WillReturnResult(sqlmock.NewResult(101, 1))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET credential_epoch = credential_epoch + 1 WHERE id = ?")).WithArgs(int64(70)).
				WillReturnResult(sqlmock.NewResult(0, 1))
			revoke := mock.ExpectExec(regexp.QuoteMeta("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?")).WithArgs(now, int64(70))
			if failure == "session revocation" {
				revoke.WillReturnError(errors.New("private rebind revoke failure"))
			} else {
				revoke.WillReturnResult(sqlmock.NewResult(0, 3))
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
					WithArgs("streamer_account_uid_rebound", int64(1), int64(70), sqlmock.AnyArg(), now).
					WillReturnError(errors.New("private rebind audit failure"))
			}
			mock.ExpectRollback()

			_, err = repository.rebindAccount(context.Background(), tokenHash, 70, EncryptedUID{Ciphertext: []byte("encrypted-new-uid"), Lookup: newLookup}, "exception", now)
			if !errors.Is(err, ErrRepositoryUnavailable) || strings.Contains(err.Error(), "private") {
				t.Fatalf("rebindAccount() error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRebindBilibiliProofForgettingMatchesTerminalState(t *testing.T) {
	now := time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		verification Verification
		pollError    error
		wantError    error
		wantForget   bool
	}{
		{name: "pending retained", pollError: ErrVerificationPending, wantError: ErrVerificationPending},
		{name: "temporary unavailable retained", pollError: ErrVerificationUnavailable, wantError: ErrVerificationUnavailable},
		{name: "terminal verifier failure forgotten", pollError: errors.New("terminal private UID 987654321"), wantError: ErrAuthenticationFailed, wantForget: true},
		{name: "expired successful proof forgotten", verification: Verification{UID: "987654321", CompletedAt: now.Add(-5*time.Minute - time.Nanosecond)}, wantError: ErrAuthenticationFailed, wantForget: true},
		{name: "malformed uid forgotten", verification: Verification{UID: "not-a-uid", CompletedAt: now}, wantError: ErrAuthenticationFailed, wantForget: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			keys := fixedServiceKeyring(t)
			verifier := &memoryVerifier{verifications: []Verification{test.verification}, pollErrs: []error{test.pollError}}
			service, err := NewService(NewRepository(database), keys, verifier, ServiceOptions{Now: nowFunc(now), ChallengeTTL: 5 * time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			tokenHash, _ := keys.HashToken("admin_session", []byte("administrator-session"))
			mock.ExpectBegin()
			expectRecentAdministrator(mock, tokenHash, now, now.Add(-time.Minute))
			mock.ExpectCommit()

			_, err = service.RebindVerifiedUID(context.Background(), "administrator-session", 61, "proof-state", "exception")
			if !errors.Is(err, test.wantError) || strings.Contains(err.Error(), "987654321") {
				t.Fatalf("RebindVerifiedUID() error = %v, want %v", err, test.wantError)
			}
			forgotten := verifier.forgotten()
			if (len(forgotten) == 1) != test.wantForget || (test.wantForget && forgotten[0] != "proof-state") {
				t.Fatalf("Forget calls = %v, wantForget=%v", forgotten, test.wantForget)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectRecentAdministrator(mock sqlmock.Sqlmock, tokenHash []byte, now, verifiedAt time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(int64(9), int64(4), now.Add(time.Hour), nil, verifiedAt))
}

type auditJSONArgument struct {
	wantReason    string
	wantOldLookup []byte
	wantNewLookup []byte
	forbidden     []string
}

func (argument auditJSONArgument) Match(value driver.Value) bool {
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return false
	}
	for _, forbidden := range argument.forbidden {
		if bytes.Contains(raw, []byte(forbidden)) {
			return false
		}
	}
	var body struct {
		Reason       string `json:"reason"`
		OldUIDLookup []byte `json:"oldUidLookup"`
		NewUIDLookup []byte `json:"newUidLookup"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Reason != argument.wantReason ||
		!bytes.Equal(body.OldUIDLookup, argument.wantOldLookup) || !bytes.Equal(body.NewUIDLookup, argument.wantNewLookup) {
		return false
	}
	return true
}

type encryptedUIDArgument struct {
	plaintext string
}

func (argument encryptedUIDArgument) Match(value driver.Value) bool {
	ciphertext, ok := value.([]byte)
	return ok && len(ciphertext) > 32 && !bytes.Contains(ciphertext, []byte(argument.plaintext))
}
