# Atomic attribute edit — final verification report

Date: 2026-08-15 (Asia/Shanghai)

## Verdict and fixed range

**READY for handoff; not released.** The atomic attribute-edit implementation was verified from the fixed feature audit base `1bc9a75` through source HEAD `e385fe5e2a5d79f7d1501c0f25a80f79f0c03f5c` on the isolated linked worktree branch `codex/fix-gift-clip-stutter`. The original Task 6 report was committed as `e7aa680ed9a45f8ee5297286ff8dc65d7b689699`. Task 6 found no production packaging closure defect; review round 1 adds only a permanent Go audit test so the previously temporary 83-path proof is rerunnable.

The report is a verification artifact, not permission to publish. No application instance was launched or stopped. No push, tag, release, signing, version, dependency, lockfile, changelog, workflow, README, remote, or FFmpeg payload/build change was made.

## Commit and task ledger

| Task | Commits / range | Outcome |
| --- | --- | --- |
| Plan preflight | `9339475`, `3d9299c` | Plan recorded; root-level Go commands corrected to `go -C goserver ...`. |
| 1 — aggregate and rewrite | `a6de761`, `b5d2302` (`3d9299c..b5d2302`) | Stable-ID target merge, reference rewrite, protected provenance, peer rule/order preservation, typed conflicts. |
| 2 — Go service, lease, HTTP | `041baf4`, `5664b77`, `5f94cd4`, `f561239` (`b5d2302..f561239`) | Strict session/submit adapters, exact token ownership, claim protocol, store-boundary liveness, expiry cleanup, safe HTTP mapping. |
| 3 — frontend adapter/storage | `1ec39e9`, `2ef895a`, `fc84376`, `300560f` | Strict bounded frontend transport and schemas, maintained current-token lease, authoritative queued publication, enriched authoritative GiftInfo boundary. |
| 4 — real editor integration | `89d9605`, `6c1c440`, `5426448`, `c072940`, `f72f61a` | Existing/new editors use narrow atomic submit; prepared sessions, stale-publication suppression, exactly-once release, publication-safe tutorial recovery. |
| 5 — deterministic runtime proof | `b6544e2`, `085aa51`, `6c67b46`, `e385fe5` (`f72f61a..e385fe5`) | Gift/timer peer races, real-store-mutex serialization, same-target later-save-wins, failure cleanup, HTTP stale/replaced/expired token proofs. |
| 6 — package/release gate | `e7aa680`, `a695473`, `5fa1518`; later report-only correction SHA is recorded externally after commit | Fresh source/race/build/package/SHA/scope/no-release verification; permanent package-closure test added; no production closure repair required. |

`git log --oneline --decorate 1bc9a75..e385fe5` contained 20 commits, from `9339475 docs: plan atomic attribute editing` through `e385fe5 test: prove peer updates between attribute saves`, with the task commits above in chronological history.

## Per-task TDD evidence

### Task 1

- RED: `rewriteFormulaIdentifier`, `rewriteAttributeReferences`, `configStore.applyAttributeEdit`, target/command types initially did not exist; the malformed-formula regression then failed with `expected parser error`. Review REDs showed peer rule-ID collisions and ambiguous live rule IDs returned `<nil>` instead of `attributeEditConflictError`.
- GREEN: focused rewrite/aggregate checks passed; final repeated gate `go -C goserver test ./... -run 'TestRewriteFormulaIdentifier|TestRewriteAttributeReferences|TestConfigStoreApplyAttributeEdit' -count=20` was `ok ... 14.456s`; race `-run 'TestConfigStoreApplyAttributeEdit' -count=5` was `ok ... 3.772s`; full Go was `ok ... 23.288s`.

### Task 2

- RED: missing `leases.Has`, service/session/handler/route types; a regression reported `submit reached persistence before acquiring store lock`. Later review REDs proved an expired active claim could be renewed/resurrected and a zero-claim expired record could survive fake-clock rollback.
- GREEN: final expiry/claim/session/HTTP repeated gate (20x) was `ok ... 11.500s`; corresponding race gate (5x) was `ok ... 4.994s`; full Go was `ok ... 17.141s`. The lock order is claim-without-held-lease-mutex, then store mutex, with liveness checks at the write boundary.

### Task 3

- RED: the frontend adapter module and `maintainAttributeEditLease` / `commitAuthoritativeStateMutation` were absent. Review regressions then caught stale authoritative publication, incomplete recovery parsing, unbounded abort-insensitive fetch/body/release work, loose nested schemas, late replacement-token cleanup, durable-success rollback, and rejection of legal authoritative `listed` / `requiresLogin` / `specialEvent` fields.
- GREEN: round-2 API/lease/storage gate passed 109/109 plus typecheck; the enriched-state follow-up passed API/lease 69/69, wizard atomic-save 7/7, and typecheck. Submit keeps one bounded fetch+body window, never automatically retries ambiguous submission, and parses command and authoritative GiftInfo shapes separately.

### Task 4

