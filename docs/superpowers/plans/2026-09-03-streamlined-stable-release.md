# Streamlined Stable Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a smaller v0.4.13 transition release and make later same-legal-publisher stable releases a one-trigger workflow, while preserving event-driven approval for an unforeseen publisher change.

**Architecture:** Keep the deployed policy-v1 wire format and embedded P-256 rotation root. Epoch 3 grants the exact NaisNet structured identity a finite stable tag window without per-artifact hashes; v0.4.12 remains exact-hash evidence, while post-enrollment releases bind bytes through Authenticode, manifest digests, Actions artifact digests, attestations, and Release readback. The release workflow carries public candidate metadata through job outputs and uses a numeric-ID, resumable draft transaction instead of manually copied environment variables.

**Tech Stack:** Go 1.26, PowerShell Authenticode, Node.js 22, TypeScript/Vitest, GitHub Actions, GitHub REST Releases API, Tencent COS/update API.

**Spec:** [`docs/superpowers/specs/2026-09-03-streamlined-stable-release-design.md`](../specs/2026-09-03-streamlined-stable-release-design.md)

## Global Constraints

- Start from the isolated `codex/update-signer-trust-rotation-design` worktree after commits `5ff2294` and `623fea6`; do not recreate or squash those commits.
- Preserve every unrelated ignored/untracked cache and local verification directory. Stage only the paths named by each task.
- Every behavior change starts with a focused failing test, records the expected RED, then reaches GREEN before the task commit.
- The published v0.4.12 tag, Release, assets, epoch-2 policy, GitHub/COS immutable objects, and current stable pointer are immutable.
- Policy epoch 3 uses the existing v1 JSON schema, exact NaisNet structured identity, exact `stable` channel, sorted tags `v0.4.13` through `v0.4.32`, and no `manifestSha256`.
- v0.4.12 enrollment verification remains exact-hash-only. A hashless authorization is accepted only for canonical stable versions strictly after v0.4.12.
- RushRush remains authorized only for `v0.4.11` on `legacy-rushrush`; legacy routing stays inactive.
- `X-Cert` prevents accidental EVSign certificate selection but never grants client trust. The actual signed leaf identity is inspected after every signing attempt.
- An unknown legal identity produces a bounded request and stops before any draft, Release, tag mutation, policy signing, or pointer update.
- Ordinary release jobs never receive `PUBLISHER_ROTATION_PRIVATE_KEY_PEM` or Tencent policy-publication credentials.
- Windows x64 CI is authoritative for Authenticode, PE, EXE size, and packaged-runtime acceptance. Linux/macOS results are compile or source-level evidence only.
- Public push, tag creation, epoch signing/publication, discovery advancement, server deployment, Release publication, and legacy activation remain separate action-time approvals.

## Existing Baseline

- `5ff2294 fix: exclude release FFmpeg staging from UI` already excludes `ffmpeg-component` and `release-ffmpeg-sealed` and adds the real path regression fixture.
- The focused asset test is GREEN, release-related tests are GREEN, TypeScript is GREEN, and a clean real build with both staging roots populated produced 14,578,176 bytes, 87 UI assets / 836,864 bytes, and zero forbidden paths.
- `623fea6 docs: design streamlined stable releases` records the design that this plan implements.

---

### Task 1: Distinguish enrollment hash authorization from post-enrollment identity authorization

**Files:**
- Modify: `goserver/internal/artifactinspect/enrollment.go`
- Modify: `goserver/internal/artifactinspect/enrollment_test.go`
- Modify: `goserver/cmd/artifact-inspector/main.go`
- Modify: `goserver/cmd/artifact-inspector/main_test.go`
- Modify: `scripts/verify-enrollment-build.mjs`
- Modify: `scripts/verify-enrollment-build.d.mts`
- Modify: `scripts/verify-enrollment-build.test.ts`

**Interfaces:**
- Produces: `AuthorizationScope`, `AuthorizationScopeArtifactSHA256`, `AuthorizationScopePublisherIdentity`.
- Produces: `authorizationScopeForStableVersion(version string) (AuthorizationScope, error)`.
- Extends `EnrollmentPolicyEvidence` and `EnrollmentEvidence` with `authorizationScope`.
- Preserves `authorizedArtifactSha256`; it is the exact policy-bound hash in artifact mode and the empty string in publisher-identity mode.

- [ ] **Step 1: Write failing Go tests for the version boundary**

Add a v0.4.13 fixture whose signed authorization policy has the exact NaisNet identity, `allowedTags:["v0.4.13"]`, and no `manifestSha256`. Assert:

