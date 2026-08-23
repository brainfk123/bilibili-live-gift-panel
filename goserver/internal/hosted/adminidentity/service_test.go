package adminidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"regexp"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestAdminCompositionDoesNotRequireAdministratorBilibiliVerifier(t *testing.T) {
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(newMemoryRepository(), keys, &MemorySender{}, ServiceOptions{}); err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
}

func TestAdministratorSessionTTLAcceptsThirtyDaysAndRejectsLonger(t *testing.T) {
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(newMemoryRepository(), keys, &MemorySender{}, ServiceOptions{SessionTTL: 30 * 24 * time.Hour}); err != nil {
		t.Fatalf("NewService(30 days) error = %v", err)
	}
	if _, err := NewService(newMemoryRepository(), keys, &MemorySender{}, ServiceOptions{SessionTTL: 30*24*time.Hour + time.Nanosecond}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NewService(over 30 days) error = %v, want ErrInvalidInput", err)
	}
}

func TestInitializeUsesEmailAndRandomHandoffTokenWithoutCreatingSession(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)

	result, err := service.Initialize(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if result.TOTPURI != "otpauth://totp/GiftPanel:owner@example.com?secret=TESTSECRET" {
		t.Fatalf("TOTPURI = %q", result.TOTPURI)
	}
	if len(result.RecoveryPassword) != 20 {
		t.Fatalf("RecoveryPassword length = %d, want 20", len(result.RecoveryPassword))
	}
	if result.HandoffToken == "" {
		t.Fatal("Initialize() omitted the random confirmation token")
	}

	if repository.initialized {
		t.Fatal("initialization activated administrator before token and new TOTP confirmation")
	}
	second, err := service.Initialize(context.Background(), "owner@example.com")
	if err != nil || second != result {
		t.Fatalf("retry Initialize() = %#v, %v; want same handoff", second, err)
	}
	if len(sender.Messages()) != 1 {
		t.Fatalf("successful retry sent %d archives, want stable prior delivery", len(sender.Messages()))
	}
	if err := service.ConfirmHandoff(context.Background(), result.HandoffToken, "123456"); err != nil {
		t.Fatalf("activate pending initialization error = %v", err)
	}
	repository.mu.Lock()
	stored := repository.identity
	codeHashes := cloneHashes(repository.activeCodes)
	repository.mu.Unlock()
	if stored.CredentialEpoch != 1 || len(stored.UIDCiphertext) != 0 || len(stored.UIDLookup) != 0 {
		t.Fatalf("stored identity = %#v", stored)
	}
	if bytes.Contains(stored.UIDCiphertext, []byte("32249588")) || bytes.Contains(stored.EmailCiphertext, []byte("owner@example.com")) || bytes.Contains(stored.TOTPSecretCiphertext, []byte("TESTSECRET")) {
		t.Fatal("initialization persisted plaintext administrator secret")
	}
	if len(codeHashes) != RecoveryCodeCount {
		t.Fatalf("stored recovery hashes = %d, want %d", len(codeHashes), RecoveryCodeCount)
	}
	if repository.sessionCount() != 0 {
		t.Fatal("initialization confirmation created an administrator login session")
	}
	for hash := range codeHashes {
		if len(hash) != sha256.Size {
			t.Fatalf("stored recovery hash length = %d", len(hash))
		}
	}

	messages := sender.Messages()
	if len(messages) != 1 || len(messages[0].Attachments) != 1 {
		t.Fatalf("messages = %#v, want one encrypted attachment", messages)
	}
	if messages[0].To != "owner@example.com" || bytes.Contains(messages[0].Attachments[0].Data, []byte(result.RecoveryPassword)) || bytes.Contains([]byte(messages[0].Text), []byte(result.RecoveryPassword)) {
		t.Fatal("initial recovery email omitted recipient or exposed its password")
	}

	if _, err := service.Initialize(context.Background(), "owner@example.com"); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("post-activation Initialize() error = %v, want ErrAlreadyInitialized", err)
	}
}

func TestLegacyUIDInitializationHandoffCannotConfirmAndIsCleaned(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 5, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service := newTestService(t, repository, &MemorySender{}, now)
	token := "legacy-handoff-token"
	tokenHash, err := service.keys.HashToken("admin_handoff_token", []byte(token))
	if err != nil {
		t.Fatal(err)
	}
	record := sqlHandoffRecord(now, HandoffInitialization)
	record.TokenHash = tokenHash
	record.UIDCiphertext = bytes.Repeat([]byte{0x41}, 48)
	record.UIDLookup = bytes.Repeat([]byte{0x42}, sha256.Size)
	handoff := pendingFromRecord(1, record)
	repository.handoffs[handoff.ID] = handoff

	if err := service.ConfirmHandoff(context.Background(), token, "123456"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("legacy ConfirmHandoff() error = %v", err)
	}
	if repository.initialized || repository.sessionCount() != 0 {
		t.Fatal("legacy UID handoff authenticated an administrator")
	}
	if err := repository.CleanupExpiredHandoffs(context.Background(), now, defaultCleanupLimit); err != nil {
		t.Fatal(err)
	}
	if _, found := repository.handoffs[handoff.ID]; found {
		t.Fatal("legacy UID handoff survived cleanup")
	}
}

func TestIdentityRecordAllowsOnlyAbsentOrCompleteLegacyUIDPair(t *testing.T) {
	base := IdentityRecord{
		CredentialEpoch:      1,
		EmailCiphertext:      bytes.Repeat([]byte{0x31}, 64),
		TOTPSecretCiphertext: bytes.Repeat([]byte{0x32}, 64),
	}
	tests := []struct {
		name   string
		record IdentityRecord
		want   bool
	}{
		{name: "absent legacy pair", record: base, want: true},
		{name: "complete legacy pair", record: IdentityRecord{CredentialEpoch: base.CredentialEpoch, UIDCiphertext: bytes.Repeat([]byte{0x33}, 48), UIDLookup: bytes.Repeat([]byte{0x34}, sha256.Size), EmailCiphertext: base.EmailCiphertext, TOTPSecretCiphertext: base.TOTPSecretCiphertext}, want: true},
		{name: "ciphertext without lookup", record: IdentityRecord{CredentialEpoch: base.CredentialEpoch, UIDCiphertext: []byte{0x33}, EmailCiphertext: base.EmailCiphertext, TOTPSecretCiphertext: base.TOTPSecretCiphertext}, want: false},
		{name: "lookup without ciphertext", record: IdentityRecord{CredentialEpoch: base.CredentialEpoch, UIDLookup: bytes.Repeat([]byte{0x34}, sha256.Size), EmailCiphertext: base.EmailCiphertext, TOTPSecretCiphertext: base.TOTPSecretCiphertext}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validIdentityRecord(test.record); got != test.want {
				t.Fatalf("validIdentityRecord() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestEmailLoginSendsOneShortCodeAndCreatesOneThirtyDaySessionWithoutTOTP(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 12, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)

	challenge, err := service.BeginEmailLogin(context.Background())
	if err != nil || challenge.ChallengeID == "" || !challenge.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("BeginEmailLogin() = %#v, %v", challenge, err)
	}
	messages := sender.Messages()
	if len(messages) != 1 || messages[0].To != "owner@example.com" || len(messages[0].Attachments) != 0 {
		t.Fatalf("email messages = %#v", messages)
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(messages[0].Text)
	if code == "" {
		t.Fatalf("email omitted six-digit code: %q", messages[0].Text)
	}
	login, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code)
	if err != nil || login.Token == "" || !login.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("VerifyEmailLogin() = %#v, %v", login, err)
	}
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("replayed VerifyEmailLogin() error = %v", err)
	}
	if repository.sessionCount() != 1 {
		t.Fatalf("sessions = %d, want one", repository.sessionCount())
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, session := range repository.sessions {
		if !session.TOTPVerifiedAt.IsZero() {
			t.Fatalf("email-login session TOTPVerifiedAt = %s, want zero", session.TOTPVerifiedAt)
		}
	}
}

func TestEmailLoginExpiresAndSMTPFailureLeavesNoUsableChallenge(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 13, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	clock := now
	service := newTestServiceWithClock(t, repository, sender, func() time.Time { return clock })
	challenge, err := service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(sender.Messages()[0].Text)
	clock = now.Add(5 * time.Minute)
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("expired VerifyEmailLogin() error = %v", err)
	}
	if repository.sessionCount() != 0 {
		t.Fatal("expired email challenge created a session")
	}

	failedSender := &MemorySender{Err: errors.New("smtp unavailable")}
	failed := newTestService(t, repository, failedSender, now)
	if _, err := failed.BeginEmailLogin(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SMTP-failed BeginEmailLogin() error = %v", err)
	}
	failed.emailMu.Lock()
	remaining := len(failed.emailLogins)
	failed.emailMu.Unlock()
	if remaining != 0 {
		t.Fatalf("SMTP failure retained %d challenges", remaining)
	}
}

func TestEmailLoginCountsFiveFailuresAndRejectsRotatedEpoch(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 14, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)

	challenge, err := service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(sender.Messages()[0].Text)
	wrongCode := "000000"
	if wrongCode == code {
		wrongCode = "999999"
	}
	for attempt := 0; attempt < emailCodeAttempts-1; attempt++ {
		if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, wrongCode); !errors.Is(err, ErrAuthenticationFailed) {
			t.Fatalf("failure %d error = %v", attempt+1, err)
		}
	}
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code); err != nil {
		t.Fatalf("correct code after four failures error = %v", err)
	}

	challenge, err = service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < emailCodeAttempts+1; attempt++ {
		if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, wrongCode); !errors.Is(err, ErrAuthenticationFailed) {
			t.Fatalf("failure %d error = %v", attempt+1, err)
		}
	}

	challenge, err = service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rotatedCode := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(sender.Messages()[2].Text)
	repository.mu.Lock()
	repository.identity.CredentialEpoch++
	repository.mu.Unlock()
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, rotatedCode); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("rotated epoch VerifyEmailLogin() error = %v", err)
	}
}

