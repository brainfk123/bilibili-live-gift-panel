package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/publish"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

const githubReleaseDownloadBase = "https://github.com/brainfk123/bilibili-live-gift-panel/releases/download/"

// Publisher is the verified COS transaction used after local validation.
type Publisher interface {
	Publish(context.Context, publish.Input) (publish.Outcome, error)
}

// PublisherFactory constructs the COS publisher lazily, after local validation.
type PublisherFactory func() (Publisher, error)

type RunOptions struct {
	DryRun bool
}

type RunResult struct {
	Tag          string
	Outcome      publish.Outcome
	NotModified  bool
	DryRun       bool
	StateInvalid bool
}

// Stage is a stable, non-sensitive transaction phase identifier.
type Stage string

const (
	StageConfiguration Stage = "configuration"
	StageState         Stage = "state"
	StageDiscovery     Stage = "discovery"
	StageFetch         Stage = "fetch"
	StageValidation    Stage = "validation"
	StagePublisher     Stage = "publisher"
	StagePublish       Stage = "publish"
	StageStateSave     Stage = "state-save"
	StageCleanup       Stage = "cleanup"
)

type runError struct {
	stage Stage
	tag   string
	cause error
}

func (err *runError) Error() string {
	if err.tag == "" {
		return fmt.Sprintf("mirror failed: stage=%s", err.stage)
	}
	return fmt.Sprintf("mirror failed: stage=%s tag=%s", err.stage, err.tag)
}

func (err *runError) Unwrap() error { return err.cause }

// StageOf extracts a safe transaction stage from a runner error.
func StageOf(err error) Stage {
	var failure *runError
	if errors.As(err, &failure) {
		return failure.stage
	}
	return ""
}

// TagOf extracts the already validated canonical tag from a runner error.
func TagOf(err error) string {
	var failure *runError
	if errors.As(err, &failure) {
		return failure.tag
	}
	return ""
}

func runnerFailure(stage Stage, tag string, cause error) error {
	if _, err := release.ParseStableTag(tag); err != nil {
		tag = ""
	}
	return &runError{stage: stage, tag: tag, cause: cause}
}

// Runner coordinates discovery, local verification, publication, and state commit.
type Runner struct {
	Source       ReleaseSource
	Fetcher      ArtifactFetcher
	State        StateRepository
	NewPublisher PublisherFactory
	Now          func() time.Time
}