```go
func TestVerifyEnrollmentArtifactAcceptsPostEnrollmentPublisherIdentityScope(t *testing.T) {
    fixture := enrollmentArtifactFixtureForVersion(t, "0.4.13", false)
    evidence, err := VerifyEnrollmentArtifact(fixture.options)
    if err != nil { t.Fatal(err) }
    if evidence.AuthorizationScope != AuthorizationScopePublisherIdentity || evidence.AuthorizedArtifactSHA256 != "" {
        t.Fatalf("authorization evidence = %#v", evidence)
    }
}
```

Retain and rename the v0.4.12 test so it explicitly asserts `AuthorizationScopeArtifactSHA256`. Add negative cases proving v0.4.12 rejects a hashless rule, v0.4.13 rejects a different organization ID, and an exact-hash v0.4.13 rule still rejects changed bytes.

- [ ] **Step 2: Run the Go tests and confirm RED**

Run:

```powershell
$env:GOCACHE='E:\bilibili\.worktrees\update-signer-trust-rotation-design\.streamlined-gocache'
go -C goserver test ./internal/artifactinspect ./cmd/artifact-inspector -run 'TestVerifyEnrollment|TestArtifactInspectorVerifyEnrollment' -count=1
```

Expected: the v0.4.13 hashless fixture fails with `final artifact hash authorization is invalid` or the new scope field is undefined.

- [ ] **Step 3: Implement the closed authorization scope**

Add:

```go
type AuthorizationScope string

const (
    AuthorizationScopeArtifactSHA256   AuthorizationScope = "artifact-sha256"
    AuthorizationScopePublisherIdentity AuthorizationScope = "publisher-identity"
)

func authorizationScopeForStableVersion(version string) (AuthorizationScope, error) {
    if version == "0.4.12" { return AuthorizationScopeArtifactSHA256, nil }
    if !isEnrollmentVersion(version) { return "", errors.New("stable version is invalid") }
    return AuthorizationScopePublisherIdentity, nil
}
```

For v0.4.12, keep `RequireManifestSHA256:true` and require exactly one matching non-empty rule hash. For later versions, require exactly one matching NaisNet primary/stable/tag rule; pass the actual artifact hash with `RequireManifestSHA256:false`, so a non-empty rule still binds exactly while an omitted hash authorizes the structured identity. Reject duplicate matching rules.

- [ ] **Step 4: Write the JavaScript RED cases**

Mirror both evidence forms in `scripts/verify-enrollment-build.test.ts`. Assert that publisher-identity scope requires an empty `authorizedArtifactSha256`, the artifact evidence still carries the actual EXE SHA-256, and artifact-hash scope requires equality.

Run:

```powershell
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/verify-enrollment-build.test.ts
```

Expected: FAIL because the current verifier has no `authorizationScope` contract.

- [ ] **Step 5: Update the JavaScript verifier and types**

Accept only the two exact scope strings. Emit:

```js
authorizationPolicy: {
  sha256: authorizationSHA256,
  epoch: options.authorizationPolicyEpoch,
  scope: inspection.authorizationScope,
  artifactSha256: inspection.authorizedArtifactSha256,
  identity: naisNetIdentity,
}
```

Do not copy the actual artifact hash into `authorizationPolicy.artifactSha256` for a hashless policy; the separate `artifact.sha256` field already records it.

- [ ] **Step 6: Run focused and regression tests**

Run the two commands from Steps 2 and 4, then:

```powershell
go -C goserver test ./... -count=1
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/release-workflow.test.ts tests/verify-enrollment-build.test.ts
```

- [ ] **Step 7: Commit**

```powershell
git add goserver/internal/artifactinspect/enrollment.go goserver/internal/artifactinspect/enrollment_test.go goserver/cmd/artifact-inspector/main.go goserver/cmd/artifact-inspector/main_test.go scripts/verify-enrollment-build.mjs scripts/verify-enrollment-build.d.mts scripts/verify-enrollment-build.test.ts
git commit -m "feat: authorize post-enrollment publisher identity"
```

---

### Task 2: Add the bounded epoch-3 same-publisher policy candidate

**Files:**
- Create: `publisher/policy-candidates/epoch-00000003.json`
- Modify: `updateapi/internal/trustpolicy/policy.go`
- Modify: `updateapi/internal/trustpolicy/policy_test.go`
- Modify: `scripts/publish-trust-policy.mjs`
- Modify: `tests/publish-trust-policy.test.ts`

