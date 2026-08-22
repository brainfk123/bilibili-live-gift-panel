# Signed FFmpeg Component Cache Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and EV-sign each canonical FFmpeg component once, reuse its immutable verified Release assets in later application releases, and retry transient EV Sign failures safely.

**Architecture:** A shared policy module computes a deterministic component descriptor and tag before MSYS2 setup. A component-asset helper creates or verifies the complete signed binary and compliance closure, while the release workflow treats only an exact GitHub 404 as a cache miss. The signing client uses a bounded injectable HTTPS transport and atomic output replacement; Authenticode and exact signer checks remain independent release gates.

**Tech Stack:** Node.js 22 ESM, TypeScript/Vitest, PowerShell 7, GitHub Actions/GitHub CLI, Go 1.26 embed validation, Windows Authenticode, FFmpeg 9.0/MSYS2.

**Spec:** `docs/superpowers/specs/2026-08-22-signed-ffmpeg-component-cache-design.md`

## Global Constraints

- Do not mutate or move the `v0.4.6` tag, and do not claim its failed historical run used this workflow.
- Component tags use `ffmpeg-component-v1-<64 lowercase hex fingerprint>` and never match the application `vMAJOR.MINOR.PATCH` trigger.
- Only an exact GitHub 404 is a cache miss; every other lookup or validation failure stops the release.
- Existing component Releases are immutable: no `--clobber`, overwrite, silent repair, or automatic deletion.
- A cache hit skips MSYS2 setup, FFmpeg compilation, and inner FFmpeg signing.
- Signed binaries from GitHub remain untrusted until digest, descriptor, component gate, Authenticode, exact signer, ZIP shape, and runtime checks all pass.
- Preserve the existing application Release FFmpeg source, source signature, build config, gate, toolchain lock, notice, and LGPL assets.
- EV Sign defaults are exactly 3 attempts, 600000 ms per attempt, and 15000/45000 ms retry delays.
- Retry only transport failures, timeouts, HTTP 408, HTTP 429, and HTTP 500-599; all other 4xx and local integrity failures are terminal.
- Never log EV Sign credentials, request headers, executable bytes, response bodies, or sensitive endpoint query data.
- Keep the Go embed file contract at `goserver/ffmpeg/ffmpeg.zip` and `goserver/ffmpeg/manifest.json` with strict exact-key parsing.
- Do not publish a component, push a tag, rerun v0.4.6, or invoke paid signing during local implementation tests.

---

### Task 1: Canonical FFmpeg policy and component identity

**Files:**
- Create: `scripts/ffmpeg-policy.mjs`
- Create: `scripts/ffmpeg-policy.d.mts`
- Create: `tests/ffmpeg-policy.test.ts`
- Modify: `scripts/verify-ffmpeg.mjs`

**Interfaces:**
- Produces: `loadFFmpegPolicy(root: string): Promise<FFmpegPolicy>`.
- Produces: `serializeFFmpegDescriptor(policy: FFmpegPolicy): Buffer`.
- Produces: `ffmpegComponentIdentity(policy: FFmpegPolicy): { descriptor: Buffer; descriptorSha256: string; fingerprint: string; tag: string }`.
- Produces: `componentGateRecord(policy: FFmpegPolicy, binary: Buffer): Buffer` for the existing binary-bound gate.
- Produces CLI command: `node scripts/ffmpeg-policy.mjs --self-test`, using synthetic policy fixtures only.
- Consumes: `third_party/ffmpeg/configure.flags`, `third_party/ffmpeg/toolchain-lock.json`, and the existing FFmpeg 9.0 source/component policy currently embedded in `verify-ffmpeg.mjs`.

- [ ] **Step 1: Write the failing canonical-identity tests**

Create a fixture policy and assert exact LF serialization and the precomputed fingerprint:

