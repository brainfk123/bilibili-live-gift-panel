// Package invitation owns invitation quota, code lifecycle, and one-time
// registration transactions for the hosted service.
package invitation

import "time"

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
	StatusExpired = "expired"
	StatusUsed    = "used"
)

type ActorKind string

const (
	ActorStreamer      ActorKind = "streamer"
	ActorAdministrator ActorKind = "administrator"
)

// Invitation is the secret-free history representation. CodeHint contains a
// fixed mask plus the persisted four-character suffix, never a complete code.
type Invitation struct {
	ID              int64      `json:"id"`
	CodeHint        string     `json:"codeHint"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"createdAt"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	RevokedAt       *time.Time `json:"revokedAt,omitempty"`
	UsedAt          *time.Time `json:"usedAt,omitempty"`
	CodeCiphertext  []byte     `json:"-"`
	UsedByAccountID int64      `json:"-"`
}

// GeneratedInvitation includes the complete code exactly once, after commit.
type GeneratedInvitation struct {
	Invitation
	Code           string `json:"code"`
	RemainingQuota uint64 `json:"remainingQuota,omitempty"`
}

type InvitationList struct {
	RemainingQuota uint64       `json:"remainingQuota"`
	Invitations    []Invitation `json:"invitations"`
}

type Quota struct {
	AccountID      int64  `json:"accountId"`
	RemainingQuota uint64 `json:"remainingQuota"`
}

type AdminInvitationRecord struct {
	ID              int64      `json:"id"`
	Code            string     `json:"code,omitempty"`
	CodeHint        string     `json:"codeHint"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"createdAt"`
	ExpiresAt       *time.Time `json:"expiresAt"`
	UsedByAccountID int64      `json:"usedByAccountId,omitempty"`
}
type AdminInvitationPage struct {
	Invitations []AdminInvitationRecord `json:"invitations"`
	NextCursor  string                  `json:"nextCursor,omitempty"`
}
type AdminInvitationQuery struct {
	Query, Status, Sort, Direction, Cursor string
	Limit                                  int
}