// Run performs one transaction. It asks the fetcher to remove each exact
// completed artifact on return and reports cleanup failures explicitly; the
// downloader alone owns any resumable EXE partial state.
func (runner *Runner) Run(ctx context.Context, options RunOptions) (result RunResult, runErr error) {
	if runner == nil || runner.Source == nil || runner.Fetcher == nil || runner.State == nil {
		return RunResult{}, runnerFailure(StageConfiguration, "", errors.New("runner dependencies are incomplete"))
	}

	prior, err := runner.State.Load()
	stateInvalid := false
	if errors.Is(err, ErrInvalidState) {
		prior = MirrorState{}
		stateInvalid = true
	} else if err != nil {
		return RunResult{}, runnerFailure(StageState, "", err)
	}

	latest, err := runner.Source.Latest(ctx, prior.ETag)
	if err != nil {
		return RunResult{StateInvalid: stateInvalid}, runnerFailure(StageDiscovery, "", err)
	}
	if latest.NotModified {
		return RunResult{NotModified: true, StateInvalid: stateInvalid}, nil
	}

	candidate := latest.Release
	if _, err := release.ParseStableTag(candidate.Tag); err != nil || candidate.PublishedAt.IsZero() || !isConditionalETag(latest.ETag) {
		return RunResult{StateInvalid: stateInvalid}, runnerFailure(StageValidation, candidate.Tag, errors.New("release identity is invalid"))
	}
	tag := candidate.Tag

	paths := make(map[string]string, 4)
	cleanupAttempted := false
	cleanupCompleted := func() error {
		if len(paths) == 0 {
			return nil
		}
		cleaner, ok := runner.Fetcher.(CompletedArtifactCleaner)
		var cleanupErrors []error
		if !ok {
			cleanupErrors = append(cleanupErrors, errors.New("artifact fetcher does not support owned cleanup"))
		} else {
			for _, name := range []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog} {
				path, exists := paths[name]
				if !exists {
					continue
				}
				if err := cleaner.CleanupCompleted(ctx, path); err != nil {
					cleanupErrors = append(cleanupErrors, err)
				}
			}
		}
		if len(cleanupErrors) != 0 {
			return errors.Join(cleanupErrors...)
		}
		clear(paths)
		return nil
	}
	defer func() {
		if cleanupAttempted || len(paths) == 0 {
			return
		}
		if cleanupErr := cleanupCompleted(); cleanupErr != nil {
			runErr = runnerFailure(StageCleanup, tag, errors.Join(cleanupErr, runErr))
		}
	}()

	for _, name := range []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog} {
		asset, ok := candidate.Assets[name]
		limit, allowed := assetLimit(name)
		if !ok || !allowed || asset.Name != name {
			return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageValidation, tag, errors.New("required asset metadata is invalid"))
		}
		path, err := runner.Fetcher.Download(ctx, DownloadSpec{
			Name: name, URL: asset.DownloadURL, Size: asset.Size, MaxBytes: limit, Resumable: name == AssetExecutable,
		})
		if err != nil {
			return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageFetch, tag, err)
		}
		paths[name] = path
	}

	bodies := make(map[string][]byte, 4)
	for _, name := range []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog} {
		body, err := readFetchedArtifact(paths[name], name, candidate.Assets[name].Size)
		if err != nil {
			return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageValidation, tag, err)
		}
		if err := validateOptionalGitHubDigest(candidate.Assets[name].Digest, body); err != nil {
			return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageValidation, tag, err)
		}
		bodies[name] = body
	}

	executableDigest := sha256.Sum256(bodies[AssetExecutable])
	hexDigest := hex.EncodeToString(executableDigest[:])
	if err := validateStrictChecksum(bodies[AssetChecksum], hexDigest); err != nil {
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageValidation, tag, err)
	}
	if err := validateFallbackManifest(bodies[AssetManifest], candidate, hexDigest, int64(len(bodies[AssetExecutable]))); err != nil {
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageValidation, tag, err)
	}
	if err := validateCandidateChangelog(bodies[AssetChangelog], tag); err != nil {
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageValidation, tag, err)
	}

	if options.DryRun {
		return RunResult{Tag: tag, DryRun: true, StateInvalid: stateInvalid}, nil
	}
	prepared, err := publish.NewPreparedRelease(bodies[AssetExecutable], bodies[AssetChecksum], bodies[AssetChangelog])
	if err != nil {
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StageValidation, tag, err)
	}
	if runner.NewPublisher == nil {
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StagePublisher, tag, errors.New("publisher factory is required"))
	}
	publisher, err := runner.NewPublisher()
	if err != nil || publisher == nil {
		if err == nil {
			err = errors.New("publisher factory returned nil")
		}
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StagePublisher, tag, err)
	}
	outcome, err := publisher.Publish(ctx, publish.Input{
		Tag:         tag,
		PublishedAt: candidate.PublishedAt.UTC(),
		Prepared:    prepared,
	})
	if err != nil {
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StagePublish, tag, err)
	}
	if outcome != publish.OutcomeStablePromoted && outcome != publish.OutcomeStableUnchanged {
		return RunResult{Tag: tag, StateInvalid: stateInvalid}, runnerFailure(StagePublish, tag, errors.New("publisher outcome is invalid"))
	}
	cleanupAttempted = true
	if err := cleanupCompleted(); err != nil {
		return RunResult{Tag: tag, Outcome: outcome, StateInvalid: stateInvalid}, runnerFailure(StageCleanup, tag, err)
	}

	now := time.Now
	if runner.Now != nil {
		now = runner.Now
	}
	completedAt := now().UTC().Round(0)
	state := MirrorState{
		ETag:        latest.ETag,
		Tag:         tag,
		SHA256:      hexDigest,
		PublishedAt: candidate.PublishedAt.UTC().Round(0),
		CompletedAt: completedAt,
	}
	if err := runner.State.Save(state); err != nil {
		return RunResult{Tag: tag, Outcome: outcome, StateInvalid: stateInvalid}, runnerFailure(StageStateSave, tag, err)
	}
	return RunResult{Tag: tag, Outcome: outcome, StateInvalid: stateInvalid}, nil
}