```ts
const fixture = {
  schema: 1,
  version: '9.0',
  sourceSha256: 'a'.repeat(64),
  sourceDateEpoch: '123',
  configureSha256: 'b'.repeat(64),
  toolchainLockSha256: 'c'.repeat(64),
  components: ['A', 'B'],
  infrastructure: ['D3D11VA', 'MEDIAFOUNDATION'],
};

expect(serializeFFmpegDescriptor(fixture).toString('utf8')).toBe(
  'schema=1\nffmpeg_version=9.0\n' +
  `source_sha256=${'a'.repeat(64)}\nsource_date_epoch=123\n` +
  `configure_sha256=${'b'.repeat(64)}\n` +
  `toolchain_lock_sha256=${'c'.repeat(64)}\n` +
  '[components]\nA\nB\n[infrastructure]\nD3D11VA\nMEDIAFOUNDATION\n',
);
expect(ffmpegComponentIdentity(fixture).fingerprint)
  .toBe('256712f90a56797f830e67e698d3c49c4b0e1cb47299de80f062f9aef0e5b81c');
expect(ffmpegComponentIdentity(fixture).tag)
  .toBe('ffmpeg-component-v1-256712f90a56797f830e67e698d3c49c4b0e1cb47299de80f062f9aef0e5b81c');
```

Also mutate each descriptor field independently and assert that its fingerprint changes, while application version and commit are absent from `FFmpegPolicy`.

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `npm test -- --run tests/ffmpeg-policy.test.ts`

Expected: FAIL because `scripts/ffmpeg-policy.mjs` does not exist.

- [ ] **Step 3: Implement the policy module and declarations**

Move the exact source SHA, epoch, component set, infrastructure set, canonical toolchain-lock normalization, configure normalization, and Authenticode-content hashing out of `verify-ffmpeg.mjs`. Implement fixed-order descriptor serialization with sorted policy sets and strict validation:

Expose these exact declarations from `scripts/ffmpeg-policy.d.mts`:

```ts
export function loadFFmpegPolicy(root: string): Promise<FFmpegPolicy>;
export function serializeFFmpegDescriptor(policy: FFmpegPolicy): Buffer;
export function ffmpegComponentIdentity(policy: FFmpegPolicy): {
  descriptor: Buffer;
  descriptorSha256: string;
  fingerprint: string;
  tag: string;
};
export function componentGateRecord(policy: FFmpegPolicy, binary: Buffer): Buffer;
```

In the implementation, hash the exact Buffer returned by `serializeFFmpegDescriptor`; use that same lowercase digest for `descriptorSha256`, `fingerprint`, and the component-tag suffix.

Reject duplicate/unsorted policy entries, malformed 64-character hashes, non-integer epochs, unknown toolchain keys, CRLF canonical output, and descriptor sizes above 16384 bytes.

Add a CLI main guard whose only direct command is `--self-test`; importing the module must have no side effects.

- [ ] **Step 4: Rewire the existing verifier without changing its runtime surface**

Import `loadFFmpegPolicy()` and `componentGateRecord()` from the new module. Delete duplicated constants/helpers from `verify-ffmpeg.mjs`; leave the exact FFmpeg CLI checks unchanged.

- [ ] **Step 5: Run focused and existing FFmpeg self-tests**

Run:

```bash
npm test -- --run tests/ffmpeg-policy.test.ts
node scripts/verify-ffmpeg.mjs --self-test
```

Expected: PASS, including the fixed fingerprint fixture and all existing malformed gate/ZIP cases.

- [ ] **Step 6: Commit Task 1**

```bash
git add -- scripts/ffmpeg-policy.mjs scripts/ffmpeg-policy.d.mts scripts/verify-ffmpeg.mjs tests/ffmpeg-policy.test.ts
git commit -m "refactor: centralize FFmpeg component policy"
```

---

### Task 2: Strict schema-1 package manifest and Go runtime parser

**Files:**
- Modify: `scripts/package-ffmpeg.mjs`
- Modify: `scripts/verify-ffmpeg.mjs`
- Modify: `goserver/gift_clip_payload.go`
- Modify: `goserver/gift_clip_payload_test.go`
- Modify: `goserver/ffmpeg/manifest.json`
- Create: `tests/ffmpeg-package-manifest.test.ts`

