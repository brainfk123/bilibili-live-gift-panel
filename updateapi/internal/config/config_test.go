package config

import (
	"bufio"
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
)

func TestRoutingEnvironmentRejectsUnknownAndDuplicateProcessEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
	}{
		{name: "typo", entries: []string{"UPDATE_LEGACY_ROUTING_ACTVE=true"}},
		{name: "unknown legacy namespace", entries: []string{"UPDATE_LEGACY_PRIVATE_MARKER=true"}},
		{name: "unknown stable namespace", entries: []string{"UPDATE_STABLE_PRIVATE_MARKER=channels/private/latest.json"}},
		{name: "unknown publisher namespace", entries: []string{"UPDATE_PUBLISHER_PRIVATE_MARKER=trust/private.json"}},
		{name: "reviewed name with arbitrary value", entries: []string{"UPDATE_STABLE_CHANNEL_KEY=private-marker"}},
		{name: "case variant", entries: []string{"update_legacy_routing_active=true"}},
		{name: "duplicate", entries: []string{"UPDATE_LEGACY_ROUTING_ACTIVE=false", "UPDATE_LEGACY_ROUTING_ACTIVE=true"}},
		{name: "malformed owned entry", entries: []string{"UPDATE_LEGACY_ROUTING_ACTIVE"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := FromEnviron(test.entries)
			if err == nil {
				t.Fatal("FromEnviron() error = nil, want rejection")
			}
			if err.Error() != "invalid update routing environment" {
				t.Fatalf("error = %q, want generic classification", err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "private") || strings.Contains(err.Error(), "ACTVE") {
				t.Fatalf("error exposed environment name or value: %q", err)
			}
		})
	}
}