- RED: the old editor failed all prepared-session cases (3/3) and all atomic-save cases (7/7), using legacy lease/config behavior. Review rounds caught stale prepared opens (3/3), early token capture, lost tutorial target/progress, later-operation overwrite, irrecoverable tutorial PATCH failure, same-target tutorial resurrection, and stale broad-PATCH recovery.
- GREEN: final full storage suite was 48/48; focused atomic/session/save/lease slice was 35/35; exact brief frontend selection was 200 passed with 31 pre-existing skips (the named `tests/attribute-time.test.ts` did not exist); actual time-value suite was 17/17; typecheck and `git diff --check` passed. Recovery now uses a queued settings-only field transaction and never repeats the attribute POST.

### Task 5

- RED/ruling: the behavioral race tests were added after Tasks 1–4 and were green against that completed implementation, so manufacturing a feature-missing RED would have required reverting valid code. Genuine RED evidence was still recorded: race detector found the test clock's non-atomic read/write; the first no-catch-up assertion chose a time that legitimately included later ticks; missing real-store hook names caused a compile RED; and the old release hook placement deadlocked a reentrant coordinator call until timeout.
- GREEN: final focused same-target trio passed 20x in `8.865s` and race 5x in `3.148s`; full Go passed `18.376s`; full race passed `29.222s`; API/lease frontend passed 69/69; full frontend passed 660 with 31 documented skips; typecheck and diff check passed. Task 5's final round was test-only.

### Task 6

- RED/ruling: no production packaging regression was found. The brief's proposed `scripts/build-go.test.mjs` does not exist in this tree; the real packaging test is `tests/ui-assets.test.ts`, and the real Go tests are `TestEmbeddedPageHandlerServesNestedUIAssets` and `TestEmbeddedUIAssetManifestMatchesEmbeddedFS`.
- GREEN: the actual focused Vitest passed 1/1 and the exact Go embedded pair passed. The first report used an uncommitted exhaustive audit for the 83 public URLs; review round 1 below replaces that evidentiary gap with a permanent, dynamically counted Go test using the same production handler and embedded filesystem.

## Stable-ID merge and reference-rewrite contract

### Merge behavior

| State area | Selection / merge rule | Ordering and protection |
| --- | --- | --- |
| Existing target attribute | Selected only by nonempty stable ID after preparation. The submitted editable fields replace the target at its existing index. | Server retains the stored `ID`, `Color`, `CreatedFromTemplateID`, and `CreatedFromTemplateVersion`; client cannot overwrite protected provenance. |
| Legacy existing attribute | Session accepts exactly one `legacyName`, requires a unique match, generates and durably persists an ID before creating the lease/snapshot. | Failure to persist or create a token fabricates no usable session. |
| New attribute | Target kind `new` accepts no client ID; backend generates a unique stable ID. | Appended once and returned as `{id,name,created:true}`; no edit lease is acquired. |
| Peer attributes | Read from the latest state inside `configStore.mu`; never supplied by the narrow client command. | Values and order survive target save. |
| Target-owned gift/timer rules | Ownership is determined from current stored target name and stable nonempty rule IDs before rename. Existing owned group is replaced by the submitted group. | Submitted group is inserted at the first former-owned position; peers retain relative order. If no owned group existed, submitted rules append. |
| Peer gift/timer rules | Never replaced by target payload; submitted rule IDs conflicting with peers are rejected. | Full peer records and relative order remain current. Ambiguous/empty live rule IDs are rejected before ID-keyed merge. |
| Gift catalog upserts | Upsert by numeric gift ID; existing slots are replaced and genuinely new IDs append. | Unmentioned catalog entries and their order remain. The duplicate-upsert Minor below remains explicitly deferred. |
| Persistence | Clone latest state, merge, normalize, validate, recheck lease liveness, then use the existing atomic multi-shard persistence seam. | Any validation/write failure exposes no partial target or reference migration. Notification occurs after the store lock is released. |

### Rename coverage

| Reference class | Rewritten atomically from old current name to new name? |
| --- | --- |
| Gift rules | Yes: `AttributeName`, tokenized `Formula`, and tokenized `Condition`. |
| Timer rules | Yes: `AttributeName`, tokenized `Formula`, and tokenized `Condition`. |
| Display scenes | Yes: every current `AttributeNames` entry. |
| Activities | Yes: current `AttributeNames`, `InitialValues` keys, milestone `AttributeName`, result `WinnerAttributeName`, and result `Values` keys. |
| Formula presets | Yes: `SourceAttributeName` and tokenized `Formula`. |
| Historical logs, receipts, contributions, statistics | No, intentionally. They are historical facts, not current configuration references. |

Tokenizer-based formula rewriting preserves surrounding formatting, changes identifier tokens only, and rejects malformed syntax without returning a partially rewritten value.

## Session / lease / submit / publication state machine

