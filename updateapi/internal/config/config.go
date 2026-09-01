// Package config validates the update API's closed deployment routing inputs.
package config

import "fmt"

const (
	stableChannelKey   = "channels/stable/latest.json"
	legacyChannelKey   = "channels/legacy-rushrush/latest.json"
	publisherPolicyKey = "trust/publisher/latest.json"
	stableChannelEnv   = "UPDATE_STABLE_CHANNEL_KEY"
	legacyChannelEnv   = "UPDATE_LEGACY_CHANNEL_KEY"
	legacyRoutingEnv   = "UPDATE_LEGACY_ROUTING_ACTIVE"
	publisherPolicyEnv = "UPDATE_PUBLISHER_POLICY_KEY"
)

// Config is the closed set of routing inputs accepted by the update API.
type Config struct {
	StableChannelKey    string
	LegacyChannelKey    string
	LegacyRoutingActive bool
	PublisherPolicyKey  string
}

// FromEnv validates a map containing only the reviewed routing variables.
// Missing variables use the production-safe reviewed defaults; explicitly
// supplied values must match the closed configuration exactly.
func FromEnv(values map[string]string) (Config, error) {
	for name := range values {
		switch name {
		case stableChannelEnv, legacyChannelEnv, legacyRoutingEnv, publisherPolicyEnv:
		default:
			return Config{}, fmt.Errorf("unknown update routing variable %q", name)
		}
	}

	configuration := Config{
		StableChannelKey:   stableChannelKey,
		LegacyChannelKey:   legacyChannelKey,
		PublisherPolicyKey: publisherPolicyKey,
	}
	for name, expected := range map[string]string{
		stableChannelEnv:   stableChannelKey,
		legacyChannelEnv:   legacyChannelKey,
		publisherPolicyEnv: publisherPolicyKey,
	} {
		if value, present := values[name]; present && value != expected {
			return Config{}, fmt.Errorf("%s must use the reviewed object key", name)
		}
	}

	if value, present := values[legacyRoutingEnv]; present {
		switch value {
		case "false":
			configuration.LegacyRoutingActive = false
		case "true":
			configuration.LegacyRoutingActive = true
		default:
			return Config{}, fmt.Errorf("%s must be exactly true or false", legacyRoutingEnv)
		}
	}
	return configuration, nil
}