**Interfaces:**
- Produces: an unsigned, canonical candidate consumed by `publisher-rotation.yml` with `candidate_epoch=3`, `expected_previous_epoch=2`.
- Produces: `maxPrimaryAllowedTags = 32` in both Go and JavaScript validators.

- [ ] **Step 1: Write failing bounded-window tests**

Test a sorted 20-tag NaisNet primary rule with no manifest hash, a 33-tag rejection, duplicate/reordered tags, a reserved v0.4.11 tag, and a hashless RushRush rejection.

```go
func TestCandidateAcceptsBoundedHashlessStableWindow(t *testing.T) {
    candidate := primaryCandidateJSON(t, Candidate{
        Epoch: 3,
        ExpiresAt: "2030-01-01T00:00:00Z",
        Publishers: []Publisher{{
            ID: "naisnet-primary", Role: "primary", Country: "CN",
            Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094",
            AllowedChannel: stableChannel,
            AllowedTags: []string{"v0.4.13", "v0.4.14", "v0.4.15", "v0.4.16", "v0.4.17", "v0.4.18", "v0.4.19", "v0.4.20", "v0.4.21", "v0.4.22", "v0.4.23", "v0.4.24", "v0.4.25", "v0.4.26", "v0.4.27", "v0.4.28", "v0.4.29", "v0.4.30", "v0.4.31", "v0.4.32"},
        }},
    })
    if _, err := ParseCandidate(candidate, CandidateOptions{ExpectedPreviousEpoch: 2, Now: candidateValidationTime}); err != nil {
        t.Fatal(err)
    }
}
```

