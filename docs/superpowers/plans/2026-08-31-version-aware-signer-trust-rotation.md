# Version-Aware Signer Trust Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Each task must use `superpowers:test-driven-development`; before any completion claim use `superpowers:verification-before-completion`.

**Goal:** Safely migrate existing v0.4.7 RushRush clients through an exact v0.4.11 bridge to the NaisNet v0.4.12 stable release, while making later same-legal-publisher certificate renewals automatic and requiring an independent Tencent Cloud KMS policy for any legal-publisher change.

**Architecture:** The Windows client verifies an embedded KMS SPKI root, a strict signed publisher policy, the downloaded asset hash, Windows Authenticode validity, and structured X.509 legal identity. The domestic update API routes the already-existing versioned `User-Agent` through an explicit allowlist to independent stable and legacy pointers. Stable publishing, bridge publishing, and publisher-policy rotation remain separate workflows and identities.

**Tech Stack:** Go 1.26, PowerShell Authenticode APIs, `crypto/x509`, ECDSA P-256/SHA-256, Tencent Cloud KMS Go SDK `github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/kms v1.3.168`, GitHub Actions, Tencent COS, Node.js/Vitest.

**Spec:** [`docs/superpowers/specs/2026-08-30-version-aware-signer-trust-rotation-design.md`](../specs/2026-08-30-version-aware-signer-trust-rotation-design.md)

## Global constraints

- Preserve the dirty main worktree. Execute this plan only in an isolated worktree.
- Start every implementation task with a failing focused test, make it pass, then run the listed regression suite.
- Make one product commit per task. Do not commit experimental fixtures, captured user logs, credentials, signed URLs, or generated private data.
- Do not mutate existing v0.4.7, v0.4.9, or v0.4.10 tags, Releases, assets, hashes, or COS immutable objects.
- Do not trust an Authenticode root/intermediate CA or a human-formatted Subject string as publisher identity.
- Do not log IP addresses, Bilibili identity/gift data, machine IDs, usernames, raw paths containing usernames, credentials, tokens, cookies, or signed URL query strings.
- KMS provisioning, KMS signing, policy publication, server deployment, tag push, Release publication, COS pointer advancement, and retirement are separate approval gates. Stop before each external mutation and obtain fresh confirmation.
- Stage order is fixed: prepare trust/routing → publish and observe NaisNet v0.4.12 for at least seven days → publish and activate RushRush v0.4.11 bridge → observe for at least seven days. Do not compress the observation windows.

## Task 1: Add the strict publisher-policy domain and canonical form

**Files:**

- Create: `goserver/update_trust_policy.go`
- Create: `goserver/update_trust_policy_test.go`
- Create: `goserver/testdata/update-trust/policy-epoch-1.json`
- Create: `goserver/testdata/update-trust/root-epoch-1-spki.der`

### Step 1: Write failing policy parsing and authorization tests

Add table tests covering the two approved identities, exact bridge scope, unknown fields, duplicate keys, trailing JSON, duplicate publisher IDs, invalid epochs/timestamps/tags, expired policy, and a different organization ID.

```go
func TestPublisherPolicyAuthorize(t *testing.T) {
	policy := mustVerifyPolicyFixture(t, "testdata/update-trust/policy-epoch-1.json")
	tests := []struct {
		name    string
		input   updateArtifactIdentity
		wantErr string
	}{
		{"stable NaisNet", updateArtifactIdentity{Tag: "v0.4.12", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}}, ""},
		{"exact bridge", updateArtifactIdentity{Tag: "v0.4.11", Channel: updateChannelLegacyRushRush, Certificate: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}, ""},
		{"RushRush cannot sign stable", updateArtifactIdentity{Tag: "v0.4.11", Channel: updateChannelStable, Certificate: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}, "publisher_not_authorized"},
		{"RushRush tag is exact", updateArtifactIdentity{Tag: "v0.4.12", Channel: updateChannelLegacyRushRush, Certificate: updateCertificateIdentity{Country: "CN", Organization: "RushRush Network Technology Ltd", OrganizationID: "91450900MADM3GLG5P"}}, "publisher_not_authorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := policy.Authorize(tt.input)
			assertErrorCode(t, err, tt.wantErr)
		})
	}
}
```

### Step 2: Run the focused test and confirm RED

Run: `go test ./... -run 'TestPublisherPolicy|TestCanonicalSignedPolicy'`

Working directory: `goserver`

Expected: build failure because the policy types and verifier do not exist.

### Step 3: Implement strict decoding, validation, canonicalization, and ECDSA verification

Use closed structs and a token-level duplicate-key check before `json.Decoder.DisallowUnknownFields`. Limit the document to 256 KiB and nesting depth to 16. Canonicalize only the `signed` member with repository-owned key ordering; do not sign or verify the `signatures` member.

```go
type updateChannel string

const (
	updateChannelStable         updateChannel = "stable"
	updateChannelLegacyRushRush updateChannel = "legacy-rushrush"
)

type updateCertificateIdentity struct {
	Country        string
	Organization   string
	OrganizationID string
}

type updateArtifactIdentity struct {
	Tag         string
	Channel     updateChannel
	SHA256      string
	Certificate updateCertificateIdentity
}

type verifiedUpdateTrustPolicy struct {
	Epoch     uint64
	ExpiresAt time.Time
	SignedRaw []byte
	Rules     []updatePublisherRule
}

func parseAndVerifyUpdateTrustPolicy(data []byte, root *ecdsa.PublicKey, now time.Time) (verifiedUpdateTrustPolicy, error)
func canonicalizePublisherPolicySigned(s publisherPolicySigned) ([]byte, error)
func (p verifiedUpdateTrustPolicy) Authorize(input updateArtifactIdentity) error
```

