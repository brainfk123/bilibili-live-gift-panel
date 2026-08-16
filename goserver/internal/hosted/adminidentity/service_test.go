package adminidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"regexp"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestInitializeStoresOneAdministratorAndEmailsOnlyEncryptedCodes(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	sender := &MemorySender{}
	verifier := &memoryVerifier{verification: identity.Verification{UID: "32249588", CompletedAt: now}}
	service := newTestService(t, repository, verifier, sender, now)

	result, err := service.Initialize(context.Background(), "32249588", "owner@example.com")
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if result.TOTPURI != "otpauth://totp/GiftPanel:owner@example.com?secret=TESTSECRET" {
		t.Fatalf("TOTPURI = %q", result.TOTPURI)
	}
	if len(result.RecoveryPassword) != 20 {
		t.Fatalf("RecoveryPassword length = %d, want 20", len(result.RecoveryPassword))
	}

	if repository.initialized {
		t.Fatal("initialization activated administrator before matching Bilibili proof and new TOTP")
	}
	second, err := service.Initialize(context.Background(), "32249588", "owner@example.com")
	if err != nil || second != result {
		t.Fatalf("retry Initialize() = %#v, %v; want same handoff", second, err)
	}
	if len(sender.Messages()) != 1 {
		t.Fatalf("successful retry sent %d archives, want stable prior delivery", len(sender.Messages()))
	}
	if _, err := service.VerifyLogin(context.Background(), "activate-proof", "123456"); err != nil {
		t.Fatalf("activate pending initialization error = %v", err)
	}
	repository.mu.Lock()
	stored := repository.identity
	codeHashes := cloneHashes(repository.activeCodes)
	repository.mu.Unlock()
	if stored.CredentialEpoch != 1 || len(stored.UIDLookup) != sha256.Size {
		t.Fatalf("stored identity = %#v", stored)
	}
	if bytes.Contains(stored.UIDCiphertext, []byte("32249588")) || bytes.Contains(stored.EmailCiphertext, []byte("owner@example.com")) || bytes.Contains(stored.TOTPSecretCiphertext, []byte("TESTSECRET")) {
		t.Fatal("initialization persisted plaintext administrator secret")
	}
	if len(codeHashes) != RecoveryCodeCount {
		t.Fatalf("stored recovery hashes = %d, want %d", len(codeHashes), RecoveryCodeCount)
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

	if _, err := service.Initialize(context.Background(), "32249588", "owner@example.com"); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("post-activation Initialize() error = %v, want ErrAlreadyInitialized", err)
	}
}

func TestVerifyLoginRequiresMatchingUIDAndRejectsReplayedTOTPStep(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 10, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	verifier := &memoryVerifier{verification: identity.Verification{UID: "11111111", CompletedAt: now}}
	service := newTestService(t, repository, verifier, &MemorySender{}, now)

	if _, err := service.VerifyLogin(context.Background(), "wrong-uid-proof", "123456"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("VerifyLogin(wrong UID) error = %v", err)
	}
	if repository.sessionCount() != 0 {
		t.Fatal("wrong UID created an administrator session")
	}

	verifier.verification = identity.Verification{UID: "32249588", CompletedAt: now}
	login, err := service.VerifyLogin(context.Background(), "matching-proof", "123456")
	if err != nil {
		t.Fatalf("VerifyLogin() error = %v", err)
	}
	if login.Token == "" || !login.ExpiresAt.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("VerifyLogin() = %#v", login)
	}
	if repository.containsSessionToken(login.Token) {
		t.Fatal("repository observed plaintext administrator session token")
	}

	if _, err := service.VerifyLogin(context.Background(), "replayed-proof", "123456"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("VerifyLogin(replayed TOTP step) error = %v", err)
	}
	if repository.sessionCount() != 1 {
		t.Fatalf("replayed TOTP created %d sessions, want one total", repository.sessionCount())
	}
}

