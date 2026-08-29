# Signed FFmpeg Component Cache Design

## Purpose

The Windows release workflow currently rebuilds and EV-signs the same pinned FFmpeg executable for every application release. A rerun of the single release job repeats that work even when the FFmpeg source, toolchain, configuration, and component surface did not change. This wastes runner time and sends unnecessary signing requests.

The release workflow shall publish each distinct signed FFmpeg build once as an immutable GitHub Release component, then reuse that exact signed component in later application releases after independent verification. The same change shall make EV Sign calls tolerate transient transport failures without weakening signature verification.

This design applies to releases after v0.4.6. It does not mutate the v0.4.6 tag or its historical workflow run.

## Goals

- Avoid rebuilding or re-signing FFmpeg when every build-defining input is unchanged.
- Treat a reused signed component as untrusted input until all provenance, digest, Authenticode, signer, and runtime checks pass.
- Preserve the current exact FFmpeg component gate and packaged application verification.
- Make transient EV Sign timeouts and service errors retryable with bounded time and diagnostic output.
- Preserve release repair behavior and fail closed on ambiguous or corrupt remote state.

## Non-goals

- Caching or reusing the application executable, whose version and embedded application content change per release.
- Changing the pinned FFmpeg version, enabled codecs, filters, protocols, toolchain, or licensing policy.
- Using `actions/cache` as a trust or publication boundary for signed binaries.
- Repairing or replacing an existing component Release in place.
- Changing the v0.4.6 tag or making the current failed v0.4.6 run use a newer workflow.

## Architecture

### Immutable component identity

A deterministic component descriptor shall be generated from the canonical build inputs already enforced by the FFmpeg component gate:

- descriptor schema version, currently `2`;
- exact FFmpeg version and pinned source SHA-256;
- `SOURCE_DATE_EPOCH`;
- SHA-256 of canonical `configure.flags` content;
- SHA-256 of the canonical toolchain lock;
- SHA-256 of the complete exact expected Authenticode signer Subject;
- the exact expected component and infrastructure sets.

The component fingerprint is the lowercase SHA-256 of the canonical UTF-8 descriptor. Canonical serialization uses fixed field order, LF line endings, no insignificant whitespace, and a final newline.

The GitHub tag and Release name are:

```text
ffmpeg-component-v2-<64-character-component-fingerprint>
```

The application release workflow only triggers for `vMAJOR.MINOR.PATCH`, so creating a component tag does not recursively start an application release.

The fingerprint deliberately uses build-defining inputs rather than compiler output, so a cache hit can skip MSYS2 setup and FFmpeg compilation entirely. On a cache miss, the component gate still records the unsigned PE Authenticode-content SHA-256 and binds the returned signed binary to that exact PE image. On a cache hit, the published component's signature, descriptor, component gate, archive closure, and real runtime surface are all reverified. Ordinary Actions caches may later accelerate cache misses, but are outside the signed-component trust boundary.

### Component assets

Each component Release contains exactly these release-facing assets:

```text
ffmpeg.zip
manifest.json
SHA256SUMS.txt
gift-clip-test-tools.zip
ffmpeg-9.0.tar.xz
ffmpeg-9.0.tar.xz.asc
ffmpeg-build-config.txt
ffmpeg-component-gate.txt
toolchain-lock.json
NOTICE.md
COPYING.LGPLv2.1
```

`ffmpeg.zip` remains the deterministic single-file archive consumed by the Go embed path. `manifest.json` extends the existing strict manifest with:

- `schema`: integer `1`;
- `component_fingerprint`;
- `descriptor` and `descriptor_sha256`;
- existing `version`, `sha256`, `archive_sha256`, `component_gate`, `component_gate_sha256`, `size`, and `authenticode` fields;
- `signer_subject`, exactly equal to the configured `EVSIGN_EXPECTED_SUBJECT`; and
- `source_release_commit`, the commit whose checked-out inputs produced the descriptor.

