package adminconsole

import "time"

type AccountStatus string

const (
	AccountStatusActive   AccountStatus = "active"
	AccountStatusDisabled AccountStatus = "disabled"
)

type AttentionKind string

const (
	AttentionMissingRoom AttentionKind = "missing_room"
	AttentionMissingOBS  AttentionKind = "missing_obs"
)

type Event struct {
	Type      string    `json:"type"`
	Text      string    `json:"text"`
	AccountID int64     `json:"accountId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type AttentionItem struct {
	Kind      AttentionKind `json:"kind"`
	AccountID int64         `json:"accountId"`
	Text      string        `json:"text"`
	Priority  int           `json:"priority"`
}

type Overview struct {
	TotalAccounts    int64           `json:"totalAccounts"`
	ActiveAccounts   int64           `json:"activeAccounts"`
	DisabledAccounts int64           `json:"disabledAccounts"`
	MissingRooms     int64           `json:"missingRooms"`
	MissingOBS       int64           `json:"missingObs"`
	Attention        []AttentionItem `json:"attention"`
	RecentEvents     []Event         `json:"recentEvents"`
}

type AccountSummary struct {
	ID              int64         `json:"id"`
	Status          AccountStatus `json:"status"`
	RoomID          string        `json:"roomId,omitempty"`
	InvitationQuota int64         `json:"invitationQuota"`
	HasOBS          bool          `json:"hasObs"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

type AccountDetail struct {
	AccountSummary
	OBSURL       string  `json:"obsUrl,omitempty"`
	RecentEvents []Event `json:"recentEvents"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type AccountQuery struct {
	Query     string
	Status    AccountStatus
	Attention AttentionKind
	Cursor    string
	Limit     int
}

type Cursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        int64     `json:"id"`
}
