package adminidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
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
	service := newTestService(t, repository, &memoryVerifier{}, sender, now)

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
		t.Fatalf("second Initialize() error = %v, want ErrAlreadyInitialized", err)
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
	repository := newMemoryRepository()
	service := newTestService(t, repository, &memoryVerifier{}, &MemorySender{}, now)

	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, err := service.Initialize(context.Background(), "32249588", "owner@example.com")
			results <- err
		}()
	}
	close(start)
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
	if winners != 1 || alreadyInitialized != 1 {
		t.Fatalf("winners=%d alreadyInitialized=%d", winners, alreadyInitialized)
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
	mu   sync.Mutex
	next byte
}

func (reader *sequenceReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for index := range buffer {
		reader.next++
		buffer[index] = reader.next
	}
	return len(buffer), nil
}

type memoryRepository struct {
	mu          sync.Mutex
	initialized bool
	identity    IdentityRecord
	rotatedAt   time.Time
	lastStep    time.Time
	sessions    map[[sha256.Size]byte]AdminSession
	activeCodes map[[sha256.Size]byte]struct{}
	usedCodes   map[[sha256.Size]byte]struct{}
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		sessions: make(map[[sha256.Size]byte]AdminSession), activeCodes: make(map[[sha256.Size]byte]struct{}), usedCodes: make(map[[sha256.Size]byte]struct{}),
	}
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
