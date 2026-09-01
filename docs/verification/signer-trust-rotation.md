# Signer trust rotation final local verification

## Result and exact scope

This record covers the user-authorized narrow bridge readiness module-closure
cycle at product commit `5fb99d80792e14f539fe073745b6b35d102a23da` on
Windows amd64 on 2026-09-01 UTC. Node was `v24.18.0`. The prior final-review Go,
Go-vet, and desktop-build evidence at `22d59058fff9d35bd7cf9ce1e390d7bf589ae520`
remains unchanged; this cycle changed only the bridge workflow and its Vitest
regression coverage and did not rerun those Go/build gates.

The focused bridge readiness and release-workflow tests passed 70/70. The exact
current-product-HEAD full Vitest sandbox run passed all 89 non-browser files and
1,335 tests, skipped the 10 browser tests after Chromium launch was denied with
`spawn EPERM`, and retained the ordinary 31 skips. A permissioned rerun of only
the two browser files passed the administrator suite 5/5 and the Hosted-auth
suite 4/5; the existing first multi-state Hosted-auth case again exceeded its
40-second bound. Combining the full non-browser run with that browser rerun
gives 1,344 passing tests, 1 failed test, and 31 skipped. This unrelated browser
limitation is recorded below and is not represented as a green full Vitest run.

This is local implementation evidence, not production acceptance. No workflow
was dispatched. No EVSign, RFC3161, KMS, COS, GitHub API, tag, Release, server,
credential, certificate-store, or update-pointer mutation occurred. No
production signing or root-rotation claim is made.

## Final-review findings closed locally

### Bridge readiness module closure

- The workflow no longer runs a lone copied `bridge-release-inputs.mjs`.
  `BRIDGE_READINESS_SCRIPT_PATH` points into the private
  `RUNNER_TEMP/release-tools/scripts` copy of the complete reviewed tooling
  checkout, which survives the later exact-target checkout.
- The reviewed tooling commit binding is unchanged: the complete private copy
  is still made from `release-tools` before target checkout, and no target
  checkout module or unreviewed `PATH` entry is used.
- A behavior test parses the actual workflow, executes its copy/path layout in
  a fresh directory, imports the isolated runtime module, and performs a
  representative readiness validation. Removing
  `publisher-policy-release-contract.mjs` produces `ERR_MODULE_NOT_FOUND`.
- The setup/import tests assert no unexpected stdout or stderr, and readiness
  continues to emit only its existing machine summary.

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

## Exact current-product-HEAD commands

| Working directory | Command | Exit | Current result |
| --- | --- | ---: | --- |
| repository root | `node node_modules/vitest/vitest.mjs run tests/bridge-release-inputs.test.ts --reporter=dot --minWorkers=1 --maxWorkers=1` | 0 | 1 file, 11 tests passed, including isolated import, representative readiness, and missing-sibling mutation. |
| repository root | `node node_modules/vitest/vitest.mjs run tests/release-workflow.test.ts --reporter=dot --minWorkers=2 --maxWorkers=2` | 0 | 1 file, 59 tests passed. |
| repository root | `node node_modules/vitest/vitest.mjs run --reporter=dot --minWorkers=2 --maxWorkers=2` in the sandbox | 1 | 89 files and 1,335 tests passed; both browser files could not launch Chromium (`spawn EPERM`); 41 tests were skipped, including those 10 browser cases. |
| repository root | `node node_modules/vitest/vitest.mjs run tests/hosted-admin-layout-browser.test.ts tests/hosted-auth-layout-browser.test.ts --reporter=dot --minWorkers=1 --maxWorkers=1` with local Chromium permission | 1 | Administrator browser 5/5 and Hosted-auth 4/5 passed; only the existing first Hosted-auth multi-state case timed out at 40 seconds. |
| repository root | `node --check scripts/bridge-release-inputs.mjs` and `node --check scripts/publisher-policy-release-contract.mjs` | 0 | No syntax diagnostics. |
| repository root | YAML parse plus PowerShell AST parse of `Build reviewed bridge security tools` and `Verify reviewed bridge readiness` | 0 | Workflow YAML and both relevant `pwsh` steps parsed without diagnostics. |
| repository root | `node node_modules/typescript/bin/tsc --noEmit --allowJs` | 0 | No diagnostics across the configured program with JavaScript module resolution enabled. |
| repository root | `node node_modules/typescript/bin/tsc --noEmit` | 1 | Exactly the existing baseline `tests/build-go.test.ts(7,59) TS7016` for undeclared `scripts/build-go.mjs`; no other diagnostic. |

The permissioned two-file snapshot is the authoritative current-HEAD browser
result. Go tests, Go vet, and the desktop build were intentionally not rerun for
this workflow/test-only closure cycle; their exact prior-HEAD evidence remains
in the preceding verification commit and was not relabeled as newly tested.

## Tested, simulated, and deferred boundaries

**Tested directly:** workflow YAML parsing, relevant PowerShell syntax, the
actual workflow-derived private tooling copy/path layout, isolated ESM import,
representative bridge readiness validation, missing-sibling mutation, and
executable PowerShell handoff gates; target/signer/publisher job capability
separation; poisoning rejection;
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