Define `primaryCandidateJSON` in the test file as a thin `json.Marshal` helper over the production `Candidate` struct; it performs no validation or expected-value derivation.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
$env:GOCACHE='E:\bilibili\.worktrees\update-signer-trust-rotation-design\.streamlined-updateapi-gocache'
go -C updateapi test ./internal/trustpolicy -run 'TestCandidate.*Window|TestCandidate.*Tags' -count=1
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/publish-trust-policy.test.ts
```

Expected: the 33-tag bound is not enforced and the epoch-3 fixture does not exist.

- [ ] **Step 3: Implement the shared bound and candidate**

Create the candidate with the exact sorted list:

```json
{
  "epoch": 3,
  "expiresAt": "2030-01-01T00:00:00Z",
  "publishers": [
    {
      "id": "naisnet-primary",
      "role": "primary",
      "country": "CN",
      "organization": "NaisNet Technology Co., Ltd.",
      "organizationId": "91210103MA7CJ3C094",
      "allowedChannel": "stable",
      "allowedTags": ["v0.4.13","v0.4.14","v0.4.15","v0.4.16","v0.4.17","v0.4.18","v0.4.19","v0.4.20","v0.4.21","v0.4.22","v0.4.23","v0.4.24","v0.4.25","v0.4.26","v0.4.27","v0.4.28","v0.4.29","v0.4.30","v0.4.31","v0.4.32"]
    },
    {
      "id": "rushrush-bridge",
      "role": "bridge",
      "country": "CN",
      "organization": "RushRush Network Technology Ltd",
      "organizationId": "91450900MADM3GLG5P",
      "allowedChannel": "legacy-rushrush",
      "allowedTags": ["v0.4.11"]
    }
  ]
}
```

Store the actual file in the repository's compact canonical candidate format, with no trailing fields and no `manifestSha256` on either rule.

- [ ] **Step 4: Run candidate validation and regressions**

```powershell
$env:GOCACHE='E:\bilibili\.worktrees\update-signer-trust-rotation-design\.streamlined-updateapi-gocache'
go -C updateapi run ./cmd/trustpolicy validate-candidate --candidate ../publisher/policy-candidates/epoch-00000003.json --candidate-epoch 3 --expected-previous-epoch 2
go -C updateapi test ./internal/trustpolicy ./cmd/trustpolicy -count=1
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/publish-trust-policy.test.ts tests/release-workflow.test.ts
```

- [ ] **Step 5: Commit**

```powershell
git add publisher/policy-candidates/epoch-00000003.json updateapi/internal/trustpolicy/policy.go updateapi/internal/trustpolicy/policy_test.go scripts/publish-trust-policy.mjs tests/publish-trust-policy.test.ts
git commit -m "feat: authorize bounded NaisNet stable window"
```

---

### Task 3: Produce a bounded request when EVSign returns an unknown legal identity

**Files:**
- Modify: `goserver/internal/artifactinspect/authenticode_windows.go`
- Modify: `goserver/internal/artifactinspect/authenticode_other.go`
- Modify: `goserver/internal/artifactinspect/authenticode_windows_test.go`
- Modify: `goserver/cmd/artifact-inspector/main.go`
- Modify: `goserver/cmd/artifact-inspector/main_test.go`
- Create: `scripts/publisher-change-request.mjs`
- Create: `scripts/publisher-change-request.d.mts`
- Create: `tests/publisher-change-request.test.ts`

**Interfaces:**
- Produces: `InspectAuthenticodeCertificate(path string) (certidentity.Certificate, error)`; `InspectAuthenticodeFile` remains an identity-only compatibility wrapper.
- Produces CLI: `artifact-inspector inspect-authenticode --file C:\build\signed.exe`.
- Produces JSON: `{schemaVersion,tag,artifactSha256,certificateDerSha256,identity,currentPolicyEpoch,runId,runAttempt}`.

- [ ] **Step 1: Write failing inspection tests**

Assert the new inspection API returns the exact DER bytes and structured identity only when Authenticode is `Valid`; invalid/missing certificates and non-Windows calls fail closed.

- [ ] **Step 2: Run Go tests and confirm RED**

```powershell
$env:GOCACHE='E:\bilibili\.worktrees\update-signer-trust-rotation-design\.streamlined-gocache'
go -C goserver test ./internal/artifactinspect ./cmd/artifact-inspector -run 'TestInspectAuthenticodeCertificate|TestInspectAuthenticodeCommand' -count=1
```

- [ ] **Step 3: Implement certificate inspection without identity expectation**

Return DER only inside `certidentity.Certificate`; the CLI hashes DER and emits:

```json
{"status":"Valid","certificateDerSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","identity":{"country":"CN","organization":"FutureCo Technology Co., Ltd.","organizationId":"91110000EXAMPLE01"}}
```

Never emit DER Base64, Subject display text, file paths, or EVSign credentials.

- [ ] **Step 4: Write the request-generator RED tests**

Tests must cover exact key order, canonical tag/hash/run fields, unknown properties, malformed identity, newline/path/token leakage, create-only output, and deterministic bytes.

- [ ] **Step 5: Implement the request generator**

Export:

```js
export function buildPublisherChangeRequest(input) { /* strict closed validation */ }
export async function writePublisherChangeRequest(path, input) { /* flag: 'wx' */ }
```

The script is credential-free and has no network access.

- [ ] **Step 6: Run focused tests and commit**

```powershell
go -C goserver test ./internal/artifactinspect ./cmd/artifact-inspector -count=1
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/publisher-change-request.test.ts
git add goserver/internal/artifactinspect/authenticode_windows.go goserver/internal/artifactinspect/authenticode_other.go goserver/internal/artifactinspect/authenticode_windows_test.go goserver/cmd/artifact-inspector/main.go goserver/cmd/artifact-inspector/main_test.go scripts/publisher-change-request.mjs scripts/publisher-change-request.d.mts tests/publisher-change-request.test.ts
git commit -m "feat: capture unexpected update publisher identity"
```

---

### Task 4: Permit an explicitly reviewed future primary identity in policy tooling

**Files:**
- Modify: `updateapi/internal/trustpolicy/policy.go`
- Modify: `updateapi/internal/trustpolicy/policy_test.go`
- Modify: `scripts/publish-trust-policy.mjs`
- Modify: `tests/publish-trust-policy.test.ts`
- Modify: `docs/runbooks/publisher-rotation.md`

**Interfaces:**
- Consumes: the structured identity fields from Task 3's request.
- Produces: strict validation for one primary stable publisher whose ID matches `^[a-z0-9]+(?:-[a-z0-9]+)*-primary$`.
- Preserves: the exact RushRush bridge rule and maximum two publishers.

- [ ] **Step 1: Write failing future-primary tests**

Accept one reviewed example such as `futureco-primary` with `country=CN`, trimmed non-empty organization, uppercase organization ID matching `^[0-9A-Z]{8,32}$`, stable channel, canonical tags, and optional manifest hash. Reject empty/Unicode-control organization data, lowercase ID, NaisNet/RushRush duplicate scope, a second primary, an arbitrary bridge, or more than two publishers.

- [ ] **Step 2: Run Go and JavaScript tests and confirm RED**

```powershell
go -C updateapi test ./internal/trustpolicy -run 'TestCandidate.*Primary|TestCandidate.*Publisher' -count=1
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/publish-trust-policy.test.ts
```

- [ ] **Step 3: Implement identical closed validators**

Do not infer trust from a CA or EVSign. Candidate bytes remain the reviewed authorization input; signing still requires the protected rotation-root environment and exact epoch transition.

- [ ] **Step 4: Document the event-time procedure**

The runbook must require comparing the Task 3 request with the actual EVSign account/certificate order, editing a new committed candidate, reviewing the public diff, and separately confirming policy signing/publication/discovery. It must prohibit automatically converting a request artifact into a signed policy.

- [ ] **Step 5: Run regressions and commit**

```powershell
go -C updateapi test ./internal/trustpolicy ./cmd/trustpolicy -count=1
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/publish-trust-policy.test.ts tests/release-workflow.test.ts
git add updateapi/internal/trustpolicy/policy.go updateapi/internal/trustpolicy/policy_test.go scripts/publish-trust-policy.mjs tests/publish-trust-policy.test.ts docs/runbooks/publisher-rotation.md
git commit -m "feat: review future stable publisher identities"
```

---

### Task 5: Add v0.4.13 to the exact stable routing matrix

**Files:**
- Modify: `updateapi/internal/service/channel_router.go`
- Modify: `updateapi/internal/service/channel_router_test.go`
- Modify: `updateapi/internal/httpapi/routing_test.go`
- Modify: `updateapi/cmd/routecheck/main.go`
- Modify: `updateapi/cmd/routecheck/main_test.go`
- Modify: `deploy/update-api/README.md`

**Interfaces:**
- Produces: `Version0413 VersionBucket = "0.4.13"`.
- Maps only exact `bilibili-live-gift-panel/0.4.13` to `stable`.

- [ ] **Step 1: Change the existing unreviewed-v0.4.13 expectations to RED**

Add v0.4.13 stable cases at the router, HTTP, and routecheck seams. Move the unknown future case to v0.4.14. Expected routecheck terminal count becomes 14.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
$env:GOCACHE='E:\bilibili\.worktrees\update-signer-trust-rotation-design\.streamlined-updateapi-gocache'
go -C updateapi test ./internal/service ./internal/httpapi ./cmd/routecheck -run 'TestChannelRouter|TestVersionBucket|TestVersionAwareRouting|TestRoutecheck' -count=1
```