func TestEmailLoginRejectsCorrectCodeAfterExactlyFiveFailures(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 14, 15, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)
	challenge, err := service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(sender.Messages()[0].Text)
	wrongCode := "000000"
	if wrongCode == code {
		wrongCode = "999999"
	}
	for attempt := 0; attempt < emailCodeAttempts; attempt++ {
		if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, wrongCode); !errors.Is(err, ErrAuthenticationFailed) {
			t.Fatalf("failure %d error = %v", attempt+1, err)
		}
	}
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("correct code after five failures error = %v", err)
	}
	if repository.sessionCount() != 0 {
		t.Fatal("correct code after five failures created a session")
	}
}

func TestEmailLoginPropagatesUnavailableSessionCreationAfterConsumingChallenge(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 14, 30, 0, time.UTC)
	memory := initializedMemoryRepository(t, now)
	repository := unavailableEmailSessionRepository{memoryRepository: memory}
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)
	challenge, err := service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(sender.Messages()[0].Text)
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("VerifyEmailLogin() error = %v, want ErrUnavailable", err)
	}
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("replayed VerifyEmailLogin() error = %v, want ErrAuthenticationFailed", err)
	}
	if memory.sessionCount() != 0 {
		t.Fatal("unavailable session creation created a session")
	}
}

func TestEmailLoginTreatsUnexpectedSessionCreationFailureAsUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 14, 45, 0, time.UTC)
	memory := initializedMemoryRepository(t, now)
	repository := failedEmailSessionRepository{memoryRepository: memory, err: ErrInvalidInput}
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)
	challenge, err := service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(sender.Messages()[0].Text)
	if _, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("VerifyEmailLogin() error = %v, want ErrUnavailable", err)
	}
}

func TestVerifyRecentTOTPRejectsReplayAndExpiresAtTenMinutes(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 20, 0, 0, time.UTC)
	clock := now
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	service := newTestServiceWithClock(t, repository, sender, func() time.Time { return clock })
	login := emailLoginForTest(t, service, sender)
	if err := service.RequireRecentTOTP(context.Background(), login.Token); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("RequireRecentTOTP() before confirmation error = %v", err)
	}

	if err := service.VerifyRecentTOTP(context.Background(), login.Token, "123456"); err != nil {
		t.Fatalf("VerifyRecentTOTP() error = %v", err)
	}
	if err := service.VerifyRecentTOTP(context.Background(), login.Token, "123456"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("VerifyRecentTOTP(replay) error = %v", err)
	}

	clock = now.Add(9*time.Minute + 59*time.Second)
	if err := service.RequireRecentTOTP(context.Background(), login.Token); err != nil {
		t.Fatalf("RequireRecentTOTP(at 9m59s) error = %v", err)
	}
	clock = now.Add(10 * time.Minute)
	if err := service.RequireRecentTOTP(context.Background(), login.Token); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("RequireRecentTOTP(at 10m) error = %v, want ErrRecentTOTPRequired", err)
	}
}

func TestAuthorizeOperationIssuesPurposeBoundSingleUseTokenAndRejectsTOTPStepReplay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)
	login := emailLoginForTest(t, service, sender)

	token, err := service.AuthorizeOperation(context.Background(), login.Token, "123456", security.OperationBiliServiceReplace, "global")
	if err != nil {
		t.Fatalf("AuthorizeOperation() error = %v", err)
	}
	if token == "" || bytes.Contains([]byte(token), []byte("123456")) {
		t.Fatalf("AuthorizeOperation() token = %q", token)
	}
	stored, ok := repository.operationByToken(service.keys, token)
	if !ok {
		t.Fatal("AuthorizeOperation() did not persist the token hash")
	}
	if stored.Purpose != security.OperationBiliServiceReplace || stored.Target != "global" || !stored.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("stored operation = %#v", stored)
	}
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	transaction, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConsumeOperation(context.Background(), transaction, login.Token, token, security.OperationBiliServiceReplace, "global", now.Add(time.Second)); err != nil {
		t.Fatalf("ConsumeOperation() error = %v", err)
	}
	if err := service.ConsumeOperation(context.Background(), transaction, login.Token, token, security.OperationBiliServiceReplace, "global", now.Add(2*time.Second)); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("ConsumeOperation(replay) error = %v, want ErrAuthenticationFailed", err)
	}
	_ = transaction.Rollback()
	if _, err := service.AuthorizeOperation(context.Background(), login.Token, "123456", security.OperationAdminEmailChange, "email-target"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("AuthorizeOperation(replayed TOTP step) error = %v, want ErrAuthenticationFailed", err)
	}
}

func TestVerifyRecentTOTPOpensExactWindowFromVerificationInstant(t *testing.T) {
	verifiedAt := time.Date(2026, 8, 16, 10, 20, 29, 0, time.UTC)
	clock := verifiedAt
	repository := initializedMemoryRepository(t, verifiedAt)
	sender := &MemorySender{}
	service := newTestServiceWithClock(t, repository, sender, func() time.Time { return clock })
	login := emailLoginForTest(t, service, sender)

	if err := service.VerifyRecentTOTP(context.Background(), login.Token, "123456"); err != nil {
		t.Fatalf("VerifyRecentTOTP() error = %v", err)
	}
	clock = verifiedAt.Add(9*time.Minute + 59*time.Second)
	if err := service.RequireRecentTOTP(context.Background(), login.Token); err != nil {
		t.Fatalf("RequireRecentTOTP(9m59s after verification) error = %v", err)
	}
	clock = verifiedAt.Add(10 * time.Minute)
	if err := service.RequireRecentTOTP(context.Background(), login.Token); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("RequireRecentTOTP(10m after verification) error = %v, want ErrRecentTOTPRequired", err)
	}
}