| Phase | Transition | Guarantees and failure state |
| --- | --- | --- |
| Existing open | UI POSTs exactly `{attributeId}` or `{legacyName}` to `/api/attribute-edits/session`; server resolves/backfills under `store.mu`, creates a non-exclusive token, returns strict `{code,attributeId,token,expiresAt,state}`. | Editor mounts only from parsed authoritative state. Failure mounts nothing and leaves no maintained session. A stale prepared response is suppressed and its lease is released once. |
| Maintain | UI heartbeats current token with PUT `/api/attribute-edit-lease`. | A 404 performs session reacquisition, not the old lease-acquire POST. Token changes only after the complete replacement response parses; the getter supplies the latest token. Transport/body work is bounded. |
| Release | UI DELETEs the current token; repeated release shares one promise. Server marks `releasing`, invokes any test hook outside `lease.mu`, waits for active claims with `Cond.Wait`, then deletes. | No early dismissal while submit/recovery is in flight; cancel/success/unload clean up once. A late valid replacement receives one bounded cleanup DELETE. |
| Existing submit claim | At actual queued execution UI reads the current token and POSTs the five-key narrow aggregate. Server `Begin`s the exact `(attributeID,token)` pair, increments claims, then releases `lease.mu`. | Missing, mismatched, replaced, released, or expired token returns `409 lease_lost`; no persistence. Submit does not auto-retry an ambiguous timeout. |
| Store critical section | Claim waits for `store.mu`; server reads/clones latest state and checks claim liveness after locking and again immediately before durable persistence. | Peer mutations completed before lock acquisition survive. Expiry at the write boundary is `lease_lost`. `Finish` always decrements the claim and removes expired/releasing zero-claim records. |
| New submit | UI sends target `{kind:'new'}` with no token; backend generates the ID inside the merge. | Same narrow aggregate and authoritative response, but no lease lifecycle. |
| Frontend publication | `commitAuthoritativeStateMutation` serializes behind `persistQueue`; monotonic operation identity allows only the latest invoked operation to publish. Every successful server response refreshes snapshots/durable fallback. | A later local operation wins visible publication. If that later operation fails, the latest successful server state is restored; superseded failures cannot roll back newer work. |
| Tutorial follow-up | After a published submit, tutorial settings use `saveStateFieldTransaction('settings', ...)` at actual queue execution against a captured tutorial baseline. | Emits settings only, preserves peer/broad fields, rejects stale baseline, is retryable, never repeats attribute submit, and releases once on completion/dismissal. |

Lease freeze is target-local: gift/timer activity for the leased target is ignored while live, peer targets continue, no frozen-event catch-up occurs, and release/expiry unfreezes the target.

## Deterministic concurrency and race proofs

| Proof | Established ordering | Verified result |
| --- | --- | --- |
| Gift peer B | `afterBegin` pauses A after claim and before `store.mu`; real `backgroundRuntime.processInboxRecord` persists B `2 -> 3`; A then enters merge. | Returned and persisted state contain edited A plus B=3, with A/B order preserved. |
| Timer peer B | Same channel barrier; a real due timer tick persists B while A is leased. | A remains its submitted value (frozen, no catch-up); B's timer update survives in response/disk/cache. |
| Same-target real mutex | Command 1 blocks inside real `writeAtomically` while holding `store.mu`; `TryLock` proves the mutex is held; command 2 reaches immediate pre-lock boundary and cannot enter early. | Commands serialize at the production mutex; removing the lock makes the test fail directly. |
| Same-target later valid save | Command 1 completes, command 2 pauses after claim, real runtime updates B to 3 between commands, then command 2 proceeds with distinct owned gift/timer groups. | Command 2 is the later successful target state; its complete owned groups plus B=3/order are authoritative on return, disk, and cache. |
| Same-target later invalid save | Valid command 1, then real B update, then name-conflicting command 2. | Command 2 rejects; command 1's full ordered target rules and B=3 remain authoritative. |
| Write failure | Live-token submit reaches injected atomic write failure; full pre-state and claim count are inspected. | No partial state, claim returns to zero, same token can later save successfully. |
| Race detector | Deterministic channel/`sync.Once` barriers; test clock uses `atomic.Int64`; no sleep/poll establishes ordering. | Fresh full `go test -race ./...` passed with no race report. |

## Fresh Task 6 gates (strict sequential order)

| Order | Command | Actual result |
| ---: | --- | --- |
| 1 | `npm run typecheck` | exit 0; `tsc --noEmit`; wall 2.7s. |
| 2 | `npm test -- --reporter=dot` | exit 0; 46/46 files; 660 passed, 31 skipped, 691 total; Vitest duration 15.23s (wall 16s). |
| 3 | `go -C goserver test ./... -count=1 -timeout=300s` | exit 0; `ok bilibili-live-gift-panel 17.622s` (wall 19s). |
| 4 | `go -C goserver test -race ./... -count=1 -timeout=300s` | exit 0; `ok bilibili-live-gift-panel 29.011s` (wall 30.4s); no race report. |
| 5 | `npm run build:ui` | exit 0; Vite 5.4.21 transformed 91 modules and built in 535ms (wall 1.3s). |
| 6 | `npm run build:exe` | exit 0; verified existing FFmpeg 9.0 payload; embedded 83 UI assets (manifest v1); rebuilt local dev EXE (wall 4.2s). |

