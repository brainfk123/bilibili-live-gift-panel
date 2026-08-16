package identity

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	AccountStatusActive   = "active"
	AccountStatusDisabled = "disabled"

	recentAdministratorTOTPWindow  = 5 * time.Minute
	administratorClockSkew         = 30 * time.Second
	maximumAdministratorReasonSize = 512
)

var (
	ErrRecentTOTPRequired      = errors.New("identity: recent totp required")
	ErrAccountManagementFailed = errors.New("identity: account management failed")
)

// ManagedAccount is the deliberately small result of an administrator account
// mutation. Bilibili identity material is never part of this interface.
type ManagedAccount struct {
	AccountID int64  `json:"accountId"`
	Status    string `json:"status"`
}

type accountAdminRepository interface {
	authorizeAccountAdministrator(context.Context, []byte, time.Time) error
	disableAccount(context.Context, []byte, int64, string, time.Time) (ManagedAccount, error)
	enableAccount(context.Context, []byte, int64, string, time.Time) (ManagedAccount, error)
	rebindAccount(context.Context, []byte, int64, EncryptedUID, string, time.Time) (ManagedAccount, error)
}

// DisableAccount authenticates the administrator from the hash-only site
// session and delegates the complete mutation to one repository transaction.
func (service *Service) DisableAccount(ctx context.Context, administratorSession string, accountID int64, reason string) (ManagedAccount, error) {
	if service == nil || administratorSession == "" {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	normalizedReason, ok := normalizeAdministratorReason(reason)
	if accountID <= 0 || !ok {
		return ManagedAccount{}, ErrInvalidInput
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(administratorSession))
	if err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	repository, ok := service.repository.(accountAdminRepository)
	if !ok {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	return repository.disableAccount(ctx, tokenHash, accountID, normalizedReason, service.now())
}

// EnableAccount clears only the disabled marker. Revoked credentials and
// invitation state are intentionally outside this mutation.
func (service *Service) EnableAccount(ctx context.Context, administratorSession string, accountID int64, reason string) (ManagedAccount, error) {
	if service == nil || administratorSession == "" {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	normalizedReason, ok := normalizeAdministratorReason(reason)
	if accountID <= 0 || !ok {
		return ManagedAccount{}, ErrInvalidInput
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(administratorSession))
	if err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	repository, ok := service.repository.(accountAdminRepository)
	if !ok {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	return repository.enableAccount(ctx, tokenHash, accountID, normalizedReason, service.now())
}

// RebindVerifiedUID consumes a terminal Bilibili proof and stores only the
// encrypted UID and keyed lookup digest in the atomic repository mutation.
func (service *Service) RebindVerifiedUID(ctx context.Context, administratorSession string, accountID int64, challengeID, reason string) (ManagedAccount, error) {
	if service == nil || administratorSession == "" || challengeID == "" || len(challengeID) > 256 {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	normalizedReason, ok := normalizeAdministratorReason(reason)
	if accountID <= 0 || !ok {
		return ManagedAccount{}, ErrInvalidInput
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(administratorSession))
	if err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	repository, ok := service.repository.(accountAdminRepository)
	if !ok {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	now := service.now()
	if err := repository.authorizeAccountAdministrator(ctx, tokenHash, now); err != nil {
		return ManagedAccount{}, err
	}
	verification, err := service.verifier.Poll(ctx, challengeID)
	if errors.Is(err, ErrVerificationPending) || errors.Is(err, ErrVerificationUnavailable) {
		return ManagedAccount{}, err
	}
	service.verifier.Forget(challengeID)
	if err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	now = service.now()
	uid, valid := canonicalUID(verification.UID)
	if !valid || verification.CompletedAt.IsZero() || verification.CompletedAt.After(now.Add(time.Minute)) || verification.CompletedAt.Before(now.Add(-service.challengeTTL)) {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	lookup, err := service.keys.Lookup("bili_uid", []byte(uid))
	if err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	ciphertext, err := service.keys.Seal("bili_uid", []byte(uid))
	if err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
	defer clear(ciphertext)
	return repository.rebindAccount(ctx, tokenHash, accountID, EncryptedUID{Ciphertext: ciphertext, Lookup: lookup}, normalizedReason, now)
}

func (repository *sqlRepository) disableAccount(ctx context.Context, tokenHash []byte, accountID int64, reason string, now time.Time) (ManagedAccount, error) {
	if !repository.ready() || len(tokenHash) != digestSize || accountID <= 0 || now.IsZero() {
		return ManagedAccount{}, ErrInvalidInput
	}
	tokenHash = bytes.Clone(tokenHash)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := requireRecentAdministrator(ctx, transaction, tokenHash, now); err != nil {
		return ManagedAccount{}, err
	}

	const accountQuery = "SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE"
	var credentialEpoch int64
	var disabledAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, accountQuery, accountID).Scan(&credentialEpoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ManagedAccount{}, ErrAccountManagementFailed
		}
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if credentialEpoch < 1 || disabledAt.Valid {
		return ManagedAccount{}, ErrAccountManagementFailed
	}
	result, err := transaction.ExecContext(ctx,
		"UPDATE streamer_accounts SET disabled_at = ?, credential_epoch = credential_epoch + 1 WHERE id = ? AND disabled_at IS NULL",
		now, accountID,
	)
	if err != nil || !exactlyOneRow(result) {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?",
		now, accountID,
	); err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if err := insertAccountAudit(ctx, transaction, "streamer_account_disabled", accountID, reason, nil, nil, now); err != nil {
		return ManagedAccount{}, err
	}
	if err := transaction.Commit(); err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	committed = true
	return ManagedAccount{AccountID: accountID, Status: AccountStatusDisabled}, nil
}

func (repository *sqlRepository) enableAccount(ctx context.Context, tokenHash []byte, accountID int64, reason string, now time.Time) (ManagedAccount, error) {
	if !repository.ready() || len(tokenHash) != digestSize || accountID <= 0 || now.IsZero() {
		return ManagedAccount{}, ErrInvalidInput
	}
	tokenHash = bytes.Clone(tokenHash)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := requireRecentAdministrator(ctx, transaction, tokenHash, now); err != nil {
		return ManagedAccount{}, err
	}

	const accountQuery = "SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE"
	var credentialEpoch int64
	var disabledAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, accountQuery, accountID).Scan(&credentialEpoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ManagedAccount{}, ErrAccountManagementFailed
		}
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if credentialEpoch < 1 || !disabledAt.Valid {
		return ManagedAccount{}, ErrAccountManagementFailed
	}
	result, err := transaction.ExecContext(ctx,
		"UPDATE streamer_accounts SET disabled_at = NULL WHERE id = ? AND disabled_at IS NOT NULL",
		accountID,
	)
	if err != nil || !exactlyOneRow(result) {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if err := insertAccountAudit(ctx, transaction, "streamer_account_enabled", accountID, reason, nil, nil, now); err != nil {
		return ManagedAccount{}, err
	}
	if err := transaction.Commit(); err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	committed = true
	return ManagedAccount{AccountID: accountID, Status: AccountStatusActive}, nil
}

func (repository *sqlRepository) rebindAccount(ctx context.Context, tokenHash []byte, accountID int64, uid EncryptedUID, reason string, now time.Time) (ManagedAccount, error) {
	if !repository.ready() || len(tokenHash) != digestSize || accountID <= 0 || len(uid.Ciphertext) == 0 || len(uid.Ciphertext) > 512 || len(uid.Lookup) != digestSize || now.IsZero() {
		return ManagedAccount{}, ErrInvalidInput
	}
	tokenHash = bytes.Clone(tokenHash)
	uid = EncryptedUID{Ciphertext: bytes.Clone(uid.Ciphertext), Lookup: bytes.Clone(uid.Lookup)}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := requireRecentAdministrator(ctx, transaction, tokenHash, now); err != nil {
		return ManagedAccount{}, err
	}

	const accountQuery = "SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE"
	var credentialEpoch int64
	var disabledAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, accountQuery, accountID).Scan(&credentialEpoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ManagedAccount{}, ErrAccountManagementFailed
		}
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if credentialEpoch < 1 {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	const bindingQuery = "SELECT uid_lookup FROM bili_uid_bindings WHERE account_id = ? AND unbound_at IS NULL FOR UPDATE"
	var oldLookup []byte
	if err := transaction.QueryRowContext(ctx, bindingQuery, accountID).Scan(&oldLookup); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ManagedAccount{}, ErrAccountManagementFailed
		}
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if len(oldLookup) != digestSize {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if subtle.ConstantTimeCompare(oldLookup, uid.Lookup) == 1 {
		return ManagedAccount{}, ErrAccountManagementFailed
	}
	const duplicateQuery = "SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE"
	var existingAccountID int64
	err = transaction.QueryRowContext(ctx, duplicateQuery, uid.Lookup).Scan(&existingAccountID)
	switch {
	case err == nil:
		return ManagedAccount{}, ErrAccountManagementFailed
	case !errors.Is(err, sql.ErrNoRows):
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	result, err := transaction.ExecContext(ctx,
		"UPDATE bili_uid_bindings SET unbound_at = ? WHERE account_id = ? AND unbound_at IS NULL",
		now, accountID,
	)
	if err != nil || !exactlyOneRow(result) {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	result, err = transaction.ExecContext(ctx,
		"INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)",
		accountID, uid.Ciphertext, uid.Lookup, now,
	)
	if err != nil {
		if isDuplicateKey(err) {
			return ManagedAccount{}, ErrAccountManagementFailed
		}
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if !exactlyOneRow(result) {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	result, err = transaction.ExecContext(ctx,
		"UPDATE streamer_accounts SET credential_epoch = credential_epoch + 1 WHERE id = ?",
		accountID,
	)
	if err != nil || !exactlyOneRow(result) {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE site_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE account_id = ?",
		now, accountID,
	); err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	if err := insertAccountAudit(ctx, transaction, "streamer_account_uid_rebound", accountID, reason, oldLookup, uid.Lookup, now); err != nil {
		return ManagedAccount{}, err
	}
	if err := transaction.Commit(); err != nil {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	committed = true
	status := AccountStatusActive
	if disabledAt.Valid {
		status = AccountStatusDisabled
	}
	return ManagedAccount{AccountID: accountID, Status: status}, nil
}

func (repository *sqlRepository) authorizeAccountAdministrator(ctx context.Context, tokenHash []byte, now time.Time) error {
	if !repository.ready() || len(tokenHash) != digestSize || now.IsZero() {
		return ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := requireRecentAdministrator(ctx, transaction, bytes.Clone(tokenHash), now); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrRepositoryUnavailable
	}
	committed = true
	return nil
}

func requireRecentAdministrator(ctx context.Context, transaction *sql.Tx, tokenHash []byte, now time.Time) error {
	const query = `
SELECT s.id, s.credential_epoch, s.expires_at, s.revoked_at, s.totp_verified_at, a.credential_epoch
FROM admin_identity AS a
JOIN site_sessions AS s ON s.admin_identity_id = a.id
WHERE a.id = 1 AND s.token_hash = ?
LIMIT 1
FOR UPDATE`
	var sessionID, sessionEpoch, administratorEpoch int64
	var expiresAt time.Time
	var revokedAt, verifiedAt sql.NullTime
	err := transaction.QueryRowContext(ctx, query, tokenHash).Scan(
		&sessionID, &sessionEpoch, &expiresAt, &revokedAt, &verifiedAt, &administratorEpoch,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthenticationFailed
	}
	if err != nil {
		return ErrRepositoryUnavailable
	}
	if sessionID <= 0 || sessionEpoch < 1 || administratorEpoch < 1 || sessionEpoch != administratorEpoch || !expiresAt.After(now) || revokedAt.Valid {
		return ErrAuthenticationFailed
	}
	if !verifiedAt.Valid || verifiedAt.Time.After(now.Add(administratorClockSkew)) || now.Sub(verifiedAt.Time) > recentAdministratorTOTPWindow {
		return ErrRecentTOTPRequired
	}
	return nil
}

func insertAccountAudit(ctx context.Context, transaction *sql.Tx, eventType string, accountID int64, reason string, oldLookup, newLookup []byte, now time.Time) error {
	event := struct {
		Reason       string `json:"reason"`
		OldUIDLookup []byte `json:"oldUidLookup,omitempty"`
		NewUIDLookup []byte `json:"newUidLookup,omitempty"`
	}{Reason: reason, OldUIDLookup: oldLookup, NewUIDLookup: newLookup}
	data, err := json.Marshal(event)
	if err != nil {
		return ErrRepositoryUnavailable
	}
	result, err := transaction.ExecContext(ctx,
		"INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)",
		eventType, int64(1), accountID, data, now,
	)
	if err != nil || !exactlyOneRow(result) {
		return ErrRepositoryUnavailable
	}
	return nil
}

func normalizeAdministratorReason(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximumAdministratorReasonSize || !utf8.ValidString(value) {
		return "", false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return value, true
}

func exactlyOneRow(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}
