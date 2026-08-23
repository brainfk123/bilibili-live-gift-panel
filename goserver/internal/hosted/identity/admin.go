package identity

import (
	"context"
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
	disableAccount(context.Context, string, int64, string, func() time.Time) (ManagedAccount, error)
	enableAccount(context.Context, string, int64, string, func() time.Time) (ManagedAccount, error)
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
	repository, ok := service.repository.(accountAdminRepository)
	if !ok {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	result, err := repository.disableAccount(ctx, administratorSession, accountID, normalizedReason, service.now)
	if err != nil {
		return ManagedAccount{}, err
	}
	if service.onAccountDisabled != nil {
		service.onAccountDisabled(result.AccountID)
	}
	return result, nil
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
	repository, ok := service.repository.(accountAdminRepository)
	if !ok {
		return ManagedAccount{}, ErrRepositoryUnavailable
	}
	return repository.enableAccount(ctx, administratorSession, accountID, normalizedReason, service.now)
}

func (repository *sqlRepository) disableAccount(ctx context.Context, sessionToken string, accountID int64, reason string, clock func() time.Time) (ManagedAccount, error) {
	if !repository.ready() || repository.administrator == nil || sessionToken == "" || accountID <= 0 || clock == nil {
		return ManagedAccount{}, ErrInvalidInput
	}
	if err := repository.administrator.RequireSession(ctx, sessionToken); err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
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
	now := clock().UTC()
	disabledAt, err := lockManagedAccount(ctx, transaction, accountID)
	if err != nil {
		return ManagedAccount{}, err
	}
	if disabledAt.Valid {
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

func (repository *sqlRepository) enableAccount(ctx context.Context, sessionToken string, accountID int64, reason string, clock func() time.Time) (ManagedAccount, error) {
	if !repository.ready() || repository.administrator == nil || sessionToken == "" || accountID <= 0 || clock == nil {
		return ManagedAccount{}, ErrInvalidInput
	}
	if err := repository.administrator.RequireSession(ctx, sessionToken); err != nil {
		return ManagedAccount{}, ErrAuthenticationFailed
	}
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
	now := clock().UTC()
	disabledAt, err := lockManagedAccount(ctx, transaction, accountID)
	if err != nil {
		return ManagedAccount{}, err
	}
	if !disabledAt.Valid {
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

func lockManagedAccount(ctx context.Context, transaction *sql.Tx, accountID int64) (sql.NullTime, error) {
	const query = "SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE"
	var credentialEpoch int64
	var disabledAt sql.NullTime
	err := transaction.QueryRowContext(ctx, query, accountID).Scan(&credentialEpoch, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullTime{}, ErrAccountManagementFailed
	}
	if err != nil || credentialEpoch < 1 {
		return sql.NullTime{}, ErrRepositoryUnavailable
	}
	return disabledAt, nil
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
