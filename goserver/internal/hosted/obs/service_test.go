package obs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/adminidentity"

	"github.com/DATA-DOG/go-sqlmock"
)

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
	if admin.sessionToken != "administrator-secret" || admin.recentToken != "administrator-secret" {
		t.Fatalf("administrator authorization = session %q recent %q", admin.sessionToken, admin.recentToken)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueCredentialRequiresRecentAdministratorTOTPBeforeDatabaseWork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	admin := &adminAuthorizerStub{recentErr: adminidentity.ErrRecentTOTPRequired}
	service, err := NewService(database, admin, ServiceOptions{Random: bytes.NewReader(make([]byte, 64)), PublicOrigin: "https://host.example"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), "stale", 1); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("Issue() error = %v, want recent TOTP", err)
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

func TestExchangeRejectsLongCredentialAfterDisableOrRebindEpochChange(t *testing.T) {
	for _, test := range []struct {
		name         string
		accountEpoch int64
		disabledAt   any
	}{
		{name: "rebind changed epoch", accountEpoch: 8},
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
	sessionToken string
	recentToken  string
	sessionErr   error
	recentErr    error
}

func (stub *adminAuthorizerStub) RequireSession(_ context.Context, token string) error {
	stub.sessionToken = token
	return stub.sessionErr
}

func (stub *adminAuthorizerStub) RequireRecentTOTP(_ context.Context, token string) error {
	stub.recentToken = token
	return stub.recentErr
}

type hashArgument struct{ want [sha256.Size]byte }

func (argument hashArgument) Match(value driver.Value) bool {
	actual, ok := value.([]byte)
	return ok && bytes.Equal(actual, argument.want[:])
}
