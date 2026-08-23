package adminsettings

import "time"

type Settings struct {
	MaskedEmail         string     `json:"maskedEmail"`
	SessionExpiresAt    time.Time  `json:"sessionExpiresAt"`
	TOTPEnabled         bool       `json:"totpEnabled"`
	RecoveryGeneratedAt *time.Time `json:"recoveryGeneratedAt"`
	ServiceHealth       string     `json:"serviceHealth"`
}
type Event struct {
	Type      string    `json:"type"`
	Text      string    `json:"text"`
	AccountID int64     `json:"accountId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}
type Diagnostics struct {
	Database    string    `json:"database"`
	BiliService string    `json:"biliService"`
	CheckedAt   time.Time `json:"checkedAt"`
}