Authorization must compare normalized exact structured fields, exact channel, exact tag constraints, time window, and the manifest hash when a rule supplies a hash. The RushRush rule is invalid unless it is `role=bridge`, `allowedChannel=legacy-rushrush`, and `allowedTags=["v0.4.11"]`.

### Step 4: Add golden and fuzz tests

Verify the epoch-1 fixture with `ecdsa.VerifyASN1`, then fuzz duplicate fields, excessive depth, oversized input, noncanonical tags, and malformed ASN.1 signatures.

```go
func FuzzParsePublisherPolicy(f *testing.F) {
	f.Add(readFixture(f, "testdata/update-trust/policy-epoch-1.json"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 300<<10 { t.Skip() }
		_, _ = parseAndVerifyUpdateTrustPolicy(data, testRootPublicKey(t), pinnedPolicyTime)
	})
}
```

### Step 5: Run tests and commit

Run: `go test ./... -run 'TestPublisherPolicy|TestCanonicalSignedPolicy|FuzzParsePublisherPolicy'`

Run: `go test ./...`

Working directory: `goserver`

Commit: `git add goserver/update_trust_policy.go goserver/update_trust_policy_test.go goserver/testdata/update-trust && git commit -m "feat: verify signed publisher policy"`

## Task 2: Return and validate structured Authenticode certificate identity

**Files:**

- Modify: `goserver/auto_update_signature_windows.go`
- Modify: `goserver/auto_update_signature_other.go`
- Modify: `goserver/auto_update_signature_windows_test.go`
- Create: `goserver/update_certificate_identity.go`
- Create: `goserver/update_certificate_identity_test.go`

### Step 1: Write failing tests for DER parsing and renewal behavior

Tests must prove that two leaf certificates with different thumbprints/serials but the same `Country`, `Organization`, and subject organization identifier produce the same legal identity; a changed organization identifier must not match. Include missing/multiple Code Signing EKU cases.

```go
func TestCertificateLegalIdentityIgnoresLeafRenewalFields(t *testing.T) {
	a := mustParseUpdateCertificate(t, renewalLeafA)
	b := mustParseUpdateCertificate(t, renewalLeafB)
	if a.LegalIdentity != b.LegalIdentity {
		t.Fatalf("same publisher renewal changed legal identity: %#v != %#v", a, b)
	}
}
```

### Step 2: Run focused tests and confirm RED

Run: `go test ./... -run 'TestCertificateLegalIdentity|TestInspectAuthenticode'`

Working directory: `goserver`

Expected: missing structured certificate inspection API.

### Step 3: Change the PowerShell contract to status plus DER

The runner must output a single compressed JSON object. It must never return or parse the display Subject.

```powershell
$signature = Get-AuthenticodeSignature -LiteralPath $Path
[pscustomobject]@{
  status = [string]$signature.Status
  certificateDerBase64 = if ($null -eq $signature.SignerCertificate) { "" } else {
    [Convert]::ToBase64String($signature.SignerCertificate.RawData)
  }
} | ConvertTo-Json -Compress
```

```go
type authenticodeInspection struct {
	Status               string `json:"status"`
	CertificateDERBase64 string `json:"certificateDerBase64"`
}

type inspectedUpdateCertificate struct {
	LegalIdentity updateCertificateIdentity
	DER           []byte
}

func inspectAuthenticodeWithRunner(path string, run powershellRunner) (inspectedUpdateCertificate, error)
func parseUpdateSigningCertificate(der []byte) (inspectedUpdateCertificate, error)
```

Require status `Valid`, parse DER with `x509.ParseCertificate`, require the Code Signing EKU, and extract `Subject.Country[0]`, `Subject.Organization[0]`, and `Subject.SerialNumber`. Reject missing, multiple, empty, or ambiguous values.

### Step 4: Keep non-Windows behavior fail-closed

`auto_update_signature_other.go` must continue to return an explicit unsupported-platform error; do not turn it into a bypass.

### Step 5: Run tests and commit

Run: `go test ./... -run 'TestCertificateLegalIdentity|TestInspectAuthenticode'`

Run: `go test ./...`

Working directory: `goserver`

Commit: `git add goserver/auto_update_signature_* goserver/update_certificate_identity* && git commit -m "feat: inspect structured update signer identity"`

## Task 3: Embed the reviewed rotation root and bootstrap policy in enrollment builds

**Files:**

- Create: `goserver/update_trust_embed.go`
- Create: `goserver/update_trust_embed_test.go`
- Modify: `scripts/build-go.mjs`
- Modify: `scripts/build-go.test.ts`
- Create: `docs/security/update-trust-root.md`

### Step 1: Write failing build-contract tests

Require release builds that set `APP_UPDATE_TRUST_REQUIRED=1` to fail unless both reviewed values are supplied. Development builds may use checked-in public test fixtures only.

```ts
it("rejects a trust-enabled release without root and bootstrap policy", async () => {
  await expect(resolveGoLdflags({
    APP_UPDATE_TRUST_REQUIRED: "1",
    APP_UPDATE_TRUST_ROOT_SPKI_B64: "",
    APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64: "",
  })).rejects.toThrow("update trust root and bootstrap policy are required");
});
```

### Step 2: Run focused tests and confirm RED

Run: `npm test -- --run scripts/build-go.test.ts`

Expected: no trust build contract exists.

### Step 3: Add link-time public trust material

