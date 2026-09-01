package artifactinspect

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/securefile"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

var (
	rushRushIdentity = certidentity.Identity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}
	naisNetIdentity  = certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
)

type VerifyArtifactOptions struct {
	UnsignedPath, SignedPath              string
	SealedDirectory                       string
	Version, Tag, Commit                  string
	RootSPKIPath, ExpectedRootSHA256      string
	BootstrapPolicyPath                   string
	ExpectedBootstrapPolicySHA256         string
	ExpectedBootstrapPolicyEpoch          uint64
	AuthorizationPolicyPath               string
	ExpectedAuthorizationPolicySHA256     string
	ExpectedAuthorizationPolicyEpoch      uint64
	StableArtifactPath, StableTag         string
	StableChannel                         updatepolicy.Channel
	FFmpegArchivePath, FFmpegManifestPath string
	Now                                   time.Time
	InspectAuthenticode                   func(string) (certidentity.Identity, error)
}

type ArtifactEvidence struct {
	Version                   string                `json:"version"`
	Tag                       string                `json:"tag"`
	Commit                    string                `json:"commit"`
	PEContentSHA256           string                `json:"peContentSha256"`
	SignedFileSHA256          string                `json:"signedFileSha256"`
	SignedFileSize            int64                 `json:"signedFileSize"`
	RootSPKISHA256            string                `json:"rootSpkiSha256"`
	BootstrapPolicySHA256     string                `json:"bootstrapPolicySha256"`
	BootstrapPolicyEpoch      uint64                `json:"bootstrapPolicyEpoch"`
	AuthorizationPolicySHA256 string                `json:"authorizationPolicySha256"`
	AuthorizationPolicyEpoch  uint64                `json:"authorizationPolicyEpoch"`
	OuterIdentity             certidentity.Identity `json:"outerIdentity"`
	FFmpegVersion             string                `json:"ffmpegVersion"`
	FFmpegSHA256              string                `json:"ffmpegSha256"`
	FFmpegSize                int64                 `json:"ffmpegSize"`
	FFmpegIdentity            certidentity.Identity `json:"ffmpegIdentity"`
}

type VerifyStaticOptions struct {
	UnsignedPath, SignedPath              string
	SealedDirectory                       string
	Version, Commit                       string
	ExpectedIdentity                      certidentity.Identity
	FFmpegArchivePath, FFmpegManifestPath string
	InspectAuthenticode                   func(string) (certidentity.Identity, error)
}

type StablePolicyOptions struct {
	RootDER, PolicyBytes []byte
	ExpectedEpoch        uint64
	ArtifactPath, Tag    string
	Channel              updatepolicy.Channel
	Now                  time.Time
	InspectAuthenticode  func(string) (certidentity.Identity, error)
}

type StablePolicyEvidence struct {
	PolicyEpoch          uint64                `json:"policyEpoch"`
	PolicySHA256         string                `json:"policySha256"`
	StableTag            string                `json:"stableTag"`
	StableChannel        updatepolicy.Channel  `json:"stableChannel"`
	StableArtifactSHA256 string                `json:"stableArtifactSha256"`
	StableIdentity       certidentity.Identity `json:"stableIdentity"`
}

