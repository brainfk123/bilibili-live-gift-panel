# Attribute Editor Final-Review Fix Report

Date: 2026-08-14
Baseline: `e5e4903`
Scope: final-review findings for attribute-editor lease safety, authoritative open state, transactional save behavior, blank numeric validation, and guide lifecycle.

## Outcome

All three Important and both Minor review findings were reproduced with focused regressions and fixed. The frontend now times out and recovers expired edit leases, opens existing editors from a post-lease authoritative snapshot resolved by stable attribute ID, rebases both existing- and new-attribute saves on a strict current snapshot, commits editor changes to live/cache state only after persistence succeeds, rejects blank random endpoints/caps, and keeps the active guide until lease acquisition succeeds.

No Go production code or protocol changes were required. The existing backend already returns 404 for expired/missing renewal tokens and accepts a fresh POST acquisition, so the recovery protocol remains client-side.

## Finding 1 — bounded renewal and expired-token recovery

### RED

Command:

```text
npm test -- tests/attribute-edit-lease.test.ts --reporter=dot
```

Initial result: 2 failed / 8 passed (10 total).

Exact failure signals:

- Hung PUT had no owned `AbortSignal`: `hungSignal?.aborted` expected `false`, received `undefined`.
- After the simulated 15-second server TTL elapsed, the client kept PUTing the immutable stale token instead of POSTing a replacement lease: `creates` expected `2`, received `1`.

The model-backed fixture removes the active token at actual TTL expiry, so it cannot incorrectly treat a second PUT with the same token as recovery.

### GREEN

Implementation in `src/ui/config/attribute-edit-lease.ts`:

- Added a four-second default request timeout, configurable in tests.
- The owned `AbortController` and timer cover both `fetch` and successful response-body parsing for POST/PUT.
- A renewal 404 triggers a fresh POST acquisition and atomically replaces the session's current token only after a valid response.
- The public token is a getter over the current token, so release uses the latest valid identity.
- Release synchronously marks the session released, clears the one heartbeat and at-most-one retry, aborts an active request, removes the one unload listener, snapshots the latest token, and starts exactly one bounded keepalive DELETE.
- Renewal errors are contained inside `renew`; released requests neither report health nor re-arm retries.

Focused result after implementation:

```text
tests/attribute-edit-lease.test.ts: 10 passed
```

Supplemental response-body and not-found health coverage was then added and passed:

```text
npm test -- tests/attribute-edit-lease.test.ts --reporter=dot
1 file passed; 12 tests passed
```

The response-body test returns headers immediately but stalls `json()`, proves the same request timeout aborts body consumption, and proves release prevents stale retry re-arming. The health test proves a definite PUT 404 reports `retrying` before replacement POST acquisition and returns to `healthy` only after a valid new token is installed.

## Finding 2 — acquire, then refresh and re-resolve by stable ID

### RED

The focused editor command below included this finding and initially failed:

```text
npm test -- tests/wizard.test.ts -t "acquires the lease before|releases and aborts|keeps live and cached|keeps failed attribute|keeps the active spotlight|blocks saving when the random" --reporter=dot
```

Initial aggregate result: 9 failed / 179 skipped.

Finding-specific failures:

- Call order contained only `POST lease`; the required `GET config` never occurred.
- The editor mounted the pre-lease value rather than the refreshed `01:01:01` value.
- Refresh failure and refreshed-state missing-ID cases sent no DELETE and therefore did not release the acquired lease.

### GREEN

Implementation in `src/ui/config/config.ts` and `src/storage.ts`:

- Opening invalidates an already-started soft poll and blocks new soft polls while `attributeEditorOpening` is true.
- A missing stable ID is persisted transactionally before lease acquisition.
- Existing editors acquire the lease first, then perform a strict configuration GET through `refreshStateFromServer`.
- The refreshed attribute is required to have exactly one match for the stable ID; its current index and `original` snapshot are replaced with the authoritative objects.
- A strict GET error, missing target, or duplicate target releases the acquired lease and aborts mounting.
- The deterministic regression prepends a decoy attribute, updates the target value, asserts `POST lease` precedes `GET config`, and proves mounting follows the stable ID rather than the old array index.