```go
var updateTrustRootSPKIBase64 string
var updateTrustBootstrapPolicyBase64 string

func embeddedUpdateTrust() (*ecdsa.PublicKey, []byte, error) {
	spkiDER, err := base64.StdEncoding.DecodeString(updateTrustRootSPKIBase64)
	if err != nil { return nil, nil, fmt.Errorf("decode update trust root: %w", err) }
	parsed, err := x509.ParsePKIXPublicKey(spkiDER)
	if err != nil { return nil, nil, fmt.Errorf("parse update trust root: %w", err) }
	root, ok := parsed.(*ecdsa.PublicKey)
	if !ok || root.Curve != elliptic.P256() { return nil, nil, errors.New("update trust root must be ECDSA P-256") }
	policy, err := base64.StdEncoding.DecodeString(updateTrustBootstrapPolicyBase64)
	return root, policy, err
}
```

`build-go.mjs` passes the values only through `-ldflags -X`; it must validate Base64 locally and print only the SHA-256 of decoded public inputs, never the raw values. `docs/security/update-trust-root.md` records SPKI SHA-256 review procedure and the reviewed non-secret digest, not credentials.

### Step 4: Test Go and Node build seams

Run: `go test ./... -run TestEmbeddedUpdateTrust`

Working directory: `goserver`

Run: `npm test -- --run scripts/build-go.test.ts`

Working directory: repository root

### Step 5: Commit

Commit: `git add goserver/update_trust_embed* scripts/build-go.mjs scripts/build-go.test.ts docs/security/update-trust-root.md && git commit -m "build: embed update rotation trust"`

## Task 4: Implement anti-rollback policy loading and atomic cache persistence

**Files:**

- Create: `goserver/update_trust_store.go`
- Create: `goserver/update_trust_store_test.go`
- Modify: `goserver/auto_update.go`
- Modify: `goserver/auto_update_test.go`

### Step 1: Write failing source-selection and interrupted-write tests

Cover embedded epoch 1, domestic newer/GitHub stale, GitHub newer/domestic stale, one invalid source, both offline with valid cache, both offline with expired cache, lower-epoch rollback, corrupt cache, and a failure between temp-file sync and rename.

```go
func TestTrustStoreChoosesHighestValidEpoch(t *testing.T) {
	store := newTestTrustStore(t, embeddedEpoch(1), cachedEpoch(2))
	got, err := store.Resolve(context.Background(), sourceEpoch(3), sourceEpoch(2))
	if err != nil { t.Fatal(err) }
	if got.Epoch != 3 { t.Fatalf("epoch = %d, want 3", got.Epoch) }
	if readHighestEpoch(t, store) != 3 { t.Fatal("highest epoch was not persisted") }
}
```

### Step 2: Run focused tests and confirm RED

Run: `go test ./... -run 'TestTrustStore|TestAtomicTrustCache'`

Working directory: `goserver`

### Step 3: Implement independent source verification and atomic persistence

```go
type updateTrustSource struct {
	Name string
	URL  string
}

type updateTrustStore struct {
	Root           *ecdsa.PublicKey
	EmbeddedPolicy []byte
	CacheDir       string
	Client         *http.Client
	Now            func() time.Time
	Rename         func(string, string) error
}

func (s *updateTrustStore) Resolve(ctx context.Context, sources ...updateTrustSource) (verifiedUpdateTrustPolicy, error)
```

Each source is downloaded, size-limited, and verified independently. Select the highest valid epoch from embedded, cached, and fetched policies. Persist `{policy bytes, SHA-256, highest epoch}` through create-exclusive temp file → write → file sync → close → atomic rename → directory sync. Never replace a higher cached epoch with a lower one.

If the selected policy is expired, permit only the legal identities already recorded by the highest previously accepted policy and reject any newly introduced identity. Represent this explicitly in the returned policy rather than silently treating an expired policy as current.

### Step 4: Integrate source configuration without enabling it by default

Add `TrustSources`, `TrustStore`, and a pinned clock to `autoUpdaterOptions`; existing non-enrollment test builds remain unchanged until `APP_UPDATE_TRUST_REQUIRED=1` is used.

### Step 5: Run tests and commit

Run: `go test ./... -run 'TestTrustStore|TestAtomicTrustCache'`

Run: `go test ./...`

Working directory: `goserver`

Commit: `git add goserver/update_trust_store* goserver/auto_update.go goserver/auto_update_test.go && git commit -m "feat: cache update trust policy safely"`

## Task 5: Bind updater candidates to channel, policy, hash, and signer identity

**Files:**

- Modify: `goserver/auto_update.go`
- Modify: `goserver/auto_update_test.go`
- Modify: `goserver/auto_update_signature_windows.go`

### Step 1: Write failing end-to-end updater tests

Add candidate tests for:

- domestic v0.4.7 response declares `legacy-rushrush` and exact v0.4.11 RushRush passes;
- v0.4.11 response declares `stable` and v0.4.12 NaisNet passes;
- GitHub fallback is stable only;
- RushRush on stable, RushRush v0.4.12, NaisNet with wrong organization ID, policy hash mismatch, missing channel header, and invalid policy all fail;
- download fallback continues only to a source whose channel and policy authorize the same target version.

```go
func TestUpdaterRejectsSignerAuthorizedForDifferentChannel(t *testing.T) {
	u := newPolicyUpdater(t, updateChannelStable, rushRushBridgeCertificate)
	err := u.checkAndDownload(context.Background())
	assertUpdateCode(t, err, "publisher_not_authorized")
}
```

### Step 2: Run focused tests and confirm RED

Run: `go test ./... -run 'TestUpdater.*(Channel|Policy|Signer|Bridge)'`

Working directory: `goserver`

### Step 3: Carry channel metadata through release resolution

```go
type updateReleaseSource struct {
	Name           string
	URL            string
	GitHub         bool
	DefaultChannel updateChannel
}

type updateReleaseCandidate struct {
	Source  updateReleaseSource
	Release githubRelease
	Version string
	Channel updateChannel
}
```

