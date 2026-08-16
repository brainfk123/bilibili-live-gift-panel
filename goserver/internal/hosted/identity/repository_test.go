package identity

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestCreateBoundAccountCommitsAccountAndBindingTogether(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	uid := EncryptedUID{
		Ciphertext: []byte("versioned-encrypted-uid"),
		Lookup:     bytes.Repeat([]byte{3}, 32),
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO streamer_accounts () VALUES ()")).
		WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup) VALUES (?, ?, ?)")).
		WithArgs(int64(42), uid.Ciphertext, uid.Lookup).
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectCommit()

	account, err := repository.CreateBoundAccount(context.Background(), uid)
	if err != nil {
		t.Fatalf("CreateBoundAccount() error = %v", err)
	}
	if account.ID != 42 || account.CredentialEpoch != 1 {
		t.Fatalf("CreateBoundAccount() = %#v, want ID 42 and credential epoch 1", account)
	}
	assertSQLExpectations(t, mock)
}

func TestCreateBoundAccountRollsBackAndMapsDuplicateUIDSafely(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	uid := EncryptedUID{
		Ciphertext: []byte("ciphertext-that-must-not-leak"),
		Lookup:     bytes.Repeat([]byte{4}, 32),
	}
	databaseMessage := "Duplicate entry secret-uid-123 for key uq_bili_uid_bindings_uid_lookup"

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO streamer_accounts () VALUES ()")).
		WillReturnResult(sqlmock.NewResult(43, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup) VALUES (?, ?, ?)")).
		WithArgs(int64(43), uid.Ciphertext, uid.Lookup).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: databaseMessage})
	mock.ExpectRollback()

	_, err := repository.CreateBoundAccount(context.Background(), uid)
	if !errors.Is(err, ErrUIDAlreadyBound) {
		t.Fatalf("CreateBoundAccount() error = %v, want ErrUIDAlreadyBound", err)
	}
	for _, forbidden := range []string{databaseMessage, "secret-uid-123", string(uid.Ciphertext)} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("CreateBoundAccount() error exposed secret/database text: %v", err)
		}
	}
	assertSQLExpectations(t, mock)
}

func TestCreateBoundAccountRollsBackGenericDatabaseFailureWithoutReflectingIt(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	databaseMessage := "database failure containing private-token"

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO streamer_accounts () VALUES ()")).
		WillReturnError(errors.New(databaseMessage))
	mock.ExpectRollback()

	_, err := repository.CreateBoundAccount(context.Background(), EncryptedUID{
		Ciphertext: []byte("encrypted"),
		Lookup:     bytes.Repeat([]byte{5}, 32),
	})
	if !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("CreateBoundAccount() error = %v, want ErrRepositoryUnavailable", err)
	}
	if strings.Contains(err.Error(), databaseMessage) || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("CreateBoundAccount() error exposed database text: %v", err)
	}
	assertSQLExpectations(t, mock)
}

func TestFindAccountByUIDLookupReturnsAccountState(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	lookup := bytes.Repeat([]byte{6}, 32)
	createdAt := time.Date(2026, 8, 16, 1, 2, 3, 4, time.UTC)
	updatedAt := createdAt.Add(time.Minute)

	mock.ExpectQuery("(?s)SELECT a.id, a.credential_epoch, a.disabled_at, a.created_at, a.updated_at.*FROM bili_uid_bindings AS b.*JOIN streamer_accounts AS a.*WHERE b.uid_lookup = .*b.unbound_at IS NULL.*LIMIT 1").
		WithArgs(lookup).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "disabled_at", "created_at", "updated_at"}).
			AddRow(int64(9), int64(3), nil, createdAt, updatedAt))

	account, err := repository.FindAccountByUIDLookup(context.Background(), lookup)
	if err != nil {
		t.Fatalf("FindAccountByUIDLookup() error = %v", err)
	}
	if account.ID != 9 || account.CredentialEpoch != 3 || account.DisabledAt != nil || !account.CreatedAt.Equal(createdAt) || !account.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("FindAccountByUIDLookup() = %#v, want active account row", account)
	}
	assertSQLExpectations(t, mock)
}

