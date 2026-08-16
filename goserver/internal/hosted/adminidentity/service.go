package adminidentity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"io"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/go-sql-driver/mysql"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	RecentTOTPWindow  = 5 * time.Minute
	defaultProofTTL   = 5 * time.Minute
	defaultSessionTTL = 12 * time.Hour
)

var (
	ErrInvalidInput          = errors.New("admin identity: invalid input")
	ErrAlreadyInitialized    = errors.New("admin identity: already initialized")
	ErrAuthenticationFailed  = errors.New("admin identity: authentication failed")
	ErrRecentTOTPRequired    = errors.New("admin identity: recent totp required")
	ErrUnavailable           = errors.New("admin identity: unavailable")
	ErrArchiveAuthentication = errors.New("admin identity: archive authentication failed")
)

type IdentityRecord struct {
	CredentialEpoch      int64
	UIDCiphertext        []byte
	UIDLookup            []byte
	EmailCiphertext      []byte
	TOTPSecretCiphertext []byte
}

type InitializationRecord struct {
	Identity           IdentityRecord
	RecoveryCodeHashes [][]byte
	CreatedAt          time.Time
}

type AdminSession struct {
	ID              int64
	CredentialEpoch int64
	ExpiresAt       time.Time
	TOTPVerifiedAt  time.Time
	Revoked         bool
}

type LoginSessionAttempt struct {
	ExpectedCredentialEpoch int64
	TokenHash               []byte
	CreatedAt               time.Time
	ExpiresAt               time.Time
	TOTPStep                time.Time
}

type ConfirmTOTPAttempt struct {
	ExpectedCredentialEpoch int64
	TokenHash               []byte
	Now                     time.Time
	TOTPStep                time.Time
}

type RecoveryReplacement struct {
	SessionTokenHash []byte
	Now              time.Time
	NewCodeHashes    [][]byte
}

type RecoveryCompletion struct {
	ExpectedCredentialEpoch int64
	UIDLookup               []byte
	ConsumedCodeHash        []byte
	NewTOTPSecretCiphertext []byte
	NewCodeHashes           [][]byte
	Now                     time.Time
}

type Repository interface {
	Initialize(context.Context, InitializationRecord) error
	Identity(context.Context) (IdentityRecord, error)
	CreateLoginSession(context.Context, LoginSessionAttempt) error
	FindSession(context.Context, []byte, time.Time) (AdminSession, error)
	ConfirmTOTP(context.Context, ConfirmTOTPAttempt) error
	ReplaceRecoveryCodes(context.Context, RecoveryReplacement) ([]byte, error)
	CompleteRecovery(context.Context, RecoveryCompletion) ([]byte, error)
}

type SQLRepository struct {
	db *sql.DB
}

func NewRepository(database *sql.DB) *SQLRepository {
	return &SQLRepository{db: database}
}

func OpenRepository(ctx context.Context, dsn string) (*SQLRepository, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrInvalidInput
	}
	normalized, err := normalizeRepositoryDSN(dsn)
	if err != nil {
		return nil, ErrInvalidInput
	}
	database, err := sql.Open("mysql", normalized)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, ErrUnavailable
	}
	return NewRepository(database), nil
}

func (repository *SQLRepository) Close() error {
	if repository == nil || repository.db == nil {
		return nil
	}
	return repository.db.Close()
}

func normalizeRepositoryDSN(dsn string) (string, error) {
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	config.ParseTime = true
	config.Loc = time.UTC
	if config.Params == nil {
		config.Params = make(map[string]string)
	}
	config.Params["time_zone"] = "'+00:00'"
	return config.FormatDSN(), nil
}