The full Vitest output retains the known single-file plugin “asset not inlined” notices. They are informational and the suite exited 0.

Focused package checks:

- `npm test -- tests/ui-assets.test.ts --reporter=dot`: 1 file / 1 test passed; Vitest duration 323ms.
- `go -C goserver test ./... -run 'TestEmbedded(PageHandlerServesNestedUIAssets|UIAssetManifestMatchesEmbeddedFS)' -count=1 -timeout=120s`: `ok ... 1.518s`.
- Initial temporary exhaustive production-handler audit: 83/83 manifest assets served at their public URLs and response size/SHA matched; `ok ... 1.589s`. `index.html` is publicly served at `/` with 200; direct `/index.html` canonically redirects to `/` with 301 by `http.FileServer`. All other manifest paths are direct 200s. Review round 1 permanently codifies and strengthens this proof below.

## Manifest closure and executable

Manifest audit of `goserver/dist/ui-assets.json`:

- version: 1;
- manifest assets: 83;
- actual UI files excluding the manifest itself: 83;
- manifest/filesystem closure differences: 0;
- every manifest size and SHA-256: matched;
- exact endpoint-literal (`"/api/attribute-edits"`) modules: 1;
- endpoint module manifest records: 1;
- endpoint module: `modules/ui/config/attribute-edit-api-DLTqn8K0.js`;
- endpoint module size: 2,758 bytes;
- endpoint module SHA-256: `df6706ae81e453b074fcd4c4c044d639786e9937aca0dfbbb4fd41b25ad69266`;
- transitive entry reachability: `modules/ui/config/config-entry-CpZdhpxs.js` -> `modules/ui/config/config-BGeij3XQ.js` -> `modules/ui/config/attribute-edit-api-DLTqn8K0.js`;
- forbidden manifest paths: 0.

The forbidden scan covered source maps; Playwright/browser payloads; test and fixture roots; FFmpeg test tools and toolchains; reports/coverage/test-results; scratch/tmp/temp roots; stale executables/update metadata. The compiled EXE binary had zero hits for the corresponding path/tool identifiers. The production FFmpeg payload is intentionally embedded separately by the application; this audit excludes only test/build tooling and confirms that payload/build files were not changed.

Final artifact:

```text
Path:   C:\Users\brain\.codex\worktrees\21fa\bilibili\dist\gift-panel.exe
Size:   14046208 bytes
SHA256: 8f39489916630a80662929660059b14db3340173448cfa8ad453377587727de2
```

`build:exe` additionally reported the unchanged existing FFmpeg 9.0 binary as 6,209,536 bytes, ZIP 2,415,506 bytes, SHA-256 `19247e960c50adcf107bc04e8a20435fd67d098e06b227d8772f0d1b8027e03c`, `authenticode=false`; this was a dev/local build, not a signed release artifact.

## Scope and no-release audit

Before creating the original report, `git diff --check` exited 0 and `git status --short` was empty. The fixed source-feature diff `git diff --name-only 1bc9a75..e385fe5` contained 17 files:

```text
docs/superpowers/plans/2026-08-14-atomic-attribute-edit.md
goserver/attribute_edit_leases.go
goserver/attribute_edit_leases_test.go
goserver/attribute_edits.go
goserver/attribute_edits_http_test.go
goserver/attribute_edits_test.go
goserver/config_store.go
goserver/formula.go
goserver/main.go
src/storage.ts
src/ui/config/attribute-edit-api.ts
src/ui/config/attribute-edit-lease.ts
src/ui/config/config.ts
tests/attribute-edit-api.test.ts
tests/attribute-edit-lease.test.ts
tests/storage.test.ts
tests/wizard.test.ts
```

Automated forbidden-scope matches: 0. Targeted diff checks returned no package/dependency/lock, version, changelog, workflow, signing, FFmpeg payload/build, or README files. No tag points at source HEAD. The observed remote remains `origin https://github.com/brainfk123/bilibili-live-gift-panel.git` for fetch/push; it was not changed or contacted for push. No tag/release/sign/publish command was run.

## Deferred Minors and residual boundary

These are accepted non-blocking items, not silently reclassified as fixed:

