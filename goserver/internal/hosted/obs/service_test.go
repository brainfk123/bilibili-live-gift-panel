package obs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestOBSCredentialResetUsesSessionOnlyAndAudits(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorizedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	clock := &obsTimeSequence{values: []time.Time{authorizedAt}}
	authorizer := &adminAuthorizerStub{}
	random := bytes.NewReader(append(bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)...))
	service, err := NewService(database, authorizer, ServiceOptions{Now: clock.Now, Random: random, PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(7), nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_sessions AS s JOIN obs_credentials AS c ON c.id = s.obs_credential_id SET s.revoked_at = ? WHERE c.account_id = ? AND s.revoked_at IS NULL")).
		WithArgs(authorizedAt, int64(41)).WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_credentials SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL")).
		WithArgs(authorizedAt, int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO obs_credentials (account_id, public_id, token_hash, credential_epoch, created_at) VALUES (?, ?, ?, ?, ?)")).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("obs_credential_reset", int64(1), int64(41), []byte("{}"), authorizedAt).
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectCommit()

	if _, err := service.Issue(context.Background(), "administrator-secret", 41); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if authorizer.sessionToken != "administrator-secret" || authorizer.authorizeCalls != 0 || authorizer.renewCalls != 0 {
		t.Fatalf("session token=%q sensitive authorize=%d renew=%d", authorizer.sessionToken, authorizer.authorizeCalls, authorizer.renewCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationCredentialIssueFencesCurrentAppliedJobAndUsesAccountActor(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	admin := &adminAuthorizerStub{}
	service, err := NewService(database, admin, ServiceOptions{Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)), PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(7, nil))
	const fenceQuery = "SELECT j.status, j.applied_config_version_id, active.config_version_id FROM migration_jobs AS j JOIN account_active_config AS active ON active.account_id = j.account_id WHERE j.id = ? AND j.account_id = ? FOR UPDATE"
	mock.ExpectQuery(regexp.QuoteMeta(fenceQuery)).WithArgs(int64(19), int64(41)).WillReturnRows(sqlmock.NewRows([]string{"status", "applied_config_version_id", "active_config_version_id"}).AddRow("applied", 88, 88))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_sessions AS s JOIN obs_credentials AS c ON c.id = s.obs_credential_id SET s.revoked_at = ? WHERE c.account_id = ? AND s.revoked_at IS NULL")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_credentials SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO obs_credentials (account_id, public_id, token_hash, credential_epoch, created_at) VALUES (?, ?, ?, ?, ?)")).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_account_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).WithArgs("obs_credential_reset", int64(41), int64(41), []byte("{}"), now).WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectCommit()
	issued, err := service.IssueForMigration(context.Background(), 41, 19, 88)
	if err != nil || issued.URL == "" || admin.sessionToken != "" {
		t.Fatalf("issued=%#v admin=%q error=%v", issued, admin.sessionToken, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationCredentialIssueRejectsConfigurationChangeAfterSelectorPreparationBeforeRotation(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service, err := NewService(database, &adminAuthorizerStub{}, ServiceOptions{Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)), PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(7, nil))
	const fenceQuery = "SELECT j.status, j.applied_config_version_id, active.config_version_id FROM migration_jobs AS j JOIN account_active_config AS active ON active.account_id = j.account_id WHERE j.id = ? AND j.account_id = ? FOR UPDATE"
	mock.ExpectQuery(regexp.QuoteMeta(fenceQuery)).WithArgs(int64(19), int64(41)).WillReturnRows(sqlmock.NewRows([]string{"status", "applied_config_version_id", "active_config_version_id"}).AddRow("applied", 88, 99))
	mock.ExpectRollback()

	if _, err := service.IssueForMigration(context.Background(), 41, 19, 88); !errors.Is(err, ErrConflict) {
		t.Fatalf("IssueForMigration() error=%v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOBSResetAuditFailureRollsBackWithoutTOTPCalls(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		name           string
		auditError     error
		renewError     error
		wantRenewCalls int
	}{
		{name: "audit failure", auditError: errors.New("private audit failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			authorizer := &adminAuthorizerStub{renewErr: test.renewError}
			random := bytes.NewReader(append(bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)...))
			service, err := NewService(database, authorizer, ServiceOptions{Now: func() time.Time { return now }, Random: random, PublicOrigin: "https://host.example"})
			if err != nil {
				t.Fatal(err)
			}

			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
				WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(7), nil))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_sessions AS s JOIN obs_credentials AS c ON c.id = s.obs_credential_id SET s.revoked_at = ? WHERE c.account_id = ? AND s.revoked_at IS NULL")).
				WillReturnResult(sqlmock.NewResult(0, 3))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_credentials SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL")).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO obs_credentials (account_id, public_id, token_hash, credential_epoch, created_at) VALUES (?, ?, ?, ?, ?)")).
				WillReturnResult(sqlmock.NewResult(9, 1))
			audit := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)"))
			if test.auditError != nil {
				audit.WillReturnError(test.auditError)
			} else {
				audit.WillReturnResult(sqlmock.NewResult(10, 1))
			}
			mock.ExpectRollback()

			if _, err := service.Issue(context.Background(), "administrator-secret", 41); err == nil {
				t.Fatal("Issue() unexpectedly succeeded")
			}
			if authorizer.renewCalls != test.wantRenewCalls {
				t.Fatalf("RenewRecentTOTP calls = %d, want %d", authorizer.renewCalls, test.wantRenewCalls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssueCredentialStoresOnlyHashesAndRevokesEveryEarlierCredential(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC)
	random := bytes.NewReader(append(bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x22}, 32)...))
	admin := &adminAuthorizerStub{}
	service, err := NewService(database, admin, ServiceOptions{Now: func() time.Time { return now }, Random: random, PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}

	longToken := "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI"
	longHash := sha256.Sum256([]byte(longToken))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(7), nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_sessions AS s JOIN obs_credentials AS c ON c.id = s.obs_credential_id SET s.revoked_at = ? WHERE c.account_id = ? AND s.revoked_at IS NULL")).
		WithArgs(now, int64(41)).WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE obs_credentials SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL")).
		WithArgs(now, int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO obs_credentials (account_id, public_id, token_hash, credential_epoch, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs(int64(41), "ERERERERERERERERERERERERERERERERERERERERERE", hashArgument{want: longHash}, int64(7), now).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs("obs_credential_reset", int64(1), int64(41), []byte("{}"), now).
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectCommit()

	issued, err := service.Issue(context.Background(), "administrator-secret", 41)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if issued.PublicID != "ERERERERERERERERERERERERERERERERERERERERERE" {
		t.Fatalf("PublicID = %q", issued.PublicID)
	}
	if issued.URL != "https://host.example/obs/ERERERERERERERERERERERERERERERERERERERERERE#token="+longToken {
		t.Fatalf("URL = %q", issued.URL)
	}
	if admin.sessionToken != "administrator-secret" || admin.authorizeCalls != 0 || admin.renewCalls != 0 {
		t.Fatalf("administrator session token=%q authorize=%d renew=%d", admin.sessionToken, admin.authorizeCalls, admin.renewCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueCredentialRejectsRevokedAdministratorSessionBeforeDomainWork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	admin := &adminAuthorizerStub{sessionErr: errors.New("revoked session")}
	service, err := NewService(database, admin, ServiceOptions{Random: bytes.NewReader(make([]byte, 64)), PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), "stale", 1); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Issue() error = %v, want authentication failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeCreatesOpaqueTwelveHourSessionBoundToCurrentEpoch(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	longToken := "long-token-not-in-a-request-target"
	longHash := sha256.Sum256([]byte(longToken))
	shortToken := "MzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzM"
	shortHash := sha256.Sum256([]byte(shortToken))
	service, err := NewService(database, &adminAuthorizerStub{}, ServiceOptions{Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{0x33}, 32)), PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("(?s)SELECT c.id, c.account_id, c.token_hash, c.credential_epoch, c.revoked_at, a.credential_epoch, a.disabled_at.*FROM obs_credentials AS c.*JOIN streamer_accounts AS a.*WHERE c.public_id = .*FOR UPDATE").
		WithArgs(testPublicID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account_id", "token_hash", "credential_epoch", "revoked_at", "account_epoch", "disabled_at"}).AddRow(8, 41, longHash[:], 7, nil, 7, nil))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO obs_sessions (obs_credential_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs(int64(8), hashArgument{want: shortHash}, int64(7), now, now.Add(12*time.Hour)).
		WillReturnResult(sqlmock.NewResult(12, 1))
	mock.ExpectCommit()

	session, err := service.Exchange(context.Background(), testPublicID, longToken)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if session.Token != shortToken || session.AccountID != 41 || !session.ExpiresAt.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("session = %+v", session)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeRejectsLongCredentialAfterDisableOrCredentialEpochChange(t *testing.T) {
	for _, test := range []struct {
		name         string
		accountEpoch int64
		disabledAt   any
	}{
		{name: "credential epoch changed", accountEpoch: 8},
		{name: "account disabled", accountEpoch: 7, disabledAt: testAuthNow},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			longToken := "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
			longHash := sha256.Sum256([]byte(longToken))
			service, err := NewService(database, &adminAuthorizerStub{}, ServiceOptions{Now: func() time.Time { return testAuthNow }, Random: bytes.NewReader(make([]byte, 32)), PublicOrigin: "https://host.example"})
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectBegin()
			mock.ExpectQuery("(?s)SELECT c.id, c.account_id, c.token_hash, c.credential_epoch, c.revoked_at, a.credential_epoch, a.disabled_at.*WHERE c.public_id = .*FOR UPDATE").
				WithArgs(testPublicID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "account_id", "token_hash", "credential_epoch", "revoked_at", "account_epoch", "disabled_at"}).AddRow(8, 41, longHash[:], 7, nil, test.accountEpoch, test.disabledAt))
			mock.ExpectRollback()
			if _, err := service.Exchange(context.Background(), testPublicID, longToken); !errors.Is(err, ErrAuthenticationFailed) {
				t.Fatalf("Exchange() error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthenticateReturnsOnlyTheBoundAccountForCurrentShortSession(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service, err := NewService(database, &adminAuthorizerStub{}, ServiceOptions{Now: func() time.Time { return testAuthNow }, PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}
	shortToken := "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
	shortHash := sha256.Sum256([]byte(shortToken))
	mock.ExpectQuery("(?s)SELECT c.public_id, c.account_id, s.credential_epoch, c.credential_epoch, s.expires_at.*WHERE s.token_hash = .*LIMIT 1").
		WithArgs(hashArgument{want: shortHash}).
		WillReturnRows(sqlmock.NewRows([]string{"public_id", "account_id", "session_epoch", "credential_epoch", "expires_at", "session_revoked_at", "credential_revoked_at", "account_epoch", "disabled_at"}).
			AddRow(testPublicID, 41, 7, 7, testAuthNow.Add(time.Hour), nil, nil, 7, nil))
	accountID, err := service.Authenticate(context.Background(), testPublicID, shortToken)
	if err != nil || accountID != 41 {
		t.Fatalf("Authenticate() = %d, %v", accountID, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticateRejectsCrossPublicIDRevokedDisabledExpiredAndChangedEpochSessions(t *testing.T) {
	tests := []struct {
		name                string
		requestedPublic     string
		rowPublic           string
		expiresAt           time.Time
		sessionRevoked      any
		credentialEpoch     int64
		longCredentialEpoch int64
		accountEpoch        int64
		disabledAt          any
		credentialRevoked   any
	}{
		{name: "cross public id", requestedPublic: testOtherPublicID, rowPublic: testPublicID, expiresAt: testAuthNow.Add(time.Hour), credentialEpoch: 4, longCredentialEpoch: 4, accountEpoch: 4},
		{name: "revoked", requestedPublic: testPublicID, rowPublic: testPublicID, expiresAt: testAuthNow.Add(time.Hour), sessionRevoked: testAuthNow, credentialEpoch: 4, longCredentialEpoch: 4, accountEpoch: 4},
		{name: "expired", requestedPublic: testPublicID, rowPublic: testPublicID, expiresAt: testAuthNow, credentialEpoch: 4, longCredentialEpoch: 4, accountEpoch: 4},
		{name: "epoch changed", requestedPublic: testPublicID, rowPublic: testPublicID, expiresAt: testAuthNow.Add(time.Hour), credentialEpoch: 4, longCredentialEpoch: 4, accountEpoch: 5},
		{name: "long credential epoch mismatch", requestedPublic: testPublicID, rowPublic: testPublicID, expiresAt: testAuthNow.Add(time.Hour), credentialEpoch: 4, longCredentialEpoch: 3, accountEpoch: 4},
		{name: "disabled", requestedPublic: testPublicID, rowPublic: testPublicID, expiresAt: testAuthNow.Add(time.Hour), credentialEpoch: 4, longCredentialEpoch: 4, accountEpoch: 4, disabledAt: testAuthNow},
		{name: "credential revoked", requestedPublic: testPublicID, rowPublic: testPublicID, expiresAt: testAuthNow.Add(time.Hour), credentialEpoch: 4, longCredentialEpoch: 4, accountEpoch: 4, credentialRevoked: testAuthNow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			service, err := NewService(database, &adminAuthorizerStub{}, ServiceOptions{Now: func() time.Time { return testAuthNow }, Random: bytes.NewReader(make([]byte, 32)), PublicOrigin: "https://host.example"})
			if err != nil {
				t.Fatal(err)
			}
			shortToken := "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
			shortHash := sha256.Sum256([]byte(shortToken))
			mock.ExpectQuery("(?s)SELECT c.public_id, c.account_id, s.credential_epoch, c.credential_epoch, s.expires_at, s.revoked_at, c.revoked_at, a.credential_epoch, a.disabled_at.*FROM obs_sessions AS s.*JOIN obs_credentials AS c.*JOIN streamer_accounts AS a.*WHERE s.token_hash = .*LIMIT 1").
				WithArgs(hashArgument{want: shortHash}).
				WillReturnRows(sqlmock.NewRows([]string{"public_id", "account_id", "session_epoch", "credential_epoch", "expires_at", "session_revoked_at", "credential_revoked_at", "account_epoch", "disabled_at"}).
					AddRow(test.rowPublic, 41, test.credentialEpoch, test.longCredentialEpoch, test.expiresAt, test.sessionRevoked, test.credentialRevoked, test.accountEpoch, test.disabledAt))
			if _, err := service.Authenticate(context.Background(), test.requestedPublic, shortToken); !errors.Is(err, ErrAuthenticationFailed) {
				t.Fatalf("Authenticate() error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

var testAuthNow = time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC)

const testPublicID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
const testOtherPublicID = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

type adminAuthorizerStub struct {
	sessionToken    string
	recentToken     string
	sessionErr      error
	recentErr       error
	renewErr        error
	writeMarkers    bool
	authorizeCalls  int
	renewCalls      int
	authorizedToken string
	authorizedAt    time.Time
	renewedAt       time.Time
	authorizeTx     *sql.Tx
	renewTx         *sql.Tx
}

func (stub *adminAuthorizerStub) RequireSession(_ context.Context, token string) error {
	stub.sessionToken = token
	return stub.sessionErr
}

func (stub *adminAuthorizerStub) RequireRecentTOTP(_ context.Context, token string) error {
	stub.recentToken = token
	return stub.recentErr
}

func (stub *adminAuthorizerStub) AuthorizeRecentTOTP(ctx context.Context, transaction *sql.Tx, token string, now time.Time) (security.SensitiveSession, error) {
	stub.authorizeCalls++
	stub.authorizedToken = token
	stub.authorizedAt = now
	stub.authorizeTx = transaction
	stub.sessionToken = token
	stub.recentToken = token
	if stub.recentErr != nil {
		return security.SensitiveSession{}, stub.recentErr
	}
	if stub.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_authorize"); err != nil {
			return security.SensitiveSession{}, err
		}
	}
	return security.SensitiveSession{}, nil
}

func (stub *adminAuthorizerStub) RenewRecentTOTP(ctx context.Context, transaction *sql.Tx, _ security.SensitiveSession, now time.Time) error {
	stub.renewCalls++
	stub.renewedAt = now
	stub.renewTx = transaction
	if stub.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_renew"); err != nil {
			return err
		}
	}
	return stub.renewErr
}

type obsTimeSequence struct {
	values []time.Time
	index  int
}

func (sequence *obsTimeSequence) Now() time.Time {
	if sequence.index >= len(sequence.values) {
		return sequence.values[len(sequence.values)-1]
	}
	value := sequence.values[sequence.index]
	sequence.index++
	return value
}

type hashArgument struct{ want [sha256.Size]byte }

func (argument hashArgument) Match(value driver.Value) bool {
	actual, ok := value.([]byte)
	return ok && bytes.Equal(actual, argument.want[:])
}
