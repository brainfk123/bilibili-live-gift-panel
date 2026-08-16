package identity

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
)

const digestSize = 32

var (
	// ErrInvalidInput reports malformed repository input without reflecting it.
	ErrInvalidInput = errors.New("identity: invalid input")
	// ErrNotFound deliberately covers absent and no-longer-valid identities.
	ErrNotFound = errors.New("identity: not found")
	// ErrUIDAlreadyBound is the stable duplicate-UID result.
	ErrUIDAlreadyBound = errors.New("identity: uid already bound")
	// ErrRepositoryUnavailable hides all database and driver error details.
	ErrRepositoryUnavailable = errors.New("identity: repository unavailable")
)

// Repository persists account bindings and hash-addressed site sessions.
type Repository interface {
	FindAccountByUIDLookup(context.Context, []byte) (Account, error)
	CreateBoundAccount(context.Context, EncryptedUID) (Account, error)
	CreateSession(context.Context, Session) error
	FindSessionByHash(context.Context, []byte, time.Time) (Session, error)
	RevokeSession(context.Context, []byte) error
}

type sqlRepository struct {
	db *sql.DB
}

// NewRepository builds the MySQL-backed identity repository.
func NewRepository(db *sql.DB) Repository {
	return &sqlRepository{db: db}
}

func (repository *sqlRepository) FindAccountByUIDLookup(ctx context.Context, lookup []byte) (Account, error) {
	if !repository.ready() || len(lookup) != digestSize {
		return Account{}, ErrInvalidInput
	}
	lookup = bytes.Clone(lookup)

	const query = `
SELECT a.id, a.credential_epoch, a.disabled_at, a.created_at, a.updated_at
FROM bili_uid_bindings AS b
JOIN streamer_accounts AS a ON a.id = b.account_id
WHERE b.uid_lookup = ? AND b.unbound_at IS NULL
LIMIT 1`

	var account Account
	var disabledAt sql.NullTime
	err := repository.db.QueryRowContext(ctx, query, lookup).Scan(
		&account.ID,
		&account.CredentialEpoch,
		&disabledAt,
		&account.CreatedAt,
		&account.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, ErrRepositoryUnavailable
	}
	if account.ID <= 0 || account.CredentialEpoch < 1 {
		return Account{}, ErrRepositoryUnavailable
	}
	if disabledAt.Valid {
		value := disabledAt.Time
		account.DisabledAt = &value
	}
	return account, nil
}

func (repository *sqlRepository) CreateBoundAccount(ctx context.Context, encryptedUID EncryptedUID) (Account, error) {
	if !repository.ready() || len(encryptedUID.Ciphertext) == 0 || len(encryptedUID.Ciphertext) > 512 || len(encryptedUID.Lookup) != digestSize {
		return Account{}, ErrInvalidInput
	}
	ciphertext := bytes.Clone(encryptedUID.Ciphertext)
	lookup := bytes.Clone(encryptedUID.Lookup)

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	result, err := transaction.ExecContext(ctx, "INSERT INTO streamer_accounts () VALUES ()")
	if err != nil {
		return Account{}, ErrRepositoryUnavailable
	}
	accountID, err := result.LastInsertId()
	if err != nil || accountID <= 0 {
		return Account{}, ErrRepositoryUnavailable
	}

	_, err = transaction.ExecContext(ctx,
		"INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup) VALUES (?, ?, ?)",
		accountID,
		ciphertext,
		lookup,
	)
	if err != nil {
		if isDuplicateKey(err) {
			return Account{}, ErrUIDAlreadyBound
		}
		return Account{}, ErrRepositoryUnavailable
	}

	if err := transaction.Commit(); err != nil {
		return Account{}, ErrRepositoryUnavailable
	}
	committed = true
	return Account{ID: accountID, CredentialEpoch: 1}, nil
}

func (repository *sqlRepository) CreateSession(ctx context.Context, session Session) error {
	if !repository.ready() || session.AccountID <= 0 || len(session.TokenHash) != digestSize || session.CredentialEpoch < 1 || session.CreatedAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) {
		return ErrInvalidInput
	}
	tokenHash := bytes.Clone(session.TokenHash)

	const statement = `
INSERT INTO site_sessions (account_id, token_hash, credential_epoch, created_at, expires_at)
SELECT id, ?, credential_epoch, ?, ?
FROM streamer_accounts
WHERE id = ? AND credential_epoch = ? AND disabled_at IS NULL`
	result, err := repository.db.ExecContext(
		ctx,
		statement,
		tokenHash,
		session.CreatedAt,
		session.ExpiresAt,
		session.AccountID,
		session.CredentialEpoch,
	)
	if err != nil {
		return ErrRepositoryUnavailable
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ErrRepositoryUnavailable
	}
	if rows == 0 {
		return ErrNotFound
	}
	if rows != 1 {
		return ErrRepositoryUnavailable
	}
	return nil
}

func (repository *sqlRepository) FindSessionByHash(ctx context.Context, tokenHash []byte, now time.Time) (Session, error) {
	if !repository.ready() || len(tokenHash) != digestSize || now.IsZero() {
		return Session{}, ErrInvalidInput
	}
	tokenHash = bytes.Clone(tokenHash)

	const query = `
SELECT s.id, s.account_id, s.credential_epoch, s.expires_at,
       s.revoked_at, s.totp_verified_at, a.disabled_at, a.credential_epoch
FROM site_sessions AS s
JOIN streamer_accounts AS a ON a.id = s.account_id
WHERE s.token_hash = ?
LIMIT 1`

	var session Session
	var revokedAt sql.NullTime
	var totpVerifiedAt sql.NullTime
	var accountDisabledAt sql.NullTime
	var accountCredentialEpoch int64
	err := repository.db.QueryRowContext(ctx, query, tokenHash).Scan(
		&session.ID,
		&session.AccountID,
		&session.CredentialEpoch,
		&session.ExpiresAt,
		&revokedAt,
		&totpVerifiedAt,
		&accountDisabledAt,
		&accountCredentialEpoch,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, ErrRepositoryUnavailable
	}
	if session.ID <= 0 || session.AccountID <= 0 || session.CredentialEpoch < 1 || !session.ExpiresAt.After(now) || revokedAt.Valid || accountDisabledAt.Valid || session.CredentialEpoch != accountCredentialEpoch {
		return Session{}, ErrNotFound
	}
	if totpVerifiedAt.Valid {
		value := totpVerifiedAt.Time
		session.TOTPVerifiedAt = &value
	}
	return session, nil
}

func (repository *sqlRepository) RevokeSession(ctx context.Context, tokenHash []byte) error {
	if !repository.ready() || len(tokenHash) != digestSize {
		return ErrInvalidInput
	}
	tokenHash = bytes.Clone(tokenHash)

	_, err := repository.db.ExecContext(ctx,
		"UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP(6)) WHERE token_hash = ?",
		tokenHash,
	)
	if err != nil {
		return ErrRepositoryUnavailable
	}
	return nil
}

func (repository *sqlRepository) ready() bool {
	return repository != nil && repository.db != nil
}

func isDuplicateKey(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}