- [ ] **Step 3: Add only the exact v0.4.13 map entry**

Do not parse arbitrary semantic versions and do not change v0.4.7 legacy behavior.

- [ ] **Step 4: Run update API regressions and commit**

```powershell
go -C updateapi test ./... -count=1
git add updateapi/internal/service/channel_router.go updateapi/internal/service/channel_router_test.go updateapi/internal/httpapi/routing_test.go updateapi/cmd/routecheck/main.go updateapi/cmd/routecheck/main_test.go deploy/update-api/README.md
git commit -m "feat: route v0.4.13 through stable updates"
```

---

### Task 6: Implement a numeric-ID resumable stable draft transaction

**Files:**
- Create: `scripts/stable-release-transaction.mjs`
- Create: `scripts/stable-release-transaction.d.mts`
- Create: `tests/stable-release-transaction.test.ts`
- Modify: `tests/release-workflow.test.ts`

**Interfaces:**
- Produces: `planStableDraft(input) -> { action:'create', missingAssets } | { action:'resume', releaseId:number, missingAssets }`.
- Produces: `runStableReleaseTransaction({ github, repository, tag, targetCommit, title, assetDirectory, requiredAssets })`.
- GitHub adapter methods: `listReleases`, `createDraft`, `getReleaseById`, `uploadAssetById`, `publishById`, `getReleaseByTag`, `getLatest`, `downloadAsset`.

- [ ] **Step 1: Write a pure planner RED matrix**

Use literal fixtures for:

- zero matches → create;
- one empty valid draft → resume/all assets missing;
- one partial valid draft → resume/only missing names;
- one complete valid draft → resume/no uploads;
- conflicting title, target commit, prerelease, published state, extra/mismatched asset, duplicate tag match → reject.

Every required asset fixture includes literal `name`, integer `size`, and lowercase `sha256`.

- [ ] **Step 2: Run the planner tests and confirm RED**

```powershell
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/stable-release-transaction.test.ts
```

- [ ] **Step 3: Implement planner and bounded GitHub adapter**

The live adapter must:

```text
GET  /repos/{repo}/releases?per_page=100&page=N
POST /repos/{repo}/releases
GET  /repos/{repo}/releases/{id}
POST {upload_url}?name={exactName}
PATCH /repos/{repo}/releases/{id}
GET  /repos/{repo}/releases/tags/{tag}        # only after publish
GET  /repos/{repo}/releases/latest            # only after publish
```

