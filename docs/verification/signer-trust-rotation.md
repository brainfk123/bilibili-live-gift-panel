# Signer trust rotation local verification

## Result and scope

This record covers local Task 13 verification of source commit
`95f7c527b45e2b6169c4ffb4021ae6c56c17001e` on Windows amd64. The verification
window began at `2026-09-01T12:21:27Z` and completed at
`2026-09-01T12:45Z`. Go was `go1.26.5 windows/amd64`, Node was `v24.18.0`, and
all Go and npm caches used worktree-local directories.

The tested source passed the local regression, policy, package, API, mirror,
bundle, and workflow semantic gates described below. This is **not production
Windows acceptance**: both the locally built `v0.4.12`-shaped outer executable
and the extracted FFmpeg are unsigned, the Authenticode success paths are
explicitly simulated, and no production service or release state was read or
changed.

No workflow was dispatched. No EVSign request, certificate-store change, KMS
operation, COS or GitHub request, tag, Release, pointer, server, credential, or
production configuration mutation occurred. All generated packages, seams,
caches, and expanded files remain local and uncommitted.

## Full repository regression

All commands below were run from the repository root unless a working directory
is shown. `GOMODCACHE`, `GOCACHE`, and `npm_config_cache` pointed to relative
`.task13-*` directories. Go dependency lookup was disabled with `GOPROXY=off`.

| UTC interval | Working directory | Command | Exit | Result |
| --- | --- | --- | ---: | --- |
| `12:24:16Z`–`12:25:01Z` | `goserver` | `go test ./... -count=1` | 0 | All 26 reported packages passed. |
| `12:21:28Z`–`12:21:51Z` | `updateapi` | `go test ./... -count=1` | 0 | All 14 reported packages passed. |
| `12:23:25Z`–`12:23:30Z` | `updateapi` | `go vet ./...` | 0 | No diagnostics. |
| `12:42:57Z`–`12:43:31Z` | root | `node node_modules/vitest/vitest.mjs run` | 0 | 91 files passed; 1,348 tests passed and 31 were skipped, including 10/10 real-Chromium tests. |
| `12:28:49Z`–`12:28:58Z` | root | `node "C:\Program Files\nodejs\node_modules\npm\bin\npm-cli.js" run build` with `GOFLAGS=-buildvcs=false` | 0 | Vite transformed 95 modules; the Go build embedded 87 UI assets and the verified local FFmpeg payload. |
| `12:29:15Z`–`12:29:18Z` | root | `node node_modules/typescript/bin/tsc --noEmit` | 2 | Exactly one known baseline diagnostic: `tests/build-go.test.ts(7,34) TS7016` for the undeclared `scripts/build-go.mjs`; no other error was emitted. |
| `12:29:16Z`–`12:29:17Z` | root | strict targeted `tsc --noEmit --allowJs` over Task 1–12 build, signing, policy, workflow, bridge, and enrollment verifier TS/MJS inputs | 0 | No diagnostics; the full-run TS7016 baseline was not hidden or changed. |

Environment-only reruns were retained in the assessment:

- the user-level `npm` wrapper referenced an absent npm CLI, so the installed
  npm CLI was invoked directly;
- the first goserver attempt deliberately used an incomplete offline cache and
  stopped before assertions; the required modules were copied from an existing
  local read-only cache into `.task13-gomodcache`, then the exact suite passed
  offline;
- two build attempts reached the Go compiler but its VCS probe rejected the
  sandbox-owned worktree. The successful local build used
  `GOFLAGS=-buildvcs=false`; the application commit remains explicitly supplied
  through the repository build seam;
- the restricted Vitest attempt passed 89 non-browser files and was blocked
  only by `spawn EPERM` for two local Chromium launches. The allowed full run
  had one existing first-navigation timeout after 90 files and 1,347 tests had
  passed; the exact timed-out browser suite immediately passed 5/5. The final
  full rerun is the completion gate below.

None of these environment failures produced a stable product RED, so no product
or test source was changed for Task 13.

## Local unsigned enrollment package

The package was built only to exercise the enrollment seam. The build used:

- `APP_VERSION=0.4.12` and the source commit above;
- the Task 1 test-only P-256 root and epoch-1 signed bootstrap policy;
- a non-routable test update origin;
- a locally packaged FFmpeg pair whose bytes are real but whose external
  Authenticode result was supplied by a process seam;
- a Go build overlay that substituted only the local test FFmpeg pair, without
  modifying tracked files; and