func TestCreateSessionPersistsOnlyHashForCurrentActiveEpoch(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	createdAt := time.Date(2026, 8, 16, 2, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(24 * time.Hour)
	tokenHash := bytes.Repeat([]byte{7}, 32)
	session := Session{
		AccountID:       11,
		TokenHash:       tokenHash,
		CredentialEpoch: 2,
		CreatedAt:       createdAt,
		ExpiresAt:       expiresAt,
	}

	mock.ExpectExec("(?s)INSERT INTO site_sessions .*SELECT id, .*credential_epoch.*FROM streamer_accounts.*WHERE id = .*credential_epoch = .*disabled_at IS NULL").
		WithArgs(tokenHash, createdAt, expiresAt, int64(11), int64(2)).
		WillReturnResult(sqlmock.NewResult(21, 1))

	if err := repository.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	assertSQLExpectations(t, mock)
}

func TestFindSessionByHashReturnsMinimalActiveSession(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	totpVerifiedAt := now.Add(-time.Minute)
	tokenHash := bytes.Repeat([]byte{8}, 32)

	expectFindSession(mock, tokenHash, now, sessionRow{
		ID:                     31,
		AccountID:              12,
		SessionCredentialEpoch: 4,
		ExpiresAt:              expiresAt,
		TOTPVerifiedAt:         &totpVerifiedAt,
		AccountCredentialEpoch: 4,
	})

	session, err := repository.FindSessionByHash(context.Background(), tokenHash, now)
	if err != nil {
		t.Fatalf("FindSessionByHash() error = %v", err)
	}
	if session.ID != 31 || session.AccountID != 12 || session.CredentialEpoch != 4 || !session.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("FindSessionByHash() = %#v, want active minimal session", session)
	}
	if session.TOTPVerifiedAt == nil || !session.TOTPVerifiedAt.Equal(totpVerifiedAt) {
		t.Fatalf("FindSessionByHash() TOTPVerifiedAt = %v, want %v", session.TOTPVerifiedAt, totpVerifiedAt)
	}
	if session.TokenHash != nil || !session.CreatedAt.IsZero() || session.RevokedAt != nil {
		t.Fatalf("FindSessionByHash() returned persistence-only or terminal fields: %#v", session)
	}
	assertSQLExpectations(t, mock)
}

func TestFindSessionByHashRejectsExpiredRevokedDisabledOrStaleEpoch(t *testing.T) {
	now := time.Date(2026, 8, 16, 4, 0, 0, 0, time.UTC)
	terminalAt := now.Add(-time.Minute)
	tests := []struct {
		name string
		row  sessionRow
	}{
		{name: "expired", row: sessionRow{ID: 1, AccountID: 2, SessionCredentialEpoch: 3, ExpiresAt: now, AccountCredentialEpoch: 3}},
		{name: "revoked", row: sessionRow{ID: 1, AccountID: 2, SessionCredentialEpoch: 3, ExpiresAt: now.Add(time.Hour), RevokedAt: &terminalAt, AccountCredentialEpoch: 3}},
		{name: "disabled account", row: sessionRow{ID: 1, AccountID: 2, SessionCredentialEpoch: 3, ExpiresAt: now.Add(time.Hour), AccountDisabledAt: &terminalAt, AccountCredentialEpoch: 3}},
		{name: "stale credential epoch", row: sessionRow{ID: 1, AccountID: 2, SessionCredentialEpoch: 3, ExpiresAt: now.Add(time.Hour), AccountCredentialEpoch: 4}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, mock, closeDB := newMockRepository(t)
			defer closeDB()
			tokenHash := bytes.Repeat([]byte{9}, 32)
			expectFindSession(mock, tokenHash, now, test.row)

			if _, err := repository.FindSessionByHash(context.Background(), tokenHash, now); !errors.Is(err, ErrNotFound) {
				t.Fatalf("FindSessionByHash() error = %v, want ErrNotFound", err)
			}
			assertSQLExpectations(t, mock)
		})
	}
}

