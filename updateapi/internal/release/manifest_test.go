package release_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

func validChannelManifest() release.ChannelManifest {
	return release.ChannelManifest{
		SchemaVersion: 2,
		Channel:       release.ChannelStable,
		TagName:       "v0.4.4",
		PublishedAt:   "2026-08-14T12:00:00Z",
		Asset: release.AssetManifest{
			Name:      "gift-panel-windows-x64.exe",
			ObjectKey: "releases/v0.4.4/gift-panel-windows-x64.exe",
			Size:      12_345_678,
			SHA256:    strings.Repeat("a", 64),
		},
		ChangelogObjectKey: "releases/v0.4.4/gift-panel-changelog.json",
	}
}

func TestParseChannelManifestTreatsSchemaOneAsStableOnly(t *testing.T) {
	body := []byte(`{"schemaVersion":1,"tagName":"v1.2.3","publishedAt":"2026-08-14T12:00:00Z","asset":{"name":"gift-panel-windows-x64.exe","objectKey":"releases/v1.2.3/gift-panel-windows-x64.exe","size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"changelogObjectKey":"releases/v1.2.3/gift-panel-changelog.json"}`)

	manifest, err := release.ParseChannelManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Channel != release.ChannelStable {
		t.Fatalf("schema 1 channel = %q, want stable", manifest.Channel)
	}
	if err := manifest.ValidateForChannel(release.ChannelLegacyRushRush); err == nil {
		t.Fatal("schema 1 manifest validated for legacy channel")
	}
}

func TestChannelManifestRejectsSchemaChannelMismatchAndUnknownChannel(t *testing.T) {
	tests := []struct {
		name    string
		schema  int
		channel release.Channel
		tag     string
		want    release.Channel
	}{
		{name: "schema 2 missing channel", schema: 2, tag: "v1.2.3", want: release.ChannelStable},
		{name: "schema 2 unknown channel", schema: 2, channel: release.Channel("preview"), tag: "v1.2.3", want: release.ChannelStable},
		{name: "stable body on legacy pointer", schema: 2, channel: release.ChannelStable, tag: "v1.2.3", want: release.ChannelLegacyRushRush},
		{name: "legacy body on stable pointer", schema: 2, channel: release.ChannelLegacyRushRush, tag: "v0.4.11", want: release.ChannelStable},
		{name: "legacy wrong tag", schema: 2, channel: release.ChannelLegacyRushRush, tag: "v0.4.10", want: release.ChannelLegacyRushRush},
		{name: "stable reserved bridge tag", schema: 2, channel: release.ChannelStable, tag: "v0.4.11", want: release.ChannelStable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validChannelManifest()
			manifest.SchemaVersion = test.schema
			manifest.Channel = test.channel
			setManifestTag(&manifest, test.tag)
			if err := manifest.ValidateForChannel(test.want); err == nil {
				t.Fatal("ValidateForChannel() error = nil, want rejection")
			}
		})
	}
}

func TestChannelManifestAcceptsOnlyExactLegacyBridge(t *testing.T) {
	manifest := validChannelManifest()
	manifest.Channel = release.ChannelLegacyRushRush
	setManifestTag(&manifest, "v0.4.11")
	if err := manifest.ValidateForChannel(release.ChannelLegacyRushRush); err != nil {
		t.Fatal(err)
	}
}

func TestParseStableTag(t *testing.T) {
	tests := []struct {
		name    string
		tag     string
		want    [3]uint64
		wantErr bool
	}{
		{name: "canonical tag", tag: "v0.4.4", want: [3]uint64{0, 4, 4}},
		{name: "numeric comparison data", tag: "v18446744073709551615.10.2", want: [3]uint64{18446744073709551615, 10, 2}},
		{name: "leading zero", tag: "v0.04.4", wantErr: true},
		{name: "missing v", tag: "0.4.4", wantErr: true},
		{name: "prerelease suffix", tag: "v0.4.4-rc.1", wantErr: true},
		{name: "build suffix", tag: "v0.4.4+build.1", wantErr: true},
		{name: "whitespace", tag: " v0.4.4 ", wantErr: true},
		{name: "overflow", tag: "v18446744073709551616.0.0", wantErr: true},
		{name: "non ASCII digits", tag: "v٠.4.4", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := release.ParseStableTag(test.tag)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseStableTag(%q) error = nil, want rejection", test.tag)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStableTag(%q) error = %v", test.tag, err)
			}
			if got != test.want {
				t.Fatalf("ParseStableTag(%q) = %v, want %v", test.tag, got, test.want)
			}
		})
	}
}

