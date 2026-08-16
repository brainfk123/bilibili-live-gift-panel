package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/mirror"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/publish"
)

const (
	defaultStateDirectory = "/var/lib/gift-panel-release-mirror"
	invocationTimeout     = 15 * time.Minute
)

type commandRunner interface {
	Run(context.Context, mirror.RunOptions) (mirror.RunResult, error)
}

type cosConfiguration struct {
	Bucket    string
	Region    string
	SecretID  string
	SecretKey string
}

type environmentLookup func(string) (string, bool)
type runnerFactory func(string, cosConfiguration) (commandRunner, error)

type commandStage string

const (
	stageArguments     commandStage = "arguments"
	stageConfiguration commandStage = "configuration"
	stageInvocation    commandStage = "invocation"
)

type commandError struct {
	stage commandStage
	cause error
}

func (err *commandError) Error() string {
	return fmt.Sprintf("mirror command failed: stage=%s", err.stage)
}
func (err *commandError) Unwrap() error { return err.cause }

func commandFailure(stage commandStage, cause error) error {
	return &commandError{stage: stage, cause: cause}
}

func commandStageOf(err error) commandStage {
	var commandFailure *commandError
	if errors.As(err, &commandFailure) {
		return commandFailure.stage
	}
	if stage := mirror.StageOf(err); stage != "" {
		return commandStage(stage)
	}
	return ""
}

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := runWithSignals(context.Background(), signals, os.Args[1:], os.LookupEnv, newProductionRunner, os.Stdout, time.Now); err != nil {
		os.Exit(1)
	}
}

func runWithSignals(parent context.Context, signals <-chan os.Signal, args []string, lookup environmentLookup, factory runnerFactory, output io.Writer, now func() time.Time) error {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	defer func() {
		close(done)
		cancel()
	}()
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		case <-done:
		}
	}()
	return run(ctx, args, lookup, factory, output, now)
}

func run(parent context.Context, args []string, lookup environmentLookup, factory runnerFactory, output io.Writer, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	startedAt := now()
	ctx, cancel := context.WithTimeout(parent, invocationTimeout)
	defer cancel()
	finishFailure := func(err error) error {
		if _, ok := err.(*commandError); !ok && mirror.StageOf(err) == "" {
			err = commandFailure(stageInvocation, err)
		}
		writeFailureSummary(output, err, now().Sub(startedAt))
		return err
	}
	if err := ctx.Err(); err != nil {
		return finishFailure(commandFailure(stageInvocation, err))
	}

	dryRun := false
	stateDirectory := defaultStateDirectory
	flags := flag.NewFlagSet("gift-panel-release-mirror", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&dryRun, "dry-run", false, "validate the latest release without COS publication")
	flags.StringVar(&stateDirectory, "state-dir", defaultStateDirectory, "absolute mirror state directory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !isAbsoluteCleanStateDirectory(stateDirectory) {
		return finishFailure(commandFailure(stageArguments, errors.New("command arguments are invalid")))
	}
	if err := ctx.Err(); err != nil {
		return finishFailure(commandFailure(stageInvocation, err))
	}

	configuration := cosConfiguration{}
	if !dryRun {
		var ok bool
		if configuration.Bucket, ok = requiredEnvironment(lookup, "COS_BUCKET"); !ok {
			return finishFailure(commandFailure(stageConfiguration, errors.New("COS configuration is incomplete")))
		}
		if configuration.Region, ok = requiredEnvironment(lookup, "COS_REGION"); !ok {
			return finishFailure(commandFailure(stageConfiguration, errors.New("COS configuration is incomplete")))
		}
		if configuration.SecretID, ok = requiredEnvironment(lookup, "COS_SECRET_ID"); !ok {
			return finishFailure(commandFailure(stageConfiguration, errors.New("COS configuration is incomplete")))
		}
		if configuration.SecretKey, ok = requiredEnvironment(lookup, "COS_SECRET_KEY"); !ok {
			return finishFailure(commandFailure(stageConfiguration, errors.New("COS configuration is incomplete")))
		}
	}
	if err := ctx.Err(); err != nil {
		return finishFailure(commandFailure(stageInvocation, err))
	}

	type constructionResult struct {
		runner commandRunner
		err    error
	}
	constructed := make(chan constructionResult, 1)
	go func() {
		runner, err := factory(stateDirectory, configuration)
		constructed <- constructionResult{runner: runner, err: err}
	}()
	var runner commandRunner
	var err error
	select {
	case <-ctx.Done():
		return finishFailure(commandFailure(stageInvocation, ctx.Err()))
	case result := <-constructed:
		runner, err = result.runner, result.err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return finishFailure(commandFailure(stageInvocation, contextErr))
	}
	if err != nil || runner == nil {
		if err == nil {
			err = errors.New("runner factory returned nil")
		}
		return finishFailure(commandFailure(stageConfiguration, err))
	}
	result, err := runner.Run(ctx, mirror.RunOptions{DryRun: dryRun})
	if err != nil {
		return finishFailure(err)
	}
	elapsed := now().Sub(startedAt)
	writeSuccessSummary(output, result, elapsed)
	return nil
}

