package identity

import "time"

// Account is the minimum persisted streamer account state needed by identity
// and authorization services.
type Account struct {
	ID              int64
	CredentialEpoch int64
	DisabledAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EncryptedUID contains only the authenticated ciphertext and the keyed lookup
// digest. UID plaintext must never cross the repository boundary.
type EncryptedUID struct {
	Ciphertext []byte
	Lookup     []byte
}

// Session represents both the values needed to persist a site session and the
// minimal authenticated session returned from a hash lookup. TokenHash and
// CreatedAt are intentionally omitted by FindSessionByHash.
type Session struct {
	ID              int64
	AccountID       int64
	TokenHash       []byte
	CredentialEpoch int64
	CreatedAt       time.Time
	ExpiresAt       time.Time
	RevokedAt       *time.Time
	TOTPVerifiedAt  *time.Time
}
