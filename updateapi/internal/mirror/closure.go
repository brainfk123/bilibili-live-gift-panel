package mirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// ValidateLocalReleaseClosure runs the same GitHub metadata, checksum,
// fallback-manifest, and changelog validators used by a ByTag mirror run, but
// reads the already prepared bytes from one local directory.
func ValidateLocalReleaseClosure(metadata []byte, directory, exactTag string) error {
	payload, err := decodeGitHubRelease(bytes.NewReader(metadata))
	if err != nil {
		return err
	}
	candidate, err := validateGitHubRelease(payload)
	if err != nil {
		return err
	}
	if candidate.Tag != exactTag {
		return errors.New("local release closure tag mismatch")
	}
	bodies := make(map[string][]byte, 4)
	for _, name := range []string{AssetExecutable, AssetChecksum, AssetManifest, AssetChangelog} {
		asset, ok := candidate.Assets[name]
		if !ok {
			return errors.New("local release closure is missing a required asset")
		}
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || int64(len(body)) != asset.Size {
			return errors.New("local release closure asset size mismatch")
		}
		if err := validateOptionalGitHubDigest(asset.Digest, body); err != nil {
			return err
		}
		bodies[name] = body
	}
	digest := sha256.Sum256(bodies[AssetExecutable])
	hexDigest := hex.EncodeToString(digest[:])
	if err := validateStrictChecksum(bodies[AssetChecksum], hexDigest); err != nil {
		return err
	}
	if err := validateFallbackManifest(bodies[AssetManifest], candidate, hexDigest, int64(len(bodies[AssetExecutable]))); err != nil {
		return err
	}
	if err := validateCandidateChangelog(bodies[AssetChangelog], exactTag); err != nil {
		return err
	}
	return nil
}
