// Package obs owns long-lived OBS credentials, their one-shot fragment
// exchange, and account-scoped short sessions. Plaintext credentials never
// cross the SQL boundary.
package obs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"regexp"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"
)

const shortSessionTTL = 12 * time.Hour

var (
	ErrInvalidInput         = errors.New("obs: invalid input")
	ErrAuthenticationFailed = errors.New("obs: authentication failed")
	ErrRecentTOTPRequired   = errors.New("obs: recent totp required")
	ErrAccountDisabled      = errors.New("obs: account disabled")
	ErrUnavailable          = errors.New("obs: unavailable")
)

var publicIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type administratorAuthorizer interface {
	security.SessionValidator
}

type ServiceOptions struct {
	Now          func() time.Time
	Random       io.Reader
	PublicOrigin string
}

type Service struct {
	database     *sql.DB
	admin        administratorAuthorizer
	now          func() time.Time
	random       io.Reader
	publicOrigin string
}

// IssuedCredential deliberately exposes the long token only inside URL. The
// value is returned once and never reconstructed from storage.
type IssuedCredential struct {
	PublicID string `json:"publicId"`
	URL      string `json:"url"`
}

type ShortSession struct {
	Token     string
	AccountID int64
	ExpiresAt time.Time
}

func NewService(database *sql.DB, admin administratorAuthorizer, options ServiceOptions) (*Service, error) {
	if database == nil || admin == nil {
		return nil, ErrInvalidInput
	}
	origin, err := url.Parse(options.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Service{database: database, admin: admin, now: options.Now, random: options.Random, publicOrigin: options.PublicOrigin}, nil
}

// Issue creates or resets the account's single active long credential.
func (service *Service) Issue(ctx context.Context, administratorToken string, accountID int64) (IssuedCredential, error) {
	if service == nil || ctx == nil || administratorToken == "" || accountID <= 0 {
		return IssuedCredential{}, ErrInvalidInput
	}
	if err := service.admin.RequireSession(ctx, administratorToken); err != nil {
		return IssuedCredential{}, ErrAuthenticationFailed
	}
	transaction, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return IssuedCredential{}, ErrUnavailable
	}
	defer transaction.Rollback()
	authorizedAt := service.now().UTC()
	publicBytes, publicID, err := service.randomToken()
	if err != nil {
		return IssuedCredential{}, err
	}
	defer wipe(publicBytes)
	longBytes, longToken, err := service.randomToken()
	if err != nil {
		return IssuedCredential{}, err
	}
	defer wipe(longBytes)
	tokenHash := sha256.Sum256([]byte(longToken))
	now := authorizedAt
	var epoch int64
	var disabledAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, "SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE", accountID).Scan(&epoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssuedCredential{}, ErrInvalidInput
		}
		return IssuedCredential{}, ErrUnavailable
	}
	if disabledAt.Valid {
		return IssuedCredential{}, ErrAccountDisabled
	}
	if epoch < 1 {
		return IssuedCredential{}, ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE obs_sessions AS s JOIN obs_credentials AS c ON c.id = s.obs_credential_id SET s.revoked_at = ? WHERE c.account_id = ? AND s.revoked_at IS NULL", now, accountID); err != nil {
		return IssuedCredential{}, ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE obs_credentials SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL", now, accountID); err != nil {
		return IssuedCredential{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "INSERT INTO obs_credentials (account_id, public_id, token_hash, credential_epoch, created_at) VALUES (?, ?, ?, ?, ?)", accountID, publicID, tokenHash[:], epoch, now)
	if err != nil || !oneRow(result) {
		return IssuedCredential{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx,
		"INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)",
		"obs_credential_reset", int64(1), accountID, []byte("{}"), now,
	)
	if err != nil || !oneRow(result) {
		return IssuedCredential{}, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return IssuedCredential{}, ErrUnavailable
	}
	return IssuedCredential{PublicID: publicID, URL: service.publicOrigin + "/obs/" + publicID + "#token=" + url.QueryEscape(longToken)}, nil
}

func (service *Service) Exchange(ctx context.Context, publicID, longToken string) (ShortSession, error) {
	if service == nil || ctx == nil || !validPublicID(publicID) || !validToken(longToken) {
		return ShortSession{}, ErrAuthenticationFailed
	}
	longHash := sha256.Sum256([]byte(longToken))
	now := service.now().UTC()
	transaction, err := service.database.BeginTx(ctx, nil)
	if err != nil {
		return ShortSession{}, ErrUnavailable
	}
	defer transaction.Rollback()
	var credentialID, accountID, credentialEpoch, accountEpoch int64
	var storedHash []byte
	var credentialRevokedAt, disabledAt sql.NullTime
	const query = `SELECT c.id, c.account_id, c.token_hash, c.credential_epoch, c.revoked_at, a.credential_epoch, a.disabled_at
FROM obs_credentials AS c
JOIN streamer_accounts AS a ON a.id = c.account_id
WHERE c.public_id = ?
FOR UPDATE`
	if err := transaction.QueryRowContext(ctx, query, publicID).Scan(&credentialID, &accountID, &storedHash, &credentialEpoch, &credentialRevokedAt, &accountEpoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ShortSession{}, ErrAuthenticationFailed
		}
		return ShortSession{}, ErrUnavailable
	}
	if credentialRevokedAt.Valid || disabledAt.Valid || credentialEpoch < 1 || credentialEpoch != accountEpoch || len(storedHash) != sha256.Size || subtle.ConstantTimeCompare(storedHash, longHash[:]) != 1 {
		return ShortSession{}, ErrAuthenticationFailed
	}
	shortBytes, shortToken, err := service.randomToken()
	if err != nil {
		return ShortSession{}, err
	}
	defer wipe(shortBytes)
	shortHash := sha256.Sum256([]byte(shortToken))
	expiresAt := now.Add(shortSessionTTL)
	result, err := transaction.ExecContext(ctx, "INSERT INTO obs_sessions (obs_credential_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, ?, ?, ?)", credentialID, shortHash[:], credentialEpoch, now, expiresAt)
	if err != nil || !oneRow(result) {
		return ShortSession{}, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return ShortSession{}, ErrUnavailable
	}
	return ShortSession{Token: shortToken, AccountID: accountID, ExpiresAt: expiresAt}, nil
}