Cap pagination at five pages, JSON bodies at 512 KiB, each asset by its reviewed maximum, and redirects to the exact GitHub release-asset hosts already allowed by repository tooling. Never call DELETE and never use tag lookup for a draft.

- [ ] **Step 4: Write executable transaction tests**

Use an injected in-memory GitHub adapter that persists releases/assets and validates the uploaded bytes. Assert numeric-ID readback, upload-only-missing behavior, final published/Latest state, and full re-download hashing. The fake must reject unexpected calls and wrong IDs.

- [ ] **Step 5: Add the workflow contract RED test**

Assert `release.yml` invokes `stable-release-transaction.mjs`, never calls `/releases/tags/` before draft publication, and has no automatic draft/Release deletion path.

- [ ] **Step 6: Run tests and commit**

```powershell
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/stable-release-transaction.test.ts tests/release-workflow.test.ts
git add scripts/stable-release-transaction.mjs scripts/stable-release-transaction.d.mts tests/stable-release-transaction.test.ts tests/release-workflow.test.ts
git commit -m "feat: transact stable drafts by release id"
```

---

### Task 7: Convert stable release to one-run public metadata handoff

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `tests/release-workflow.test.ts`
- Modify: `scripts/verify-enrollment-build.mjs`
- Modify: `scripts/verify-enrollment-build.test.ts`
- Modify: `docs/release.md`

**Interfaces:**
- Consumes: Task 1 authorization scope, Task 3 unexpected-identity request, Task 6 transaction CLI.
- Produces workflow operation: `release` for one exact existing canonical tag.
- `publish-candidate.needs`: `[classify, prepare-candidate, sign-candidate]`.
- Candidate metadata comes from `needs.sign-candidate.outputs` and `github.run_id/run_attempt`, never `STABLE_CANDIDATE_*` environment variables.

- [ ] **Step 1: Write failing workflow-graph tests**

Assert:

```ts
expect(inputs.operation.options).toEqual(['release', 'verify-existing'])
expect(publish.needs).toEqual(['classify','prepare-candidate','sign-candidate'])
expect(JSON.stringify(publish)).not.toContain('vars.STABLE_CANDIDATE_')
```

Require public output validation for unsigned/signed artifact IDs and digests. Assert no signer Secret is visible to `publish-candidate`, no rotation-root Secret appears anywhere in `release.yml`, and no target code executes after the signing environment is entered.

- [ ] **Step 2: Run the workflow test and confirm RED**

```powershell
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/release-workflow.test.ts
```

- [ ] **Step 3: Rewire the classifier and job outputs**

Remove the tag-push candidate branch from this workflow. `workflow_dispatch(operation=release, tag=vX.Y.Z)` verifies that the tag exists, is canonical, is not v0.4.11, and has no published Release. It then runs prepare → sign → publish in one run.

Expose from `sign-candidate`:

```yaml
outputs:
  candidate-sha256: ${{ steps.candidate.outputs.sha256 }}
  candidate-size: ${{ steps.candidate.outputs.size }}
  artifact-id: ${{ steps.upload.outputs.artifact-id }}
  artifact-digest: ${{ steps.upload.outputs.artifact-digest }}
  artifact-name: ${{ steps.candidate.outputs.artifact-name }}
  inspector-sha256: ${{ steps.candidate.outputs.inspector-sha256 }}
  verifier-sha256: ${{ steps.candidate.outputs.verifier-sha256 }}
```

The publisher downloads by the exact same-run artifact ID and validates `workflow_run.id == github.run_id` without copied timestamps or manual variables.

- [ ] **Step 4: Load the reusable current policy without per-release Base64 variables**

Keep one non-secret `STABLE_AUTHORIZATION_POLICY_EPOCH`, initially `3`. Download the exact three assets from `publisher-policy-epoch-00000003`, import them into a private bundle, verify them with the committed rotation SPKI, and pass the verified policy bytes to the final inspector. Do not read the rotation private key.

- [ ] **Step 5: Add unexpected-signer request behavior**

After EVSign returns a file, run Task 3's inspection command. If identity equals the reviewed active primary, run the normal static/candidate verifier. Otherwise create and upload the exact name `"publisher-change-request-" + tag + "-" + artifactSha256` with `if: failure()` and fail before `sign-candidate` exposes a publishable artifact ID.

- [ ] **Step 6: Replace inline draft commands with Task 6 transaction**

