package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strconv"
	"time"

	"bilibili-live-gift-panel/internal/artifactinspect"
	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

type verifiedBundleRecord struct {
	Name   string `json:"name"`
	Length uint64 `json:"length"`
	SHA256 string `json:"sha256"`
}

type verifiedBundleArtifact struct {
	verifiedBundleRecord
	BytesBase64 string `json:"bytesBase64"`
}

type verifiedBundleEnvelope struct {
	SchemaVersion uint64 `json:"schemaVersion"`
	Verification  struct {
		Epoch                 uint64 `json:"epoch"`
		ExpectedPreviousEpoch uint64 `json:"expectedPreviousEpoch"`
		SPKISHA256            string `json:"spkiSha256"`
	} `json:"verification"`
	Commit struct {
		SchemaVersion uint64               `json:"schemaVersion"`
		Policy        verifiedBundleRecord `json:"policy"`
		Audit         verifiedBundleRecord `json:"audit"`
	} `json:"commit"`
	Policy verifiedBundleArtifact `json:"policy"`
	Audit  verifiedBundleArtifact `json:"audit"`
}

type policyCommandEvidence struct {
	SchemaVersion uint64 `json:"schemaVersion"`
	artifactinspect.StablePolicyEvidence
}

func runVerifyPolicyWithInspector(args []string, output io.Writer, inspect func(string) (certidentity.Identity, error), now func() time.Time) error {
	if output == nil || inspect == nil || now == nil {
		return errors.New("policy verifier is unavailable")
	}
	flags := flag.NewFlagSet("verify-policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rootPath := flags.String("root-spki", "", "reviewed root")
	envelopePath := flags.String("verified-bundle", "", "verified machine envelope")
	epochValue := flags.String("epoch", "", "policy epoch")
	stableArtifact := flags.String("stable-artifact", "", "exact stable artifact")
	stableTag := flags.String("stable-tag", "", "exact stable tag")
	stableChannel := flags.String("stable-channel", "", "exact stable channel")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("policy arguments invalid")
	}
	epoch, err := strconv.ParseUint(*epochValue, 10, 64)
	if err != nil || epoch == 0 || *stableArtifact == "" || *stableTag == "" || *stableChannel != string(updatepolicy.ChannelStable) {
		return errors.New("policy arguments invalid")
	}
	root, err := readBoundedRegularFile(*rootPath, 4<<10)
	if err != nil {
		return err
	}
	envelopeBytes, err := readBoundedRegularFile(*envelopePath, 512<<10)
	if err != nil {
		return err
	}
	policy, err := policyFromVerifiedBundle(envelopeBytes, root, epoch)
	if err != nil {
		return err
	}
	evidence, err := artifactinspect.VerifyStableArtifactPolicy(artifactinspect.StablePolicyOptions{
		RootDER: root, PolicyBytes: policy, ExpectedEpoch: epoch, ArtifactPath: *stableArtifact,
		Tag: *stableTag, Channel: updatepolicy.Channel(*stableChannel), Now: now().UTC(), InspectAuthenticode: inspect,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(policyCommandEvidence{SchemaVersion: 1, StablePolicyEvidence: evidence})
}

func policyFromVerifiedBundle(data, root []byte, expectedEpoch uint64) ([]byte, error) {
	if err := updatepolicy.ValidateJSON(data); err != nil {
		return nil, errors.New("verified bundle JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope verifiedBundleEnvelope
	if decoder.Decode(&envelope) != nil {
		return nil, errors.New("verified bundle is invalid")
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return nil, errors.New("verified bundle has trailing data")
	}
	rootDigest := sha256.Sum256(root)
	if envelope.SchemaVersion != 2 || envelope.Verification.Epoch != expectedEpoch || envelope.Verification.ExpectedPreviousEpoch != 0 || envelope.Verification.SPKISHA256 != hex.EncodeToString(rootDigest[:]) || envelope.Commit.SchemaVersion != 1 {
		return nil, errors.New("verified bundle binding is invalid")
	}
	policy, err := verifyEnvelopeArtifact(envelope.Policy, envelope.Commit.Policy, "policy.json", 256<<10)
	if err != nil {
		return nil, err
	}
	if _, err := verifyEnvelopeArtifact(envelope.Audit, envelope.Commit.Audit, "audit.json", 64<<10); err != nil {
		return nil, err
	}
	return policy, nil
}

func verifyEnvelopeArtifact(artifact verifiedBundleArtifact, committed verifiedBundleRecord, name string, maximum int) ([]byte, error) {
	if artifact.Name != name || committed.Name != name || artifact.verifiedBundleRecord != committed || artifact.Length == 0 || artifact.Length > uint64(maximum) || artifact.SHA256 == "" {
		return nil, errors.New("verified bundle artifact binding is invalid")
	}
	contents, err := base64.StdEncoding.Strict().DecodeString(artifact.BytesBase64)
	if err != nil || len(contents) != int(artifact.Length) || base64.StdEncoding.EncodeToString(contents) != artifact.BytesBase64 {
		return nil, errors.New("verified bundle artifact bytes are invalid")
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, errors.New("verified bundle artifact digest is invalid")
	}
	return contents, nil
}

func readBoundedRegularFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("reviewed input is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("reviewed input is unavailable")
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(contents)) != info.Size() || int64(len(contents)) > maximum {
		return nil, errors.New("reviewed input is invalid")
	}
	final, err := os.Lstat(path)
	if err != nil || final.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, final) {
		return nil, errors.New("reviewed input changed")
	}
	return contents, nil
}
