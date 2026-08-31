package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/publish"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

func TestRunnerBridgeDiscoveryUsesByTagAndForwardsLegacyChannel(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.source.result = LatestResult{NotModified: true}
	fixture.state.loaded = validRunnerMirrorState(fixture)
	fixture.state.loaded.Tag = "v0.4.11"

	result, err := fixture.runner().Run(context.Background(), RunOptions{Channel: release.ChannelLegacyRushRush, Tag: "v0.4.11"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NotModified || fixture.source.method != "by-tag" || fixture.source.tag != "v0.4.11" {
		t.Fatalf("bridge discovery result=%+v method=%q tag=%q", result, fixture.source.method, fixture.source.tag)
	}
	if fixture.source.etag != fixture.state.loaded.ETag {
		t.Fatalf("ByTag ETag = %q, want channel-local %q", fixture.source.etag, fixture.state.loaded.ETag)
	}
}

func TestRunnerChannelNeverAccepts304FromOtherChannelState(t *testing.T) {
	tests := []struct {
		name    string
		options RunOptions
		prior   string
	}{
		{name: "legacy ignores stable state", options: RunOptions{Channel: release.ChannelLegacyRushRush, Tag: "v0.4.11"}, prior: "v1.2.3"},
		{name: "stable ignores legacy state", options: RunOptions{Channel: release.ChannelStable}, prior: "v0.4.11"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			fixture.source.result = LatestResult{NotModified: true}
			fixture.state.loaded = validRunnerMirrorState(fixture)
			fixture.state.loaded.Tag = test.prior

			result, err := fixture.runner().Run(context.Background(), test.options)
			if err == nil || StageOf(err) != StageDiscovery {
				t.Fatalf("cross-channel 304 result=%+v error=%v stage=%q", result, err, StageOf(err))
			}
			if fixture.source.etag != "" || result.NotModified {
				t.Fatalf("cross-channel discovery ETag=%q result=%+v", fixture.source.etag, result)
			}
		})
	}
}

func TestRunnerRejectsCrossChannelOptionsBeforeStateOrDiscovery(t *testing.T) {
	for _, options := range []RunOptions{
		{Channel: release.ChannelStable, Tag: "v0.4.11"},
		{Channel: release.ChannelLegacyRushRush},
		{Channel: release.ChannelLegacyRushRush, Tag: "v0.4.10"},
		{Channel: release.Channel("preview")},
	} {
		fixture := newRunnerFixture(t)
		if _, err := fixture.runner().Run(context.Background(), options); err == nil {
			t.Fatalf("Run(%+v) error = nil, want rejection", options)
		}
		if fixture.source.method != "" {
			t.Fatalf("Run(%+v) performed discovery via %q", options, fixture.source.method)
		}
	}
}

// Mutation caught: treating a 304 without a valid conditional state as success can permanently suppress repair.
func TestRunnerRejects304AfterDiscardingCorruptStateAndAllowsRetry(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.state.loadErr = fmt.Errorf("%w: unsafe path C:/secret/state.json", ErrInvalidState)
	fixture.source.result = LatestResult{NotModified: true}

	result, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err == nil || StageOf(err) != StageDiscovery {
		t.Fatalf("Run() result=%+v error=%v stage=%q, want failed closed discovery", result, err, StageOf(err))
	}
	if fixture.source.etag != "" {
		t.Fatalf("Latest() ETag = %q, want empty after invalid state", fixture.source.etag)
	}
	if len(fixture.fetcher.specs) != 0 || fixture.factoryCalls != 0 || fixture.state.saveCalls != 0 {
		t.Fatalf("rejected 304 side effects: downloads=%d factory=%d saves=%d", len(fixture.fetcher.specs), fixture.factoryCalls, fixture.state.saveCalls)
	}

	fixture.source.result = LatestResult{ETag: `"release-etag"`, Release: fixture.release}
	retry, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("retry after rejected 304: %v", err)
	}
	if !retry.StateInvalid || retry.NotModified || fixture.publisher.calls != 1 || fixture.state.saveCalls != 1 {
		t.Fatalf("retry result=%+v publishes=%d saves=%d", retry, fixture.publisher.calls, fixture.state.saveCalls)
	}
}