func TestVerifyRecentTOTPRejectsReplayAndExpiresAfterFiveMinutes(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 20, 0, 0, time.UTC)
	clock := now
	repository := initializedMemoryRepository(t, now)
	verifier := &memoryVerifier{verification: identity.Verification{UID: "32249588", CompletedAt: now}}
	service := newTestServiceWithClock(t, repository, verifier, &MemorySender{}, func() time.Time { return clock })
	login, err := service.VerifyLogin(context.Background(), "login-proof", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RequireRecentTOTP(context.Background(), login.Token); err != nil {
		t.Fatalf("RequireRecentTOTP() immediately error = %v", err)
	}

	clock = now.Add(30 * time.Second)
	if err := service.VerifyRecentTOTP(context.Background(), login.Token, "123456"); err != nil {
		t.Fatalf("VerifyRecentTOTP(new step) error = %v", err)
	}
	if err := service.VerifyRecentTOTP(context.Background(), login.Token, "123456"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("VerifyRecentTOTP(replay) error = %v", err)
	}

	clock = now.Add(5*time.Minute + 31*time.Second)
	if err := service.RequireRecentTOTP(context.Background(), login.Token); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("RequireRecentTOTP(expired) error = %v, want ErrRecentTOTPRequired", err)
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
	service := newTestService(t, repository, &memoryVerifier{}, sender, now)

	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			_, err := service.Initialize(context.Background(), "32249588", "owner@example.com")
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
	service := newTestService(t, repository, &memoryVerifier{}, sender, now)

	if _, err := service.Initialize(context.Background(), "32249588", "owner@example.com"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first Initialize() error = %v, want ErrUnavailable", err)
	}
	result, err := service.Initialize(context.Background(), "32249588", "owner@example.com")
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

func TestBeginVerificationForgetsMalformedVerifierState(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 40, 0, 0, time.UTC)
	verifier := &memoryVerifier{challenge: identity.Challenge{ID: "malformed-admin-proof", ExpiresAt: now.Add(time.Minute)}}
	service := newTestService(t, newMemoryRepository(), verifier, &MemorySender{}, now)

	if _, err := service.BeginVerification(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("BeginVerification() error = %v", err)
	}
	verifier.mu.Lock()
	forgotten := append([]string(nil), verifier.forgotten...)
	verifier.mu.Unlock()
	if len(forgotten) != 1 || forgotten[0] != "malformed-admin-proof" {
		t.Fatalf("Forget calls = %v", forgotten)
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

func TestSQLRepositoryRecoveryHandoffRollsBackAllCredentialChanges(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 7, 0, 0, time.UTC)
	tokenHash := bytes.Repeat([]byte{2}, sha256.Size)
	mock.ExpectBegin()
	columns := []string{"id", "handoff_kind", "handoff_state", "request_hash", "token_hash", "token_ciphertext", "uid_ciphertext", "uid_lookup", "email_ciphertext", "totp_secret_ciphertext", "totp_uri_ciphertext", "archive_password_ciphertext", "recovery_archive", "created_at", "expires_at", "mail_delivered_at", "reserved_recovery_code_id"}
	mock.ExpectQuery("SELECT .*reserved_recovery_code_id.*FOR UPDATE").WithArgs(tokenHash).WillReturnRows(sqlmock.NewRows(columns).AddRow(7, HandoffRecovery, HandoffPending, bytes.Repeat([]byte{1}, 32), tokenHash, []byte("token"), nil, nil, []byte("email"), []byte("secret"), []byte("uri"), []byte("password"), []byte("archive"), now.Add(-time.Minute), now.Add(time.Minute), nil, 11))
	codeRows := sqlmock.NewRows([]string{"code_hash"})
	for index := 0; index < RecoveryCodeCount; index++ {
		codeRows.AddRow(bytes.Repeat([]byte{byte(index + 1)}, sha256.Size))
	}
	mock.ExpectQuery("SELECT code_hash FROM admin_handoff_recovery_codes").WithArgs(int64(7)).WillReturnRows(codeRows)
	mock.ExpectQuery("SELECT 1 FROM admin_recovery_codes").WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(1))
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

func TestSQLRepositoryRejectsDifferentRequestForPendingAdministratorRecovery(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 10, 8, 0, 0, time.UTC)
	candidate := sqlHandoffRecord(now, HandoffRecovery)
	uidLookup := bytes.Repeat([]byte{0x31}, sha256.Size)
	codeHash := bytes.Repeat([]byte{0x32}, sha256.Size)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT uid_lookup FROM admin_identity").WillReturnRows(sqlmock.NewRows([]string{"uid_lookup"}).AddRow(uidLookup))
	mock.ExpectQuery("SELECT id FROM admin_recovery_codes").WithArgs(codeHash).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
	columns := []string{"id", "handoff_kind", "handoff_state", "request_hash", "token_hash", "token_ciphertext", "uid_ciphertext", "uid_lookup", "email_ciphertext", "totp_secret_ciphertext", "totp_uri_ciphertext", "archive_password_ciphertext", "recovery_archive", "created_at", "expires_at", "mail_delivered_at", "reserved_recovery_code_id"}
	mock.ExpectQuery("SELECT .*admin_identity_id = 1 FOR UPDATE").WillReturnRows(sqlmock.NewRows(columns).AddRow(7, HandoffRecovery, HandoffPending, bytes.Repeat([]byte{0x7f}, sha256.Size), candidate.TokenHash, candidate.TokenCiphertext, nil, nil, candidate.EmailCiphertext, candidate.TOTPSecretCiphertext, candidate.TOTPURICiphertext, candidate.PasswordCiphertext, candidate.Archive, now, now.Add(time.Minute), nil, 11))
	mock.ExpectRollback()
	if _, err := repository.PrepareRecoveryHandoff(context.Background(), uidLookup, codeHash, candidate); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("PrepareRecoveryHandoff() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositorySerializesLoginAndRejectsGlobalTOTPStepReplay(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 12, 10, 0, 0, time.UTC)
	previousStep := now.Truncate(30 * time.Second)
	attempt := LoginSessionAttempt{
		ExpectedCredentialEpoch: 3, TokenHash: bytes.Repeat([]byte{0x51}, sha256.Size),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), TOTPStep: previousStep,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(3))
	mock.ExpectQuery(sqlPattern("SELECT MAX(totp_verified_at) FROM site_sessions WHERE admin_identity_id = 1 AND credential_epoch = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"max_totp_verified_at"}).AddRow(previousStep))
	mock.ExpectRollback()
	if err := repository.CreateLoginSession(context.Background(), attempt); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("replayed CreateLoginSession() error = %v", err)
	}

	attempt.TOTPStep = previousStep.Add(30 * time.Second)
	attempt.TokenHash = bytes.Repeat([]byte{0x52}, sha256.Size)
	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(3))
	mock.ExpectQuery(sqlPattern("SELECT MAX(totp_verified_at) FROM site_sessions WHERE admin_identity_id = 1 AND credential_epoch = ?")).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"max_totp_verified_at"}).AddRow(previousStep))
	mock.ExpectExec(sqlPattern("INSERT INTO site_sessions")).
		WithArgs(int64(1), attempt.TokenHash, int64(3), now, now.Add(time.Hour), attempt.TOTPStep).
		WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectCommit()
	if err := repository.CreateLoginSession(context.Background(), attempt); err != nil {
		t.Fatalf("new-step CreateLoginSession() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRecoveryRollsBackEpochTOTPCodeAndSessionChangesTogether(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewRepository(database)
	now := time.Date(2026, 8, 16, 12, 20, 0, 0, time.UTC)
	attempt := RecoveryCompletion{
		ExpectedCredentialEpoch: 4,
		UIDLookup:               bytes.Repeat([]byte{0x61}, sha256.Size),
		ConsumedCodeHash:        bytes.Repeat([]byte{0x62}, sha256.Size),
		NewTOTPSecretCiphertext: bytes.Repeat([]byte{0x63}, 48),
		NewCodeHashes:           make([][]byte, RecoveryCodeCount),
		Now:                     now,
	}
	for index := range attempt.NewCodeHashes {
		attempt.NewCodeHashes[index] = bytes.Repeat([]byte{byte(0x70 + index)}, sha256.Size)
	}
	emailCiphertext := bytes.Repeat([]byte{0x42}, 64)

	mock.ExpectBegin()
	mock.ExpectQuery(sqlPattern("SELECT credential_epoch, email_ciphertext FROM admin_identity WHERE id = 1 AND uid_lookup = ? FOR UPDATE")).
		WithArgs(attempt.UIDLookup).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "email_ciphertext"}).AddRow(4, emailCiphertext))
	mock.ExpectExec(sqlPattern("UPDATE admin_recovery_codes SET used_at = ?")).
		WithArgs(now, int64(1), attempt.ConsumedCodeHash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(sqlPattern("UPDATE admin_recovery_codes SET invalidated_at = ?")).
		WithArgs(now, int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 9))
	mock.ExpectExec(sqlPattern("UPDATE admin_totp SET secret_ciphertext = ?, rotated_at = ?")).
		WithArgs(attempt.NewTOTPSecretCiphertext, now, int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(sqlPattern("UPDATE admin_identity SET credential_epoch = credential_epoch + 1, updated_at = ?")).
		WithArgs(now, int64(1), int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(sqlPattern("UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?)")).
		WithArgs(now, int64(1)).
		WillReturnError(errors.New("database detail that must be hidden"))
	mock.ExpectRollback()
	if _, err := repository.CompleteRecovery(context.Background(), attempt); !errors.Is(err, ErrUnavailable) || stringsContains(err.Error(), "database detail") {
		t.Fatalf("CompleteRecovery() error = %v", err)
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
			UIDCiphertext:   bytes.Repeat([]byte{0x11}, 48), UIDLookup: bytes.Repeat([]byte{0x22}, sha256.Size),
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
	return HandoffRecord{Kind: kind, RequestHash: bytes.Repeat([]byte{1}, sha256.Size), TokenHash: bytes.Repeat([]byte{2}, sha256.Size), TokenCiphertext: []byte("token-ciphertext"), UIDCiphertext: []byte("uid-ciphertext"), UIDLookup: bytes.Repeat([]byte{3}, sha256.Size), EmailCiphertext: []byte("email-ciphertext"), TOTPSecretCiphertext: []byte("totp-ciphertext"), TOTPURICiphertext: []byte("uri-ciphertext"), PasswordCiphertext: []byte("password-ciphertext"), Archive: []byte("encrypted-archive"), RecoveryCodeHashes: hashes, CreatedAt: now, ExpiresAt: now.Add(defaultHandoffTTL)}
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

type memoryVerifier struct {
	mu           sync.Mutex
	verification identity.Verification
	challenge    identity.Challenge
	err          error
	forgotten    []string
}

func (verifier *memoryVerifier) Begin(context.Context) (identity.Challenge, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	if verifier.challenge.ID == "" {
		verifier.challenge = identity.Challenge{ID: "admin-proof", QRImage: "data:image/png;base64,qr", ExpiresAt: time.Now().Add(time.Minute)}
	}
	return verifier.challenge, verifier.err
}

func (verifier *memoryVerifier) Poll(context.Context, string) (identity.Verification, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	return verifier.verification, verifier.err
}

func (verifier *memoryVerifier) Forget(challengeID string) {
	verifier.mu.Lock()
	verifier.forgotten = append(verifier.forgotten, challengeID)
	verifier.mu.Unlock()
}

func newTestService(t *testing.T, repository Repository, verifier identity.BiliVerifier, sender MailSender, now time.Time) *Service {
	t.Helper()
	return newTestServiceWithClock(t, repository, verifier, sender, func() time.Time { return now })
}

func newTestServiceWithClock(t *testing.T, repository Repository, verifier identity.BiliVerifier, sender MailSender, now func() time.Time) *Service {
	t.Helper()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, keys, verifier, sender, ServiceOptions{
		Now: now, TOTP: fixedTOTP{}, Random: &sequenceReader{}, SessionTTL: 12 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func initializedMemoryRepository(t *testing.T, now time.Time) *memoryRepository {
	t.Helper()
	repository := newMemoryRepository()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x41}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	uidCiphertext, _ := keys.Seal("admin_uid", []byte("32249588"))
	uidLookup, _ := keys.Lookup("bili_uid", []byte("32249588"))
	emailCiphertext, _ := keys.Seal("admin_email", []byte("owner@example.com"))
	secretCiphertext, _ := keys.Seal("admin_totp", []byte("TESTSECRET"))
	repository.identity = IdentityRecord{
		CredentialEpoch: 1, UIDCiphertext: uidCiphertext, UIDLookup: uidLookup,
		EmailCiphertext: emailCiphertext, TOTPSecretCiphertext: secretCiphertext,
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
		sessions: make(map[[sha256.Size]byte]AdminSession), activeCodes: make(map[[sha256.Size]byte]struct{}), usedCodes: make(map[[sha256.Size]byte]struct{}), handoffs: make(map[int64]PendingHandoff), handoffReserved: make(map[int64][sha256.Size]byte), mailClaims: make(map[int64]chan struct{}),
	}
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

func (repository *memoryRepository) PendingInitialization(_ context.Context, lookup []byte, now time.Time) (PendingHandoff, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, handoff := range repository.handoffs {
		if handoff.Kind == HandoffInitialization && handoff.State == HandoffPending && handoff.ExpiresAt.After(now) && bytes.Equal(handoff.UIDLookup, lookup) {
			return handoff, nil
		}
	}
	return PendingHandoff{}, ErrAuthenticationFailed
}

func (repository *memoryRepository) ActivateInitialization(_ context.Context, attempt ActivateInitializationAttempt) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	handoff, found := repository.handoffs[attempt.HandoffID]
	if !found || repository.initialized || handoff.State != HandoffPending || !handoff.ExpiresAt.After(attempt.CreatedAt) || !bytes.Equal(handoff.UIDLookup, attempt.UIDLookup) {
		return ErrAuthenticationFailed
	}
	repository.initialized = true
	repository.identity = IdentityRecord{CredentialEpoch: 1, UIDCiphertext: bytes.Clone(handoff.UIDCiphertext), UIDLookup: bytes.Clone(handoff.UIDLookup), EmailCiphertext: bytes.Clone(handoff.EmailCiphertext), TOTPSecretCiphertext: bytes.Clone(handoff.TOTPSecretCiphertext)}
	for _, hash := range handoff.RecoveryCodeHashes {
		key, _ := hashKey(hash)
		repository.activeCodes[key] = struct{}{}
	}
	key, _ := hashKey(attempt.TokenHash)
	repository.sessions[key] = AdminSession{ID: 1, CredentialEpoch: 1, ExpiresAt: attempt.ExpiresAt, TOTPVerifiedAt: attempt.TOTPStep}
	repository.lastStep = attempt.TOTPStep
	handoff.State, handoff.TokenCiphertext, handoff.TOTPSecretCiphertext, handoff.TOTPURICiphertext, handoff.PasswordCiphertext, handoff.Archive, handoff.RecoveryCodeHashes = HandoffConfirmed, nil, nil, nil, nil, nil, nil
	repository.handoffs[handoff.ID] = handoff
	return nil
}

func (repository *memoryRepository) PrepareRecoveryHandoff(_ context.Context, uidLookup, codeHash []byte, record HandoffRecord) (PendingHandoff, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	codeKey, ok := hashKey(codeHash)
	if !ok || !repository.initialized || !bytes.Equal(uidLookup, repository.identity.UIDLookup) {
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
		if handoff.State == HandoffPending && !handoff.ExpiresAt.After(now) {
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

func (repository *memoryRepository) CreateLoginSession(_ context.Context, attempt LoginSessionAttempt) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.initialized || attempt.ExpectedCredentialEpoch != repository.identity.CredentialEpoch || !attempt.TOTPStep.After(repository.lastStep) {
		return ErrAuthenticationFailed
	}
	key, ok := hashKey(attempt.TokenHash)
	if !ok {
		return ErrUnavailable
	}
	repository.lastStep = attempt.TOTPStep
	repository.sessions[key] = AdminSession{ID: int64(len(repository.sessions) + 1), CredentialEpoch: attempt.ExpectedCredentialEpoch, ExpiresAt: attempt.ExpiresAt, TOTPVerifiedAt: attempt.TOTPStep}
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

func (repository *memoryRepository) ConfirmTOTP(_ context.Context, attempt ConfirmTOTPAttempt) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key, ok := hashKey(attempt.TokenHash)
	session, found := repository.sessions[key]
	if !ok || !found || session.Revoked || !session.ExpiresAt.After(attempt.Now) || attempt.ExpectedCredentialEpoch != repository.identity.CredentialEpoch || !attempt.TOTPStep.After(repository.lastStep) {
		return ErrAuthenticationFailed
	}
	repository.lastStep = attempt.TOTPStep
	session.TOTPVerifiedAt = attempt.TOTPStep
	repository.sessions[key] = session
	return nil
}

func (repository *memoryRepository) ReplaceRecoveryCodes(_ context.Context, attempt RecoveryReplacement) ([]byte, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key, ok := hashKey(attempt.SessionTokenHash)
	session, found := repository.sessions[key]
	if !ok || !found || session.Revoked || !session.ExpiresAt.After(attempt.Now) || session.CredentialEpoch != repository.identity.CredentialEpoch || attempt.Now.Sub(session.TOTPVerifiedAt) > RecentTOTPWindow {
		return nil, ErrRecentTOTPRequired
	}
	clear(repository.activeCodes)
	for _, hash := range attempt.NewCodeHashes {
		codeKey, valid := hashKey(hash)
		if !valid {
			return nil, ErrUnavailable
		}
		repository.activeCodes[codeKey] = struct{}{}
	}
	return bytes.Clone(repository.identity.EmailCiphertext), nil
}

func (repository *memoryRepository) CompleteRecovery(_ context.Context, attempt RecoveryCompletion) ([]byte, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	codeKey, ok := hashKey(attempt.ConsumedCodeHash)
	if !ok || !repository.initialized || attempt.ExpectedCredentialEpoch != repository.identity.CredentialEpoch || !bytes.Equal(attempt.UIDLookup, repository.identity.UIDLookup) {
		return nil, ErrAuthenticationFailed
	}
	if _, active := repository.activeCodes[codeKey]; !active {
		return nil, ErrAuthenticationFailed
	}
	delete(repository.activeCodes, codeKey)
	repository.usedCodes[codeKey] = struct{}{}
	clear(repository.activeCodes)
	repository.identity.TOTPSecretCiphertext = bytes.Clone(attempt.NewTOTPSecretCiphertext)
	repository.identity.CredentialEpoch++
	repository.rotatedAt = attempt.Now
	for key, session := range repository.sessions {
		session.Revoked = true
		repository.sessions[key] = session
	}
	for _, hash := range attempt.NewCodeHashes {
		newKey, valid := hashKey(hash)
		if !valid {
			return nil, ErrUnavailable
		}
		repository.activeCodes[newKey] = struct{}{}
	}
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