For the domestic API, require exactly one `X-Gift-Panel-Update-Channel` response header with value `stable` or `legacy-rushrush`. For GitHub, force `stable`; it must never be used for legacy discovery. Continue sending the existing `User-Agent: bilibili-live-gift-panel/<currentVersion>` on release, checksum, and asset requests.

### Step 4: Replace exact Subject verification with the full trust decision

After size and SHA-256 checks, inspect Authenticode and call:

```go
func verifyUpdateArtifact(path string, candidate updateReleaseCandidate, sha256Hex string, policy verifiedUpdateTrustPolicy) error {
	cert, err := inspectAuthenticode(path)
	if err != nil { return err }
	return policy.Authorize(updateArtifactIdentity{
		Tag: candidate.Release.TagName, Channel: candidate.Channel,
		SHA256: sha256Hex, Certificate: cert.LegalIdentity,
	})
}
```

Keep the old exact-Subject verifier only for building pre-enrollment historical versions; enrollment builds must not call it. Add privacy-safe diagnostic result codes and never log the raw certificate, URL query, or local path.

### Step 5: Run tests and commit

Run: `go test ./... -run 'TestUpdater.*(Channel|Policy|Signer|Bridge)'`

Run: `go test ./...`

Working directory: `goserver`

Commit: `git add goserver/auto_update.go goserver/auto_update_test.go goserver/auto_update_signature_windows.go && git commit -m "feat: authorize update artifacts by signed policy"`

## Task 6: Add strict version-aware API routing and policy distribution

**Files:**

- Create: `updateapi/internal/service/channel_router.go`
- Create: `updateapi/internal/service/channel_router_test.go`
- Modify: `updateapi/internal/service/service.go`
- Modify: `updateapi/internal/service/service_test.go`
- Modify: `updateapi/internal/httpapi/handler.go`
- Modify: `updateapi/internal/httpapi/handler_test.go`
- Modify: `updateapi/cmd/server/main.go`

### Step 1: Write failing route matrix tests

Test exact User-Agent values and rejection of missing, duplicate, malformed, oversized, prerelease, development, and unknown versions. Include the legacy-disabled state.

```go
func TestChannelRouter(t *testing.T) {
	tests := []struct{ ua string; legacy bool; want Channel; code int }{
		{"bilibili-live-gift-panel/0.4.7", true, ChannelLegacyRushRush, 200},
		{"bilibili-live-gift-panel/0.4.7", false, "", 503},
		{"bilibili-live-gift-panel/0.4.9", false, ChannelStable, 200},
		{"bilibili-live-gift-panel/0.4.10", false, ChannelStable, 200},
		{"bilibili-live-gift-panel/0.4.11", false, ChannelStable, 200},
		{"bilibili-live-gift-panel/0.4.12", false, ChannelStable, 200},
		{"", false, "", 400},
		{"bilibili-live-gift-panel/dev", false, "", 400},
		{"bilibili-live-gift-panel/0.4.8", false, "", 400},
	}
}
```

The stable allowlist for the migration is exact: `0.4.9`, `0.4.10`, `0.4.11`, and `0.4.12`. Later versions require a code-reviewed allowlist/range policy and tests; do not implement a blanket `>=0.4.12` parser in this task.

### Step 2: Run focused tests and confirm RED

Run: `go test ./... -run 'TestChannelRouter|TestLatestRoutesByUserAgent|TestPublisherPolicyEndpoint'`

Working directory: `updateapi`

### Step 3: Implement explicit route selection

```go
type Channel string

const (
	ChannelStable         Channel = "stable"
	ChannelLegacyRushRush Channel = "legacy-rushrush"
)

type ChannelRouter struct { LegacyActive func(context.Context) (bool, error) }

func (r ChannelRouter) Select(ctx context.Context, values []string) (Channel, error)
```

Reject anything except one canonical value matching `^bilibili-live-gift-panel/(0\.4\.(7|9|10|11|12))$`. Route `0.4.7` to legacy only when the legacy pointer exists and activation is enabled. Route the other reviewed versions to stable.

### Step 4: Make the release service channel-aware and expose the policy

Remove the stable-only guard from `service.manifest()` and replace it with a typed channel-key map:

```go
var channelKeys = map[Channel]string{
	ChannelStable: "channels/stable/latest.json",
	ChannelLegacyRushRush: "channels/legacy-rushrush/latest.json",
}

func (s *Service) Latest(ctx context.Context, channel Channel) (release.PublicRelease, error)
func (s *Service) PublisherPolicy(ctx context.Context) ([]byte, error)
```

`GET /api/v1/releases/latest` selects the channel, emits `X-Gift-Panel-Update-Channel`, `Vary: User-Agent`, and `Cache-Control: private, no-store`. `GET /api/v1/trust/publisher-policy` reads `trust/publisher/latest.json`, returns the complete signed envelope, and also uses `private, no-store`. Keep changelog on stable and document that it is not a discovery endpoint.

### Step 5: Add bounded aggregate metrics

Record only canonical version bucket, channel, and bounded result code. Test that request IP, arbitrary User-Agent, query, and headers are not passed to the metrics seam.

### Step 6: Run tests and commit

Run: `go test ./... -run 'TestChannelRouter|TestLatestRoutesByUserAgent|TestPublisherPolicyEndpoint'`

Run: `go test ./...`

Working directory: `updateapi`

Commit: `git add updateapi/internal/service updateapi/internal/httpapi updateapi/cmd/server && git commit -m "feat: route update channels by client version"`

## Task 7: Separate stable and legacy publication/mirroring

**Files:**