func TestRunnerRejects304WithoutMatchingPriorStateForLatestAndByTag(t *testing.T) {
	tests := []struct {
		name    string
		options RunOptions
	}{
		{name: "latest without state", options: RunOptions{Channel: release.ChannelStable}},
		{name: "by tag without state", options: RunOptions{Channel: release.ChannelLegacyRushRush, Tag: "v0.4.11"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			fixture.source.result = LatestResult{NotModified: true}
			result, err := fixture.runner().Run(context.Background(), test.options)
			if err == nil || StageOf(err) != StageDiscovery || result.NotModified {
				t.Fatalf("Run() result=%+v error=%v stage=%q", result, err, StageOf(err))
			}
			if fixture.source.etag != "" {
				t.Fatalf("discovery ETag = %q, want empty", fixture.source.etag)
			}
		})
	}
}

func TestRunnerRecoversCorruptFileStateThenUsesSavedETagWithoutRepublishing(t *testing.T) {
	// Mutation caught: treating corrupt state as a cache miss only during Load still leaves Save permanently blocked.
	fixture := newRunnerFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.directory, stateFileName), []byte(`{"etag":`), 0o600); err != nil {
		t.Fatal(err)
	}
	state := mustNewFileStateRepository(t, fixture.directory)
	runner := fixture.runner()
	runner.State = state

	first, err := runner.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if !first.StateInvalid || fixture.publisher.calls != 1 {
		t.Fatalf("first Run() result=%+v publishes=%d, want recovered publication", first, fixture.publisher.calls)
	}
	saved, err := state.Load()
	if err != nil {
		t.Fatalf("Load() after recovery error = %v", err)
	}
	if saved.ETag != fixture.source.result.ETag {
		t.Fatalf("saved ETag = %q, want %q", saved.ETag, fixture.source.result.ETag)
	}

	fixture.source.result = LatestResult{NotModified: true}
	second, err := runner.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if !second.NotModified || second.StateInvalid || fixture.source.etag != saved.ETag {
		t.Fatalf("second Run() result=%+v discovery ETag=%q, want clean 304 for %q", second, fixture.source.etag, saved.ETag)
	}
	if fixture.publisher.calls != 1 || len(fixture.fetcher.specs) != 4 {
		t.Fatalf("304 repeated work: publishes=%d downloads=%d", fixture.publisher.calls, len(fixture.fetcher.specs))
	}
}

func TestRunnerRecoversOversizedFileStateThenUsesSavedETagWithoutRepublishing(t *testing.T) {
	// Mutation caught: an oversized but safely identified state file can otherwise keep every validated run stuck at state-save.
	fixture := newRunnerFixture(t)
	writeOversizedStateFile(t, fixture.directory, maxStateBytes*4096)
	state := mustNewFileStateRepository(t, fixture.directory)
	runner := fixture.runner()
	runner.State = state

	first, err := runner.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if !first.StateInvalid || fixture.publisher.calls != 1 {
		t.Fatalf("first Run() result=%+v publishes=%d, want recovered publication", first, fixture.publisher.calls)
	}
	saved, err := state.Load()
	if err != nil {
		t.Fatalf("Load() after recovery error = %v", err)
	}

	fixture.source.result = LatestResult{NotModified: true}
	second, err := runner.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if !second.NotModified || second.StateInvalid || fixture.source.etag != saved.ETag {
		t.Fatalf("second Run() result=%+v discovery ETag=%q, want clean 304 for %q", second, fixture.source.etag, saved.ETag)
	}
	if fixture.publisher.calls != 1 || len(fixture.fetcher.specs) != 4 {
		t.Fatalf("304 repeated work: publishes=%d downloads=%d", fixture.publisher.calls, len(fixture.fetcher.specs))
	}
}