func TestChannelManifestValidateRejectsUntrustedValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*release.ChannelManifest)
	}{
		{
			name: "unsupported schema version",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.SchemaVersion = 3
			},
		},
		{
			name: "prerelease tag",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.TagName = "v0.4.4-rc.1"
			},
		},
		{
			name: "non semver tag",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.TagName = "v0.4"
			},
		},
		{
			name: "tag without v prefix",
			mutate: func(manifest *release.ChannelManifest) {
				setManifestTag(manifest, "0.4.4")
			},
		},
		{
			name: "tag with surrounding whitespace",
			mutate: func(manifest *release.ChannelManifest) {
				setManifestTag(manifest, " v0.4.4 ")
			},
		},
		{
			name: "tag with leading zero",
			mutate: func(manifest *release.ChannelManifest) {
				setManifestTag(manifest, "v0.04.4")
			},
		},
		{
			name: "tag with build metadata",
			mutate: func(manifest *release.ChannelManifest) {
				setManifestTag(manifest, "v0.4.4+repair")
			},
		},
		{
			name: "unexpected asset name",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.Asset.Name = "gift-panel-windows-arm64.exe"
			},
		},
		{
			name: "zero asset size",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.Asset.Size = 0
			},
		},
		{
			name: "oversized asset",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.Asset.Size = (256 << 20) + 1
			},
		},
		{
			name: "non hexadecimal sha256",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.Asset.SHA256 = strings.Repeat("g", 64)
			},
		},
		{
			name: "asset key traversal",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.Asset.ObjectKey = "releases/v0.4.4/../gift-panel-windows-x64.exe"
			},
		},
		{
			name: "asset key version mismatch",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.Asset.ObjectKey = "releases/v0.4.3/gift-panel-windows-x64.exe"
			},
		},
		{
			name: "changelog key outside release directory",
			mutate: func(manifest *release.ChannelManifest) {
				manifest.ChangelogObjectKey = "releases/v0.4.5/gift-panel-changelog.json"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validChannelManifest()
			test.mutate(&manifest)

			if err := manifest.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want rejection")
			}
		})
	}
}

func setManifestTag(manifest *release.ChannelManifest, tag string) {
	manifest.TagName = tag
	manifest.Asset.ObjectKey = "releases/" + tag + "/gift-panel-windows-x64.exe"
	manifest.ChangelogObjectKey = "releases/" + tag + "/gift-panel-changelog.json"
}

func TestChannelManifestParseRejectsInvalidJSONDocuments(t *testing.T) {
	valid, err := json.Marshal(validChannelManifest())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"unexpected":true}`)...)},
		{name: "second JSON value", data: append(append([]byte{}, valid...), []byte(` {}`)...)},
		{name: "invalid published timestamp", data: bytes.Replace(valid, []byte(`2026-08-14T12:00:00Z`), []byte(`not-a-timestamp`), 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := release.ParseChannelManifest(test.data); err == nil {
				t.Fatal("ParseChannelManifest() error = nil, want rejection")
			}
		})
	}
}

func TestChannelManifestPublicReturnsGitHubReleaseShape(t *testing.T) {
	manifest := validChannelManifest()

	public := manifest.Public("https://cos.example.invalid/signed-download")

	if public.TagName != "v0.4.4" {
		t.Fatalf("TagName = %q, want v0.4.4", public.TagName)
	}
	if public.Draft {
		t.Fatal("Draft = true, want false")
	}
	if public.Prerelease {
		t.Fatal("Prerelease = true, want false")
	}
	if len(public.Assets) != 1 {
		t.Fatalf("len(Assets) = %d, want 1", len(public.Assets))
	}
	asset := public.Assets[0]
	if asset.Name != "gift-panel-windows-x64.exe" {
		t.Fatalf("asset.Name = %q, want gift-panel-windows-x64.exe", asset.Name)
	}
	if asset.DownloadURL != "https://cos.example.invalid/signed-download" {
		t.Fatalf("asset.DownloadURL = %q, want signed download URL", asset.DownloadURL)
	}
	if asset.Size != 12_345_678 {
		t.Fatalf("asset.Size = %d, want 12345678", asset.Size)
	}
	if asset.Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("asset.Digest = %q, want manifest SHA-256 with sha256: prefix", asset.Digest)
	}
}

func TestParseChangelogAcceptsOnlyTrustedDocument(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{name: "valid document", data: []byte(`{"schemaVersion":1,"releases":[{"version":"0.4.4"}]}`)},
		{name: "unsupported schema version", data: []byte(`{"schemaVersion":2,"releases":[{}]}`), wantErr: true},
		{name: "empty releases", data: []byte(`{"schemaVersion":1,"releases":[]}`), wantErr: true},
		{name: "unknown field", data: []byte(`{"schemaVersion":1,"releases":[{}],"unexpected":true}`), wantErr: true},
		{name: "second JSON value", data: []byte(`{"schemaVersion":1,"releases":[{}]} {}`), wantErr: true},
		{name: "more than two mebibytes", data: append([]byte(`{"schemaVersion":1,"releases":[{}],"padding":"`), append(bytes.Repeat([]byte("x"), (2<<20)+1), []byte(`"}`)...)...), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := release.ParseChangelog(test.data)
			if test.wantErr {
				if err == nil {
					t.Fatal("ParseChangelog() error = nil, want rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if document.SchemaVersion != 1 {
				t.Fatalf("SchemaVersion = %d, want 1", document.SchemaVersion)
			}
			if len(document.Releases) != 1 {
				t.Fatalf("len(Releases) = %d, want 1", len(document.Releases))
			}
		})
	}
}