- Modify: `updateapi/internal/release/manifest.go`
- Modify: `updateapi/internal/release/manifest_test.go`
- Modify: `updateapi/internal/publish/publish.go`
- Modify: `updateapi/internal/publish/publish_test.go`
- Modify: `updateapi/internal/mirror/runner.go`
- Modify: `updateapi/internal/mirror/runner_test.go`
- Modify: `updateapi/internal/mirror/github.go`
- Modify: `updateapi/internal/mirror/github_test.go`
- Modify: `updateapi/cmd/publish/main.go`
- Modify: `updateapi/cmd/mirror/main.go`

### Step 1: Write failing channel-isolation tests

Prove stable publishing cannot write the legacy pointer, bridge publishing cannot write stable, legacy accepts only exact `v0.4.11`, bridge mirroring fetches by tag rather than GitHub latest, and state/ETags are isolated per channel.

```go
func TestBridgePublishCannotMutateStable(t *testing.T) {
	store := newRecordingStore()
	_, err := New(store).Publish(ctx, Input{Channel: release.ChannelLegacyRushRush, Tag: "v0.4.11", Prepared: bridgeFixture()})
	if err != nil { t.Fatal(err) }
	if store.Wrote("channels/stable/latest.json") { t.Fatal("bridge wrote stable pointer") }
	if !store.Wrote("channels/legacy-rushrush/latest.json") { t.Fatal("legacy pointer not written") }
}
```

### Step 2: Run focused tests and confirm RED

Run: `go test ./... -run 'Test.*(Channel|Bridge|Legacy|ByTag)'`

Working directory: `updateapi`

### Step 3: Version and validate channel manifests

Add `channel` to internal manifests and support reading existing schema 1 as stable. Write only schema 2 after this task.

```go
type ChannelManifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	Channel       Channel `json:"channel"`
	Tag           string  `json:"tag"`
	// existing fields remain unchanged
}

func (m ChannelManifest) ValidateForChannel(channel Channel) error
```

For legacy, reject every tag except `v0.4.11`. Reuse immutable `releases/<tag>/...` objects; never overwrite a key. Pointer keys come only from a closed channel map.

### Step 4: Add tag-specific GitHub retrieval and CLI flags

```go
type ReleaseSource interface {
	Latest(context.Context, string) (SourceRelease, string, bool, error)
	ByTag(context.Context, string, string) (SourceRelease, string, bool, error)
}

type RunOptions struct {
	DryRun  bool
	Channel release.Channel
	Tag     string
}
```

`cmd/mirror` accepts `--channel stable|legacy-rushrush`; `--tag v0.4.11` is mandatory for legacy and forbidden for stable. State directory is `<base>/stable` or `<base>/legacy-rushrush`. `cmd/publish` has the same closed channel flag and exact tag validation.

### Step 5: Run tests and commit

Run: `go test ./... -run 'Test.*(Channel|Bridge|Legacy|ByTag)'`

Run: `go test ./...`

Working directory: `updateapi`

Commit: `git add updateapi/internal/release updateapi/internal/publish updateapi/internal/mirror updateapi/cmd/publish updateapi/cmd/mirror && git commit -m "feat: isolate stable and bridge update channels"`

## Task 8: Build and locally verify the KMS publisher-policy signer

**Files:**

- Modify: `updateapi/go.mod`
- Modify: `updateapi/go.sum`
- Create: `updateapi/internal/trustpolicy/policy.go`
- Create: `updateapi/internal/trustpolicy/policy_test.go`
- Create: `updateapi/internal/trustpolicy/kms.go`
- Create: `updateapi/internal/trustpolicy/kms_test.go`
- Create: `updateapi/cmd/trustpolicy/main.go`
- Create: `updateapi/testdata/trustpolicy/epoch-1-candidate.json`

### Step 1: Write failing candidate validation and fake-KMS tests

The candidate is only the `signed` object. Test exact epoch progression, deterministic canonical bytes, RushRush bridge restrictions, NaisNet stable identity, KMS digest input, returned DER signature verification, expected SPKI digest, and output redaction.

```go
type Signer interface {
	PublicKey(context.Context, string) ([]byte, string, error)
	SignDigest(context.Context, string, []byte) ([]byte, string, error)
}

func TestSignPolicyUsesSHA256DigestAndVerifiesLocally(t *testing.T) {
	fake := newFakeKMSSigner(t)
	out, audit, err := Sign(ctx, fake, candidateEpoch1, SignOptions{KeyID: "kms-key-id", ExpectedPreviousEpoch: 0})
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(fake.Digest, sha256Bytes(out.CanonicalSigned)) { t.Fatal("wrong KMS digest") }
	if audit.RequestID == "" { t.Fatal("missing non-secret KMS request id") }
}
```

### Step 2: Run focused tests and confirm RED

Run: `go test ./... -run 'Test(SignPolicy|PolicyCandidate|KMSSigner)'`

Working directory: `updateapi`

### Step 3: Add the pinned Tencent KMS SDK and adapter

Run: `go get github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/kms@v1.3.168`

Use `kms/v20190118`. `GetPublicKey` must compare the returned SPKI SHA-256 with the reviewed command input. `SignByAsymmetricKey` must use `Algorithm=ECC_P256_R1`, `MessageType=DIGEST`, and Base64-encoded SHA-256 digest.

```go
request := kms.NewSignByAsymmetricKeyRequest()
request.KeyId = common.StringPtr(keyID)
request.Algorithm = common.StringPtr("ECC_P256_R1")
request.MessageType = common.StringPtr("DIGEST")
request.Message = common.StringPtr(base64.StdEncoding.EncodeToString(digest))
response, err := client.SignByAsymmetricKeyWithContext(ctx, request)
```

Verify the returned ASN.1 DER signature locally with the fetched reviewed public key before writing output.

### Step 4: Implement a no-secret CLI contract

