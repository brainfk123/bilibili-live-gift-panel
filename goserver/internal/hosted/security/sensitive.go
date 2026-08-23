package security

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrSensitiveAuthenticationFailed = errors.New("admin identity: authentication failed")
	ErrSensitiveRecentTOTPRequired   = errors.New("admin identity: recent totp required")
)

// SessionValidator checks an active administrator session without requiring
// an operation-specific second factor.
type SessionValidator interface {
	RequireSession(context.Context, string) error
}

// SensitiveAuthorizer binds recent-TOTP authorization and renewal to the
// caller-owned transaction that contains a protected mutation.
type SensitiveAuthorizer interface {
	AuthorizeRecentTOTP(context.Context, *sql.Tx, string, time.Time) (SensitiveSession, error)
	RenewRecentTOTP(context.Context, *sql.Tx, SensitiveSession, time.Time) error
}

// SensitiveSession is an opaque fence for one administrator session and its
// credential epoch. Protected-operation callers only carry this value from
// authorization to renewal; it never contains a raw token or principal data.
type SensitiveSession struct {
	sessionID       int64
	credentialEpoch int64
	provenance      *sensitiveSessionProvenance
}

type sensitiveSessionProvenance struct {
	marker byte
}

// SensitiveSessionIssuer binds fences to one authorizer instance. Other
// packages can carry a SensitiveSession but cannot reopen or forge one for an
// issuer they do not own.
type SensitiveSessionIssuer struct {
	provenance *sensitiveSessionProvenance
}

func NewSensitiveSessionIssuer() *SensitiveSessionIssuer {
	return &SensitiveSessionIssuer{provenance: &sensitiveSessionProvenance{marker: 1}}
}

func (issuer *SensitiveSessionIssuer) Issue(sessionID, credentialEpoch int64) (SensitiveSession, bool) {
	if issuer == nil || issuer.provenance == nil || sessionID <= 0 || credentialEpoch <= 0 {
		return SensitiveSession{}, false
	}
	return SensitiveSession{sessionID: sessionID, credentialEpoch: credentialEpoch, provenance: issuer.provenance}, true
}

func (issuer *SensitiveSessionIssuer) Open(session SensitiveSession) (sessionID, credentialEpoch int64, valid bool) {
	if issuer == nil || issuer.provenance == nil || session.provenance != issuer.provenance || session.sessionID <= 0 || session.credentialEpoch <= 0 {
		return 0, 0, false
	}
	return session.sessionID, session.credentialEpoch, true
}