| Source | Deferred item | Final ruling |
| --- | --- | --- |
| Task 1 | Duplicate `giftCatalogUpserts` for an existing ID effectively use the last duplicate (map replacement), while duplicate brand-new IDs append the first duplicate. | Real but outside the atomic peer-preservation/package gate; normal UI emits unique gift IDs. A future command-validation change should reject duplicates or define one rule consistently. |
| Task 2 | Private `applyAttributeEditLocked` exposes an unenforced caller-holds-`store.mu` precondition and is currently an unused speculative seam. | Maintainability Minor only; production service uses the authorized locked path. Do not call this seam without the lock; a future cleanup may remove or encode the precondition. |
| Task 2 | A panic in the test-only `afterBegin` hook can occur before `Submit` installs its `Finish` defer and leak a claim. | Test-instrumentation Minor; hook is unexported, default nil, and never installed in production. Production paths do not introduce that panic source. |
| Task 3 | Pre-existing `refreshStateFromServer` does not join the storage operation epoch and could publish an older GET after a later mutation. | Existing broader storage concern, not introduced by atomic submit and not used as the editor's submit publication path. Deferred to a separately scoped storage change. |
| Task 4 | Shared wizard atomic backend fixture has simpler string rename/order behavior than the real Go tokenizer/anchor merge. | Test-fixture fidelity Minor. Go aggregate tests independently prove production tokenizer, group-anchor, and ordering behavior; wizard tests prove UI wire/publication behavior. |
| Task 4 | The shared fixture can issue an unrelated gift-catalog-only PATCH on mount. | Pre-existing isolated fixture behavior. Recovery tests explicitly require settings-only persistence and forbid recovery-owned `attributes`, `rules`, or `timerRules` PATCHes. |

Parser placement in `attribute-edit-lease.ts` is also a maintainability concern, not a correctness finding; command and authoritative schemas are deliberately distinct and tested.

Finally, the atomic guarantee covers mutations that participate in `configStore` locking and its atomic multi-shard persistence seam. **Unsupported external processes or tools that directly rewrite shard files outside `configStore` are outside this mutation guarantee.** Such writers can bypass the in-process lock and require their own coordination/recovery protocol; this boundary does not weaken the verified API/runtime behavior.

## Review fix round 1/5 — permanent package-closure evidence

### Finding and test boundary

The original temporary exhaustive test was not independently rerunnable from the committed tree. Existing `TestEmbeddedUIAssetManifestMatchesEmbeddedFS` checked only that each manifest path could be read from `embeddedFS`; it did not reject extra embedded files, validate manifest size/SHA, exercise the production HTTP handler, or compare response bytes. Round 1 adds `TestEmbeddedUIAssetManifestClosesAndServesProductionAssets` in `goserver/ui_assets_test.go`. It consumes the existing `//go:embed` filesystem, `ui-assets.json`, and `newEmbeddedPageHandler`; it creates no copy, build, manifest, or packaging logic.

The test dynamically walks the embedded UI filesystem (excluding the manifest itself), rejects invalid/duplicate manifest paths, compares the exact sorted manifest and embedded file sets, verifies embedded size/SHA, then requests every record through its canonical public URL (`index.html` as `/`) and requires HTTP 200 plus byte-for-byte/size/SHA agreement. It deliberately does not hardcode 83, so additions and removals remain auditable.

### TDD evidence

RED after adding the focused test entry point before its verifier:

```text
go -C goserver test ./... -run '^TestEmbeddedUIAssetManifestClosesAndServesProductionAssets$' -count=1 -timeout=120s
# bilibili-live-gift-panel [bilibili-live-gift-panel.test]
.\ui_assets_test.go:6:2: undefined: verifyEmbeddedUIAssetClosure
FAIL bilibili-live-gift-panel [build failed]
```

GREEN after adding only the test-side verifier:

```text
go -C goserver test ./... -run '^TestEmbeddedUIAssetManifestClosesAndServesProductionAssets$' -count=1 -timeout=120s -v
=== RUN   TestEmbeddedUIAssetManifestClosesAndServesProductionAssets
    ui_assets_test.go:17: verified manifest closure and production handler bytes for 83 embedded UI assets
--- PASS: TestEmbeddedUIAssetManifestClosesAndServesProductionAssets (0.03s)
PASS
ok   bilibili-live-gift-panel 1.529s
```

Relevant embedded tests and full Go also passed:

```text
go -C goserver test ./... -run '^TestEmbedded' -count=1 -timeout=120s -v
TestEmbeddedPageHandlerServesNestedUIAssets: PASS
TestEmbeddedUIAssetManifestMatchesEmbeddedFS: PASS
TestEmbeddedUIAssetManifestClosesAndServesProductionAssets: PASS (83 assets)
ok   bilibili-live-gift-panel 1.607s

go -C goserver test ./... -count=1 -timeout=300s
ok   bilibili-live-gift-panel 19.065s
```

### Commit and audit recording protocol

The original verification report is committed at `e7aa680ed9a45f8ee5297286ff8dc65d7b689699`. The closure test and its evidence are committed together at `a69547340233267d3ed65cdf2e8ce8d7deff1ef1` (`test: make packaged UI closure auditable`). The first scope-record correction is committed at `5fa15187267626502595ee85f11df834bd96902b` (`docs: finalize atomic edit scope audit`).

This wording correction is necessarily report-only. It cannot embed its own final commit SHA, and this report does not claim otherwise. It changes only the already-counted `final-report.md` path, so it does not alter the audited product/test path membership. After committing the correction, the controller must run the scope/status/diff/tag commands against the actual new `HEAD`, then record the exact new SHA and command output in the external Task 6 handoff/ignored ledger. That post-commit evidence, rather than a self-referential promise in this report, establishes the correction commit's final repository state.