Focused final result for all editor findings:

```text
1 file passed; 9 tests passed; 179 skipped
```

## Finding 3 — detached transactional persistence

### RED

Finding-specific failures from the same focused RED run:

- A failed generated-ID PATCH left the ID in cached/live state and could proceed toward lease creation.
- A failed final editor PATCH left the live/cached attribute renamed to `倒计时` with value `60`; the related rules were also locally rewritten, and Cancel could not discard those mutations.

### GREEN

Implementation:

- Added `saveStateTransaction(candidate)` in `src/storage.ts`. It normalizes and snapshots a detached candidate, waits on the existing persistence queue, persists first, and changes `cachedState` only after success.
- The internal queue tail absorbs the already-observed transaction rejection so a caller-handled failed transaction cannot produce an unhandled rejection; the returned transaction still rejects to its caller.
- Missing-ID persistence now edits a detached candidate and assigns the committed state to the long-lived UI object only after PATCH succeeds.
- Final attribute save builds all attribute/rule/timer/scene/activity/catalog/tutorial mutations on one detached candidate. It persists that candidate and commits to the live state only after success.
- Failed save leaves the editor and lease active, preserves original live/cache data, and allows Cancel to close without applying the draft.

The focused run briefly exposed two unhandled internal queue-tail rejections despite 8/9 behavioral tests passing. Making only the stored queue tail non-rejecting removed those unhandled errors without hiding the caller-visible transaction failure. Final focused result was 9/9 clean.

## Finding 4 — blank random numeric fields

### RED

Three table-driven DOM cases failed because whitespace was converted with `Number(...)` to zero:

- cleared minimum: expected `随机范围必须使用整数`, received no error;
- cleared maximum: expected `随机范围必须使用整数`, received no error;
- enabled cleared cap: expected `随机范围的上限必须是有效数字`, received no error.

All three also asserted that save sends no configuration PATCH and leaves the modal open.

### GREEN

- Raw min, max, and enabled-cap strings are trimmed before numeric conversion; blank input becomes `NaN`, not zero.
- `validateQuickGiftRuleDraft` now rejects a present non-finite cap with a stable message before formula construction.
- The invalid draft remains attached to the editor, so clicking Save cannot bypass inline validation.

All three DOM cases pass.

## Finding 5 — preserve the active guide until acquisition succeeds

### RED

The regression uses a deferred lease POST and asserts the exact spotlight guide remains mounted while acquisition is pending and after a 409 response. Before the fix, the guide was synchronously disposed when opening began.

### GREEN

Guide disposal now occurs only after ID persistence, lease acquisition, strict refresh, and stable-ID lookup all succeed. Acquisition failure keeps the same guide object and leaves the editor closed. The deferred-POST regression passes.

## Independent review follow-up

An independent spec/concurrency review was run after the five requested findings first reached green. It found adjacent failure modes in the same lease and persistence seams. Each was reproduced before its fix.

### Lease health during recovery and opening

RED:

```text
npm test -- tests/attribute-edit-lease.test.ts -t "reports retrying throughout not-found" --reporter=dot
1 failed / 11 skipped
```

Exact signal: expected the first post-404 health event to be `retrying`, received `undefined`.

The opening-path regression deferred the strict configuration GET until a heartbeat failed. Before the fix the health callback ran before the warning element existed, the state was discarded, and the mounted warning was hidden (`expected false, received true`).

GREEN:

- PUT 404 reports `retrying` before reacquisition begins.
- The editor always records the current lease health even before its warning element mounts, then initializes visibility from that state.

### Authoritative pre-save rebase

RED:

```text
npm test -- tests/wizard.test.ts -t "rebases save|keeps the editor and lease when|mounts with the retry warning" --reporter=dot
4 failed / 188 skipped
```

