package invitation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"
	"github.com/go-sql-driver/mysql"
)

type adminInvitationCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        int64     `json:"id"`
	Rank      int       `json:"rank,omitempty"`
}

func (service *Service) ListAdministrator(ctx context.Context, token string, query AdminInvitationQuery) (AdminInvitationPage, error) {
	if service == nil || service.administrator == nil || token == "" {
		return AdminInvitationPage{}, ErrAuthentication
	}
	if err := service.administrator.RequireSession(ctx, token); err != nil {
		return AdminInvitationPage{}, ErrAuthentication
	}
	query.Query = strings.TrimSpace(strings.ToUpper(query.Query))
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 100 {
		return AdminInvitationPage{}, ErrInvalidInput
	}
	if query.Sort == "" {
		query.Sort = "created_at"
	}
	if query.Direction == "" {
		query.Direction = "desc"
	}
	if query.Sort != "created_at" && query.Sort != "status" || query.Direction != "asc" && query.Direction != "desc" || query.Status != "" && query.Status != StatusActive && query.Status != StatusUsed && query.Status != StatusRevoked && query.Status != StatusExpired {
		return AdminInvitationPage{}, ErrInvalidInput
	}
	where := []string{"creator_admin_identity_id=1"}
	args := []any{}
	if query.Status != "" {
		where = append(where, "status=?")
		args = append(args, query.Status)
	}
	if query.Query != "" {
		suffix := query.Query
		if len(suffix) > 4 {
			suffix = suffix[len(suffix)-4:]
		}
		if id, err := strconv.ParseInt(query.Query, 10, 64); err == nil && id > 0 {
			where = append(where, "(code_hint LIKE ? OR invited_account_id=?)")
			args = append(args, "%"+suffix, id)
		} else {
			where = append(where, "code_hint LIKE ?")
			args = append(args, "%"+suffix)
		}
	}
	direction := "DESC"
	comparison := "<"
	if query.Direction == "asc" {
		direction = "ASC"
		comparison = ">"
	}
	order := "created_at"
	if query.Sort == "status" {
		order = "CASE status WHEN 'active' THEN 1 WHEN 'used' THEN 2 WHEN 'revoked' THEN 3 ELSE 4 END"
	}
	if query.Cursor != "" {
		cursor, err := decodeAdminInvitationCursor(query.Cursor)
		if err != nil {
			return AdminInvitationPage{}, ErrInvalidInput
		}
		if query.Sort == "created_at" {
			where = append(where, fmt.Sprintf("(created_at %s ? OR (created_at = ? AND id %s ?))", comparison, comparison))
			args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
		} else {
			where = append(where, fmt.Sprintf("(%s %s ? OR (%s = ? AND id %s ?))", order, comparison, order, comparison))
			args = append(args, cursor.Rank, cursor.Rank, cursor.ID)
		}
	}
	args = append(args, query.Limit+1)
	rows, err := service.db.QueryContext(ctx, `SELECT id,code_ciphertext,code_hint,status,created_at,expires_at,COALESCE(invited_account_id,0) FROM invitations WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` `+direction+`,id `+direction+` LIMIT ?`, args...)
	if err != nil {
		return AdminInvitationPage{}, ErrUnavailable
	}
	defer rows.Close()
	items := []AdminInvitationRecord{}
	for rows.Next() {
		var item AdminInvitationRecord
		var cipher []byte
		var expiry sql.NullTime
		if err := rows.Scan(&item.ID, &cipher, &item.CodeHint, &item.Status, &item.CreatedAt, &expiry, &item.UsedByAccountID); err != nil {
			return AdminInvitationPage{}, ErrUnavailable
		}
		item.CodeHint = "****" + item.CodeHint
		if expiry.Valid {
			value := expiry.Time
			item.ExpiresAt = &value
		}
		if item.Status == StatusActive {
			plain, err := service.keys.Open("invitation_code_ciphertext", cipher)
			if err != nil {
				return AdminInvitationPage{}, ErrUnavailable
			}
			item.Code = string(plain)
		}
		items = append(items, item)
	}
	page := AdminInvitationPage{Invitations: items}
	if len(items) > query.Limit {
		last := items[query.Limit-1]
		page.Invitations = items[:query.Limit]
		rank := statusRank(last.Status)
		page.NextCursor = encodeAdminInvitationCursor(adminInvitationCursor{last.CreatedAt, last.ID, rank})
	}
	return page, rows.Err()
}

func encodeAdminInvitationCursor(cursor adminInvitationCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeAdminInvitationCursor(value string) (adminInvitationCursor, error) {
	var cursor adminInvitationCursor
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID <= 0 || cursor.CreatedAt.IsZero() {
		return cursor, ErrInvalidInput
	}
	return cursor, nil
}
func statusRank(status string) int {
	switch status {
	case StatusActive:
		return 1
	case StatusUsed:
		return 2
	case StatusRevoked:
		return 3
	default:
		return 4
	}
}

func (service *Service) GenerateAdministratorBatch(ctx context.Context, token string, count int, validity string) ([]AdminInvitationRecord, error) {
	if service == nil || service.administrator == nil || token == "" || count < 1 || count > 50 {
		return nil, ErrInvalidInput
	}
	if err := service.administrator.RequireSession(ctx, token); err != nil {
		return nil, ErrAuthentication
	}
	var ttl time.Duration
	switch validity {
	case "7d":
		ttl = 7 * 24 * time.Hour
	case "30d":
		ttl = 30 * 24 * time.Hour
	case "permanent":
		ttl = 0
	default:
		return nil, ErrInvalidInput
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer tx.Rollback()
	now := service.now().UTC()
	items := make([]AdminInvitationRecord, 0, count)
	for range count {
		code, err := newInvitationCode(service.keys)
		if err != nil {
			return nil, ErrUnavailable
		}
		digest := sha256.Sum256([]byte(code))
		cipher, err := service.keys.Seal("invitation_code_ciphertext", []byte(code))
		if err != nil {
			return nil, ErrUnavailable
		}
		hint := code[4:]
		var expiry any
		var expiryPointer *time.Time
		if ttl > 0 {
			value := now.Add(ttl)
			expiry = value
			expiryPointer = &value
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO invitations (code_hash,code_ciphertext,code_hint,creator_admin_identity_id,status,created_at,expires_at) VALUES (?,?,?,1,'active',?,?)", digest[:], cipher, hint, now, expiry)
		if err != nil {
			return nil, ErrUnavailable
		}
		id, err := positiveInsertID(result)
		if err != nil {
			return nil, err
		}
		items = append(items, AdminInvitationRecord{ID: id, Code: code, CodeHint: "****" + hint, Status: StatusActive, CreatedAt: now, ExpiresAt: expiryPointer})
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}

func (service *Service) RevokeAdministrator(ctx context.Context, token string, id int64) error {
	if service == nil || service.administrator == nil || token == "" || id <= 0 {
		return ErrInvalidInput
	}
	if err := service.administrator.RequireSession(ctx, token); err != nil {
		return ErrAuthentication
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	now := service.now().UTC()
	result, err := tx.ExecContext(ctx, "UPDATE invitations SET status='revoked',revoked_at=?,code_ciphertext=NULL WHERE id=? AND creator_admin_identity_id=1 AND status='active'", now, id)
	if err != nil || !oneRow(result) {
		return ErrInvitationInvalid
	}
	if err := insertAudit(ctx, tx, auditEvent{eventType: "invitation_revoked", actor: administratorAuditActor(), invitationID: id}, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

const (
	digestSize           = sha256.Size
	invitationCodeLength = 8
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
	Administrator security.SessionValidator
}

type Service struct {
	db            *sql.DB
	keys          security.Keyring
	intents       registrationIntentSource
	administrator security.SessionValidator
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
	return &Service{db: db, keys: keys, intents: intents, administrator: options.Administrator, now: options.Now, invitationTTL: options.InvitationTTL, sessionTTL: options.SessionTTL}, nil
}

func (service *Service) Generate(ctx context.Context, sessionToken string, actor ActorKind) (GeneratedInvitation, error) {
	if service == nil || sessionToken == "" || (actor != ActorStreamer && actor != ActorAdministrator) {
		return GeneratedInvitation{}, ErrInvalidInput
	}
	code, err := newInvitationCode(service.keys)
	if err != nil {
		return GeneratedInvitation{}, ErrUnavailable
	}
	codeDigest := sha256.Sum256([]byte(code))
	codeCiphertext, err := service.keys.Seal("invitation_code_ciphertext", []byte(code))
	if err != nil {
		return GeneratedInvitation{}, ErrUnavailable
	}
	hint := code[len(code)-4:]

	var tokenHash []byte
	if actor == ActorStreamer {
		tokenHash, err = service.keys.HashToken("site_session", []byte(sessionToken))
		if err != nil {
			return GeneratedInvitation{}, ErrAuthentication
		}
	} else if service.administrator == nil {
		return GeneratedInvitation{}, ErrUnavailable
	}
	if actor == ActorAdministrator {
		if err := service.administrator.RequireSession(ctx, sessionToken); err != nil {
			return GeneratedInvitation{}, ErrAuthentication
		}
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
	var now, expiresAt time.Time
	switch actor {
	case ActorStreamer:
		authorization, err := lockCurrentStreamer(ctx, transaction, tokenHash)
		if err != nil {
			return GeneratedInvitation{}, err
		}
		quotaErr := transaction.QueryRowContext(ctx, "SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE", authorization.accountID).Scan(&remaining)
		if quotaErr != nil && !errors.Is(quotaErr, sql.ErrNoRows) {
			return GeneratedInvitation{}, ErrUnavailable
		}
		now = service.now()
		if err := authorization.validate(now); err != nil {
			return GeneratedInvitation{}, err
		}
		if errors.Is(quotaErr, sql.ErrNoRows) {
			return GeneratedInvitation{}, ErrQuotaExhausted
		}
		expiresAt = now.Add(service.invitationTTL)
		if remaining == 0 {
			return GeneratedInvitation{}, ErrQuotaExhausted
		}
		result, err := transaction.ExecContext(ctx,
			"INSERT INTO invitations (code_hash, code_ciphertext, code_hint, creator_account_id, status, created_at, expires_at) VALUES (?, ?, ?, ?, 'active', ?, ?)",
			codeDigest[:], codeCiphertext, hint, authorization.accountID, now, expiresAt,
		)
		if err != nil {
			return GeneratedInvitation{}, ErrUnavailable
		}
		invitationID, err = positiveInsertID(result)
		if err != nil {
			return GeneratedInvitation{}, err
		}
		remaining--
		if result, err = transaction.ExecContext(ctx, "UPDATE invitation_quotas SET remaining_quota = ?, updated_at = ? WHERE account_id = ?", remaining, now, authorization.accountID); err != nil || !oneRow(result) {
			return GeneratedInvitation{}, ErrUnavailable
		}
		if result, err = transaction.ExecContext(ctx,
			"INSERT INTO invitation_quota_events (account_id, invitation_id, quota_delta, quota_after, reason, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			authorization.accountID, invitationID, int64(-1), remaining, "invitation_generated", now,
		); err != nil || !oneRow(result) {
			return GeneratedInvitation{}, ErrUnavailable
		}
		if err := insertAudit(ctx, transaction, auditEvent{
			eventType: "invitation_generated", actor: streamerAuditActor(authorization.accountID), invitationID: invitationID,
		}, now); err != nil {
			return GeneratedInvitation{}, err
		}
	case ActorAdministrator:
		now = service.now().UTC()
		expiresAt = now.Add(service.invitationTTL)
		result, err := transaction.ExecContext(ctx,
			"INSERT INTO invitations (code_hash, code_ciphertext, code_hint, creator_admin_identity_id, status, created_at, expires_at) VALUES (?, ?, ?, 1, 'active', ?, ?)",
			codeDigest[:], codeCiphertext, hint, now, expiresAt,
		)
		if err != nil {
			return GeneratedInvitation{}, ErrUnavailable
		}
		invitationID, err = positiveInsertID(result)
		if err != nil {
			return GeneratedInvitation{}, err
		}
		if err := insertAudit(ctx, transaction, auditEvent{
			eventType: "invitation_generated", actor: administratorAuditActor(), invitationID: invitationID,
		}, now); err != nil {
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

func newInvitationCode(_ security.Keyring) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	code := make([]byte, invitationCodeLength)
	random := make([]byte, invitationCodeLength)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	for index, value := range random {
		code[index] = alphabet[int(value)%len(alphabet)]
	}
	return string(code), nil
}

func (service *Service) AdjustQuota(ctx context.Context, administratorSession string, accountID int64, remaining uint64, reason string) (Quota, error) {
	normalizedReason, valid := normalizeReason(reason)
	if service == nil || service.administrator == nil || administratorSession == "" || accountID <= 0 || !valid {
		return Quota{}, ErrInvalidInput
	}
	if err := service.administrator.RequireSession(ctx, administratorSession); err != nil {
		return Quota{}, ErrAuthentication
	}
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
	now := service.now().UTC()
	var credentialEpoch int64
	if accountErr := transaction.QueryRowContext(ctx, "SELECT credential_epoch FROM streamer_accounts WHERE id = ? FOR UPDATE", accountID).Scan(&credentialEpoch); accountErr != nil {
		if errors.Is(accountErr, sql.ErrNoRows) {
			return Quota{}, ErrInvitationInvalid
		}
		return Quota{}, ErrUnavailable
	}
	if credentialEpoch < 1 {
		return Quota{}, ErrUnavailable
	}
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, CURRENT_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE account_id = account_id",
		accountID,
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
	if err := insertAudit(ctx, transaction, auditEvent{
		eventType: "invitation_quota_adjusted", actor: administratorAuditActor(), targetAccountID: accountID,
		quota: &quotaAudit{Reason: normalizedReason, Before: before, After: remaining, Delta: delta},
	}, now); err != nil {
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
	authorization, err := lockCurrentStreamer(ctx, transaction, tokenHash)
	if err != nil {
		return err
	}
	var status string
	var expiresAt time.Time
	if invitationErr := transaction.QueryRowContext(ctx, "SELECT status, expires_at FROM invitations WHERE id = ? AND creator_account_id = ? FOR UPDATE", invitationID, authorization.accountID).Scan(&status, &expiresAt); invitationErr != nil {
		now := service.now()
		if err := authorization.validate(now); err != nil {
			return err
		}
		if errors.Is(invitationErr, sql.ErrNoRows) {
			return ErrInvitationInvalid
		}
		return ErrUnavailable
	}
	now := service.now()
	if err := authorization.validate(now); err != nil {
		return err
	}
	if status != StatusActive || !now.Before(expiresAt) {
		return ErrInvitationInvalid
	}
	result, err := transaction.ExecContext(ctx, "UPDATE invitations SET status = 'revoked', revoked_at = ?, code_ciphertext = NULL WHERE id = ? AND status = 'active'", now, invitationID)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if err := insertAudit(ctx, transaction, auditEvent{
		eventType: "invitation_revoked", actor: streamerAuditActor(authorization.accountID), invitationID: invitationID,
	}, now); err != nil {
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
	now := service.now()
	if status != StatusActive || now.Before(expiresAt) {
		return ErrInvitationInvalid
	}
	result, err := transaction.ExecContext(ctx, "UPDATE invitations SET status = 'expired', code_ciphertext = NULL WHERE id = ? AND status = 'active'", invitationID)
	if err != nil || !oneRow(result) {
		return ErrUnavailable
	}
	if err := insertAudit(ctx, transaction, auditEvent{eventType: "invitation_expired", invitationID: invitationID}, now); err != nil {
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
	authorization, err := lockCurrentStreamer(ctx, transaction, tokenHash)
	if err != nil {
		return InvitationList{}, err
	}
	now := service.now()
	if err := authorization.validate(now); err != nil {
		return InvitationList{}, err
	}
	accountID := authorization.accountID
	if _, err := transaction.ExecContext(ctx, "UPDATE invitations SET status = 'expired', code_ciphertext = NULL WHERE creator_account_id = ? AND status = 'active' AND expires_at <= ?", accountID, now); err != nil {
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
	if invitationID <= 0 {
		return identity.SiteSession{}, ErrUnavailable
	}
	var existingAccountID int64
	err = transaction.QueryRowContext(ctx, "SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE", uid.Lookup).Scan(&existingAccountID)
	if err == nil {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return identity.SiteSession{}, ErrUnavailable
	}
	if !reservation.Valid() {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	now := service.now()
	if status != StatusActive || !now.Before(expiresAt) || !now.Before(intentExpiresAt) {
		return identity.SiteSession{}, ErrInvitationInvalid
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
	if result, err = transaction.ExecContext(ctx, "UPDATE invitations SET status = 'used', used_at = ?, invited_account_id = ?, code_ciphertext = NULL WHERE id = ? AND status = 'active'", now, accountID, invitationID); err != nil || !oneRow(result) {
		return identity.SiteSession{}, ErrUnavailable
	}
	if err := insertAudit(ctx, transaction, auditEvent{
		eventType: "invitation_redeemed", targetAccountID: accountID, invitationID: invitationID,
	}, now); err != nil {
		return identity.SiteSession{}, err
	}
	if !reservation.Valid() {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	commitAt := service.now()
	if !commitAt.Before(intentExpiresAt) || !commitAt.Before(expiresAt) {
		return identity.SiteSession{}, ErrInvitationInvalid
	}
	if err := transaction.Commit(); err != nil {
		return identity.SiteSession{}, ErrUnavailable
	}
	committed = true
	reservation.Commit()
	return identity.SiteSession{Token: siteToken, AccountID: accountID, ExpiresAt: sessionExpiresAt}, nil
}

type streamerAuthorization struct {
	accountID      int64
	accountEpoch   int64
	disabled       bool
	sessionID      int64
	sessionEpoch   int64
	sessionExpires time.Time
	sessionRevoked bool
}

func lockCurrentStreamer(ctx context.Context, transaction *sql.Tx, tokenHash []byte) (streamerAuthorization, error) {
	var authorization streamerAuthorization
	var accountID int64
	if err := transaction.QueryRowContext(ctx, "SELECT account_id FROM site_sessions WHERE token_hash = ? AND account_id IS NOT NULL LIMIT 1", tokenHash).Scan(&accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return authorization, ErrAuthentication
		}
		return authorization, ErrUnavailable
	}
	if accountID <= 0 {
		return authorization, ErrUnavailable
	}
	var disabledAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, "SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE", accountID).Scan(&authorization.accountEpoch, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return authorization, ErrAuthentication
		}
		return authorization, ErrUnavailable
	}
	var revokedAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, "SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE account_id = ? AND token_hash = ? FOR UPDATE", accountID, tokenHash).Scan(&authorization.sessionID, &authorization.sessionEpoch, &authorization.sessionExpires, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return authorization, ErrAuthentication
		}
		return authorization, ErrUnavailable
	}
	authorization.accountID = accountID
	authorization.disabled = disabledAt.Valid
	authorization.sessionRevoked = revokedAt.Valid
	return authorization, nil
}

func (authorization streamerAuthorization) validate(now time.Time) error {
	if authorization.accountID <= 0 || authorization.accountEpoch < 1 {
		return ErrUnavailable
	}
	if authorization.disabled || authorization.sessionID <= 0 || authorization.sessionEpoch < 1 ||
		authorization.sessionEpoch != authorization.accountEpoch || !authorization.sessionExpires.After(now) || authorization.sessionRevoked {
		return ErrAuthentication
	}
	return nil
}

func mapSensitiveError(err error) error {
	switch {
	case errors.Is(err, security.ErrSensitiveRecentTOTPRequired):
		return ErrRecentTOTPRequired
	case errors.Is(err, security.ErrSensitiveAuthenticationFailed):
		return ErrAuthentication
	default:
		return ErrUnavailable
	}
}

type quotaAudit struct {
	Reason string `json:"reason"`
	Before uint64 `json:"before"`
	After  uint64 `json:"after"`
	Delta  int64  `json:"delta"`
}

type auditActorKind uint8

const (
	auditActorNone auditActorKind = iota
	auditActorStreamer
	auditActorAdministrator
)

type auditActor struct {
	kind auditActorKind
	id   int64
}

func streamerAuditActor(accountID int64) auditActor {
	return auditActor{kind: auditActorStreamer, id: accountID}
}

func administratorAuditActor() auditActor {
	return auditActor{kind: auditActorAdministrator, id: 1}
}

type auditEvent struct {
	eventType       string
	actor           auditActor
	targetAccountID int64
	invitationID    int64
	quota           *quotaAudit
}

func insertAudit(ctx context.Context, transaction *sql.Tx, event auditEvent, now time.Time) error {
	if event.eventType == "" || transaction == nil || event.targetAccountID < 0 || event.invitationID < 0 ||
		(event.actor.kind == auditActorNone && event.actor.id != 0) ||
		(event.actor.kind == auditActorStreamer && event.actor.id <= 0) ||
		(event.actor.kind == auditActorAdministrator && event.actor.id != 1) ||
		event.actor.kind > auditActorAdministrator {
		return ErrUnavailable
	}
	payload := struct {
		InvitationID int64       `json:"invitationId,omitempty"`
		Quota        *quotaAudit `json:"quota,omitempty"`
	}{InvitationID: event.invitationID, Quota: event.quota}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ErrUnavailable
	}
	var result sql.Result
	switch {
	case event.actor.kind == auditActorStreamer && event.targetAccountID > 0:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, actor_account_id, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?, ?)", event.eventType, event.actor.id, event.targetAccountID, encoded, now)
	case event.actor.kind == auditActorStreamer:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, actor_account_id, event_data, created_at) VALUES (?, ?, ?, ?)", event.eventType, event.actor.id, encoded, now)
	case event.actor.kind == auditActorAdministrator && event.targetAccountID > 0:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, 1, ?, ?, ?)", event.eventType, event.targetAccountID, encoded, now)
	case event.actor.kind == auditActorAdministrator:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES (?, 1, ?, ?)", event.eventType, encoded, now)
	case event.targetAccountID > 0:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?)", event.eventType, event.targetAccountID, encoded, now)
	default:
		result, err = transaction.ExecContext(ctx, "INSERT INTO audit_events (event_type, event_data, created_at) VALUES (?, ?, ?)", event.eventType, encoded, now)
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