// Mutation caught: ignoring a valid prior state ETag defeats conditional discovery.
func TestRunnerPassesOnlyValidStateETagToDiscovery(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.state.loaded = validRunnerMirrorState(fixture)
	fixture.source.result = LatestResult{NotModified: true}

	if _, err := fixture.runner().Run(context.Background(), RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, want := fixture.source.etag, fixture.state.loaded.ETag; got != want {
		t.Fatalf("Latest() ETag = %q, want %q", got, want)
	}
}

func TestRunnerPersistsWeakGitHubDiscoveryETagForConditionalRequests(t *testing.T) {
	// Mutation caught: reusing the resumable-download strong ETag rule for
	// GitHub release discovery rejects GitHub's real W/"..." response ETags.
	fixture := newRunnerFixture(t)
	fixture.source.result.ETag = `W/"github-release"`
	state := mustNewFileStateRepository(t, fixture.directory)
	runner := fixture.runner()
	runner.State = state

	first, err := runner.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if first.Tag != "v1.2.3" || fixture.publisher.calls != 1 {
		t.Fatalf("first Run() result=%+v publishes=%d, want one publication", first, fixture.publisher.calls)
	}
	saved, err := state.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if saved.ETag != `W/"github-release"` {
		t.Fatalf("saved ETag = %q, want GitHub weak ETag", saved.ETag)
	}

	fixture.source.result = LatestResult{NotModified: true}
	second, err := runner.Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if !second.NotModified || fixture.source.etag != `W/"github-release"` {
		t.Fatalf("second Run() result=%+v discovery ETag=%q", second, fixture.source.etag)
	}
}

func TestRunnerTreatsChangedGitHubETagForSameReleaseIdentityAsNoOp(t *testing.T) {
	// GitHub release ETags include volatile metadata such as asset download
	// counts. Downloading a mirrored asset can therefore invalidate the ETag
	// without changing the immutable release identity.
	fixture := newRunnerFixture(t)
	prior := validRunnerMirrorState(fixture)
	fixture.state.loaded = prior
	fixture.source.result.ETag = `W/"download-count-changed"`

	result, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NotModified || result.Tag != fixture.release.Tag {
		t.Fatalf("Run() result = %+v, want tagged no-op", result)
	}
	if len(fixture.fetcher.specs) != 0 || fixture.factoryCalls != 0 || fixture.publisher.calls != 0 {
		t.Fatalf("same-release side effects: downloads=%d factory=%d publishes=%d", len(fixture.fetcher.specs), fixture.factoryCalls, fixture.publisher.calls)
	}
	want := prior
	want.ETag = fixture.source.result.ETag
	if fixture.state.saveCalls != 1 || fixture.state.saved != want {
		t.Fatalf("refreshed state: calls=%d state=%+v, want %+v", fixture.state.saveCalls, fixture.state.saved, want)
	}
}

// Mutation caught: reordering or omitting a required artifact can publish a partially validated release.
func TestRunnerDownloadsFourFixedAssetsBeforePublishingAndSavingState(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.publisher.outcome = publish.OutcomeStablePromoted

	result, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tag != fixture.release.Tag || result.Outcome != publish.OutcomeStablePromoted {
		t.Fatalf("Run() result = %+v", result)
	}
	wantNames := []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog}
	if len(fixture.fetcher.specs) != len(wantNames) {
		t.Fatalf("Download() calls = %d, want %d", len(fixture.fetcher.specs), len(wantNames))
	}
	for index, wantName := range wantNames {
		spec := fixture.fetcher.specs[index]
		asset := fixture.release.Assets[wantName]
		if spec.Name != wantName || spec.URL != asset.DownloadURL || spec.Size != asset.Size || spec.Resumable != (wantName == AssetExecutable) {
			t.Fatalf("Download() spec[%d] = %+v, want metadata for %q", index, spec, wantName)
		}
	}
	if fixture.publisher.calls != 1 || fixture.factoryCalls != 1 {
		t.Fatalf("publish calls=%d factory calls=%d, want one each", fixture.publisher.calls, fixture.factoryCalls)
	}
	input := fixture.publisher.input
	if input.Tag != fixture.release.Tag || input.PublishedAt != fixture.release.PublishedAt ||
		input.Prepared == nil || input.AssetPath != "" || input.ChecksumPath != "" || input.ChangelogPath != "" {
		t.Fatalf("Publish() input = %+v", input)
	}
	if fixture.state.saveCalls != 1 {
		t.Fatalf("Save() calls = %d, want 1", fixture.state.saveCalls)
	}
	wantDigest := sha256.Sum256(fixture.bodies[AssetExecutable])
	wantState := MirrorState{
		ETag:        fixture.source.result.ETag,
		Tag:         fixture.release.Tag,
		SHA256:      hex.EncodeToString(wantDigest[:]),
		PublishedAt: fixture.release.PublishedAt,
		CompletedAt: fixture.now,
	}
	if fixture.state.saved != wantState {
		t.Fatalf("Save() state = %+v, want %+v", fixture.state.saved, wantState)
	}
}

// Mutation caught: a loose checksum parser accepts extra records, alternate names, or whitespace variants.
func TestRunnerRejectsNonStrictChecksumBeforeCOS(t *testing.T) {
	for _, test := range []struct {
		name string
		body func(*runnerFixture) []byte
	}{
		{name: "extra record", body: func(f *runnerFixture) []byte {
			return append(append([]byte{}, f.bodies[AssetChecksum]...), []byte("\n"+strings.Repeat("0", 64)+"  other.exe")...)
		}},
		{name: "wrong filename", body: func(f *runnerFixture) []byte { return []byte(strings.Repeat("0", 64) + "  other.exe") }},
		{name: "single separator", body: func(f *runnerFixture) []byte {
			sum := sha256.Sum256(f.bodies[AssetExecutable])
			return []byte(hex.EncodeToString(sum[:]) + " " + AssetExecutable)
		}},
		{name: "non hex", body: func(*runnerFixture) []byte { return []byte(strings.Repeat("z", 64) + "  " + AssetExecutable) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			fixture.replaceBody(AssetChecksum, test.body(fixture))

			assertRunnerStageFailure(t, fixture, StageValidation)
		})
	}
}

