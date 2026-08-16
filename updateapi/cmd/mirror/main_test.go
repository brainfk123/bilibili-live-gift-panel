package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/mirror"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/publish"
)

// Mutation caught: exposing repository, endpoint, asset, or arbitrary positional controls expands the public CLI trust boundary.
func TestRunRejectsEveryFlagExceptDryRunAndStateDir(t *testing.T) {
	for _, args := range [][]string{
		{"--repo", "attacker/repo"},
		{"--github-api", "https://secret.invalid/?token=credential"},
		{"--asset", "other.exe"},
		{"positional-secret"},
	} {
		factoryCalls := 0
		var output bytes.Buffer
		err := run(context.Background(), args, emptyEnvironment, func(string, cosConfiguration) (commandRunner, error) {
			factoryCalls++
			return &fakeCommandRunner{}, nil
		}, &output, time.Now)
		if err == nil || commandStageOf(err) != stageArguments {
			t.Fatalf("run(%q) error = %v, stage=%q", args, err, commandStageOf(err))
		}
		if factoryCalls != 0 {
			t.Fatalf("run(%q) factory calls = %d", args, factoryCalls)
		}
		for _, secret := range []string{"attacker/repo", "secret.invalid", "token=", "credential", "other.exe", "positional-secret"} {
			if strings.Contains(err.Error(), secret) || strings.Contains(output.String(), secret) {
				t.Fatalf("run(%q) leaked %q: error=%v output=%q", args, secret, err, output.String())
			}
		}
	}
}

// Mutation caught: accepting empty, relative, or unclean directories lets downloads escape the dedicated state root.
func TestRunRequiresAbsoluteCleanStateDirectory(t *testing.T) {
	unclean := t.TempDir() + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "unclean"
	for _, stateDir := range []string{"", "relative", "relative/path", unclean} {
		var output bytes.Buffer
		err := run(context.Background(), []string{"--dry-run", "--state-dir", stateDir}, emptyEnvironment, func(string, cosConfiguration) (commandRunner, error) {
			return &fakeCommandRunner{}, nil
		}, &output, time.Now)
		if err == nil || commandStageOf(err) != stageArguments {
			t.Fatalf("state dir %q error = %v, stage=%q", stateDir, err, commandStageOf(err))
		}
	}
}

// Mutation caught: changing the production default silently relocates durable ETag and resume state.
func TestRunUsesFixedProductionStateDirectoryDefault(t *testing.T) {
	var gotStateDir string
	var output bytes.Buffer
	err := run(context.Background(), []string{"--dry-run"}, emptyEnvironment, func(stateDir string, _ cosConfiguration) (commandRunner, error) {
		gotStateDir = stateDir
		return &fakeCommandRunner{result: mirror.RunResult{Tag: "v1.2.3", DryRun: true}}, nil
	}, &output, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if gotStateDir != "/var/lib/gift-panel-release-mirror" {
		t.Fatalf("state directory = %q", gotStateDir)
	}
}

// Mutation caught: validating COS variables during dry-run prevents credential-free local release verification.
func TestRunDryRunNeedsNoCOSEnvironmentAndForwardsOption(t *testing.T) {
	var gotConfiguration cosConfiguration
	runner := &fakeCommandRunner{result: mirror.RunResult{Tag: "v1.2.3", DryRun: true}}
	var output bytes.Buffer
	err := run(context.Background(), []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, func(_ string, configuration cosConfiguration) (commandRunner, error) {
		gotConfiguration = configuration
		return runner, nil
	}, &output, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.options.DryRun {
		t.Fatal("Runner.Run() DryRun = false")
	}
	if gotConfiguration != (cosConfiguration{}) {
		t.Fatalf("COS configuration = %+v, want empty", gotConfiguration)
	}
}

// Mutation caught: allowing any missing COS field defers a permanent configuration error until after downloads.
func TestRunNormalModeRequiresAllFourCOSVariables(t *testing.T) {
	complete := map[string]string{
		"COS_BUCKET":     "bucket-123456",
		"COS_REGION":     "ap-shanghai",
		"COS_SECRET_ID":  "id-secret",
		"COS_SECRET_KEY": "key-secret",
	}
	for missing := range complete {
		t.Run(missing, func(t *testing.T) {
			environment := cloneEnvironment(complete)
			delete(environment, missing)
			factoryCalls := 0
			var output bytes.Buffer
			err := run(context.Background(), []string{"--state-dir", t.TempDir()}, mapEnvironment(environment), func(string, cosConfiguration) (commandRunner, error) {
				factoryCalls++
				return &fakeCommandRunner{}, nil
			}, &output, time.Now)
			if err == nil || commandStageOf(err) != stageConfiguration || factoryCalls != 0 {
				t.Fatalf("missing %s: error=%v stage=%q factory=%d", missing, err, commandStageOf(err), factoryCalls)
			}
			if strings.Contains(err.Error(), "id-secret") || strings.Contains(err.Error(), "key-secret") || strings.Contains(output.String(), "secret") {
				t.Fatalf("missing %s leaked environment: error=%v output=%q", missing, err, output.String())
			}
		})
	}

	var got cosConfiguration
	var output bytes.Buffer
	err := run(context.Background(), []string{"--state-dir", t.TempDir()}, mapEnvironment(complete), func(_ string, configuration cosConfiguration) (commandRunner, error) {
		got = configuration
		return &fakeCommandRunner{result: mirror.RunResult{NotModified: true}}, nil
	}, &output, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bucket != complete["COS_BUCKET"] || got.Region != complete["COS_REGION"] || got.SecretID != complete["COS_SECRET_ID"] || got.SecretKey != complete["COS_SECRET_KEY"] {
		t.Fatalf("COS configuration = %+v", got)
	}
}

// Mutation caught: omitting the invocation deadline lets a oneshot timer instance hang indefinitely.
func TestRunSuppliesFiniteOverallDeadline(t *testing.T) {
	runner := &fakeCommandRunner{inspectContext: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("deadline is missing")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > invocationTimeout {
			return fmt.Errorf("deadline remaining %s", remaining)
		}
		return nil
	}, result: mirror.RunResult{Tag: "v1.2.3", DryRun: true}}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, fixedRunnerFactory(runner), &output, time.Now); err != nil {
		t.Fatal(err)
	}
}

// Mutation caught: replacing the caller context with Background prevents service shutdown cancellation.
func TestRunPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeCommandRunner{inspectContext: func(ctx context.Context) error { return ctx.Err() }}
	var output bytes.Buffer
	err := run(ctx, []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, fixedRunnerFactory(runner), &output, time.Now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want context.Canceled", err)
	}
}