func VerifyStaticArtifact(options VerifyStaticOptions) (ArtifactEvidence, error) {
	unsigned, err := securefile.ReadBoundedRegular(options.UnsignedPath, 128<<20, nil)
	if err != nil {
		return ArtifactEvidence{}, errors.New("unsigned PE is unavailable")
	}
	signedSnapshot, err := securefile.SnapshotRegular(options.SignedPath, 128<<20, "gift-panel-static-inspector-", "signed.exe")
	if err != nil {
		return ArtifactEvidence{}, errors.New("signed PE is unavailable")
	}
	defer signedSnapshot.Close()
	signed := signedSnapshot.Bytes
	unsignedDigest, err := AuthenticodeContentSHA256(unsigned)
	if err != nil {
		return ArtifactEvidence{}, err
	}
	signedDigest, err := AuthenticodeContentSHA256(signed)
	if err != nil || signedDigest != unsignedDigest {
		return ArtifactEvidence{}, errors.New("signed artifact does not match unsigned PE content")
	}
	versionOK, _ := CoveredContains(signed, []byte(options.Version))
	commitOK, _ := CoveredContains(signed, []byte(options.Commit))
	if !versionOK || !commitOK {
		return ArtifactEvidence{}, errors.New("static artifact version binding is invalid")
	}
	archive, err := securefile.ReadBoundedRegular(options.FFmpegArchivePath, 40<<20, nil)
	if err != nil {
		return ArtifactEvidence{}, err
	}
	manifestBytes, err := securefile.ReadBoundedRegular(options.FFmpegManifestPath, 64<<10, nil)
	if err != nil {
		return ArtifactEvidence{}, err
	}
	archiveOK, _ := CoveredContains(signed, archive)
	manifestOK, _ := CoveredContains(signed, manifestBytes)
	if !archiveOK || !manifestOK {
		return ArtifactEvidence{}, errors.New("static artifact embedded FFmpeg binding is invalid")
	}
	var manifest struct {
		Version, SHA256 string
		Size            int64
		Authenticode    bool
	}
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.Version != "9.0" || !manifest.Authenticode {
		return ArtifactEvidence{}, errors.New("static FFmpeg manifest is invalid")
	}
	ffmpeg, err := extractSingleFFmpeg(archive, manifest.Size)
	if err != nil || sha256HexBytes(ffmpeg) != manifest.SHA256 {
		return ArtifactEvidence{}, errors.New("static FFmpeg bytes are invalid")
	}
	inspect := options.InspectAuthenticode
	if inspect == nil {
		inspect = InspectAuthenticodeFile
	}
	outer, err := inspect(signedSnapshot.Path)
	if err != nil || outer != options.ExpectedIdentity {
		return ArtifactEvidence{}, errors.New("static outer identity is invalid")
	}
	if err := signedSnapshot.Revalidate(); err != nil {
		return ArtifactEvidence{}, errors.New("static outer snapshot changed during Authenticode inspection")
	}
	ffmpegSnapshot, err := securefile.SnapshotBytes(ffmpeg, "gift-panel-static-ffmpeg-", "ffmpeg.exe")
	if err != nil {
		return ArtifactEvidence{}, errors.New("static FFmpeg inspection failed")
	}
	defer ffmpegSnapshot.Close()
	inner, err := inspect(ffmpegSnapshot.Path)
	if err != nil || inner != naisNetIdentity {
		return ArtifactEvidence{}, errors.New("static FFmpeg identity is invalid")
	}
	if err := ffmpegSnapshot.Revalidate(); err != nil {
		return ArtifactEvidence{}, errors.New("static FFmpeg snapshot changed during Authenticode inspection")
	}
	sealed, err := signedSnapshot.SealContentAddressed(options.SealedDirectory, ".exe", nil)
	if err != nil {
		return ArtifactEvidence{}, errors.New("verified static executable could not be sealed")
	}
	return ArtifactEvidence{Version: options.Version, Commit: options.Commit, PEContentSHA256: signedDigest, SignedFileSHA256: sealed.SHA256, SignedFileSize: sealed.Size, OuterIdentity: outer, FFmpegVersion: manifest.Version, FFmpegSHA256: manifest.SHA256, FFmpegSize: manifest.Size, FFmpegIdentity: inner}, nil
}