The staged pre-commit audit (`git diff --name-only 1bc9a75`) contained exactly 19 unique paths: 2 documentation/report paths, 5 Go production paths, 4 Go test paths, 4 TypeScript production paths, and 4 TypeScript test paths. The only round-1 path addition is `goserver/ui_assets_test.go`; the report path already existed. `git diff --cached --check` exited 0, staged scope was exactly the test plus this report, and the forbidden package/lock/version/changelog/workflow/signing/FFmpeg/README matcher returned 0.

### Observed post-commit audit at `5fa1518` (not a later-final-HEAD claim)

The following audit was actually run after `5fa15187267626502595ee85f11df834bd96902b` existed and while `git rev-parse HEAD` returned that exact SHA. It is evidence for `5fa1518`, not a claim that `5fa1518` remains HEAD after this later correction.

Exact command:

```text
git diff --name-only 1bc9a75..5fa1518
```

Exact 19-path output:

```text
.superpowers/sdd/2026-08-14-atomic-attribute-edit/final-report.md
docs/superpowers/plans/2026-08-14-atomic-attribute-edit.md
goserver/attribute_edit_leases.go
goserver/attribute_edit_leases_test.go
goserver/attribute_edits.go
goserver/attribute_edits_http_test.go
goserver/attribute_edits_test.go
goserver/config_store.go
goserver/formula.go
goserver/main.go
goserver/ui_assets_test.go
src/storage.ts
src/ui/config/attribute-edit-api.ts
src/ui/config/attribute-edit-lease.ts
src/ui/config/config.ts
tests/attribute-edit-api.test.ts
tests/attribute-edit-lease.test.ts
tests/storage.test.ts
tests/wizard.test.ts
```

Observed derived audit output:

```text
PATH_COUNT=19
CATEGORIES=docs/report:2,go-production:5,go-tests:4,ts-production:4,ts-tests:4
FORBIDDEN_COUNT=0
```

The remaining commands were also actually run after `5fa1518`:

```text
git status --short
<no output>

git tag --points-at 5fa1518
<no output>
TAG_COUNT=0

git diff --check 5fa1518^..5fa1518
<no output>
DIFF_CHECK_EXIT=0

git rev-parse HEAD
5fa15187267626502595ee85f11df834bd96902b
```

The correction commit containing this block is report-only and changes no product/test path. It is not represented above as though its own SHA were already knowable; its actual post-commit `HEAD` audit belongs in the controller's final handoff and ignored Task 6 ledger.

## Handoff

The original report is committed as `e7aa680`, the permanent package-closure proof and its evidence as `a695473`, and the observed post-commit `5fa1518` audit is recorded above. The controller records this correction commit's actual SHA and post-commit HEAD audit externally after the commit exists. All required gates, focused package tests, manifest/handler audit, EXE hash, and no-release checks passed. No production packaging closure repair was required. Publishing remains explicitly unauthorized and out of scope.

## Final review fix wave — 2026-08-15

This section supersedes the earlier source-HEAD verdict for the one authorized final fix wave. The fixed input HEAD was `f0590429f8f0db321d743252d0841b845779935c`. The fix-wave commit cannot record its own SHA in this file; the controller records the actual full SHA and post-commit status externally after the commit exists. No push, tag, release, signing, version, package/dependency/lockfile, workflow, changelog, README, or FFmpeg source/payload/build change was made.

### Final-review findings and RED → GREEN evidence

1. **Published-journal commit boundary and committed-result reconciliation.** `statePersistenceOutcome` now separates pre-publication failure from logical commit; a generated transaction is validated before publication; journal publication uses the rename-aware atomic outcome; and shard application records whether every shard is committed independently of journal cleanup. Once the complete journal reaches its final name it is the authoritative logical commit record, including a reported containing-directory sync warning, so a later replay can never turn an ordinary reported failure into a committed edit. Attribute submit/backfill synchronously retries replay and reads the committed state when possible. If replay still fails, an isolated authoritative candidate is retained. On restart, any readable, schema-valid, canonically validated journal reconstructs that candidate before shard replay; unreadable or invalid evidence remains a permanent recovery block. Every read retries replay and serves a clone of the candidate instead of partial shards, mutations remain blocked until replay succeeds, and startup defers migration while valid evidence remains pending. Notifications occur once. Genuine failures before journal publication still fail and notify zero listeners.
   - RED: the initial four shard stages of `TestAttributeEditSubmitReconcilesCommittedPostJournalFailures` each returned `durably journaled submit returned an error: injected post-journal shard failure`. Extending the stage table first failed to compile on missing `removeStateTransaction`, `syncStateTransactionDirectory`, and `writeAtomicallyOutcome` seams. The first persistent HTTP proof returned `500 {"code":"internal_error","message":"服务器暂时无法处理请求"}` for a committed shard failure and left `mutationBlockKind="transaction_recovery"` after persistent journal-removal failure. Independent completion review then added three stronger gaps: combined journal-publication sync warning plus persistent shard failure still returned HTTP 500 for a new target; a same-process read while replay remained broken either exposed a partial shard mix or permanently set `transaction_recovery`; and restart while the same replay failure remained active lost the memory-only candidate, exposed partial shards, and could not resume recovery. The restart regression's first run was deterministic RED at compile time (`undefined: initializeConfigStore`) before the startup seam and journal rehydration existed.
   - GREEN: `TestAttributeEditSubmitReconcilesCommittedPostJournalFailures` covers journal publication sync warning, all four shards, journal removal, and final directory sync. `TestAttributeEditHTTPReturnsSuccessWhileCommittedJournalReconciliationStillFails` also covers the combined warning/failure on a new target, keeps every fault active through HTTP 200, same-process read/mutation attempts, and a real startup initialization of a new store, and verifies each read returns the same authoritative public state without a permanent block. It then clears the restarted-store fault and proves in-process replay recovery, clears the original fault, and proves same-store plus fresh-store restart equal the response with no duplicate notification. `TestAttributeEditSubmitWriteFailureLeavesNoPartialStateAndAllowsLaterSave` proves the pre-publication failure contract and zero notifications.