// Mutation caught: failing to connect SIGTERM to the invocation context leaves systemd stop waiting for timeout.
func TestRunWithSignalsCancelsOnSIGTERM(t *testing.T) {
	signals := make(chan os.Signal, 1)
	started := make(chan struct{})
	runner := &fakeCommandRunner{inspectContext: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	result := make(chan error, 1)
	go func() {
		var output bytes.Buffer
		result <- runWithSignals(context.Background(), signals, []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, fixedRunnerFactory(runner), &output, time.Now)
	}()
	<-started
	signals <- syscall.SIGTERM
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runWithSignals() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not cancel the invocation")
	}
}

// Mutation caught: running the factory synchronously prevents SIGTERM from returning while state-directory construction is blocked.
func TestRunWithSignalsCancelsBlockingFactoryOnSIGTERM(t *testing.T) {
	signals := make(chan os.Signal, 1)
	started := make(chan struct{})
	releaseFactory := make(chan struct{})
	runner := &fakeCommandRunner{}
	result := make(chan error, 1)
	go func() {
		var output bytes.Buffer
		result <- runWithSignals(context.Background(), signals, []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, func(string, cosConfiguration) (commandRunner, error) {
			close(started)
			<-releaseFactory
			return runner, nil
		}, &output, time.Now)
	}()
	<-started
	signals <- syscall.SIGTERM
	select {
	case err := <-result:
		close(releaseFactory)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runWithSignals() error = %v, want context.Canceled", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(releaseFactory)
		<-result
		t.Fatal("SIGTERM did not interrupt blocking runner construction")
	}
	time.Sleep(20 * time.Millisecond)
	if runner.calls != 0 {
		t.Fatalf("late factory result executed Runner.Run() %d times", runner.calls)
	}
}

// Mutation caught: a caller deadline cannot stop synchronous runner construction before Runner.Run receives a context.
func TestRunCallerDeadlineCancelsBlockingFactory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := make(chan struct{})
	releaseFactory := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		var output bytes.Buffer
		result <- run(ctx, []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, func(string, cosConfiguration) (commandRunner, error) {
			close(started)
			<-releaseFactory
			return &fakeCommandRunner{}, nil
		}, &output, time.Now)
	}()
	<-started
	select {
	case err := <-result:
		close(releaseFactory)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run() error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(releaseFactory)
		<-result
		t.Fatal("caller deadline did not interrupt blocking runner construction")
	}
}

