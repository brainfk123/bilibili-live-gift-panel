package service

import (
	"context"
	"errors"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

var (
	ErrClientVersionInvalid     = errors.New("client version invalid")
	ErrLegacyChannelUnavailable = errors.New("legacy channel unavailable")
)

type VersionBucket string

const (
	VersionInvalid VersionBucket = "invalid"
	Version047     VersionBucket = "0.4.7"
	Version049     VersionBucket = "0.4.9"
	Version0410    VersionBucket = "0.4.10"
	Version0411    VersionBucket = "0.4.11"
	Version0412    VersionBucket = "0.4.12"
)

type reviewedClient struct {
	version VersionBucket
	channel release.Channel
}

var reviewedUserAgents = map[string]reviewedClient{
	"bilibili-live-gift-panel/0.4.7":  {version: Version047, channel: release.ChannelLegacyRushRush},
	"bilibili-live-gift-panel/0.4.9":  {version: Version049, channel: release.ChannelStable},
	"bilibili-live-gift-panel/0.4.10": {version: Version0410, channel: release.ChannelStable},
	"bilibili-live-gift-panel/0.4.11": {version: Version0411, channel: release.ChannelStable},
	"bilibili-live-gift-panel/0.4.12": {version: Version0412, channel: release.ChannelStable},
}

// ChannelRouter maps one exact, reviewed User-Agent value to a closed channel.
type ChannelRouter struct {
	LegacyActive func(context.Context) (bool, error)
}

func (router ChannelRouter) Select(ctx context.Context, values []string) (release.Channel, error) {
	if len(values) != 1 {
		return "", ErrClientVersionInvalid
	}
	client, ok := reviewedUserAgents[values[0]]
	if !ok {
		return "", ErrClientVersionInvalid
	}
	if client.channel == release.ChannelLegacyRushRush {
		if router.LegacyActive == nil {
			return "", ErrLegacyChannelUnavailable
		}
		active, err := router.LegacyActive(ctx)
		if err != nil || !active {
			return "", ErrLegacyChannelUnavailable
		}
		return client.channel, nil
	}
	return client.channel, nil
}

func VersionBucketForUserAgent(values []string) VersionBucket {
	if len(values) != 1 {
		return VersionInvalid
	}
	client, ok := reviewedUserAgents[values[0]]
	if !ok {
		return VersionInvalid
	}
	return client.version
}