`source_release_commit` is audit metadata and is not part of the component fingerprint. The same component may therefore be reused by later commits with identical canonical build inputs.

Published component manifests always require `authenticode=true`, a non-empty exact signer Subject, and a real lowercase 40-character release commit. The checked-in unsigned development payload uses the same exact schema with `authenticode=false`, an empty `signer_subject`, and forty zeroes for `source_release_commit`; release verification rejects that development-only state.

`gift-clip-test-tools.zip` preserves the pinned full FFmpeg/FFprobe test-tool directory used by the deterministic browser export E2E. After extraction, its existing strict tool manifest and binary hashes must pass before use. The remaining assets preserve the source, source signature, exact build record, toolchain, notice, and LGPL license closure already attached to application Releases. A cache hit downloads them from the component Release so the application Release can continue publishing the same compliance assets and running the same E2E without rebuilding FFmpeg.

`SHA256SUMS.txt` contains lowercase SHA-256 values for every other component asset in a strict fixed order and format. GitHub's asset digest, when present, is an additional check and never replaces the repository's own digest checks.

Before publication, GitHub artifact attestations are issued for every component asset. Every cache hit and post-publication redownload verifies those attestations against this repository before any unsigned test tool is extracted or executed. This authenticates the full component closure in addition to the inner FFmpeg Authenticode check.

The component Release is published, non-draft, and non-prerelease. Existing assets are never replaced with `--clobber`. A tag or Release that already exists but is incomplete, draft, prerelease, duplicated, or inconsistent is an integrity failure requiring manual recovery.

## Release data flow

1. Check out and validate the application release tag as today.
2. Generate the canonical descriptor and component fingerprint from the checked-in pinned source digest, canonical toolchain lock, configure flags, and exact component policy.
3. Query the exact component tag through the GitHub API.
4. On a cache hit, download and fully verify the component without setting up MSYS2 or compiling FFmpeg.
5. On HTTP 404 only, enter the component creation path: install the pinned FFmpeg toolchain, build the unsigned FFmpeg binary, and run the existing unsigned component and runtime verification, then:
   - EV-sign the unsigned FFmpeg with bounded retry;
   - verify Authenticode status and exact signer subject;
   - package the signed FFmpeg and strict manifest;
   - rerun component-gate and runtime verification on the signed payload;
   - create the immutable component tag and Release without overwriting existing state;
   - upload the three assets; and
   - fetch Release metadata again and validate the published closure.
7. For either a cache hit or the newly published component, verify the complete remote closure before placing `ffmpeg.zip` and `manifest.json` in `goserver/ffmpeg` transactionally.
8. Build, test, and EV-sign the application executable as today, using bounded retry for the outer signature too.
9. Run the existing packaged gift-clip browser E2E and final application release checks.

The lookup and verification path must not require EV Sign secrets. Secrets are exposed only to a component cache miss and to the outer application signing step.

## Reuse verification

A component cache hit is accepted only when all of the following pass:

- exact canonical component tag and published Release state;
- exactly one asset of each required name and no conflicting duplicate name;
- `SHA256SUMS.txt`, manifest hashes, archive hash, binary hash, size, and available GitHub asset digests agree;
- every component asset has a valid GitHub artifact attestation for this repository;
- source archive, detached signature, build config, component gate, toolchain lock, notice, and license hashes agree with `SHA256SUMS.txt`, and checked-in policy files equal their downloaded canonical counterparts;
- manifest has only the allowed schema-1 keys with correct JSON types;
- manifest fingerprint equals the locally calculated fingerprint;
- descriptor hash and every descriptor field equal the locally calculated checked-in build inputs;
- component gate hash and content equal the locally expected gate, except signed binary hash and size are validated using the existing Authenticode-content binding rule;
- ZIP is the existing strict single-file `ffmpeg.exe` archive shape;
- Authenticode status is `Valid`;
- signer certificate exists and its Subject exactly equals `EVSIGN_EXPECTED_SUBJECT`;
- FFmpeg version, build configuration, protocols, codecs, filters, muxers, hardware acceleration, and license restrictions pass the existing runtime surface verifier; and
- the copied `goserver/ffmpeg` pair passes `verify:ffmpeg` before application compilation.