The publisher prepares a closed asset descriptor, invokes the transaction script, and requires final numeric-ID/tag/latest closure. Remove direct `gh release create`, `gh release upload`, and draft tag lookup.

- [ ] **Step 7: Remove per-release configuration dependencies**

Delete workflow references to all `STABLE_CANDIDATE_*` variables. Retain only long-lived public trust/tooling inputs, the epoch number, fixed FFmpeg identity, EVSign protected credentials, and update API base URL.

- [ ] **Step 8: Run workflow/security regressions and commit**

```powershell
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/release-workflow.test.ts tests/stable-release-transaction.test.ts tests/verify-enrollment-build.test.ts tests/publisher-change-request.test.ts
git add .github/workflows/release.yml tests/release-workflow.test.ts scripts/verify-enrollment-build.mjs scripts/verify-enrollment-build.test.ts docs/release.md
git commit -m "ci: streamline same-publisher stable releases"
```

---

### Task 8: Prepare the v0.4.13 release content and reviewed changelog history

**Files:**
- Modify: `package.json`
- Modify: `package-lock.json`
- Modify: `gift-panel-changelog.json`
- Modify: `.github/changelog-history.json`
- Modify: `tests/changelog.test.ts`
- Modify: `tests/release-workflow.test.ts`

**Interfaces:**
- Produces version `0.4.13` and exact tag `v0.4.13`.
- Produces a target changelog whose previous reviewed history begins with published v0.4.12.

- [ ] **Step 1: Write failing changelog/version tests**