2. **Frontend durable rollback snapshots.** Every stored fallback is cloned at capture time, operation snapshots are isolated before asynchronous persistence, and restore publishes a new clone rather than the private fallback object.
   - RED: the two new storage regressions restored `"unsaved-room"` instead of `"persisted-room"` after a top-level in-place mutation and restored nested crop value `0.8` instead of durable `0.1` after a second failure.
   - GREEN: `restores a durable top-level value after the mounted state is mutated in place and saving fails` and `keeps a nested durable rollback snapshot isolated across repeated in-place mutations` both pass; the full focused storage/API/lease run passed 125/125.

3. **Legacy authoritative unit and recoverable lease cleanup.** Only authoritative session/submit parsing accepts legacy attribute `unit: "number"`, mapping it to canonical `"none"`; `isAttribute` and outbound submit validation remain strict. Cleanup identity parsing is deliberately narrower than adoption parsing. Initial parse failure, late initial response, and malformed/later reacquisition issue bounded keepalive DELETE requests whenever a valid `{attributeId, token}` can be recovered, including a mismatched response attribute ID.
   - RED: real `prepareAttributeEditSession` and `submitAttributeEdit` calls threw `属性编辑响应无效` for legacy authoritative state; initial malformed/late session cleanup issued only the POST; malformed reacquisition deleted only the current `A` token. The strengthened mismatch proof showed the replacement `B` token incorrectly deleted under `attribute-1` instead of its authoritative `attribute-2`.
   - GREEN: authoritative session and submit return `unit: "none"`; a runtime-cast outbound `"number"` command still rejects before fetch; initial and late tokens are cleaned up; malformed reacquisition keeps the current token, stays retrying, and deletes both the replacement pair and current pair exactly once.

4. **Legacy-name selection is migration-only.** `legacyName` matches only attributes whose trimmed stable ID is empty.
   - RED: the ID-bearing fixture was selected successfully by the service and the HTTP adapter returned 200.
   - GREEN: `TestAttributeEditSessionLegacyNameDoesNotSelectIDBearingAttribute` returns typed not-found at the service boundary and HTTP 404, creates no lease, and never calls the ID generator.

5. **Protected fields on new attributes.** The server always overwrites the client ID and initializes `Color`, `CreatedFromTemplateID`, and `CreatedFromTemplateVersion` to server-owned zero values while preserving editable fields.
   - RED: the protected-field regression returned `Color:"#abcdef"`, template ID `"forged-template"`, and template version `99`.
   - GREEN: `TestConfigStoreApplyAttributeEditCreatesWithGeneratedIDAndAppends` proves generated ID plus zero protected fields in both result and persisted state while retaining the editable broadcast message.

6. **Canonical gift/timer rule-ID identity.** Submitted rule slices are copied and IDs trimmed; ownership, peer collision, duplicate detection, and merge maps use trimmed identity; the merged aggregate is revalidated. Untouched legacy peer storage remains byte-for-byte unchanged.
   - RED: both gift and timer whitespace-duplicate cases returned nil error, both peer-collision cases returned nil error, and both canonical-storage cases persisted surrounding whitespace.
   - GREEN: gift and timer duplicates are typed input errors, peer collisions are typed conflicts, submitted IDs persist trimmed and support later edits, and `TestConfigStoreApplyAttributeEditPreservesNonTargetLegacyRuleIDStorage` proves non-target raw legacy IDs remain unchanged across future valid edits.

The tightly scoped same-origin minor was also fixed: session and submit now reuse the existing host-aware proxy-compatible policy already used by the lease endpoint. RED was 403 for same-host scheme mismatch in all three adapter assertions; GREEN accepts the request past origin validation while cross-host Origin and `Sec-Fetch-Site: cross-site` remain forbidden. The initial-session temporary-freeze cleanup minor is covered by finding 3. The five explicitly deferred minors were not pursued.

