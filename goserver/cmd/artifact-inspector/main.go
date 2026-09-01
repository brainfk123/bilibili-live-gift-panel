// artifact-inspector performs credential-free release artifact verification.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"bilibili-live-gift-panel/internal/artifactinspect"
	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/ffmpegseal"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "artifact inspection failed")
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	if len(args) == 0 || output == nil {
		return errors.New("command is required")
	}
	switch args[0] {
	case "certificate":
		return runCertificate(args[1:], output, false)
	case "authenticode":
		return runCertificate(args[1:], output, true)
	case "pe-content-digest":
		return runPEDigest(args[1:], output)
	case "verify-artifact":
		return runVerifyArtifact(args[1:], output)
	case "verify-static":
		return runVerifyStatic(args[1:], output)
	case "verify-policy":
		return runVerifyPolicy(args[1:], output)
	case "seal-ffmpeg":
		return runSealFFmpeg(args[1:], output)
	case "publish-ffmpeg":
		return runPublishFFmpeg(args[1:], output)
	case "verify-enrollment":
		return runVerifyEnrollment(args[1:], output)
	case "verify-enrollment-policies":
		return runVerifyEnrollmentPolicies(args[1:], output)
	default:
		return errors.New("unknown command")
	}
}

func runVerifyEnrollmentPolicies(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("verify-enrollment-policies", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options artifactinspect.VerifyEnrollmentPoliciesOptions
	flags.StringVar(&options.RootSPKIPath, "root-spki", "", "reviewed root SPKI")
	flags.StringVar(&options.ExpectedRootSHA256, "root-sha256", "", "reviewed root digest")
	flags.StringVar(&options.BootstrapPolicyPath, "bootstrap-policy", "", "embedded bootstrap policy")
	flags.StringVar(&options.ExpectedBootstrapPolicySHA256, "bootstrap-policy-sha256", "", "bootstrap policy digest")
	bootstrapEpoch := flags.String("bootstrap-policy-epoch", "", "bootstrap policy epoch")
	flags.StringVar(&options.AuthorizationPolicyPath, "authorization-policy", "", "final artifact authorization policy")
	flags.StringVar(&options.ExpectedAuthorizationPolicySHA256, "authorization-policy-sha256", "", "authorization policy digest")
	authorizationEpoch := flags.String("authorization-policy-epoch", "", "authorization policy epoch")
	flags.StringVar(&options.Tag, "tag", "", "stable tag")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("enrollment policy arguments are invalid")
	}
	parsedBootstrapEpoch, bootstrapErr := strconv.ParseUint(*bootstrapEpoch, 10, 64)
	parsedAuthorizationEpoch, authorizationErr := strconv.ParseUint(*authorizationEpoch, 10, 64)
	if bootstrapErr != nil || authorizationErr != nil || parsedBootstrapEpoch == 0 || parsedAuthorizationEpoch <= parsedBootstrapEpoch || options.RootSPKIPath == "" || options.BootstrapPolicyPath == "" || options.AuthorizationPolicyPath == "" || options.Tag == "" {
		return errors.New("enrollment policy arguments are invalid")
	}
	options.ExpectedBootstrapPolicyEpoch = parsedBootstrapEpoch
	options.ExpectedAuthorizationPolicyEpoch = parsedAuthorizationEpoch
	options.Now = time.Now().UTC()
	evidence, err := artifactinspect.VerifyEnrollmentPolicies(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(evidence)
}

func runVerifyEnrollment(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("verify-enrollment", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options artifactinspect.VerifyEnrollmentOptions
	flags.StringVar(&options.ArtifactPath, "artifact", "", "content-addressed sealed executable")
	flags.StringVar(&options.ExpectedPEContentSHA256, "pe-content-sha256", "", "covered PE digest")
	flags.StringVar(&options.Version, "version", "", "application version")
	flags.StringVar(&options.Tag, "tag", "", "stable tag")
	flags.StringVar(&options.Commit, "commit", "", "release commit")
	flags.StringVar(&options.RootSPKIPath, "root-spki", "", "reviewed root SPKI")
	flags.StringVar(&options.ExpectedRootSHA256, "root-sha256", "", "reviewed root digest")
	flags.StringVar(&options.BootstrapPolicyPath, "bootstrap-policy", "", "embedded bootstrap policy")
	flags.StringVar(&options.ExpectedBootstrapPolicySHA256, "bootstrap-policy-sha256", "", "bootstrap policy digest")
	bootstrapEpoch := flags.String("bootstrap-policy-epoch", "", "bootstrap policy epoch")
	flags.StringVar(&options.AuthorizationPolicyPath, "authorization-policy", "", "final artifact authorization policy")
	flags.StringVar(&options.ExpectedAuthorizationPolicySHA256, "authorization-policy-sha256", "", "authorization policy digest")
	authorizationEpoch := flags.String("authorization-policy-epoch", "", "authorization policy epoch")
	flags.StringVar(&options.FFmpegArchivePath, "ffmpeg-archive", "", "sealed FFmpeg archive")
	flags.StringVar(&options.FFmpegManifestPath, "ffmpeg-manifest", "", "sealed FFmpeg manifest")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("enrollment arguments are invalid")
	}
	parsedBootstrapEpoch, bootstrapErr := strconv.ParseUint(*bootstrapEpoch, 10, 64)
	parsedAuthorizationEpoch, authorizationErr := strconv.ParseUint(*authorizationEpoch, 10, 64)
	if bootstrapErr != nil || authorizationErr != nil || parsedBootstrapEpoch == 0 || parsedAuthorizationEpoch == 0 ||
		options.ArtifactPath == "" || options.RootSPKIPath == "" || options.BootstrapPolicyPath == "" || options.AuthorizationPolicyPath == "" || options.FFmpegArchivePath == "" || options.FFmpegManifestPath == "" {
		return errors.New("enrollment arguments are invalid")
	}
	options.ExpectedBootstrapPolicyEpoch = parsedBootstrapEpoch
	options.ExpectedAuthorizationPolicyEpoch = parsedAuthorizationEpoch
	options.Now = time.Now().UTC()
	evidence, err := artifactinspect.VerifyEnrollmentArtifact(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(evidence)
}

func runSealFFmpeg(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("seal-ffmpeg", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options ffmpegseal.Options
	flags.StringVar(&options.ArchivePath, "archive", "", "source FFmpeg ZIP")
	flags.StringVar(&options.ManifestPath, "manifest", "", "source FFmpeg manifest")
	flags.StringVar(&options.SealedDirectory, "sealed-directory", "", "exact sealed output directory")
	if flags.Parse(args) != nil || flags.NArg() != 0 || options.ArchivePath == "" || options.ManifestPath == "" || options.SealedDirectory == "" {
		return errors.New("FFmpeg seal arguments are invalid")
	}
	evidence, err := ffmpegseal.VerifyAndSeal(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(evidence)
}

func runPublishFFmpeg(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("publish-ffmpeg", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options ffmpegseal.PublishOptions
	flags.StringVar(&options.ArchivePath, "archive", "", "sealed FFmpeg ZIP")
	flags.StringVar(&options.ManifestPath, "manifest", "", "sealed FFmpeg manifest")
	flags.StringVar(&options.Destination, "destination", "", "exact FFmpeg destination directory")
	if flags.Parse(args) != nil || flags.NArg() != 0 || options.ArchivePath == "" || options.ManifestPath == "" || options.Destination == "" {
		return errors.New("FFmpeg publication arguments are invalid")
	}
	evidence, err := ffmpegseal.PublishSealed(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(evidence)
}

func runVerifyPolicy(args []string, output io.Writer) error {
	return runVerifyPolicyWithInspector(args, output, artifactinspect.InspectAuthenticodeFile, time.Now)
}

func runVerifyStatic(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("verify-static", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options artifactinspect.VerifyStaticOptions
	flags.StringVar(&options.UnsignedPath, "unsigned", "", "unsigned PE")
	flags.StringVar(&options.SignedPath, "signed", "", "signed PE")
	flags.StringVar(&options.SealedDirectory, "sealed-directory", "", "sealed executable output directory")
	flags.StringVar(&options.Version, "version", "", "version")
	flags.StringVar(&options.Commit, "commit", "", "commit")
	flags.StringVar(&options.ExpectedIdentity.Country, "country", "", "country")
	flags.StringVar(&options.ExpectedIdentity.Organization, "organization", "", "organization")
	flags.StringVar(&options.ExpectedIdentity.OrganizationID, "organization-id", "", "organization ID")
	flags.StringVar(&options.FFmpegArchivePath, "ffmpeg-archive", "", "FFmpeg archive")
	flags.StringVar(&options.FFmpegManifestPath, "ffmpeg-manifest", "", "FFmpeg manifest")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("static artifact arguments are invalid")
	}
	evidence, err := artifactinspect.VerifyStaticArtifact(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(evidence)
}

func identityFlags(name string, args []string) (*flag.FlagSet, *string, *certidentity.Identity, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("file", "", "artifact or DER path")
	identity := &certidentity.Identity{}
	flags.StringVar(&identity.Country, "country", "", "exact country")
	flags.StringVar(&identity.Organization, "organization", "", "exact organization")
	flags.StringVar(&identity.OrganizationID, "organization-id", "", "exact Subject serialNumber")
	if name == "certificate" {
		flags.StringVar(path, "der", "", "certificate DER path")
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" || identity.Country == "" || identity.Organization == "" || identity.OrganizationID == "" {
		return nil, nil, nil, errors.New("identity arguments are invalid")
	}
	return flags, path, identity, nil
}

func runCertificate(args []string, output io.Writer, authenticode bool) error {
	name := "certificate"
	if authenticode {
		name = "authenticode"
	}
	_, path, expected, err := identityFlags(name, args)
	if err != nil {
		return err
	}
	var actual certidentity.Identity
	if authenticode {
		actual, err = artifactinspect.InspectAuthenticodeFile(*path)
	} else {
		der, readErr := os.ReadFile(*path)
		if readErr != nil {
			return errors.New("certificate DER is unavailable")
		}
		parsed, parseErr := certidentity.ParseCertificateDER(der)
		if parseErr != nil {
			return parseErr
		}
		actual = parsed.Identity
	}
	if err != nil || actual != *expected {
		return errors.New("certificate identity mismatch")
	}
	return json.NewEncoder(output).Encode(struct {
		Status   string                `json:"status"`
		Identity certidentity.Identity `json:"identity"`
	}{Status: "Valid", Identity: actual})
}

func runPEDigest(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("pe-content-digest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("file", "", "PE path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" {
		return errors.New("PE arguments are invalid")
	}
	contents, err := os.ReadFile(*path)
	if err != nil {
		return errors.New("PE is unavailable")
	}
	digest, err := artifactinspect.AuthenticodeContentSHA256(contents)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, digest)
	return err
}

func runVerifyArtifact(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("verify-artifact", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options artifactinspect.VerifyArtifactOptions
	flags.StringVar(&options.UnsignedPath, "unsigned", "", "unsigned PE")
	flags.StringVar(&options.SignedPath, "signed", "", "signed PE")
	flags.StringVar(&options.SealedDirectory, "sealed-directory", "", "sealed executable output directory")
	flags.StringVar(&options.Version, "version", "", "version")
	flags.StringVar(&options.Tag, "tag", "", "tag")
	flags.StringVar(&options.Commit, "commit", "", "commit")
	flags.StringVar(&options.RootSPKIPath, "root-spki", "", "root SPKI")
	flags.StringVar(&options.ExpectedRootSHA256, "root-sha256", "", "root digest")
	flags.StringVar(&options.PolicyPath, "policy", "", "policy")
	flags.StringVar(&options.ExpectedPolicySHA256, "policy-sha256", "", "policy digest")
	flags.StringVar(&options.StableArtifactPath, "stable-artifact", "", "exact stable convergence artifact")
	flags.StringVar(&options.StableTag, "stable-tag", "", "exact stable tag")
	stableChannel := flags.String("stable-channel", "", "exact stable channel")
	policyEpoch := flags.String("policy-epoch", "", "policy epoch")
	flags.StringVar(&options.FFmpegArchivePath, "ffmpeg-archive", "", "FFmpeg archive")
	flags.StringVar(&options.FFmpegManifestPath, "ffmpeg-manifest", "", "FFmpeg manifest")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("artifact arguments are invalid")
	}
	epoch, err := strconv.ParseUint(*policyEpoch, 10, 64)
	if err != nil || epoch == 0 {
		return errors.New("artifact policy epoch is invalid")
	}
	options.ExpectedPolicyEpoch = epoch
	options.StableChannel = updatepolicy.Channel(*stableChannel)
	if options.StableChannel != updatepolicy.ChannelStable || options.StableArtifactPath == "" || options.StableTag == "" {
		return errors.New("stable artifact arguments are invalid")
	}
	options.Now = time.Now().UTC()
	evidence, err := artifactinspect.VerifyBoundArtifact(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(evidence)
}
