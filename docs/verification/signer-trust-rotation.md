# Signer trust rotation final local verification

## Result and exact scope

This record covers the final-review fix wave at source commit
`22d59058fff9d35bd7cf9ce1e390d7bf589ae520` on Windows amd64 on
2026-09-01 UTC. Go was `go1.26.5 windows/amd64`; Node was `v24.18.0`.
Go/npm caches were worktree-local, Go dependency lookup was disabled with
`GOPROXY=off`, and the build used `GOFLAGS=-buildvcs=false` because this
sandbox-owned linked worktree cannot satisfy Go's VCS ownership probe.

The signer-rotation code, focused security tests, both Go repositories, Go vet,
the desktop build, and the JavaScript-aware TypeScript check passed. The exact
full Vitest snapshot passed 90 of 91 files and 1,343 tests with 31 skipped. Its
only failure was the unrelated existing real-Chromium Hosted-auth layout case:
the first harness navigation/test exceeded its 30/40-second bound. That same
case failed in two isolated retries; the other four tests in that browser file
and all five tests in the administrator browser file passed. This environmental
browser limitation is recorded below and is not represented as a green full
Vitest run.

This is local implementation evidence, not production acceptance. No workflow
was dispatched. No EVSign, RFC3161, KMS, COS, GitHub API, tag, Release, server,
credential, certificate-store, or update-pointer mutation occurred. No
production signing or root-rotation claim is made.

## Final-review findings closed locally

### Protected signer isolation

- Stable and bridge target code runs only in unprivileged build jobs with
  read-only repository permission, no protected environment, and no EVSign
  variable or secret.
- Each build uploads a closed content-addressed unsigned handoff. A fresh
  protected signing runner downloads and byte-verifies that handoff before it
  checks out/builds reviewed tools; it executes no target code. Publication is
  a third signer-free runner.
- Executed workflow tests inject extra `PATH`/`GITHUB_ENV`/tool-shaped files
  into both handoffs and prove rejection before reviewed-tool checkout.
- `scripts/sign-evsign.mjs` has one fixed endpoint,
  `https://api.evsign.cn/v1`; `EVSIGN_ENDPOINT` is rejected before a request.
- The Windows Authenticode helper obtains the system directory with
  `GetSystemDirectoryW`, selects the absolute system PowerShell, ignores
  poisoned `WINDIR`, `SystemRoot`, and `PATH`, and fails closed when that system
  binary is unavailable.

### One immutable policy Release contract

- Task 9 publishes and reads back exactly
  `gift-panel-publisher-policy.json`,
  `gift-panel-publisher-policy.audit.json`, and
  `gift-panel-publisher-policy.commit.json`, each with the shared canonical
  name, media type, size cap, size, digest, and exact bytes.
- `trustpolicy verify-bundle` now emits the exact retained commit-marker bytes,
  Base64, size, and SHA-256 as well as the parsed marker.
- The bridge uses the same contract to map those remote names only to local
  `policy.json`, `audit.json`, and `commit.json` before retained bundle import.
- A real in-process Task 9 publisher adapter → exact Release metadata/bytes →
  bridge local mapping/readiness integration test passed.

### Bootstrap versus final authorization

- The embedded epoch-1 bootstrap is verified independently from a higher
  external authorization policy.
- The higher policy must advance bootstrap and bind the actual v0.4.12 EXE
  SHA-256 plus actual NaisNet identity through shared Go `AuthorizeAt`.
- Bridge readiness and final artifact evidence bind both epochs/hashes, stable
  and policy Release IDs, audit digest, commit digest, KMS request ID, and trust
  attestation digest.
- Reusing bootstrap as authorization, a non-advancing epoch, a wrong candidate
  hash, a moved policy Release, and changed policy/audit/commit bytes fail.
- The final bridge inspector checks that only root/bootstrap are embedded; the
  exact-final-hash authorization policy remains external.

### COS capability and remaining review items

- Every Go COS client is constructed with one closed mutable-pointer
  capability: none, stable, or legacy RushRush. The update API uses none;
  stable and legacy publishers receive only their own exact key.
- Real-client/fake-HTTP tests prove exact legacy publication succeeds and that
  read-only, cross-pointer, and arbitrary writes fail before HTTP.
- `artifactinspect` itself enforces a 40 MiB per-entry and total uncompressed
  ZIP limit.