**Interfaces:**
- Consumes: `ffmpegComponentIdentity()` and `componentGateRecord()` from Task 1.
- Produces manifest keys: `schema`, `component_fingerprint`, `descriptor`, `descriptor_sha256`, `version`, `sha256`, `archive_sha256`, `component_gate`, `component_gate_sha256`, `size`, `authenticode`, `signer_subject`, `source_release_commit`.
- Produces: exported `publishPairTransactionally(directory, archive, manifest)` for Task 3's verified install path.
- Go consumes the exact same schema through `giftClipFFmpegManifest` and `parseGiftClipFFmpegManifest`.

- [ ] **Step 1: Write failing Node manifest tests**

Import the package manifest builder and assert an exact key list, exact fingerprint/descriptor binding, signer subject, and 40-character lowercase source commit. Add table cases that remove, duplicate, mistype, or alter every new field and expect rejection by the verifier.

```ts
expect(Object.keys(manifest)).toEqual([
  'schema', 'component_fingerprint', 'descriptor', 'descriptor_sha256',
  'version', 'sha256', 'archive_sha256', 'component_gate',
  'component_gate_sha256', 'size', 'authenticode', 'signer_subject',
  'source_release_commit',
]);
expect(manifest.schema).toBe(1);
expect(manifest.signer_subject).toBe('CN=Release Test');
expect(manifest.source_release_commit).toBe('1'.repeat(40));
```

- [ ] **Step 2: Write failing Go strict-parser tests**

Extend the valid fixture with all schema-1 fields. For each new key, test missing, duplicate, wrong JSON type, malformed hash, inconsistent descriptor hash/fingerprint, empty signer subject, and malformed source commit. Keep the existing unknown-key rejection.

- [ ] **Step 3: Run tests and verify the schema mismatch**

Run:

```bash
npm test -- --run tests/ffmpeg-package-manifest.test.ts
go -C goserver test ./... -run 'Test.*GiftClip.*Manifest' -count=1
```

Expected: FAIL because package output and Go parsing still use the legacy seven-field manifest.

- [ ] **Step 4: Implement manifest creation and Node verification**

Make `package-ffmpeg.mjs` load the local policy, calculate identity, require `EVSIGN_EXPECTED_SUBJECT` and `APP_COMMIT` for release packaging, and emit the exact schema-1 object. For development-only unsigned packaging, emit an empty signer subject and forty zeroes as the source commit. Keep signed PE-content binding and transactional pair publication.

Make `verify-ffmpeg.mjs` enforce exact keys/types and compare the descriptor/fingerprint to the local checked-in policy. When `authenticode` is true, have its PowerShell probe return both status and certificate Subject as JSON and compare Subject exactly to `EVSIGN_EXPECTED_SUBJECT`.

- [ ] **Step 5: Implement strict Go parsing and validation**

Add typed fields to `giftClipFFmpegManifest`, enumerate every key in the existing token-level parser, and validate:

```go
if manifest.Schema != 1 ||
   !sha256Pattern.MatchString(manifest.ComponentFingerprint) ||
   !sha256Pattern.MatchString(manifest.DescriptorSHA256) ||
   manifest.ComponentFingerprint != manifest.DescriptorSHA256 ||
   !gitCommitPattern.MatchString(manifest.SourceReleaseCommit) ||
   (manifest.Authenticode && manifest.SignerSubject == "") ||
   (!manifest.Authenticode && (manifest.SignerSubject != "" || manifest.SourceReleaseCommit != strings.Repeat("0", 40))) {
    return fmt.Errorf("%w: invalid component manifest", errGiftClipPayloadIntegrity)
}
```

Recompute `descriptor_sha256` from the descriptor bytes. Preserve existing archive, binary, gate, size, and ZIP-shape validation. Upgrade the checked-in unsigned `goserver/ffmpeg/manifest.json` to this explicit development state; do not alter its ZIP payload.

- [ ] **Step 6: Run focused package and Go tests**

Run:

```bash
npm test -- --run tests/ffmpeg-package-manifest.test.ts
node scripts/package-ffmpeg.mjs --self-test
node scripts/verify-ffmpeg.mjs --self-test
go -C goserver test ./... -run 'Test.*GiftClip.*Manifest|Test.*GiftClip.*Payload' -count=1
```

Expected: PASS with exact schema enforcement in both languages.

- [ ] **Step 7: Commit Task 2**

```bash
git add -- scripts/package-ffmpeg.mjs scripts/verify-ffmpeg.mjs goserver/gift_clip_payload.go goserver/gift_clip_payload_test.go goserver/ffmpeg/manifest.json tests/ffmpeg-package-manifest.test.ts
git commit -m "feat: bind FFmpeg payload to component identity"
```

---

### Task 3: Immutable component asset closure

**Files:**
- Create: `scripts/ffmpeg-component-assets.mjs`
- Create: `scripts/ffmpeg-component-assets.d.mts`
- Create: `tests/ffmpeg-component-assets.test.ts`
- Modify: `package.json`

**Interfaces:**
- Consumes: Task 1 identity and Task 2 strict payload pair.
- Produces: `REQUIRED_COMPONENT_ASSETS` in this exact order: `ffmpeg.zip`, `manifest.json`, `ffmpeg-9.0.tar.xz`, `ffmpeg-9.0.tar.xz.asc`, `ffmpeg-build-config.txt`, `ffmpeg-component-gate.txt`, `toolchain-lock.json`, `NOTICE.md`, `COPYING.LGPLv2.1`.
- Produces: `prepareComponentAssets({ root, outputDirectory }): Promise<ComponentIdentity>`.
- Produces: `verifyComponentAssets({ root, inputDirectory, expectedSigner }): Promise<ComponentIdentity>`.
- Produces CLI commands: `identity`, `prepare`, `verify`, and `install`.

- [ ] **Step 1: Write failing component closure tests**

Create a temporary valid fixture containing the strict payload pair and compliance files. Assert `prepare` emits all nine source/payload assets plus `SHA256SUMS.txt`, whose lines use lowercase hashes, two spaces, fixed order, and a final newline.

For every asset, add missing, duplicate-name metadata, byte mutation, malformed checksum, reordered checksum, traversal name, symlink, and unexpected-file cases. Assert `install` leaves an existing `goserver/ffmpeg` pair unchanged after any failure.

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `npm test -- --run tests/ffmpeg-component-assets.test.ts`

Expected: FAIL because the component asset module does not exist.

- [ ] **Step 3: Implement identity and prepare commands**

`identity` prints one compact JSON object to stdout:

```json
{"schema":1,"fingerprint":"<64 lowercase hex>","tag":"ffmpeg-component-v1-<64 lowercase hex>"}
```

`prepare` copies regular files only, rejects links and unexpected source shapes, writes `SHA256SUMS.txt` last, and never includes credentials or application-version metadata.

- [ ] **Step 4: Implement verification and transactional install**

Parse checksum lines without permissive whitespace or path normalization. Verify every file before reading the manifest. Recompute local identity, validate the strict manifest and exact checked-in `toolchain-lock.json`, notice, and license bytes, then call the signed payload verifier with `EVSIGN_EXPECTED_SUBJECT`.

`install` verifies from a temporary download directory first, then uses Task 2's `publishPairTransactionally()` to replace only `goserver/ffmpeg/ffmpeg.zip` and `manifest.json` as one recoverable pair.

- [ ] **Step 5: Add package scripts and run tests**

Add:

```json
"ffmpeg:component": "node scripts/ffmpeg-component-assets.mjs"
```

Run:

```bash
npm test -- --run tests/ffmpeg-component-assets.test.ts tests/ffmpeg-policy.test.ts tests/ffmpeg-package-manifest.test.ts
node scripts/ffmpeg-component-assets.mjs --self-test
```

Expected: PASS; invalid closure fixtures never alter the installed pair.

- [ ] **Step 6: Commit Task 3**

```bash
git add -- package.json scripts/ffmpeg-component-assets.mjs scripts/ffmpeg-component-assets.d.mts tests/ffmpeg-component-assets.test.ts
git commit -m "feat: verify immutable FFmpeg component assets"
```

