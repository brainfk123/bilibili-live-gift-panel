package invitation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"
	"github.com/go-sql-driver/mysql"
)

const (
	digestSize                    = sha256.Size
	recentAdministratorTOTPWindow = 5 * time.Minute
	administratorClockSkew        = 30 * time.Second
)

var (
	ErrInvalidInput       = errors.New("invitation: invalid input")
	ErrAuthentication     = errors.New("invitation: authentication failed")
	ErrRecentTOTPRequired = errors.New("invitation: recent totp required")
	ErrQuotaExhausted     = errors.New("invitation: quota exhausted")
	ErrInvitationInvalid  = errors.New("invitation: invitation invalid")
	ErrUnavailable        = errors.New("invitation: unavailable")
)

type registrationIntentSource interface {
	ReserveRegistrationIntent(string) (identity.RegistrationIntentReservation, error)
}

type ServiceOptions struct {
	Now           func() time.Time
	InvitationTTL time.Duration
	SessionTTL    time.Duration
}

type Service struct {
	db            *sql.DB
	keys          security.Keyring
	intents       registrationIntentSource
	now           func() time.Time
	invitationTTL time.Duration
	sessionTTL    time.Duration
}

func NewService(db *sql.DB, keys security.Keyring, intents registrationIntentSource, options ServiceOptions) (*Service, error) {
	if db == nil || intents == nil {
		return nil, ErrInvalidInput
	}
	if _, err := keys.HashToken("site_session", []byte("constructor-check")); err != nil {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.InvitationTTL == 0 {
		options.InvitationTTL = 7 * 24 * time.Hour
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = 24 * time.Hour
	}
	if options.InvitationTTL <= 0 || options.InvitationTTL > 30*24*time.Hour || options.SessionTTL <= 0 {
		return nil, ErrInvalidInput
	}
	return &Service{db: db, keys: keys, intents: intents, now: options.Now, invitationTTL: options.InvitationTTL, sessionTTL: options.SessionTTL}, nil
}

func (service *Service) Generate(ctx context.Context, sessionToken string, actor ActorKind) (GeneratedInvitation, error) {
	if service == nil || sessionToken == "" || (actor != ActorStreamer && actor != ActorAdministrator) {
		return GeneratedInvitation{}, ErrInvalidInput
	}
	code, err := service.keys.NewToken()
	if err != nil || len(code) < 4 {
		return GeneratedInvitation{}, ErrUnavailable
	}
	codeDigest := sha256.Sum256([]byte(code))
	hint := code[len(code)-4:]
	now := service.now()
	expiresAt := now.Add(service.invitationTTL)

	purpose := "site_session"
	if actor == ActorAdministrator {
		purpose = "admin_session"
	}
	tokenHash, err := service.keys.HashToken(purpose, []byte(sessionToken))
	if err != nil {
		return GeneratedInvitation{}, ErrAuthentication
	}
	transaction, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return GeneratedInvitation{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var invitationID int64
	var remaining uint64
	switch actor {
	case ActorStreamer:
		accountID, _, err := requireCurrentStreamer(ctx, transaction, tokenHash, now)
		if err != nil {
			return GeneratedInvitation{}, err
		}
		if err := transaction.QueryRowContext(ctx, "SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE", accountID).Scan(&remaining); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return GeneratedInvitation{}, ErrQuotaExhausted
			}
			return GeneratedInvitation{}, ErrUnavailable
		}
		if remaining == 0 {
			return GeneratedInvitation{}, ErrQuotaExhausted
		}
		result, err := transaction.ExecContext(ctx,
			"INSERT INTO invitations (code_hash, code_hint, creator_account_id, status, created_at, expires_at) VALUES (?, ?, ?, 'active', ?, ?)",
			codeDigest[:], hint, accountID, now, expiresAt,
		)
		if err != nil {
			return GeneratedInvitation{}, ErrUnavailable
		}
		invitationID, err = positiveInsertID(result)
		if err != nil {
			return GeneratedInvitation{}, err
		}
		remaining--
		if result, err = transaction.ExecContext(ctx, "UPDATE invitation_quotas SET remaining_quota = ?, updated_at = ? WHERE account_id = ?", remaining, now, accountID); err != nil || !oneRow(result) {
			return GeneratedInvitation{}, ErrUnavailable
		}
		if result, err = transaction.ExecContext(ctx,
			"INSERT INTO invitation_quota_events (account_id, invitation_id, quota_delta, quota_after, reason, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			accountID, invitationID, int64(-1), remaining, "invitation_generated", now,
		); err != nil || !oneRow(result) {
			return GeneratedInvitation{}, ErrUnavailable
		}
		if err := insertAudit(ctx, transaction, "invitation_generated", accountID, 0, 0, invitationID, now, nil); err != nil {
			return GeneratedInvitation{}, err
		}
	case ActorAdministrator:
		if err := requireRecentAdministrator(ctx, transaction, tokenHash, now); err != nil {
			return GeneratedInvitation{}, err
		}
		result, err := transaction.ExecContext(ctx,
			"INSERT INTO invitations (code_hash, code_hint, creator_admin_identity_id, status, created_at, expires_at) VALUES (?, ?, 1, 'active', ?, ?)",
			codeDigest[:], hint, now, expiresAt,
		)
		if err != nil {
			return GeneratedInvitation{}, ErrUnavailable
		}
		invitationID, err = positiveInsertID(result)
		if err != nil {
			return GeneratedInvitation{}, err
		}
		if err := insertAudit(ctx, transaction, "invitation_generated", 0, 1, 0, invitationID, now, nil); err != nil {
			return GeneratedInvitation{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return GeneratedInvitation{}, ErrUnavailable
	}
	committed = true
	return GeneratedInvitation{
		Invitation: Invitation{ID: invitationID, CodeHint: "****" + hint, Status: StatusActive, CreatedAt: now, ExpiresAt: expiresAt},
		Code:       code, RemainingQuota: remaining,
	}, nil
}

func (service *Service) AdjustQuota(ctx context.Context, administratorSession string, accountID int64, remaining uint64, reason string) (Quota, error) {
	normalizedReason, valid := normalizeReason(reason)
	if service == nil || administratorSession == "" || accountID <= 0 || !valid {
		return Quota{}, ErrInvalidInput
	}
	tokenHash, err := service.keys.HashToken("admin_session", []byte(administratorSession))
	if err != nil {
		return Quota{}, ErrAuthentication
	}
	now := service.now()
	transaction, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return Quota{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := requireRecentAdministrator(ctx, transaction, tokenHash, now); err != nil {
		return Quota{}, err
	}
	var credentialEpoch int64
	if err := transaction.QueryRowContext(ctx, "SELECT credential_epoch FROM streamer_accounts WHERE id = ? FOR UPDATE", accountID).Scan(&credentialEpoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Quota{}, ErrInvitationInvalid
		}
		return Quota{}, ErrUnavailable
	}
	if credentialEpoch < 1 {
		return Quota{}, ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, ?) ON DUPLICATE KEY UPDATE account_id = account_id",
		accountID, now,
	); err != nil {
		return Quota{}, ErrUnavailable
	}
	var before uint64
	if err := transaction.QueryRowContext(ctx, "SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE", accountID).Scan(&before); err != nil {
		return Quota{}, ErrUnavailable
	}
	delta, ok := signedDelta(before, remaining)
	if !ok || delta == 0 {
		return Quota{}, ErrInvalidInput
	}
	result, err := transaction.ExecContext(ctx, "UPDATE invitation_quotas SET remaining_quota = ?, updated_at = ? WHERE account_id = ?", remaining, now, accountID)
	if err != nil || !oneRow(result) {
		return Quota{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx,
		"INSERT INTO invitation_quota_events (account_id, actor_admin_identity_id, quota_delta, quota_after, reason, created_at) VALUES (?, 1, ?, ?, ?, ?)",
		accountID, delta, remaining, normalizedReason, now,
	)
	if err != nil || !oneRow(result) {
		return Quota{}, ErrUnavailable
	}
	if err := insertAudit(ctx, transaction, "invitation_quota_adjusted", 0, 1, accountID, 0, now, &quotaAudit{Reason: normalizedReason, Before: before, After: remaining, Delta: delta}); err != nil {
		return Quota{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Quota{}, ErrUnavailable
	}
	committed = true
	return Quota{AccountID: accountID, RemainingQuota: remaining}, nil
}

func (service *Service) Revoke(ctx context.Context, streamerSession string, invitationID int64) error {
	if service == nil || streamerSession == "" || invitationID <= 0 {
		return ErrInvalidInput
	}
	tokenHash, err := service.keys.HashToken("site_session", []byte(streamerSession))
	if err != nil {
		return ErrAuthentication
	}
	now := service.now()
	transaction, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	accountID, _, err := requireCurrentStreamer(ctx, transaction, tokenHash, now)
	if err != nil {
		return err
	}
	var status string
	var expiresAt time.Time
	if err := transaction.QueryRowContext(ctx, "SELECT status, expires_at FROM invitations WHERE id = ? AND creator_account_id = ? FOR UPDATE", invitationID, accountID).Scan(&status, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvitationInvalid
		}
		return ErrUnavailable
	}
	if status != StatusActive || !now.Before(expiresAt) {
		return ErrInvitationInvalid
	}
	result, err := transaction.ExecContext(ctx, "UPDATE invitations SET status = 'revoked', revoked_at = ? WHERE id = ? AND status = 'active'", now, invitationID)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if err := insertAudit(ctx, transaction, "invitation_revoked", accountID, 0, 0, invitationID, now, nil); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (service *Service) Expire(ctx context.Context, invitationID int64) error {
	if service == nil || invitationID <= 0 {
		return ErrInvalidInput
	}
	now := service.now()
	transaction, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var status string
	var expiresAt time.Time
	if err := transaction.QueryRowContext(ctx, "SELECT status, expires_at FROM invitations WHERE id = ? FOR UPDATE", invitationID).Scan(&status, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvitationInvalid
		}
		return ErrUnavailable
	}
	if status != StatusActive || now.Before(expiresAt) {
		return ErrInvitationInvalid
	}
	result, err := transaction.ExecContext(ctx, "UPDATE invitations SET status = 'expired' WHERE id = ? AND status = 'active'", invitationID)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if err := insertAudit(ctx, transaction, "invitation_expired", 0, 0, 0, invitationID, now, nil); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (service *Service) List(ctx context.Context, streamerSession string) (InvitationList, error) {
	if service == nil || streamerSession == "" {
		return InvitationList{}, ErrInvalidInput
	}
	tokenHash, err := service.keys.HashToken("site_session", []byte(streamerSession))
	if err != nil {
		return InvitationList{}, ErrAuthentication
	}
	now := service.now()
	transaction, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return InvitationList{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	accountID, _, err := requireCurrentStreamer(ctx, transaction, tokenHash, now)
	if err != nil {
		return InvitationList{}, err
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE invitations SET status = 'expired' WHERE creator_account_id = ? AND status = 'active' AND expires_at <= ?", accountID, now); err != nil {
		return InvitationList{}, ErrUnavailable
	}
	var remaining uint64
	if err := transaction.QueryRowContext(ctx, "SELECT remaining_quota FROM invitation_quotas WHERE account_id = ?", accountID).Scan(&remaining); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return InvitationList{}, ErrUnavailable
	}
	rows, err := transaction.QueryContext(ctx, "SELECT id, code_hint, status, created_at, expires_at, revoked_at, used_at FROM invitations WHERE creator_account_id = ? ORDER BY id DESC", accountID)
	if err != nil {
		return InvitationList{}, ErrUnavailable
	}
	defer rows.Close()
	result := InvitationList{RemainingQuota: remaining, Invitations: make([]Invitation, 0)}
	for rows.Next() {
		var item Invitation
		var hint string
		var revokedAt, usedAt sql.NullTime
		if err := rows.Scan(&item.ID, &hint, &item.Status, &item.CreatedAt, &item.ExpiresAt, &revokedAt, &usedAt); err != nil || item.ID <= 0 || len(hint) != 4 || !validStatus(item.Status) {
			return InvitationList{}, ErrUnavailable
		}
		item.CodeHint = "****" + hint
		if revokedAt.Valid {
			value := revokedAt.Time
			item.RevokedAt = &value
		}
		if usedAt.Valid {
			value := usedAt.Time
			item.UsedAt = &value
		}
		result.Invitations = append(result.Invitations, item)
	}
	if err := rows.Err(); err != nil {
		return InvitationList{}, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return InvitationList{}, ErrUnavailable
	}
	committed = true
	return result, nil
}

func (service *Service) Redeem(ctx context.Context, code, registrationIntent string) (identity.SiteSession, error) {
	if service == nil || code == "" || len(code) > 128 || registrationIntent == "" || len(registrationIntent) > 512 {
		return identity.SiteSession{}, ErrInvalidInput
	}
	reservation, err := service.intents.ReserveRegistrationIntent(registrationIntent)
	if err != nil || reservation == nil {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	defer reservation.Abort()
	uid, intentExpiresAt, ok := reservation.Identity()
	if !ok || len(uid.Ciphertext) == 0 || len(uid.Ciphertext) > 512 || len(uid.Lookup) != digestSize {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	defer destroyUID(&uid)
	now := service.now()
	if !now.Before(intentExpiresAt) {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	siteToken, err := service.keys.NewToken()
	if err != nil {
		return identity.SiteSession{}, ErrUnavailable
	}
	siteHash, err := service.keys.HashToken("site_session", []byte(siteToken))
	if err != nil {
		return identity.SiteSession{}, ErrUnavailable
	}
	codeHash := sha256.Sum256([]byte(code))
	transaction, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.SiteSession{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var invitationID int64
	var status string
	var expiresAt time.Time
	if err := transaction.QueryRowContext(ctx, "SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE", codeHash[:]).Scan(&invitationID, &status, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.SiteSession{}, ErrInvitationInvalid
		}
		return identity.SiteSession{}, ErrUnavailable
	}
	if invitationID <= 0 || status != StatusActive || !now.Before(expiresAt) {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	var existingAccountID int64
	err = transaction.QueryRowContext(ctx, "SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE", uid.Lookup).Scan(&existingAccountID)
	if err == nil {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return identity.SiteSession{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "INSERT INTO streamer_accounts (credential_epoch, created_at, updated_at) VALUES (1, ?, ?)", now, now)
	if err != nil {
		return identity.SiteSession{}, ErrUnavailable
	}
	accountID, err := positiveInsertID(result)
	if err != nil {
		return identity.SiteSession{}, err
	}
	result, err = transaction.ExecContext(ctx, "INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)", accountID, uid.Ciphertext, uid.Lookup, now)
	if err != nil {
		if duplicateKey(err) {
			return identity.SiteSession{}, ErrInvitationInvalid
		}
		return identity.SiteSession{}, ErrUnavailable
	}
	if !oneRow(result) {
		return identity.SiteSession{}, ErrUnavailable
	}
	if result, err = transaction.ExecContext(ctx, "INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, ?)", accountID, now); err != nil || !oneRow(result) {
		return identity.SiteSession{}, ErrUnavailable
	}
	sessionExpiresAt := now.Add(service.sessionTTL)
	if result, err = transaction.ExecContext(ctx, "INSERT INTO site_sessions (account_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, 1, ?, ?)", accountID, siteHash, now, sessionExpiresAt); err != nil || !oneRow(result) {
		return identity.SiteSession{}, ErrUnavailable
	}
	if result, err = transaction.ExecContext(ctx, "UPDATE invitations SET status = 'used', used_at = ?, invited_account_id = ? WHERE id = ? AND status = 'active'", now, accountID, invitationID); err != nil || !oneRow(result) {
		return identity.SiteSession{}, ErrUnavailable
	}
	if err := insertAudit(ctx, transaction, "invitation_redeemed", 0, 0, accountID, invitationID, now, nil); err != nil {
		return identity.SiteSession{}, err
	}
	if !reservation.Valid() || !service.now().Before(intentExpiresAt) {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	if err := transaction.Commit(); err != nil {
		return identity.SiteSession{}, ErrUnavailable
	}
	committed = true
	reservation.Commit()
	return identity.SiteSession{Token: siteToken, AccountID: accountID, ExpiresAt: sessionExpiresAt}, nil
}

func requireCurrentStreamer(ctx context.Context, transaction *sql.Tx, tokenHash []byte, now time.Time) (int64, int64, error) {
	var accountID int64
	if err := transaction.QueryRowContext(ctx, "SELECT account_id FROM site_sessions WHERE token_hash = ? AND account_id IS NOT NULL LIMIT 1", tokenHash).Scan(&accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrAuthentication
		}
		return 0, 0, ErrUnavailable
	}
	if accountID <= 0 {
		return 0, 0, ErrUnavailable
	}
	var accountEpoch int64
	var disabledAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, "SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE", accountID).Scan(&accountEpoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrAuthentication
		}
		return 0, 0, ErrUnavailable
	}
	if accountEpoch < 1 {
		return 0, 0, ErrUnavailable
	}
	if disabledAt.Valid {
		return 0, 0, ErrAuthentication
	}
	var sessionID, sessionEpoch int64
	var expiresAt time.Time
	var revokedAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, "SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE account_id = ? AND token_hash = ? FOR UPDATE", accountID, tokenHash).Scan(&sessionID, &sessionEpoch, &expiresAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrAuthentication
		}
		return 0, 0, ErrUnavailable
	}
	if sessionID <= 0 || sessionEpoch < 1 || sessionEpoch != accountEpoch || !expiresAt.After(now) || revokedAt.Valid {
		return 0, 0, ErrAuthentication
	}
	return accountID, accountEpoch, nil
}

func requireRecentAdministrator(ctx context.Context, transaction *sql.Tx, tokenHash []byte, now time.Time) error {
	var administratorEpoch int64
	err := transaction.QueryRowContext(ctx, "SELECT credential_epoch FROM admin_identity WHERE id = 1 FOR UPDATE").Scan(&administratorEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthentication
	}
	if err != nil || administratorEpoch < 1 {
		return ErrUnavailable
	}
	var sessionID, sessionEpoch int64
	var expiresAt time.Time
	var revokedAt, verifiedAt sql.NullTime
	err = transaction.QueryRowContext(ctx, "SELECT id, credential_epoch, expires_at, revoked_at, totp_verified_at FROM site_sessions WHERE admin_identity_id = 1 AND token_hash = ? FOR UPDATE", tokenHash).
		Scan(&sessionID, &sessionEpoch, &expiresAt, &revokedAt, &verifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthentication
	}
	if err != nil {
		return ErrUnavailable
	}
	if sessionID <= 0 || sessionEpoch < 1 || sessionEpoch != administratorEpoch || !expiresAt.After(now) || revokedAt.Valid {
		return ErrAuthentication
	}
	if !verifiedAt.Valid || verifiedAt.Time.After(now.Add(administratorClockSkew)) || now.Sub(verifiedAt.Time) > recentAdministratorTOTPWindow {
		return ErrRecentTOTPRequired
	}
	return nil
}

type quotaAudit struct {
	Reason string `json:"reason"`
	Before uint64 `json:"before"`
	After  uint64 `json:"after"`
	Delta  int64  `json:"delta"`
}

func insertAudit(ctx context.Context, transaction *sql.Tx, eventType string, actorAccountID, actorAdminID, targetAccountID, invitationID int64, now time.Time, quota *quotaAudit) error {
	event := struct {
		InvitationID int64       `json:"invitationId,omitempty"`
		Quota        *quotaAudit `json:"quota,omitempty"`
	}{InvitationID: invitationID, Quota: quota}
	encoded, err := json.Marshal(event)
	if err != nil {
		return ErrUnavailable
	}
	var result sql.Result
	switch {
	case actorAccountID > 0:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, actor_account_id, event_data, created_at) VALUES (?, ?, ?, ?)", eventType, actorAccountID, encoded, now)
	case actorAdminID > 0 && targetAccountID > 0:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, 1, ?, ?, ?)", eventType, targetAccountID, encoded, now)
	case actorAdminID > 0:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES (?, 1, ?, ?)", eventType, encoded, now)
	case targetAccountID > 0:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?)", eventType, targetAccountID, encoded, now)
	default:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, event_data, created_at) VALUES (?, ?, ?)", eventType, encoded, now)
	}
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	return nil
}

func normalizeReason(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "", false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return "", false
		}
	}
	return value, true
}

func signedDelta(before, after uint64) (int64, bool) {
	if after >= before {
		difference := after - before
		if difference > math.MaxInt64 {
			return 0, false
		}
		return int64(difference), true
	}
	difference := before - after
	if difference > math.MaxInt64 {
		return 0, false
	}
	return -int64(difference), true
}

func validStatus(status string) bool {
	return status == StatusActive || status == StatusRevoked || status == StatusExpired || status == StatusUsed
}

func positiveInsertID(result sql.Result) (int64, error) {
	if result == nil {
		return 0, ErrUnavailable
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return 0, ErrUnavailable
	}
	return id, nil
}

func oneRow(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

func duplicateKey(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func destroyUID(uid *identity.EncryptedUID) {
	if uid == nil {
		return
	}
	clear(uid.Ciphertext)
	clear(uid.Lookup)
	uid.Ciphertext = nil
	uid.Lookup = nil
}
