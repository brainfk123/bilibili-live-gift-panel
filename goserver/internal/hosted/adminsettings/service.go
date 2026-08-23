package adminsettings

import (
	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/security"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"sort"
	"strings"
	"time"
)

var (
	ErrAuthentication  = errors.New("admin settings: authentication failed")
	ErrInvalidInput    = errors.New("admin settings: invalid input")
	ErrCurrentSession  = errors.New("admin settings: current session")
	ErrSessionNotFound = errors.New("admin settings: session not found")
	ErrUnavailable     = errors.New("admin settings: unavailable")
)

type sessionValidator interface {
	RequireSession(context.Context, string) error
	AdministratorSessions(context.Context, string) ([]adminidentity.AdministratorSession, error)
	RevokeAdministratorSession(context.Context, string, string) error
	AdministratorLoginEvents(context.Context, string, int) ([]adminidentity.AdministratorLoginEvent, error)
}

type Session struct {
	ID            string `json:"id"`
	DeviceLabel   string `json:"deviceLabel"`
	ClientNetwork string `json:"clientNetwork"`
	CreatedAt     string `json:"createdAt"`
	LastSeenAt    string `json:"lastSeenAt"`
	ExpiresAt     string `json:"expiresAt"`
	Current       bool   `json:"current"`
}

type LoginEvent struct {
	Result        string `json:"result"`
	DeviceLabel   string `json:"deviceLabel"`
	ClientNetwork string `json:"clientNetwork"`
	OccurredAt    string `json:"occurredAt"`
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

func (s *Service) Sessions(ctx context.Context, token string) ([]Session, error) {
	if token == "" {
		return nil, ErrAuthentication
	}
	sessions, err := s.sessions.AdministratorSessions(ctx, token)
	if err != nil {
		return nil, mapIdentityError(err)
	}
	result := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if !validPublicID(session.PublicID) || !validClientSummary(session.DeviceLabel, session.ClientNetwork) || session.CreatedAt.IsZero() || session.LastSeenAt.Before(session.CreatedAt) || !session.ExpiresAt.After(session.CreatedAt) {
			return nil, ErrUnavailable
		}
		result = append(result, Session{
			ID: session.PublicID, DeviceLabel: session.DeviceLabel, ClientNetwork: session.ClientNetwork,
			CreatedAt: rfc3339(session.CreatedAt), LastSeenAt: rfc3339(session.LastSeenAt), ExpiresAt: rfc3339(session.ExpiresAt), Current: session.Current,
		})
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Current != result[right].Current {
			return result[left].Current
		}
		if result[left].LastSeenAt != result[right].LastSeenAt {
			return result[left].LastSeenAt > result[right].LastSeenAt
		}
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func (s *Service) RevokeSession(ctx context.Context, token, publicID string) error {
	if token == "" {
		return ErrAuthentication
	}
	if !validPublicID(publicID) {
		return ErrInvalidInput
	}
	return mapIdentityError(s.sessions.RevokeAdministratorSession(ctx, token, publicID))
}

func (s *Service) LoginEvents(ctx context.Context, token string, limit int) ([]LoginEvent, error) {
	if token == "" {
		return nil, ErrAuthentication
	}
	if limit < 1 || limit > 50 {
		return nil, ErrInvalidInput
	}
	events, err := s.sessions.AdministratorLoginEvents(ctx, token, limit)
	if err != nil {
		return nil, mapIdentityError(err)
	}
	result := make([]LoginEvent, 0, len(events))
	for _, event := range events {
		if (event.Result != "success" && event.Result != "failure") || !validClientSummary(event.DeviceLabel, event.ClientNetwork) || event.OccurredAt.IsZero() {
			return nil, ErrUnavailable
		}
		result = append(result, LoginEvent{Result: event.Result, DeviceLabel: event.DeviceLabel, ClientNetwork: event.ClientNetwork, OccurredAt: rfc3339(event.OccurredAt)})
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].OccurredAt > result[right].OccurredAt })
	return result, nil
}

func mapIdentityError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, adminidentity.ErrAuthenticationFailed):
		return ErrAuthentication
	case errors.Is(err, adminidentity.ErrInvalidInput):
		return ErrInvalidInput
	case errors.Is(err, adminidentity.ErrCurrentAdminSession):
		return ErrCurrentSession
	case errors.Is(err, adminidentity.ErrAdminSessionNotFound):
		return ErrSessionNotFound
	default:
		return ErrUnavailable
	}
}

func validPublicID(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func validClientSummary(deviceLabel, clientNetwork string) bool {
	if len(deviceLabel) == 0 || len(deviceLabel) > 80 || len(clientNetwork) == 0 || len(clientNetwork) > 64 {
		return false
	}
	device, browser, ok := strings.Cut(deviceLabel, " · ")
	if !ok || !allowed(device, "iPhone", "iPad", "Android", "Windows", "macOS", "Linux", "其他设备") || !allowed(browser, "Edge", "Firefox", "Chrome", "Safari", "其他浏览器") {
		return false
	}
	if clientNetwork == "—" {
		return true
	}
	if strings.HasSuffix(clientNetwork, ".*") {
		return net.ParseIP(strings.TrimSuffix(clientNetwork, "*")+"0").To4() != nil
	}
	if strings.HasSuffix(clientNetwork, "::*") {
		parsed := net.ParseIP(strings.TrimSuffix(clientNetwork, "*"))
		return parsed != nil && parsed.To4() == nil
	}
	return false
}

func allowed(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func rfc3339(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
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
