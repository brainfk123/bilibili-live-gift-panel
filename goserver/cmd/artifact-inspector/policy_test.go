package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/certidentity"
	"bilibili-live-gift-panel/internal/updatepolicy"
)

func TestVerifyPolicyCommandAuthorizesTheActualStableArtifactIdentity(t *testing.T) {
	args, wantHash := stablePolicyCommandFixture(t)
	actual := certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	var output bytes.Buffer
	if err := runVerifyPolicyWithInspector(args, &output, func(string) (certidentity.Identity, error) { return actual, nil }, func() time.Time {
		return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	}); err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		SchemaVersion        uint64                `json:"schemaVersion"`
		PolicyEpoch          uint64                `json:"policyEpoch"`
		StableTag            string                `json:"stableTag"`
		StableChannel        updatepolicy.Channel  `json:"stableChannel"`
		StableArtifactSHA256 string                `json:"stableArtifactSha256"`
		StableIdentity       certidentity.Identity `json:"stableIdentity"`
	}
	if err := json.Unmarshal(output.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 1 || evidence.PolicyEpoch != 1 || evidence.StableTag != "v0.4.12" || evidence.StableChannel != updatepolicy.ChannelStable || evidence.StableArtifactSHA256 != wantHash || evidence.StableIdentity != actual {
		t.Fatalf("evidence = %#v", evidence)
	}
	wrong := actual
	wrong.Organization = "Hard-coded tuple must not replace the observed signer"
	if err := runVerifyPolicyWithInspector(args, &bytes.Buffer{}, func(string) (certidentity.Identity, error) { return wrong, nil }, func() time.Time {
		return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	}); err == nil {
		t.Fatal("wrong actual signer was authorized")
	}
}

func TestVerifyPolicyCommandRejectsDuplicateMachineEnvelopeFields(t *testing.T) {
	args, _ := stablePolicyCommandFixture(t)
	envelopePath := commandArgument(args, "--verified-bundle")
	contents, err := os.ReadFile(envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	contents = bytes.Replace(contents, []byte(`"schemaVersion":2`), []byte(`"schemaVersion":2,"schemaVersion":2`), 1)
	if err := os.WriteFile(envelopePath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	actual := certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	if err := runVerifyPolicyWithInspector(args, &bytes.Buffer{}, func(string) (certidentity.Identity, error) { return actual, nil }, func() time.Time {
		return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	}); err == nil {
		t.Fatal("duplicate machine-envelope field was accepted")
	}
}

func TestVerifyPolicyCommandRejectsRootAndEnvelopeSymlinksWithoutTrustedOutput(t *testing.T) {
	for _, flagName := range []string{"--root-spki", "--verified-bundle"} {
		t.Run(flagName, func(t *testing.T) {
			args, _ := stablePolicyCommandFixture(t)
			path := commandArgument(args, flagName)
			realPath := path + ".real"
			if err := os.Rename(path, realPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(realPath, path); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			var output bytes.Buffer
			err := runVerifyPolicyWithInspector(args, &output, func(string) (certidentity.Identity, error) {
				return certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}, nil
			}, func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) })
			if err == nil || output.Len() != 0 {
				t.Fatalf("symlinked reviewed input produced trusted output: error=%v output=%q", err, output.String())
			}
		})
	}
}

func TestVerifyPolicyCommandPreventsRootAndEnvelopeSwapRestoreWhileOpen(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the production Authenticode workflow is Windows-only and relies on Windows share-mode locking")
	}
	for _, flagName := range []string{"--root-spki", "--verified-bundle"} {
		t.Run(flagName, func(t *testing.T) {
			args, _ := stablePolicyCommandFixture(t)
			path := commandArgument(args, flagName)
			var output bytes.Buffer
			err := runVerifyPolicyWithInspector(args, &output, func(string) (certidentity.Identity, error) {
				return certidentity.Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}, nil
			}, func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }, map[string]boundedReadHooks{
				path: {AfterOpen: func() error {
					parked := path + ".parked"
					if renameErr := os.Rename(path, parked); renameErr != nil {
						return renameErr
					}
					_ = os.Rename(parked, path)
					return nil
				}},
			})
			if err == nil || output.Len() != 0 {
				t.Fatalf("swap/restore attempt produced trusted output: error=%v output=%q", err, output.String())
			}
		})
	}
}

func commandArgument(args []string, name string) string {
	for index := range args {
		if args[index] == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func stablePolicyCommandFixture(t testing.TB) ([]string, string) {
	t.Helper()
	root := t.TempDir()
	artifact := []byte("actual stable executable fixture")
	artifactHash := sha256HexCommand(artifact)
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	rule := updatepolicy.PublisherRule{ID: "observed", Role: "primary", Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094", AllowedChannel: updatepolicy.ChannelStable, AllowedTags: []string{"v0.4.12"}, ManifestSHA256: artifactHash}
	signed := updatepolicy.Signed{Epoch: 1, ExpiresAt: "2030-01-01T00:00:00Z", Publishers: []updatepolicy.PublisherRule{rule}}
	canonical, err := updatepolicy.CanonicalSigned(signed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	signature, err := ecdsa.SignASN1(rand.Reader, private, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(updatepolicy.Document{Signed: signed, Signatures: []updatepolicy.Signature{{Algorithm: "ecdsa-p256-sha256", Signature: base64.StdEncoding.EncodeToString(signature)}}})
	if err != nil {
		t.Fatal(err)
	}
	audit := []byte(`{"fixture":"audit"}`)
	policyRecord := map[string]any{"name": "policy.json", "length": len(policy), "sha256": sha256HexCommand(policy)}
	auditRecord := map[string]any{"name": "audit.json", "length": len(audit), "sha256": sha256HexCommand(audit)}
	envelope, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"verification":  map[string]any{"epoch": 1, "expectedPreviousEpoch": 0, "spkiSha256": sha256HexCommand(spki)},
		"commit":        map[string]any{"schemaVersion": 1, "policy": policyRecord, "audit": auditRecord},
		"policy":        map[string]any{"name": "policy.json", "length": len(policy), "sha256": sha256HexCommand(policy), "bytesBase64": base64.StdEncoding.EncodeToString(policy)},
		"audit":         map[string]any{"name": "audit.json", "length": len(audit), "sha256": sha256HexCommand(audit), "bytesBase64": base64.StdEncoding.EncodeToString(audit)},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string][]byte{"root.der": spki, "verified-bundle.json": envelope, "stable.exe": artifact}
	for name, contents := range paths {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{
		"--root-spki", filepath.Join(root, "root.der"),
		"--verified-bundle", filepath.Join(root, "verified-bundle.json"),
		"--epoch", "1",
		"--stable-artifact", filepath.Join(root, "stable.exe"),
		"--stable-tag", "v0.4.12",
		"--stable-channel", "stable",
	}, artifactHash
}

func sha256HexCommand(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
