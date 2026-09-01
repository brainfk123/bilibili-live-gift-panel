package artifactinspect

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
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
	if options.Tag != "v"+options.Version || options.Version == "" || !isLowerHex(options.Commit, 40) ||
		!bytes.Contains(signed, []byte(options.Version)) || !bytes.Contains(signed, []byte(options.Commit)) {
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
	if !bytes.Contains(signed, []byte(base64.StdEncoding.EncodeToString(rootDER))) || !bytes.Contains(signed, []byte(base64.StdEncoding.EncodeToString(policy))) {
		return ArtifactEvidence{}, errors.New("signed artifact embedded trust bytes do not match reviewed inputs")
	}
	policyEpoch, err := verifyStablePolicy(rootDER, policy, options.ExpectedPolicyEpoch, options.Now)
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
	if !bytes.Contains(signed, archive) || !bytes.Contains(signed, manifestBytes) {
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

type signedPolicyDocument struct {
	Signed     json.RawMessage                         `json:"signed"`
	Signatures []struct{ Algorithm, Signature string } `json:"signatures"`
}
type signedPolicy struct {
	Epoch      uint64 `json:"epoch"`
	ExpiresAt  string `json:"expiresAt"`
	Publishers []struct {
		Role, Country, Organization, OrganizationID, AllowedChannel string
		AllowedTags                                                 []string `json:"allowedTags"`
	} `json:"publishers"`
}

func verifyStablePolicy(rootDER, policyBytes []byte, expectedEpoch uint64, now time.Time) (uint64, error) {
	parsedRoot, err := x509.ParsePKIXPublicKey(rootDER)
	root, ok := parsedRoot.(*ecdsa.PublicKey)
	if err != nil || !ok || root.Curve != elliptic.P256() {
		return 0, errors.New("reviewed trust root is not P-256")
	}
	decoder := json.NewDecoder(bytes.NewReader(policyBytes))
	decoder.DisallowUnknownFields()
	var document signedPolicyDocument
	if err := decoder.Decode(&document); err != nil || len(document.Signed) == 0 || len(document.Signatures) != 1 || document.Signatures[0].Algorithm != "ecdsa-p256-sha256" {
		return 0, errors.New("reviewed trust policy envelope is invalid")
	}
	var signed signedPolicy
	if err := json.Unmarshal(document.Signed, &signed); err != nil || signed.Epoch != expectedEpoch {
		return 0, errors.New("reviewed trust policy epoch is invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339, signed.ExpiresAt)
	if err != nil || !expiresAt.After(now.UTC()) {
		return 0, errors.New("reviewed trust policy is expired")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(document.Signatures[0].Signature)
	digest := sha256.Sum256(document.Signed)
	if err != nil || !ecdsa.VerifyASN1(root, digest[:], signature) {
		return 0, errors.New("reviewed trust policy signature is invalid")
	}
	stable := 0
	for _, publisher := range signed.Publishers {
		if publisher.Role == "primary" && publisher.Country == naisNetIdentity.Country && publisher.Organization == naisNetIdentity.Organization &&
			publisher.OrganizationID == naisNetIdentity.OrganizationID && publisher.AllowedChannel == "stable" && containsExact(publisher.AllowedTags, "v0.4.12") {
			stable++
		}
	}
	if stable != 1 {
		return 0, errors.New("reviewed trust policy does not authorize exact NaisNet stable v0.4.12")
	}
	return signed.Epoch, nil
}

func containsExact(values []string, want string) bool {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count == 1
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