func TestRoutingEnvironmentIgnoresNormalUnrelatedProcessEntries(t *testing.T) {
	cfg, err := FromEnviron([]string{
		"PATH=C:\\Windows\\System32",
		"UPDATE_API_LISTEN=127.0.0.1:12450",
		"COS_BUCKET=private-marker",
		"UPDATE_LEGACY_ROUTING_ACTIVE=false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LegacyRoutingActive {
		t.Fatal("unrelated process entries changed routing activation")
	}
}

const (
	stableKey = "channels/stable/latest.json"
	legacyKey = "channels/legacy-rushrush/latest.json"
	policyKey = "trust/publisher/latest.json"
)

func TestLegacyRoutingDefaultsInactive(t *testing.T) {
	cfg, err := FromEnv(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LegacyRoutingActive {
		t.Fatal("legacy routing must default inactive")
	}

	router := service.ChannelRouter{LegacyActive: func(context.Context) (bool, error) {
		return cfg.LegacyRoutingActive, nil
	}}
	if _, err := router.Select(context.Background(), []string{"bilibili-live-gift-panel/0.4.7"}); err != service.ErrLegacyChannelUnavailable {
		t.Fatalf("v0.4.7 route error = %v, want controlled legacy unavailable", err)
	}
}

func TestLegacyRoutingAcceptsOnlyExactReviewedBooleans(t *testing.T) {
	for _, value := range []string{"", "TRUE", "False", "1", "0", "yes", " true"} {
		t.Run(value, func(t *testing.T) {
			if _, err := FromEnv(map[string]string{"UPDATE_LEGACY_ROUTING_ACTIVE": value}); err == nil {
				t.Fatalf("UPDATE_LEGACY_ROUTING_ACTIVE=%q accepted, want rejection", value)
			}
		})
	}

	for value, want := range map[string]bool{"false": false, "true": true} {
		t.Run("accepted_"+value, func(t *testing.T) {
			cfg, err := FromEnv(map[string]string{"UPDATE_LEGACY_ROUTING_ACTIVE": value})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LegacyRoutingActive != want {
				t.Fatalf("LegacyRoutingActive = %v, want %v", cfg.LegacyRoutingActive, want)
			}
		})
	}
}

func TestChannelConfigurationUsesOnlyReviewedObjectKeys(t *testing.T) {
	cfg, err := FromEnv(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ObjectKeys.StableChannel != stableKey || cfg.ObjectKeys.LegacyChannel != legacyKey || cfg.ObjectKeys.PublisherPolicy != policyKey {
		t.Fatalf("default object keys = %#v, want reviewed stable, legacy, and policy keys", cfg)
	}

	tests := []map[string]string{
		{"UPDATE_STABLE_CHANNEL_KEY": ""},
		{"UPDATE_STABLE_CHANNEL_KEY": "channels/preview/latest.json"},
		{"UPDATE_LEGACY_CHANNEL_KEY": ""},
		{"UPDATE_LEGACY_CHANNEL_KEY": stableKey},
		{"UPDATE_PUBLISHER_POLICY_KEY": ""},
		{"UPDATE_PUBLISHER_POLICY_KEY": "trust/publisher/candidate.json"},
		{"UPDATE_ARBITRARY_CHANNEL_KEY": "channels/arbitrary/latest.json"},
	}
	for _, env := range tests {
		for name, value := range env {
			t.Run(name+"="+value, func(t *testing.T) {
				if _, err := FromEnv(env); err == nil {
					t.Fatalf("FromEnv(%#v) error = nil, want fail-closed rejection", env)
				}
			})
		}
	}
}

func TestChannelConfigurationDeploymentArtifactsAreIsolated(t *testing.T) {
	deployRoot := filepath.Clean(filepath.Join("..", "..", "..", "deploy", "update-api"))
	apiEnv := parseEnvironmentFile(t, filepath.Join(deployRoot, "gift-panel-update-api.env.example"))
	routingEnv := map[string]string{}
	for _, name := range []string{
		"UPDATE_STABLE_CHANNEL_KEY",
		"UPDATE_LEGACY_CHANNEL_KEY",
		"UPDATE_LEGACY_ROUTING_ACTIVE",
		"UPDATE_PUBLISHER_POLICY_KEY",
	} {
		value, ok := apiEnv[name]
		if !ok {
			t.Fatalf("API environment omits required routing variable %s", name)
		}
		routingEnv[name] = value
	}
	cfg, err := FromEnv(routingEnv)
	if err != nil {
		t.Fatalf("API environment is not a closed configuration: %v", err)
	}
	if cfg.LegacyRoutingActive {
		t.Fatal("deployed example activates legacy routing")
	}
	for name := range apiEnv {
		if strings.Contains(strings.ToUpper(name), "KMS") {
			t.Fatalf("API environment grants a KMS provider input %s", name)
		}
	}

	stable := parseUnit(t, filepath.Join(deployRoot, "gift-panel-release-mirror.service"))
	legacy := parseUnit(t, filepath.Join(deployRoot, "gift-panel-legacy-release-mirror.service"))
	for _, directive := range []string{"User", "Group", "EnvironmentFile", "StateDirectory", "LogNamespace"} {
		if stable.service[directive] == "" || legacy.service[directive] == "" {
			t.Fatalf("%s must be explicit in both mirror units", directive)
		}
		if stable.service[directive] == legacy.service[directive] {
			t.Fatalf("mirror units share %s=%q", directive, stable.service[directive])
		}
	}
	if got := strings.Fields(stable.service["ExecStart"]); !equalStrings(got[1:], []string{"--channel", "stable"}) {
		t.Fatalf("stable ExecStart arguments = %#v, want explicit stable channel", got[1:])
	}
	if got := strings.Fields(legacy.service["ExecStart"]); !equalStrings(got[1:], []string{"--channel", "legacy-rushrush", "--tag", "v0.4.11", "--state-dir", "/var/lib/gift-panel-legacy-release-mirror"}) {
		t.Fatalf("legacy ExecStart arguments = %#v, want exact bridge channel, tag, and isolated state base", got[1:])
	}
	stableState := path.Join(stable.execStateBase(), stable.execChannel())
	legacyState := path.Join(legacy.execStateBase(), legacy.execChannel())
	if stableState == legacyState || path.Dir(stableState) == path.Dir(legacyState) {
		t.Fatalf("mirror runtime state is not isolated: stable=%s legacy=%s", stableState, legacyState)
	}

	for _, unit := range []parsedUnit{stable, legacy, parseUnit(t, filepath.Join(deployRoot, "gift-panel-update-api.service"))} {
		for name := range unit.environment {
			if strings.Contains(strings.ToUpper(name), "KMS") {
				t.Fatalf("unit %s receives KMS environment %s", unit.path, name)
			}
		}
	}
	for _, unit := range []parsedUnit{stable, legacy} {
		exampleName := strings.TrimPrefix(unit.service["EnvironmentFile"], "/etc/") + ".example"
		environment := parseEnvironmentFile(t, filepath.Join(deployRoot, exampleName))
		for name := range environment {
			if strings.Contains(strings.ToUpper(name), "KMS") {
				t.Fatalf("credential example %s grants a KMS provider input", exampleName)
			}
		}
		if len(environment) != 4 {
			t.Fatalf("credential example %s exposes %d variables, want four COS inputs only", exampleName, len(environment))
		}
		for _, name := range []string{"COS_BUCKET", "COS_REGION", "COS_SECRET_ID", "COS_SECRET_KEY"} {
			if _, ok := environment[name]; !ok {
				t.Fatalf("credential example %s omits %s", exampleName, name)
			}
		}
	}
	legacyTimers, err := filepath.Glob(filepath.Join(deployRoot, "*legacy*.timer"))
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyTimers) != 0 {
		t.Fatalf("legacy scheduler is installed by default: %#v", legacyTimers)
	}

	if stable.install["WantedBy"] != "" || legacy.install["WantedBy"] != "" {
		t.Fatal("oneshot mirror services must not be enableable schedulers")
	}
	if release.Channel(stable.execChannel()) != release.ChannelStable || release.Channel(legacy.execChannel()) != release.ChannelLegacyRushRush {
		t.Fatal("mirror unit channel arguments do not resolve to their closed channels")
	}
}

func parseEnvironmentFile(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || name == "" {
			t.Fatalf("invalid environment assignment %q in %s", line, path)
		}
		if _, exists := values[name]; exists {
			t.Fatalf("duplicate environment assignment %s in %s", name, path)
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

type parsedUnit struct {
	path        string
	service     map[string]string
	install     map[string]string
	environment map[string]string
}

func parseUnit(t *testing.T, path string) parsedUnit {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	unit := parsedUnit{path: path, service: map[string]string{}, install: map[string]string{}, environment: map[string]string{}}
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid unit directive %q in %s", line, path)
		}
		switch section {
		case "Service":
			unit.service[name] = value
			if name == "Environment" {
				envName, envValue, ok := strings.Cut(value, "=")
				if !ok {
					t.Fatalf("invalid Environment directive %q in %s", value, path)
				}
				unit.environment[envName] = envValue
			}
		case "Install":
			unit.install[name] = value
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return unit
}

func (unit parsedUnit) execChannel() string {
	fields := strings.Fields(unit.service["ExecStart"])
	for i := 1; i+1 < len(fields); i++ {
		if fields[i] == "--channel" {
			return fields[i+1]
		}
	}
	return ""
}

func (unit parsedUnit) execStateBase() string {
	fields := strings.Fields(unit.service["ExecStart"])
	for i := 1; i+1 < len(fields); i++ {
		if fields[i] == "--state-dir" {
			return fields[i+1]
		}
	}
	return "/var/lib/gift-panel-release-mirror"
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
