package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"io"
	"math"
	"time"
)

const ownerTokenSize = 32

var ErrOwnershipConflict = errors.New("runtime: ownership conflict")

type OwnerToken [ownerTokenSize]byte

type OwnerFence struct {
	AccountID int64
	Token     OwnerToken
	Epoch     uint64
}

type OwnerClaim struct {
	Fence     OwnerFence
	Reconcile bool
}

func NewOwnerToken(random io.Reader) (OwnerToken, error) {
	var token OwnerToken
	if random == nil {
		return token, ErrInvalidInput
	}
	if _, err := io.ReadFull(random, token[:]); err != nil || token == (OwnerToken{}) {
		return OwnerToken{}, ErrInvalidInput
	}
	return token, nil
}

func (repository *SessionRepository) ClaimOwnership(ctx context.Context, accountID int64, token OwnerToken, ttl time.Duration) (OwnerClaim, error) {
	if !repository.ready() || ctx == nil || accountID <= 0 || token == (OwnerToken{}) || ttl <= 0 {
		return OwnerClaim{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return OwnerClaim{}, ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	if err := lockEnabledOwnerAccount(ctx, transaction, accountID); err != nil {
		return OwnerClaim{}, err
	}
	var currentToken []byte
	var currentEpoch uint64
	var expired bool
	err = transaction.QueryRowContext(ctx, "SELECT owner_token, fencing_epoch, expires_at <= UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE", accountID).Scan(&currentToken, &currentEpoch, &expired)
	claim := OwnerClaim{Fence: OwnerFence{AccountID: accountID, Token: token}}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		result, insertErr := transaction.ExecContext(ctx, "INSERT INTO runtime_account_owners (account_id, owner_token, fencing_epoch, expires_at) VALUES (?, ?, 1, DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND))", accountID, token[:], ttl.Microseconds())
		if insertErr != nil || !oneAffected(result) {
			return OwnerClaim{}, ErrUnavailable
		}
		claim.Fence.Epoch = 1
	case err != nil || len(currentToken) != ownerTokenSize || currentEpoch == 0:
		return OwnerClaim{}, ErrUnavailable
	case !expired && subtle.ConstantTimeCompare(currentToken, token[:]) == 1:
		result, renewErr := transaction.ExecContext(ctx, "UPDATE runtime_account_owners SET expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND) WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? AND expires_at > UTC_TIMESTAMP(6)", ttl.Microseconds(), accountID, token[:], currentEpoch)
		if renewErr != nil || !oneAffected(result) {
			return OwnerClaim{}, ErrOwnershipConflict
		}
		claim.Fence.Epoch = currentEpoch
	case !expired:
		return OwnerClaim{}, ErrOwnershipConflict
	case currentEpoch == math.MaxUint64:
		return OwnerClaim{}, ErrUnavailable
	default:
		result, takeoverErr := transaction.ExecContext(ctx, "UPDATE runtime_account_owners SET owner_token = ?, fencing_epoch = fencing_epoch + 1, expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND) WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? AND expires_at <= UTC_TIMESTAMP(6)", token[:], ttl.Microseconds(), accountID, currentToken, currentEpoch)
		if takeoverErr != nil || !oneAffected(result) {
			return OwnerClaim{}, ErrOwnershipConflict
		}
		claim.Fence.Epoch = currentEpoch + 1
		claim.Reconcile = true
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		return OwnerClaim{}, ErrUnavailable
	}
	finished = true
	return claim, nil
}

func (repository *SessionRepository) RenewOwnership(ctx context.Context, fence OwnerFence, ttl time.Duration) error {
	if !repository.ready() || ctx == nil || !validOwnerFence(fence) || ttl <= 0 {
		return ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	if err := lockEnabledOwnerAccount(ctx, transaction, fence.AccountID); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, "UPDATE runtime_account_owners SET expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND) WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? AND expires_at > UTC_TIMESTAMP(6)", ttl.Microseconds(), fence.AccountID, fence.Token[:], fence.Epoch)
	if err != nil {
		return ErrUnavailable
	}
	if !oneAffected(result) {
		return ErrOwnershipConflict
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		return ErrUnavailable
	}
	finished = true
	return nil
}

func (repository *SessionRepository) ReleaseOwnership(ctx context.Context, fence OwnerFence) error {
	if !repository.ready() || ctx == nil || !validOwnerFence(fence) {
		return ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	var lockedAccountID int64
	if err := transaction.QueryRowContext(ctx, "SELECT id FROM streamer_accounts WHERE id = ? FOR UPDATE", fence.AccountID).Scan(&lockedAccountID); err != nil || lockedAccountID != fence.AccountID {
		return ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "UPDATE runtime_account_owners SET expires_at = UTC_TIMESTAMP(6) WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ?", fence.AccountID, fence.Token[:], fence.Epoch)
	if err != nil {
		return ErrUnavailable
	}
	if !oneAffected(result) {
		return ErrOwnershipConflict
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		return ErrUnavailable
	}
	finished = true
	return nil
}

func lockEnabledOwnerAccount(ctx context.Context, transaction *sql.Tx, accountID int64) error {
	var enabled bool
	if err := transaction.QueryRowContext(ctx, "SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE", accountID).Scan(&enabled); err != nil {
		return ErrUnavailable
	}
	if !enabled {
		return ErrAccountDisabled
	}
	return nil
}

func validOwnerFence(fence OwnerFence) bool {
	return fence.AccountID > 0 && fence.Token != (OwnerToken{}) && fence.Epoch > 0
}
