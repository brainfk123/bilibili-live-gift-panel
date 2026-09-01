package artifactinspect

import (
	"encoding/base64"
	"errors"
	"path/filepath"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/securefile"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

type VerifyEnrollmentCandidateOptions struct {
	ArtifactPath, ExpectedPEContentSHA256 string
	Version, Tag, Commit                  string
	RootSPKIPath, ExpectedRootSHA256      string
	BootstrapPolicyPath                   string
	ExpectedBootstrapPolicySHA256         string
	ExpectedBootstrapPolicyEpoch          uint64
	FFmpegArchivePath, FFmpegManifestPath string
	Now                                   time.Time
	InspectAuthenticode                   func(string) (certidentity.Identity, error)
}

type EnrollmentCandidateEvidence struct {
	SchemaVersion            uint64                `json:"schemaVersion"`
	Version                  string                `json:"version"`
	Tag                      string                `json:"tag"`
	Commit                   string                `json:"commit"`
	SignedFileSHA256         string                `json:"signedFileSha256"`
	SignedFileSize           int64                 `json:"signedFileSize"`
	PEContentSHA256          string                `json:"peContentSha256"`
	RootSPKISHA256           string                `json:"rootSpkiSha256"`
	RootKeyID                string                `json:"rootKeyId"`
	BootstrapPolicySHA256    string                `json:"bootstrapPolicySha256"`
	BootstrapPolicyEpoch     uint64                `json:"bootstrapPolicyEpoch"`
	BootstrapSignatureStatus string                `json:"bootstrapSignatureStatus"`
	OuterIdentity            certidentity.Identity `json:"outerIdentity"`
	AuthenticodeStatus       string                `json:"authenticodeStatus"`
	FFmpegVersion            string                `json:"ffmpegVersion"`
	FFmpegSHA256             string                `json:"ffmpegSha256"`
	FFmpegSize               int64                 `json:"ffmpegSize"`
	FFmpegArchiveSHA256      string                `json:"ffmpegArchiveSha256"`
	FFmpegManifestSHA256     string                `json:"ffmpegManifestSha256"`
	FFmpegIdentity           certidentity.Identity `json:"ffmpegIdentity"`
	FFmpegSignatureStatus    string                `json:"ffmpegSignatureStatus"`
}

func VerifyEnrollmentCandidate(options VerifyEnrollmentCandidateOptions) (EnrollmentCandidateEvidence, error) {
	if !isEnrollmentVersion(options.Version) || options.Tag != "v"+options.Version || !isLowerHex(options.Commit, 40) || !isLowerHex(options.ExpectedPEContentSHA256, 64) || !isLowerHex(options.ExpectedRootSHA256, 64) || !isLowerHex(options.ExpectedBootstrapPolicySHA256, 64) || options.ExpectedBootstrapPolicyEpoch == 0 {
		return EnrollmentCandidateEvidence{}, errors.New("enrollment candidate arguments are invalid")
	}
	artifactSnapshot, err := securefile.SnapshotRegular(options.ArtifactPath, 128<<20, "gift-panel-enrollment-candidate-", "candidate.exe")
	if err != nil {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate is unavailable")
	}
	defer artifactSnapshot.Close()
	artifactSHA256 := sha256HexBytes(artifactSnapshot.Bytes)
	if filepath.Base(options.ArtifactPath) != artifactSHA256+".exe" {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate is not content-addressed")
	}
	peContentSHA256, err := AuthenticodeContentSHA256(artifactSnapshot.Bytes)
	if err != nil || peContentSHA256 != options.ExpectedPEContentSHA256 {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate PE binding is invalid")
	}
	versionCovered, versionErr := CoveredContains(artifactSnapshot.Bytes, []byte(options.Version))
	commitCovered, commitErr := CoveredContains(artifactSnapshot.Bytes, []byte(options.Commit))
	if versionErr != nil || commitErr != nil || !versionCovered || !commitCovered {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate version binding is invalid")
	}
	rootDER, err := securefile.ReadBoundedRegular(options.RootSPKIPath, 4<<10, nil)
	if err != nil || sha256HexBytes(rootDER) != options.ExpectedRootSHA256 {
		return EnrollmentCandidateEvidence{}, errors.New("reviewed enrollment root is invalid")
	}
	bootstrapPolicy, err := securefile.ReadBoundedRegular(options.BootstrapPolicyPath, 256<<10, nil)
	if err != nil || sha256HexBytes(bootstrapPolicy) != options.ExpectedBootstrapPolicySHA256 {
		return EnrollmentCandidateEvidence{}, errors.New("reviewed bootstrap policy is invalid")
	}
	rootCovered, rootErr := CoveredContains(artifactSnapshot.Bytes, []byte(base64.StdEncoding.EncodeToString(rootDER)))
	bootstrapCovered, bootstrapErr := CoveredContains(artifactSnapshot.Bytes, []byte(base64.StdEncoding.EncodeToString(bootstrapPolicy)))
	if rootErr != nil || bootstrapErr != nil || !rootCovered || !bootstrapCovered {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate trust binding is invalid")
	}
	inspect := options.InspectAuthenticode
	if inspect == nil {
		inspect = InspectAuthenticodeFile
	}
	outerIdentity, err := inspect(artifactSnapshot.Path)
	if err != nil || outerIdentity != naisNetIdentity {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate identity is invalid")
	}
	if err := artifactSnapshot.Revalidate(); err != nil {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate changed during inspection")
	}
	root, err := parseEnrollmentRoot(rootDER)
	if err != nil {
		return EnrollmentCandidateEvidence{}, err
	}
	bootstrap, err := updatepolicy.ParseAndVerify(bootstrapPolicy, root, options.Now)
	if err != nil || bootstrap.Epoch != options.ExpectedBootstrapPolicyEpoch {
		return EnrollmentCandidateEvidence{}, errors.New("bootstrap policy signature or epoch is invalid")
	}
	if err := bootstrap.AuthorizeAt(updatepolicy.ArtifactIdentity{Tag: options.Tag, Channel: updatepolicy.ChannelStable, Certificate: outerIdentity}, options.Now); err != nil {
		return EnrollmentCandidateEvidence{}, errors.New("bootstrap policy NaisNet stable authorization is invalid")
	}
	archive, err := securefile.ReadBoundedRegular(options.FFmpegArchivePath, 40<<20, nil)
	if err != nil {
		return EnrollmentCandidateEvidence{}, errors.New("sealed candidate FFmpeg archive is unavailable")
	}
	manifestBytes, err := securefile.ReadBoundedRegular(options.FFmpegManifestPath, 64<<10, nil)
	if err != nil {
		return EnrollmentCandidateEvidence{}, errors.New("sealed candidate FFmpeg manifest is unavailable")
	}
	archiveCovered, archiveErr := CoveredContains(artifactSnapshot.Bytes, archive)
	manifestCovered, manifestErr := CoveredContains(artifactSnapshot.Bytes, manifestBytes)
	if archiveErr != nil || manifestErr != nil || !archiveCovered || !manifestCovered {
		return EnrollmentCandidateEvidence{}, errors.New("sealed enrollment candidate FFmpeg binding is invalid")
	}
	manifest, manifestErr := parseEnrollmentFFmpegManifest(manifestBytes)
	if manifestErr != nil || manifest.Version != "9.0" || !manifest.Authenticode || manifest.Size <= 0 || manifest.Size > 40<<20 || !isLowerHex(manifest.SHA256, 64) {
		return EnrollmentCandidateEvidence{}, errors.New("sealed candidate FFmpeg manifest is invalid")
	}
	ffmpeg, err := extractSingleFFmpeg(archive, manifest.Size)
	if err != nil || sha256HexBytes(ffmpeg) != manifest.SHA256 {
		return EnrollmentCandidateEvidence{}, errors.New("sealed candidate FFmpeg bytes are invalid")
	}
	ffmpegSnapshot, err := securefile.SnapshotBytes(ffmpeg, "gift-panel-enrollment-candidate-ffmpeg-", "ffmpeg.exe")
	if err != nil {
		return EnrollmentCandidateEvidence{}, errors.New("sealed candidate FFmpeg snapshot is unavailable")
	}
	defer ffmpegSnapshot.Close()
	ffmpegIdentity, err := inspect(ffmpegSnapshot.Path)
	if err != nil || ffmpegIdentity != naisNetIdentity {
		return EnrollmentCandidateEvidence{}, errors.New("sealed candidate FFmpeg identity is invalid")
	}
	if err := ffmpegSnapshot.Revalidate(); err != nil {
		return EnrollmentCandidateEvidence{}, errors.New("sealed candidate FFmpeg changed during inspection")
	}
	return EnrollmentCandidateEvidence{
		SchemaVersion: 1, Version: options.Version, Tag: options.Tag, Commit: options.Commit,
		SignedFileSHA256: artifactSHA256, SignedFileSize: int64(len(artifactSnapshot.Bytes)), PEContentSHA256: peContentSHA256,
		RootSPKISHA256: options.ExpectedRootSHA256, RootKeyID: "sha256:" + options.ExpectedRootSHA256,
		BootstrapPolicySHA256: options.ExpectedBootstrapPolicySHA256, BootstrapPolicyEpoch: bootstrap.Epoch, BootstrapSignatureStatus: "Valid",
		OuterIdentity: outerIdentity, AuthenticodeStatus: "Valid",
		FFmpegVersion: manifest.Version, FFmpegSHA256: manifest.SHA256, FFmpegSize: manifest.Size,
		FFmpegArchiveSHA256: sha256HexBytes(archive), FFmpegManifestSHA256: sha256HexBytes(manifestBytes), FFmpegIdentity: ffmpegIdentity, FFmpegSignatureStatus: "Valid",
	}, nil
}