// Mutation caught: trusting declared metadata instead of hashing and sizing the downloaded EXE permits substitution.
func TestRunnerVerifiesActualExecutableAgainstAllMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*runnerFixture)
	}{
		{name: "declared size", mutate: func(f *runnerFixture) {
			asset := f.release.Assets[AssetExecutable]
			asset.Size++
			f.release.Assets[AssetExecutable] = asset
		}},
		{name: "checksum digest", mutate: func(f *runnerFixture) {
			f.replaceBody(AssetChecksum, []byte(strings.Repeat("0", 64)+"  "+AssetExecutable))
		}},
		{name: "optional GitHub digest", mutate: func(f *runnerFixture) {
			asset := f.release.Assets[AssetExecutable]
			asset.Digest = "sha256:" + strings.Repeat("0", 64)
			f.release.Assets[AssetExecutable] = asset
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			test.mutate(fixture)

			assertRunnerStageFailure(t, fixture, StageValidation)
		})
	}
}

// Mutation caught: a permissive fallback-manifest decoder accepts schema drift or metadata inconsistent with the EXE.
func TestRunnerRequiresExactFallbackManifest(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{name: "wrong tag", mutate: func(body string) string {
			return strings.Replace(body, `"tag_name":"v1.2.3"`, `"tag_name":"v1.2.4"`, 1)
		}},
		{name: "draft", mutate: func(body string) string { return strings.Replace(body, `"draft":false`, `"draft":true`, 1) }},
		{name: "wrong asset name", mutate: func(body string) string { return strings.Replace(body, AssetExecutable, "other.exe", 1) }},
		{name: "wrong URL", mutate: func(body string) string {
			return strings.Replace(body, "releases/download/v1.2.3", "releases/download/v1.2.4", 1)
		}},
		{name: "wrong size", mutate: func(body string) string { return strings.Replace(body, `"size":21`, `"size":22`, 1) }},
		{name: "wrong digest", mutate: func(body string) string {
			marker := `"digest":"sha256:`
			index := strings.Index(body, marker)
			return body[:index+len(marker)] + "0" + body[index+len(marker)+1:]
		}},
		{name: "unknown field", mutate: func(body string) string {
			return strings.Replace(body, `"draft":false`, `"unknown":0,"draft":false`, 1)
		}},
		{name: "duplicate field", mutate: func(body string) string {
			return strings.Replace(body, `"draft":false`, `"draft":false,"draft":false`, 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			fixture.replaceBody(AssetManifest, []byte(test.mutate(string(fixture.bodies[AssetManifest]))))

			assertRunnerStageFailure(t, fixture, StageValidation)
		})
	}
}

// Mutation caught: parsing changelog syntax without proving the candidate version is present publishes mismatched notes.
func TestRunnerRequiresCandidateVersionInChangelog(t *testing.T) {
	for _, body := range []string{
		`{"schemaVersion":2,"releases":[{"version":"1.2.3"}]}`,
		`{"schemaVersion":1,"releases":[]}`,
		`{"schemaVersion":1,"releases":[{"version":"1.2.2"}]}`,
	} {
		fixture := newRunnerFixture(t)
		fixture.replaceBody(AssetChangelog, []byte(body))

		assertRunnerStageFailure(t, fixture, StageValidation)
	}
}