func newProductionRunner(stateDirectory string, configuration cosConfiguration) (commandRunner, error) {
	state, err := mirror.NewFileStateRepository(stateDirectory)
	if err != nil {
		return nil, errors.New("mirror state repository is unavailable")
	}
	fetcher, err := mirror.NewDownloader(stateDirectory)
	if err != nil {
		return nil, errors.New("mirror downloader is unavailable")
	}
	return &mirror.Runner{
		Source:  mirror.NewGitHubReleaseSource(mirror.NewRestrictedHTTPClient()),
		Fetcher: fetcher,
		State:   state,
		NewPublisher: func() (mirror.Publisher, error) {
			store, err := cosstore.New(configuration.Bucket, configuration.Region, configuration.SecretID, configuration.SecretKey, nil)
			if err != nil {
				return nil, errors.New("COS publisher configuration is invalid")
			}
			publisher, err := publish.NewPublisher(store)
			if err != nil {
				return nil, errors.New("COS publisher is unavailable")
			}
			return publisher, nil
		},
		Now: time.Now,
	}, nil
}

func requiredEnvironment(lookup environmentLookup, name string) (string, bool) {
	if lookup == nil {
		return "", false
	}
	value, ok := lookup(name)
	return value, ok && strings.TrimSpace(value) != ""
}

func isAbsoluteCleanStateDirectory(value string) bool {
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return path.IsAbs(value) && path.Clean(value) == value
	}
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func writeFailureSummary(output io.Writer, err error, elapsed time.Duration) {
	if output == nil {
		return
	}
	stage := commandStageOf(err)
	if stage == "" {
		stage = stageInvocation
	}
	if tag := mirror.TagOf(err); tag != "" {
		fmt.Fprintf(output, "stage=%s tag=%s outcome=failed elapsed=%s\n", stage, tag, elapsed)
		return
	}
	fmt.Fprintf(output, "stage=%s outcome=failed elapsed=%s\n", stage, elapsed)
}

func writeSuccessSummary(output io.Writer, result mirror.RunResult, elapsed time.Duration) {
	if output == nil {
		return
	}
	if result.StateInvalid {
		fmt.Fprintf(output, "stage=state outcome=invalid-state-ignored elapsed=%s\n", elapsed)
	}
	outcome := "not-modified"
	switch {
	case result.DryRun:
		outcome = "dry-run"
	case result.NotModified:
		outcome = "not-modified"
	case result.Outcome == publish.OutcomeStablePromoted:
		outcome = "stable-promoted"
	case result.Outcome == publish.OutcomeStableUnchanged:
		outcome = "stable-unchanged"
	}
	if result.Tag != "" {
		fmt.Fprintf(output, "tag=%s outcome=%s elapsed=%s\n", result.Tag, outcome, elapsed)
		return
	}
	fmt.Fprintf(output, "outcome=%s elapsed=%s\n", outcome, elapsed)
}