// Mutation caught: creating the 15-minute timeout after factory completion gives construction unbounded extra time.
func TestRunOverallDeadlineStartsBeforeFactoryConstruction(t *testing.T) {
	runner := &fakeCommandRunner{inspectContext: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("deadline is missing")
		}
		if remaining := time.Until(deadline); remaining >= invocationTimeout-50*time.Millisecond {
			return fmt.Errorf("deadline started after construction: remaining %s", remaining)
		}
		return nil
	}, result: mirror.RunResult{Tag: "v1.2.3", DryRun: true}}
	var output bytes.Buffer
	err := run(context.Background(), []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, func(string, cosConfiguration) (commandRunner, error) {
		time.Sleep(100 * time.Millisecond)
		return runner, nil
	}, &output, time.Now)
	if err != nil {
		t.Fatal(err)
	}
}

// Mutation caught: formatting whole results or dependency values can expose paths, URLs, or credentials in success logs.
func TestRunWritesOnlySafeSuccessSummaries(t *testing.T) {
	for _, result := range []mirror.RunResult{
		{NotModified: true},
		{Tag: "v1.2.3", DryRun: true, StateInvalid: true},
		{Tag: "v1.2.3", Outcome: publish.OutcomeStablePromoted},
		{Tag: "v1.2.3", Outcome: publish.OutcomeStableUnchanged},
	} {
		clock := &stepClock{times: []time.Time{time.Unix(0, 0), time.Unix(2, 0)}}
		var output bytes.Buffer
		err := run(context.Background(), []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, fixedRunnerFactory(&fakeCommandRunner{result: result}), &output, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		text := output.String()
		if !strings.Contains(text, "outcome=") || !strings.Contains(text, "elapsed=2s") {
			t.Fatalf("summary = %q", text)
		}
		for _, leaked := range []string{"http://", "https://", "token=", "credential", `C:\\`, "/tmp/"} {
			if strings.Contains(text, leaked) {
				t.Fatalf("summary leaked %q: %q", leaked, text)
			}
		}
	}
}

// Mutation caught: logging an unwrapped runner error leaks its raw URL, body, credential, or filesystem path.
func TestRunWritesOnlySafeStageFailureSummary(t *testing.T) {
	secret := "https://secret.invalid/file?token=credential C:/private response-body"
	state := &cliStateRepository{}
	runner := &mirror.Runner{
		Source:  cliReleaseSource{err: errors.New(secret)},
		Fetcher: cliArtifactFetcher{},
		State:   state,
	}
	clock := &stepClock{times: []time.Time{time.Unix(0, 0), time.Unix(3, 0)}}
	var output bytes.Buffer
	err := run(context.Background(), []string{"--dry-run", "--state-dir", t.TempDir()}, emptyEnvironment, fixedRunnerFactory(runner), &output, clock.Now)
	if err == nil || mirror.StageOf(err) != mirror.StageDiscovery {
		t.Fatalf("run() error = %v, stage=%q", err, mirror.StageOf(err))
	}
	if got := output.String(); !strings.Contains(got, "stage=discovery") || !strings.Contains(got, "outcome=failed") || !strings.Contains(got, "elapsed=3s") {
		t.Fatalf("failure summary = %q", got)
	}
	for _, leaked := range []string{"secret.invalid", "token=", "credential", "C:/private", "response-body"} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(output.String(), leaked) {
			t.Fatalf("failure leaked %q: error=%v output=%q", leaked, err, output.String())
		}
	}
}

type fakeCommandRunner struct {
	result         mirror.RunResult
	err            error
	options        mirror.RunOptions
	inspectContext func(context.Context) error
	calls          int
}

func (runner *fakeCommandRunner) Run(ctx context.Context, options mirror.RunOptions) (mirror.RunResult, error) {
	runner.calls++
	runner.options = options
	if runner.inspectContext != nil {
		if err := runner.inspectContext(ctx); err != nil {
			return mirror.RunResult{}, err
		}
	}
	return runner.result, runner.err
}

func fixedRunnerFactory(runner commandRunner) runnerFactory {
	return func(string, cosConfiguration) (commandRunner, error) { return runner, nil }
}

func emptyEnvironment(string) (string, bool) { return "", false }

func mapEnvironment(values map[string]string) environmentLookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func cloneEnvironment(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

type stepClock struct {
	mu    sync.Mutex
	times []time.Time
}

func (clock *stepClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	result := clock.times[0]
	clock.times = clock.times[1:]
	return result
}

type cliReleaseSource struct{ err error }

func (source cliReleaseSource) Latest(context.Context, string) (mirror.LatestResult, error) {
	return mirror.LatestResult{}, source.err
}

type cliArtifactFetcher struct{}

func (cliArtifactFetcher) Download(context.Context, mirror.DownloadSpec) (string, error) {
	return "", errors.New("unused")
}

type cliStateRepository struct{}

func (*cliStateRepository) Load() (mirror.MirrorState, error) { return mirror.MirrorState{}, nil }
func (*cliStateRepository) Save(mirror.MirrorState) error     { return errors.New("unused") }