```text
trustpolicy sign \
  --candidate testdata/trustpolicy/epoch-1-candidate.json \
  --expected-previous-epoch 0 \
  --kms-region ap-shanghai \
  --kms-key-id-env GIFT_PANEL_KMS_KEY_ID \
  --expected-spki-sha256-env GIFT_PANEL_KMS_SPKI_SHA256 \
  --output gift-panel-publisher-policy.json \
  --audit-output gift-panel-publisher-policy.audit.json
```

Credentials use the Tencent SDK's normal short-lived credential chain; no secret values are accepted as flags. Audit output contains only key ID, epoch, policy SHA-256, KMS request ID, UTC time, and CI actor.

### Step 5: Run tests and commit

Run: `go test ./... -run 'Test(SignPolicy|PolicyCandidate|KMSSigner)'`

Run: `go test ./...`

Working directory: `updateapi`

Commit: `git add updateapi/go.mod updateapi/go.sum updateapi/internal/trustpolicy updateapi/cmd/trustpolicy updateapi/testdata/trustpolicy && git commit -m "feat: sign publisher policy with Tencent KMS"`

## Task 9: Add protected policy-rotation and policy-publication workflows

**Files:**

- Create: `.github/workflows/publisher-rotation.yml`
- Create: `scripts/publish-trust-policy.mjs`
- Create: `scripts/publish-trust-policy.test.ts`
- Modify: `scripts/release-workflow.test.ts`
- Create: `docs/runbooks/publisher-rotation.md`

### Step 1: Write failing workflow contract tests

Parse the workflow and assert:

- only `workflow_dispatch` can start it;
- environment is exactly `publisher-rotation`;
- permissions are read-only except the minimum needed to create the dedicated policy Release;
- candidate epoch and expected previous epoch are explicit inputs;
- it invokes only `updateapi/cmd/trustpolicy`, never EVSign or EXE signing;
- immutable epoch publication happens before mutable pointer advancement;
- a failed immutable upload cannot advance either pointer.

```ts
it("keeps publisher rotation separate from executable signing", () => {
  const text = readWorkflow("publisher-rotation.yml");
  expect(text).toContain("environment: publisher-rotation");
  expect(text).not.toContain("sign-evsign.mjs");
  expect(text).not.toContain("build-go.mjs");
});
```

### Step 2: Run focused tests and confirm RED

Run: `npm test -- --run scripts/publish-trust-policy.test.ts scripts/release-workflow.test.ts`

### Step 3: Implement the protected workflow and dry-run publisher

The publisher validates an already KMS-signed envelope, uploads `trust/publisher/epochs/%08d.json` with create-only semantics, creates `publisher-policy-epoch-%08d` with an immutable asset, re-reads both hashes, then conditionally advances discovery pointers. Add `--dry-run` that performs all validation and prints only target keys/hashes.

The workflow must expose separate jobs:

1. `validate-candidate` without cloud credentials;
2. `sign-policy` in protected environment with short-lived KMS authorization;
3. `publish-immutable`;
4. `advance-discovery` requiring an explicit workflow input `advance_discovery=true`.

### Step 4: Document the external approval stop

`docs/runbooks/publisher-rotation.md` must state that repository code completion does not authorize:

1. creating/enabling the KMS key or CAM identity;
2. signing epoch 1;
3. uploading immutable policy objects;
4. advancing discovery pointers.

Each item is a separate confirmation. Include preflight commands that print only region, key state, key usage, algorithm, deletion protection, public SPKI SHA-256, CAM action names, and CloudAudit availability.

### Step 5: Run tests and commit

Run: `npm test -- --run scripts/publish-trust-policy.test.ts scripts/release-workflow.test.ts`

Run: `npm test`

Commit: `git add .github/workflows/publisher-rotation.yml scripts/publish-trust-policy* scripts/release-workflow.test.ts docs/runbooks/publisher-rotation.md && git commit -m "ci: protect publisher policy rotation"`

## Task 10: Add a dedicated exact-version RushRush bridge workflow

**Files:**

- Create: `.github/workflows/bridge-release.yml`
- Modify: `scripts/release-workflow.test.ts`
- Modify: `scripts/sign-evsign.mjs`
- Modify: `scripts/sign-evsign.test.ts`
- Create: `docs/runbooks/bridge-release.md`

### Step 1: Write failing bridge workflow isolation tests

Assert exact tag `v0.4.11`, `latest=false`, RushRush outer signer profile, NaisNet embedded FFmpeg verification, embedded trust root/bootstrap policy requirements, complete checksums/changelog, and the absence of stable-pointer/KMS permissions.

```ts
it("publishes only the exact non-latest bridge", () => {
  const workflow = readWorkflow("bridge-release.yml");
  expect(workflow).toContain('BRIDGE_TAG: "v0.4.11"');
  expect(workflow).toContain("--latest=false");
  expect(workflow).not.toContain("channels/stable/latest.json");
  expect(workflow).not.toContain("SignByAsymmetricKey");
});
```

### Step 2: Run focused tests and confirm RED

Run: `npm test -- --run scripts/release-workflow.test.ts scripts/sign-evsign.test.ts`

### Step 3: Implement explicit signer roles

Refactor `sign-evsign.mjs` to accept closed profiles rather than a free-form expected subject:

```js
const profiles = {
  stable: { certificateEnv: "EVSIGN_CERTIFICATE", expectedIdentityEnv: "EVSIGN_PUBLISHER_IDENTITY" },
  bridge: { certificateEnv: "EVSIGN_BRIDGE_CERTIFICATE", expectedIdentityEnv: "EVSIGN_BRIDGE_PUBLISHER_IDENTITY" },
};
```