func (repository *SQLRepository) Initialize(ctx context.Context, record InitializationRecord) error {
	if !repository.ready() || !validInitialization(record) {
		return ErrInvalidInput
	}
	record = cloneInitialization(record)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	_, err = transaction.ExecContext(ctx,
		"INSERT INTO admin_identity (id, credential_epoch, uid_ciphertext, uid_lookup, email_ciphertext, created_at, updated_at) VALUES (?, 1, ?, ?, ?, ?, ?)",
		int64(1), record.Identity.UIDCiphertext, record.Identity.UIDLookup, record.Identity.EmailCiphertext, record.CreatedAt, record.CreatedAt,
	)
	if err != nil {
		if repositoryDuplicate(err) {
			return ErrAlreadyInitialized
		}
		return ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO admin_totp (admin_identity_id, secret_ciphertext, rotated_at) VALUES (?, ?, ?)",
		int64(1), record.Identity.TOTPSecretCiphertext, record.CreatedAt,
	); err != nil {
		return ErrUnavailable
	}
	for _, hash := range record.RecoveryCodeHashes {
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO admin_recovery_codes (admin_identity_id, code_hash, created_at) VALUES (?, ?, ?)",
			int64(1), hash, record.CreatedAt,
		); err != nil {
			return ErrUnavailable
		}
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (repository *SQLRepository) Identity(ctx context.Context) (IdentityRecord, error) {
	if !repository.ready() {
		return IdentityRecord{}, ErrInvalidInput
	}
	const query = "SELECT a.credential_epoch, a.uid_ciphertext, a.uid_lookup, a.email_ciphertext, t.secret_ciphertext FROM admin_identity AS a JOIN admin_totp AS t ON t.admin_identity_id = a.id WHERE a.id = 1 LIMIT 1"
	var record IdentityRecord
	err := repository.db.QueryRowContext(ctx, query).Scan(
		&record.CredentialEpoch, &record.UIDCiphertext, &record.UIDLookup, &record.EmailCiphertext, &record.TOTPSecretCiphertext,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return IdentityRecord{}, ErrAuthenticationFailed
	}
	if err != nil || !validIdentityRecord(record) {
		return IdentityRecord{}, ErrUnavailable
	}
	return cloneIdentityRecord(record), nil
}

func (repository *SQLRepository) CreateLoginSession(ctx context.Context, attempt LoginSessionAttempt) error {
	if !repository.ready() || !validLoginAttempt(attempt) {
		return ErrInvalidInput
	}
	attempt.TokenHash = bytes.Clone(attempt.TokenHash)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	epoch, err := lockAdminEpoch(ctx, transaction)
	if err != nil {
		return err
	}
	if epoch != attempt.ExpectedCredentialEpoch {
		return ErrAuthenticationFailed
	}
	lastStep, err := globalTOTPStep(ctx, transaction, epoch)
	if err != nil {
		return err
	}
	if lastStep.Valid && !attempt.TOTPStep.After(lastStep.Time) {
		return ErrAuthenticationFailed
	}
	result, err := transaction.ExecContext(ctx,
		"INSERT INTO site_sessions (admin_identity_id, token_hash, credential_epoch, created_at, expires_at, totp_verified_at) VALUES (?, ?, ?, ?, ?, ?)",
		int64(1), attempt.TokenHash, attempt.ExpectedCredentialEpoch, attempt.CreatedAt, attempt.ExpiresAt, attempt.TOTPStep,
	)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (repository *SQLRepository) FindSession(ctx context.Context, tokenHash []byte, now time.Time) (AdminSession, error) {
	if !repository.ready() || len(tokenHash) != sha256.Size || now.IsZero() {
		return AdminSession{}, ErrInvalidInput
	}
	tokenHash = bytes.Clone(tokenHash)
	const query = "SELECT s.id, s.credential_epoch, s.expires_at, s.totp_verified_at, s.revoked_at, a.credential_epoch FROM site_sessions AS s JOIN admin_identity AS a ON a.id = s.admin_identity_id WHERE s.admin_identity_id = 1 AND s.token_hash = ? LIMIT 1"
	var session AdminSession
	var verifiedAt, revokedAt sql.NullTime
	var currentEpoch int64
	err := repository.db.QueryRowContext(ctx, query, tokenHash).Scan(&session.ID, &session.CredentialEpoch, &session.ExpiresAt, &verifiedAt, &revokedAt, &currentEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminSession{}, ErrAuthenticationFailed
	}
	if err != nil {
		return AdminSession{}, ErrUnavailable
	}
	if session.ID <= 0 || session.CredentialEpoch < 1 || session.CredentialEpoch != currentEpoch || !session.ExpiresAt.After(now) || revokedAt.Valid || !verifiedAt.Valid {
		return AdminSession{}, ErrAuthenticationFailed
	}
	session.TOTPVerifiedAt = verifiedAt.Time
	return session, nil
}

func (repository *SQLRepository) ConfirmTOTP(ctx context.Context, attempt ConfirmTOTPAttempt) error {
	if !repository.ready() || len(attempt.TokenHash) != sha256.Size || attempt.ExpectedCredentialEpoch < 1 || attempt.Now.IsZero() || attempt.TOTPStep.IsZero() {
		return ErrInvalidInput
	}
	attempt.TokenHash = bytes.Clone(attempt.TokenHash)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	epoch, err := lockAdminEpoch(ctx, transaction)
	if err != nil || epoch != attempt.ExpectedCredentialEpoch {
		return ErrAuthenticationFailed
	}
	const sessionQuery = "SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE"
	var sessionID, sessionEpoch int64
	var expiresAt time.Time
	var revokedAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, sessionQuery, attempt.TokenHash).Scan(&sessionID, &sessionEpoch, &expiresAt, &revokedAt); err != nil || sessionID <= 0 || sessionEpoch != epoch || !expiresAt.After(attempt.Now) || revokedAt.Valid {
		return ErrAuthenticationFailed
	}
	lastStep, err := globalTOTPStep(ctx, transaction, epoch)
	if err != nil {
		return err
	}
	if lastStep.Valid && !attempt.TOTPStep.After(lastStep.Time) {
		return ErrAuthenticationFailed
	}
	result, err := transaction.ExecContext(ctx,
		"UPDATE site_sessions SET totp_verified_at = ? WHERE id = ? AND revoked_at IS NULL AND credential_epoch = ?",
		attempt.TOTPStep, sessionID, epoch,
	)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (repository *SQLRepository) ReplaceRecoveryCodes(ctx context.Context, attempt RecoveryReplacement) ([]byte, error) {
	if !repository.ready() || len(attempt.SessionTokenHash) != sha256.Size || attempt.Now.IsZero() || !validCodeHashes(attempt.NewCodeHashes) {
		return nil, ErrInvalidInput
	}
	attempt.SessionTokenHash = bytes.Clone(attempt.SessionTokenHash)
	attempt.NewCodeHashes = cloneByteSlices(attempt.NewCodeHashes)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	const identityQuery = "SELECT credential_epoch, email_ciphertext FROM admin_identity WHERE id = 1 FOR UPDATE"
	var epoch int64
	var emailCiphertext []byte
	if err := transaction.QueryRowContext(ctx, identityQuery).Scan(&epoch, &emailCiphertext); err != nil || epoch < 1 || len(emailCiphertext) == 0 {
		return nil, ErrAuthenticationFailed
	}
	const sessionQuery = "SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE"
	var sessionID, sessionEpoch int64
	var expiresAt time.Time
	var revokedAt, verifiedAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, sessionQuery, attempt.SessionTokenHash).Scan(&sessionID, &sessionEpoch, &expiresAt, &revokedAt, &verifiedAt); err != nil || sessionID <= 0 || sessionEpoch != epoch || !expiresAt.After(attempt.Now) || revokedAt.Valid {
		return nil, ErrAuthenticationFailed
	}
	if !verifiedAt.Valid || verifiedAt.Time.After(attempt.Now.Add(30*time.Second)) || attempt.Now.Sub(verifiedAt.Time) > RecentTOTPWindow {
		return nil, ErrRecentTOTPRequired
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE admin_recovery_codes SET invalidated_at = ? WHERE admin_identity_id = ? AND used_at IS NULL AND invalidated_at IS NULL",
		attempt.Now, int64(1),
	); err != nil {
		return nil, ErrUnavailable
	}
	if err := insertRecoveryCodes(ctx, transaction, attempt.NewCodeHashes, attempt.Now); err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, ErrUnavailable
	}
	committed = true
	return bytes.Clone(emailCiphertext), nil
}