---

### Task 4: Bounded EV Sign HTTPS retries

**Files:**
- Modify: `scripts/sign-evsign.mjs`
- Create: `scripts/sign-evsign.d.mts`
- Create: `tests/sign-evsign.test.ts`

**Interfaces:**
- Produces: `requestSignedBytes(request: SignRequest, dependencies?: SignDependencies): Promise<Buffer>`.
- Produces: `signFileWithRetry(options: SignFileOptions, dependencies?: SignDependencies): Promise<void>`.
- Defaults: `maxAttempts=3`, `attemptTimeoutMs=600000`, `retryDelaysMs=[15000, 45000]`.
- CLI remains: `node scripts/sign-evsign.mjs <input.exe> [output.exe]`.

- [ ] **Step 1: Write failing deterministic retry tests**

Inject a fake request transport, fake monotonic clock, and fake sleep. Cover these exact sequences:

```ts
it.each([
  ['timeout then success', [timeoutError(), signedResponse]],
  ['HTTP 408 then success', [httpError(408), signedResponse]],
  ['HTTP 429 then success', [httpError(429), signedResponse]],
  ['HTTP 503 twice then success', [httpError(503), httpError(503), signedResponse]],
])('%s', async (_name, responses) => {
  await signFileWithRetry(options, fakeDependencies(responses));
  expect(attempts).toBe(responses.length);
});
```

Assert delays are exactly 15000 and 45000 ms, every request body equals the original unsigned bytes, and output replacement happens once after success.

Add terminal cases for HTTP 400/401/403/404/409/422, empty 200, oversized 200, local write/rename failure, and exhausted timeout/5xx attempts. Assert original output bytes survive and all temporary files are removed.

- [ ] **Step 2: Run the focused test and verify the current fetch implementation fails**

Run: `npm test -- --run tests/sign-evsign.test.ts`

Expected: FAIL because retry interfaces and injectable timing do not exist.

- [ ] **Step 3: Replace fetch with a bounded `node:https` transport**

Use `https.request()` with one deadline covering connect, headers, and response body. Reject redirects, enforce HTTPS, cap returned bytes before buffering beyond the limit, and destroy the request on timeout. Classify errors into `transport`, `timeout`, `http-retryable`, `http-terminal`, and `integrity` without including response bodies in thrown/logged messages.

- [ ] **Step 4: Implement retry orchestration and atomic output**

Read the source once. For every attempt, submit the same Buffer and fixed headers. Log only attempt, elapsed time, safe category/status, and next delay. Write a unique sibling temporary file with exclusive creation, then atomically replace the output only after a complete non-empty 200 response. Cleanup runs in `finally` for every outcome.

Expose optional test-only dependencies through function arguments, never environment variables. CLI policy remains configurable only through validated `EVSIGN_MAX_ATTEMPTS`, `EVSIGN_ATTEMPT_TIMEOUT_MS`, and fixed comma-separated `EVSIGN_RETRY_DELAYS_MS`; workflow uses defaults.

- [ ] **Step 5: Run retry and redaction tests**

Run:

```bash
npm test -- --run tests/sign-evsign.test.ts
node scripts/sign-evsign.mjs 2>&1 | grep -F 'Usage:'
```

Expected: all Vitest cases PASS; no test log contains fixture key, password, certificate, endpoint query, request bytes, or response body.

- [ ] **Step 6: Commit Task 4**

```bash
git add -- scripts/sign-evsign.mjs scripts/sign-evsign.d.mts tests/sign-evsign.test.ts
git commit -m "fix: retry transient EV signing failures"
```

---

### Task 5: Release workflow cache hit, miss, publication, and recovery paths

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `tests/release-workflow.test.ts`
- Modify: `docs/research/evsign-api-sign.md`