// Mutation caught: constructing COS before every local check makes dry-run and invalid releases reach credentials.
func TestRunnerDryRunCompletesLocalValidationWithoutCOSOrStateMutation(t *testing.T) {
	fixture := newRunnerFixture(t)

	result, err := fixture.runner().Run(context.Background(), RunOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Tag != fixture.release.Tag || result.Outcome != "" {
		t.Fatalf("Run() result = %+v", result)
	}
	if len(fixture.fetcher.specs) != 4 || fixture.factoryCalls != 0 || fixture.publisher.calls != 0 || fixture.state.saveCalls != 0 {
		t.Fatalf("dry-run side effects: downloads=%d factory=%d publishes=%d saves=%d", len(fixture.fetcher.specs), fixture.factoryCalls, fixture.publisher.calls, fixture.state.saveCalls)
	}
}

// Mutation caught: advancing state before a fully verified promoted-or-unchanged outcome can suppress a necessary retry.
func TestRunnerAdvancesStateOnlyForVerifiedPublisherOutcomes(t *testing.T) {
	for _, outcome := range []publish.Outcome{publish.OutcomeStablePromoted, publish.OutcomeStableUnchanged} {
		t.Run(string(outcome), func(t *testing.T) {
			fixture := newRunnerFixture(t)
			fixture.publisher.outcome = outcome

			result, err := fixture.runner().Run(context.Background(), RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != outcome || fixture.state.saveCalls != 1 {
				t.Fatalf("result=%+v save calls=%d", result, fixture.state.saveCalls)
			}
		})
	}

	fixture := newRunnerFixture(t)
	fixture.publisher.outcome = publish.Outcome("unexpected")
	_, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err == nil || StageOf(err) != StagePublish {
		t.Fatalf("Run() error = %v, stage = %q, want %q", err, StageOf(err), StagePublish)
	}
	if fixture.factoryCalls != 1 || fixture.publisher.calls != 1 || fixture.state.saveCalls != 0 {
		t.Fatalf("invalid-outcome side effects: factory=%d publish=%d saves=%d", fixture.factoryCalls, fixture.publisher.calls, fixture.state.saveCalls)
	}
}

// Mutation caught: wrapping dependency errors verbatim leaks URL queries, response bodies, credentials, or local paths.
func TestRunnerReturnsSafeRetryableStageErrorsWithoutSensitiveValues(t *testing.T) {
	secret := "https://secret.invalid/file?token=credential C:/private/state response-body"
	for _, test := range []struct {
		name      string
		wantStage Stage
		mutate    func(*runnerFixture)
	}{
		{name: "state", wantStage: StageState, mutate: func(f *runnerFixture) { f.state.loadErr = errors.New(secret) }},
		{name: "discovery", wantStage: StageDiscovery, mutate: func(f *runnerFixture) { f.source.err = errors.New(secret) }},
		{name: "fetch", wantStage: StageFetch, mutate: func(f *runnerFixture) { f.fetcher.errName, f.fetcher.err = AssetManifest, errors.New(secret) }},
		{name: "publisher factory", wantStage: StagePublisher, mutate: func(f *runnerFixture) { f.factoryErr = errors.New(secret) }},
		{name: "publish", wantStage: StagePublish, mutate: func(f *runnerFixture) { f.publisher.err = errors.New(secret) }},
		{name: "state save", wantStage: StageStateSave, mutate: func(f *runnerFixture) { f.state.saveErr = fmt.Errorf("%w: %s", ErrIndeterminateStateCommit, secret) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			test.mutate(fixture)

			_, err := fixture.runner().Run(context.Background(), RunOptions{})
			if err == nil || StageOf(err) != test.wantStage {
				t.Fatalf("Run() error = %v, stage = %q, want %q", err, StageOf(err), test.wantStage)
			}
			if strings.Contains(err.Error(), "secret.invalid") || strings.Contains(err.Error(), "token=") || strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "C:/private") || strings.Contains(err.Error(), "response-body") {
				t.Fatalf("Run() error leaked sensitive value: %v", err)
			}
			if test.wantStage != StageStateSave && fixture.state.saveCalls != 0 {
				t.Fatalf("Save() calls = %d after %s failure", fixture.state.saveCalls, test.wantStage)
			}
			if test.wantStage == StageStateSave && !errors.Is(err, ErrIndeterminateStateCommit) {
				t.Fatalf("Run() error = %v, want retryable indeterminate state cause", err)
			}
		})
	}
}

// Mutation caught: leaving complete downloads after any return grows persistent state and bypasses fresh validation.
func TestRunnerCleansCompleteArtifactsAfterSuccessAndFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*runnerFixture)
	}{
		{name: "success", mutate: func(*runnerFixture) {}},
		{name: "validation failure", mutate: func(f *runnerFixture) { f.replaceBody(AssetChangelog, []byte(`{"schemaVersion":1,"releases":[]}`)) }},
		{name: "publish failure", mutate: func(f *runnerFixture) { f.publisher.err = errors.New("retry") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			test.mutate(fixture)
			_, _ = fixture.runner().Run(context.Background(), RunOptions{})

			for _, path := range fixture.fetcher.returnedPaths {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("completed artifact %q remains, error=%v", filepath.Base(path), err)
				}
			}
		})
	}
}

