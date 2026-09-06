# v0.4.13 streamlined stable release verification

## Scope

- Verification UTC: `2026-09-03T15:53:51Z`
- Reviewed implementation range: `5494baa2c611e07987bd856ccca66e5fcdadf99a..861dcbd8d5e4b8c0d613a7e46e4e9f3a073f402e`
- `0fcd235` updates stale v0.4.10/v0.4.12 changelog UI assertions; `19beda5` and `861dcbd` fix the independent review findings described below.
- Production rollout, tag creation, GitHub Release mutation, COS pointer mutation, and legacy bridge activation were not performed by this verification.

## Local regression

- `go -C goserver test ./... -count=1`: passed all packages.
- `go -C updateapi test ./... -count=1`: passed all packages.
- Final full non-browser Vitest run: `1504 passed`, `31 skipped`, `98` files passed.
- The earlier all-suite run had two stale v0.4.13 changelog assertions; they were corrected before the final run. Two Chromium suites initially could not spawn inside the sandbox.
- `tests/wizard.test.ts` after correction: `180 passed`, `31 skipped`.
- `tests/hosted-admin-layout-browser.test.ts` outside the sandbox: `5 passed`.
- `tests/hosted-auth-layout-browser.test.ts` outside the sandbox: four tests passed; one unrelated production-DOM layout case twice timed out while navigating to its local Vite harness. The timeout is recorded, not represented as a product pass.
- `node node_modules/typescript/bin/tsc --noEmit`: passed.

## Windows package acceptance

The exact `861dcbd8d5e4b8c0d613a7e46e4e9f3a073f402e` archive was built in a separate clean directory with the populated `dist/ffmpeg-component` and `dist/release-ffmpeg-sealed` trees. The embedded payload used the reviewed production P-256 SPKI and its locally verified epoch-1 bootstrap policy. The update URL was the non-production `https://updates.example.test` size-acceptance value; production CI supplies the protected reviewed URL.

- FFmpeg version: `9.0`
- FFmpeg Authenticode: valid
- FFmpeg executable size: `6,219,976` bytes
- FFmpeg SHA-256: `bf843d4aa7201fb3d49ba23ca396334fd7ce8dda0117073b59c2e353fa745dd3`
- UI manifest files: `87`
- UI manifest bytes: `836,926`
- Forbidden embedded `ffmpeg-component` / `release-ffmpeg-sealed` paths: `0`
- Unsigned EXE size: `14,590,464` bytes (`13.91 MiB`), below the `18 MiB` gate
- Unsigned EXE SHA-256: `184d1c3ee2878299a56cd138bdcb3f463372af7dd41a34dcde045993683f5d50`
- Runtime launch reached `/health` and reported version `0.4.13`. The repository smoke wrapper then rejected the release build because that wrapper intentionally expects its CI sentinel version `0.0.0`; this is not recorded as a complete route smoke.

## Policy compatibility

- Exact epoch-3 candidate validation passed with `candidate_epoch=3` and `expected_previous_epoch=2`.
- Post-enrollment NaisNet v0.4.13 identity authorization passed.
- A changed organization ID and a wrong exact artifact hash were rejected.
- v0.4.12 remained exact-artifact-hash-only.
- The persisted epoch-3 rollback floor rejected epoch 2 after restart.

Commands covered the candidate CLI plus the focused `artifactinspect` enrollment tests and `TestTrustStoreRejectsLowerEpochRollbackAfterRestart`.

## Release transaction and signer-change behavior

- Stable Release planner/transaction: `17 passed`, covering absent, empty, partial, complete, conflicting, duplicate, live HTTP-shape, pagination-bound, and redirect-host states.
- A complete draft produced no create or upload call; a partial draft uploaded only the missing asset.
- Final checks use numeric Release ID, published tag, Latest, and a full asset re-download/hash pass.
- The live adapter contains no DELETE operation and never replaces an existing asset.
- Publisher-change request generator: `15 passed`, including closed schema, canonical fields, bounded identity data, deterministic bytes, and create-only output.
- Windows certificate inspection tests passed and expose only DER SHA-256 plus structured identity.
- Workflow contract tests prove an unexpected observed identity uploads only the bounded request and fails before candidate sealing/output.

## Requirement review

- Same NaisNet legal identity: one `operation=release` workflow dispatch drives prepare, protected signing, and publication through same-run public outputs.
- Unknown legal identity: fails closed and requires an explicitly reviewed higher root-signed policy epoch.
- Trust remains exact legal identity plus signed policy; EVSign, a public CA, or `X-Cert` is not a trust anchor.
- Stable draft handling is numeric-ID based, resumable, upload-only-missing, and delete-free.
- v0.4.13 is the only newly added update API client bucket; v0.4.14 remains rejected.
- v0.4.12 Release and epoch-2 immutable evidence remain unchanged.
- The RushRush bridge stays inactive and is deferred until a separate amendment after the seven-day v0.4.13 observation window.

The independent review initially found three release blockers/gaps. All were reproduced or checked before correction:

- Real primary-plus-bridge policies no longer double-count a primary authorization; the focused tests now use the exact two-rule shape.
- The active stable primary is derived from the verified current signed policy and bound through EVSign configuration, actual certificate inspection, candidate verification, and final authorization. A future explicitly authorized primary no longer requires another code change.
- The immutable epoch-1 bootstrap is verified against its fixed `v0.4.12` enrollment scope instead of the new release tag; v0.4.13 and future releases use the current signed policy for their artifact authorization.
- Zero-state draft creation and the live GitHub HTTP adapter now have executable coverage, including five-page bounds and credential stripping on the release-asset redirect.

The final focused re-review of `19beda5..861dcbd` reported no remaining Critical or Important issue and marked the implementation ready.

## Remaining production gates

1. Review and push the implementation commits.
2. Deploy the v0.4.13 update API route with legacy inactive.
3. Sign, publish, and advance epoch 3.
4. Create the exact v0.4.13 tag.
5. Dispatch the one-trigger stable release and verify GitHub Latest plus domestic COS delivery.
6. Start the seven-day observation window before any bridge work.