func readFetchedArtifact(path, name string, declaredSize int64) ([]byte, error) {
	if path == "" || filepath.Base(path) != name || declaredSize <= 0 {
		return nil, errors.New("downloaded artifact identity is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != declaredSize {
		return nil, errors.New("downloaded artifact is not the declared regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("downloaded artifact cannot be opened")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.New("downloaded artifact changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, declaredSize+1))
	if err != nil || int64(len(body)) != declaredSize {
		return nil, errors.New("downloaded artifact bytes do not match declared size")
	}
	return body, nil
}

func validateOptionalGitHubDigest(digest string, body []byte) error {
	if digest == "" {
		return nil
	}
	sum := sha256.Sum256(body)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if digest != want {
		return errors.New("GitHub artifact digest does not match downloaded bytes")
	}
	return nil
}

func validateStrictChecksum(body []byte, wantDigest string) error {
	line := body
	if bytes.HasSuffix(line, []byte("\r\n")) {
		line = line[:len(line)-2]
	} else if bytes.HasSuffix(line, []byte("\n")) {
		line = line[:len(line)-1]
	}
	wantLength := sha256.Size*2 + 2 + len(AssetExecutable)
	if len(line) != wantLength || !bytes.Equal(line[sha256.Size*2:sha256.Size*2+2], []byte("  ")) || string(line[sha256.Size*2+2:]) != AssetExecutable {
		return errors.New("checksum format is invalid")
	}
	digest := line[:sha256.Size*2]
	decoded, err := hex.DecodeString(string(digest))
	if err != nil || !strings.EqualFold(string(digest), wantDigest) || len(decoded) != sha256.Size {
		return errors.New("checksum digest does not match executable")
	}
	return nil
}

type fallbackManifest struct {
	TagName    string          `json:"tag_name"`
	Draft      *bool           `json:"draft"`
	Prerelease *bool           `json:"prerelease"`
	Assets     []fallbackAsset `json:"assets"`
}

type fallbackAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
}

func validateFallbackManifest(body []byte, candidate RemoteRelease, digest string, size int64) error {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return errors.New("fallback manifest JSON is ambiguous")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest fallbackManifest
	if err := decoder.Decode(&manifest); err != nil || ensureJSONEOF(decoder) != nil {
		return errors.New("fallback manifest JSON is invalid")
	}
	if manifest.Draft == nil || *manifest.Draft || manifest.Prerelease == nil || *manifest.Prerelease || manifest.TagName != candidate.Tag || len(manifest.Assets) != 1 {
		return errors.New("fallback manifest release metadata is inconsistent")
	}
	asset := manifest.Assets[0]
	expectedURL := githubReleaseDownloadBase + candidate.Tag + "/" + AssetExecutable
	if asset.Name != AssetExecutable || asset.DownloadURL != expectedURL || asset.Size != size || asset.Digest != "sha256:"+digest {
		return errors.New("fallback manifest asset metadata is inconsistent")
	}
	return nil
}

func validateCandidateChangelog(body []byte, tag string) error {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return errors.New("changelog JSON is ambiguous")
	}
	document, err := release.ParseChangelog(body)
	if err != nil {
		return errors.New("changelog document is invalid")
	}
	wantVersion := strings.TrimPrefix(tag, "v")
	for _, raw := range document.Releases {
		var entry struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &entry); err == nil && entry.Version == wantVersion {
			return nil
		}
	}
	return errors.New("changelog does not contain candidate version")
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := scanUniqueJSONValue(decoder, first); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func scanUniqueJSONValue(decoder *json.Decoder, token json.Token) error {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("JSON object key is duplicated")
			}
			seen[key] = struct{}{}
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := scanUniqueJSONValue(decoder, value); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("JSON object is incomplete")
		}
	case '[':
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := scanUniqueJSONValue(decoder, value); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("JSON array is incomplete")
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	return nil
}