func TestSQLRepositoryConfirmTOTPStoresVerificationInstantNotCodeStep(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	verifiedAt := time.Date(2026, 8, 16, 10, 20, 29, 0, time.UTC)
	codeStep := verifiedAt.Truncate(30 * time.Second)
	tokenHash := bytes.Repeat([]byte{0x5a}, sha256.Size)

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at"}).
			AddRow(int64(17), int64(4), verifiedAt.Add(time.Hour), nil))
	mock.ExpectQuery(sqlPattern("SELECT MAX(totp_verified_at) FROM site_sessions WHERE admin_identity_id = 1 AND credential_epoch = ?")).
		WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"totp_verified_at"}).AddRow(nil))
	mock.ExpectExec(sqlPattern("UPDATE site_sessions SET totp_verified_at = ? WHERE id = ? AND revoked_at IS NULL AND credential_epoch = ?")).
		WithArgs(verifiedAt, int64(17), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repository.ConfirmTOTP(context.Background(), ConfirmTOTPAttempt{
		ExpectedCredentialEpoch: 4,
		TokenHash:               tokenHash,
		Now:                     verifiedAt,
		TOTPStep:                codeStep,
	}); err != nil {
		t.Fatalf("ConfirmTOTP() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRecentTOTPRejectsFutureCodeStep(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 21, 29, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, keys, sender, ServiceOptions{
		Now: func() time.Time { return now }, TOTP: futureStepTOTP{}, Random: &sequenceReader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	login := emailLoginForTest(t, service, sender)

	if err := service.VerifyRecentTOTP(context.Background(), login.Token, "123456"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("VerifyRecentTOTP(future step) error = %v, want ErrAuthenticationFailed", err)
	}
	if err := service.RequireRecentTOTP(context.Background(), login.Token); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("RequireRecentTOTP() after rejected future step error = %v, want ErrRecentTOTPRequired", err)
	}
}

func TestSensitiveAuthorizationRenewsExactSessionAtOperationCompletion(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorizedAt := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	completedAt := authorizedAt.Add(500 * time.Millisecond)
	verifiedAt := authorizedAt.Add(-9*time.Minute - 59*time.Second)
	service := newTestService(t, NewRepository(database), &MemorySender{}, authorizedAt)
	tokenHash, err := service.keys.HashToken("admin_session", []byte("administrator-session"))
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(int64(17), int64(4), authorizedAt.Add(time.Hour), nil, verifiedAt))
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE id = ? AND admin_identity_id = 1 AND credential_epoch = ? FOR UPDATE")).
		WithArgs(int64(17), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(authorizedAt.Add(time.Hour), nil, verifiedAt))
	mock.ExpectExec(sqlPattern("UPDATE site_sessions SET totp_verified_at = ? WHERE id = ? AND credential_epoch = ? AND revoked_at IS NULL")).
		WithArgs(completedAt, int64(17), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.AuthorizeRecentTOTP(context.Background(), transaction, "administrator-session", authorizedAt)
	if err != nil {
		t.Fatalf("AuthorizeRecentTOTP() error = %v", err)
	}
	if err := service.RenewRecentTOTP(context.Background(), transaction, session, completedAt); err != nil {
		t.Fatalf("RenewRecentTOTP() error = %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveAuthorizationUsesExactTenMinuteBoundaryAndActiveEpoch(t *testing.T) {
	now := time.Date(2026, 8, 21, 9, 20, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Minute)
	tests := []struct {
		name         string
		sessionEpoch int64
		expiresAt    time.Time
		revokedAt    any
		verifiedAt   any
		wantError    error
	}{
		{name: "9m59s remains active", sessionEpoch: 4, expiresAt: now.Add(time.Hour), verifiedAt: now.Add(-9*time.Minute - 59*time.Second)},
		{name: "10m idle is expired", sessionEpoch: 4, expiresAt: now.Add(time.Hour), verifiedAt: now.Add(-10 * time.Minute), wantError: ErrRecentTOTPRequired},
		{name: "missing totp", sessionEpoch: 4, expiresAt: now.Add(time.Hour), verifiedAt: nil, wantError: ErrRecentTOTPRequired},
		{name: "any future timestamp is invalid", sessionEpoch: 4, expiresAt: now.Add(time.Hour), verifiedAt: now.Add(time.Nanosecond), wantError: ErrRecentTOTPRequired},
		{name: "future beyond skew", sessionEpoch: 4, expiresAt: now.Add(time.Hour), verifiedAt: now.Add(30*time.Second + time.Nanosecond), wantError: ErrRecentTOTPRequired},
		{name: "revoked session", sessionEpoch: 4, expiresAt: now.Add(time.Hour), revokedAt: revokedAt, verifiedAt: now.Add(-time.Minute), wantError: ErrAuthenticationFailed},
		{name: "expired session", sessionEpoch: 4, expiresAt: now, verifiedAt: now.Add(-time.Minute), wantError: ErrAuthenticationFailed},
		{name: "wrong epoch", sessionEpoch: 3, expiresAt: now.Add(time.Hour), verifiedAt: now.Add(-time.Minute), wantError: ErrAuthenticationFailed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			service := newTestService(t, NewRepository(database), &MemorySender{}, now)
			tokenHash, err := service.keys.HashToken("admin_session", []byte("administrator-session"))
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectBegin()
			mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
				WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
			mock.ExpectQuery(sqlPattern("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
				WithArgs(tokenHash).
				WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at", "totp_verified_at"}).
					AddRow(int64(17), test.sessionEpoch, test.expiresAt, test.revokedAt, test.verifiedAt))
			mock.ExpectRollback()

			transaction, err := database.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, gotErr := service.AuthorizeRecentTOTP(context.Background(), transaction, "administrator-session", now)
			if test.wantError == nil && gotErr != nil {
				t.Fatalf("AuthorizeRecentTOTP() error = %v", gotErr)
			}
			if test.wantError != nil && !errors.Is(gotErr, test.wantError) {
				t.Fatalf("AuthorizeRecentTOTP() error = %v, want %v", gotErr, test.wantError)
			}
			if err := transaction.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSensitiveRenewalDoesNotReviveWindowThatExpiresDuringMutation(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorizedAt := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	verifiedAt := authorizedAt.Add(-9*time.Minute - 59*time.Second)
	service := newTestService(t, NewRepository(database), &MemorySender{}, authorizedAt)
	tokenHash, err := service.keys.HashToken("admin_session", []byte("administrator-session"))
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(int64(17), int64(4), authorizedAt.Add(time.Hour), nil, verifiedAt))
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE id = ? AND admin_identity_id = 1 AND credential_epoch = ? FOR UPDATE")).
		WithArgs(int64(17), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(authorizedAt.Add(time.Hour), nil, verifiedAt))
	mock.ExpectRollback()

	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.AuthorizeRecentTOTP(context.Background(), transaction, "administrator-session", authorizedAt)
	if err != nil {
		t.Fatalf("AuthorizeRecentTOTP() error = %v", err)
	}
	if err := service.RenewRecentTOTP(context.Background(), transaction, session, authorizedAt.Add(time.Second)); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("RenewRecentTOTP() error = %v, want ErrRecentTOTPRequired", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveRenewalRejectsRevokedExpiredAndWrongEpochFence(t *testing.T) {
	now := time.Date(2026, 8, 21, 10, 20, 0, 0, time.UTC)
	revokedAt := now.Add(-time.Minute)
	tests := []struct {
		name               string
		administratorEpoch int64
		sessionRow         *sqlmock.Rows
		wantError          error
	}{
		{name: "wrong administrator epoch", administratorEpoch: 5, wantError: ErrAuthenticationFailed},
		{name: "missing exact session", administratorEpoch: 4, wantError: ErrAuthenticationFailed},
		{name: "revoked exact session", administratorEpoch: 4, sessionRow: sqlmock.NewRows([]string{"expires_at", "revoked_at", "totp_verified_at"}).AddRow(now.Add(time.Hour), revokedAt, now.Add(-time.Minute)), wantError: ErrAuthenticationFailed},
		{name: "expired exact session", administratorEpoch: 4, sessionRow: sqlmock.NewRows([]string{"expires_at", "revoked_at", "totp_verified_at"}).AddRow(now, nil, now.Add(-time.Minute)), wantError: ErrAuthenticationFailed},
		{name: "expired recent window", administratorEpoch: 4, sessionRow: sqlmock.NewRows([]string{"expires_at", "revoked_at", "totp_verified_at"}).AddRow(now.Add(time.Hour), nil, now.Add(-10*time.Minute)), wantError: ErrRecentTOTPRequired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			repository := NewRepository(database)
			service := newTestService(t, repository, &MemorySender{}, now)
			fence, valid := repository.sensitiveSessions.Issue(17, 4)
			if !valid {
				t.Fatal("test fence is invalid")
			}
			mock.ExpectBegin()
			mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
				WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(test.administratorEpoch))
			if test.administratorEpoch == 4 {
				expectation := mock.ExpectQuery(sqlPattern("SELECT expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE id = ? AND admin_identity_id = 1 AND credential_epoch = ? FOR UPDATE")).
					WithArgs(int64(17), int64(4))
				if test.sessionRow == nil {
					expectation.WillReturnError(sql.ErrNoRows)
				} else {
					expectation.WillReturnRows(test.sessionRow)
				}
			}
			mock.ExpectRollback()

			transaction, err := database.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.RenewRecentTOTP(context.Background(), transaction, fence, now); !errors.Is(err, test.wantError) {
				t.Fatalf("RenewRecentTOTP() error = %v, want %v", err, test.wantError)
			}
			if err := transaction.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSensitiveRenewalRejectsForeignOpaqueFenceBeforeDatabaseLookup(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 21, 10, 30, 0, 0, time.UTC)
	repository := NewRepository(database)
	service := newTestService(t, repository, &MemorySender{}, now)
	foreignFence, valid := security.NewSensitiveSessionIssuer().Issue(17, 4)
	if !valid {
		t.Fatal("foreign test fence is invalid")
	}

	mock.ExpectBegin()
	mock.ExpectRollback()
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RenewRecentTOTP(context.Background(), transaction, foreignFence, now); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("RenewRecentTOTP(foreign fence) error = %v, want ErrAuthenticationFailed", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveRecoveryRotationRenewsOnlyAfterAuditInSameTransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorizedAt := time.Date(2026, 8, 21, 10, 40, 0, 0, time.UTC)
	completedAt := authorizedAt.Add(2 * time.Second)
	verifiedAt := authorizedAt.Add(-time.Minute)
	repository := NewRepository(database)
	service := newTestService(t, repository, &MemorySender{}, authorizedAt)
	tokenHash, err := service.keys.HashToken("admin_session", []byte("administrator-session"))
	if err != nil {
		t.Fatal(err)
	}
	emailCiphertext, err := service.keys.Seal("admin_email", []byte("owner@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	hashes := sqlInitializationRecord(authorizedAt).RecoveryCodeHashes
	clockCalls := 0
	clock := func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return authorizedAt
		}
		return completedAt
	}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(int64(17), int64(4), authorizedAt.Add(time.Hour), nil, verifiedAt))
	mock.ExpectQuery(sqlPattern("SELECT email_ciphertext FROM admin_identity WHERE id = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"email_ciphertext"}).AddRow(emailCiphertext))
	mock.ExpectExec(sqlPattern("UPDATE admin_recovery_codes SET invalidated_at = ? WHERE admin_identity_id = ? AND used_at IS NULL AND invalidated_at IS NULL")).
		WithArgs(authorizedAt, int64(1)).WillReturnResult(sqlmock.NewResult(0, 10))
	for _, hash := range hashes {
		mock.ExpectExec(sqlPattern("INSERT INTO admin_recovery_codes (admin_identity_id, code_hash, created_at) VALUES (?, ?, ?)")).
			WithArgs(int64(1), hash, authorizedAt).WillReturnResult(sqlmock.NewResult(20, 1))
	}
	mock.ExpectExec(sqlPattern("INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES (?, ?, ?, ?)")).
		WithArgs("admin_recovery_material_rotated", int64(1), []byte("{}"), authorizedAt).
		WillReturnResult(sqlmock.NewResult(30, 1))
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE id = ? AND admin_identity_id = 1 AND credential_epoch = ? FOR UPDATE")).
		WithArgs(int64(17), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(authorizedAt.Add(time.Hour), nil, verifiedAt))
	mock.ExpectExec(sqlPattern("UPDATE site_sessions SET totp_verified_at = ? WHERE id = ? AND credential_epoch = ? AND revoked_at IS NULL")).
		WithArgs(completedAt, int64(17), int64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	storedEmail, err := repository.RotateRecoveryCodes(context.Background(), service, "administrator-session", hashes, clock)
	if err != nil {
		t.Fatalf("RotateRecoveryCodes() error = %v", err)
	}
	if subtle.ConstantTimeCompare(storedEmail, emailCiphertext) != 1 || clockCalls != 2 {
		t.Fatalf("RotateRecoveryCodes() email match=%t clock calls=%d", subtle.ConstantTimeCompare(storedEmail, emailCiphertext) == 1, clockCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveRecoveryRotationRenewalFailureRollsBackMaterialAndAudit(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 21, 10, 50, 0, 0, time.UTC)
	repository := NewRepository(database)
	service := newTestService(t, repository, &MemorySender{}, now)
	tokenHash, err := service.keys.HashToken("admin_session", []byte("administrator-session"))
	if err != nil {
		t.Fatal(err)
	}
	hashes := sqlInitializationRecord(now).RecoveryCodeHashes

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at", "totp_verified_at"}).
			AddRow(int64(17), int64(4), now.Add(time.Hour), nil, now.Add(-time.Minute)))
	mock.ExpectQuery(sqlPattern("SELECT email_ciphertext FROM admin_identity WHERE id = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"email_ciphertext"}).AddRow([]byte("email-ciphertext")))
	mock.ExpectExec(sqlPattern("UPDATE admin_recovery_codes SET invalidated_at = ? WHERE admin_identity_id = ? AND used_at IS NULL AND invalidated_at IS NULL")).
		WillReturnResult(sqlmock.NewResult(0, 10))
	for range hashes {
		mock.ExpectExec(sqlPattern("INSERT INTO admin_recovery_codes (admin_identity_id, code_hash, created_at) VALUES (?, ?, ?)")).
			WillReturnResult(sqlmock.NewResult(20, 1))
	}
	mock.ExpectExec(sqlPattern("INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES (?, ?, ?, ?)")).
		WillReturnResult(sqlmock.NewResult(30, 1))
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(4)))
	mock.ExpectQuery(sqlPattern("SELECT expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE id = ? AND admin_identity_id = 1 AND credential_epoch = ? FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"expires_at", "revoked_at", "totp_verified_at"}).AddRow(now.Add(time.Hour), nil, now.Add(-time.Minute)))
	mock.ExpectExec(sqlPattern("UPDATE site_sessions SET totp_verified_at = ? WHERE id = ? AND credential_epoch = ? AND revoked_at IS NULL")).
		WillReturnError(errors.New("private renewal failure"))
	mock.ExpectRollback()

	if _, err := repository.RotateRecoveryCodes(context.Background(), service, "administrator-session", hashes, func() time.Time { return now }); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RotateRecoveryCodes() error = %v, want ErrUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRequireSessionAcceptsActiveAdminWithoutRequiringRecentTOTP(t *testing.T) {
	now := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)
	login := emailLoginForTest(t, service, sender)
	if err := service.RequireSession(context.Background(), login.Token); err != nil {
		t.Fatalf("RequireSession(active admin) = %v", err)
	}
	if err := service.RequireSession(context.Background(), "streamer-session"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("RequireSession(non-admin token) = %v", err)
	}
}

func TestConcurrentInitializeHasExactlyOneWinner(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 30, 0, 0, time.UTC)
	repository := &synchronizedPrepareRepository{
		memoryRepository: newMemoryRepository(),
		prepared:         make(chan struct{}, 2),
		release:          make(chan struct{}),
	}
	sender := &MemorySender{}
	service := newTestService(t, repository, sender, now)

	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			_, err := service.Initialize(context.Background(), "owner@example.com")
			results <- err
		}()
	}
	for index := 0; index < 2; index++ {
		<-repository.prepared
	}
	close(repository.release)
	winners := 0
	alreadyInitialized := 0
	for index := 0; index < 2; index++ {
		err := <-results
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrAlreadyInitialized):
			alreadyInitialized++
		default:
			t.Fatalf("Initialize() error = %v", err)
		}
	}
	if winners != 2 || alreadyInitialized != 0 {
		t.Fatalf("winners=%d alreadyInitialized=%d", winners, alreadyInitialized)
	}
	if got := len(sender.Messages()); got != 1 {
		t.Fatalf("concurrent exact initialization sent %d attachments, want one", got)
	}
}

func TestInitializeRetriesArchiveAfterSMTPFailure(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 31, 0, 0, time.UTC)
	repository := newMemoryRepository()
	sender := &failOnceSender{}
	service := newTestService(t, repository, sender, now)

	if _, err := service.Initialize(context.Background(), "owner@example.com"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first Initialize() error = %v, want ErrUnavailable", err)
	}
	result, err := service.Initialize(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("retry Initialize() error = %v", err)
	}
	if result.TOTPURI == "" || result.RecoveryPassword == "" {
		t.Fatalf("retry Initialize() result = %#v", result)
	}
	if sender.Attempts() != 2 {
		t.Fatalf("SMTP attempts = %d, want 2", sender.Attempts())
	}
	if got := len(sender.Messages()); got != 1 {
		t.Fatalf("accepted messages = %d, want 1", got)
	}
}

func TestSequenceReaderDoesNotWrapAfter256Bytes(t *testing.T) {
	reader := &sequenceReader{}
	first := make([]byte, 32)
	if _, err := io.ReadFull(reader, first); err != nil {
		t.Fatal(err)
	}
	discard := make([]byte, 224)
	if _, err := io.ReadFull(reader, discard); err != nil {
		t.Fatal(err)
	}
	next := make([]byte, 32)
	if _, err := io.ReadFull(reader, next); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, next) {
		t.Fatal("sequenceReader wrapped after 256 bytes")
	}
}

func TestSQLRepositoryInitializeUsesOneTransactionAndMapsSingletonConflict(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	record := sqlInitializationRecord(now)

	mock.ExpectBegin()
	mock.ExpectExec(sqlPattern("INSERT INTO admin_identity")).
		WithArgs(int64(1), record.Identity.UIDCiphertext, record.Identity.UIDLookup, record.Identity.EmailCiphertext, now, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(sqlPattern("INSERT INTO admin_totp")).
		WithArgs(int64(1), record.Identity.TOTPSecretCiphertext, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, hash := range record.RecoveryCodeHashes {
		mock.ExpectExec(sqlPattern("INSERT INTO admin_recovery_codes")).
			WithArgs(int64(1), hash, now).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectCommit()
	if err := repository.Initialize(context.Background(), record); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	database2, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database2.Close()
	repository2 := NewRepository(database2)
	mock2.ExpectBegin()
	mock2.ExpectExec(sqlPattern("INSERT INTO admin_identity")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "sensitive duplicate detail"})
	mock2.ExpectRollback()
	if err := repository2.Initialize(context.Background(), record); !errors.Is(err, ErrAlreadyInitialized) || stringsContains(err.Error(), "sensitive") {
		t.Fatalf("duplicate Initialize() error = %v", err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryCommitsPendingInitializationBeforeDelivery(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 6, 0, 0, time.UTC)
	record := sqlHandoffRecord(now, HandoffInitialization)
	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT id FROM admin_identity WHERE id = 1 FOR UPDATE")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT .* FROM admin_credential_handoffs.*FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("INSERT INTO admin_credential_handoffs").WillReturnResult(sqlmock.NewResult(9, 1))
	for range record.RecoveryCodeHashes {
		mock.ExpectExec("INSERT INTO admin_handoff_recovery_codes").WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectCommit()
	handoff, err := repository.PrepareInitialization(context.Background(), record)
	if err != nil {
		t.Fatalf("PrepareInitialization() error=%v", err)
	}
	if handoff.ID != 9 || handoff.State != HandoffPending || !bytes.Equal(handoff.Archive, record.Archive) {
		t.Fatalf("handoff=%#v", handoff)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryActivatesInitializationByTokenWithoutCreatingSession(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 6, 15, 0, time.UTC)
	handoff := sqlHandoffRecord(now.Add(-time.Minute), HandoffInitialization)
	handoffID := int64(9)
	columns := []string{"id", "handoff_kind", "handoff_state", "request_hash", "token_hash", "token_ciphertext", "uid_ciphertext", "uid_lookup", "email_ciphertext", "totp_secret_ciphertext", "totp_uri_ciphertext", "archive_password_ciphertext", "recovery_archive", "created_at", "expires_at", "mail_delivered_at"}
	rows := sqlmock.NewRows(columns).AddRow(handoffID, handoff.Kind, HandoffPending, handoff.RequestHash, handoff.TokenHash, handoff.TokenCiphertext, nil, nil, handoff.EmailCiphertext, handoff.TOTPSecretCiphertext, handoff.TOTPURICiphertext, handoff.PasswordCiphertext, handoff.Archive, handoff.CreatedAt, handoff.ExpiresAt, now.Add(-time.Second))
	codeRows := sqlmock.NewRows([]string{"code_hash"})
	for _, hash := range handoff.RecoveryCodeHashes {
		codeRows.AddRow(hash)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("FROM admin_credential_handoffs WHERE token_hash = ? FOR UPDATE")).WithArgs(handoff.TokenHash).WillReturnRows(rows)
	mock.ExpectQuery(sqlPattern("SELECT code_hash FROM admin_handoff_recovery_codes WHERE handoff_id = ? ORDER BY code_ordinal")).WithArgs(handoffID).WillReturnRows(codeRows)
	mock.ExpectExec(sqlPattern("INSERT INTO admin_identity")).WithArgs(nil, nil, handoff.EmailCiphertext, now, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(sqlPattern("INSERT INTO admin_totp")).WithArgs(handoff.TOTPSecretCiphertext, now).WillReturnResult(sqlmock.NewResult(1, 1))
	for _, hash := range handoff.RecoveryCodeHashes {
		mock.ExpectExec(sqlPattern("INSERT INTO admin_recovery_codes")).WithArgs(int64(1), hash, now).WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectExec(sqlPattern("DELETE FROM admin_handoff_recovery_codes WHERE handoff_id = ?")).WithArgs(handoffID).WillReturnResult(sqlmock.NewResult(0, RecoveryCodeCount))
	mock.ExpectExec(sqlPattern("UPDATE admin_credential_handoffs SET handoff_state = 'confirmed'")).WithArgs(now, handoffID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repository.ActivateInitialization(context.Background(), ActivateInitializationAttempt{TokenHash: handoff.TokenHash, Now: now, TOTPStep: now.Truncate(30 * time.Second)}); err != nil {
		t.Fatalf("ActivateInitialization() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryCleanupRemovesExpiredAndLegacyUIDPendingHandoffs(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 6, 20, 0, time.UTC)
	mock.ExpectExec(sqlPattern("DELETE FROM admin_credential_handoffs WHERE handoff_state = 'pending' AND (expires_at <= ? OR uid_ciphertext IS NOT NULL OR uid_lookup IS NOT NULL) ORDER BY id LIMIT ?")).WithArgs(now, defaultCleanupLimit).WillReturnResult(sqlmock.NewResult(0, 2))
	if err := repository.CleanupExpiredHandoffs(context.Background(), now, defaultCleanupLimit); err != nil {
		t.Fatalf("CleanupExpiredHandoffs() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryHandoffMailClaimSerializesAndRereadsDelivery(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 6, 30, 0, time.UTC)
	lockName := handoffMailLockPrefix + "9"

	mock.ExpectQuery(sqlPattern("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, handoffMailLockWait).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectQuery(sqlPattern("SELECT handoff_state, mail_delivered_at IS NOT NULL FROM admin_credential_handoffs WHERE id = ?")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"handoff_state", "mail_delivered"}).AddRow(HandoffPending, false))
	claim, err := repository.AcquireHandoffMailClaim(context.Background(), 9)
	if err != nil {
		t.Fatalf("AcquireHandoffMailClaim() error = %v", err)
	}
	if claim.MailDelivered() {
		t.Fatal("fresh handoff unexpectedly marked delivered")
	}
	mock.ExpectExec(sqlPattern("UPDATE admin_credential_handoffs SET mail_attempt_count = mail_attempt_count + 1, last_mail_attempt_at = ?, mail_delivered_at = ? WHERE id = ? AND handoff_state = 'pending' AND mail_delivered_at IS NULL")).
		WithArgs(now, now, int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := claim.MarkAttempt(now, true); err != nil {
		t.Fatalf("MarkAttempt() error = %v", err)
	}
	mock.ExpectQuery(sqlPattern("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
	if err := claim.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryHandoffMailClaimReleaseIgnoresRequestCancellation(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	lockName := handoffMailLockPrefix + "11"
	ctx, cancel := context.WithCancel(context.Background())

	mock.ExpectQuery(sqlPattern("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, handoffMailLockWait).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectQuery(sqlPattern("SELECT handoff_state, mail_delivered_at IS NOT NULL FROM admin_credential_handoffs WHERE id = ?")).
		WithArgs(int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"handoff_state", "mail_delivered"}).AddRow(HandoffPending, true))
	claim, err := repository.AcquireHandoffMailClaim(ctx, 11)
	if err != nil {
		t.Fatalf("AcquireHandoffMailClaim() error = %v", err)
	}
	cancel()
	mock.ExpectQuery(sqlPattern("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))
	if err := claim.Release(); err != nil {
		t.Fatalf("Release() after request cancellation error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryHandoffMailClaimHasBoundedContentionFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	lockName := handoffMailLockPrefix + "13"

	mock.ExpectQuery(sqlPattern("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, handoffMailLockWait).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(0))
	if _, err := repository.AcquireHandoffMailClaim(context.Background(), 13); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AcquireHandoffMailClaim() error = %v, want ErrUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryHandoffMailClaimReleaseFailureIsGeneric(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	lockName := handoffMailLockPrefix + "17"

	mock.ExpectQuery(sqlPattern("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, handoffMailLockWait).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(1))
	mock.ExpectQuery(sqlPattern("SELECT handoff_state, mail_delivered_at IS NOT NULL FROM admin_credential_handoffs WHERE id = ?")).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"handoff_state", "mail_delivered"}).AddRow(HandoffPending, true))
	claim, err := repository.AcquireHandoffMailClaim(context.Background(), 17)
	if err != nil {
		t.Fatalf("AcquireHandoffMailClaim() error = %v", err)
	}
	mock.ExpectQuery(sqlPattern("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnError(errors.New("private database detail"))
	if err := claim.Release(); !errors.Is(err, ErrUnavailable) || stringsContains(err.Error(), "private") {
		t.Fatalf("Release() error = %v, want generic ErrUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRecoveryHandoffLocksAdminIdentityBeforeHandoffAndRecoveryCodes(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 7, 0, 0, time.UTC)
	tokenHash := bytes.Repeat([]byte{2}, sha256.Size)
	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(1))
	columns := []string{"id", "handoff_kind", "handoff_state", "request_hash", "token_hash", "token_ciphertext", "uid_ciphertext", "uid_lookup", "email_ciphertext", "totp_secret_ciphertext", "totp_uri_ciphertext", "archive_password_ciphertext", "recovery_archive", "created_at", "expires_at", "mail_delivered_at", "reserved_recovery_code_id"}
	mock.ExpectQuery(sqlPattern("SELECT " + handoffColumns + ", reserved_recovery_code_id FROM admin_credential_handoffs WHERE token_hash = ? FOR UPDATE")).WithArgs(tokenHash).WillReturnRows(sqlmock.NewRows(columns).AddRow(7, HandoffRecovery, HandoffPending, bytes.Repeat([]byte{1}, 32), tokenHash, []byte("token"), nil, nil, []byte("email"), []byte("secret"), []byte("uri"), []byte("password"), []byte("archive"), now.Add(-time.Minute), now.Add(time.Minute), nil, 11))
	codeRows := sqlmock.NewRows([]string{"code_hash"})
	for index := 0; index < RecoveryCodeCount; index++ {
		codeRows.AddRow(bytes.Repeat([]byte{byte(index + 1)}, sha256.Size))
	}
	mock.ExpectQuery(sqlPattern("SELECT code_hash FROM admin_handoff_recovery_codes WHERE handoff_id = ? ORDER BY code_ordinal")).WithArgs(int64(7)).WillReturnRows(codeRows)
	mock.ExpectQuery(sqlPattern("SELECT 1 FROM admin_recovery_codes WHERE id = ? AND admin_identity_id = 1 AND used_at IS NULL AND invalidated_at IS NULL FOR UPDATE")).WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(1))
	mock.ExpectExec("UPDATE admin_recovery_codes SET used_at").WithArgs(now, int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE admin_recovery_codes SET invalidated_at").WithArgs(now).WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec("UPDATE admin_totp SET secret_ciphertext").WithArgs([]byte("secret"), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE admin_identity SET credential_epoch").WithArgs(now).WillReturnError(errors.New("database detail must stay private"))
	mock.ExpectRollback()
	if err := repository.ConfirmRecoveryHandoff(context.Background(), tokenHash, now); !errors.Is(err, ErrUnavailable) || stringsContains(err.Error(), "database detail") {
		t.Fatalf("ConfirmRecoveryHandoff() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRecoveryHandoffLocksAdminIdentityBeforeConfirmedHandoff(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	tokenHash := bytes.Repeat([]byte{3}, sha256.Size)
	columns := []string{"id", "handoff_kind", "handoff_state", "request_hash", "token_hash", "token_ciphertext", "uid_ciphertext", "uid_lookup", "email_ciphertext", "totp_secret_ciphertext", "totp_uri_ciphertext", "archive_password_ciphertext", "recovery_archive", "created_at", "expires_at", "mail_delivered_at", "reserved_recovery_code_id"}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(1))
	mock.ExpectQuery(sqlPattern("SELECT " + handoffColumns + ", reserved_recovery_code_id FROM admin_credential_handoffs WHERE token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(7, HandoffRecovery, HandoffConfirmed, bytes.Repeat([]byte{1}, sha256.Size), tokenHash, nil, nil, nil, nil, nil, nil, nil, nil, now.Add(-time.Minute), now.Add(time.Minute), nil, nil))
	mock.ExpectCommit()
	if err := repository.ConfirmRecoveryHandoff(context.Background(), tokenHash, now); err != nil {
		t.Fatalf("ConfirmRecoveryHandoff() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRecoveryHandoffLocksAdminIdentityBeforeExpiredHandoff(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 21, 10, 1, 0, 0, time.UTC)
	tokenHash := bytes.Repeat([]byte{4}, sha256.Size)
	columns := []string{"id", "handoff_kind", "handoff_state", "request_hash", "token_hash", "token_ciphertext", "uid_ciphertext", "uid_lookup", "email_ciphertext", "totp_secret_ciphertext", "totp_uri_ciphertext", "archive_password_ciphertext", "recovery_archive", "created_at", "expires_at", "mail_delivered_at", "reserved_recovery_code_id"}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(1))
	mock.ExpectQuery(sqlPattern("SELECT " + handoffColumns + ", reserved_recovery_code_id FROM admin_credential_handoffs WHERE token_hash = ? FOR UPDATE")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(8, HandoffRecovery, HandoffPending, bytes.Repeat([]byte{1}, sha256.Size), tokenHash, nil, nil, nil, nil, nil, nil, nil, nil, now.Add(-time.Minute), now, nil, nil))
	mock.ExpectRollback()
	if err := repository.ConfirmRecoveryHandoff(context.Background(), tokenHash, now); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("ConfirmRecoveryHandoff() error=%v, want ErrAuthenticationFailed", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRejectsDifferentRequestForPendingAdministratorRecovery(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 8, 0, 0, time.UTC)
	candidate := sqlHandoffRecord(now, HandoffRecovery)
	codeHash := bytes.Repeat([]byte{0x32}, sha256.Size)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT credential_epoch, email_ciphertext FROM admin_identity").WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "email_ciphertext"}).AddRow(1, candidate.EmailCiphertext))
	mock.ExpectQuery("SELECT id FROM admin_recovery_codes").WithArgs(codeHash).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
	columns := []string{"id", "handoff_kind", "handoff_state", "request_hash", "token_hash", "token_ciphertext", "uid_ciphertext", "uid_lookup", "email_ciphertext", "totp_secret_ciphertext", "totp_uri_ciphertext", "archive_password_ciphertext", "recovery_archive", "created_at", "expires_at", "mail_delivered_at", "reserved_recovery_code_id"}
	mock.ExpectQuery("SELECT .*admin_identity_id = 1 FOR UPDATE").WillReturnRows(sqlmock.NewRows(columns).AddRow(7, HandoffRecovery, HandoffPending, bytes.Repeat([]byte{0x7f}, sha256.Size), candidate.TokenHash, candidate.TokenCiphertext, nil, nil, candidate.EmailCiphertext, candidate.TOTPSecretCiphertext, candidate.TOTPURICiphertext, candidate.PasswordCiphertext, candidate.Archive, now, now.Add(time.Minute), nil, 11))
	mock.ExpectRollback()
	if _, err := repository.PrepareRecoveryHandoff(context.Background(), 1, codeHash, candidate); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("PrepareRecoveryHandoff() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRecoveryPreparationRejectsChangedEmailIdentity(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 8, 30, 0, time.UTC)
	candidate := sqlHandoffRecord(now, HandoffRecovery)
	codeHash := bytes.Repeat([]byte{0x33}, sha256.Size)
	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch, email_ciphertext FROM admin_identity WHERE id = 1 FOR UPDATE")).WillReturnRows(
		sqlmock.NewRows([]string{"credential_epoch", "email_ciphertext"}).AddRow(3, bytes.Repeat([]byte{0x7f}, 48)),
	)
	mock.ExpectRollback()
	if _, err := repository.PrepareRecoveryHandoff(context.Background(), 2, codeHash, candidate); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("PrepareRecoveryHandoff() changed identity error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryEmailLoginSessionWritesNullTOTPAndVerifiesAmbiguousCommit(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 12, 12, 0, 0, time.UTC)
	attempt := EmailLoginSessionAttempt{ExpectedCredentialEpoch: 3, TokenHash: bytes.Repeat([]byte{0x53}, sha256.Size), CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(3))
	mock.ExpectExec(sqlPattern("INSERT INTO site_sessions (admin_identity_id, token_hash, credential_epoch, created_at, expires_at, totp_verified_at) VALUES (?, ?, ?, ?, ?, NULL)")).
		WithArgs(int64(1), attempt.TokenHash, int64(3), now, now.Add(7*24*time.Hour)).
		WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectCommit().WillReturnError(errors.New("ambiguous commit"))
	mock.ExpectQuery(sqlPattern("SELECT 1 FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? AND credential_epoch = ? AND created_at = ? AND expires_at = ? AND totp_verified_at IS NULL LIMIT 1")).
		WithArgs(attempt.TokenHash, int64(3), now, now.Add(7*24*time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"present"}).AddRow(1))
	if err := repository.CreateEmailLoginSession(context.Background(), attempt); err != nil {
		t.Fatalf("CreateEmailLoginSession() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryEmailLoginSessionVerificationSurvivesCancelledRequest(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 12, 13, 0, 0, time.UTC)
	attempt := EmailLoginSessionAttempt{ExpectedCredentialEpoch: 3, TokenHash: bytes.Repeat([]byte{0x54}, sha256.Size), CreatedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour)}
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	mock.ExpectQuery(sqlPattern("SELECT 1 FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? AND credential_epoch = ? AND created_at = ? AND expires_at = ? AND totp_verified_at IS NULL LIMIT 1")).
		WithArgs(attempt.TokenHash, int64(3), now, now.Add(7*24*time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"present"}).AddRow(1))
	if err := repository.verifyEmailLoginSession(requestContext, attempt); err != nil {
		t.Fatalf("verifyEmailLoginSession() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryFindSessionAcceptsNullEmailLoginTOTP(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 12, 14, 0, 0, time.UTC)
	tokenHash := bytes.Repeat([]byte{0x55}, sha256.Size)
	mock.ExpectQuery(sqlPattern("SELECT s.id, s.credential_epoch, s.expires_at, s.totp_verified_at, s.revoked_at, a.credential_epoch FROM site_sessions AS s JOIN admin_identity AS a ON a.id = s.admin_identity_id WHERE s.admin_identity_id = 1 AND s.token_hash = ? LIMIT 1")).
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "totp_verified_at", "revoked_at", "current_epoch"}).AddRow(9, 3, now.Add(time.Hour), nil, nil, 3))
	session, err := repository.FindSession(context.Background(), tokenHash, now)
	if err != nil || session.ID != 9 || session.CredentialEpoch != 3 || !session.ExpiresAt.Equal(now.Add(time.Hour)) || !session.TOTPVerifiedAt.IsZero() {
		t.Fatalf("FindSession() = %#v, %v", session, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRevokesOnlyAdministratorSessionToken(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 12, 15, 0, 0, time.UTC)
	tokenHash := bytes.Repeat([]byte{0x56}, sha256.Size)
	mock.ExpectExec(sqlPattern("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE admin_identity_id = 1 AND token_hash = ?")).WithArgs(now, tokenHash).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repository.RevokeSession(context.Background(), tokenHash, now); err != nil {
		t.Fatalf("RevokeSession() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeRepositoryDSNForcesParsedUTCTime(t *testing.T) {
	normalized, err := normalizeRepositoryDSN("user:password@tcp(127.0.0.1:3306)/gift_panel")
	if err != nil {
		t.Fatalf("normalizeRepositoryDSN() error = %v", err)
	}
	parsed, err := mysql.ParseDSN(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.ParseTime || parsed.Loc != time.UTC || parsed.Params["time_zone"] != "'+00:00'" {
		t.Fatalf("normalized DSN ParseTime=%v Loc=%v params=%v", parsed.ParseTime, parsed.Loc, parsed.Params)
	}
}

func sqlInitializationRecord(now time.Time) InitializationRecord {
	hashes := make([][]byte, RecoveryCodeCount)
	for index := range hashes {
		hashes[index] = bytes.Repeat([]byte{byte(index + 1)}, sha256.Size)
	}
	return InitializationRecord{
		Identity: IdentityRecord{
			CredentialEpoch: 1,
			EmailCiphertext: bytes.Repeat([]byte{0x33}, 64), TOTPSecretCiphertext: bytes.Repeat([]byte{0x44}, 64),
		},
		RecoveryCodeHashes: hashes,
		CreatedAt:          now,
	}
}

func sqlHandoffRecord(now time.Time, kind string) HandoffRecord {
	hashes := make([][]byte, RecoveryCodeCount)
	for index := range hashes {
		hashes[index] = bytes.Repeat([]byte{byte(index + 1)}, sha256.Size)
	}
	return HandoffRecord{Kind: kind, RequestHash: bytes.Repeat([]byte{1}, sha256.Size), TokenHash: bytes.Repeat([]byte{2}, sha256.Size), TokenCiphertext: []byte("token-ciphertext"), EmailCiphertext: []byte("email-ciphertext"), TOTPSecretCiphertext: []byte("totp-ciphertext"), TOTPURICiphertext: []byte("uri-ciphertext"), PasswordCiphertext: []byte("password-ciphertext"), Archive: []byte("encrypted-archive"), RecoveryCodeHashes: hashes, CreatedAt: now, ExpiresAt: now.Add(defaultHandoffTTL)}
}

func sqlPattern(fragment string) string { return ".*" + regexp.QuoteMeta(fragment) + ".*" }

func stringsContains(value, fragment string) bool {
	return bytes.Contains([]byte(value), []byte(fragment))
}

type fixedTOTP struct{}

func (fixedTOTP) Generate(string, string) (string, string, error) {
	return "TESTSECRET", "otpauth://totp/GiftPanel:owner@example.com?secret=TESTSECRET", nil
}

func (fixedTOTP) Validate(code, secret string, now time.Time) (time.Time, bool) {
	if code != "123456" || secret != "TESTSECRET" {
		return time.Time{}, false
	}
	return now.Truncate(30 * time.Second), true
}

type futureStepTOTP struct{ fixedTOTP }

func (futureStepTOTP) Validate(code, secret string, now time.Time) (time.Time, bool) {
	if code != "123456" || secret != "TESTSECRET" {
		return time.Time{}, false
	}
	return now.Add(30 * time.Second), true
}

func newTestService(t *testing.T, repository Repository, sender MailSender, now time.Time) *Service {
	t.Helper()
	return newTestServiceWithClock(t, repository, sender, func() time.Time { return now })
}

func newTestServiceWithClock(t *testing.T, repository Repository, sender MailSender, now func() time.Time) *Service {
	t.Helper()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, keys, sender, ServiceOptions{
		Now: now, TOTP: fixedTOTP{}, Random: &sequenceReader{},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func emailLoginForTest(t *testing.T, service *Service, sender *MemorySender) LoginResult {
	t.Helper()
	before := len(sender.Messages())
	challenge, err := service.BeginEmailLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginEmailLogin() error = %v", err)
	}
	messages := sender.Messages()
	if len(messages) != before+1 {
		t.Fatalf("email login messages=%d, want %d", len(messages), before+1)
	}
	code := regexp.MustCompile(`\b[0-9]{6}\b`).FindString(messages[len(messages)-1].Text)
	if code == "" {
		t.Fatalf("email login message omitted code: %q", messages[len(messages)-1].Text)
	}
	login, err := service.VerifyEmailLogin(context.Background(), challenge.ChallengeID, code)
	if err != nil {
		t.Fatalf("VerifyEmailLogin() error = %v", err)
	}
	return login
}

func initializedMemoryRepository(t *testing.T, now time.Time) *memoryRepository {
	t.Helper()
	repository := newMemoryRepository()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	emailCiphertext, _ := keys.Seal("admin_email", []byte("owner@example.com"))
	secretCiphertext, _ := keys.Seal("admin_totp", []byte("TESTSECRET"))
	repository.identity = IdentityRecord{
		CredentialEpoch: 1, EmailCiphertext: emailCiphertext, TOTPSecretCiphertext: secretCiphertext,
	}
	repository.initialized = true
	repository.rotatedAt = now
	return repository
}

type sequenceReader struct {
	mu      sync.Mutex
	counter uint64
	buffer  []byte
}

type failOnceSender struct {
	mu       sync.Mutex
	attempts int
	accepted MemorySender
}

func (sender *failOnceSender) Send(ctx context.Context, message Message) error {
	sender.mu.Lock()
	sender.attempts++
	attempt := sender.attempts
	sender.mu.Unlock()
	if attempt == 1 {
		return errors.New("synthetic smtp failure")
	}
	return sender.accepted.Send(ctx, message)
}

func (sender *failOnceSender) Attempts() int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.attempts
}

func (sender *failOnceSender) Messages() []Message { return sender.accepted.Messages() }

func (reader *sequenceReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	written := 0
	for written < len(buffer) {
		if len(reader.buffer) == 0 {
			reader.counter++
			seed := make([]byte, 8)
			binary.BigEndian.PutUint64(seed, reader.counter)
			digest := sha256.Sum256(seed)
			reader.buffer = append(reader.buffer[:0], digest[:]...)
		}
		count := copy(buffer[written:], reader.buffer)
		reader.buffer = reader.buffer[count:]
		written += count
	}
	return len(buffer), nil
}

type memoryRepository struct {
	mu              sync.Mutex
	initialized     bool
	identity        IdentityRecord
	rotatedAt       time.Time
	lastStep        time.Time
	sessions        map[[sha256.Size]byte]AdminSession
	activeCodes     map[[sha256.Size]byte]struct{}
	usedCodes       map[[sha256.Size]byte]struct{}
	handoffs        map[int64]PendingHandoff
	handoffReserved map[int64][sha256.Size]byte
	mailClaims      map[int64]chan struct{}
	nextHandoffID   int64
	operations      map[[sha256.Size]byte]OperationAuthorization
	operationSteps  map[time.Time]struct{}
}

type unavailableEmailSessionRepository struct{ *memoryRepository }

func (unavailableEmailSessionRepository) CreateEmailLoginSession(context.Context, EmailLoginSessionAttempt) error {
	return ErrUnavailable
}

type failedEmailSessionRepository struct {
	*memoryRepository
	err error
}

func (repository failedEmailSessionRepository) CreateEmailLoginSession(context.Context, EmailLoginSessionAttempt) error {
	return repository.err
}

type synchronizedPrepareRepository struct {
	*memoryRepository
	prepared chan struct{}
	release  chan struct{}
}

func (repository *synchronizedPrepareRepository) PrepareInitialization(ctx context.Context, record HandoffRecord) (PendingHandoff, error) {
	handoff, err := repository.memoryRepository.PrepareInitialization(ctx, record)
	repository.prepared <- struct{}{}
	select {
	case <-repository.release:
		return handoff, err
	case <-ctx.Done():
		return PendingHandoff{}, ctx.Err()
	}
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		sessions: make(map[[sha256.Size]byte]AdminSession), activeCodes: make(map[[sha256.Size]byte]struct{}), usedCodes: make(map[[sha256.Size]byte]struct{}), handoffs: make(map[int64]PendingHandoff), handoffReserved: make(map[int64][sha256.Size]byte), mailClaims: make(map[int64]chan struct{}), operations: make(map[[sha256.Size]byte]OperationAuthorization), operationSteps: make(map[time.Time]struct{}),
	}
}

func (repository *memoryRepository) CreateOperationAuthorization(_ context.Context, attempt OperationAuthorizationAttempt) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	var session AdminSession
	for hash, candidate := range repository.sessions {
		if bytes.Equal(hash[:], attempt.SessionTokenHash) {
			session = candidate
			break
		}
	}
	if session.ID == 0 || session.Revoked || !session.ExpiresAt.After(attempt.CreatedAt) || session.CredentialEpoch != attempt.ExpectedCredentialEpoch {
		return ErrAuthenticationFailed
	}
	if _, exists := repository.operationSteps[attempt.TOTPStep]; exists {
		return ErrAuthenticationFailed
	}
	var tokenHash [sha256.Size]byte
	copy(tokenHash[:], attempt.AuthorizationTokenHash)
	repository.operations[tokenHash] = OperationAuthorization{Purpose: attempt.Purpose, Target: attempt.Target, ExpiresAt: attempt.ExpiresAt}
	repository.operationSteps[attempt.TOTPStep] = struct{}{}
	return nil
}

func (repository *memoryRepository) ConsumeOperationAuthorization(_ context.Context, _ *sql.Tx, sessionTokenHash, authorizationTokenHash []byte, purpose security.OperationPurpose, target string, now time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	var session AdminSession
	for hash, candidate := range repository.sessions {
		if bytes.Equal(hash[:], sessionTokenHash) {
			session = candidate
			break
		}
	}
	var key [sha256.Size]byte
	copy(key[:], authorizationTokenHash)
	operation, ok := repository.operations[key]
	if session.ID == 0 || session.Revoked || !session.ExpiresAt.After(now) || !ok || operation.ConsumedAt != nil || !operation.ExpiresAt.After(now) || operation.Purpose != purpose || operation.Target != target {
		return ErrAuthenticationFailed
	}
	consumedAt := now
	operation.ConsumedAt = &consumedAt
	repository.operations[key] = operation
	return nil
}

func (repository *memoryRepository) operationByToken(keys security.Keyring, token string) (OperationAuthorization, bool) {
	hash, err := keys.HashToken("admin_operation_authorization", []byte(token))
	if err != nil {
		return OperationAuthorization{}, false
	}
	var key [sha256.Size]byte
	copy(key[:], hash)
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, ok := repository.operations[key]
	return operation, ok
}

func pendingFromRecord(id int64, record HandoffRecord) PendingHandoff {
	return PendingHandoff{ID: id, Kind: record.Kind, State: HandoffPending, RequestHash: bytes.Clone(record.RequestHash), TokenHash: bytes.Clone(record.TokenHash), TokenCiphertext: bytes.Clone(record.TokenCiphertext), UIDCiphertext: bytes.Clone(record.UIDCiphertext), UIDLookup: bytes.Clone(record.UIDLookup), EmailCiphertext: bytes.Clone(record.EmailCiphertext), TOTPSecretCiphertext: bytes.Clone(record.TOTPSecretCiphertext), TOTPURICiphertext: bytes.Clone(record.TOTPURICiphertext), PasswordCiphertext: bytes.Clone(record.PasswordCiphertext), Archive: bytes.Clone(record.Archive), RecoveryCodeHashes: cloneByteSlices(record.RecoveryCodeHashes), CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt}
}

func (repository *memoryRepository) PrepareInitialization(_ context.Context, record HandoffRecord) (PendingHandoff, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.initialized {
		return PendingHandoff{}, ErrAlreadyInitialized
	}
	for _, handoff := range repository.handoffs {
		if handoff.Kind == HandoffInitialization && handoff.State == HandoffPending {
			if len(handoff.UIDCiphertext) != 0 || len(handoff.UIDLookup) != 0 || !handoff.ExpiresAt.After(record.CreatedAt) {
				delete(repository.handoffs, handoff.ID)
				continue
			}
			if handoff.ExpiresAt.After(record.CreatedAt) && bytes.Equal(handoff.RequestHash, record.RequestHash) {
				return handoff, nil
			}
			return PendingHandoff{}, ErrAlreadyInitialized
		}
	}
	repository.nextHandoffID++
	handoff := pendingFromRecord(repository.nextHandoffID, record)
	repository.handoffs[handoff.ID] = handoff
	return handoff, nil
}

func (repository *memoryRepository) ActivateInitialization(_ context.Context, attempt ActivateInitializationAttempt) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	var handoff PendingHandoff
	for _, candidate := range repository.handoffs {
		if bytes.Equal(candidate.TokenHash, attempt.TokenHash) {
			handoff = candidate
			break
		}
	}
	if handoff.ID == 0 || repository.initialized || handoff.State != HandoffPending || !handoff.ExpiresAt.After(attempt.Now) || len(handoff.UIDCiphertext) != 0 || len(handoff.UIDLookup) != 0 {
		return ErrAuthenticationFailed
	}
	repository.initialized = true
	repository.identity = IdentityRecord{CredentialEpoch: 1, EmailCiphertext: bytes.Clone(handoff.EmailCiphertext), TOTPSecretCiphertext: bytes.Clone(handoff.TOTPSecretCiphertext)}
	for _, hash := range handoff.RecoveryCodeHashes {
		key, _ := hashKey(hash)
		repository.activeCodes[key] = struct{}{}
	}
	handoff.State, handoff.TokenCiphertext, handoff.TOTPSecretCiphertext, handoff.TOTPURICiphertext, handoff.PasswordCiphertext, handoff.Archive, handoff.RecoveryCodeHashes = HandoffConfirmed, nil, nil, nil, nil, nil, nil
	repository.handoffs[handoff.ID] = handoff
	return nil
}

func (repository *memoryRepository) PrepareRecoveryHandoff(_ context.Context, expectedCredentialEpoch int64, codeHash []byte, record HandoffRecord) (PendingHandoff, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	codeKey, ok := hashKey(codeHash)
	if !ok || !repository.initialized || repository.identity.CredentialEpoch != expectedCredentialEpoch || subtle.ConstantTimeCompare(repository.identity.EmailCiphertext, record.EmailCiphertext) != 1 {
		return PendingHandoff{}, ErrAuthenticationFailed
	}
	if _, active := repository.activeCodes[codeKey]; !active {
		return PendingHandoff{}, ErrAuthenticationFailed
	}
	for id, reserved := range repository.handoffReserved {
		handoff := repository.handoffs[id]
		if handoff.Kind != HandoffRecovery || handoff.State != HandoffPending {
			continue
		}
		if !handoff.ExpiresAt.After(record.CreatedAt) {
			delete(repository.handoffs, id)
			delete(repository.handoffReserved, id)
			continue
		}
		if reserved != codeKey || subtle.ConstantTimeCompare(handoff.RequestHash, record.RequestHash) != 1 {
			return PendingHandoff{}, ErrAuthenticationFailed
		}
		return handoff, nil
	}
	repository.nextHandoffID++
	handoff := pendingFromRecord(repository.nextHandoffID, record)
	repository.handoffs[handoff.ID] = handoff
	repository.handoffReserved[handoff.ID] = codeKey
	return handoff, nil
}

func (repository *memoryRepository) HandoffByToken(_ context.Context, tokenHash []byte) (PendingHandoff, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, handoff := range repository.handoffs {
		if bytes.Equal(handoff.TokenHash, tokenHash) {
			return handoff, nil
		}
	}
	return PendingHandoff{}, ErrAuthenticationFailed
}

func (repository *memoryRepository) ConfirmRecoveryHandoff(_ context.Context, tokenHash []byte, now time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for id, handoff := range repository.handoffs {
		if !bytes.Equal(handoff.TokenHash, tokenHash) {
			continue
		}
		if handoff.State == HandoffConfirmed {
			return nil
		}
		reserved, ok := repository.handoffReserved[id]
		if !ok || handoff.State != HandoffPending || !handoff.ExpiresAt.After(now) {
			return ErrAuthenticationFailed
		}
		if _, active := repository.activeCodes[reserved]; !active {
			return ErrAuthenticationFailed
		}
		delete(repository.activeCodes, reserved)
		repository.usedCodes[reserved] = struct{}{}
		clear(repository.activeCodes)
		repository.identity.TOTPSecretCiphertext = bytes.Clone(handoff.TOTPSecretCiphertext)
		repository.identity.CredentialEpoch++
		for key, session := range repository.sessions {
			session.Revoked = true
			repository.sessions[key] = session
		}
		for _, hash := range handoff.RecoveryCodeHashes {
			key, _ := hashKey(hash)
			repository.activeCodes[key] = struct{}{}
		}
		handoff.State, handoff.TokenCiphertext, handoff.TOTPSecretCiphertext, handoff.TOTPURICiphertext, handoff.PasswordCiphertext, handoff.Archive, handoff.RecoveryCodeHashes = HandoffConfirmed, nil, nil, nil, nil, nil, nil
		repository.handoffs[id] = handoff
		return nil
	}
	return ErrAuthenticationFailed
}

type memoryHandoffMailClaim struct {
	repository *memoryRepository
	handoffID  int64
	delivered  bool
	semaphore  chan struct{}
	release    sync.Once
}

func (repository *memoryRepository) AcquireHandoffMailClaim(ctx context.Context, id int64) (HandoffMailClaim, error) {
	repository.mu.Lock()
	semaphore, ok := repository.mailClaims[id]
	if !ok {
		semaphore = make(chan struct{}, 1)
		semaphore <- struct{}{}
		repository.mailClaims[id] = semaphore
	}
	repository.mu.Unlock()
	select {
	case <-semaphore:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	repository.mu.Lock()
	handoff, ok := repository.handoffs[id]
	repository.mu.Unlock()
	if !ok {
		semaphore <- struct{}{}
		return nil, ErrUnavailable
	}
	return &memoryHandoffMailClaim{repository: repository, handoffID: id, delivered: handoff.MailDelivered, semaphore: semaphore}, nil
}

func (claim *memoryHandoffMailClaim) MailDelivered() bool { return claim.delivered }

func (claim *memoryHandoffMailClaim) MarkAttempt(_ time.Time, delivered bool) error {
	claim.repository.mu.Lock()
	defer claim.repository.mu.Unlock()
	handoff, ok := claim.repository.handoffs[claim.handoffID]
	if !ok || handoff.State != HandoffPending || handoff.MailDelivered {
		return ErrUnavailable
	}
	if delivered {
		handoff.MailDelivered = true
	}
	claim.repository.handoffs[claim.handoffID] = handoff
	claim.delivered = delivered
	return nil
}

func (claim *memoryHandoffMailClaim) Release() error {
	claim.release.Do(func() { claim.semaphore <- struct{}{} })
	return nil
}

func (repository *memoryRepository) CleanupExpiredHandoffs(_ context.Context, now time.Time, limit int) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for id, handoff := range repository.handoffs {
		if limit == 0 {
			break
		}
		if handoff.State == HandoffPending && (!handoff.ExpiresAt.After(now) || len(handoff.UIDCiphertext) != 0 || len(handoff.UIDLookup) != 0) {
			delete(repository.handoffs, id)
			delete(repository.handoffReserved, id)
			limit--
		}
	}
	return nil
}

func (repository *memoryRepository) Initialize(_ context.Context, record InitializationRecord) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.initialized {
		return ErrAlreadyInitialized
	}
	repository.initialized = true
	repository.identity = record.Identity
	repository.rotatedAt = record.CreatedAt
	for _, hash := range record.RecoveryCodeHashes {
		key, ok := hashKey(hash)
		if !ok {
			return ErrUnavailable
		}
		repository.activeCodes[key] = struct{}{}
	}
	return nil
}

func (repository *memoryRepository) Identity(context.Context) (IdentityRecord, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.initialized {
		return IdentityRecord{}, ErrAuthenticationFailed
	}
	return cloneIdentity(repository.identity), nil
}

func (repository *memoryRepository) CreateEmailLoginSession(_ context.Context, attempt EmailLoginSessionAttempt) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.initialized || attempt.ExpectedCredentialEpoch != repository.identity.CredentialEpoch {
		return ErrAuthenticationFailed
	}
	key, ok := hashKey(attempt.TokenHash)
	if !ok {
		return ErrUnavailable
	}
	repository.sessions[key] = AdminSession{ID: int64(len(repository.sessions) + 1), CredentialEpoch: attempt.ExpectedCredentialEpoch, ExpiresAt: attempt.ExpiresAt}
	return nil
}

func (repository *memoryRepository) FindSession(_ context.Context, tokenHash []byte, now time.Time) (AdminSession, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key, ok := hashKey(tokenHash)
	session, found := repository.sessions[key]
	if !ok || !found || session.Revoked || !session.ExpiresAt.After(now) || session.CredentialEpoch != repository.identity.CredentialEpoch {
		return AdminSession{}, ErrAuthenticationFailed
	}
	return session, nil
}

func (repository *memoryRepository) RevokeSession(_ context.Context, tokenHash []byte, _ time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key, ok := hashKey(tokenHash)
	if !ok {
		return ErrAuthenticationFailed
	}
	if session, found := repository.sessions[key]; found {
		session.Revoked = true
		repository.sessions[key] = session
	}
	return nil
}

func (repository *memoryRepository) ConfirmTOTP(_ context.Context, attempt ConfirmTOTPAttempt) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key, ok := hashKey(attempt.TokenHash)
	session, found := repository.sessions[key]
	if !ok || !found || session.Revoked || !session.ExpiresAt.After(attempt.Now) || attempt.ExpectedCredentialEpoch != repository.identity.CredentialEpoch || attempt.TOTPStep.After(attempt.Now) || !attempt.TOTPStep.After(repository.lastStep) {
		return ErrAuthenticationFailed
	}
	repository.lastStep = attempt.Now
	session.TOTPVerifiedAt = attempt.Now
	repository.sessions[key] = session
	return nil
}

func (repository *memoryRepository) RotateRecoveryCodes(_ context.Context, _ security.SensitiveAuthorizer, sessionToken string, newCodeHashes [][]byte, clock func() time.Time) ([]byte, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if sessionToken == "" || clock == nil || !validCodeHashes(newCodeHashes) {
		return nil, ErrInvalidInput
	}
	authorizedAt := clock()
	var sessionKey [sha256.Size]byte
	var session AdminSession
	found := false
	for key, candidate := range repository.sessions {
		if !candidate.Revoked && candidate.ExpiresAt.After(authorizedAt) && candidate.CredentialEpoch == repository.identity.CredentialEpoch && recentTOTPAt(sql.NullTime{Time: candidate.TOTPVerifiedAt, Valid: !candidate.TOTPVerifiedAt.IsZero()}, authorizedAt) {
			sessionKey, session, found = key, candidate, true
			break
		}
	}
	if !found {
		return nil, ErrRecentTOTPRequired
	}
	completedAt := clock()
	if !session.ExpiresAt.After(completedAt) || !recentTOTPAt(sql.NullTime{Time: session.TOTPVerifiedAt, Valid: !session.TOTPVerifiedAt.IsZero()}, completedAt) {
		return nil, ErrRecentTOTPRequired
	}
	clear(repository.activeCodes)
	for _, hash := range newCodeHashes {
		codeKey, valid := hashKey(hash)
		if !valid {
			return nil, ErrUnavailable
		}
		repository.activeCodes[codeKey] = struct{}{}
	}
	session.TOTPVerifiedAt = completedAt
	repository.sessions[sessionKey] = session
	return bytes.Clone(repository.identity.EmailCiphertext), nil
}

func (repository *memoryRepository) sessionCount() int {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return len(repository.sessions)
}

func (repository *memoryRepository) containsSessionToken(token string) bool {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for hash := range repository.sessions {
		if bytes.Contains(hash[:], []byte(token)) {
			return true
		}
	}
	return false
}

func cloneIdentity(record IdentityRecord) IdentityRecord {
	record.UIDCiphertext = bytes.Clone(record.UIDCiphertext)
	record.UIDLookup = bytes.Clone(record.UIDLookup)
	record.EmailCiphertext = bytes.Clone(record.EmailCiphertext)
	record.TOTPSecretCiphertext = bytes.Clone(record.TOTPSecretCiphertext)
	return record
}

func cloneHashes(source map[[sha256.Size]byte]struct{}) map[[sha256.Size]byte]struct{} {
	result := make(map[[sha256.Size]byte]struct{}, len(source))
	for hash := range source {
		result[hash] = struct{}{}
	}
	return result
}

func hashKey(hash []byte) ([sha256.Size]byte, bool) {
	var result [sha256.Size]byte
	if len(hash) != len(result) {
		return result, false
	}
	copy(result[:], hash)
	return result, true
}

var _ Repository = (*memoryRepository)(nil)
