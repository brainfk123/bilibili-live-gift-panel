// Package config validates the update API's closed deployment routing inputs.
package config

import (
	"errors"
	"strings"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

const (
	stableChannelEnv   = "UPDATE_STABLE_CHANNEL_KEY"
	legacyChannelEnv   = "UPDATE_LEGACY_CHANNEL_KEY"
	legacyRoutingEnv   = "UPDATE_LEGACY_ROUTING_ACTIVE"
	publisherPolicyEnv = "UPDATE_PUBLISHER_POLICY_KEY"
)

var errInvalidRoutingEnvironment = errors.New("invalid update routing environment")

// Config is the closed set of routing inputs accepted by the update API.
type Config struct {
	ObjectKeys          service.ObjectKeys
	LegacyRoutingActive bool
}

// FromEnv validates a map containing only the reviewed routing variables.
// Missing variables use the production-safe reviewed defaults; explicitly
// supplied values must match the closed configuration exactly.
func FromEnv(values map[string]string) (Config, error) {
	for name := range values {
		switch name {
		case stableChannelEnv, legacyChannelEnv, legacyRoutingEnv, publisherPolicyEnv:
		default:
			return Config{}, errInvalidRoutingEnvironment
		}
	}
	return fromValues(values)
}

// FromEnviron enumerates process environment entries in the closed routing
// namespaces before applying defaults. Unrelated process variables are ignored;
// every unknown, malformed, or duplicate owned entry fails closed.
func FromEnviron(entries []string) (Config, error) {
	values := make(map[string]string)
	for _, entry := range entries {
		name, value, hasValue := strings.Cut(entry, "=")
		upperName := strings.ToUpper(name)
		owned := strings.HasPrefix(upperName, "UPDATE_STABLE_") ||
			strings.HasPrefix(upperName, "UPDATE_LEGACY_") ||
			strings.HasPrefix(upperName, "UPDATE_PUBLISHER_")
		if !owned {
			continue
		}
		if !hasValue {
			return Config{}, errInvalidRoutingEnvironment
		}
		switch name {
		case stableChannelEnv, legacyChannelEnv, legacyRoutingEnv, publisherPolicyEnv:
		default:
			return Config{}, errInvalidRoutingEnvironment
		}
		if _, duplicate := values[name]; duplicate {
			return Config{}, errInvalidRoutingEnvironment
		}
		values[name] = value
	}
	return fromValues(values)
}

func fromValues(values map[string]string) (Config, error) {

	reviewed := service.ReviewedObjectKeys()
	configuration := Config{ObjectKeys: reviewed}
	for name, expected := range map[string]string{
		stableChannelEnv:   reviewed.StableChannel,
		legacyChannelEnv:   reviewed.LegacyChannel,
		publisherPolicyEnv: reviewed.PublisherPolicy,
	} {
		if value, present := values[name]; present && value != expected {
			return Config{}, errInvalidRoutingEnvironment
		}
	}

	if value, present := values[legacyRoutingEnv]; present {
		switch value {
		case "false":
			configuration.LegacyRoutingActive = false
		case "true":
			configuration.LegacyRoutingActive = true
		default:
			return Config{}, errInvalidRoutingEnvironment
		}
	}
	return configuration, nil
}
