package artifactinspect

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/securefile"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

type VerifyEnrollmentOptions struct {
	ArtifactPath, ExpectedPEContentSHA256 string
	Version, Tag, Commit                  string
	RootSPKIPath, ExpectedRootSHA256      string
	BootstrapPolicyPath                   string
	ExpectedBootstrapPolicySHA256         string
	ExpectedBootstrapPolicyEpoch          uint64
	AuthorizationPolicyPath               string
	ExpectedAuthorizationPolicySHA256     string
	ExpectedAuthorizationPolicyEpoch      uint64
	FFmpegArchivePath, FFmpegManifestPath string
	Now                                   time.Time
	InspectAuthenticode                   func(string) (certidentity.Identity, error)
}

type AuthorizationScope string

const (
	AuthorizationScopeArtifactSHA256    AuthorizationScope = "artifact-sha256"
	AuthorizationScopePublisherIdentity AuthorizationScope = "publisher-identity"
)

type EnrollmentEvidence struct {
	SchemaVersion                uint64                `json:"schemaVersion"`
	Version                      string                `json:"version"`
	Tag                          string                `json:"tag"`
	Commit                       string                `json:"commit"`
	SignedFileSHA256             string                `json:"signedFileSha256"`
	SignedFileSize               int64                 `json:"signedFileSize"`
	PEContentSHA256              string                `json:"peContentSha256"`
	RootSPKISHA256               string                `json:"rootSpkiSha256"`
	BootstrapPolicySHA256        string                `json:"bootstrapPolicySha256"`
	BootstrapPolicyEpoch         uint64                `json:"bootstrapPolicyEpoch"`
	BootstrapSignatureStatus     string                `json:"bootstrapSignatureStatus"`
	AuthorizationPolicySHA256    string                `json:"authorizationPolicySha256"`
	AuthorizationPolicyEpoch     uint64                `json:"authorizationPolicyEpoch"`
	AuthorizationSignatureStatus string                `json:"authorizationSignatureStatus"`
	AuthorizationScope           AuthorizationScope    `json:"authorizationScope"`
	AuthorizedChannel            updatepolicy.Channel  `json:"authorizedChannel"`
	AuthorizedTag                string                `json:"authorizedTag"`
	AuthorizedArtifactSHA256     string                `json:"authorizedArtifactSha256"`
	AuthorizedIdentity           certidentity.Identity `json:"authorizedIdentity"`
	OuterIdentity                certidentity.Identity `json:"outerIdentity"`
	AuthenticodeStatus           string                `json:"authenticodeStatus"`
	FFmpegVersion                string                `json:"ffmpegVersion"`
	FFmpegSHA256                 string                `json:"ffmpegSha256"`
	FFmpegSize                   int64                 `json:"ffmpegSize"`
	FFmpegArchiveSHA256          string                `json:"ffmpegArchiveSha256"`
	FFmpegManifestSHA256         string                `json:"ffmpegManifestSha256"`
	FFmpegIdentity               certidentity.Identity `json:"ffmpegIdentity"`
	FFmpegSignatureStatus        string                `json:"ffmpegSignatureStatus"`
}

type VerifyEnrollmentPoliciesOptions struct {
	RootSPKIPath, ExpectedRootSHA256  string
	BootstrapPolicyPath               string
	ExpectedBootstrapPolicySHA256     string
	ExpectedBootstrapPolicyEpoch      uint64
	AuthorizationPolicyPath           string
	ExpectedAuthorizationPolicySHA256 string
	ExpectedAuthorizationPolicyEpoch  uint64
	Tag                               string
	Now                               time.Time
}

type EnrollmentPolicyEvidence struct {
	SchemaVersion                uint64                `json:"schemaVersion"`
	Tag                          string                `json:"tag"`
	RootSPKISHA256               string                `json:"rootSpkiSha256"`
	BootstrapPolicySHA256        string                `json:"bootstrapPolicySha256"`
	BootstrapPolicyEpoch         uint64                `json:"bootstrapPolicyEpoch"`
	BootstrapSignatureStatus     string                `json:"bootstrapSignatureStatus"`
	AuthorizationPolicySHA256    string                `json:"authorizationPolicySha256"`
	AuthorizationPolicyEpoch     uint64                `json:"authorizationPolicyEpoch"`
	AuthorizationSignatureStatus string                `json:"authorizationSignatureStatus"`
	AuthorizationScope           AuthorizationScope    `json:"authorizationScope"`
	AuthorizedChannel            updatepolicy.Channel  `json:"authorizedChannel"`
	AuthorizedArtifactSHA256     string                `json:"authorizedArtifactSha256"`
	AuthorizedIdentity           certidentity.Identity `json:"authorizedIdentity"`
}