**Interfaces:**
- Consumes CLI JSON from `node scripts/ffmpeg-component-assets.mjs identity`.
- Sets workflow environment: `FFMPEG_COMPONENT_FINGERPRINT`, `FFMPEG_COMPONENT_TAG`, and `FFMPEG_COMPONENT_EXISTS`.
- Cache-hit download directory: `dist/ffmpeg-component-download`.
- Cache-miss publication directory: `dist/ffmpeg-component-publish`.

- [ ] **Step 1: Write failing workflow ordering and branch tests**

Extend YAML contract tests to require this order:

```text
Resolve FFmpeg component identity
Inspect signed FFmpeg component
Download signed FFmpeg component (hit)
Set up MSYS2 host environment (miss only)
Build and verify pinned FFmpeg (miss only)
Sign and verify inner FFmpeg (miss only)
Package signed FFmpeg component (miss only)
Publish signed FFmpeg component (miss only)
Download published FFmpeg component (miss only)
Verify and install signed FFmpeg component
Build release executable
```

Assert the MSYS2/build/inner-sign/package/publish conditions are exactly `env.RELEASE_EXISTS != 'true' && env.FFMPEG_COMPONENT_EXISTS != 'true'`. Assert cache hit skips them. Assert both inner and outer signing call the same `scripts/sign-evsign.mjs` without workflow-level duplicate retry loops.

- [ ] **Step 2: Add PowerShell fixture tests for lookup classification**

Extract the lookup step text as existing tests do for release validation. Stub `Invoke-RestMethod` and assert:

- exact 404 sets `FFMPEG_COMPONENT_EXISTS=false`;
- published complete metadata sets it to true;
- 401, 403, 408, 429, 500, malformed JSON, draft, prerelease, missing `published_at`, wrong tag, duplicate asset names, missing asset, or unexpected asset fails without selecting miss.

- [ ] **Step 3: Run workflow tests and verify they fail before YAML changes**

Run: `npm test -- --run tests/release-workflow.test.ts`

Expected: FAIL at missing component identity/lookup steps and old unconditional MSYS2/build conditions.

- [ ] **Step 4: Implement identity and exact lookup before MSYS2**

Parse CLI identity JSON with `System.Text.Json`, validate exact keys/types and canonical tag, then append values to `GITHUB_ENV`. Query `/releases/tags/<encoded component tag>` with the existing pinned GitHub API version. Catch only `[Net.HttpStatusCode]::NotFound` as a miss; rethrow all other failures with no response body.

- [ ] **Step 5: Implement the cache-hit path**

Use `gh release download $env:FFMPEG_COMPONENT_TAG` with ten explicit `--pattern` arguments and a fresh destination. Do not download by wildcard. Validate GitHub metadata before download, then run:

```powershell
node scripts/ffmpeg-component-assets.mjs verify --input dist/ffmpeg-component-download
node scripts/ffmpeg-component-assets.mjs install --input dist/ffmpeg-component-download
npm run verify:ffmpeg
```

Pass only `EVSIGN_EXPECTED_SUBJECT`, not EV Sign credentials.

- [ ] **Step 6: Implement the cache-miss build/sign/package path**

Condition MSYS2 setup and all current FFmpeg build steps on a miss. Add `EVSIGN_EXPECTED_SUBJECT` to inner package/verification. Prepare the complete component publication directory and `SHA256SUMS.txt`, then run its local verifier before any GitHub mutation.

- [ ] **Step 7: Implement immutable component publication**

Create the component tag at `RELEASE_COMMIT` only after all local checks pass. Create a draft Release with exact title, upload the ten explicit assets without `--clobber`, publish it as non-latest, refetch metadata, delete the local download directory, redownload the published assets, and run the same verify/install path used by a hit.

If tag or Release creation reports an already-exists conflict, do not upload. Refetch the winner and route through full cache-hit verification. Any incomplete existing state fails with `Manual recovery required` and is left untouched.

- [ ] **Step 8: Preserve application Release compliance assets**

Before `Create GitHub release`, copy the verified downloaded component's source archive, detached signature, build config, component gate, toolchain lock, notice, and license to the exact paths currently uploaded by the application Release. Keep the existing application asset command and final validation behavior.

- [ ] **Step 9: Document retry and component recovery**