The bridge job must fail unless the final EXE is Windows-valid and its structured identity is exactly RushRush. It must independently verify the bundled FFmpeg as NaisNet, the embedded root SPKI digest, and the bootstrap policy epoch/hash. It may create immutable bridge assets and a GitHub Release only; no stable or legacy pointer mutation occurs in this workflow.

### Step 4: Add an approval-gated bridge runbook

Document two separate later actions: publish the v0.4.11 GitHub Release, then—after packaged acceptance—mirror/advance the legacy pointer. Explicitly prohibit running either before v0.4.12 has completed its seven-day stable observation.

### Step 5: Run tests and commit

Run: `npm test -- --run scripts/release-workflow.test.ts scripts/sign-evsign.test.ts`

Run: `npm test`

Commit: `git add .github/workflows/bridge-release.yml scripts/release-workflow.test.ts scripts/sign-evsign* docs/runbooks/bridge-release.md && git commit -m "ci: add isolated RushRush bridge release"`

## Task 11: Enforce enrollment trust in the ordinary stable Release workflow

**Files:**

- Modify: `.github/workflows/release.yml`
- Modify: `scripts/release-workflow.test.ts`
- Create: `scripts/verify-enrollment-build.mjs`
- Create: `scripts/verify-enrollment-build.test.ts`
- Modify: `docs/release.md`

### Step 1: Write failing stable workflow contract tests

Require `APP_UPDATE_TRUST_REQUIRED=1`, reviewed root SPKI digest, bootstrap policy epoch/hash, structured final NaisNet identity verification, embedded FFmpeg verification, and no KMS/legacy permissions. Assert GitHub latest remains true for stable releases.

```ts
it("requires enrollment trust for stable release builds", () => {
  const workflow = readWorkflow("release.yml");
  expect(workflow).toContain("APP_UPDATE_TRUST_REQUIRED: 1");
  expect(workflow).toContain("verify-enrollment-build.mjs");
  expect(workflow).not.toContain("EVSIGN_BRIDGE_CERTIFICATE");
  expect(workflow).not.toContain("SignByAsymmetricKey");
});
```

### Step 2: Run focused tests and confirm RED

Run: `npm test -- --run scripts/release-workflow.test.ts scripts/verify-enrollment-build.test.ts`

### Step 3: Implement artifact inspection

`verify-enrollment-build.mjs` invokes the built EXE's read-only build-info mode or a companion Go inspector to verify:

- application version equals the tag;
- embedded root SPKI SHA-256 equals the reviewed value;
- embedded bootstrap policy signature is valid and epoch is 1 or higher;
- policy authorizes the final NaisNet legal identity for stable;
- final EXE and standalone/bundled FFmpeg hashes match sidecars;
- Authenticode status and structured identities are correct.

It outputs a JSON evidence file containing only public hashes, version, tag, identities, policy epoch, root key ID, and signature status.

### Step 4: Update stable release documentation

Document that v0.4.12 is the first enrollment stable release and that its tag/Release/COS publication still requires action-time confirmation. Do not change the application version or changelog during this task; those belong to the separately approved release execution.

### Step 5: Run tests and commit

Run: `npm test -- --run scripts/release-workflow.test.ts scripts/verify-enrollment-build.test.ts`

Run: `npm test`

Commit: `git add .github/workflows/release.yml scripts/release-workflow.test.ts scripts/verify-enrollment-build* docs/release.md && git commit -m "ci: verify enrollment trust in stable releases"`

## Task 12: Add deployment configuration with legacy inactive by default

**Files:**

- Modify: `deploy/update-api.env.example`
- Modify: `deploy/update-api.service`
- Modify: `deploy/gift-panel-release-mirror.service`
- Create: `deploy/gift-panel-legacy-release-mirror.service`
- Modify: `deploy/README.md`
- Create: `updateapi/internal/config/config.go`
- Create: `updateapi/internal/config/config_test.go`

### Step 1: Write failing configuration safety tests

Test that startup fails on unknown channel keys, legacy activation defaults false, stable and legacy use distinct state directories/credentials, and the API cannot infer activation merely because a legacy object exists.

```go
func TestLegacyRoutingDefaultsInactive(t *testing.T) {
	cfg, err := config.FromEnv(map[string]string{})
	if err != nil { t.Fatal(err) }
	if cfg.LegacyRoutingActive { t.Fatal("legacy routing must default inactive") }
}
```

### Step 2: Run focused tests and confirm RED

Run: `go test ./... -run 'TestLegacyRouting|TestChannelConfiguration'`

Working directory: `updateapi`

### Step 3: Implement closed deployment configuration

Use exact environment names:

```text
UPDATE_STABLE_CHANNEL_KEY=channels/stable/latest.json
UPDATE_LEGACY_CHANNEL_KEY=channels/legacy-rushrush/latest.json
UPDATE_LEGACY_ROUTING_ACTIVE=false
UPDATE_PUBLISHER_POLICY_KEY=trust/publisher/latest.json
```

Legacy mirror service runs `mirror --channel legacy-rushrush --tag v0.4.11` with its own state directory and credential file. Stable mirror continues `mirror --channel stable`. Neither service receives KMS permissions.

### Step 4: Document dry-run, deployment, and rollback checks

The runbook must require:

- API route matrix dry-run against captured public v0.4.7/v0.4.9/v0.4.10 User-Agent strings;
- missing/malformed/duplicate/unknown requests fail closed;
- stable hash/pointer unchanged while legacy is inactive;
- policy endpoint returns a locally verified signed envelope;
- deployment confirmation before service restart;
- rollback restores the prior binary/config with legacy inactive and never mutates stable as part of bridge rollback.

### Step 5: Run tests and commit

Run: `go test ./... -run 'TestLegacyRouting|TestChannelConfiguration'`

