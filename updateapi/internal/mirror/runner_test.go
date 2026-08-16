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
)

// Mutation caught: forwarding an untrusted ETag after corrupt state, or doing work after a 304 response.
func TestRunnerTreatsCorruptStateAsEmptyCacheAndStopsOnNotModified(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.state.loadErr = fmt.Errorf("%w: unsafe path C:/secret/state.json", ErrInvalidState)
	fixture.source.result = LatestResult{NotModified: true}

	result, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NotModified || !result.StateInvalid {
		t.Fatalf("Run() result = %+v, want not-modified with invalid-state signal", result)
	}
	if fixture.source.etag != "" {
		t.Fatalf("Latest() ETag = %q, want empty after invalid state", fixture.source.etag)
	}
	if len(fixture.fetcher.specs) != 0 || fixture.factoryCalls != 0 || fixture.state.saveCalls != 0 {
		t.Fatalf("304 side effects: downloads=%d factory=%d saves=%d", len(fixture.fetcher.specs), fixture.factoryCalls, fixture.state.saveCalls)
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
		if spec.Name != wantName || spec.URL != asset.DownloadURL || spec.Size != asset.Size {
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

// Mutation caught: ignoring companion cleanup failures reports success after publish/state save and hides the incomplete cleanup.
func TestRunnerReturnsSafeCleanupStageAfterPublishedStateCannotBeCleaned(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.fetcher.cleanupErr = errors.New("C:/private/artifact https://secret.invalid/?token=credential")

	result, err := fixture.runner().Run(context.Background(), RunOptions{})
	if err == nil || StageOf(err) != StageCleanup {
		t.Fatalf("Run() result=%+v error=%v stage=%q, want cleanup failure", result, err, StageOf(err))
	}
	if fixture.publisher.calls != 1 || fixture.state.saveCalls != 1 {
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
	etag   string
	result LatestResult
	err    error
}

func (source *fakeReleaseSource) Latest(_ context.Context, etag string) (LatestResult, error) {
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

func (fetcher *fakeArtifactFetcher) CleanupCompleted(_ context.Context, path string) error {
	fetcher.cleanupPaths = append(fetcher.cleanupPaths, path)
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
	return repository.saveErr
}

type fakePublisher struct {
	input   publish.Input
	outcome publish.Outcome
	err     error
	calls   int
}

func (publisher *fakePublisher) Publish(_ context.Context, input publish.Input) (publish.Outcome, error) {
	publisher.calls++
	publisher.input = input
	return publisher.outcome, publisher.err
}