func (service *Service) Authenticate(ctx context.Context, publicID, shortToken string) (int64, error) {
	if service == nil || ctx == nil || !validPublicID(publicID) || !validToken(shortToken) {
		return 0, ErrAuthenticationFailed
	}
	tokenHash := sha256.Sum256([]byte(shortToken))
	const query = `SELECT c.public_id, c.account_id, s.credential_epoch, c.credential_epoch, s.expires_at, s.revoked_at, c.revoked_at, a.credential_epoch, a.disabled_at
FROM obs_sessions AS s
JOIN obs_credentials AS c ON c.id = s.obs_credential_id
JOIN streamer_accounts AS a ON a.id = c.account_id
WHERE s.token_hash = ?
LIMIT 1`
	var storedPublicID string
	var accountID, sessionEpoch, credentialEpoch, accountEpoch int64
	var expiresAt time.Time
	var sessionRevokedAt, credentialRevokedAt, disabledAt sql.NullTime
	if err := service.database.QueryRowContext(ctx, query, tokenHash[:]).Scan(&storedPublicID, &accountID, &sessionEpoch, &credentialEpoch, &expiresAt, &sessionRevokedAt, &credentialRevokedAt, &accountEpoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrAuthenticationFailed
		}
		return 0, ErrUnavailable
	}
	if subtle.ConstantTimeCompare([]byte(storedPublicID), []byte(publicID)) != 1 || accountID <= 0 || sessionEpoch < 1 || sessionEpoch != credentialEpoch || sessionEpoch != accountEpoch || !expiresAt.After(service.now().UTC()) || sessionRevokedAt.Valid || credentialRevokedAt.Valid || disabledAt.Valid {
		return 0, ErrAuthenticationFailed
	}
	return accountID, nil
}

func (service *Service) randomToken() ([]byte, string, error) {
	buffer := make([]byte, 32)
	if _, err := io.ReadFull(service.random, buffer); err != nil {
		wipe(buffer)
		return nil, "", ErrUnavailable
	}
	return buffer, base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validPublicID(value string) bool { return publicIDPattern.MatchString(value) }
func validToken(value string) bool    { return len(value) >= 20 && len(value) <= 512 }

func oneRow(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