func VerifyEnrollmentPolicies(options VerifyEnrollmentPoliciesOptions) (EnrollmentPolicyEvidence, error) {
	version := strings.TrimPrefix(options.Tag, "v")
	if options.Tag != "v"+version || !isEnrollmentVersion(version) || !isLowerHex(options.ExpectedRootSHA256, 64) || !isLowerHex(options.ExpectedBootstrapPolicySHA256, 64) || !isLowerHex(options.ExpectedAuthorizationPolicySHA256, 64) || options.ExpectedBootstrapPolicyEpoch == 0 || options.ExpectedAuthorizationPolicyEpoch <= options.ExpectedBootstrapPolicyEpoch {
		return EnrollmentPolicyEvidence{}, errors.New("enrollment policy arguments are invalid")
	}
	rootDER, err := securefile.ReadBoundedRegular(options.RootSPKIPath, 4<<10, nil)
	if err != nil || sha256HexBytes(rootDER) != options.ExpectedRootSHA256 {
		return EnrollmentPolicyEvidence{}, errors.New("reviewed enrollment root is invalid")
	}
	bootstrapPolicy, err := securefile.ReadBoundedRegular(options.BootstrapPolicyPath, 256<<10, nil)
	if err != nil || sha256HexBytes(bootstrapPolicy) != options.ExpectedBootstrapPolicySHA256 {
		return EnrollmentPolicyEvidence{}, errors.New("reviewed bootstrap policy is invalid")
	}
	authorizationPolicy, err := securefile.ReadBoundedRegular(options.AuthorizationPolicyPath, 256<<10, nil)
	if err != nil || sha256HexBytes(authorizationPolicy) != options.ExpectedAuthorizationPolicySHA256 {
		return EnrollmentPolicyEvidence{}, errors.New("reviewed authorization policy is invalid")
	}
	root, err := parseEnrollmentRoot(rootDER)
	if err != nil {
		return EnrollmentPolicyEvidence{}, err
	}
	bootstrap, err := updatepolicy.ParseAndVerify(bootstrapPolicy, root, options.Now)
	if err != nil || bootstrap.Epoch != options.ExpectedBootstrapPolicyEpoch {
		return EnrollmentPolicyEvidence{}, errors.New("bootstrap policy signature or epoch is invalid")
	}
	identity := updatepolicy.ArtifactIdentity{Tag: options.Tag, Channel: updatepolicy.ChannelStable, Certificate: naisNetIdentity}
	if err := bootstrap.AuthorizeAt(identity, options.Now); err != nil {
		return EnrollmentPolicyEvidence{}, errors.New("bootstrap policy NaisNet stable authorization is invalid")
	}
	authorization, err := updatepolicy.ParseAndVerify(authorizationPolicy, root, options.Now)
	if err != nil || authorization.Epoch != options.ExpectedAuthorizationPolicyEpoch {
		return EnrollmentPolicyEvidence{}, errors.New("final authorization policy signature or epoch is invalid")
	}
	minimumScope, err := authorizationScopeForStableVersion(version)
	if err != nil {
		return EnrollmentPolicyEvidence{}, err
	}
	authorizationScope, authorizedHash, err := authorizeStablePolicy(authorization, options.Tag, "", naisNetIdentity, minimumScope, options.Now)
	if err != nil {
		return EnrollmentPolicyEvidence{}, errors.New("final stable authorization policy is invalid")
	}
	return EnrollmentPolicyEvidence{
		SchemaVersion: 1, Tag: options.Tag, RootSPKISHA256: options.ExpectedRootSHA256,
		BootstrapPolicySHA256: options.ExpectedBootstrapPolicySHA256, BootstrapPolicyEpoch: bootstrap.Epoch, BootstrapSignatureStatus: "Valid",
		AuthorizationPolicySHA256: options.ExpectedAuthorizationPolicySHA256, AuthorizationPolicyEpoch: authorization.Epoch, AuthorizationSignatureStatus: "Valid",
		AuthorizationScope: authorizationScope, AuthorizedChannel: updatepolicy.ChannelStable, AuthorizedArtifactSHA256: authorizedHash, AuthorizedIdentity: naisNetIdentity,
	}, nil
}

