// Command routecheck runs the deployment routing matrix entirely in memory.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	deploymentconfig "github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/config"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/httpapi"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/service"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

const (
	policySPKISHA256 = "5cd252fb0ce8932436faf8ccd1040981b89ee4ad6b9fe9e2a2b7e71aacb27cd3"
	fixtureDownload  = "https://fixture.invalid/download"
)

var (
	//go:embed testdata/policy-epoch-1.json
	policyFixture []byte
	//go:embed testdata/root-epoch-1-spki.b64
	policySPKIBase64 string
)

type fixtureStore struct {
	objects map[string][]byte
	reads   []string
}

func (store *fixtureStore) Get(_ context.Context, key string, maxBytes int64) ([]byte, string, error) {
	store.reads = append(store.reads, key)
	body, ok := store.objects[key]
	if !ok {
		return nil, "", errors.New("fixture object unavailable")
	}
	if int64(len(body)) > maxBytes {
		return append([]byte(nil), body...), "fixture-etag", nil
	}
	return append([]byte(nil), body...), "fixture-etag", nil
}

func (*fixtureStore) PresignGet(context.Context, string, time.Duration) (string, error) {
	return fixtureDownload, nil
}

type routeCase struct {
	name          string
	userAgents    []string
	legacyActive  bool
	legacyBody    []byte
	legacyPresent bool
	wantStatus    int
	wantChannel   string
	wantOutcome   string
}

func main() {
	if len(os.Args) != 1 || run(os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, "routecheck failed")
		os.Exit(1)
	}
}

func run(output io.Writer) error {
	if output == nil {
		return errors.New("routecheck output unavailable")
	}
	configuration, err := deploymentconfig.FromEnv(map[string]string{})
	if err != nil {
		return errors.New("routecheck configuration invalid")
	}
	stable := fixtureManifest(release.ChannelStable, "v0.4.12")
	legacy := fixtureManifest(release.ChannelLegacyRushRush, "v0.4.11")
	cases := []routeCase{
		{name: "stable-0.4.9", userAgents: []string{"bilibili-live-gift-panel/0.4.9"}, wantStatus: 200, wantChannel: "stable", wantOutcome: "ok"},
		{name: "stable-0.4.10", userAgents: []string{"bilibili-live-gift-panel/0.4.10"}, wantStatus: 200, wantChannel: "stable", wantOutcome: "ok"},
		{name: "stable-0.4.11", userAgents: []string{"bilibili-live-gift-panel/0.4.11"}, wantStatus: 200, wantChannel: "stable", wantOutcome: "ok"},
		{name: "stable-0.4.12", userAgents: []string{"bilibili-live-gift-panel/0.4.12"}, wantStatus: 200, wantChannel: "stable", wantOutcome: "ok"},
		{name: "legacy-inactive", userAgents: []string{"bilibili-live-gift-panel/0.4.7"}, legacyBody: legacy, legacyPresent: true, wantStatus: 503, wantOutcome: "legacy_channel_unavailable"},
		{name: "legacy-active-missing", userAgents: []string{"bilibili-live-gift-panel/0.4.7"}, legacyActive: true, wantStatus: 503, wantChannel: "legacy-rushrush", wantOutcome: "release_unavailable"},
		{name: "legacy-active-malformed", userAgents: []string{"bilibili-live-gift-panel/0.4.7"}, legacyActive: true, legacyBody: []byte(`{"schemaVersion":2}`), legacyPresent: true, wantStatus: 503, wantChannel: "legacy-rushrush", wantOutcome: "release_invalid"},
		{name: "legacy-active-wrong-channel", userAgents: []string{"bilibili-live-gift-panel/0.4.7"}, legacyActive: true, legacyBody: stable, legacyPresent: true, wantStatus: 503, wantChannel: "legacy-rushrush", wantOutcome: "release_invalid"},
		{name: "ua-missing", wantStatus: 400, wantOutcome: "client_version_invalid"},
		{name: "ua-duplicate", userAgents: []string{"bilibili-live-gift-panel/0.4.12", "bilibili-live-gift-panel/0.4.12"}, wantStatus: 400, wantOutcome: "client_version_invalid"},
		{name: "ua-malformed", userAgents: []string{"bilibili-live-gift-panel/0.4.12-rc.1"}, wantStatus: 400, wantOutcome: "client_version_invalid"},
		{name: "ua-unknown", userAgents: []string{"bilibili-live-gift-panel/0.4.13"}, wantStatus: 400, wantOutcome: "client_version_invalid"},
	}

	var result bytes.Buffer
	for _, test := range cases {
		outcome, channel, err := executeRouteCase(configuration.ObjectKeys, stable, test)
		if err != nil {
			return err
		}
		fmt.Fprintf(&result, "case=%s status=%d channel=%s outcome=%s\n", test.name, test.wantStatus, displayChannel(channel), outcome)
	}
	if err := verifyPolicyEndpoint(configuration.ObjectKeys); err != nil {
		return err
	}
	fmt.Fprintln(&result, "case=publisher-policy status=200 channel=- outcome=policy_verified")
	fmt.Fprintln(&result, "routecheck=ok cases=13")
	_, err = io.Copy(output, &result)
	return err
}