func (repository *SQLRepository) CompleteRecovery(ctx context.Context, attempt RecoveryCompletion) ([]byte, error) {
	if !repository.ready() || attempt.ExpectedCredentialEpoch < 1 || len(attempt.UIDLookup) != sha256.Size || len(attempt.ConsumedCodeHash) != sha256.Size || len(attempt.NewTOTPSecretCiphertext) == 0 || len(attempt.NewTOTPSecretCiphertext) > 512 || !validCodeHashes(attempt.NewCodeHashes) || attempt.Now.IsZero() {
		return nil, ErrInvalidInput
	}
	attempt.UIDLookup = bytes.Clone(attempt.UIDLookup)
	attempt.ConsumedCodeHash = bytes.Clone(attempt.ConsumedCodeHash)
	attempt.NewTOTPSecretCiphertext = bytes.Clone(attempt.NewTOTPSecretCiphertext)
	attempt.NewCodeHashes = cloneByteSlices(attempt.NewCodeHashes)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	const identityQuery = "SELECT credential_epoch, email_ciphertext FROM admin_identity WHERE id = 1 AND uid_lookup = ? FOR UPDATE"
	var epoch int64
	var emailCiphertext []byte
	if err := transaction.QueryRowContext(ctx, identityQuery, attempt.UIDLookup).Scan(&epoch, &emailCiphertext); err != nil || epoch != attempt.ExpectedCredentialEpoch || len(emailCiphertext) == 0 {
		return nil, ErrAuthenticationFailed
	}
	result, err := transaction.ExecContext(ctx,
		"UPDATE admin_recovery_codes SET used_at = ? WHERE admin_identity_id = ? AND code_hash = ? AND used_at IS NULL AND invalidated_at IS NULL",
		attempt.Now, int64(1), attempt.ConsumedCodeHash,
	)
	if err != nil || !oneRow(result) {
		return nil, ErrAuthenticationFailed
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE admin_recovery_codes SET invalidated_at = ? WHERE admin_identity_id = ? AND used_at IS NULL AND invalidated_at IS NULL",
		attempt.Now, int64(1),
	); err != nil {
		return nil, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx,
		"UPDATE admin_totp SET secret_ciphertext = ?, rotated_at = ? WHERE admin_identity_id = ?",
		attempt.NewTOTPSecretCiphertext, attempt.Now, int64(1),
	)
	if err != nil || !oneRow(result) {
		return nil, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx,
		"UPDATE admin_identity SET credential_epoch = credential_epoch + 1, updated_at = ? WHERE id = ? AND credential_epoch = ?",
		attempt.Now, int64(1), attempt.ExpectedCredentialEpoch,
	)
	if err != nil || !oneRow(result) {
		return nil, ErrAuthenticationFailed
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE admin_identity_id = ?",
		attempt.Now, int64(1),
	); err != nil {
		return nil, ErrUnavailable
	}
	if err := insertRecoveryCodes(ctx, transaction, attempt.NewCodeHashes, attempt.Now); err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, ErrUnavailable
	}
	committed = true
	return bytes.Clone(emailCiphertext), nil
}