func VerifyEnrollmentArtifact(options VerifyEnrollmentOptions) (EnrollmentEvidence, error) {
	if !isEnrollmentVersion(options.Version) || options.Tag != "v"+options.Version || !isLowerHex(options.Commit, 40) || !isLowerHex(options.ExpectedPEContentSHA256, 64) || options.ExpectedBootstrapPolicyEpoch == 0 || options.ExpectedAuthorizationPolicyEpoch <= options.ExpectedBootstrapPolicyEpoch {
		return EnrollmentEvidence{}, errors.New("enrollment artifact arguments are invalid")
	}
	artifactSnapshot, err := securefile.SnapshotRegular(options.ArtifactPath, 128<<20, "gift-panel-enrollment-artifact-", "enrollment.exe")
	if err != nil {
		return EnrollmentEvidence{}, errors.New("sealed enrollment artifact is unavailable")
	}
	defer artifactSnapshot.Close()
	artifactSHA256 := sha256HexBytes(artifactSnapshot.Bytes)
	if filepath.Base(options.ArtifactPath) != artifactSHA256+".exe" {
		return EnrollmentEvidence{}, errors.New("sealed enrollment artifact is not content-addressed")
	}
	peContentSHA256, err := AuthenticodeContentSHA256(artifactSnapshot.Bytes)
	if err != nil || peContentSHA256 != options.ExpectedPEContentSHA256 {
		return EnrollmentEvidence{}, errors.New("sealed enrollment PE content binding is invalid")
	}
	versionCovered, versionErr := CoveredContains(artifactSnapshot.Bytes, []byte(options.Version))
	commitCovered, commitErr := CoveredContains(artifactSnapshot.Bytes, []byte(options.Commit))
	if versionErr != nil || commitErr != nil || !versionCovered || !commitCovered {
		return EnrollmentEvidence{}, errors.New("sealed enrollment version or commit binding is invalid")
	}
	rootDER, err := securefile.ReadBoundedRegular(options.RootSPKIPath, 4<<10, nil)
	if err != nil || sha256HexBytes(rootDER) != options.ExpectedRootSHA256 {
		return EnrollmentEvidence{}, errors.New("reviewed enrollment root is invalid")
	}
	bootstrapPolicy, err := securefile.ReadBoundedRegular(options.BootstrapPolicyPath, 256<<10, nil)
	if err != nil || sha256HexBytes(bootstrapPolicy) != options.ExpectedBootstrapPolicySHA256 {
		return EnrollmentEvidence{}, errors.New("reviewed bootstrap policy is invalid")
	}
	authorizationPolicy, err := securefile.ReadBoundedRegular(options.AuthorizationPolicyPath, 256<<10, nil)
	if err != nil || sha256HexBytes(authorizationPolicy) != options.ExpectedAuthorizationPolicySHA256 {
		return EnrollmentEvidence{}, errors.New("reviewed authorization policy is invalid")
	}
	rootCovered, rootErr := CoveredContains(artifactSnapshot.Bytes, []byte(base64.StdEncoding.EncodeToString(rootDER)))
	bootstrapCovered, bootstrapErr := CoveredContains(artifactSnapshot.Bytes, []byte(base64.StdEncoding.EncodeToString(bootstrapPolicy)))
	if rootErr != nil || bootstrapErr != nil || !rootCovered || !bootstrapCovered {
		return EnrollmentEvidence{}, errors.New("sealed enrollment embedded trust binding is invalid")
	}
	inspect := options.InspectAuthenticode
	if inspect == nil {
		inspect = InspectAuthenticodeFile
	}
	outerIdentity, err := inspect(artifactSnapshot.Path)
	if err != nil || outerIdentity != naisNetIdentity {
		return EnrollmentEvidence{}, errors.New("sealed enrollment Authenticode identity is invalid")
	}
	if err := artifactSnapshot.Revalidate(); err != nil {
		return EnrollmentEvidence{}, errors.New("sealed enrollment artifact changed during Authenticode inspection")
	}
	root, err := parseEnrollmentRoot(rootDER)
	if err != nil {
		return EnrollmentEvidence{}, err
	}
	bootstrap, err := updatepolicy.ParseAndVerify(bootstrapPolicy, root, options.Now)
	if err != nil || bootstrap.Epoch != options.ExpectedBootstrapPolicyEpoch {
		return EnrollmentEvidence{}, errors.New("bootstrap policy signature or epoch is invalid")
	}
	bootstrapIdentity := updatepolicy.ArtifactIdentity{Tag: options.Tag, Channel: updatepolicy.ChannelStable, Certificate: outerIdentity}
	if err := bootstrap.AuthorizeAt(bootstrapIdentity, options.Now); err != nil {
		return EnrollmentEvidence{}, errors.New("bootstrap policy NaisNet stable authorization is invalid")
	}
	authorization, err := updatepolicy.ParseAndVerify(authorizationPolicy, root, options.Now)
	if err != nil || authorization.Epoch != options.ExpectedAuthorizationPolicyEpoch {
		return EnrollmentEvidence{}, errors.New("final authorization policy signature or epoch is invalid")
	}
	minimumScope, err := authorizationScopeForStableVersion(options.Version)
	if err != nil {
		return EnrollmentEvidence{}, err
	}
	authorizationScope, authorizedHash, err := authorizeStablePolicy(authorization, options.Tag, artifactSHA256, outerIdentity, minimumScope, options.Now)
	if err != nil {
		return EnrollmentEvidence{}, errors.New("final stable authorization is invalid")
	}
	archive, err := securefile.ReadBoundedRegular(options.FFmpegArchivePath, 40<<20, nil)
	if err != nil {
		return EnrollmentEvidence{}, errors.New("sealed enrollment FFmpeg archive is unavailable")
	}
	manifestBytes, err := securefile.ReadBoundedRegular(options.FFmpegManifestPath, 64<<10, nil)
	if err != nil {
		return EnrollmentEvidence{}, errors.New("sealed enrollment FFmpeg manifest is unavailable")
	}
	archiveCovered, archiveErr := CoveredContains(artifactSnapshot.Bytes, archive)
	manifestCovered, manifestErr := CoveredContains(artifactSnapshot.Bytes, manifestBytes)
	if archiveErr != nil || manifestErr != nil || !archiveCovered || !manifestCovered {
		return EnrollmentEvidence{}, errors.New("sealed enrollment embedded FFmpeg binding is invalid")
	}
	manifest, manifestParseErr := parseEnrollmentFFmpegManifest(manifestBytes)
	if manifestParseErr != nil || manifest.Version != "9.0" || !manifest.Authenticode || manifest.Size <= 0 || manifest.Size > 40<<20 || !isLowerHex(manifest.SHA256, 64) {
		return EnrollmentEvidence{}, errors.New("sealed enrollment FFmpeg manifest is invalid")
	}
	ffmpeg, err := extractSingleFFmpeg(archive, manifest.Size)
	if err != nil || sha256HexBytes(ffmpeg) != manifest.SHA256 {
		return EnrollmentEvidence{}, errors.New("sealed enrollment FFmpeg archive does not match manifest")
	}
	ffmpegSnapshot, err := securefile.SnapshotBytes(ffmpeg, "gift-panel-enrollment-ffmpeg-", "ffmpeg.exe")
	if err != nil {
		return EnrollmentEvidence{}, errors.New("sealed enrollment FFmpeg inspection file is unavailable")
	}
	defer ffmpegSnapshot.Close()
	ffmpegIdentity, err := inspect(ffmpegSnapshot.Path)
	if err != nil || ffmpegIdentity != naisNetIdentity {
		return EnrollmentEvidence{}, errors.New("sealed enrollment FFmpeg Authenticode identity is invalid")
	}
	if err := ffmpegSnapshot.Revalidate(); err != nil {
		return EnrollmentEvidence{}, errors.New("sealed enrollment FFmpeg snapshot changed")
	}
	if err := artifactSnapshot.Revalidate(); err != nil {
		return EnrollmentEvidence{}, errors.New("sealed enrollment artifact changed during verification")
	}
	return EnrollmentEvidence{
		SchemaVersion: 1, Version: options.Version, Tag: options.Tag, Commit: options.Commit,
		SignedFileSHA256: artifactSHA256, SignedFileSize: int64(len(artifactSnapshot.Bytes)), PEContentSHA256: peContentSHA256,
		RootSPKISHA256:        options.ExpectedRootSHA256,
		BootstrapPolicySHA256: options.ExpectedBootstrapPolicySHA256, BootstrapPolicyEpoch: bootstrap.Epoch, BootstrapSignatureStatus: "Valid",
		AuthorizationPolicySHA256: options.ExpectedAuthorizationPolicySHA256, AuthorizationPolicyEpoch: authorization.Epoch, AuthorizationSignatureStatus: "Valid",
		AuthorizationScope: authorizationScope, AuthorizedChannel: updatepolicy.ChannelStable, AuthorizedTag: options.Tag, AuthorizedArtifactSHA256: authorizedHash, AuthorizedIdentity: outerIdentity,
		OuterIdentity: outerIdentity, AuthenticodeStatus: "Valid",
		FFmpegVersion: manifest.Version, FFmpegSHA256: manifest.SHA256, FFmpegSize: manifest.Size,
		FFmpegArchiveSHA256: sha256HexBytes(archive), FFmpegManifestSHA256: sha256HexBytes(manifestBytes), FFmpegIdentity: ffmpegIdentity, FFmpegSignatureStatus: "Valid",
	}, nil
}

