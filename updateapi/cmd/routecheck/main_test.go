package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoutecheckRunsClosedLocalMatrix(t *testing.T) {
	var output bytes.Buffer
	if err := run(&output); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"case=stable-0.4.9 status=200 channel=stable outcome=ok",
		"case=stable-0.4.10 status=200 channel=stable outcome=ok",
		"case=stable-0.4.11 status=200 channel=stable outcome=ok",
		"case=stable-0.4.12 status=200 channel=stable outcome=ok",
		"case=legacy-inactive status=503 channel=- outcome=legacy_channel_unavailable",
		"case=legacy-active-missing status=503 channel=legacy-rushrush outcome=release_unavailable",
		"case=legacy-active-malformed status=503 channel=legacy-rushrush outcome=release_invalid",
		"case=legacy-active-wrong-channel status=503 channel=legacy-rushrush outcome=release_invalid",
		"case=ua-missing status=400 channel=- outcome=client_version_invalid",
		"case=ua-duplicate status=400 channel=- outcome=client_version_invalid",
		"case=ua-malformed status=400 channel=- outcome=client_version_invalid",
		"case=ua-unknown status=400 channel=- outcome=client_version_invalid",
		"case=publisher-policy status=200 channel=- outcome=policy_verified",
		"routecheck=ok cases=13",
	}, "\n") + "\n"
	if output.String() != want {
		t.Fatalf("output =\n%s\nwant =\n%s", output.String(), want)
	}
	for _, forbidden := range []string{"http://", "https://", "channels/", "releases/", "trust/", "?"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("bounded output contains %q: %s", forbidden, output.String())
		}
	}
}