func executeRouteCase(keys service.ObjectKeys, stable []byte, test routeCase) (string, string, error) {
	objects := map[string][]byte{keys.StableChannel: stable}
	if test.legacyPresent {
		objects[keys.LegacyChannel] = test.legacyBody
	}
	store := &fixtureStore{objects: objects}
	releaseService, err := service.NewWithObjectKeys(store, func() time.Time { return time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC) }, keys)
	if err != nil {
		return "", "", errors.New("routecheck service construction failed")
	}
	router := service.ChannelRouter{LegacyActive: func(context.Context) (bool, error) { return test.legacyActive, nil }}
	handler := httpapi.New(releaseService, router, func() string { return strings.Repeat("0", 32) }, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/releases/latest", nil)
	request.Header.Del("User-Agent")
	for _, value := range test.userAgents {
		request.Header.Add("User-Agent", value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != test.wantStatus {
		return "", "", errors.New("routecheck status mismatch")
	}
	channel := response.Header().Get("X-Gift-Panel-Update-Channel")
	if channel != test.wantChannel {
		return "", "", errors.New("routecheck channel mismatch")
	}
	outcome := "ok"
	if response.Code != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
			return "", "", errors.New("routecheck error response invalid")
		}
		outcome = failure.Code
	}
	if outcome != test.wantOutcome {
		return "", "", errors.New("routecheck outcome mismatch")
	}
	if strings.HasPrefix(test.name, "legacy-") {
		if test.legacyActive {
			if len(store.reads) != 1 || store.reads[0] != keys.LegacyChannel {
				return "", "", errors.New("routecheck legacy fallback detected")
			}
		} else if len(store.reads) != 0 {
			return "", "", errors.New("routecheck inactive legacy read detected")
		}
	}
	return outcome, channel, nil
}

func verifyPolicyEndpoint(keys service.ObjectKeys) error {
	spki, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(policySPKIBase64))
	if err != nil {
		return errors.New("routecheck policy root invalid")
	}
	policy := bytes.TrimSpace(policyFixture)
	if _, err := trustpolicy.VerifySignedPolicy(policy, spki, policySPKISHA256, 0, time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		return errors.New("routecheck policy signature invalid")
	}
	store := &fixtureStore{objects: map[string][]byte{keys.PublisherPolicy: policy}}
	releaseService, err := service.NewWithObjectKeys(store, nil, keys)
	if err != nil {
		return errors.New("routecheck policy service construction failed")
	}
	handler := httpapi.New(releaseService, service.ChannelRouter{}, func() string { return strings.Repeat("0", 32) }, nil, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/trust/publisher-policy", nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), policy) || len(store.reads) != 1 || store.reads[0] != keys.PublisherPolicy {
		return errors.New("routecheck policy endpoint mismatch")
	}
	return nil
}

func fixtureManifest(channel release.Channel, tag string) []byte {
	body, err := json.Marshal(release.ChannelManifest{
		SchemaVersion: 2,
		Channel:       channel,
		TagName:       tag,
		PublishedAt:   "2026-09-01T00:00:00Z",
		Asset: release.AssetManifest{
			Name:      "gift-panel-windows-x64.exe",
			ObjectKey: "releases/" + tag + "/gift-panel-windows-x64.exe",
			Size:      1024,
			SHA256:    strings.Repeat("a", 64),
		},
		ChangelogObjectKey: "releases/" + tag + "/gift-panel-changelog.json",
	})
	if err != nil {
		panic("static routecheck manifest is invalid")
	}
	return body
}

func displayChannel(channel string) string {
	if channel == "" {
		return "-"
	}
	return channel
}