In `docs/research/evsign-api-sign.md`, document:

- default 3-attempt/10-minute retry classification;
- lack of documented server idempotency;
- component tag naming and immutable asset list;
- cache-hit verification and zero inner-sign expectation;
- manual recovery procedure for an incomplete component Release; and
- explicit warning that deleting/replacing a valid component is not routine cleanup.

- [ ] **Step 10: Run workflow and script contracts**

Run:

```bash
npm test -- --run tests/release-workflow.test.ts tests/ffmpeg-component-assets.test.ts tests/sign-evsign.test.ts
npm run typecheck
```

Expected: PASS; the YAML parser sees pinned Actions, exact branch conditions, immutable upload behavior, and final release gates.

- [ ] **Step 11: Commit Task 5**

```bash
git add -- .github/workflows/release.yml tests/release-workflow.test.ts docs/research/evsign-api-sign.md
git commit -m "feat: reuse signed FFmpeg release components"
```

---

### Task 6: Full local supply-chain regression and release handoff

**Files:**
- Modify only if a failing gate identifies a scoped defect in Task 1-5 files.
- Record results in the final task report; do not create or publish Release assets locally.

**Interfaces:**
- Consumes all Tasks 1-5.
- Produces evidence that source tests, Go runtime parsing, UI packaging, workflow contracts, and local unsigned development builds remain valid.

- [ ] **Step 1: Run the complete frontend and script suite**

Run:

```bash
npm test
npm run typecheck
npm run build:ui
```

Expected: all tests PASS, TypeScript exits 0, and Vite emits the complete UI graph.

- [ ] **Step 2: Run the complete Go suite and race detector**

Run:

```bash
go -C goserver test ./... -count=1 -timeout=300s
go -C goserver test -race ./... -count=1 -timeout=300s
go -C updateapi test ./... -race -count=1
```

Expected: all packages PASS without manifest parser, payload extraction, updater, or race regressions.

- [ ] **Step 3: Run all offline FFmpeg contract self-tests**

Run:

```bash
node scripts/ffmpeg-policy.mjs --self-test
node scripts/package-ffmpeg.mjs --self-test
node scripts/verify-ffmpeg.mjs --self-test
node scripts/ffmpeg-component-assets.mjs --self-test
```

Expected: PASS using synthetic fixtures only; no network, GitHub write, EV Sign call, or paid signing occurs.

- [ ] **Step 4: Build and verify the local development executable**

Run:

```bash
npm run prepare:go-assets
npm run build:exe
```

Expected: local development EXE builds with the repository's development FFmpeg fixture policy. Do not describe it as release-signed.

- [ ] **Step 5: Inspect exact release diff and secret exposure**

Run:

```bash
git diff origin/master...HEAD -- .github/workflows/release.yml scripts goserver/gift_clip_payload.go goserver/gift_clip_payload_test.go tests package.json docs/research/evsign-api-sign.md
rg -n "EVSIGN_(KEY|PASSWORD)|X-Key|X-Password" .github/workflows/release.yml scripts tests
```

Expected: only intended files changed; secrets appear only as environment references/header construction and never as literals or log statements.

- [ ] **Step 6: Request code review before integration**

Use `superpowers:requesting-code-review` against the complete implementation. Required review questions:

- Can any non-404 state select the component creation path?
- Can any cache hit bypass exact signer or real FFmpeg runtime validation?
- Can publication overwrite or silently repair an existing component?
- Can a failed signing attempt alter the original executable?
- Does a cache hit preserve every existing application Release compliance asset?

- [ ] **Step 7: Prepare, but do not execute, production acceptance**

Record these post-merge release checks for the first authorized release:

```text
First run: component miss -> one inner signing -> immutable component published -> redownloaded -> verified.
Second unchanged run: same fingerprint -> no MSYS2 -> no FFmpeg build -> no inner signing -> component verified -> app packaged.
Outer signing transient fault: retry logs show bounded attempts; only a valid exact-subject EXE can publish.
```

Do not push, tag, publish, rerun v0.4.6, or call EV Sign without a separate explicit approval.
