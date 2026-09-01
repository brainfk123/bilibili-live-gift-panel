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
	"os"
	"path/filepath"
	"strings"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

var (
	rushRushIdentity = certidentity.Identity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}
	naisNetIdentity  = certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
)

type VerifyArtifactOptions struct {
	UnsignedPath, SignedPath              string
	Version, Tag, Commit                  string
	RootSPKIPath, ExpectedRootSHA256      string
	PolicyPath, ExpectedPolicySHA256      string
	ExpectedPolicyEpoch                   uint64
	StableArtifactSHA256                  string
	FFmpegArchivePath, FFmpegManifestPath string
	Now                                   time.Time
	InspectAuthenticode                   func(string) (certidentity.Identity, error)
}

type ArtifactEvidence struct {
	Version         string                `json:"version"`
	Tag             string                `json:"tag"`
	Commit          string                `json:"commit"`
	PEContentSHA256 string                `json:"peContentSha256"`
	RootSPKISHA256  string                `json:"rootSpkiSha256"`
	PolicySHA256    string                `json:"policySha256"`
	PolicyEpoch     uint64                `json:"policyEpoch"`
	OuterIdentity   certidentity.Identity `json:"outerIdentity"`
	FFmpegVersion   string                `json:"ffmpegVersion"`
	FFmpegSHA256    string                `json:"ffmpegSha256"`
	FFmpegSize      int64                 `json:"ffmpegSize"`
	FFmpegIdentity  certidentity.Identity `json:"ffmpegIdentity"`
}

type VerifyStaticOptions struct {
	UnsignedPath, SignedPath              string
	Version, Commit                       string
	ExpectedIdentity                      certidentity.Identity
	FFmpegArchivePath, FFmpegManifestPath string
	InspectAuthenticode                   func(string) (certidentity.Identity, error)
}

func VerifyStaticArtifact(options VerifyStaticOptions) (ArtifactEvidence, error) {
	unsigned, err := os.ReadFile(options.UnsignedPath)
	if err != nil {
		return ArtifactEvidence{}, errors.New("unsigned PE is unavailable")
	}
	signed, err := os.ReadFile(options.SignedPath)
	if err != nil {
		return ArtifactEvidence{}, errors.New("signed PE is unavailable")
	}
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
	archive, err := os.ReadFile(options.FFmpegArchivePath)
	if err != nil {
		return ArtifactEvidence{}, err
	}
	manifestBytes, err := os.ReadFile(options.FFmpegManifestPath)
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
	outer, err := inspect(options.SignedPath)
	if err != nil || outer != options.ExpectedIdentity {
		return ArtifactEvidence{}, errors.New("static outer identity is invalid")
	}
	root, err := os.MkdirTemp("", "gift-panel-static-inspector-")
	if err != nil {
		return ArtifactEvidence{}, err
	}
	defer os.RemoveAll(root)
	ffmpegPath := filepath.Join(root, "ffmpeg.exe")
	if os.WriteFile(ffmpegPath, ffmpeg, 0o600) != nil {
		return ArtifactEvidence{}, errors.New("static FFmpeg inspection failed")
	}
	inner, err := inspect(ffmpegPath)
	if err != nil || inner != naisNetIdentity {
		return ArtifactEvidence{}, errors.New("static FFmpeg identity is invalid")
	}
	return ArtifactEvidence{Version: options.Version, Commit: options.Commit, PEContentSHA256: signedDigest, OuterIdentity: outer, FFmpegVersion: manifest.Version, FFmpegSHA256: manifest.SHA256, FFmpegSize: manifest.Size, FFmpegIdentity: inner}, nil
}