// Mutation caught: committing state before cleanup hides a failed cleanup behind the new ETag.
func TestRunnerReturnsSafeCleanupStageAfterPublishedStateCannotBeCleaned(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.fetcher.cleanupErr = errors.New("C:/private/artifact https://secret.invalid/?token=credential")

	result, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err == nil || StageOf(err) != StageCleanup {
		t.Fatalf("Run() result=%+v error=%v stage=%q, want cleanup failure", result, err, StageOf(err))
	}
	if fixture.publisher.calls != 1 || fixture.state.saveCalls != 0 {
		t.Fatalf("cleanup ordering: publishes=%d saves=%d", fixture.publisher.calls, fixture.state.saveCalls)
	}
	if len(fixture.fetcher.cleanupPaths) != 4 {
		t.Fatalf("CleanupCompleted() calls = %d, want 4", len(fixture.fetcher.cleanupPaths))
	}
	for _, leaked := range []string{"C:/private", "secret.invalid", "token=", "credential"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("cleanup error leaked %q: %v", leaked, err)
		}
	}
}

func TestRunnerRetriesPublishedReleaseAfterCleanupFailureAndCommitsOnlyAfterCleanup(t *testing.T) {
	// Mutation caught: advancing ETag on the first run turns the next run into a 304 and strands completed artifacts.
	fixture := newRunnerFixture(t)
	fixture.fetcher.cleanupErr = errors.New("transient cleanup failure")
	first, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err == nil || StageOf(err) != StageCleanup || first.Tag != fixture.release.Tag {
		t.Fatalf("first Run() result=%+v error=%v, want cleanup retry", first, err)
	}
	if fixture.publisher.calls != 1 || fixture.state.saveCalls != 0 {
		t.Fatalf("first ordering: publishes=%d saves=%d", fixture.publisher.calls, fixture.state.saveCalls)
	}

	fixture.fetcher.cleanupErr = nil
	fixture.publisher.outcome = publish.OutcomeStableUnchanged
	second, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if second.Outcome != publish.OutcomeStableUnchanged || fixture.publisher.calls != 2 || fixture.state.saveCalls != 1 {
		t.Fatalf("second Run() result=%+v publishes=%d saves=%d", second, fixture.publisher.calls, fixture.state.saveCalls)
	}
	if fixture.source.etag != "" {
		t.Fatalf("retry discovery ETag = %q, want unadvanced cache", fixture.source.etag)
	}
	for _, path := range fixture.fetcher.returnedPaths[len(fixture.fetcher.returnedPaths)-4:] {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("retried completed artifact %q remains, error=%v", filepath.Base(path), statErr)
		}
	}
	fixture.source.result = LatestResult{NotModified: true}
	third, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err != nil || !third.NotModified {
		t.Fatalf("third Run() result=%+v error=%v, want cached 304", third, err)
	}
	if fixture.source.etag != fixture.state.saved.ETag || fixture.publisher.calls != 2 || fixture.state.saveCalls != 1 {
		t.Fatalf("post-cleanup cache: ETag=%q publishes=%d saves=%d", fixture.source.etag, fixture.publisher.calls, fixture.state.saveCalls)
	}
}

func TestRunnerCancellationDuringPostPublishCleanupDoesNotCommitState(t *testing.T) {
	// Mutation caught: saving after cancellation during cleanup can cache a release whose local cleanup never completed.
	fixture := newRunnerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.publisher.onPublish = cancel
	_, err := fixture.runner().Run(ctx, RunOptions{})
	if err == nil || StageOf(err) != StageCleanup || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v stage=%q, want canceled cleanup", err, StageOf(err))
	}
	if fixture.publisher.calls != 1 || fixture.state.saveCalls != 0 {
		t.Fatalf("canceled ordering: publishes=%d saves=%d", fixture.publisher.calls, fixture.state.saveCalls)
	}
}

// Mutation caught: reopening validated paths after the publisher factory runs allows a coherent replacement release to be published without strict validation.
func TestRunnerPublishesTheExactValidatedSnapshotAfterFactorySwapsFiles(t *testing.T) {
	fixture := newRunnerFixture(t)
	originalExecutable := append([]byte(nil), fixture.bodies[AssetExecutable]...)
	store := newRunnerSnapshotStore()
	fixture.newPublisher = func() (Publisher, error) {
		replacementExecutable := []byte("MZ replacement executable")
		replacementDigest := sha256.Sum256(replacementExecutable)
		replacements := map[string][]byte{
			AssetExecutable: replacementExecutable,
			AssetChecksum:   []byte(hex.EncodeToString(replacementDigest[:]) + "  " + AssetExecutable),
			AssetChangelog:  []byte(`{"schemaVersion":1,"releases":[{"version":"1.2.3","summary":"replacement"}]}`),
		}
		for name, body := range replacements {
			if err := os.WriteFile(filepath.Join(fixture.directory, name), body, 0o600); err != nil {
				return nil, err
			}
		}
		return publish.NewPublisher(store)
	}

	if _, err := fixture.runner().Run(context.Background(), RunOptions{}); err != nil {
		t.Fatal(err)
	}
	published := store.objects["releases/v1.2.3/"+AssetExecutable]
	if !bytes.Equal(published, originalExecutable) {
		t.Fatalf("published EXE = %q, want exact validated bytes %q", published, originalExecutable)
	}
}

