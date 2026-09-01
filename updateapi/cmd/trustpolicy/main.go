package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

const (
	maxCandidateBytes            = 256 << 10
	verifyBundleSchemaVersion    = 1
	maxVerifyBundleEnvelopeBytes = 512 << 10
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type environmentLookup func(string) (string, bool)
type signerFactory func(region, expectedSPKISHA256, providerMode string) (trustpolicy.Signer, error)

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
	if ctx == nil || len(args) == 0 {
		return errCommand
	}
	if output == nil {
		output = io.Discard
	}
	if args[0] == "verify-bundle" {
		return runVerifyBundle(args[1:], output, bundlefs.ReadCommittedBundle)
	}
	if lookup == nil || factory == nil || args[0] != "sign" {
		return errCommand
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
	if hasRepeatedRegisteredFlag(args[1:], flags) {
		return errCommand
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !allRequiredFlagsPresent(flags) ||
		region != "ap-shanghai" || !environmentName.MatchString(keyIDEnvironment) || !environmentName.MatchString(expectedSPKIEnvironment) ||
		keyIDEnvironment == expectedSPKIEnvironment {
		return errCommand
	}
	candidateAbsolute, policyAbsolute, auditAbsolute, err := validatePaths(candidatePath, policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	if _, err := validateOutputBundlePaths(policyAbsolute, auditAbsolute); err != nil {
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
	providerMode, providerModeOK := lookup("GIFT_PANEL_KMS_PROVIDER_MODE")
	actor, actorOK := lookup("GITHUB_ACTOR")
	if !keyIDOK || !expectedSPKIOK || !providerModeOK || !trustpolicy.ValidKMSProviderMode(providerMode) || !actorOK {
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
	signer, err := factory(region, expectedSPKI, providerMode)
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
	if err := writeOutputBundle(policyAbsolute, signed.Policy, auditAbsolute, auditBytes, bundleHooks{}); err != nil {
		return errCommand
	}
	if _, err := readCommittedBundle(policyAbsolute, auditAbsolute); err != nil {
		return errCommand
	}
	_, _ = fmt.Fprintln(output, "publisher policy signed")
	return nil
}

type verifyBundleArtifact struct {
	Length      uint64 `json:"length"`
	SHA256      string `json:"sha256"`
	BytesBase64 string `json:"bytesBase64"`
}

type verifyBundleEnvelope struct {
	SchemaVersion uint64               `json:"schemaVersion"`
	Policy        verifyBundleArtifact `json:"policy"`
	Audit         verifyBundleArtifact `json:"audit"`
}

type committedBundleReader func(string, string) (trustpolicy.CommittedBundle, error)

func runVerifyBundle(args []string, output io.Writer, reader committedBundleReader) error {
	if output == nil {
		output = io.Discard
	}
	if reader == nil {
		return errCommand
	}
	var policyPath string
	var auditPath string
	flags := flag.NewFlagSet("trustpolicy verify-bundle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&policyPath, "policy", "", "committed policy path")
	flags.StringVar(&auditPath, "audit", "", "committed audit path")
	if hasRepeatedRegisteredFlag(args, flags) {
		return errCommand
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !requiredFlagsPresent(flags, "policy", "audit") {
		return errCommand
	}
	committed, err := reader(policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	envelope, err := json.Marshal(verifyBundleEnvelope{
		SchemaVersion: verifyBundleSchemaVersion,
		Policy: verifyBundleArtifact{
			Length:      committed.Commit.Policy.Length,
			SHA256:      committed.Commit.Policy.SHA256,
			BytesBase64: base64.StdEncoding.EncodeToString(committed.Policy),
		},
		Audit: verifyBundleArtifact{
			Length:      committed.Commit.Audit.Length,
			SHA256:      committed.Commit.Audit.SHA256,
			BytesBase64: base64.StdEncoding.EncodeToString(committed.Audit),
		},
	})
	if err != nil || len(envelope) == 0 || len(envelope)+1 > maxVerifyBundleEnvelopeBytes {
		return errCommand
	}
	envelope = append(envelope, '\n')
	if written, err := output.Write(envelope); err != nil || written != len(envelope) {
		return errCommand
	}
	return nil
}

func hasRepeatedRegisteredFlag(args []string, flags *flag.FlagSet) bool {
	seen := make(map[string]struct{})
	for index := 0; index < len(args); index++ {
		token := args[index]
		if token == "--" {
			break
		}
		if len(token) < 2 || token[0] != '-' {
			continue
		}
		nameAndValue := strings.TrimPrefix(token, "-")
		nameAndValue = strings.TrimPrefix(nameAndValue, "-")
		name, _, hasEquals := strings.Cut(nameAndValue, "=")
		if name == "" || flags.Lookup(name) == nil {
			continue
		}
		if _, exists := seen[name]; exists {
			return true
		}
		seen[name] = struct{}{}
		if !hasEquals {
			index++
		}
	}
	return false
}

func allRequiredFlagsPresent(flags *flag.FlagSet) bool {
	return requiredFlagsPresent(flags, "candidate", "expected-previous-epoch", "kms-region", "kms-key-id-env", "expected-spki-sha256-env", "output", "audit-output")
}

func requiredFlagsPresent(flags *flag.FlagSet, names ...string) bool {
	seen := make(map[string]struct{}, len(names))
	flags.Visit(func(value *flag.Flag) { seen[value.Name] = struct{}{} })
	for _, name := range names {
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
	candidateAbsolute = filepath.Clean(candidateAbsolute)
	candidateInfo, err := os.Lstat(candidateAbsolute)
	if err != nil || !candidateInfo.Mode().IsRegular() || candidateInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", "", errCommand
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidateAbsolute)
	if err != nil {
		return "", "", "", errCommand
	}
	resolvedCandidate, err = filepath.Abs(resolvedCandidate)
	if err != nil || !samePath(candidateAbsolute, resolvedCandidate) {
		return "", "", "", errCommand
	}
	bundlePaths, err := validateOutputBundlePaths(policyPath, auditPath)
	if err != nil || samePath(candidateAbsolute, bundlePaths.policy) || samePath(candidateAbsolute, bundlePaths.audit) {
		return "", "", "", errCommand
	}
	return candidateAbsolute, bundlePaths.policy, bundlePaths.audit, nil
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
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