- A direct Go-compiler failure-path regression proves all failed attempts
  sanitize both trust inputs before the final bounded error.
- The spec documents the exact deployed v1 wire fields and
  `ecdsa-p256-sha256`; schema-v2 fields require a coordinated migration.
- Root rotation is explicitly a future protocol sketch/non-goal. Task 14 may
  add only separately approved public SPKI/candidate/attestation/evidence paths
  and cannot change product behavior.

## Exact current-HEAD commands

| Working directory | Command | Exit | Current result |
| --- | --- | ---: | --- |
| `goserver` | `go test ./... -count=1` | 0 | All 26 reported packages passed. |
| `updateapi` | `go test ./... -count=1` | 0 | All 15 reported packages completed (14 test-bearing plus `cmd/release-closure` with no tests). |
| `updateapi` | `go vet ./...` | 0 | No diagnostics. |
| repository root | `node node_modules/vitest/vitest.mjs run tests/sign-evsign.test.ts tests/bridge-release-inputs.test.ts tests/publish-trust-policy.test.ts tests/release-workflow.test.ts` | 0 | 4 files, 166 tests passed. |
| repository root | `node node_modules/vitest/vitest.mjs run` with local headless-Chromium permission | 1 | 90 files passed, 1 browser file failed; 1,343 tests passed, 1 failed, 31 skipped. The failure was only `hosted-auth-layout-browser` first-harness navigation/test timeout. |
| repository root | direct npm CLI `run build` with local caches and `GOFLAGS=-buildvcs=false` | 0 | Vite transformed 95 modules; the Go build embedded 87 UI assets and built the local dev EXE after verifying FFmpeg 9.0. |
| repository root | `node node_modules/typescript/bin/tsc --noEmit --allowJs` | 0 | No diagnostics across the configured program with JavaScript module resolution enabled. |
| repository root | `node node_modules/typescript/bin/tsc --noEmit` | 1 | Exactly the existing baseline `tests/build-go.test.ts(7,59) TS7016` for undeclared `scripts/build-go.mjs`; no other diagnostic. |

The initial sandbox-only full Vitest attempt also passed every non-browser file
but could not spawn either Chromium process (`spawn EPERM`). The permissioned
snapshot above is the authoritative browser result. The administrator browser
suite passed 5/5; the Hosted-auth browser suite passed 4/5 and repeatedly timed
out only in its first multi-state navigation case.

## Tested, simulated, and deferred boundaries

**Tested directly:** workflow YAML parsing and executable PowerShell handoff
gates; target/signer/publisher job capability separation; poisoning rejection;
fixed EVSign endpoint selection; Windows system-directory PowerShell selection;
strict policy/audit/commit bytes; real local ECDSA policy verification; shared
`AuthorizeAt`; actual local PE/ZIP/filesystem operations; 40 MiB inflation
bounds; Go COS request signing against fake HTTP; stable/legacy/read-only
pointer capabilities; full Go repositories; Go vet; desktop build; focused and
full Vitest snapshots; TypeScript contracts.

**Simulated explicitly:** EVSign HTTP responses; Authenticode `Valid` and
certificate-return results in artifact-policy tests; KMS responses/credentials;
COS and GitHub transports; Actions artifacts and workflow contexts; actual
NaisNet/RushRush signed EXEs. Tests used generated/test-only public material and
never installed a certificate or invoked a production provider.

**Deferred to separately approved Task 14:** production KMS/root provisioning
and dual review; epoch-1 bootstrap signing/publication; higher exact-v0.4.12
authorization-policy signing/publication; real NaisNet and RushRush signing;
real certificate chain/timestamp inspection; GitHub tags/Releases/latest;
immutable COS objects and pointers; server/mirror changes; actual
`v0.4.7 → v0.4.11 → v0.4.12` Windows acceptance; observation windows and
rollback exercises. Root-key rotation is not a Task 14 capability and requires
a new approved design.

## Remaining local concern

The one persistent Hosted-auth browser navigation timeout is outside the signer
rotation change set and reproduced without a signer-specific assertion. It is
not waived as passing: the full Vitest command remains exit 1 in this record.
All signer-policy/workflow browser-independent tests and the other real browser
suite passed. Production rollout remains deferred regardless of this local
browser limitation.