Exact save signals: the expected second configuration GET was absent (`expected 2, received 1`); therefore a peer attribute update/reorder could be overwritten. Pre-save GET failure and missing-target cases also lacked the required keep-open/no-PATCH behavior.

The first new-attribute regression independently failed with `config GET expected 1, received 0`, proving that only rebasing existing attributes left new-attribute creation on stale state.

GREEN:

- Both existing and new saves perform a strict GET while the existing attribute's lease remains held.
- Existing saves re-resolve the target by stable ID, not opening-time array index.
- The candidate starts from the fresh array/order and preserves an unfrozen peer's updated value.
- A concurrent persisted rename of the same stable ID is treated as authoritative for rewriting gift-rule, timer-rule, scene, and activity references to the editor's final name.
- Pre-save GET failure or missing/duplicate target sends no PATCH, keeps the editor and lease open, and allows a safe retry/cancel.
- New-attribute creation refreshes first, rechecks duplicate names against fresh state, and appends without reverting peers.

Focused stable-ID rename/reorder result:

```text
npm test -- tests/wizard.test.ts -t "rebases save by stable ID" --reporter=dot
1 passed / 192 skipped
```

### Transaction publication and reset ordering

The detached transaction initially prevented failed PATCHes from publishing drafts, but independent review exercised queue interleavings as well.

RED signatures:

- Pending transaction followed by ordinary save: `loadState().roomId` expected `later`, received `transaction`.
- Two queued transactions: PATCH count expected `1`, received `2`; the stale second transaction wrote before noticing its conflict.
- Deferred ordinary save followed by reset/re-save: methods expected `[PATCH, DELETE, PATCH]`, received `[PATCH, DELETE]` because the older PATCH repopulated snapshots before DELETE.

GREEN architecture:

- Every cache publication goes through a monotonic in-memory revision.
- `saveStateTransaction` captures its invocation revision and rejects before PATCH if already superseded, then checks again after awaited persistence before publishing. The stored queue tail remains non-rejecting, so caller-visible conflicts do not create unhandled rejections or poison later saves.
- An ordinary save that occurs during a transaction remains authoritative in cache and is queued to restore the server after the conflicting transaction write.
- Reset clears persisted snapshots and forced fields inside its queued operation, after older writes drain and a successful DELETE (or the queued no-fetch path), so re-saving identical data cannot be skipped against an empty server.

Focused final storage results:

```text
npm test -- tests/storage.test.ts -t "superseded" --reporter=dot
2 passed / 30 skipped

npm test -- tests/storage.test.ts --reporter=dot
33 passed
```

Independent final verdict: **clean** — no remaining P1/P2 in the current diff for the five review findings.

## Legacy fixture fallout

The first complete affected-file run found seven legacy test mocks that returned HTTP 204 for every post-lease configuration GET, plus one stale-poll fixture that reused an already-consumed `Response`. These were test-contract mismatches introduced by the newly required strict refresh, not product regressions.

After narrowly teaching those fixtures to return the current configuration (and the stale-poll case to return a fresh response on its second GET), the affected suites passed:

```text
npm test -- tests/attribute-edit-lease.test.ts tests/wizard.test.ts --reporter=dot
2 files passed; 167 tests passed; 31 skipped
```

The subsequently added stalled-body test is included in the later feature/full-suite counts.

## Verification evidence

### Frontend

```text
npm run typecheck
PASS — tsc --noEmit, no diagnostics

npm test -- tests/storage.test.ts tests/attribute-edit-lease.test.ts tests/wizard.test.ts --reporter=dot
3 files passed; 207 tests passed; 31 skipped (238 total)

npm test -- --reporter=dot
45 files passed; 577 tests passed; 31 skipped (608 total)
```

### Backend protocol/runtime assumptions

No backend product file changed. The existing lease and frozen-runtime contracts were nevertheless exercised repeatedly:

```text
cd goserver
go test ./... -run 'Test(AttributeEditLease|RegisterAttributeEditLeaseRouteSharesCoordinatorWithRuntime|ApplyGiftEventSkipsOnlyFrozenAttribute|BackgroundRuntimeFrozenTimerDoesNotCatchUp)' -count=20
PASS — ok bilibili-live-gift-panel 5.805s

go test -race ./... -run 'Test(AttributeEditLease|RegisterAttributeEditLeaseRouteSharesCoordinatorWithRuntime|ApplyGiftEventSkipsOnlyFrozenAttribute|BackgroundRuntimeFrozenTimerDoesNotCatchUp)' -count=5
PASS — ok bilibili-live-gift-panel 2.247s

go test ./... -count=1
PASS — ok bilibili-live-gift-panel 23.841s
```

### Packaging

```text
npm run build:ui
PASS — 90 modules transformed; built in 532ms

npm run build:exe
PASS — verified FFmpeg 9.0 payload; embedded 82 UI assets (manifest v1);
built dist/gift-panel.exe (dev, local)

cd goserver
go test ./... -run 'TestEmbedded(UIAssetManifestMatchesEmbeddedFS|PageHandlerServesNestedUIAssets)' -count=1
PASS — ok bilibili-live-gift-panel 1.863s
```

The generated `goserver/dist/ui-assets.json` passed `verifyUiAssetManifest` with 82 assets. Exact required module records:

- `modules/ui/config/attribute-edit-lease-D-WTr0d5.js` — 2361 bytes — SHA-256 `5954c672959b7d3d49ba740017e3bcb196988ad1b84bb3357273232d155b7e24`
- `modules/ui/config/attribute-time-value-5Dqa6512.js` — 1354 bytes — SHA-256 `efb742e824e3847779bf755f94565ba31e0a7cc8844230d278f77e49e4c698d4`

The production `config-DkQunY89.js` chunk imports both hashed modules, and `config-entry-CEYXpluv.js` imports that config chunk, proving entry-to-module closure.

### Scope and hygiene

```text
git diff --check
PASS (exit 0; only the repository's normal LF-to-CRLF checkout advisory)
```

Generated `dist` and `goserver/dist` output is ignored and is not part of the source commit. No version, package, changelog, workflow, FFmpeg, tag, push, or release file changed.

## Files changed

- `src/ui/config/attribute-edit-lease.ts`
- `src/ui/config/config.ts`
- `src/storage.ts`
- `src/gift-rule-operations.ts`
- `tests/attribute-edit-lease.test.ts`
- `tests/storage.test.ts`
- `tests/wizard.test.ts`
- `.superpowers/sdd/2026-08-14-attribute-editor-random-time-freeze/final-review-fix-report.md`

## Self-review and remaining concerns

- Lease ownership is still ultimately best-effort over HTTP. If DELETE cannot reach the server, the unchanged 15-second backend TTL remains the final cleanup mechanism.
- Reacquisition uses the existing POST contract only after a definite PUT 404; transient failures retain the current token and retry PUT, avoiding unnecessary parallel leases.
- Exactly one heartbeat interval, at most one retry timer, one unload listener, and one release promise exist per session. Release prevents all later health updates/retries.
- Strict refresh intentionally treats HTTP 204, malformed JSON, missing IDs, and duplicate IDs as an opening failure after lease acquisition; the lease is released and the editor does not mount.
- Transactional editor persistence is intentionally limited to the missing-ID and final editor-save paths identified by review. Existing immediate-save interactions outside the editor keep the established immediate-publication `saveState` semantics; revision checks make overlap with a detached transaction fail safely.
- The strict pre-save GET removes the long editor-open stale-snapshot window, but GET followed by whole-field PATCH is not a backend-atomic compare-and-swap. A peer update in the narrow interval between them can still be overwritten. Eliminating that residual requires a backend configuration revision/`If-Match` protocol or stable-ID atomic merge endpoint and is outside this client-only review fix.
- No unresolved P1/P2 remains in the current diff. The independent final review explicitly classified the backend GET/PATCH race as the architectural residual above.

## Commits

- Implementation and regression tests: `5457c8f` (`fix: harden attribute editor lease and saves`)
- This evidence report is committed separately so it can record the implementation commit exactly.