Require package and lock versions `0.4.13`, one target release, date `2026-09-03`, non-empty highlights, and reviewed history order `0.4.12, 0.4.10, 0.4.9, 0.4.7` with no v0.4.8.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/changelog.test.ts tests/release-workflow.test.ts
```

- [ ] **Step 3: Update exact release content**

Use title `缩小安装包并简化安全发布` and summary that states v0.4.12 functionality is retained, FFmpeg source/test/release staging is no longer embedded as UI, the signed FFmpeg 9.0 runtime remains bundled, and same-legal-publisher releases no longer require one policy epoch per version.

Use three highlights:

- `体积` — remove duplicate release staging from the EXE;
- `更新` — one-trigger same-publisher release flow;
- `安全` — unknown legal identity still pauses for explicit authorization.

- [ ] **Step 4: Derive changelog history from the committed file**

The one-run workflow computes the SHA-256 of `.github/changelog-history.json` from its exact tooling checkout and records it in candidate evidence. Remove the per-release `STABLE_CHANGELOG_HISTORY_SHA256` configuration requirement.

- [ ] **Step 5: Run tests and commit**

```powershell
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test -- --run tests/changelog.test.ts tests/release-workflow.test.ts
git add package.json package-lock.json gift-panel-changelog.json .github/changelog-history.json tests/changelog.test.ts tests/release-workflow.test.ts
git commit -m "release: prepare v0.4.13"
```

---

### Task 9: Run complete local and Windows packaged acceptance

**Files:**
- Create: `docs/verification/streamlined-stable-v0.4.13.md`
- Modify only on an evidenced defect: task-owned source/test files above

**Interfaces:**
- Produces privacy-safe verification evidence for the exact implementation commit.

- [ ] **Step 1: Run full local regression**

```powershell
$env:GOCACHE='E:\bilibili\.worktrees\update-signer-trust-rotation-design\.streamlined-goserver-gocache'
$env:GOMODCACHE='E:\bilibili\.worktrees\update-signer-trust-rotation-design\.streamlined-gomodcache'
go -C goserver test ./... -count=1
go -C updateapi test ./... -count=1
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' test
node 'C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js' run typecheck
```

Record UTC time, commit, command, exit code, and concise totals. Do not waive a failure unless the user explicitly accepts an evidenced unrelated failure.

- [ ] **Step 2: Build with the real signed FFmpeg closure and populated staging roots**

Use a clean Windows checkout. Download/attest the fixed FFmpeg component, populate `dist/ffmpeg-component` and `dist/release-ffmpeg-sealed`, install the verified payload to `goserver/ffmpeg`, and run the release build contract with reviewed public root/bootstrap inputs.

Require:

```text
ui_manifest_files = 87 (or the reviewed new UI count)
ui_manifest_bytes <= 2 MiB
forbidden_embedded_paths = 0
exe_size <= 18 MiB before Authenticode
```

Any intentional UI growth must be explained by manifest entries; do not increase the cap to accommodate staging files.

- [ ] **Step 3: Execute policy compatibility acceptance**

Run v0.4.12 policy parsing/authorization fixtures against the exact epoch-3 candidate. Verify NaisNet v0.4.13 passes, different organization ID fails, v0.4.12 exact-hash evidence remains valid, and rollback epoch 2 is rejected after epoch 3 is stored.

- [ ] **Step 4: Execute release transaction integration acceptance**

Run zero/partial/complete/conflicting/multiple draft scenarios and prove no DELETE call occurs. Re-run a complete draft to demonstrate idempotence.

- [ ] **Step 5: Execute Windows signer mismatch acceptance**

Use test certificates to prove same-identity leaf renewal continues, a changed organization ID emits the bounded request, and no draft or signed-candidate artifact ID is produced for the mismatch path.

- [ ] **Step 6: Review against spec and commit evidence**

Check every design requirement and record any deferred bridge work. If user-authorized agent review is available, use `superpowers:requesting-code-review`; otherwise perform the same standards/spec review inline.

```powershell
git add docs/verification/streamlined-stable-v0.4.13.md
git commit -m "test: verify streamlined v0.4.13 release"
```

---

### Task 10: Execute the approval-gated v0.4.13 production transition

**Files:**
- Update only after each completed gate: `docs/verification/streamlined-stable-v0.4.13.md`
- No source correction is allowed inside a rollout gate; an evidenced defect returns to RED→GREEN implementation.

**Interfaces:**
- Consumes: exact implementation commit, epoch-3 candidate, v0.4.13 tag, protected EVSign/rotation/COS environments.
- Produces: epoch-3 immutable policy closure, v0.4.13 GitHub Latest Release, stable COS pointer, and observation baseline.

- [ ] **Gate 1: Review and push implementation**

Show exact commits/files, remote `master` base, regression totals, unsigned EXE size/manifest, and rollback scope. Obtain confirmation, then fast-forward push only the reviewed commits. Verify `git ls-remote origin refs/heads/master` equals the intended commit.

- [ ] **Gate 2: Deploy v0.4.13 routing with legacy inactive**

Build the update API from the pushed commit, show the binary/config diff and rollback command, obtain deployment confirmation, deploy with `UPDATE_LEGACY_ROUTING_ACTIVE=false`, and verify:

```text
0.4.9  -> stable 200
0.4.10 -> stable 200
0.4.11 -> stable 200
0.4.12 -> stable 200
0.4.13 -> stable 200
0.4.14 -> 400
0.4.7  -> controlled unavailable while legacy inactive
```

- [ ] **Gate 3: Sign, publish, and advance epoch 3**

Validate the committed candidate with `candidate_epoch=3`, `expected_previous_epoch=2`. Show exact bytes/length/SHA-256 and the NaisNet tag window. Obtain confirmation, run `publisher-rotation.yml` with discovery advancement, then independently verify GitHub immutable assets, COS immutable object, GitHub discovery, domestic discovery, root signature, and epoch monotonicity.

- [ ] **Gate 4: Create and push exact v0.4.13 tag**

Show the release commit, package/changelog version, annotated tag target, and current remote state. Obtain confirmation, push the commit and exact `v0.4.13` tag without modifying v0.4.12.

- [ ] **Gate 5: Run the one-trigger stable release**

Show the exact dispatch input `operation=release`, `tag=v0.4.13`. Obtain the public-publication confirmation and dispatch once. Capture:

- workflow run/attempt;
- signed artifact ID/digest;
- signed EXE exact size/SHA-256;
- Authenticode structured identity;
- FFmpeg hash/identity;
- authorization epoch/scope;
- numeric draft Release ID;
- attestation ID;
- final published Release ID and Latest state.

- [ ] **Gate 6: Verify GitHub and domestic delivery**

Re-read every GitHub asset name/size/digest, download and hash the EXE, verify the COS stable manifest uses the same 0.4.13 bytes, and run real update checks for v0.4.9, v0.4.10, and v0.4.12. Do not log presigned URLs.

- [ ] **Gate 7: Observe before bridge work**

Start a seven-day bounded observation window for v0.4.13 policy/update result codes. Keep legacy inactive. Do not run the existing bridge workflow; its v0.4.12 convergence assumptions require a separate reviewed amendment after this observation passes.

- [ ] **Final verification**

Use `superpowers:verification-before-completion`. A successful GitHub Release alone is not completion: epoch 3, GitHub Latest, COS stable, exact EXE bytes, client routing, and Windows acceptance must all have current evidence.