func authorizationScopeForStableVersion(version string) (AuthorizationScope, error) {
	if !isEnrollmentVersion(version) {
		return "", errors.New("stable version is invalid")
	}
	if version == "0.4.12" {
		return AuthorizationScopeArtifactSHA256, nil
	}
	return AuthorizationScopePublisherIdentity, nil
}

func authorizeStablePolicy(policy updatepolicy.Verified, tag, artifactSHA256 string, identity certidentity.Identity, minimum AuthorizationScope, now time.Time) (AuthorizationScope, string, error) {
	matchCount := 0
	matchedScope := AuthorizationScope("")
	matchedHash := ""
	for _, rule := range policy.Rules {
		candidateHash := artifactSHA256
		requireHash := false
		scope := AuthorizationScopePublisherIdentity
		if rule.ManifestSHA256 != "" {
			if candidateHash == "" {
				candidateHash = rule.ManifestSHA256
			}
			requireHash = true
			scope = AuthorizationScopeArtifactSHA256
		} else if minimum == AuthorizationScopeArtifactSHA256 {
			continue
		}
		candidate := updatepolicy.ArtifactIdentity{
			Tag: tag, Channel: updatepolicy.ChannelStable, SHA256: candidateHash,
			Certificate: identity, RequireManifestSHA256: requireHash,
		}
		if policy.AuthorizeAt(candidate, now) != nil {
			continue
		}
		matchCount++
		matchedScope = scope
		if scope == AuthorizationScopeArtifactSHA256 {
			matchedHash = rule.ManifestSHA256
		}
	}
	if matchCount != 1 {
		return "", "", errors.New("stable policy must contain one matching authorization")
	}
	return matchedScope, matchedHash, nil
}

