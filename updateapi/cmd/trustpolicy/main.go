package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
)

const maxCandidateBytes = 256 << 10

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type environmentLookup func(string) (string, bool)
type signerFactory func(region, expectedSPKISHA256 string) (trustpolicy.Signer, error)

type commandError string

func (err commandError) Error() string { return string(err) }

const errCommand commandError = "trust policy command failed"

func main() {
	if err := run(context.Background(), os.Args[1:], os.LookupEnv, trustpolicy.NewTencentKMSSigner, os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, errCommand)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookup environmentLookup, factory signerFactory, output io.Writer, now func() time.Time) error {
	if ctx == nil || lookup == nil || factory == nil || len(args) == 0 || args[0] != "sign" {
		return errCommand
	}
	if output == nil {
		output = io.Discard
	}
	if now == nil {
		now = time.Now
	}

	var candidatePath string
	var expectedPreviousEpoch uint64
	var region string
	var keyIDEnvironment string
	var expectedSPKIEnvironment string
	var policyPath string
	var auditPath string
	flags := flag.NewFlagSet("trustpolicy sign", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&candidatePath, "candidate", "", "path to the complete signed-object candidate")
	flags.Uint64Var(&expectedPreviousEpoch, "expected-previous-epoch", 0, "exact previously accepted policy epoch")
	flags.StringVar(&region, "kms-region", "", "Tencent KMS region")
	flags.StringVar(&keyIDEnvironment, "kms-key-id-env", "", "environment variable containing reviewed KMS key ID")
	flags.StringVar(&expectedSPKIEnvironment, "expected-spki-sha256-env", "", "environment variable containing reviewed SPKI SHA-256")
	flags.StringVar(&policyPath, "output", "", "create-only signed policy output")
	flags.StringVar(&auditPath, "audit-output", "", "create-only audit output")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !allRequiredFlagsPresent(flags) ||
		region != "ap-shanghai" || !environmentName.MatchString(keyIDEnvironment) || !environmentName.MatchString(expectedSPKIEnvironment) ||
		keyIDEnvironment == expectedSPKIEnvironment {
		return errCommand
	}
	candidateAbsolute, policyAbsolute, auditAbsolute, err := validatePaths(candidatePath, policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	if err := preflightCreateOnly(policyAbsolute, auditAbsolute); err != nil {
		return errCommand
	}

	candidateBytes, err := readBoundedFile(candidateAbsolute, maxCandidateBytes)
	if err != nil {
		return errCommand
	}
	signingTime := now().UTC()
	candidate, err := trustpolicy.ParseCandidate(candidateBytes, trustpolicy.CandidateOptions{ExpectedPreviousEpoch: expectedPreviousEpoch, Now: signingTime})
	if err != nil {
		return errCommand
	}
	keyID, keyIDOK := lookup(keyIDEnvironment)
	expectedSPKI, expectedSPKIOK := lookup(expectedSPKIEnvironment)
	actor, actorOK := lookup("GITHUB_ACTOR")
	if !keyIDOK || !expectedSPKIOK || !actorOK {
		return errCommand
	}
	options := trustpolicy.SignOptions{
		KeyID:                 keyID,
		ExpectedPreviousEpoch: expectedPreviousEpoch,
		ExpectedSPKISHA256:    expectedSPKI,
		Now:                   signingTime,
		CIActor:               actor,
	}
	if err := trustpolicy.ValidateSignOptions(candidate, options); err != nil {
		return errCommand
	}
	signer, err := factory(region, expectedSPKI)
	if err != nil || signer == nil {
		return errCommand
	}
	signed, audit, err := trustpolicy.Sign(ctx, signer, candidate, options)
	if err != nil {
		return errCommand
	}
	auditBytes, err := json.Marshal(audit)
	if err != nil {
		return errCommand
	}
	if err := writeCreateOnlyPair(policyAbsolute, signed.Policy, auditAbsolute, auditBytes); err != nil {
		return errCommand
	}
	_, _ = fmt.Fprintln(output, "publisher policy signed")
	return nil
}

func allRequiredFlagsPresent(flags *flag.FlagSet) bool {
	seen := make(map[string]struct{})
	flags.Visit(func(value *flag.Flag) { seen[value.Name] = struct{}{} })
	for _, name := range []string{"candidate", "expected-previous-epoch", "kms-region", "kms-key-id-env", "expected-spki-sha256-env", "output", "audit-output"} {
		if _, ok := seen[name]; !ok {
			return false
		}
	}
	return true
}

func validatePaths(candidatePath, policyPath, auditPath string) (string, string, string, error) {
	if strings.TrimSpace(candidatePath) == "" || strings.TrimSpace(policyPath) == "" || strings.TrimSpace(auditPath) == "" {
		return "", "", "", errCommand
	}
	candidateAbsolute, err := filepath.Abs(candidatePath)
	if err != nil {
		return "", "", "", errCommand
	}
	policyAbsolute, err := filepath.Abs(policyPath)
	if err != nil {
		return "", "", "", errCommand
	}
	auditAbsolute, err := filepath.Abs(auditPath)
	if err != nil {
		return "", "", "", errCommand
	}
	if samePath(candidateAbsolute, policyAbsolute) || samePath(candidateAbsolute, auditAbsolute) || samePath(policyAbsolute, auditAbsolute) {
		return "", "", "", errCommand
	}
	return candidateAbsolute, policyAbsolute, auditAbsolute, nil
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func preflightCreateOnly(paths ...string) error {
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errCommand
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil || !parent.IsDir() {
			return errCommand
		}
	}
	return nil
}

func readBoundedFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errCommand
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return nil, errCommand
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, errCommand
	}
	return data, nil
}

func writeCreateOnlyPair(policyPath string, policy []byte, auditPath string, audit []byte) error {
	policyTemporary, err := stagePrivateFile(policyPath, policy)
	if err != nil {
		return errCommand
	}
	defer os.Remove(policyTemporary)
	auditTemporary, err := stagePrivateFile(auditPath, audit)
	if err != nil {
		return errCommand
	}
	defer os.Remove(auditTemporary)
	if err := os.Link(policyTemporary, policyPath); err != nil {
		return errCommand
	}
	if err := os.Link(auditTemporary, auditPath); err != nil {
		_ = os.Remove(policyPath)
		return errCommand
	}
	return nil
}

func stagePrivateFile(target string, data []byte) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(target), ".trustpolicy-*")
	if err != nil {
		return "", errCommand
	}
	name := file.Name()
	keep := false
	defer func() {
		if !keep {
			file.Close()
			os.Remove(name)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", errCommand
	}
	if _, err := file.Write(data); err != nil {
		return "", errCommand
	}
	if err := file.Sync(); err != nil {
		return "", errCommand
	}
	if err := file.Close(); err != nil {
		return "", errCommand
	}
	keep = true
	return name, nil
}
