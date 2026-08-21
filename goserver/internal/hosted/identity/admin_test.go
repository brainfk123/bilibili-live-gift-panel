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

	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSensitiveDisableAccountRenewsOnlyAfterAuditInSameTransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorizedAt := time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)
	completedAt := authorizedAt.Add(2 * time.Second)
	clock := &identityTimeSequence{values: []time.Time{authorizedAt, completedAt}}
	authorizer := &identitySensitiveAuthorizer{writeMarkers: true}
	disableHookCalls := 0
	service, err := NewService(NewRepository(database, authorizer), fixedServiceKeyring(t), &memoryVerifier{}, ServiceOptions{
		Now: clock.Now, OnAccountDisabled: func(int64) { disableHookCalls++ },
	})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("sensitive_authorize").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(6), nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET disabled_at = ?, credential_epoch = credential_epoch + 1 WHERE id = ? AND disabled_at IS NULL")).
		WithArgs(authorizedAt, int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?")).
		WithArgs(authorizedAt, int64(42)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("streamer_account_disabled", int64(1), int64(42), sqlmock.AnyArg(), authorizedAt).
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectExec("sensitive_renew").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := service.DisableAccount(context.Background(), "administrator-session", 42, "policy violation")
	if err != nil {
		t.Fatalf("DisableAccount() error = %v", err)
	}
	if result.AccountID != 42 || result.Status != AccountStatusDisabled || disableHookCalls != 1 {
		t.Fatalf("DisableAccount() = %#v, hook calls = %d", result, disableHookCalls)
	}
	if authorizer.authorizeCalls != 1 || authorizer.renewCalls != 1 || authorizer.authorizeTx != authorizer.renewTx {
		t.Fatalf("sensitive calls authorize=%d renew=%d sameTx=%t", authorizer.authorizeCalls, authorizer.renewCalls, authorizer.authorizeTx == authorizer.renewTx)
	}
	if !authorizer.authorizedAt.Equal(authorizedAt) || !authorizer.renewedAt.Equal(completedAt) {
		t.Fatalf("sensitive timestamps authorize=%s renew=%s", authorizer.authorizedAt, authorizer.renewedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveDisableRenewalFailureRollsBackDomainAuditAndHook(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 21, 13, 10, 0, 0, time.UTC)
	authorizer := &identitySensitiveAuthorizer{writeMarkers: true, renewErr: errors.New("private renewal failure")}
	disableHookCalls := 0
	service, err := NewService(NewRepository(database, authorizer), fixedServiceKeyring(t), &memoryVerifier{}, ServiceOptions{
		Now: func() time.Time { return now }, OnAccountDisabled: func(int64) { disableHookCalls++ },
	})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("sensitive_authorize").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(6), nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET disabled_at = ?, credential_epoch = credential_epoch + 1 WHERE id = ? AND disabled_at IS NULL")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?")).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectExec("sensitive_renew").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	if _, err := service.DisableAccount(context.Background(), "administrator-session", 42, "policy violation"); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("DisableAccount() error = %v, want ErrRepositoryUnavailable", err)
	}
	if disableHookCalls != 0 {
		t.Fatalf("rollback invoked disable hook %d times", disableHookCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveDisableMapsRecentTOTPBeforeDomainWork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 21, 13, 20, 0, 0, time.UTC)
	authorizer := &identitySensitiveAuthorizer{authorizeErr: security.ErrSensitiveRecentTOTPRequired}
	service, err := NewService(NewRepository(database, authorizer), fixedServiceKeyring(t), &memoryVerifier{}, ServiceOptions{Now: nowFunc(now)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectRollback()

	if _, err := service.DisableAccount(context.Background(), "administrator-session", 42, "policy violation"); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("DisableAccount() error = %v, want ErrRecentTOTPRequired", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDisableAccountAuthorizesRecentAdministratorAndCommitsAtomicChanges(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedServiceKeyring(t)
	authorizer := &identitySensitiveAuthorizer{}
	disableHookCalls := 0
	service, err := NewService(NewRepository(database, authorizer), keys, &memoryVerifier{}, ServiceOptions{
		Now: nowFunc(now),
		OnAccountDisabled: func(accountID int64) {
			disableHookCalls++
			if accountID != 42 {
				t.Fatalf("OnAccountDisabled accountID = %d, want 42", accountID)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("OnAccountDisabled fired before disable transaction committed: %v", err)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
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
	if disableHookCalls != 1 {
		t.Fatalf("OnAccountDisabled calls = %d, want exactly 1", disableHookCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveEnableAccountRenewsAfterAuditWithoutRestoringCredentials(t *testing.T) {
	authorizedAt := time.Date(2026, 8, 16, 14, 10, 0, 0, time.UTC)
	completedAt := authorizedAt.Add(2 * time.Second)
	disabledAt := authorizedAt.Add(-time.Hour)
	clock := &identityTimeSequence{values: []time.Time{authorizedAt, completedAt}}
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedServiceKeyring(t)
	authorizer := &identitySensitiveAuthorizer{writeMarkers: true}
	service, err := NewService(NewRepository(database, authorizer), keys, &memoryVerifier{}, ServiceOptions{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("sensitive_authorize").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(43)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(8), disabledAt))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET disabled_at = NULL WHERE id = ? AND disabled_at IS NOT NULL")).
		WithArgs(int64(43)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("streamer_account_enabled", int64(1), int64(43), auditJSONArgument{wantReason: "appeal accepted"}, authorizedAt).
		WillReturnResult(sqlmock.NewResult(82, 1))
	mock.ExpectExec("sensitive_renew").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := service.EnableAccount(context.Background(), "administrator-session", 43, " appeal accepted ")
	if err != nil {
		t.Fatalf("EnableAccount() error = %v", err)
	}
	if result.AccountID != 43 || result.Status != AccountStatusActive {
		t.Fatalf("EnableAccount() = %#v", result)
	}
	if authorizer.authorizeCalls != 1 || authorizer.renewCalls != 1 || authorizer.authorizeTx != authorizer.renewTx {
		t.Fatalf("sensitive calls authorize=%d renew=%d sameTx=%t", authorizer.authorizeCalls, authorizer.renewCalls, authorizer.authorizeTx == authorizer.renewTx)
	}
	if !authorizer.authorizedAt.Equal(authorizedAt) || !authorizer.renewedAt.Equal(completedAt) {
		t.Fatalf("sensitive timestamps authorize=%s renew=%s", authorizer.authorizedAt, authorizer.renewedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
			authorizer := &identitySensitiveAuthorizer{}
			disableHookCalls := 0
			service, err := NewService(NewRepository(database, authorizer), keys, &memoryVerifier{}, ServiceOptions{Now: nowFunc(now), OnAccountDisabled: func(int64) { disableHookCalls++ }})
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectBegin()
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
			if disableHookCalls != 0 {
				t.Fatalf("OnAccountDisabled calls = %d after rollback, want 0", disableHookCalls)
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
	authorizer := &identitySensitiveAuthorizer{}
	service, err := NewService(NewRepository(database, authorizer), keys, &memoryVerifier{}, ServiceOptions{Now: nowFunc(now)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
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
	for _, operation := range []string{"disable", "enable"} {
		t.Run(operation, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			authorizer := &identitySensitiveAuthorizer{}
			repository := NewRepository(database, authorizer).(*sqlRepository)
			mock.ExpectBegin()
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
				_, err = repository.disableAccount(context.Background(), "administrator-session", 54, "security", nowFunc(now))
			case "enable":
				_, err = repository.enableAccount(context.Background(), "administrator-session", 54, "appeal", nowFunc(now))
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

type auditJSONArgument struct {
	wantReason    string
	wantOldLookup []byte
	wantNewLookup []byte
	forbidden     []string
}

type identitySensitiveAuthorizer struct {
	writeMarkers   bool
	authorizeErr   error
	renewErr       error
	authorizeCalls int
	renewCalls     int
	authorizedAt   time.Time
	renewedAt      time.Time
	authorizeTx    *sql.Tx
	renewTx        *sql.Tx
}

func (authorizer *identitySensitiveAuthorizer) AuthorizeRecentTOTP(ctx context.Context, transaction *sql.Tx, _ string, now time.Time) (security.SensitiveSession, error) {
	authorizer.authorizeCalls++
	authorizer.authorizedAt = now
	authorizer.authorizeTx = transaction
	if authorizer.authorizeErr != nil {
		return security.SensitiveSession{}, authorizer.authorizeErr
	}
	if authorizer.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_authorize"); err != nil {
			return security.SensitiveSession{}, err
		}
	}
	return security.SensitiveSession{}, nil
}

func (authorizer *identitySensitiveAuthorizer) RenewRecentTOTP(ctx context.Context, transaction *sql.Tx, _ security.SensitiveSession, now time.Time) error {
	authorizer.renewCalls++
	authorizer.renewedAt = now
	authorizer.renewTx = transaction
	if authorizer.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_renew"); err != nil {
			return err
		}
	}
	return authorizer.renewErr
}

type identityTimeSequence struct {
	values []time.Time
	index  int
}

func (sequence *identityTimeSequence) Now() time.Time {
	if sequence.index >= len(sequence.values) {
		return sequence.values[len(sequence.values)-1]
	}
	value := sequence.values[sequence.index]
	sequence.index++
	return value
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