- `go build -overlay .task13-artifacts/ffmpeg-overlay.json` with link flags
  resolved by `scripts/build-go.mjs`.

The local packager command
`node scripts/package-ffmpeg.mjs --input .task13-artifacts/extracted/ffmpeg.exe`
ran from `12:32:37Z` to `12:32:38Z` and exited 0. The package build ran from
`12:33:20Z` to `12:33:26Z` and exited 0. A content-addressed verification copy
was then checked with a local overlay test in the production
`internal/artifactinspect` package:

```text
go test -overlay .task13-artifacts/acceptance-overlay.json \
  ./internal/artifactinspect \
  -run '^TestTask13LocalUnsignedEnrollmentPackage$' -count=1 -v
```

That command ran from `12:33:57Z` to `12:34:00Z` and exited 0. It used the real
PE parser, exact covered-byte searches, real P-256 policy verification, bounded
ZIP parsing, real file snapshots, content-addressed naming, and retained-file
revalidation. Only the external Authenticode identity/status callback was fake.

| Evidence | Value |
| --- | --- |
| Actual outer EXE Windows signature status | `NotSigned` |
| Actual extracted FFmpeg Windows signature status | `NotSigned` |
| Executable SHA-256 | `1188781a2e264f053d4cd4f8c94e740f6790979916c93b0f0955ed342b7c08c9` |
| Executable size | `14,554,112` bytes |
| Authenticode-covered PE SHA-256 | `27f9eec3f9062dbab24a07519066db8e57412113774021d5ce55fb7b18b27843` |
| Embedded test root SPKI SHA-256 | `5cd252fb0ce8932436faf8ccd1040981b89ee4ad6b9fe9e2a2b7e71aacb27cd3` |
| Embedded test bootstrap policy SHA-256 | `205b8ea9bf7e79d55292d63a1266a4882ab01fa5edb3eb79421724ddb9265d0e` |
| Embedded bootstrap epoch | `1` |
| Embedded FFmpeg version | `9.0` |
| Embedded FFmpeg SHA-256 / size | `19247e960c50adcf107bc04e8a20435fd67d098e06b227d8772f0d1b8027e03c` / `6,209,536` bytes |
| Embedded FFmpeg archive SHA-256 | `6773544556e0f821b51d59eff4e167d0c6a199b91d606a95a5d3a55056c38839` |
| Test-only FFmpeg manifest SHA-256 (generated with simulated Authenticode evidence) | `2f464bbea0de8131939cc384b84720b9966bf6829aff7dbccba18fca5d93891d` |

The Authenticode metadata in the test-only FFmpeg manifest and the value
`Valid` in the local candidate evidence were generated from simulated external
results, not actual signatures. No self-signed root was installed into any
Windows certificate store.

## Windows security matrix

The focused root-package command ran from `12:34:33Z` to `12:34:43Z` and exited
0:

```text
go test . -count=1 -v \
  -run 'Test(CertificateLegalIdentity|InspectAuthenticode|VerifyAuthenticodePublisher|PublisherPolicy|TrustStore|AtomicTrustCache|Updater.*(Enrollment|Pending|Bridge|Stable|Channel|Expired)|Enrollment|UnknownAndCorruptPending)'
```

The closure command ran from `12:34:57Z` to `12:34:59Z` and exited 0:

```text
go test ./internal/updatepolicy ./internal/artifactinspect \
  ./internal/ffmpegseal ./internal/securefile ./cmd/artifact-inspector \
  -count=1 -v
```

| Required boundary | Direct local evidence |
| --- | --- |
| Same NaisNet legal identity, different leaf | Generated DER certificates with different renewal fields produced the same structured legal identity. |
| Changed organization ID | A generated code-signing DER with a changed `serialNumber` was rejected even when the injected Authenticode transport result said `Valid`. |
| `Valid` alone is insufficient | Missing DER, display-Subject-only data, malformed DER/Base64/JSON, wrong legal identity, and non-code-signing EKU were rejected. |
| RushRush bridge boundary | RushRush was accepted only for `legacy-rushrush` and exact `v0.4.11`; stable use and other tags were rejected. |
| Wrong tag/channel/hash | The shared production authorization matcher rejected each mutation independently, including a manifest hash mismatch. |
| Expired/malformed/bad signature | Expired policy, unknown/duplicate/trailing JSON, malformed ASN.1 ECDSA, and a signature from another P-256 root were rejected with bounded errors. |
| Rollback and interrupted cache | Lower epochs, corrupt current cache, concurrent lower-epoch overwrite, and interruption before rename failed closed while preserving prior evidence. |
| Pending provenance and ABA | Deleted or partial provenance, changed source/policy/artifact fingerprints, policy/mode substitution, schema deletion, floor interruption, and PE/source swap-restore were rejected. |
| Cleanup and diagnostics | All five Windows rechecks, helper/restart failures, corrupt pending state, and cleanup-failure combinations retained bounded primary result codes and excluded recognizable sensitive values. |
| Sealed artifact and FFmpeg | Real PE/ZIP/files verified content-addressed names, covered bytes, retained snapshots, exact pair publication, 40 MiB inflation bounds, junction rejection, and final revalidation. |