type enrollmentFFmpegManifest struct {
	Schema               uint64 `json:"schema"`
	ComponentFingerprint string `json:"component_fingerprint"`
	Descriptor           string `json:"descriptor"`
	DescriptorSHA256     string `json:"descriptor_sha256"`
	Version              string `json:"version"`
	SHA256               string `json:"sha256"`
	ArchiveSHA256        string `json:"archive_sha256"`
	ComponentGate        string `json:"component_gate"`
	ComponentGateSHA256  string `json:"component_gate_sha256"`
	Size                 int64  `json:"size"`
	Authenticode         bool   `json:"authenticode"`
	SignerSubject        string `json:"signer_subject"`
	SourceReleaseCommit  string `json:"source_release_commit"`
}

func parseEnrollmentFFmpegManifest(contents []byte) (enrollmentFFmpegManifest, error) {
	if err := updatepolicy.ValidateJSON(contents); err != nil {
		return enrollmentFFmpegManifest{}, errors.New("enrollment FFmpeg manifest JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest enrollmentFFmpegManifest
	if err := decoder.Decode(&manifest); err != nil {
		return enrollmentFFmpegManifest{}, errors.New("enrollment FFmpeg manifest schema is invalid")
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return enrollmentFFmpegManifest{}, errors.New("enrollment FFmpeg manifest has trailing data")
	}
	return manifest, nil
}

func isEnrollmentVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	parsed := [3]uint64{}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return false
		}
		parsed[index] = number
	}
	return parsed[0] > 0 || (parsed[0] == 0 && (parsed[1] > 4 || (parsed[1] == 4 && parsed[2] >= 12)))
}

func parseEnrollmentRoot(rootDER []byte) (*ecdsa.PublicKey, error) {
	parsed, err := x509.ParsePKIXPublicKey(rootDER)
	root, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok || root.Curve.Params().Name != "P-256" {
		return nil, errors.New("reviewed enrollment root is not P-256")
	}
	return root, nil
}