func TestFindSessionByHashMapsMissingAndDatabaseErrorsSafely(t *testing.T) {
	tests := []struct {
		name      string
		database  error
		want      error
		forbidden string
	}{
		{name: "missing", database: sql.ErrNoRows, want: ErrNotFound},
		{name: "database unavailable", database: errors.New("driver error with secret-token"), want: ErrRepositoryUnavailable, forbidden: "secret-token"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, mock, closeDB := newMockRepository(t)
			defer closeDB()
			now := time.Date(2026, 8, 16, 5, 0, 0, 0, time.UTC)
			tokenHash := bytes.Repeat([]byte{10}, 32)
			mock.ExpectQuery("(?s)SELECT s.id, s.account_id, s.credential_epoch, s.expires_at.*FROM site_sessions AS s.*JOIN streamer_accounts AS a.*WHERE s.token_hash = .*LIMIT 1").
				WithArgs(tokenHash).
				WillReturnError(test.database)

			_, err := repository.FindSessionByHash(context.Background(), tokenHash, now)
			if !errors.Is(err, test.want) {
				t.Fatalf("FindSessionByHash() error = %v, want %v", err, test.want)
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("FindSessionByHash() error exposed database text: %v", err)
			}
			assertSQLExpectations(t, mock)
		})
	}
}

func TestRevokeSessionIsParameterizedAndIdempotent(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	tokenHash := bytes.Repeat([]byte{11}, 32)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP(6)) WHERE token_hash = ?")).
		WithArgs(tokenHash).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := repository.RevokeSession(context.Background(), tokenHash); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	assertSQLExpectations(t, mock)
}

func TestRepositoryRejectsMalformedInputsBeforeSQL(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 16, 6, 0, 0, 0, time.UTC)

	if _, err := repository.FindAccountByUIDLookup(context.Background(), []byte("short")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("FindAccountByUIDLookup() error = %v, want ErrInvalidInput", err)
	}
	if _, err := repository.CreateBoundAccount(context.Background(), EncryptedUID{Ciphertext: nil, Lookup: bytes.Repeat([]byte{1}, 32)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateBoundAccount() error = %v, want ErrInvalidInput", err)
	}
	if err := repository.CreateSession(context.Background(), Session{AccountID: 1, TokenHash: []byte("short"), CredentialEpoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateSession() error = %v, want ErrInvalidInput", err)
	}
	if _, err := repository.FindSessionByHash(context.Background(), []byte("short"), now); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("FindSessionByHash() error = %v, want ErrInvalidInput", err)
	}
	if err := repository.RevokeSession(context.Background(), []byte("short")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RevokeSession() error = %v, want ErrInvalidInput", err)
	}
	assertSQLExpectations(t, mock)
}

type sessionRow struct {
	ID                     int64
	AccountID              int64
	SessionCredentialEpoch int64
	ExpiresAt              time.Time
	RevokedAt              *time.Time
	TOTPVerifiedAt         *time.Time
	AccountDisabledAt      *time.Time
	AccountCredentialEpoch int64
}

func expectFindSession(mock sqlmock.Sqlmock, tokenHash []byte, now time.Time, row sessionRow) {
	mock.ExpectQuery("(?s)SELECT s.id, s.account_id, s.credential_epoch, s.expires_at.*s.revoked_at, s.totp_verified_at, a.disabled_at, a.credential_epoch.*FROM site_sessions AS s.*JOIN streamer_accounts AS a.*WHERE s.token_hash = .*LIMIT 1").
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "session_credential_epoch", "expires_at", "revoked_at", "totp_verified_at", "disabled_at", "account_credential_epoch",
		}).AddRow(
			row.ID,
			row.AccountID,
			row.SessionCredentialEpoch,
			row.ExpiresAt,
			nullableTime(row.RevokedAt),
			nullableTime(row.TOTPVerifiedAt),
			nullableTime(row.AccountDisabledAt),
			row.AccountCredentialEpoch,
		))
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func newMockRepository(t *testing.T) (Repository, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	return NewRepository(db), mock, func() { _ = db.Close() }
}

func assertSQLExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}