The tests used real generated X.509 DER, real canonical JSON and ECDSA
verification, real PE bytes, real ZIP bytes, real temporary files, Windows DACL
queries, junctions, retained handles, and atomic file operations. They simulated
only the Windows Authenticode command result and updater process/install/apply
callbacks. No production certificate or private key was used.

Ordinary Windows symlink creation remained unavailable even when retried with
local process permission, so six symlink-specific subcases stayed skipped. The
separate real junction, replacement, retained-handle, DACL, and path-swap tests
ran and passed. This environment limitation is deferred; it is not counted as
production symlink acceptance.

## API, policy, publication, and mirror integration

All integration tests used in-memory stores, injected transports, local
`httptest` servers, or temporary files. No external network request was made.

The API/publisher group and the executable routecheck ran from `12:35:36Z` to
`12:35:45Z` and exited 0:

```text
go test ./internal/config ./internal/service ./internal/httpapi \
  ./internal/release ./internal/publish ./cmd/routecheck -count=1 -v
go run ./cmd/routecheck
```

The routecheck printed `routecheck=ok cases=13` after verifying the local
publisher-policy signature and these public routes:

| User-Agent | Result |
| --- | --- |
| `bilibili-live-gift-panel/0.4.7` | exact legacy route when active; controlled 503 without reading a pointer when inactive |
| `bilibili-live-gift-panel/0.4.9` | stable 200 |
| `bilibili-live-gift-panel/0.4.10` | stable 200 |
| `bilibili-live-gift-panel/0.4.11` | stable 200 |
| `bilibili-live-gift-panel/0.4.12` | stable 200 |

Missing, duplicate, whitespace, wrong-product, oversized, prerelease,
development, `0.4.8`, later-unreviewed, and unknown-major values returned the
controlled HTTP 400 code without a release-service read. Responses asserted
`Vary: User-Agent`, `Cache-Control: private, no-store`, and the exact channel
header. The policy GET/HEAD endpoint returned the complete bounded envelope;
the routecheck verified its real Task 1 ECDSA signature locally.

Schema 1 remained stable-only. Schema 2 required an exact channel; legacy
accepted only `v0.4.11`. Stable and legacy pointer reads, caches, refresh locks,
object keys, writes, and rollback paths remained independent.

The mirror group ran from `12:36:00Z` to `12:36:12Z` and exited 0:

```text
go test ./internal/mirror ./cmd/mirror -count=1 -v
```

It covered GitHub `Latest` and exact `ByTag` discovery, strict weak/strong ETag
grammar, 200/304 behavior, cross-channel state rejection, schema isolation,
four-asset closure, resume binding, bounded downloads, content/hash checks,
state replacement/rollback, cleanup, cancellation, exact snapshot publication,
and dry-run without COS or state mutation. Publisher tests proved bridge cannot
mutate stable and stable cannot mutate legacy or use the bridge tag.

The trust-policy/bundle group ran from `12:36:01Z` to `12:36:24Z` and exited 0:

```text
go test ./internal/trustpolicy ./internal/trustpolicy/bundlefs \
  ./cmd/trustpolicy -count=1 -v
```

It exercised real local ECDSA and canonical bytes, fake KMS adapter responses,
provider error redaction, exact epoch transitions, create-only output, commit
marker last, close failures, crash checkpoints, Windows private DACLs, retained
file/directory handles, concurrent bundle ownership, import, and marker/hash
readback. No real KMS credential or request was used.

## Workflow and tooling semantics

The focused Vitest command ran from `12:37:20Z` to `12:37:53Z` and exited 0:

```text
node node_modules/vitest/vitest.mjs run \
  tests/build-go.test.ts tests/sign-evsign.test.ts \
  tests/publish-trust-policy.test.ts tests/release-workflow.test.ts \
  tests/bridge-release-inputs.test.ts tests/verify-enrollment-build.test.ts \
  tests/ffmpeg-component-assets.test.ts tests/bounded-github-asset.test.ts \
  tests/update-api-deploy.test.ts
```

Result: 9 files and 262 tests passed. The suite executed workflow PowerShell
scriptblocks and local fakes; it did not dispatch workflows or contact external
services. It covered:

- stable prepare/publish as two isolated capabilities with candidate
  provenance, readback equality, sealed exact assets, and no signer in the
  publisher stage;
- the exact RushRush `v0.4.11` non-latest bridge and its inability to change
  stable, legacy pointers, KMS, or ordinary latest state;
- policy rotation jobs that cannot build or sign an executable, and ordinary
  release jobs that cannot call KMS or legacy publication;
- enrollment evidence cross-binding the content-addressed EXE, both policies,
  exact NaisNet identities, sidecars, and FFmpeg closure;
- exact asset closure, retained tooling, fixed FFmpeg component verification,
  junction rejection, dry-run install/rollback, and bounded downloads; and
- stable reviewed history containing exactly the required recent versions for
  first enrollment.

## Tested, simulated, and deferred

**Tested directly:** repository code; test-only P-256 signatures; strict JSON;
generated certificate DER parsing; real PE covered-byte binding; real ZIP and
filesystem operations; Windows DACLs and junctions; cache rollback; API headers,
status, and routing; mirror ETags/state; workflow YAML and executable local
script semantics; diagnostic bounds and redaction.

**Simulated explicitly:** external Authenticode `Valid` status and certificate
return; EVSign process success/failure; updater installer/restart/apply process
callbacks; KMS `GetPublicKey`/sign responses and credentials; COS/GitHub HTTP
adapters and workflow contexts. The outer EXE and extracted FFmpeg were each
independently checked as actual `NotSigned` before the simulated candidate
acceptance.

**Deferred to approval-gated Task 14:** production KMS provisioning and dual
SPKI review; production bootstrap/authorization policy signatures; real
NaisNet- and RushRush-signed public packages; certificate-chain/timestamp
inspection; actual `v0.4.7 -> v0.4.11 -> v0.4.12` Windows upgrade acceptance;
GitHub tag/Release/latest changes; COS immutable objects and pointer changes;
production mirror/server changes; observation windows and rollback exercise.

Task 14 must not treat this document's test-only digests or simulated status as
production trust material.

## Final completion gate

The completion-gate reruns were performed after the evidence draft:

| UTC interval | Command | Exit | Result |
| --- | --- | ---: | --- |
| `12:41:38Z`–`12:42:21Z` | `go test ./... -count=1` in `goserver` | 0 | All 26 reported packages passed. |
| `12:41:38Z`–`12:42:05Z` | `go test ./... -count=1` in `updateapi` | 0 | All 14 reported packages passed. |
| `12:41:39Z`–`12:41:51Z` | `go vet ./...` in `updateapi` | 0 | No diagnostics. |
| `12:42:37Z`–`12:42:47Z` | direct npm CLI `run build` with `GOFLAGS=-buildvcs=false` | 0 | 95 UI modules, 87 embedded assets, verified FFmpeg, and local Go EXE build. |
| `12:42:37Z`–`12:42:38Z` | targeted strict `tsc --noEmit --allowJs` | 0 | No diagnostics. |
| `12:42:38Z`–`12:42:41Z` | full `tsc --noEmit` | 2 | Only the unchanged `tests/build-go.test.ts(7,34) TS7016` baseline. |
| `12:42:57Z`–`12:43:31Z` | full Vitest with local Chromium permission | 0 | 91 files, 1,348 passed, 31 skipped; both browser suites passed 10/10. |
| `12:44Z`–`12:45Z` | `git diff --cached --check` plus `git diff --cached --name-only` | 0 | The staged set contained only `docs/verification/signer-trust-rotation.md`. |

The final diff, privacy, and exact staged-file checks are recorded by the Task
13 evidence commit. Only this document is eligible for staging; all caches,
unsigned packages, local seams, and prior task reports remain untracked or
ignored.

Main-controller review is requested against
`docs/superpowers/specs/2026-08-30-version-aware-signer-trust-rotation-design.md`
and
`docs/superpowers/plans/2026-08-31-version-aware-signer-trust-rotation.md`.
