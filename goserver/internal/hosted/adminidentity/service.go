package adminidentity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/go-sql-driver/mysql"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	RecentTOTPWindow                     = 10 * time.Minute
	defaultEmailChallengeTTL             = 5 * time.Minute
	DefaultAdministratorSessionTTL       = 30 * 24 * time.Hour
	emailCodeLength                      = 6
	emailCodeAttempts                    = 5
	emailLoginSessionVerificationTimeout = 2 * time.Second
	operationAuthorizationTTL            = 5 * time.Minute
)

var (
	ErrInvalidInput          = errors.New("admin identity: invalid input")
	ErrAlreadyInitialized    = errors.New("admin identity: already initialized")
	ErrAuthenticationFailed  = security.ErrSensitiveAuthenticationFailed
	ErrRecentTOTPRequired    = security.ErrSensitiveRecentTOTPRequired
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

type EmailLoginSessionAttempt struct {
	ExpectedCredentialEpoch int64
	TokenHash               []byte
	CreatedAt               time.Time
	ExpiresAt               time.Time
}

type ConfirmTOTPAttempt struct {
	ExpectedCredentialEpoch int64
	TokenHash               []byte
	Now                     time.Time
	TOTPStep                time.Time
}

type OperationAuthorization struct {
	Purpose    security.OperationPurpose
	Target     string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

type OperationAuthorizationAttempt struct {
	SessionTokenHash        []byte
	AuthorizationTokenHash  []byte
	ExpectedCredentialEpoch int64
	Purpose                 security.OperationPurpose
	Target                  string
	CreatedAt               time.Time
	ExpiresAt               time.Time
	TOTPStep                time.Time
}

const (
	HandoffInitialization     = "initialization"
	HandoffRecovery           = "recovery"
	HandoffPending            = "pending"
	HandoffConfirmed          = "confirmed"
	defaultHandoffTTL         = 30 * time.Minute
	defaultCleanupLimit       = 100
	handoffMailLockPrefix     = "gift_panel_admin_handoff_mail_"
	handoffMailLockWait       = 5
	handoffMailAcquireTimeout = 6 * time.Second
	handoffMailDBTimeout      = 5 * time.Second
)

type HandoffRecord struct {
	Kind                 string
	RequestHash          []byte
	TokenHash            []byte
	TokenCiphertext      []byte
	UIDCiphertext        []byte
	UIDLookup            []byte
	EmailCiphertext      []byte
	TOTPSecretCiphertext []byte
	TOTPURICiphertext    []byte
	PasswordCiphertext   []byte
	Archive              []byte
	RecoveryCodeHashes   [][]byte
	CreatedAt            time.Time
	ExpiresAt            time.Time
}

type PendingHandoff struct {
	ID                   int64
	Kind                 string
	State                string
	RequestHash          []byte
	TokenHash            []byte
	TokenCiphertext      []byte
	UIDCiphertext        []byte
	UIDLookup            []byte
	EmailCiphertext      []byte
	TOTPSecretCiphertext []byte
	TOTPURICiphertext    []byte
	PasswordCiphertext   []byte
	Archive              []byte
	RecoveryCodeHashes   [][]byte
	CreatedAt            time.Time
	ExpiresAt            time.Time
	MailDelivered        bool
}

type ActivateInitializationAttempt struct {
	TokenHash []byte
	Now       time.Time
	TOTPStep  time.Time
}

type HandoffMailClaim interface {
	MailDelivered() bool
	MarkAttempt(time.Time, bool) error
	Release() error
}

type HandoffRepository interface {
	PrepareInitialization(context.Context, HandoffRecord) (PendingHandoff, error)
	ActivateInitialization(context.Context, ActivateInitializationAttempt) error
	PrepareRecoveryHandoff(context.Context, int64, []byte, HandoffRecord) (PendingHandoff, error)
	HandoffByToken(context.Context, []byte) (PendingHandoff, error)
	ConfirmRecoveryHandoff(context.Context, []byte, time.Time) error
	AcquireHandoffMailClaim(context.Context, int64) (HandoffMailClaim, error)
	CleanupExpiredHandoffs(context.Context, time.Time, int) error
}

type Repository interface {
	Initialize(context.Context, InitializationRecord) error
	Identity(context.Context) (IdentityRecord, error)
	CreateEmailLoginSession(context.Context, EmailLoginSessionAttempt) error
	FindSession(context.Context, []byte, time.Time) (AdminSession, error)
	RevokeSession(context.Context, []byte, time.Time) error
	ConfirmTOTP(context.Context, ConfirmTOTPAttempt) error
}

type sensitiveSessionRepository interface {
	authorizeRecentTOTP(context.Context, *sql.Tx, []byte, time.Time) (security.SensitiveSession, error)
	renewRecentTOTP(context.Context, *sql.Tx, security.SensitiveSession, time.Time) error
}

type operationAuthorizationRepository interface {
	CreateOperationAuthorization(context.Context, OperationAuthorizationAttempt) error
	ConsumeOperationAuthorization(context.Context, *sql.Tx, []byte, []byte, security.OperationPurpose, string, time.Time) error
}

type recoveryRotationRepository interface {
	RotateRecoveryCodes(context.Context, security.SensitiveAuthorizer, string, [][]byte, func() time.Time) ([]byte, error)
}

type SQLRepository struct {
	db                *sql.DB
	sensitiveSessions *security.SensitiveSessionIssuer
}

func NewRepository(database *sql.DB) *SQLRepository {
	return &SQLRepository{db: database, sensitiveSessions: security.NewSensitiveSessionIssuer()}
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

func (repository *SQLRepository) CreateEmailLoginSession(ctx context.Context, attempt EmailLoginSessionAttempt) error {
	if !repository.ready() || !validEmailLoginSessionAttempt(attempt) {
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
	result, err := transaction.ExecContext(ctx,
		"INSERT INTO site_sessions (admin_identity_id, token_hash, credential_epoch, created_at, expires_at, totp_verified_at) VALUES (?, ?, ?, ?, ?, NULL)",
		int64(1), attempt.TokenHash, attempt.ExpectedCredentialEpoch, attempt.CreatedAt, attempt.ExpiresAt,
	)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		committed = true
		return repository.verifyEmailLoginSession(ctx, attempt)
	}
	committed = true
	return nil
}

func (repository *SQLRepository) verifyEmailLoginSession(_ context.Context, attempt EmailLoginSessionAttempt) error {
	const query = "SELECT 1 FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? AND credential_epoch = ? AND created_at = ? AND expires_at = ? AND totp_verified_at IS NULL LIMIT 1"
	verificationContext, cancel := context.WithTimeout(context.Background(), emailLoginSessionVerificationTimeout)
	defer cancel()
	var present int
	err := repository.db.QueryRowContext(verificationContext, query, attempt.TokenHash, attempt.ExpectedCredentialEpoch, attempt.CreatedAt, attempt.ExpiresAt).Scan(&present)
	if err != nil || present != 1 {
		return ErrUnavailable
	}
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
	if session.ID <= 0 || session.CredentialEpoch < 1 || session.CredentialEpoch != currentEpoch || !session.ExpiresAt.After(now) || revokedAt.Valid {
		return AdminSession{}, ErrAuthenticationFailed
	}
	if verifiedAt.Valid {
		session.TOTPVerifiedAt = verifiedAt.Time
	}
	return session, nil
}

func (repository *SQLRepository) RevokeSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	if !repository.ready() || len(tokenHash) != sha256.Size || now.IsZero() {
		return ErrInvalidInput
	}
	_, err := repository.db.ExecContext(ctx,
		"UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE admin_identity_id = 1 AND token_hash = ?",
		now, bytes.Clone(tokenHash),
	)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (repository *SQLRepository) ConfirmTOTP(ctx context.Context, attempt ConfirmTOTPAttempt) error {
	if !repository.ready() || len(attempt.TokenHash) != sha256.Size || attempt.ExpectedCredentialEpoch < 1 || attempt.Now.IsZero() || attempt.TOTPStep.IsZero() || attempt.TOTPStep.After(attempt.Now) {
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
		attempt.Now, sessionID, epoch,
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

func (repository *SQLRepository) CreateOperationAuthorization(ctx context.Context, attempt OperationAuthorizationAttempt) error {
	if !repository.ready() || len(attempt.SessionTokenHash) != sha256.Size || len(attempt.AuthorizationTokenHash) != sha256.Size || attempt.ExpectedCredentialEpoch < 1 || attempt.CreatedAt.IsZero() || !attempt.ExpiresAt.After(attempt.CreatedAt) || attempt.TOTPStep.IsZero() || attempt.TOTPStep.After(attempt.CreatedAt) || !security.ValidOperationTarget(attempt.Target) {
		return ErrInvalidInput
	}
	if parsed, ok := security.ParseOperationPurpose(string(attempt.Purpose)); !ok || parsed != attempt.Purpose {
		return ErrInvalidInput
	}
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
	if err := transaction.QueryRowContext(ctx, sessionQuery, bytes.Clone(attempt.SessionTokenHash)).Scan(&sessionID, &sessionEpoch, &expiresAt, &revokedAt); err != nil || sessionID <= 0 || sessionEpoch != epoch || !expiresAt.After(attempt.CreatedAt) || revokedAt.Valid {
		return ErrAuthenticationFailed
	}
	result, err := transaction.ExecContext(ctx,
		"INSERT INTO admin_operation_authorizations (token_hash, session_id, credential_epoch, purpose, target, totp_step, created_at, expires_at, consumed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)",
		bytes.Clone(attempt.AuthorizationTokenHash), sessionID, epoch, string(attempt.Purpose), attempt.Target, attempt.TOTPStep, attempt.CreatedAt, attempt.ExpiresAt,
	)
	if err != nil || !oneRow(result) {
		return ErrAuthenticationFailed
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (repository *SQLRepository) ConsumeOperationAuthorization(ctx context.Context, transaction *sql.Tx, sessionTokenHash, authorizationTokenHash []byte, purpose security.OperationPurpose, target string, now time.Time) error {
	if !repository.ready() || transaction == nil || len(sessionTokenHash) != sha256.Size || len(authorizationTokenHash) != sha256.Size || now.IsZero() || !security.ValidOperationTarget(target) {
		return ErrInvalidInput
	}
	parsed, ok := security.ParseOperationPurpose(string(purpose))
	if !ok || parsed != purpose {
		return ErrInvalidInput
	}
	const query = "SELECT o.id, o.credential_epoch, o.expires_at, o.consumed_at, s.credential_epoch, s.expires_at, s.revoked_at, a.credential_epoch FROM admin_operation_authorizations AS o JOIN site_sessions AS s ON s.id = o.session_id JOIN admin_identity AS a ON a.id = s.admin_identity_id WHERE o.token_hash = ? AND s.admin_identity_id = 1 AND s.token_hash = ? AND o.purpose = ? AND o.target = ? FOR UPDATE"
	var operationID, operationEpoch, sessionEpoch, identityEpoch int64
	var operationExpiresAt, sessionExpiresAt time.Time
	var consumedAt, revokedAt sql.NullTime
	err := transaction.QueryRowContext(ctx, query, bytes.Clone(authorizationTokenHash), bytes.Clone(sessionTokenHash), string(purpose), target).Scan(&operationID, &operationEpoch, &operationExpiresAt, &consumedAt, &sessionEpoch, &sessionExpiresAt, &revokedAt, &identityEpoch)
	if err != nil || operationID <= 0 || operationEpoch != sessionEpoch || sessionEpoch != identityEpoch || consumedAt.Valid || revokedAt.Valid || !operationExpiresAt.After(now) || !sessionExpiresAt.After(now) {
		return ErrAuthenticationFailed
	}
	result, err := transaction.ExecContext(ctx, "UPDATE admin_operation_authorizations SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL", now, operationID)
	if err != nil || !oneRow(result) {
		return ErrAuthenticationFailed
	}
	return nil
}

func (repository *SQLRepository) authorizeRecentTOTP(ctx context.Context, transaction *sql.Tx, tokenHash []byte, now time.Time) (security.SensitiveSession, error) {
	if !repository.ready() || transaction == nil || len(tokenHash) != sha256.Size || now.IsZero() {
		return security.SensitiveSession{}, ErrInvalidInput
	}
	epoch, err := lockAdminEpoch(ctx, transaction)
	if err != nil {
		return security.SensitiveSession{}, err
	}
	const query = "SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE"
	var sessionID, sessionEpoch int64
	var expiresAt time.Time
	var revokedAt, verifiedAt sql.NullTime
	err = transaction.QueryRowContext(ctx, query, bytes.Clone(tokenHash)).Scan(&sessionID, &sessionEpoch, &expiresAt, &revokedAt, &verifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return security.SensitiveSession{}, ErrAuthenticationFailed
	}
	if err != nil {
		return security.SensitiveSession{}, ErrUnavailable
	}
	if sessionID <= 0 || sessionEpoch != epoch || !expiresAt.After(now) || revokedAt.Valid {
		return security.SensitiveSession{}, ErrAuthenticationFailed
	}
	if !recentTOTPAt(verifiedAt, now) {
		return security.SensitiveSession{}, ErrRecentTOTPRequired
	}
	if repository.sensitiveSessions == nil {
		return security.SensitiveSession{}, ErrUnavailable
	}
	fence, valid := repository.sensitiveSessions.Issue(sessionID, sessionEpoch)
	if !valid {
		return security.SensitiveSession{}, ErrUnavailable
	}
	return fence, nil
}

func (repository *SQLRepository) renewRecentTOTP(ctx context.Context, transaction *sql.Tx, fence security.SensitiveSession, now time.Time) error {
	if !repository.ready() || transaction == nil || now.IsZero() {
		return ErrInvalidInput
	}
	if repository.sensitiveSessions == nil {
		return ErrUnavailable
	}
	sessionID, credentialEpoch, valid := repository.sensitiveSessions.Open(fence)
	if !valid {
		return ErrAuthenticationFailed
	}
	currentEpoch, err := lockAdminEpoch(ctx, transaction)
	if err != nil {
		return err
	}
	if currentEpoch != credentialEpoch {
		return ErrAuthenticationFailed
	}
	const query = "SELECT expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE id = ? AND admin_identity_id = 1 AND credential_epoch = ? FOR UPDATE"
	var expiresAt time.Time
	var revokedAt, verifiedAt sql.NullTime
	err = transaction.QueryRowContext(ctx, query, sessionID, credentialEpoch).Scan(&expiresAt, &revokedAt, &verifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthenticationFailed
	}
	if err != nil {
		return ErrUnavailable
	}
	if !expiresAt.After(now) || revokedAt.Valid {
		return ErrAuthenticationFailed
	}
	if !recentTOTPAt(verifiedAt, now) {
		return ErrRecentTOTPRequired
	}
	result, err := transaction.ExecContext(ctx,
		"UPDATE site_sessions SET totp_verified_at = ? WHERE id = ? AND credential_epoch = ? AND revoked_at IS NULL",
		now, sessionID, credentialEpoch,
	)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	return nil
}

func (repository *SQLRepository) RotateRecoveryCodes(ctx context.Context, authorizer security.SensitiveAuthorizer, sessionToken string, newCodeHashes [][]byte, clock func() time.Time) ([]byte, error) {
	if !repository.ready() || authorizer == nil || sessionToken == "" || !validCodeHashes(newCodeHashes) || clock == nil {
		return nil, ErrInvalidInput
	}
	newCodeHashes = cloneByteSlices(newCodeHashes)
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
	now := clock().UTC()
	if now.IsZero() {
		return nil, ErrInvalidInput
	}
	sensitiveSession, err := authorizer.AuthorizeRecentTOTP(ctx, transaction, sessionToken, now)
	if err != nil {
		return nil, err
	}
	const identityQuery = "SELECT email_ciphertext FROM admin_identity WHERE id = 1"
	var emailCiphertext []byte
	if err := transaction.QueryRowContext(ctx, identityQuery).Scan(&emailCiphertext); err != nil || len(emailCiphertext) == 0 {
		return nil, ErrAuthenticationFailed
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE admin_recovery_codes SET invalidated_at = ? WHERE admin_identity_id = ? AND used_at IS NULL AND invalidated_at IS NULL",
		now, int64(1),
	); err != nil {
		return nil, ErrUnavailable
	}
	if err := insertRecoveryCodes(ctx, transaction, newCodeHashes, now); err != nil {
		return nil, err
	}
	result, err := transaction.ExecContext(ctx,
		"INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES (?, ?, ?, ?)",
		"admin_recovery_material_rotated", int64(1), []byte("{}"), now,
	)
	if err != nil || !oneRow(result) {
		return nil, ErrUnavailable
	}
	if err := authorizer.RenewRecentTOTP(ctx, transaction, sensitiveSession, clock().UTC()); err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, ErrUnavailable
	}
	committed = true
	return bytes.Clone(emailCiphertext), nil
}

const handoffColumns = "id, handoff_kind, handoff_state, request_hash, token_hash, token_ciphertext, uid_ciphertext, uid_lookup, email_ciphertext, totp_secret_ciphertext, totp_uri_ciphertext, archive_password_ciphertext, recovery_archive, created_at, expires_at, mail_delivered_at"

type rowScanner interface {
	Scan(...any) error
}

func scanHandoff(row rowScanner) (PendingHandoff, error) {
	var handoff PendingHandoff
	var delivered sql.NullTime
	err := row.Scan(&handoff.ID, &handoff.Kind, &handoff.State, &handoff.RequestHash, &handoff.TokenHash, &handoff.TokenCiphertext,
		&handoff.UIDCiphertext, &handoff.UIDLookup, &handoff.EmailCiphertext, &handoff.TOTPSecretCiphertext,
		&handoff.TOTPURICiphertext, &handoff.PasswordCiphertext, &handoff.Archive, &handoff.CreatedAt, &handoff.ExpiresAt, &delivered)
	if err != nil {
		return PendingHandoff{}, err
	}
	handoff.MailDelivered = delivered.Valid
	return handoff, nil
}

func scanHandoffWithReservedCode(row rowScanner) (PendingHandoff, sql.NullInt64, error) {
	var handoff PendingHandoff
	var delivered sql.NullTime
	var reserved sql.NullInt64
	err := row.Scan(&handoff.ID, &handoff.Kind, &handoff.State, &handoff.RequestHash, &handoff.TokenHash, &handoff.TokenCiphertext,
		&handoff.UIDCiphertext, &handoff.UIDLookup, &handoff.EmailCiphertext, &handoff.TOTPSecretCiphertext,
		&handoff.TOTPURICiphertext, &handoff.PasswordCiphertext, &handoff.Archive, &handoff.CreatedAt, &handoff.ExpiresAt, &delivered, &reserved)
	if err != nil {
		return PendingHandoff{}, sql.NullInt64{}, err
	}
	handoff.MailDelivered = delivered.Valid
	return handoff, reserved, nil
}

func (repository *SQLRepository) PrepareInitialization(ctx context.Context, candidate HandoffRecord) (PendingHandoff, error) {
	if !repository.ready() || !validHandoffRecord(candidate, HandoffInitialization) {
		return PendingHandoff{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return PendingHandoff{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var active int64
	err = transaction.QueryRowContext(ctx, "SELECT id FROM admin_identity WHERE id = 1 FOR UPDATE").Scan(&active)
	if err == nil {
		return PendingHandoff{}, ErrAlreadyInitialized
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PendingHandoff{}, ErrUnavailable
	}
	query := "SELECT " + handoffColumns + " FROM admin_credential_handoffs WHERE handoff_kind = 'initialization' AND handoff_state = 'pending' FOR UPDATE"
	existing, scanErr := scanHandoff(transaction.QueryRowContext(ctx, query))
	if scanErr == nil {
		legacyUIDBound := len(existing.UIDCiphertext) != 0 || len(existing.UIDLookup) != 0
		if !legacyUIDBound && existing.ExpiresAt.After(candidate.CreatedAt) {
			if subtle.ConstantTimeCompare(existing.RequestHash, candidate.RequestHash) == 1 {
				if err := loadHandoffCodes(ctx, transaction, &existing); err != nil {
					return PendingHandoff{}, err
				}
				if err := transaction.Commit(); err != nil {
					return PendingHandoff{}, ErrUnavailable
				}
				committed = true
				return existing, nil
			}
			return PendingHandoff{}, ErrAlreadyInitialized
		}
		if err := expireHandoff(ctx, transaction, existing.ID, candidate.CreatedAt); err != nil {
			return PendingHandoff{}, err
		}
	}
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return PendingHandoff{}, ErrUnavailable
	}
	handoff, err := insertHandoff(ctx, transaction, candidate, nil)
	if err != nil {
		if errors.Is(err, ErrAlreadyInitialized) {
			_ = transaction.Rollback()
			committed = true
			return repository.PrepareInitialization(ctx, candidate)
		}
		return PendingHandoff{}, err
	}
	if err := transaction.Commit(); err != nil {
		return PendingHandoff{}, ErrUnavailable
	}
	committed = true
	return handoff, nil
}

func (repository *SQLRepository) ActivateInitialization(ctx context.Context, attempt ActivateInitializationAttempt) error {
	if !repository.ready() || len(attempt.TokenHash) != sha256.Size || attempt.Now.IsZero() || attempt.TOTPStep.IsZero() {
		return ErrInvalidInput
	}
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
	query := "SELECT " + handoffColumns + " FROM admin_credential_handoffs WHERE token_hash = ? FOR UPDATE"
	handoff, err := scanHandoff(transaction.QueryRowContext(ctx, query, bytes.Clone(attempt.TokenHash)))
	if err != nil || handoff.Kind != HandoffInitialization {
		return ErrAuthenticationFailed
	}
	if handoff.State == HandoffConfirmed {
		if err := transaction.Commit(); err != nil {
			return ErrUnavailable
		}
		committed = true
		return nil
	}
	if handoff.State != HandoffPending || len(handoff.UIDCiphertext) != 0 || len(handoff.UIDLookup) != 0 || !handoff.ExpiresAt.After(attempt.Now) {
		if handoff.State == HandoffPending {
			if err := expireHandoff(ctx, transaction, handoff.ID, attempt.Now); err != nil {
				return err
			}
			if err := transaction.Commit(); err != nil {
				return ErrUnavailable
			}
			committed = true
		}
		return ErrAuthenticationFailed
	}
	if err := loadHandoffCodes(ctx, transaction, &handoff); err != nil {
		return err
	}
	if !validCodeHashes(handoff.RecoveryCodeHashes) {
		return ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO admin_identity (id, credential_epoch, uid_ciphertext, uid_lookup, email_ciphertext, created_at, updated_at) VALUES (1, 1, ?, ?, ?, ?, ?)", nil, nil, handoff.EmailCiphertext, attempt.Now, attempt.Now); err != nil {
		return ErrAuthenticationFailed
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO admin_totp (admin_identity_id, secret_ciphertext, rotated_at) VALUES (1, ?, ?)", handoff.TOTPSecretCiphertext, attempt.Now); err != nil {
		return ErrUnavailable
	}
	if err := insertRecoveryCodes(ctx, transaction, handoff.RecoveryCodeHashes, attempt.Now); err != nil {
		return err
	}
	if err := destroyHandoff(ctx, transaction, handoff.ID, attempt.Now); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (repository *SQLRepository) PrepareRecoveryHandoff(ctx context.Context, expectedCredentialEpoch int64, codeHash []byte, candidate HandoffRecord) (PendingHandoff, error) {
	if !repository.ready() || expectedCredentialEpoch < 1 || len(codeHash) != sha256.Size || !validHandoffRecord(candidate, HandoffRecovery) {
		return PendingHandoff{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return PendingHandoff{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var credentialEpoch int64
	var emailCiphertext []byte
	if err := transaction.QueryRowContext(ctx, "SELECT credential_epoch, email_ciphertext FROM admin_identity WHERE id = 1 FOR UPDATE").Scan(&credentialEpoch, &emailCiphertext); err != nil || credentialEpoch != expectedCredentialEpoch || subtle.ConstantTimeCompare(emailCiphertext, candidate.EmailCiphertext) != 1 {
		return PendingHandoff{}, ErrAuthenticationFailed
	}
	var codeID int64
	if err := transaction.QueryRowContext(ctx, "SELECT id FROM admin_recovery_codes WHERE admin_identity_id = 1 AND code_hash = ? AND used_at IS NULL AND invalidated_at IS NULL FOR UPDATE", bytes.Clone(codeHash)).Scan(&codeID); err != nil {
		return PendingHandoff{}, ErrAuthenticationFailed
	}
	query := "SELECT " + handoffColumns + ", reserved_recovery_code_id FROM admin_credential_handoffs WHERE handoff_kind = 'recovery' AND handoff_state = 'pending' AND admin_identity_id = 1 FOR UPDATE"
	existing, reserved, scanErr := scanHandoffWithReservedCode(transaction.QueryRowContext(ctx, query))
	if scanErr == nil {
		if existing.ExpiresAt.After(candidate.CreatedAt) {
			if !reserved.Valid || reserved.Int64 != codeID || subtle.ConstantTimeCompare(existing.RequestHash, candidate.RequestHash) != 1 {
				return PendingHandoff{}, ErrAuthenticationFailed
			}
			if err := loadHandoffCodes(ctx, transaction, &existing); err != nil {
				return PendingHandoff{}, err
			}
			if err := transaction.Commit(); err != nil {
				return PendingHandoff{}, ErrUnavailable
			}
			committed = true
			return existing, nil
		}
		if err := expireHandoff(ctx, transaction, existing.ID, candidate.CreatedAt); err != nil {
			return PendingHandoff{}, err
		}
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		return PendingHandoff{}, ErrUnavailable
	}
	handoff, err := insertHandoff(ctx, transaction, candidate, &codeID)
	if err != nil {
		return PendingHandoff{}, err
	}
	if err := transaction.Commit(); err != nil {
		return PendingHandoff{}, ErrUnavailable
	}
	committed = true
	return handoff, nil
}

func (repository *SQLRepository) HandoffByToken(ctx context.Context, tokenHash []byte) (PendingHandoff, error) {
	if !repository.ready() || len(tokenHash) != sha256.Size {
		return PendingHandoff{}, ErrInvalidInput
	}
	query := "SELECT " + handoffColumns + " FROM admin_credential_handoffs WHERE token_hash = ? LIMIT 1"
	handoff, err := scanHandoff(repository.db.QueryRowContext(ctx, query, bytes.Clone(tokenHash)))
	if errors.Is(err, sql.ErrNoRows) {
		return PendingHandoff{}, ErrAuthenticationFailed
	}
	if err != nil {
		return PendingHandoff{}, ErrUnavailable
	}
	return handoff, nil
}

func (repository *SQLRepository) ConfirmRecoveryHandoff(ctx context.Context, tokenHash []byte, now time.Time) error {
	if !repository.ready() || len(tokenHash) != sha256.Size || now.IsZero() {
		return ErrInvalidInput
	}
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
	if _, err := lockAdminEpoch(ctx, transaction); err != nil {
		return err
	}
	query := "SELECT " + handoffColumns + ", reserved_recovery_code_id FROM admin_credential_handoffs WHERE token_hash = ? FOR UPDATE"
	handoff, reserved, err := scanHandoffWithReservedCode(transaction.QueryRowContext(ctx, query, bytes.Clone(tokenHash)))
	if err != nil || handoff.Kind != HandoffRecovery {
		return ErrAuthenticationFailed
	}
	if handoff.State == HandoffConfirmed {
		if err := transaction.Commit(); err != nil {
			return ErrUnavailable
		}
		committed = true
		return nil
	}
	if handoff.State == HandoffPending && (len(handoff.UIDCiphertext) != 0 || len(handoff.UIDLookup) != 0) {
		if err := expireHandoff(ctx, transaction, handoff.ID, now); err != nil {
			return err
		}
		if err := transaction.Commit(); err != nil {
			return ErrUnavailable
		}
		committed = true
		return ErrAuthenticationFailed
	}
	if handoff.State != HandoffPending || !handoff.ExpiresAt.After(now) || !reserved.Valid {
		return ErrAuthenticationFailed
	}
	if err := loadHandoffCodes(ctx, transaction, &handoff); err != nil {
		return err
	}
	if !validCodeHashes(handoff.RecoveryCodeHashes) {
		return ErrUnavailable
	}
	var active int
	if err := transaction.QueryRowContext(ctx, "SELECT 1 FROM admin_recovery_codes WHERE id = ? AND admin_identity_id = 1 AND used_at IS NULL AND invalidated_at IS NULL FOR UPDATE", reserved.Int64).Scan(&active); err != nil {
		return ErrAuthenticationFailed
	}
	if result, err := transaction.ExecContext(ctx, "UPDATE admin_recovery_codes SET used_at = ? WHERE id = ? AND used_at IS NULL AND invalidated_at IS NULL", now, reserved.Int64); err != nil || !oneRow(result) {
		return ErrAuthenticationFailed
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE admin_recovery_codes SET invalidated_at = ? WHERE admin_identity_id = 1 AND used_at IS NULL AND invalidated_at IS NULL", now); err != nil {
		return ErrUnavailable
	}
	if result, err := transaction.ExecContext(ctx, "UPDATE admin_totp SET secret_ciphertext = ?, rotated_at = ? WHERE admin_identity_id = 1", handoff.TOTPSecretCiphertext, now); err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if result, err := transaction.ExecContext(ctx, "UPDATE admin_identity SET credential_epoch = credential_epoch + 1, updated_at = ? WHERE id = 1", now); err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE admin_identity_id = 1", now); err != nil {
		return ErrUnavailable
	}
	if err := insertRecoveryCodes(ctx, transaction, handoff.RecoveryCodeHashes, now); err != nil {
		return err
	}
	if err := destroyHandoff(ctx, transaction, handoff.ID, now); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

type sqlHandoffMailClaim struct {
	conn      *sql.Conn
	handoffID int64
	lockName  string
	delivered bool
}

func (repository *SQLRepository) AcquireHandoffMailClaim(ctx context.Context, id int64) (HandoffMailClaim, error) {
	if !repository.ready() || id <= 0 {
		return nil, ErrInvalidInput
	}
	acquireCtx, cancel := context.WithTimeout(ctx, handoffMailAcquireTimeout)
	defer cancel()
	conn, err := repository.db.Conn(acquireCtx)
	if err != nil {
		return nil, ErrUnavailable
	}
	lockName := handoffMailLockPrefix + strconv.FormatInt(id, 10)
	var acquired sql.NullInt64
	// GET_LOCK is scoped to this dedicated MySQL connection. A process crash or
	// broken connection releases the claim instead of stranding a database row.
	if err := conn.QueryRowContext(acquireCtx, "SELECT GET_LOCK(?, ?)", lockName, handoffMailLockWait).Scan(&acquired); err != nil {
		discardSQLConn(conn)
		return nil, ErrUnavailable
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	claim := &sqlHandoffMailClaim{conn: conn, handoffID: id, lockName: lockName}
	var state string
	if err := conn.QueryRowContext(acquireCtx, "SELECT handoff_state, mail_delivered_at IS NOT NULL FROM admin_credential_handoffs WHERE id = ?", id).Scan(&state, &claim.delivered); err != nil || state != HandoffPending {
		_ = claim.Release()
		return nil, ErrUnavailable
	}
	return claim, nil
}

func (claim *sqlHandoffMailClaim) MailDelivered() bool {
	return claim != nil && claim.delivered
}

func (claim *sqlHandoffMailClaim) MarkAttempt(now time.Time, delivered bool) error {
	if claim == nil || claim.conn == nil || now.IsZero() || claim.delivered {
		return ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(context.Background(), handoffMailDBTimeout)
	defer cancel()
	var result sql.Result
	var err error
	if delivered {
		result, err = claim.conn.ExecContext(ctx, "UPDATE admin_credential_handoffs SET mail_attempt_count = mail_attempt_count + 1, last_mail_attempt_at = ?, mail_delivered_at = ? WHERE id = ? AND handoff_state = 'pending' AND mail_delivered_at IS NULL", now, now, claim.handoffID)
	} else {
		result, err = claim.conn.ExecContext(ctx, "UPDATE admin_credential_handoffs SET mail_attempt_count = mail_attempt_count + 1, last_mail_attempt_at = ? WHERE id = ? AND handoff_state = 'pending' AND mail_delivered_at IS NULL", now, claim.handoffID)
	}
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	claim.delivered = delivered
	return nil
}

func (claim *sqlHandoffMailClaim) Release() error {
	if claim == nil || claim.conn == nil {
		return nil
	}
	conn := claim.conn
	claim.conn = nil
	ctx, cancel := context.WithTimeout(context.Background(), handoffMailDBTimeout)
	defer cancel()
	var released sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", claim.lockName).Scan(&released); err != nil || !released.Valid || released.Int64 != 1 {
		discardSQLConn(conn)
		return ErrUnavailable
	}
	if err := conn.Close(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func discardSQLConn(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func (repository *SQLRepository) CleanupExpiredHandoffs(ctx context.Context, now time.Time, limit int) error {
	if !repository.ready() || now.IsZero() || limit < 1 || limit > 1000 {
		return ErrInvalidInput
	}
	_, err := repository.db.ExecContext(ctx, "DELETE FROM admin_credential_handoffs WHERE handoff_state = 'pending' AND (expires_at <= ? OR uid_ciphertext IS NOT NULL OR uid_lookup IS NOT NULL) ORDER BY id LIMIT ?", now, limit)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func insertHandoff(ctx context.Context, transaction *sql.Tx, candidate HandoffRecord, reservedCodeID *int64) (PendingHandoff, error) {
	result, err := transaction.ExecContext(ctx, "INSERT INTO admin_credential_handoffs (handoff_kind, handoff_state, request_hash, token_hash, token_ciphertext, admin_identity_id, reserved_recovery_code_id, uid_ciphertext, uid_lookup, email_ciphertext, totp_secret_ciphertext, totp_uri_ciphertext, archive_password_ciphertext, recovery_archive, created_at, expires_at) VALUES (?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", candidate.Kind, candidate.RequestHash, candidate.TokenHash, candidate.TokenCiphertext, nullableAdminID(candidate.Kind), reservedCodeID, candidate.UIDCiphertext, candidate.UIDLookup, candidate.EmailCiphertext, candidate.TOTPSecretCiphertext, candidate.TOTPURICiphertext, candidate.PasswordCiphertext, candidate.Archive, candidate.CreatedAt, candidate.ExpiresAt)
	if err != nil {
		if repositoryDuplicate(err) {
			return PendingHandoff{}, ErrAlreadyInitialized
		}
		return PendingHandoff{}, ErrUnavailable
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return PendingHandoff{}, ErrUnavailable
	}
	for ordinal, hash := range candidate.RecoveryCodeHashes {
		if _, err := transaction.ExecContext(ctx, "INSERT INTO admin_handoff_recovery_codes (handoff_id, code_ordinal, code_hash) VALUES (?, ?, ?)", id, ordinal, hash); err != nil {
			return PendingHandoff{}, ErrUnavailable
		}
	}
	return PendingHandoff{ID: id, Kind: candidate.Kind, State: HandoffPending, RequestHash: bytes.Clone(candidate.RequestHash), TokenHash: bytes.Clone(candidate.TokenHash), TokenCiphertext: bytes.Clone(candidate.TokenCiphertext), UIDCiphertext: bytes.Clone(candidate.UIDCiphertext), UIDLookup: bytes.Clone(candidate.UIDLookup), EmailCiphertext: bytes.Clone(candidate.EmailCiphertext), TOTPSecretCiphertext: bytes.Clone(candidate.TOTPSecretCiphertext), TOTPURICiphertext: bytes.Clone(candidate.TOTPURICiphertext), PasswordCiphertext: bytes.Clone(candidate.PasswordCiphertext), Archive: bytes.Clone(candidate.Archive), RecoveryCodeHashes: cloneByteSlices(candidate.RecoveryCodeHashes), CreatedAt: candidate.CreatedAt, ExpiresAt: candidate.ExpiresAt}, nil
}

func nullableAdminID(kind string) any {
	if kind == HandoffRecovery {
		return int64(1)
	}
	return nil
}

func loadHandoffCodes(ctx context.Context, transaction *sql.Tx, handoff *PendingHandoff) error {
	rows, err := transaction.QueryContext(ctx, "SELECT code_hash FROM admin_handoff_recovery_codes WHERE handoff_id = ? ORDER BY code_ordinal", handoff.ID)
	if err != nil {
		return ErrUnavailable
	}
	defer rows.Close()
	handoff.RecoveryCodeHashes = nil
	for rows.Next() {
		var hash []byte
		if rows.Scan(&hash) != nil {
			return ErrUnavailable
		}
		handoff.RecoveryCodeHashes = append(handoff.RecoveryCodeHashes, bytes.Clone(hash))
	}
	if rows.Err() != nil {
		return ErrUnavailable
	}
	return nil
}

func destroyHandoff(ctx context.Context, transaction *sql.Tx, id int64, now time.Time) error {
	if _, err := transaction.ExecContext(ctx, "DELETE FROM admin_handoff_recovery_codes WHERE handoff_id = ?", id); err != nil {
		return ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "UPDATE admin_credential_handoffs SET handoff_state = 'confirmed', confirmed_at = ?, token_ciphertext = NULL, uid_ciphertext = NULL, uid_lookup = NULL, totp_secret_ciphertext = NULL, totp_uri_ciphertext = NULL, archive_password_ciphertext = NULL, recovery_archive = NULL WHERE id = ? AND handoff_state = 'pending'", now, id)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	return nil
}

func expireHandoff(ctx context.Context, transaction *sql.Tx, id int64, now time.Time) error {
	if _, err := transaction.ExecContext(ctx, "DELETE FROM admin_handoff_recovery_codes WHERE handoff_id = ?", id); err != nil {
		return ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE admin_credential_handoffs SET handoff_state = 'expired', token_ciphertext = NULL, uid_ciphertext = NULL, uid_lookup = NULL, totp_secret_ciphertext = NULL, totp_uri_ciphertext = NULL, archive_password_ciphertext = NULL, recovery_archive = NULL WHERE id = ? AND handoff_state = 'pending'", id); err != nil {
		return ErrUnavailable
	}
	return nil
}

func validHandoffRecord(record HandoffRecord, kind string) bool {
	return record.Kind == kind && len(record.RequestHash) == sha256.Size && len(record.TokenHash) == sha256.Size && len(record.TokenCiphertext) > 0 && len(record.UIDCiphertext) == 0 && len(record.UIDLookup) == 0 && len(record.EmailCiphertext) > 0 && len(record.TOTPSecretCiphertext) > 0 && len(record.TOTPURICiphertext) > 0 && len(record.PasswordCiphertext) > 0 && len(record.Archive) > 0 && !record.CreatedAt.IsZero() && record.ExpiresAt.After(record.CreatedAt) && validCodeHashes(record.RecoveryCodeHashes)
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

func recentTOTPAt(verifiedAt sql.NullTime, now time.Time) bool {
	return verifiedAt.Valid && !verifiedAt.Time.After(now) && now.Sub(verifiedAt.Time) < RecentTOTPWindow
}

func repositoryDuplicate(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func validInitialization(record InitializationRecord) bool {
	return record.CreatedAt.IsZero() == false && record.Identity.CredentialEpoch == 1 && validIdentityRecord(record.Identity) && validCodeHashes(record.RecoveryCodeHashes)
}

func validIdentityRecord(record IdentityRecord) bool {
	legacyUID := (len(record.UIDCiphertext) == 0 && len(record.UIDLookup) == 0) ||
		(len(record.UIDCiphertext) > 0 && len(record.UIDCiphertext) <= 512 && len(record.UIDLookup) == sha256.Size)
	return record.CredentialEpoch >= 1 && legacyUID && len(record.EmailCiphertext) > 0 && len(record.EmailCiphertext) <= 1024 && len(record.TOTPSecretCiphertext) > 0 && len(record.TOTPSecretCiphertext) <= 512
}

func validEmailLoginSessionAttempt(attempt EmailLoginSessionAttempt) bool {
	return attempt.ExpectedCredentialEpoch >= 1 && len(attempt.TokenHash) == sha256.Size && !attempt.CreatedAt.IsZero() && attempt.ExpiresAt.After(attempt.CreatedAt)
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
	Now               func() time.Time
	Random            io.Reader
	TOTP              TOTPProvider
	Issuer            string
	EmailChallengeTTL time.Duration
	SessionTTL        time.Duration
	HandoffTTL        time.Duration
}

type Service struct {
	repository  Repository
	keys        security.Keyring
	sender      MailSender
	now         func() time.Time
	random      io.Reader
	totp        TOTPProvider
	issuer      string
	emailTTL    time.Duration
	sessionTTL  time.Duration
	handoffTTL  time.Duration
	emailMu     sync.Mutex
	emailLogins map[string]*emailLoginState
}

type InitializeResult struct {
	TOTPURI          string `json:"totpUri"`
	RecoveryPassword string `json:"recoveryPassword"`
	HandoffToken     string `json:"handoffToken"`
}

type LoginResult struct {
	Token     string    `json:"-"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type EmailLoginChallenge struct {
	ChallengeID string    `json:"challengeId"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type emailLoginState struct {
	expiresAt time.Time
	codeHash  []byte
	epoch     int64
	attempts  int
	verifying bool
	timer     *time.Timer
}

type RecoveryResult struct {
	RecoveryPassword string `json:"recoveryPassword"`
}

type RecoveryPreparationResult struct {
	TOTPURI          string `json:"totpUri"`
	RecoveryPassword string `json:"recoveryPassword"`
	HandoffToken     string `json:"handoffToken"`
}

func NewService(repository Repository, keys security.Keyring, sender MailSender, options ServiceOptions) (*Service, error) {
	if repository == nil || sender == nil {
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
	if options.EmailChallengeTTL == 0 {
		options.EmailChallengeTTL = defaultEmailChallengeTTL
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = DefaultAdministratorSessionTTL
	}
	if options.HandoffTTL == 0 {
		options.HandoffTTL = defaultHandoffTTL
	}
	if options.EmailChallengeTTL <= 0 || options.EmailChallengeTTL > defaultEmailChallengeTTL || options.SessionTTL <= 0 || options.SessionTTL > DefaultAdministratorSessionTTL || options.HandoffTTL <= 0 || options.HandoffTTL > 24*time.Hour || len(options.Issuer) > 128 {
		return nil, ErrInvalidInput
	}
	return &Service{
		repository: repository, keys: keys, sender: sender,
		now: options.Now, random: options.Random, totp: options.TOTP, issuer: options.Issuer,
		emailTTL: options.EmailChallengeTTL, sessionTTL: options.SessionTTL, handoffTTL: options.HandoffTTL,
		emailLogins: make(map[string]*emailLoginState),
	}, nil
}

func (service *Service) BeginEmailLogin(ctx context.Context) (EmailLoginChallenge, error) {
	if service == nil {
		return EmailLoginChallenge{}, ErrUnavailable
	}
	record, err := service.repository.Identity(ctx)
	if err != nil || record.CredentialEpoch < 1 {
		return EmailLoginChallenge{}, ErrUnavailable
	}
	email, err := service.keys.Open("admin_email", record.EmailCiphertext)
	if err != nil || !validEmail(string(email)) {
		clear(email)
		return EmailLoginChallenge{}, ErrUnavailable
	}
	defer clear(email)
	code, err := randomNumericCode(service.random, emailCodeLength)
	if err != nil {
		return EmailLoginChallenge{}, ErrUnavailable
	}
	defer clear(code)
	codeHash, err := service.keys.Lookup("admin_email_login_code", code)
	if err != nil {
		return EmailLoginChallenge{}, ErrUnavailable
	}
	challengeID, err := service.keys.NewToken()
	if err != nil {
		return EmailLoginChallenge{}, ErrUnavailable
	}
	now := service.now()
	expiresAt := now.Add(service.emailTTL)
	state := &emailLoginState{expiresAt: expiresAt, codeHash: bytes.Clone(codeHash), epoch: record.CredentialEpoch}
	service.emailMu.Lock()
	if service.emailLogins[challengeID] != nil {
		service.emailMu.Unlock()
		clear(state.codeHash)
		return EmailLoginChallenge{}, ErrUnavailable
	}
	service.emailLogins[challengeID] = state
	state.timer = time.AfterFunc(service.emailTTL, func() { service.expireEmailLogin(challengeID, state) })
	service.emailMu.Unlock()
	message := Message{To: string(email), Subject: "Gift Panel administrator login code", Text: "Your Gift Panel administrator login code is " + string(code) + ". It expires in five minutes."}
	if err := service.sender.Send(ctx, message); err != nil {
		service.removeEmailLogin(challengeID, state)
		return EmailLoginChallenge{}, ErrUnavailable
	}
	return EmailLoginChallenge{ChallengeID: challengeID, ExpiresAt: expiresAt}, nil
}

func (service *Service) VerifyEmailLogin(ctx context.Context, challengeID, emailCode string) (LoginResult, error) {
	if service == nil || challengeID == "" || !validEmailLoginCode(emailCode) {
		return LoginResult{}, ErrAuthenticationFailed
	}
	codeHash, err := service.keys.Lookup("admin_email_login_code", []byte(emailCode))
	if err != nil {
		return LoginResult{}, ErrAuthenticationFailed
	}
	now := service.now()
	service.emailMu.Lock()
	state := service.emailLogins[challengeID]
	if state == nil || state.verifying || !now.Before(state.expiresAt) || subtle.ConstantTimeCompare(codeHash, state.codeHash) != 1 {
		if state != nil && !state.verifying {
			state.attempts++
			if state.attempts >= emailCodeAttempts || !now.Before(state.expiresAt) {
				service.deleteEmailLoginLocked(challengeID, state)
			}
		}
		service.emailMu.Unlock()
		return LoginResult{}, ErrAuthenticationFailed
	}
	state.verifying = true
	expectedEpoch := state.epoch
	service.emailMu.Unlock()

	record, err := service.repository.Identity(ctx)
	if err != nil || record.CredentialEpoch != expectedEpoch {
		service.failEmailLogin(challengeID, state)
		return LoginResult{}, ErrAuthenticationFailed
	}
	if !service.consumeEmailLogin(challengeID, state) {
		return LoginResult{}, ErrAuthenticationFailed
	}
	token, err := service.keys.NewToken()
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(token))
	if err != nil {
		return LoginResult{}, ErrUnavailable
	}
	expiresAt := now.Add(service.sessionTTL)
	if err := service.repository.CreateEmailLoginSession(ctx, EmailLoginSessionAttempt{ExpectedCredentialEpoch: expectedEpoch, TokenHash: tokenHash, CreatedAt: now, ExpiresAt: expiresAt}); err != nil {
		if errors.Is(err, ErrAuthenticationFailed) {
			return LoginResult{}, ErrAuthenticationFailed
		}
		return LoginResult{}, ErrUnavailable
	}
	return LoginResult{Token: token, ExpiresAt: expiresAt}, nil
}

func randomNumericCode(random io.Reader, length int) ([]byte, error) {
	if random == nil || length <= 0 || length > 32 {
		return nil, ErrInvalidInput
	}
	code := make([]byte, 0, length)
	buffer := []byte{0}
	for len(code) < length {
		if _, err := io.ReadFull(random, buffer); err != nil {
			clear(code)
			return nil, ErrUnavailable
		}
		if buffer[0] < 250 {
			code = append(code, '0'+buffer[0]%10)
		}
	}
	return code, nil
}

func validEmailLoginCode(code string) bool {
	if len(code) != emailCodeLength {
		return false
	}
	for index := range code {
		if code[index] < '0' || code[index] > '9' {
			return false
		}
	}
	return true
}

func (service *Service) failEmailLogin(challengeID string, expected *emailLoginState) {
	service.emailMu.Lock()
	if state := service.emailLogins[challengeID]; state == expected {
		state.verifying = false
		state.attempts++
		if state.attempts >= emailCodeAttempts || !service.now().Before(state.expiresAt) {
			service.deleteEmailLoginLocked(challengeID, state)
		}
	}
	service.emailMu.Unlock()
}

func (service *Service) expireEmailLogin(challengeID string, expected *emailLoginState) {
	service.removeEmailLogin(challengeID, expected)
}

func (service *Service) consumeEmailLogin(challengeID string, expected *emailLoginState) bool {
	service.emailMu.Lock()
	defer service.emailMu.Unlock()
	if service.emailLogins[challengeID] != expected || !expected.verifying || !service.now().Before(expected.expiresAt) {
		return false
	}
	service.deleteEmailLoginLocked(challengeID, expected)
	return true
}

func (service *Service) removeEmailLogin(challengeID string, expected *emailLoginState) {
	service.emailMu.Lock()
	if service.emailLogins[challengeID] == expected {
		service.deleteEmailLoginLocked(challengeID, expected)
	}
	service.emailMu.Unlock()
}

func (service *Service) deleteEmailLoginLocked(challengeID string, state *emailLoginState) {
	delete(service.emailLogins, challengeID)
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	clear(state.codeHash)
}

func (service *Service) Initialize(ctx context.Context, email string) (InitializeResult, error) {
	if service == nil || !validEmail(email) {
		return InitializeResult{}, ErrInvalidInput
	}
	repository, ok := service.repository.(HandoffRepository)
	if !ok {
		return InitializeResult{}, ErrUnavailable
	}
	return service.initializeHandoff(ctx, repository, email)
}

func (service *Service) initializeHandoff(ctx context.Context, repository HandoffRepository, email string) (InitializeResult, error) {
	now := service.now()
	_ = repository.CleanupExpiredHandoffs(ctx, now, defaultCleanupLimit)
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
	emailCiphertext, err := service.keys.Seal("admin_email", []byte(email))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	secretCiphertext, err := service.keys.Seal("admin_totp", []byte(secret))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	uriCiphertext, err := service.keys.Seal("admin_handoff_totp_uri", []byte(uri))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	passwordCiphertext, err := service.keys.Seal("admin_handoff_password", []byte(material.Password))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	token, err := service.keys.NewToken()
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	tokenHash, err := service.keys.HashToken("admin_handoff_token", []byte(token))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	tokenCiphertext, err := service.keys.Seal("admin_handoff_token", []byte(token))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	requestHash, err := service.keys.HashToken("admin_handoff_request", []byte(email))
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	handoff, err := repository.PrepareInitialization(ctx, HandoffRecord{Kind: HandoffInitialization, RequestHash: requestHash, TokenHash: tokenHash, TokenCiphertext: tokenCiphertext, EmailCiphertext: emailCiphertext, TOTPSecretCiphertext: secretCiphertext, TOTPURICiphertext: uriCiphertext, PasswordCiphertext: passwordCiphertext, Archive: bytes.Clone(material.Archive), RecoveryCodeHashes: hashes, CreatedAt: now, ExpiresAt: now.Add(service.handoffTTL)})
	if err != nil {
		if errors.Is(err, ErrAlreadyInitialized) {
			return InitializeResult{}, ErrAlreadyInitialized
		}
		return InitializeResult{}, ErrUnavailable
	}
	return service.deliverPendingInitialization(ctx, repository, handoff)
}

func (service *Service) deliverPendingInitialization(ctx context.Context, repository HandoffRepository, handoff PendingHandoff) (InitializeResult, error) {
	uri, err := service.keys.Open("admin_handoff_totp_uri", handoff.TOTPURICiphertext)
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	defer clear(uri)
	password, err := service.keys.Open("admin_handoff_password", handoff.PasswordCiphertext)
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	defer clear(password)
	token, err := service.keys.Open("admin_handoff_token", handoff.TokenCiphertext)
	if err != nil {
		return InitializeResult{}, ErrUnavailable
	}
	defer clear(token)
	email, err := service.keys.Open("admin_email", handoff.EmailCiphertext)
	if err != nil || !validEmail(string(email)) {
		clear(email)
		return InitializeResult{}, ErrUnavailable
	}
	defer clear(email)
	if err := service.deliverHandoffArchive(ctx, repository, handoff.ID, string(email), handoff.Archive); err != nil {
		return InitializeResult{}, err
	}
	if len(password) != 20 || len(uri) == 0 || len(token) == 0 {
		return InitializeResult{}, ErrUnavailable
	}
	return InitializeResult{TOTPURI: string(uri), RecoveryPassword: string(password), HandoffToken: string(token)}, nil
}

func (service *Service) deliverHandoffArchive(ctx context.Context, repository HandoffRepository, handoffID int64, email string, archive []byte) error {
	claim, err := repository.AcquireHandoffMailClaim(ctx, handoffID)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = claim.Release() }()
	if claim.MailDelivered() {
		if err := claim.Release(); err != nil {
			return ErrUnavailable
		}
		return nil
	}

	// The advisory claim prevents concurrent sends across service instances. It
	// cannot make SMTP acceptance and the following database mark atomic: a
	// process crash or mark failure in that narrow interval can cause the same
	// still-valid attachment to be sent again by a later retry.
	sendErr := service.sendArchive(ctx, email, archive)
	markErr := claim.MarkAttempt(service.now(), sendErr == nil)
	releaseErr := claim.Release()
	if sendErr != nil || markErr != nil || releaseErr != nil {
		return ErrUnavailable
	}
	return nil
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
	if !valid || step.IsZero() || step.After(now) {
		return ErrAuthenticationFailed
	}
	if err := service.repository.ConfirmTOTP(ctx, ConfirmTOTPAttempt{
		ExpectedCredentialEpoch: record.CredentialEpoch, TokenHash: tokenHash, Now: now, TOTPStep: step,
	}); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

func (service *Service) AuthorizeOperation(ctx context.Context, sessionToken, code string, purpose security.OperationPurpose, target string) (string, error) {
	if service == nil || sessionToken == "" || !validTOTPCode(code) || !security.ValidOperationTarget(target) {
		return "", ErrAuthenticationFailed
	}
	parsed, ok := security.ParseOperationPurpose(string(purpose))
	if !ok || parsed != purpose {
		return "", ErrAuthenticationFailed
	}
	repository, ok := service.repository.(operationAuthorizationRepository)
	if !ok {
		return "", ErrUnavailable
	}
	sessionHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return "", ErrAuthenticationFailed
	}
	now := service.now().UTC()
	session, err := service.repository.FindSession(ctx, sessionHash, now)
	if err != nil {
		return "", ErrAuthenticationFailed
	}
	record, err := service.repository.Identity(ctx)
	if err != nil || record.CredentialEpoch != session.CredentialEpoch {
		return "", ErrAuthenticationFailed
	}
	secret, err := service.keys.Open("admin_totp", record.TOTPSecretCiphertext)
	if err != nil {
		return "", ErrAuthenticationFailed
	}
	defer clear(secret)
	step, valid := service.totp.Validate(code, string(secret), now)
	if !valid || step.IsZero() || step.After(now) {
		return "", ErrAuthenticationFailed
	}
	authorizationToken, err := service.keys.NewToken()
	if err != nil {
		return "", ErrUnavailable
	}
	authorizationHash, err := service.keys.HashToken("admin_operation_authorization", []byte(authorizationToken))
	if err != nil {
		return "", ErrUnavailable
	}
	if err := repository.CreateOperationAuthorization(ctx, OperationAuthorizationAttempt{
		SessionTokenHash: sessionHash, AuthorizationTokenHash: authorizationHash,
		ExpectedCredentialEpoch: record.CredentialEpoch, Purpose: purpose, Target: target,
		CreatedAt: now, ExpiresAt: now.Add(operationAuthorizationTTL), TOTPStep: step,
	}); err != nil {
		return "", ErrAuthenticationFailed
	}
	return authorizationToken, nil
}

func (service *Service) ConsumeOperation(ctx context.Context, transaction *sql.Tx, sessionToken, authorizationToken string, purpose security.OperationPurpose, target string, now time.Time) error {
	if service == nil || transaction == nil || sessionToken == "" || authorizationToken == "" || now.IsZero() || !security.ValidOperationTarget(target) {
		return ErrAuthenticationFailed
	}
	parsed, ok := security.ParseOperationPurpose(string(purpose))
	if !ok || parsed != purpose {
		return ErrAuthenticationFailed
	}
	repository, ok := service.repository.(operationAuthorizationRepository)
	if !ok {
		return ErrUnavailable
	}
	sessionHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return ErrAuthenticationFailed
	}
	authorizationHash, err := service.keys.HashToken("admin_operation_authorization", []byte(authorizationToken))
	if err != nil {
		return ErrAuthenticationFailed
	}
	if err := repository.ConsumeOperationAuthorization(ctx, transaction, sessionHash, authorizationHash, purpose, target, now.UTC()); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

func (service *Service) AuthorizeRecentTOTP(ctx context.Context, transaction *sql.Tx, sessionToken string, now time.Time) (security.SensitiveSession, error) {
	if service == nil || transaction == nil || sessionToken == "" || now.IsZero() {
		return security.SensitiveSession{}, ErrAuthenticationFailed
	}
	repository, ok := service.repository.(sensitiveSessionRepository)
	if !ok {
		return security.SensitiveSession{}, ErrUnavailable
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return security.SensitiveSession{}, ErrAuthenticationFailed
	}
	return repository.authorizeRecentTOTP(ctx, transaction, tokenHash, now)
}

func (service *Service) RenewRecentTOTP(ctx context.Context, transaction *sql.Tx, session security.SensitiveSession, now time.Time) error {
	if service == nil || transaction == nil || now.IsZero() {
		return ErrAuthenticationFailed
	}
	repository, ok := service.repository.(sensitiveSessionRepository)
	if !ok {
		return ErrUnavailable
	}
	return repository.renewRecentTOTP(ctx, transaction, session, now)
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
	if !recentTOTPAt(sql.NullTime{Time: session.TOTPVerifiedAt, Valid: !session.TOTPVerifiedAt.IsZero()}, now) {
		return ErrRecentTOTPRequired
	}
	return nil
}

// RequireSession validates only an active administrator session. It exposes no
// identity details and deliberately does not turn a read-only status request
// into a recent-TOTP operation.
func (service *Service) RequireSession(ctx context.Context, sessionToken string) error {
	if service == nil || sessionToken == "" {
		return ErrAuthenticationFailed
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return ErrAuthenticationFailed
	}
	if _, err := service.repository.FindSession(ctx, tokenHash, service.now()); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

func (service *Service) Logout(ctx context.Context, sessionToken string) error {
	if service == nil || sessionToken == "" {
		return ErrAuthenticationFailed
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(sessionToken))
	if err != nil {
		return ErrAuthenticationFailed
	}
	if err := service.repository.RevokeSession(ctx, tokenHash, service.now()); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

func (service *Service) SendRecovery(ctx context.Context, sessionToken string) (RecoveryResult, error) {
	if err := service.RequireRecentTOTP(ctx, sessionToken); err != nil {
		return RecoveryResult{}, err
	}
	repository, ok := service.repository.(recoveryRotationRepository)
	if !ok {
		return RecoveryResult{}, ErrUnavailable
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
	storedEmail, err := repository.RotateRecoveryCodes(ctx, service, sessionToken, hashes, service.now)
	if err != nil {
		if errors.Is(err, ErrRecentTOTPRequired) {
			return RecoveryResult{}, ErrRecentTOTPRequired
		}
		if errors.Is(err, ErrAuthenticationFailed) {
			return RecoveryResult{}, ErrAuthenticationFailed
		}
		return RecoveryResult{}, ErrUnavailable
	}
	if subtle.ConstantTimeCompare(storedEmail, record.EmailCiphertext) != 1 {
		return RecoveryResult{}, ErrUnavailable
	}
	return RecoveryResult{RecoveryPassword: material.Password}, nil
}

func (service *Service) PrepareRecovery(ctx context.Context, recoveryCode string) (RecoveryPreparationResult, error) {
	if service == nil || recoveryCode == "" || len(recoveryCode) > 256 {
		return RecoveryPreparationResult{}, ErrAuthenticationFailed
	}
	repository, ok := service.repository.(HandoffRepository)
	if !ok {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	record, err := service.repository.Identity(ctx)
	if err != nil || record.CredentialEpoch < 1 {
		return RecoveryPreparationResult{}, ErrAuthenticationFailed
	}
	now := service.now()
	_ = repository.CleanupExpiredHandoffs(ctx, now, defaultCleanupLimit)
	secret, uri, err := service.totp.Generate(service.issuer, "administrator")
	if err != nil || secret == "" || uri == "" {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	material, err := buildRecoveryPackage(service.random)
	if err != nil {
		return RecoveryPreparationResult{}, err
	}
	hashes, err := recoveryCodeHashes(material.Codes)
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	secretCiphertext, err := service.keys.Seal("admin_totp", []byte(secret))
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	uriCiphertext, err := service.keys.Seal("admin_handoff_totp_uri", []byte(uri))
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	passwordCiphertext, err := service.keys.Seal("admin_handoff_password", []byte(material.Password))
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	token, err := service.keys.NewToken()
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	tokenHash, err := service.keys.HashToken("admin_handoff_token", []byte(token))
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	tokenCiphertext, err := service.keys.Seal("admin_handoff_token", []byte(token))
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	requestHash, err := service.keys.HashToken("admin_handoff_request", []byte(recoveryCode))
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	codeHash := sha256.Sum256([]byte(recoveryCode))
	handoff, err := repository.PrepareRecoveryHandoff(ctx, record.CredentialEpoch, codeHash[:], HandoffRecord{Kind: HandoffRecovery, RequestHash: requestHash, TokenHash: tokenHash, TokenCiphertext: tokenCiphertext, EmailCiphertext: record.EmailCiphertext, TOTPSecretCiphertext: secretCiphertext, TOTPURICiphertext: uriCiphertext, PasswordCiphertext: passwordCiphertext, Archive: bytes.Clone(material.Archive), RecoveryCodeHashes: hashes, CreatedAt: now, ExpiresAt: now.Add(service.handoffTTL)})
	if err != nil {
		return RecoveryPreparationResult{}, ErrAuthenticationFailed
	}
	return service.deliverPendingRecovery(ctx, repository, handoff)
}

func (service *Service) deliverPendingRecovery(ctx context.Context, repository HandoffRepository, handoff PendingHandoff) (RecoveryPreparationResult, error) {
	uri, err := service.keys.Open("admin_handoff_totp_uri", handoff.TOTPURICiphertext)
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	defer clear(uri)
	password, err := service.keys.Open("admin_handoff_password", handoff.PasswordCiphertext)
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	defer clear(password)
	token, err := service.keys.Open("admin_handoff_token", handoff.TokenCiphertext)
	if err != nil {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	defer clear(token)
	email, err := service.keys.Open("admin_email", handoff.EmailCiphertext)
	if err != nil || !validEmail(string(email)) {
		clear(email)
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	defer clear(email)
	if err := service.deliverHandoffArchive(ctx, repository, handoff.ID, string(email), handoff.Archive); err != nil {
		return RecoveryPreparationResult{}, err
	}
	if len(password) != 20 || len(uri) == 0 || len(token) == 0 {
		return RecoveryPreparationResult{}, ErrUnavailable
	}
	return RecoveryPreparationResult{TOTPURI: string(uri), RecoveryPassword: string(password), HandoffToken: string(token)}, nil
}

func (service *Service) ConfirmHandoff(ctx context.Context, handoffToken, code string) error {
	if service == nil || handoffToken == "" || !validTOTPCode(code) {
		return ErrAuthenticationFailed
	}
	repository, ok := service.repository.(HandoffRepository)
	if !ok {
		return ErrUnavailable
	}
	tokenHash, err := service.keys.HashToken("admin_handoff_token", []byte(handoffToken))
	if err != nil {
		return ErrAuthenticationFailed
	}
	handoff, err := repository.HandoffByToken(ctx, tokenHash)
	if err != nil || (handoff.Kind != HandoffInitialization && handoff.Kind != HandoffRecovery) {
		return ErrAuthenticationFailed
	}
	if handoff.State == HandoffConfirmed {
		return nil
	}
	now := service.now()
	if handoff.State != HandoffPending || !handoff.ExpiresAt.After(now) || len(handoff.UIDCiphertext) != 0 || len(handoff.UIDLookup) != 0 {
		_ = repository.CleanupExpiredHandoffs(ctx, now, defaultCleanupLimit)
		return ErrAuthenticationFailed
	}
	secret, err := service.keys.Open("admin_totp", handoff.TOTPSecretCiphertext)
	if err != nil {
		return ErrAuthenticationFailed
	}
	step, valid := service.totp.Validate(code, string(secret), now)
	clear(secret)
	if !valid || step.IsZero() {
		return ErrAuthenticationFailed
	}
	if handoff.Kind == HandoffInitialization {
		if err := repository.ActivateInitialization(ctx, ActivateInitializationAttempt{TokenHash: tokenHash, Now: now, TOTPStep: step}); err != nil {
			return ErrAuthenticationFailed
		}
		return nil
	}
	if err := repository.ConfirmRecoveryHandoff(ctx, tokenHash, now); err != nil {
		return ErrAuthenticationFailed
	}
	return nil
}

func (service *Service) RunHandoffCleanup(ctx context.Context, interval time.Duration) {
	repository, ok := service.repository.(HandoffRepository)
	if !ok || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = repository.CleanupExpiredHandoffs(ctx, service.now(), defaultCleanupLimit)
		}
	}
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
