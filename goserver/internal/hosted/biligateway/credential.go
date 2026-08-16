// Package biligateway owns the only production boundary that may use the
// administrator-managed Bilibili service credential.
package biligateway

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"
)

const credentialPurpose = "bili_service_credential"

const (
	activeCredentialQuery      = "SELECT id, credential_version FROM bili_service_credentials WHERE revoked_at IS NULL FOR UPDATE"
	insertCredentialQuery      = "INSERT INTO bili_service_credentials (credential_version, cookie_ciphertext, created_at) VALUES (?, ?, ?)"
	revokeCredentialQuery      = "UPDATE bili_service_credentials SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL"
	insertCredentialAuditQuery = "INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES ('bili_service_credential_replaced', 1, ?, ?)"
	loadCredentialQuery        = "SELECT credential_version, cookie_ciphertext, created_at FROM bili_service_credentials WHERE revoked_at IS NULL"
)

var (
	ErrCredentialUnavailable = errors.New("credential_unavailable")
	ErrCredentialNotFound    = errors.New("credential_not_found")
)

// Credential is deliberately useful only to the controlled production
// gateway. HTTP code must never serialize Cookie.
type Credential struct {
	Version   int64
	Cookie    []byte
	CreatedAt time.Time
}
type CredentialStatus struct {
	Version        int64
	Health         string
	LastVerifiedAt *time.Time
}

// CredentialStore serializes replacement through an InnoDB transaction:
// prior active credential revocation, new encrypted credential, and immutable
// audit event either all commit or all roll back.
type CredentialStore struct {
	database *sql.DB
	keys     security.Keyring
	now      func() time.Time
}

func NewCredentialStore(database *sql.DB, keys security.Keyring, now func() time.Time) *CredentialStore {
	if now == nil {
		now = time.Now
	}
	return &CredentialStore{database: database, keys: keys, now: now}
}

func (store *CredentialStore) Replace(ctx context.Context, cookie []byte) (Credential, error) {
	if store == nil || store.database == nil || len(cookie) == 0 {
		return Credential{}, ErrCredentialUnavailable
	}
	ciphertext, err := store.keys.Seal(credentialPurpose, cookie)
	if err != nil {
		return Credential{}, ErrCredentialUnavailable
	}
	defer clear(ciphertext)
	now := store.now().UTC()
	tx, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return Credential{}, ErrCredentialUnavailable
	}
	defer tx.Rollback()
	var priorID, priorVersion int64
	err = tx.QueryRowContext(ctx, activeCredentialQuery).Scan(&priorID, &priorVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrCredentialUnavailable
	}
	version := priorVersion + 1
	if errors.Is(err, sql.ErrNoRows) {
		version = 1
	}
	if priorID != 0 {
		result, execErr := tx.ExecContext(ctx, revokeCredentialQuery, now, priorID)
		if execErr != nil {
			return Credential{}, ErrCredentialUnavailable
		}
		affected, affectedErr := result.RowsAffected()
		if affectedErr != nil || affected != 1 {
			return Credential{}, ErrCredentialUnavailable
		}
	}
	if _, err := tx.ExecContext(ctx, insertCredentialQuery, version, ciphertext, now); err != nil {
		return Credential{}, ErrCredentialUnavailable
	}
	audit := []byte(`{"credentialVersion":` + integerText(version) + `}`)
	defer clear(audit)
	if _, err := tx.ExecContext(ctx, insertCredentialAuditQuery, audit, now); err != nil {
		return Credential{}, ErrCredentialUnavailable
	}
	if err := tx.Commit(); err != nil {
		return Credential{}, ErrCredentialUnavailable
	}
	// Replace never returns plaintext. The caller already owns the callback
	// buffer and biliqr destroys it once this transaction succeeds.
	return Credential{Version: version, CreatedAt: now}, nil
}

func (store *CredentialStore) Load(ctx context.Context) (Credential, error) {
	if store == nil || store.database == nil {
		return Credential{}, ErrCredentialUnavailable
	}
	var credential Credential
	var ciphertext []byte
	if err := store.database.QueryRowContext(ctx, loadCredentialQuery).Scan(&credential.Version, &ciphertext, &credential.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Credential{}, ErrCredentialNotFound
		}
		return Credential{}, ErrCredentialUnavailable
	}
	defer clear(ciphertext)
	cookie, err := store.keys.Open(credentialPurpose, ciphertext)
	if err != nil {
		return Credential{}, ErrCredentialUnavailable
	}
	credential.Cookie = cookie
	return credential, nil
}
func (store *CredentialStore) Status(ctx context.Context) CredentialStatus {
	credential, err := store.Load(ctx)
	if err == nil {
		defer clear(credential.Cookie)
		verifiedAt := credential.CreatedAt.UTC()
		return CredentialStatus{Version: credential.Version, Health: "healthy", LastVerifiedAt: &verifiedAt}
	}
	if errors.Is(err, ErrCredentialNotFound) {
		return CredentialStatus{Health: "missing"}
	}
	return CredentialStatus{Health: "unavailable"}
}

func integerText(value int64) string {
	if value == 0 {
		return "0"
	}
	var output [20]byte
	index := len(output)
	for value > 0 {
		index--
		output[index] = byte('0' + value%10)
		value /= 10
	}
	return string(output[index:])
}