### Final verification matrix

| Gate | Exact command | Result |
|---|---|---|
| Focused frontend | `npm test -- tests/storage.test.ts tests/attribute-edit-api.test.ts tests/attribute-edit-lease.test.ts --reporter=dot` | exit 0; 3/3 files, 125/125 tests. |
| TypeScript | `npm run typecheck` | exit 0; `tsc --noEmit`. |
| Focused Go | `go -C goserver test ./... -run '^(TestAttributeEditSubmitReconcilesCommittedPostJournalFailures|TestAttributeEditHTTPReturnsSuccessWhileCommittedJournalReconciliationStillFails|TestPendingStateTransaction.*|TestTransactionPending.*|TestApplicationLifecycleStartsDiagnosticsWithUnrecoverableTransactionEvidence|TestConfigResetClearsCorruptTransactionAndRuntimeArtifactsThroughProductionHandler)$' -count=1 -timeout=180s` | exit 0; `ok ... 3.346s`. |
| Focused Go race | `go -C goserver test -race ./... -run '^(TestAttributeEditSubmitReconcilesCommittedPostJournalFailures|TestAttributeEditHTTPReturnsSuccessWhileCommittedJournalReconciliationStillFails|TestPendingStateTransaction.*|TestTransactionPending.*)$' -count=1 -timeout=240s` | exit 0; `ok ... 3.406s`; no race report. |
| Deterministic stress | `go -C goserver test ./... -run '^(TestAttributeEditSubmitReconcilesCommittedPostJournalFailures|TestAttributeEditHTTPReturnsSuccessWhileCommittedJournalReconciliationStillFails)$' -count=20 -timeout=300s` | exit 0; `ok ... 15.101s`. |
| Full Go | `go -C goserver test ./... -count=1 -timeout=600s` | exit 0; final frozen-tree rerun `ok ... 18.385s`. |
| Full Go race | `go -C goserver test -race ./... -count=1 -timeout=900s` | exit 0; final frozen-tree rerun `ok ... 30.838s`; no race report. |
| Full Vitest | `npm test -- --reporter=dot` | exit 0; 46/46 files, 667 passed, 31 skipped, 698 total. Known single-file asset notices remained informational. |
| UI build | `npm run build:ui` | exit 0; Vite 5.4.21 transformed 91 modules and built in 532ms. |
| Local EXE build | `npm run build:exe` | exit 0; unchanged FFmpeg 9.0 payload verified; 83 UI assets embedded (manifest v1); local dev EXE built. |
| Permanent embedded closure | `go -C goserver test ./... -run '^TestEmbedded(UIAssetManifestClosesAndServesProductionAssets|PageHandlerServesNestedUIAssets|UIAssetManifestMatchesEmbeddedFS)$' -count=1 -timeout=120s -v` | exit 0; final post-build rerun passed all three tests; manifest/handler bytes verified for 83 assets; `ok ... 1.581s`. |
| Whitespace/scope | `git diff --check` plus `git status --short` | diff check exit 0; exactly the 13 intended source/test/report paths are tracked changes after this report update. |

The freshly rebuilt ignored local artifact was `dist/gift-panel.exe`, 14,059,520 bytes, SHA-256 `fc4c5eec55a3aa3dda493b778d9a8d96824c0f7416fc4a6a4dcf418fde86014d`. It was not signed, published, staged, or released. The existing FFmpeg payload remained 6,209,536 bytes with ZIP SHA-256 `19247e960c50adcf107bc04e8a20435fd67d098e06b227d8772f0d1b8027e03c`; no FFmpeg file changed.

### Final fix-wave scope and remaining boundary

The intended tracked scope is exactly one report, four Go production files, two Go test files, three TypeScript production files, and three TypeScript test files:

```text
.superpowers/sdd/2026-08-14-atomic-attribute-edit/final-report.md
goserver/attribute_edit_leases_test.go
goserver/attribute_edits.go
goserver/attribute_edits_test.go
goserver/config_store.go
goserver/state_shards.go
goserver/state_transaction.go
src/storage.ts
src/ui/config/attribute-edit-api.ts
src/ui/config/attribute-edit-lease.ts
tests/attribute-edit-api.test.ts
tests/attribute-edit-lease.test.ts
tests/storage.test.ts
```

The explicit committed-outcome interpretation is intentionally used by atomic attribute submit and legacy-ID backfill, the request paths named by the final finding. Other generic `configStore` mutation callers retain their existing error-return contract and were not broadened in this feature-only wave. Unsupported external processes that directly rewrite shard files remain outside the `configStore` atomicity guarantee. Independent frontend review completed with no Critical, Important, or Minor findings. Independent Go review returned not-ready twice on the combined publication/read-recovery and restart-survival gaps; each was reproduced and fixed, and its final read-only re-review completed READY with no Critical, Important, or Minor findings. No additional product concern is known within the authorized final-review scope.
