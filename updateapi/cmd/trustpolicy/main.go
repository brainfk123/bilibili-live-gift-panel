package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	maxPublisherEpoch            = 99_999_999
	maxReviewedSPKIBytes         = 4 << 10
	maxImportedCommitBytes       = 4 << 10
	verifyBundleSchemaVersion    = 2
	maxVerifyBundleEnvelopeBytes = 512 << 10
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var sha256Input = regexp.MustCompile(`^[0-9a-f]{64}$`)

type environmentLookup func(string) (string, bool)
type signerFactory func(privateKeyPEM []byte, expectedSPKISHA256, requestID string) (trustpolicy.Signer, error)

type commandError string

func (err commandError) Error() string { return string(err) }

const errCommand commandError = "trust policy command failed"

func main() {
	if err := run(context.Background(), os.Args[1:], os.LookupEnv, trustpolicy.NewPrivateKeySigner, os.Stdout, time.Now); err != nil {
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
	if now == nil {
		now = time.Now
	}
	if args[0] == "verify-bundle" {
		return runVerifyBundle(args[1:], output, bundlefs.ReadCommittedBundle)
	}
	if args[0] == "import-bundle" {
		return runImportBundle(args[1:], output)
	}
	if args[0] == "validate-candidate" {
		return runValidateCandidate(args[1:], output, now)
	}
	if lookup == nil || factory == nil || args[0] != "sign" {
		return errCommand
	}

	var candidatePath string
	var expectedPreviousEpoch uint64
	var privateKeyEnvironment string
	var keyIDEnvironment string
	var expectedSPKIEnvironment string
	var requestIDEnvironment string
	var policyPath string
	var auditPath string
	flags := flag.NewFlagSet("trustpolicy sign", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&candidatePath, "candidate", "", "path to the complete signed-object candidate")
	flags.Uint64Var(&expectedPreviousEpoch, "expected-previous-epoch", 0, "exact previously accepted policy epoch")
	flags.StringVar(&privateKeyEnvironment, "private-key-env", "", "environment variable containing protected PKCS#8 P-256 private key PEM")
	flags.StringVar(&keyIDEnvironment, "key-id-env", "", "environment variable containing reviewed signing key ID")
	flags.StringVar(&expectedSPKIEnvironment, "expected-spki-sha256-env", "", "environment variable containing reviewed SPKI SHA-256")
	flags.StringVar(&requestIDEnvironment, "request-id-env", "", "environment variable containing non-secret signing audit request ID")
	flags.StringVar(&policyPath, "output", "", "create-only signed policy output")
	flags.StringVar(&auditPath, "audit-output", "", "create-only audit output")
	if hasRepeatedRegisteredFlag(args[1:], flags) {
		return errCommand
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !allRequiredFlagsPresent(flags) ||
		!distinctEnvironmentNames(privateKeyEnvironment, keyIDEnvironment, expectedSPKIEnvironment, requestIDEnvironment) {
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
	if err != nil || candidate.Epoch > maxPublisherEpoch {
		return errCommand
	}
	privateKeyPEM, privateKeyOK := lookup(privateKeyEnvironment)
	keyID, keyIDOK := lookup(keyIDEnvironment)
	expectedSPKI, expectedSPKIOK := lookup(expectedSPKIEnvironment)
	requestIDValue, requestIDOK := lookup(requestIDEnvironment)
	actor, actorOK := lookup("GITHUB_ACTOR")
	if !privateKeyOK || strings.TrimSpace(privateKeyPEM) == "" || !keyIDOK || !expectedSPKIOK || !requestIDOK || !actorOK {
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
	privateKeyBytes := []byte(privateKeyPEM)
	privateKeyPEM = ""
	signer, err := factory(privateKeyBytes, expectedSPKI, requestIDValue)
	clear(privateKeyBytes)
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

func runValidateCandidate(args []string, output io.Writer, now func() time.Time) error {
	var candidatePath string
	var candidateEpoch uint64
	var expectedPreviousEpoch uint64
	flags := flag.NewFlagSet("trustpolicy validate-candidate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&candidatePath, "candidate", "", "path to the complete signed-object candidate")
	flags.Uint64Var(&candidateEpoch, "candidate-epoch", 0, "exact declared candidate epoch")
	flags.Uint64Var(&expectedPreviousEpoch, "expected-previous-epoch", 0, "exact previously accepted policy epoch")
	if hasRepeatedRegisteredFlag(args, flags) {
		return errCommand
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 ||
		!requiredFlagsPresent(flags, "candidate", "candidate-epoch", "expected-previous-epoch") || candidateEpoch == 0 || candidateEpoch > maxPublisherEpoch {
		return errCommand
	}
	candidateBytes, err := readReviewedPublicFile(candidatePath, maxCandidateBytes)
	if err != nil {
		return errCommand
	}
	candidate, err := trustpolicy.ParseCandidate(candidateBytes, trustpolicy.CandidateOptions{
		ExpectedPreviousEpoch: expectedPreviousEpoch,
		Now:                   now().UTC(),
	})
	if err != nil || candidate.Epoch != candidateEpoch {
		return errCommand
	}
	if written, err := io.WriteString(output, "publisher policy candidate valid\n"); err != nil || written != len("publisher policy candidate valid\n") {
		return errCommand
	}
	return nil
}

type verifyBundleArtifact struct {
	Name        string `json:"name"`
	Length      uint64 `json:"length"`
	SHA256      string `json:"sha256"`
	BytesBase64 string `json:"bytesBase64"`
}

type verifyBundleVerification struct {
	Epoch                 uint64 `json:"epoch"`
	ExpectedPreviousEpoch uint64 `json:"expectedPreviousEpoch"`
	SPKISHA256            string `json:"spkiSha256"`
}

type verifyBundleEnvelope struct {
	SchemaVersion  uint64                   `json:"schemaVersion"`
	Verification   verifyBundleVerification `json:"verification"`
	Commit         trustpolicy.BundleCommit `json:"commit"`
	Policy         verifyBundleArtifact     `json:"policy"`
	Audit          verifyBundleArtifact     `json:"audit"`
	CommitArtifact verifyBundleArtifact     `json:"commitArtifact"`
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
	var reviewedSPKIPath string
	var expectedSPKISHA256 string
	var expectedPreviousEpoch uint64
	flags := flag.NewFlagSet("trustpolicy verify-bundle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&policyPath, "policy", "", "committed policy path")
	flags.StringVar(&auditPath, "audit", "", "committed audit path")
	flags.StringVar(&reviewedSPKIPath, "reviewed-spki", "", "reviewed P-256 SPKI DER path")
	flags.StringVar(&expectedSPKISHA256, "expected-spki-sha256", "", "reviewed SPKI SHA-256")
	flags.Uint64Var(&expectedPreviousEpoch, "expected-previous-epoch", 0, "exact previously accepted policy epoch")
	if hasRepeatedRegisteredFlag(args, flags) {
		return errCommand
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 ||
		!requiredFlagsPresent(flags, "policy", "audit", "reviewed-spki", "expected-spki-sha256", "expected-previous-epoch") ||
		!sha256Input.MatchString(expectedSPKISHA256) {
		return errCommand
	}
	committed, err := reader(policyPath, auditPath)
	if err != nil {
		return errCommand
	}
	reviewedSPKI, err := readReviewedPublicFile(reviewedSPKIPath, maxReviewedSPKIBytes)
	if err != nil {
		return errCommand
	}
	epoch, err := trustpolicy.VerifySignedPolicy(committed.Policy, reviewedSPKI, expectedSPKISHA256, expectedPreviousEpoch, time.Now().UTC())
	if err != nil {
		return errCommand
	}
	commitDigest := sha256.Sum256(committed.CommitBytes)
	envelope, err := json.Marshal(verifyBundleEnvelope{
		SchemaVersion: verifyBundleSchemaVersion,
		Verification: verifyBundleVerification{
			Epoch:                 epoch,
			ExpectedPreviousEpoch: expectedPreviousEpoch,
			SPKISHA256:            expectedSPKISHA256,
		},
		Commit: committed.Commit,
		Policy: verifyBundleArtifact{
			Name:        committed.Commit.Policy.Name,
			Length:      committed.Commit.Policy.Length,
			SHA256:      committed.Commit.Policy.SHA256,
			BytesBase64: base64.StdEncoding.EncodeToString(committed.Policy),
		},
		Audit: verifyBundleArtifact{
			Name:        committed.Commit.Audit.Name,
			Length:      committed.Commit.Audit.Length,
			SHA256:      committed.Commit.Audit.SHA256,
			BytesBase64: base64.StdEncoding.EncodeToString(committed.Audit),
		},
		CommitArtifact: verifyBundleArtifact{
			Name:        trustpolicy.BundleCommitFileName,
			Length:      uint64(len(committed.CommitBytes)),
			SHA256:      hex.EncodeToString(commitDigest[:]),
			BytesBase64: base64.StdEncoding.EncodeToString(committed.CommitBytes),
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

func readReviewedPublicFile(path string, maximum int64) ([]byte, error) {
	if strings.TrimSpace(path) == "" || maximum <= 0 {
		return nil, errCommand
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errCommand
	}
	absolute = filepath.Clean(absolute)
	pathInfo, err := os.Lstat(absolute)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Size() <= 0 || pathInfo.Size() > maximum {
		return nil, errCommand
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, errCommand
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != pathInfo.Size() || !os.SameFile(pathInfo, info) {
		_ = file.Close()
		return nil, errCommand
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() || int64(len(data)) > maximum {
		_ = file.Close()
		return nil, errCommand
	}
	finalInfo, err := os.Lstat(absolute)
	if err != nil || finalInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, finalInfo) {
		_ = file.Close()
		return nil, errCommand
	}
	if err := file.Close(); err != nil {
		return nil, errCommand
	}
	return data, nil
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
	return requiredFlagsPresent(flags, "candidate", "expected-previous-epoch", "private-key-env", "key-id-env", "expected-spki-sha256-env", "request-id-env", "output", "audit-output")
}

func distinctEnvironmentNames(names ...string) bool {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !environmentName.MatchString(name) {
			return false
		}
		if _, exists := seen[name]; exists {
			return false
		}
		seen[name] = struct{}{}
	}
	return true
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
