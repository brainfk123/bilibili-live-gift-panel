package adminsettings

import (
	"bilibili-live-gift-panel/internal/hosted/security"
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	ErrAuthentication = errors.New("admin settings: authentication failed")
	ErrUnavailable    = errors.New("admin settings: unavailable")
)

type sessionValidator interface {
	RequireSession(context.Context, string) error
}
type Service struct {
	db       *sql.DB
	keys     security.Keyring
	sessions sessionValidator
	health   func(context.Context) string
	now      func() time.Time
}

func NewService(db *sql.DB, keys security.Keyring, sessions sessionValidator, health func(context.Context) string) (*Service, error) {
	if db == nil || sessions == nil {
		return nil, ErrUnavailable
	}
	if _, err := keys.HashToken("admin_session", []byte("constructor-check")); err != nil {
		return nil, ErrUnavailable
	}
	if health == nil {
		health = func(context.Context) string { return "unavailable" }
	}
	return &Service{db: db, keys: keys, sessions: sessions, health: health, now: time.Now}, nil
}
func (s *Service) Settings(ctx context.Context, token string) (Settings, error) {
	if token == "" || s.sessions.RequireSession(ctx, token) != nil {
		return Settings{}, ErrAuthentication
	}
	hash, err := s.keys.HashToken("admin_session", []byte(token))
	if err != nil {
		return Settings{}, ErrAuthentication
	}
	var cipher []byte
	var result Settings
	var recovery sql.NullTime
	err = s.db.QueryRowContext(ctx, `SELECT a.email_ciphertext,s.expires_at,EXISTS(SELECT 1 FROM admin_totp t WHERE t.admin_identity_id=a.id),MAX(r.created_at) FROM admin_identity a JOIN site_sessions s ON s.admin_identity_id=a.id LEFT JOIN admin_recovery_codes r ON r.admin_identity_id=a.id WHERE a.id=1 AND s.token_hash=? AND s.revoked_at IS NULL GROUP BY a.email_ciphertext,s.expires_at,a.id`, hash).Scan(&cipher, &result.SessionExpiresAt, &result.TOTPEnabled, &recovery)
	if err != nil {
		return Settings{}, ErrUnavailable
	}
	email, err := s.keys.Open("admin_email", cipher)
	if err != nil {
		return Settings{}, ErrUnavailable
	}
	defer clear(email)
	result.MaskedEmail = maskEmail(string(email))
	if recovery.Valid {
		value := recovery.Time
		result.RecoveryGeneratedAt = &value
	}
	result.ServiceHealth = s.health(ctx)
	return result, nil
}
func (s *Service) RevokeOtherSessions(ctx context.Context, token string) error {
	if token == "" || s.sessions.RequireSession(ctx, token) != nil {
		return ErrAuthentication
	}
	hash, err := s.keys.HashToken("admin_session", []byte(token))
	if err != nil {
		return ErrAuthentication
	}
	_, err = s.db.ExecContext(ctx, `UPDATE site_sessions SET revoked_at=? WHERE admin_identity_id=1 AND token_hash<>? AND revoked_at IS NULL`, s.now().UTC(), hash)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Service) Events(ctx context.Context, token string) ([]Event, error) {
	if token == "" || s.sessions.RequireSession(ctx, token) != nil {
		return nil, ErrAuthentication
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_type,COALESCE(target_account_id,0),created_at FROM audit_events WHERE actor_admin_identity_id=1 ORDER BY created_at DESC,id DESC LIMIT 50`)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	copy := map[string]string{"streamer_account_disabled": "账号已停用", "streamer_account_enabled": "账号已启用", "invitation_quota_adjusted": "邀请码额度已调整", "obs_credential_reset": "OBS 地址已更新", "bili_service_credential_replaced": "B站服务账号已替换", "invitation_revoked": "邀请码已作废"}
	events := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.Type, &event.AccountID, &event.CreatedAt); err != nil {
			return nil, ErrUnavailable
		}
		text, ok := copy[event.Type]
		if !ok {
			continue
		}
		event.Text = text
		events = append(events, event)
	}
	return events, rows.Err()
}
func (s *Service) Diagnostics(ctx context.Context, token string) (Diagnostics, error) {
	if token == "" || s.sessions.RequireSession(ctx, token) != nil {
		return Diagnostics{}, ErrAuthentication
	}
	database := "ok"
	if s.db.PingContext(ctx) != nil {
		database = "unavailable"
	}
	return Diagnostics{Database: database, BiliService: s.health(ctx), CheckedAt: s.now().UTC()}, nil
}
func maskEmail(value string) string {
	local, domain, ok := strings.Cut(value, "@")
	if !ok || local == "" || domain == "" {
		return "***"
	}
	runes := []rune(local)
	prefix := string(runes[:1])
	return prefix + "***@" + domain
}
func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