func VerifyBoundArtifact(options VerifyArtifactOptions) (ArtifactEvidence, error) {
	unsigned, err := os.ReadFile(options.UnsignedPath)
	if err != nil {
		return ArtifactEvidence{}, errors.New("unsigned PE is unavailable")
	}
	signed, err := os.ReadFile(options.SignedPath)
	if err != nil {
		return ArtifactEvidence{}, errors.New("signed PE is unavailable")
	}
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

	rootDER, err := os.ReadFile(options.RootSPKIPath)
	if err != nil || sha256HexBytes(rootDER) != options.ExpectedRootSHA256 {
		return ArtifactEvidence{}, errors.New("reviewed trust root is invalid")
	}
	policy, err := os.ReadFile(options.PolicyPath)
	if err != nil || sha256HexBytes(policy) != options.ExpectedPolicySHA256 {
		return ArtifactEvidence{}, errors.New("reviewed trust policy is invalid")
	}
	rootCovered, rootCoveredErr := CoveredContains(signed, []byte(base64.StdEncoding.EncodeToString(rootDER)))
	policyCovered, policyCoveredErr := CoveredContains(signed, []byte(base64.StdEncoding.EncodeToString(policy)))
	if rootCoveredErr != nil || policyCoveredErr != nil || !rootCovered || !policyCovered {
		return ArtifactEvidence{}, errors.New("signed artifact embedded trust bytes do not match reviewed inputs")
	}
	policyEpoch, err := VerifyStablePolicy(rootDER, policy, options.ExpectedPolicyEpoch, options.StableArtifactSHA256, options.Now)
	if err != nil {
		return ArtifactEvidence{}, err
	}

	archive, err := os.ReadFile(options.FFmpegArchivePath)
	if err != nil {
		return ArtifactEvidence{}, errors.New("reviewed FFmpeg archive is unavailable")
	}
	manifestBytes, err := os.ReadFile(options.FFmpegManifestPath)
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

	inspect := options.InspectAuthenticode
	if inspect == nil {
		inspect = InspectAuthenticodeFile
	}
	outerIdentity, err := inspect(options.SignedPath)
	if err != nil || outerIdentity != rushRushIdentity {
		return ArtifactEvidence{}, errors.New("signed outer Authenticode identity is invalid")
	}
	temporaryRoot, err := os.MkdirTemp("", "gift-panel-artifact-inspector-")
	if err != nil {
		return ArtifactEvidence{}, errors.New("FFmpeg inspection workspace is unavailable")
	}
	defer os.RemoveAll(temporaryRoot)
	ffmpegPath := filepath.Join(temporaryRoot, "ffmpeg.exe")
	if err := os.WriteFile(ffmpegPath, ffmpeg, 0o600); err != nil {
		return ArtifactEvidence{}, errors.New("FFmpeg inspection file is unavailable")
	}
	ffmpegIdentity, err := inspect(ffmpegPath)
	if err != nil || ffmpegIdentity != naisNetIdentity {
		return ArtifactEvidence{}, errors.New("embedded FFmpeg Authenticode identity is invalid")
	}

	return ArtifactEvidence{
		Version: options.Version, Tag: options.Tag, Commit: options.Commit,
		PEContentSHA256: signedDigest,
		RootSPKISHA256:  options.ExpectedRootSHA256,
		PolicySHA256:    options.ExpectedPolicySHA256, PolicyEpoch: policyEpoch,
		OuterIdentity: outerIdentity,
		FFmpegVersion: manifest.Version, FFmpegSHA256: manifest.SHA256, FFmpegSize: manifest.Size, FFmpegIdentity: ffmpegIdentity,
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

func VerifyStablePolicy(rootDER, policyBytes []byte, expectedEpoch uint64, stableSHA256 string, now time.Time) (uint64, error) {
	parsedRoot, err := x509.ParsePKIXPublicKey(rootDER)
	root, ok := parsedRoot.(*ecdsa.PublicKey)
	if err != nil || !ok {
		return 0, errors.New("reviewed trust root is not P-256")
	}
	verified, err := updatepolicy.ParseAndVerify(policyBytes, root, now)
	if err != nil || verified.Epoch != expectedEpoch {
		return 0, errors.New("reviewed trust policy epoch is invalid")
	}
	if err := verified.AuthorizeExactManifest(updatepolicy.ArtifactIdentity{Tag: "v0.4.12", Channel: updatepolicy.ChannelStable, SHA256: stableSHA256, Certificate: naisNetIdentity}); err != nil {
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