func VerifyBoundArtifact(options VerifyArtifactOptions) (ArtifactEvidence, error) {
	unsigned, err := securefile.ReadBoundedRegular(options.UnsignedPath, 128<<20, nil)
	if err != nil {
		return ArtifactEvidence{}, errors.New("unsigned PE is unavailable")
	}
	signedSnapshot, err := securefile.SnapshotRegular(options.SignedPath, 128<<20, "gift-panel-artifact-inspector-", "signed.exe")
	if err != nil {
		return ArtifactEvidence{}, errors.New("signed PE is unavailable")
	}
	defer signedSnapshot.Close()
	signed := signedSnapshot.Bytes
	unsignedDigest, err := AuthenticodeContentSHA256(unsigned)
	if err != nil {
		return ArtifactEvidence{}, errors.New("unsigned PE content is invalid")
	}
	signedDigest, err := AuthenticodeContentSHA256(signed)
	if err != nil || signedDigest != unsignedDigest {
		return ArtifactEvidence{}, errors.New("signed artifact does not match unsigned PE content")
	}
	versionCovered, versionErr := CoveredContains(signed, []byte(options.Version))
	commitCovered, commitErr := CoveredContains(signed, []byte(options.Commit))
	if options.Tag != "v"+options.Version || options.Version == "" || !isLowerHex(options.Commit, 40) || versionErr != nil || commitErr != nil || !versionCovered || !commitCovered {
		return ArtifactEvidence{}, errors.New("signed artifact version, tag, or commit binding is invalid")
	}

	rootDER, err := securefile.ReadBoundedRegular(options.RootSPKIPath, 4<<10, nil)
	if err != nil || sha256HexBytes(rootDER) != options.ExpectedRootSHA256 {
		return ArtifactEvidence{}, errors.New("reviewed trust root is invalid")
	}
	bootstrapPolicy, err := securefile.ReadBoundedRegular(options.BootstrapPolicyPath, 256<<10, nil)
	if err != nil || sha256HexBytes(bootstrapPolicy) != options.ExpectedBootstrapPolicySHA256 {
		return ArtifactEvidence{}, errors.New("reviewed bootstrap policy is invalid")
	}
	authorizationPolicy, err := securefile.ReadBoundedRegular(options.AuthorizationPolicyPath, 256<<10, nil)
	if err != nil || sha256HexBytes(authorizationPolicy) != options.ExpectedAuthorizationPolicySHA256 {
		return ArtifactEvidence{}, errors.New("reviewed authorization policy is invalid")
	}
	if options.ExpectedAuthorizationPolicyEpoch <= options.ExpectedBootstrapPolicyEpoch ||
		options.ExpectedAuthorizationPolicySHA256 == options.ExpectedBootstrapPolicySHA256 || bytes.Equal(authorizationPolicy, bootstrapPolicy) {
		return ArtifactEvidence{}, errors.New("authorization policy must advance bootstrap policy")
	}
	rootCovered, rootCoveredErr := CoveredContains(signed, []byte(base64.StdEncoding.EncodeToString(rootDER)))
	bootstrapCovered, bootstrapCoveredErr := CoveredContains(signed, []byte(base64.StdEncoding.EncodeToString(bootstrapPolicy)))
	if rootCoveredErr != nil || bootstrapCoveredErr != nil || !rootCovered || !bootstrapCovered {
		return ArtifactEvidence{}, errors.New("signed artifact embedded trust bytes do not match reviewed inputs")
	}
	parsedRoot, err := x509.ParsePKIXPublicKey(rootDER)
	root, ok := parsedRoot.(*ecdsa.PublicKey)
	if err != nil || !ok {
		return ArtifactEvidence{}, errors.New("reviewed trust root is not P-256")
	}
	bootstrap, err := updatepolicy.ParseAndVerify(bootstrapPolicy, root, options.Now)
	if err != nil || bootstrap.Epoch != options.ExpectedBootstrapPolicyEpoch {
		return ArtifactEvidence{}, errors.New("bootstrap policy signature or epoch is invalid")
	}
	inspect := options.InspectAuthenticode
	if inspect == nil {
		inspect = InspectAuthenticodeFile
	}
	stableEvidence, err := VerifyStableArtifactPolicy(StablePolicyOptions{
		RootDER: rootDER, PolicyBytes: authorizationPolicy, ExpectedEpoch: options.ExpectedAuthorizationPolicyEpoch,
		ArtifactPath: options.StableArtifactPath, Tag: options.StableTag, Channel: options.StableChannel,
		Now: options.Now, InspectAuthenticode: inspect,
	})
	if err != nil {
		return ArtifactEvidence{}, err
	}

	archive, err := securefile.ReadBoundedRegular(options.FFmpegArchivePath, 40<<20, nil)
	if err != nil {
		return ArtifactEvidence{}, errors.New("reviewed FFmpeg archive is unavailable")
	}
	manifestBytes, err := securefile.ReadBoundedRegular(options.FFmpegManifestPath, 64<<10, nil)
	if err != nil {
		return ArtifactEvidence{}, errors.New("reviewed FFmpeg manifest is unavailable")
	}
	archiveCovered, archiveCoveredErr := CoveredContains(signed, archive)
	manifestCovered, manifestCoveredErr := CoveredContains(signed, manifestBytes)
	if archiveCoveredErr != nil || manifestCoveredErr != nil || !archiveCovered || !manifestCovered {
		return ArtifactEvidence{}, errors.New("signed artifact embedded FFmpeg does not match reviewed inputs")
	}
	var manifest struct {
		Version      string `json:"version"`
		SHA256       string `json:"sha256"`
		Size         int64  `json:"size"`
		Authenticode bool   `json:"authenticode"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.Version != "9.0" || !manifest.Authenticode || manifest.Size <= 0 || !isLowerHex(manifest.SHA256, 64) {
		return ArtifactEvidence{}, errors.New("reviewed FFmpeg manifest is invalid")
	}
	ffmpeg, err := extractSingleFFmpeg(archive, manifest.Size)
	if err != nil || sha256HexBytes(ffmpeg) != manifest.SHA256 {
		return ArtifactEvidence{}, errors.New("reviewed FFmpeg archive does not match manifest")
	}

	outerIdentity, err := inspect(signedSnapshot.Path)
	if err != nil || outerIdentity != rushRushIdentity {
		return ArtifactEvidence{}, errors.New("signed outer Authenticode identity is invalid")
	}
	if err := signedSnapshot.Revalidate(); err != nil {
		return ArtifactEvidence{}, errors.New("signed outer snapshot changed during Authenticode inspection")
	}
	if err := bootstrap.AuthorizeAt(updatepolicy.ArtifactIdentity{
		Tag: options.Tag, Channel: updatepolicy.ChannelLegacyRushRush,
		Certificate: outerIdentity,
	}, options.Now); err != nil {
		return ArtifactEvidence{}, errors.New("embedded bootstrap policy does not authorize the exact bridge")
	}
	ffmpegSnapshot, err := securefile.SnapshotBytes(ffmpeg, "gift-panel-artifact-ffmpeg-", "ffmpeg.exe")
	if err != nil {
		return ArtifactEvidence{}, errors.New("FFmpeg inspection file is unavailable")
	}
	defer ffmpegSnapshot.Close()
	ffmpegIdentity, err := inspect(ffmpegSnapshot.Path)
	if err != nil || ffmpegIdentity != naisNetIdentity {
		return ArtifactEvidence{}, errors.New("embedded FFmpeg Authenticode identity is invalid")
	}
	if err := ffmpegSnapshot.Revalidate(); err != nil {
		return ArtifactEvidence{}, errors.New("embedded FFmpeg snapshot changed during Authenticode inspection")
	}
	sealed, err := signedSnapshot.SealContentAddressed(options.SealedDirectory, ".exe", nil)
	if err != nil {
		return ArtifactEvidence{}, errors.New("verified bridge executable could not be sealed")
	}

	return ArtifactEvidence{
		Version: options.Version, Tag: options.Tag, Commit: options.Commit,
		PEContentSHA256: signedDigest, SignedFileSHA256: sealed.SHA256, SignedFileSize: sealed.Size,
		RootSPKISHA256:            options.ExpectedRootSHA256,
		BootstrapPolicySHA256:     options.ExpectedBootstrapPolicySHA256,
		BootstrapPolicyEpoch:      bootstrap.Epoch,
		AuthorizationPolicySHA256: options.ExpectedAuthorizationPolicySHA256,
		AuthorizationPolicyEpoch:  stableEvidence.PolicyEpoch,
		OuterIdentity:             outerIdentity,
		FFmpegVersion:             manifest.Version, FFmpegSHA256: manifest.SHA256, FFmpegSize: manifest.Size, FFmpegIdentity: ffmpegIdentity,
	}, nil
}

func VerifyStableArtifactPolicy(options StablePolicyOptions) (StablePolicyEvidence, error) {
	stableSnapshot, err := securefile.SnapshotRegular(options.ArtifactPath, 128<<20, "gift-panel-stable-inspector-", "stable.exe")
	if err != nil {
		return StablePolicyEvidence{}, errors.New("stable convergence artifact is invalid")
	}
	defer stableSnapshot.Close()
	stableSHA256 := sha256HexBytes(stableSnapshot.Bytes)
	inspect := options.InspectAuthenticode
	if inspect == nil {
		inspect = InspectAuthenticodeFile
	}
	stableIdentity, err := inspect(stableSnapshot.Path)
	if err != nil {
		return StablePolicyEvidence{}, errors.New("stable convergence Authenticode is invalid")
	}
	if err := stableSnapshot.Revalidate(); err != nil {
		return StablePolicyEvidence{}, errors.New("stable convergence artifact changed during Authenticode inspection")
	}
	epoch, err := VerifyStablePolicy(options.RootDER, options.PolicyBytes, options.ExpectedEpoch, updatepolicy.ArtifactIdentity{
		Tag: options.Tag, Channel: options.Channel, SHA256: stableSHA256, Certificate: stableIdentity,
	}, options.Now)
	if err != nil {
		return StablePolicyEvidence{}, err
	}
	return StablePolicyEvidence{
		PolicyEpoch: epoch, PolicySHA256: sha256HexBytes(options.PolicyBytes), StableTag: options.Tag,
		StableChannel: options.Channel, StableArtifactSHA256: stableSHA256, StableIdentity: stableIdentity,
	}, nil
}

func extractSingleFFmpeg(archive []byte, expectedSize int64) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || len(reader.File) != 1 {
		return nil, errors.New("invalid FFmpeg archive")
	}
	entry := reader.File[0]
	if entry.Name != "ffmpeg.exe" || entry.FileInfo().IsDir() || int64(entry.UncompressedSize64) != expectedSize {
		return nil, errors.New("invalid FFmpeg archive entry")
	}
	opened, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	contents, err := io.ReadAll(io.LimitReader(opened, expectedSize+1))
	if err != nil || int64(len(contents)) != expectedSize {
		return nil, errors.New("invalid FFmpeg archive contents")
	}
	return contents, nil
}

func VerifyStablePolicy(rootDER, policyBytes []byte, expectedEpoch uint64, stable updatepolicy.ArtifactIdentity, now time.Time) (uint64, error) {
	parsedRoot, err := x509.ParsePKIXPublicKey(rootDER)
	root, ok := parsedRoot.(*ecdsa.PublicKey)
	if err != nil || !ok {
		return 0, errors.New("reviewed trust root is not P-256")
	}
	verified, err := updatepolicy.ParseAndVerify(policyBytes, root, now)
	if err != nil || verified.Epoch != expectedEpoch {
		return 0, errors.New("reviewed trust policy epoch is invalid")
	}
	stable.RequireManifestSHA256 = true
	if err := verified.AuthorizeAt(stable, now); err != nil {
		return 0, errors.New("reviewed trust policy manifest authorization is invalid")
	}
	return verified.Epoch, nil
}
func sha256HexBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
func isLowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