func (repository *SQLRepository) ready() bool {
	return repository != nil && repository.db != nil
}

func lockAdminEpoch(ctx context.Context, transaction *sql.Tx) (int64, error) {
	var epoch int64
	err := transaction.QueryRowContext(ctx, "SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrAuthenticationFailed
	}
	if err != nil || epoch < 1 {
		return 0, ErrUnavailable
	}
	return epoch, nil
}

func globalTOTPStep(ctx context.Context, transaction *sql.Tx, credentialEpoch int64) (sql.NullTime, error) {
	var step sql.NullTime
	if err := transaction.QueryRowContext(ctx, "SELECT MAX(totp_verified_at) FROM site_sessions WHERE admin_identity_id = 1 AND credential_epoch = ?", credentialEpoch).Scan(&step); err != nil {
		return sql.NullTime{}, ErrUnavailable
	}
	return step, nil
}

func insertRecoveryCodes(ctx context.Context, transaction *sql.Tx, hashes [][]byte, createdAt time.Time) error {
	for _, hash := range hashes {
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO admin_recovery_codes (admin_identity_id, code_hash, created_at) VALUES (?, ?, ?)",
			int64(1), hash, createdAt,
		); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func oneRow(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

func repositoryDuplicate(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func validInitialization(record InitializationRecord) bool {
	return record.CreatedAt.IsZero() == false && record.Identity.CredentialEpoch == 1 && validIdentityRecord(record.Identity) && validCodeHashes(record.RecoveryCodeHashes)
}

func validIdentityRecord(record IdentityRecord) bool {
	return record.CredentialEpoch >= 1 && len(record.UIDCiphertext) > 0 && len(record.UIDCiphertext) <= 512 && len(record.UIDLookup) == sha256.Size && len(record.EmailCiphertext) > 0 && len(record.EmailCiphertext) <= 1024 && len(record.TOTPSecretCiphertext) > 0 && len(record.TOTPSecretCiphertext) <= 512
}

func validLoginAttempt(attempt LoginSessionAttempt) bool {
	return attempt.ExpectedCredentialEpoch >= 1 && len(attempt.TokenHash) == sha256.Size && !attempt.CreatedAt.IsZero() && attempt.ExpiresAt.After(attempt.CreatedAt) && !attempt.TOTPStep.IsZero()
}

func validCodeHashes(hashes [][]byte) bool {
	if len(hashes) != RecoveryCodeCount {
		return false
	}
	seen := make(map[[sha256.Size]byte]struct{}, len(hashes))
	for _, hash := range hashes {
		var key [sha256.Size]byte
		if len(hash) != len(key) {
			return false
		}
		copy(key[:], hash)
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func cloneInitialization(record InitializationRecord) InitializationRecord {
	record.Identity = cloneIdentityRecord(record.Identity)
	record.RecoveryCodeHashes = cloneByteSlices(record.RecoveryCodeHashes)
	return record
}

func cloneIdentityRecord(record IdentityRecord) IdentityRecord {
	record.UIDCiphertext = bytes.Clone(record.UIDCiphertext)
	record.UIDLookup = bytes.Clone(record.UIDLookup)
	record.EmailCiphertext = bytes.Clone(record.EmailCiphertext)
	record.TOTPSecretCiphertext = bytes.Clone(record.TOTPSecretCiphertext)
	return record
}

func cloneByteSlices(values [][]byte) [][]byte {
	result := make([][]byte, len(values))
	for index, value := range values {
		result[index] = bytes.Clone(value)
	}
	return result
}

type TOTPProvider interface {
	Generate(issuer, account string) (secret, uri string, err error)
	Validate(code, secret string, now time.Time) (acceptedStep time.Time, ok bool)
}

type ServiceOptions struct {
	Now        func() time.Time
	Random     io.Reader
	TOTP       TOTPProvider
	Issuer     string
	ProofTTL   time.Duration
	SessionTTL time.Duration
}

type Service struct {
	repository Repository
	keys       security.Keyring
	verifier   identity.BiliVerifier
	sender     MailSender
	now        func() time.Time
	random     io.Reader
	totp       TOTPProvider
	issuer     string
	proofTTL   time.Duration
	sessionTTL time.Duration
}

type InitializeResult struct {
	TOTPURI          string `json:"totpUri"`
	RecoveryPassword string `json:"recoveryPassword"`
}

type LoginResult struct {
	Token     string    `json:"-"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type RecoveryResult struct {
	RecoveryPassword string `json:"recoveryPassword"`
}

type RecoveryCompletionResult struct {
	TOTPURI          string `json:"totpUri"`
	RecoveryPassword string `json:"recoveryPassword"`
}

func NewService(repository Repository, keys security.Keyring, verifier identity.BiliVerifier, sender MailSender, options ServiceOptions) (*Service, error) {
	if repository == nil || verifier == nil || sender == nil {
		return nil, ErrInvalidInput
	}
	if _, err := keys.HashToken("admin_session", []byte("constructor-check")); err != nil {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.TOTP == nil {
		options.TOTP = standardTOTP{}
	}
	if options.Issuer == "" {
		options.Issuer = "Gift Panel Hosted"
	}
	if options.ProofTTL == 0 {
		options.ProofTTL = defaultProofTTL
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = defaultSessionTTL
	}
	if options.ProofTTL <= 0 || options.ProofTTL > defaultProofTTL || options.SessionTTL <= 0 || options.SessionTTL > 7*24*time.Hour || len(options.Issuer) > 128 {
		return nil, ErrInvalidInput
	}
	return &Service{
		repository: repository, keys: keys, verifier: verifier, sender: sender,
		now: options.Now, random: options.Random, totp: options.TOTP, issuer: options.Issuer,
		proofTTL: options.ProofTTL, sessionTTL: options.SessionTTL,
	}, nil
}

func (service *Service) BeginVerification(ctx context.Context) (identity.Challenge, error) {
	if service == nil {
		return identity.Challenge{}, ErrUnavailable
	}
	challenge, err := service.verifier.Begin(ctx)
	if err != nil || challenge.ID == "" || challenge.QRImage == "" || !challenge.ExpiresAt.After(service.now()) {
		if challenge.ID != "" {
			service.verifier.Forget(challenge.ID)
		}
		return identity.Challenge{}, ErrUnavailable
	}
	return challenge, nil
}

func (service *Service) CancelVerification(challengeID string) {
	if service != nil && challengeID != "" {
		service.verifier.Forget(challengeID)
	}
}

func (service *Service) Initialize(ctx context.Context, uid, email string) (InitializeResult, error) {
	canonical, ok := canonicalAdminUID(uid)
	if service == nil || !ok || !validEmail(email) {
		return InitializeResult{}, ErrInvalidInput
	}
	secret, uri, err := service.totp.Generate(service.issuer, email)
	if err != nil || secret == "" || uri == "" {
		return InitializeResult{}, ErrUnavailable
	}
	material, err := buildRecoveryPackage(service.random)
	if err != nil {
		return InitializeResult{}, err
	}
	hashes, err := recoveryCodeHashes(material.Codes)
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	uidCiphertext, err := service.keys.Seal("admin_uid", []byte(canonical))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	uidLookup, err := service.keys.Lookup("bili_uid", []byte(canonical))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	emailCiphertext, err := service.keys.Seal("admin_email", []byte(email))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	secretCiphertext, err := service.keys.Seal("admin_totp", []byte(secret))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	if err := service.sendArchive(ctx, email, material.Archive); err != nil {
		return InitializeResult{}, err
	}
	err = service.repository.Initialize(ctx, InitializationRecord{
		Identity:           IdentityRecord{CredentialEpoch: 1, UIDCiphertext: uidCiphertext, UIDLookup: uidLookup, EmailCiphertext: emailCiphertext, TOTPSecretCiphertext: secretCiphertext},
		RecoveryCodeHashes: hashes, CreatedAt: service.now(),
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyInitialized) {
			return InitializeResult{}, ErrAlreadyInitialized
		}
		return InitializeResult{}, ErrUnavailable
	}
	return InitializeResult{TOTPURI: uri, RecoveryPassword: material.Password}, nil
}

func (service *Service) VerifyLogin(ctx context.Context, challengeID, code string) (LoginResult, error) {
	if service == nil || challengeID == "" || !validTOTPCode(code) {
		return LoginResult{}, ErrAuthenticationFailed
	}
	record, step, err := service.verifyProofAndTOTP(ctx, challengeID, code)
	if err != nil {
		return LoginResult{}, err
	}
	token, err := service.keys.NewToken()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(token))
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	now := service.now()
	expiresAt := now.Add(service.sessionTTL)
	if err := service.repository.CreateLoginSession(ctx, LoginSessionAttempt{
		ExpectedCredentialEpoch: record.CredentialEpoch, TokenHash: tokenHash,
		CreatedAt: now, ExpiresAt: expiresAt, TOTPStep: step,
	}); err != nil {
		return LoginResult{}, ErrAuthenticationFailed
	}
	return LoginResult{Token: token, ExpiresAt: expiresAt}, nil
}

func (service *Service) VerifyRecentTOTP(ctx context.Context, sessionToken, code string) error {
	if service == nil || sessionToken == "" || !validTOTPCode(code) {
		return ErrAuthenticationFailed
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return ErrAuthenticationFailed
	}
	now := service.now()
	session, err := service.repository.FindSession(ctx, tokenHash, now)
	if err != nil {
		return ErrAuthenticationFailed
	}
	record, err := service.repository.Identity(ctx)
	if err != nil || record.CredentialEpoch != session.CredentialEpoch {
		return ErrAuthenticationFailed
	}
	secret, err := service.keys.Open("admin_totp", record.TOTPSecretCiphertext)
	if err != nil {
		return ErrAuthenticationFailed
	}
	defer clear(secret)
	step, valid := service.totp.Validate(code, string(secret), now)
	if !valid || step.IsZero() {
		return ErrAuthenticationFailed
	}
	if err := service.repository.ConfirmTOTP(ctx, ConfirmTOTPAttempt{
		ExpectedCredentialEpoch: record.CredentialEpoch, TokenHash: tokenHash, Now: now, TOTPStep: step,
	}); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

func (service *Service) RequireRecentTOTP(ctx context.Context, sessionToken string) error {
	if service == nil || sessionToken == "" {
		return ErrAuthenticationFailed
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return ErrAuthenticationFailed
	}
	now := service.now()
	session, err := service.repository.FindSession(ctx, tokenHash, now)
	if err != nil {
		return ErrAuthenticationFailed
	}
	if session.TOTPVerifiedAt.IsZero() || session.TOTPVerifiedAt.After(now.Add(30*time.Second)) || now.Sub(session.TOTPVerifiedAt) > RecentTOTPWindow {
		return ErrRecentTOTPRequired
	}
	return nil
}

func (service *Service) SendRecovery(ctx context.Context, sessionToken string) (RecoveryResult, error) {
	if err := service.RequireRecentTOTP(ctx, sessionToken); err != nil {
		return RecoveryResult{}, err
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return RecoveryResult{}, ErrAuthenticationFailed
	}
	material, err := buildRecoveryPackage(service.random)
	if err != nil {
		return RecoveryResult{}, err
	}
	hashes, err := recoveryCodeHashes(material.Codes)
	if err != nil {
		return RecoveryResult{}, ErrUnavailable
	}
	record, err := service.repository.Identity(ctx)
	if err != nil {
		return RecoveryResult{}, ErrAuthenticationFailed
	}
	email, err := service.keys.Open("admin_email", record.EmailCiphertext)
	if err != nil || !validEmail(string(email)) {
		clear(email)
		return RecoveryResult{}, ErrUnavailable
	}
	defer clear(email)
	if err := service.sendArchive(ctx, string(email), material.Archive); err != nil {
		return RecoveryResult{}, err
	}
	storedEmail, err := service.repository.ReplaceRecoveryCodes(ctx, RecoveryReplacement{SessionTokenHash: tokenHash, Now: service.now(), NewCodeHashes: hashes})
	if err != nil {
		if errors.Is(err, ErrRecentTOTPRequired) {
			return RecoveryResult{}, ErrRecentTOTPRequired
		}
		return RecoveryResult{}, ErrAuthenticationFailed
	}
	if subtle.ConstantTimeCompare(storedEmail, record.EmailCiphertext) != 1 {
		return RecoveryResult{}, ErrUnavailable
	}
	return RecoveryResult{RecoveryPassword: material.Password}, nil
}

func (service *Service) CompleteRecovery(ctx context.Context, challengeID, recoveryCode string) (RecoveryCompletionResult, error) {
	if service == nil || challengeID == "" || recoveryCode == "" || len(recoveryCode) > 256 {
		return RecoveryCompletionResult{}, ErrAuthenticationFailed
	}
	verification, err := service.consumeProof(ctx, challengeID)
	if err != nil {
		return RecoveryCompletionResult{}, err
	}
	uid, ok := canonicalAdminUID(verification.UID)
	if !ok {
		return RecoveryCompletionResult{}, ErrAuthenticationFailed
	}
	uidLookup, err := service.keys.Lookup("bili_uid", []byte(uid))
	if err != nil {
		return RecoveryCompletionResult{}, ErrAuthenticationFailed
	}
	record, err := service.repository.Identity(ctx)
	if err != nil || subtle.ConstantTimeCompare(uidLookup, record.UIDLookup) != 1 {
		return RecoveryCompletionResult{}, ErrAuthenticationFailed
	}
	secret, uri, err := service.totp.Generate(service.issuer, "administrator")
	if err != nil || secret == "" || uri == "" {
		return RecoveryCompletionResult{}, ErrUnavailable
	}
	secretCiphertext, err := service.keys.Seal("admin_totp", []byte(secret))
	if err != nil {
		return RecoveryCompletionResult{}, ErrUnavailable
	}
	material, err := buildRecoveryPackage(service.random)
	if err != nil {
		return RecoveryCompletionResult{}, err
	}
	hashes, err := recoveryCodeHashes(material.Codes)
	if err != nil {
		return RecoveryCompletionResult{}, ErrUnavailable
	}
	email, err := service.keys.Open("admin_email", record.EmailCiphertext)
	if err != nil || !validEmail(string(email)) {
		clear(email)
		return RecoveryCompletionResult{}, ErrUnavailable
	}
	defer clear(email)
	if err := service.sendArchive(ctx, string(email), material.Archive); err != nil {
		return RecoveryCompletionResult{}, err
	}
	consumedHash := sha256.Sum256([]byte(recoveryCode))
	storedEmail, err := service.repository.CompleteRecovery(ctx, RecoveryCompletion{
		ExpectedCredentialEpoch: record.CredentialEpoch, UIDLookup: uidLookup,
		ConsumedCodeHash: consumedHash[:], NewTOTPSecretCiphertext: secretCiphertext,
		NewCodeHashes: hashes, Now: service.now(),
	})
	if err != nil {
		return RecoveryCompletionResult{}, ErrAuthenticationFailed
	}
	if subtle.ConstantTimeCompare(storedEmail, record.EmailCiphertext) != 1 {
		return RecoveryCompletionResult{}, ErrUnavailable
	}
	return RecoveryCompletionResult{TOTPURI: uri, RecoveryPassword: material.Password}, nil
}

func (service *Service) verifyProofAndTOTP(ctx context.Context, challengeID, code string) (IdentityRecord, time.Time, error) {
	verification, err := service.consumeProof(ctx, challengeID)
	if err != nil {
		return IdentityRecord{}, time.Time{}, err
	}
	uid, ok := canonicalAdminUID(verification.UID)
	if !ok {
		return IdentityRecord{}, time.Time{}, ErrAuthenticationFailed
	}
	lookup, err := service.keys.Lookup("bili_uid", []byte(uid))
	if err != nil {
		return IdentityRecord{}, time.Time{}, ErrAuthenticationFailed
	}
	record, err := service.repository.Identity(ctx)
	if err != nil || subtle.ConstantTimeCompare(lookup, record.UIDLookup) != 1 || record.CredentialEpoch < 1 {
		return IdentityRecord{}, time.Time{}, ErrAuthenticationFailed
	}
	secret, err := service.keys.Open("admin_totp", record.TOTPSecretCiphertext)
	if err != nil {
		return IdentityRecord{}, time.Time{}, ErrAuthenticationFailed
	}
	defer clear(secret)
	step, valid := service.totp.Validate(code, string(secret), service.now())
	if !valid || step.IsZero() {
		return IdentityRecord{}, time.Time{}, ErrAuthenticationFailed
	}
	return record, step, nil
}

func (service *Service) consumeProof(ctx context.Context, challengeID string) (identity.Verification, error) {
	verification, err := service.verifier.Poll(ctx, challengeID)
	if errors.Is(err, identity.ErrVerificationPending) || errors.Is(err, identity.ErrVerificationUnavailable) {
		return identity.Verification{}, err
	}
	service.verifier.Forget(challengeID)
	if err != nil {
		return identity.Verification{}, ErrAuthenticationFailed
	}
	now := service.now()
	if verification.CompletedAt.IsZero() || verification.CompletedAt.After(now.Add(time.Minute)) || verification.CompletedAt.Before(now.Add(-service.proofTTL)) {
		return identity.Verification{}, ErrAuthenticationFailed
	}
	return verification, nil
}

func (service *Service) sendArchive(ctx context.Context, email string, archive []byte) error {
	message := Message{
		To: email, Subject: "Gift Panel administrator recovery archive",
		Text:        "The encrypted administrator recovery archive is attached. Its decryption password is shown separately and is never sent by email.",
		Attachments: []Attachment{{Filename: "gift-panel-admin-recovery.bin", ContentType: "application/octet-stream", Data: bytes.Clone(archive)}},
	}
	if err := service.sender.Send(ctx, message); err != nil {
		return ErrUnavailable
	}
	return nil
}

type standardTOTP struct{}

func (standardTOTP) Generate(issuer, account string) (string, string, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account, Period: 30, SecretSize: 20, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func (standardTOTP) Validate(code, secret string, now time.Time) (time.Time, bool) {
	options := totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	for _, offset := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		candidate := now.Add(offset)
		valid, err := totp.ValidateCustom(code, secret, candidate, options)
		if err == nil && valid {
			return candidate.Truncate(30 * time.Second), true
		}
	}
	return time.Time{}, false
}

func canonicalAdminUID(value string) (string, bool) {
	if value == "" || len(value) > 20 {
		return "", false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return "", false
	}
	canonical := strconv.FormatUint(parsed, 10)
	return canonical, canonical == value
}

func validEmail(value string) bool {
	if value == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

func validTOTPCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, character := range code {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