Any verification failure stops the application release. It must never fall back to rebuilding or re-signing after an existing component tag was found, because that could hide tampering or inconsistent publication.

## Concurrency and recovery

The existing non-canceling `gift-panel-production-release` concurrency group serializes application releases, so normal component creation cannot race within this workflow. The creation logic nevertheless treats `already exists` as a possible race with a manual or future component publisher:

- after a create conflict, discard local publication assumptions;
- fetch the exact existing component Release;
- run the full cache-hit verification; and
- reuse it only if it is complete and identical.

A signing or application release failure after successful component publication leaves the immutable component available for the rerun. This is intentional: a later run recomputes the same fingerprint and reuses the verified component without another inner signing request.

Failures before the component Release closure is fully published may leave an incomplete remote Release. The workflow fails closed and reports manual recovery; it does not delete remote tags/releases automatically and does not overwrite partial assets. Recovery must remove the incomplete component publication explicitly before rerunning, preserving an auditable destructive-action boundary.

## EV Sign retry policy

`scripts/sign-evsign.mjs` shall replace Node's `fetch` transport because its internal five-minute headers timeout can fire before the script's intended abort timer. The replacement HTTPS transport shall enforce the configured per-attempt deadline over connection, headers, and body receipt.

Default policy:

- maximum attempts: `3`;
- per-attempt timeout: `600000` milliseconds (10 minutes);
- delays before attempts 2 and 3: `15000` and `45000` milliseconds;
- retryable failures: connection/DNS/TLS reset or interruption, request timeout, HTTP 408, HTTP 429, and HTTP 500-599;
- terminal failures: all other HTTP 400-499 responses, invalid configuration, empty successful response, oversized response, local filesystem failure, and final Authenticode verification failure.

Each attempt uploads the original input bytes. Returned bytes are written to a unique temporary file, bounded by the existing maximum executable policy plus an explicit signature-growth allowance, then atomically replace the output only after a complete HTTP 200 response. A failed attempt never modifies the source or output executable.

Logs include attempt number, maximum attempts, elapsed milliseconds, retry category, HTTP status when safe, and next delay. Logs never include the license key, password, certificate selector, request headers, executable bytes, response body, or endpoint query credentials.

The workflow retains independent PowerShell Authenticode verification after the script, including exact signer Subject checks for both inner FFmpeg and outer application executables. Transport success is not signature success.

EV Sign's public API material does not document an idempotency key. If the service completes a signing operation but the client loses the response, a retry may create another server-side signing record. The local artifact remains atomic and uncorrupted, but the client cannot guarantee server-side exactly-once behavior. Bounded retries and signed-component reuse limit this exposure.

## Error handling

- GitHub component lookup 404: expected cache miss; build/sign/publish once.
- GitHub lookup authentication, rate limit, timeout, or 5xx: fail the release; do not interpret as a miss.
- Existing component malformed or unverifiable: fail with the first precise validation boundary and require manual recovery.
- EV Sign retry exhaustion: retain the unsigned local input, remove temporary output, and fail before packaging.
- Authenticode signer mismatch: terminal supply-chain failure, never retried as transport.
- Component publication conflict: refetch and fully verify the winner.
- Outer application signing failure: do not create the application Release; a rerun may reuse the already published FFmpeg component.
- Existing complete application Release repair: preserve the current no-rebuild/no-resign path.

## Implementation boundaries

The expected implementation touches:

- `.github/workflows/release.yml` for lookup, miss/hit branching, component publication, and reuse gates;
- `scripts/sign-evsign.mjs` for bounded HTTPS retries and atomic output;
- a focused component descriptor/cache helper under `scripts/` rather than embedding complex parsing in YAML;
- `scripts/package-ffmpeg.mjs` and `scripts/verify-ffmpeg.mjs` for schema-1 component metadata and exact signer checks;
- `goserver/gift_clip_payload.go` and its focused tests so the embedded runtime manifest continues to use strict exact-key parsing while requiring and validating the new schema-1 component fields;
- `tests/release-workflow.test.ts` plus focused script tests for descriptor, cache verification, and signing retry behavior; and
- release documentation describing immutable component recovery.

The Go embed file contract remains `goserver/ffmpeg/ffmpeg.zip` plus `goserver/ffmpeg/manifest.json`. Its strict manifest parser is extended to require the schema-1 component fields rather than ignoring unknown metadata. Application runtime behavior and FFmpeg execution behavior do not change.

## Test strategy

### Unit and script tests

- Canonical descriptor serialization and stable fingerprint fixture.
- Fingerprint changes for each build-defining input and remains independent of application version and commit.
- Strict manifest keys/types and checksum closure.
- Compliance closure rejects a missing, renamed, duplicated, reordered-checksum, or digest-mismatched source, toolchain, build-record, notice, or license asset.
- Cache hit accepts a valid signed fixture and rejects wrong fingerprint, descriptor, archive, binary, component gate, signer, or runtime surface.
- Go payload parsing accepts the exact schema-1 manifest and rejects a missing, duplicated, mistyped, unknown, or inconsistent component field.
- A found-but-invalid Release never enters the cache-miss signing path.
- Sign transport retries the exact allowed network/status classes with deterministic injected timers.
- Sign transport does not retry terminal 4xx, empty/oversized 200 responses, filesystem errors, or exhausted attempts.
- Failed sign attempts preserve the original output and clean temporary files.
- Logs redact all signing credentials and response content.

### Workflow contract tests

- External Actions remain pinned to immutable commit SHAs.
- Component lookup occurs before MSYS2 setup, FFmpeg compilation, and inner signing.
- Cache hit skips MSYS2 setup and FFmpeg compilation as well as inner signing.
- Only an exact 404 selects the signing path.
- Cache hit skips inner signing and component publication.
- Cache miss signs, verifies, publishes without clobbering, refetches, and validates.
- Both inner and outer signing use the same bounded retry implementation.
- Existing complete application Release still skips all builds and signatures.
- Final application publication remains gated by signed payload E2E, provenance attestation, exact outer signer, and checksums.

### Release acceptance

On the first post-change release with the current FFmpeg inputs:

- observe one component cache miss, one FFmpeg EV signing request, immutable component publication, complete component validation, and successful application packaging.

On a second synthetic or real release with unchanged FFmpeg inputs:

- observe the same fingerprint, component cache hit, no MSYS2 setup or FFmpeg compilation, zero FFmpeg EV signing calls, successful Authenticode and runtime verification of the downloaded component, preservation of all application Release FFmpeg compliance assets, and successful application packaging.

Change one canonical FFmpeg build input in an isolated test fixture:

- observe a different fingerprint and cache miss without publishing a production component.

## Operational notes

Component Releases will be visible alongside application Releases. Their current `ffmpeg-component-v2-` prefix distinguishes them from user-facing `vMAJOR.MINOR.PATCH` releases. Descriptor schema 2 includes `signer_subject_sha256`, so a signer change cannot reuse the previous component. Legacy `ffmpeg-component-v1-` Releases remain immutable historical supply-chain records but are never reused by v2. Component Releases must not be edited or deleted during routine cleanup.

The v0.4.6 failure remains a separate recovery action. Its tagged workflow cannot benefit from this future component cache, and rerunning its monolithic failed job will rebuild and re-sign FFmpeg once more.