func assertRunnerStageFailure(t *testing.T, fixture *runnerFixture, wantStage Stage) {
	t.Helper()
	_, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err == nil || StageOf(err) != wantStage {
		t.Fatalf("Run() error = %v, stage = %q, want %q", err, StageOf(err), wantStage)
	}
	if fixture.factoryCalls != 0 || fixture.publisher.calls != 0 || fixture.state.saveCalls != 0 {
		t.Fatalf("failure side effects: factory=%d publish=%d saves=%d", fixture.factoryCalls, fixture.publisher.calls, fixture.state.saveCalls)
	}
}

type runnerFixture struct {
	t            *testing.T
	directory    string
	now          time.Time
	bodies       map[string][]byte
	release      RemoteRelease
	source       *fakeReleaseSource
	fetcher      *fakeArtifactFetcher
	state        *fakeStateRepository
	publisher    *fakePublisher
	factoryCalls int
	factoryErr   error
	newPublisher PublisherFactory
}

func newRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	directory := t.TempDir()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	publishedAt := time.Date(2026, 8, 15, 10, 30, 0, 0, time.UTC)
	executable := []byte("MZ windows executable")
	digest := sha256.Sum256(executable)
	hexDigest := hex.EncodeToString(digest[:])
	downloadURL := "https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v1.2.3/" + AssetExecutable
	bodies := map[string][]byte{
		AssetExecutable: executable,
		AssetChecksum:   []byte(hexDigest + "  " + AssetExecutable),
		AssetManifest: []byte(fmt.Sprintf(
			`{"tag_name":"v1.2.3","draft":false,"prerelease":false,"assets":[{"name":"%s","browser_download_url":"%s","size":%d,"digest":"sha256:%s"}]}`,
			AssetExecutable, downloadURL, len(executable), hexDigest)),
		AssetChangelog: []byte(`{"schemaVersion":1,"releases":[{"version":"1.2.3"}]}`),
	}
	assets := make(map[string]RemoteAsset, len(bodies))
	for _, name := range []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog} {
		body := bodies[name]
		assets[name] = RemoteAsset{
			Name:        name,
			DownloadURL: "https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/v1.2.3/" + name,
			Size:        int64(len(body)),
		}
	}
	assets[AssetExecutable] = RemoteAsset{Name: AssetExecutable, DownloadURL: downloadURL, Size: int64(len(executable)), Digest: "sha256:" + hexDigest}
	release := RemoteRelease{Tag: "v1.2.3", PublishedAt: publishedAt, Assets: assets}
	source := &fakeReleaseSource{result: LatestResult{ETag: `"release-etag"`, Release: release}}
	fetcher := &fakeArtifactFetcher{directory: directory, bodies: bodies}
	return &runnerFixture{
		t:         t,
		directory: directory,
		now:       now,
		bodies:    bodies,
		release:   release,
		source:    source,
		fetcher:   fetcher,
		state:     &fakeStateRepository{},
		publisher: &fakePublisher{outcome: publish.OutcomeStablePromoted},
	}
}

func (fixture *runnerFixture) replaceBody(name string, body []byte) {
	fixture.bodies[name] = body
	fixture.fetcher.bodies = fixture.bodies
	asset := fixture.release.Assets[name]
	asset.Size = int64(len(body))
	fixture.release.Assets[name] = asset
	fixture.source.result.Release = fixture.release
}

func (fixture *runnerFixture) runner() *Runner {
	fixture.source.result.Release = fixture.release
	return &Runner{
		Source:  fixture.source,
		Fetcher: fixture.fetcher,
		State:   fixture.state,
		NewPublisher: func() (Publisher, error) {
			fixture.factoryCalls++
			if fixture.newPublisher != nil {
				return fixture.newPublisher()
			}
			if fixture.factoryErr != nil {
				return nil, fixture.factoryErr
			}
			return fixture.publisher, nil
		},
		Now: func() time.Time { return fixture.now },
	}
}

