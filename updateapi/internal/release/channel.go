package release

// Channel is the closed set of update streams served by the update API.
type Channel string

const (
	ChannelStable         Channel = "stable"
	ChannelLegacyRushRush Channel = "legacy-rushrush"
)