Run: `go test ./...`

Working directory: `updateapi`

Commit: `git add deploy updateapi/internal/config && git commit -m "deploy: stage version-aware update routing"`

## Task 13: Run repository regression and packaged Windows security acceptance

**Files:**

- Create: `docs/verification/signer-trust-rotation.md`
- Modify only if a product defect is found: the task-specific source/test files above

### Step 1: Run all local regression suites

Run: `go test ./...`

Working directory: `goserver`

Run: `go test ./...`

Working directory: `updateapi`

Run: `npm test`

Working directory: repository root

Run: `npm run build`

Working directory: repository root

Expected: all commands exit 0. Record command, UTC time, commit, exit code, and concise output summary in the verification document.

### Step 2: Build unsigned test packages and inspect embedded public trust

Build the v0.4.12-shaped enrollment artifact with test-only keys and certificates. Verify version, root SPKI digest, bootstrap policy, cache behavior, update diagnostics, and FFmpeg packaging. These artifacts stay local and are not committed or uploaded.

### Step 3: Execute the Windows negative matrix

Using test certificates/policies, verify:

- same NaisNet legal identity with a different leaf passes;
- different organization ID fails despite Authenticode `Valid`;
- RushRush passes only for `v0.4.11` on legacy;
- wrong tag/channel/hash, expired policy, malformed JSON, invalid KMS signature, rollback epoch, and interrupted cache write fail with bounded result codes;
- no diagnostic contains a credential, signed query, username path, Bilibili identity, or gift content.

### Step 4: Execute API and mirror integration acceptance locally

Replay exact public version headers:

```text
bilibili-live-gift-panel/0.4.7  -> legacy when active, otherwise controlled unavailable
bilibili-live-gift-panel/0.4.9  -> stable
bilibili-live-gift-panel/0.4.10 -> stable
bilibili-live-gift-panel/0.4.11 -> stable
bilibili-live-gift-panel/0.4.12 -> stable
```

Verify missing, duplicate, malformed, prerelease, development, oversized, and unrecognized values return HTTP 400; verify `Vary: User-Agent`, `private, no-store`, channel header, and independent state/pointer writes.

### Step 5: Request code review and fix only evidenced defects

Use `superpowers:requesting-code-review` against the design and this plan. Any fix starts with a new failing regression test and a separate commit. Re-run the full suite after fixes.

### Step 6: Commit verification evidence

Commit only public, privacy-safe evidence:

`git add docs/verification/signer-trust-rotation.md && git commit -m "test: record signer trust rotation verification"`

## Task 14: Execute the approval-gated production rollout

**Files:**

- Update after each approved stage: `docs/verification/signer-trust-rotation.md`
- No other repository file should change unless a defect is found through the same RED/GREEN process

This task is intentionally a sequence of stops. Never infer authorization for a later stop from an earlier confirmation.

### Gate 1: Provision and independently review the KMS root

Preflight reports exact region, key usage, algorithm, deletion protection, CAM actions, and CloudAudit status. Ask for confirmation, then provision. Export only SPKI, calculate SHA-256 on two independent machines/processes, and require matching review before proceeding.

### Gate 2: Sign and publish epoch 1 policy

First run the policy workflow in validation/dry-run mode. Ask separately to sign epoch 1. Verify locally. Ask separately to publish immutable COS/GitHub epoch objects. Re-read and compare hashes. Ask separately to advance discovery pointers.

### Gate 3: Deploy strict routing with legacy inactive

Show route-matrix, current stable pointer/hash, candidate binary/config diff, restart/rollback commands, and health checks. Ask for deployment confirmation. After deployment verify v0.4.7/v0.4.9/v0.4.10 decisions, policy endpoint, privacy-safe aggregate metrics, and unchanged stable behavior. Keep `UPDATE_LEGACY_ROUTING_ACTIVE=false`.

### Gate 4: Publish NaisNet v0.4.12 stable

Prepare version/changelog/tag and fully signed artifacts. Record Authenticode identity, asset/sidecar hashes, policy epoch/root key ID, GitHub latest flag, and proposed stable manifest. Ask for confirmation before push/tag/Release/COS promotion. Then verify real v0.4.9 and v0.4.10 upgrades through domestic and GitHub sources.

Observe for at least seven days. Record daily bounded counts for policy and updater result codes. Do not publish or activate the bridge during this window.

### Gate 5: Publish RushRush v0.4.11 bridge

Only after Gate 4's observation passes, prepare the exact bridge. Record RushRush outer identity, NaisNet embedded FFmpeg identity, hashes, policy/root evidence, `latest=false`, and proof that stable is untouched. Ask for confirmation before push/tag/Release. Do not advance legacy yet.

### Gate 6: Activate the legacy pointer

Using the public v0.4.7 package, first prove in a controlled Windows acceptance environment:

1. v0.4.7 downloads and accepts v0.4.11 from the domestic API;
2. v0.4.11 restarts with existing User-Agent version `0.4.11`;
3. v0.4.11 routes to stable and accepts NaisNet v0.4.12;
4. v0.4.9 and v0.4.10 never receive v0.4.11.

Show the exact legacy pointer diff and rollback target, then ask for a separate confirmation to advance `channels/legacy-rushrush/latest.json` and enable legacy routing.

### Gate 7: Observe and close convergence

Observe at least seven days after activation. Verify aggregate convergence/result codes and retain bridge objects for the support window. Do not retire any route, Release, key, certificate, COS object, or credential; retirement requires a new design and a separate confirmation.

### Final verification

Before claiming production completion, use `superpowers:verification-before-completion` and attach current evidence for every gate. The implementation is not complete if any required Windows path, source fallback, pointer, signature, hash, policy epoch, or observation window lacks direct evidence.