type runnerSnapshotStore struct {
	objects map[string][]byte
}

func newRunnerSnapshotStore() *runnerSnapshotStore {
	return &runnerSnapshotStore{objects: make(map[string][]byte)}
}

func (store *runnerSnapshotStore) Head(_ context.Context, key string) (cosstore.ObjectInfo, error) {
	body, ok := store.objects[key]
	if !ok {
		return cosstore.ObjectInfo{}, cosstore.ErrNotFound
	}
	digest := sha256.Sum256(body)
	return cosstore.ObjectInfo{Size: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}, nil
}

func (store *runnerSnapshotStore) PutImmutable(_ context.Context, key string, body io.Reader, size int64, _ string, _ string) error {
	if _, exists := store.objects[key]; exists {
		return cosstore.ErrAlreadyExists
	}
	return store.put(key, body, size)
}

func (store *runnerSnapshotStore) Put(_ context.Context, key string, body io.Reader, size int64, _ string, _ string) error {
	return store.put(key, body, size)
}

func (store *runnerSnapshotStore) put(key string, reader io.Reader, size int64) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if int64(len(body)) != size {
		return errors.New("test store size mismatch")
	}
	store.objects[key] = append([]byte(nil), body...)
	return nil
}

func (store *runnerSnapshotStore) Get(_ context.Context, key string, _ int64) ([]byte, string, error) {
	body, ok := store.objects[key]
	if !ok {
		return nil, "", cosstore.ErrNotFound
	}
	return append([]byte(nil), body...), "", nil
}

func validRunnerMirrorState(fixture *runnerFixture) MirrorState {
	digest := sha256.Sum256(fixture.bodies[AssetExecutable])
	return MirrorState{
		ETag:        `"prior-etag"`,
		Tag:         fixture.release.Tag,
		SHA256:      hex.EncodeToString(digest[:]),
		PublishedAt: fixture.release.PublishedAt,
		CompletedAt: fixture.release.PublishedAt.Add(time.Minute),
	}
}

type fakeReleaseSource struct {
	method string
	tag    string
	etag   string
	result LatestResult
	err    error
}

func (source *fakeReleaseSource) Latest(_ context.Context, etag string) (LatestResult, error) {
	source.method = "latest"
	source.etag = etag
	return source.result, source.err
}

func (source *fakeReleaseSource) ByTag(_ context.Context, tag, etag string) (LatestResult, error) {
	source.method = "by-tag"
	source.tag = tag
	source.etag = etag
	return source.result, source.err
}

type fakeArtifactFetcher struct {
	directory     string
	bodies        map[string][]byte
	specs         []DownloadSpec
	returnedPaths []string
	errName       string
	err           error
	cleanupPaths  []string
	cleanupErr    error
}

func (fetcher *fakeArtifactFetcher) Download(_ context.Context, spec DownloadSpec) (string, error) {
	fetcher.specs = append(fetcher.specs, spec)
	if spec.Name == fetcher.errName {
		return "", fetcher.err
	}
	path := filepath.Join(fetcher.directory, spec.Name)
	if err := os.WriteFile(path, fetcher.bodies[spec.Name], 0o600); err != nil {
		return "", err
	}
	fetcher.returnedPaths = append(fetcher.returnedPaths, path)
	return path, nil
}

func (fetcher *fakeArtifactFetcher) CleanupCompleted(ctx context.Context, path string) error {
	fetcher.cleanupPaths = append(fetcher.cleanupPaths, path)
	if err := ctx.Err(); err != nil {
		return err
	}
	if fetcher.cleanupErr != nil {
		return fetcher.cleanupErr
	}
	return os.Remove(path)
}

type fakeStateRepository struct {
	loaded    MirrorState
	loadErr   error
	saved     MirrorState
	saveErr   error
	saveCalls int
}

func (repository *fakeStateRepository) Load() (MirrorState, error) {
	return repository.loaded, repository.loadErr
}

func (repository *fakeStateRepository) Save(state MirrorState) error {
	repository.saveCalls++
	repository.saved = state
	if repository.saveErr == nil {
		repository.loaded = state
	}
	return repository.saveErr
}

type fakePublisher struct {
	input     publish.Input
	outcome   publish.Outcome
	err       error
	calls     int
	onPublish func()
}

func (publisher *fakePublisher) Publish(_ context.Context, input publish.Input) (publish.Outcome, error) {
	publisher.calls++
	publisher.input = input
	if publisher.onPublish != nil {
		publisher.onPublish()
	}
	return publisher.outcome, publisher.err
}
