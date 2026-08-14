# Atomic Attribute Editing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the attribute editor's client-side whole-config PATCH with a backend atomic stable-ID merge so live peer-attribute updates can never be overwritten.

**Architecture:** A dedicated backend module prepares existing edit sessions and atomically applies one attribute aggregate under `configStore.mu`. The frontend submits only the edited attribute, its owned gift/timer rules, and gift-catalog upserts; the backend preserves peer state/order, rewrites current name references, validates, persists, and returns the authoritative state. Existing `/api/config` remains compatible for every other editor.

**Tech Stack:** Go 1.26 HTTP/config store, TypeScript, Vitest, Vite, existing multi-shard state transaction, existing attribute edit leases.

## Global Constraints

- Only the edited attribute is frozen; other attributes, receipts, statistics, and contributions continue normally.
- Multiple sessions may edit one attribute; the last successful submission wins the target attribute and its owned gift/timer rules.
- A later target save must never overwrite peer attributes, peer rules, live values, or peer array order.
- Existing targets are selected only by stable nonempty ID after session preparation; legacy ID-less targets are backfilled atomically by unique name.
- New attributes receive a backend-generated stable ID and do not acquire an edit lease.
- Existing saves require an exact live attribute/token pair; leases remain non-exclusive.
- Renames atomically update current name-based references in gift rules, timer rules, scenes, activities, and formula presets; historical logs, receipts, contributions, and statistics are not rewritten.
- Formula/condition rewriting replaces tokenizer-confirmed identifier tokens only, preserving whitespace and every non-target rune.
- All candidate states are normalized, fully validated, and persisted through the existing durable multi-shard transaction before cache publication or notification.
- Strict mutation endpoints reject cross-site, oversized, unknown-field, and trailing JSON requests; all responses set `Cache-Control: no-store` and never expose internal paths/raw causes.
- Existing `/api/config` PUT/PATCH/DELETE behavior remains compatible for non-attribute callers.
- Do not modify version metadata, release workflows, FFmpeg payloads, tags, remotes, or publish artifacts.

---

### Task 1: Build the Atomic Attribute Aggregate Module

**Files:**
- Create: `goserver/attribute_edits.go`
- Create: `goserver/attribute_edits_test.go`
- Modify: `goserver/config_store.go`
- Modify: `goserver/formula.go`

**Interfaces:**
- Produces: `type attributeEditCommand`, `type attributeEditResult`, and `(*configStore).applyAttributeEdit(command attributeEditCommand, newID func() (string, error)) (attributeEditResult, error)`.
- Produces: `rewriteFormulaIdentifier(input, oldName, newName string) (string, error)` and `rewriteAttributeReferences(state *appState, oldName, newName string) error`.
- Consumes: `configStore.mu`, `readStateLocked`, `cloneAppState`, `normalizeAppState`, `validateAppState`, and `persistStateLocked`.
- Preserves: legacy `configStore.handle` and `/api/config` behavior.

- [ ] **Step 1: Write failing identifier-rewrite tests**

```go
func TestRewriteFormulaIdentifierPreservesFormattingAndSubstrings(t *testing.T) {
    cases := []struct{ input, want string }{
        {"积分 + MAX(积分, 积分2)", "能量 + MAX(能量, 积分2)"},
        {"IF(积分>=10,积分,0)", "IF(能量>=10,能量,0)"},
        {"RANDOMCHOICE( 积分 , 1 )", "RANDOMCHOICE( 能量 , 1 )"},
    }
    for _, tc := range cases {
        got, err := rewriteFormulaIdentifier(tc.input, "积分", "能量")
        if err != nil { t.Fatal(err) }
        if got != tc.want { t.Fatalf("rewrite %q = %q, want %q", tc.input, got, tc.want) }
    }
}
```

Also assert malformed formulas return the parser error without partial output.

- [ ] **Step 2: Run the identifier tests and verify RED**

Run from `goserver`:

```bash
go test ./... -run '^TestRewriteFormulaIdentifier' -count=1
```

Expected: compile failure because the rewrite function does not exist.

- [ ] **Step 3: Implement position-based formula rewriting**

Use `tokenizeFormula` and each token's rune `pos`. Rebuild from the original rune slice; replace only tokens with `kind == "ident" && value == oldName`. Never serialize tokens back into normalized text.

```go
func rewriteFormulaIdentifier(input, oldName, newName string) (string, error) {
    tokens, err := tokenizeFormula(input)
    if err != nil { return "", err }
    runes := []rune(input)
    var out strings.Builder
    cursor := 0
    for _, token := range tokens {
        if token.kind != "ident" || token.value != oldName { continue }
        out.WriteString(string(runes[cursor:token.pos]))
        out.WriteString(newName)
        cursor = token.pos + len([]rune(token.value))
    }
    out.WriteString(string(runes[cursor:]))
    return out.String(), nil
}
```

- [ ] **Step 4: Write failing aggregate-merge tests**

Build one fixture with target `attribute-a`, peer `attribute-b`, target and peer gift/timer rules, a scene, formula preset, and every activity reference. Cover:

- target replacement stays at its current index and preserves its ID/color/template provenance;
- peer value and relative order remain unchanged;
- target-owned gift/timer rules are replaced/removed/inserted at the target group's original anchor;
- peer rules remain in exact relative order;
- catalog upserts update by gift ID and append only new IDs;
- rename updates rule targets/formulas/conditions, scene names, activity lists/maps/result/milestones/winner, and formula presets;
- history/log/receipt/contribution names remain unchanged;
- a new target gets an injected ID and appends;
- a second command for the same stable ID overwrites the first target definition and owned rules.

```go
func TestConfigStoreApplyAttributeEditPreservesPeersAndLastWriteWins(t *testing.T) {
    store := attributeEditFixtureStore(t)
    if _, err := store.applyAttributeEdit(existingEdit("attribute-a", "能量", 10), fixedAttributeID); err != nil { t.Fatal(err) }
    second, err := store.applyAttributeEdit(existingEdit("attribute-a", "热度", 20), fixedAttributeID)
    if err != nil { t.Fatal(err) }
    if second.State.findAttribute("热度").Value != 20 { t.Fatal("last target write did not win") }
    if second.State.findAttribute("B").Value != 2 { t.Fatal("peer was overwritten") }
}
```

- [ ] **Step 5: Run aggregate tests and verify RED**

```bash
go test ./... -run '^TestConfigStoreApplyAttributeEdit|^TestRewriteAttributeReferences' -count=1
```

Expected: compile failures for the missing command/store methods.

- [ ] **Step 6: Implement the deep atomic module**

```go
type attributeEditTarget struct {
    Kind        string `json:"kind"`
    AttributeID string `json:"attributeId,omitempty"`
    LeaseToken  string `json:"leaseToken,omitempty"`
}

type attributeEditCommand struct {
    Target             attributeEditTarget `json:"target"`
    Attribute          attributeState      `json:"attribute"`
    GiftRules          []giftRule          `json:"giftRules"`
    TimerRules         []timerRule         `json:"timerRules"`
    GiftCatalogUpserts []giftInfo           `json:"giftCatalogUpserts"`
}

type attributeEditResult struct {
    State       appState
    ID          string
    Name        string
    Created     bool
    Previous    appState
    PreviousErr error
}
```

`applyAttributeEdit` locks once, reads/clones the latest state, merges, normalizes, validates, and persists before returning. Ignore client-supplied ID/color/template provenance for existing targets; generate the new ID for new targets; reject duplicate final names and ambiguous/duplicate nonempty IDs; return typed input/conflict/not-found errors for Task 2.

Add a private rule-group merge helper keyed by rule ID. Determine target ownership from the current stored name before rename. Rename untouched current references first, then overlay submitted target drafts whose `AttributeName` equals the final name.

- [ ] **Step 7: Add durability-failure and validation tests**

Inject the existing `writeAtomically` failure seam and assert no partial state/reference migration is visible. Add invalid formula, duplicate name/ID, wrong target name in submitted rule, and protected-field overwrite cases.

- [ ] **Step 8: Run Task 1 gates**

```bash
go test ./... -run 'TestRewriteFormulaIdentifier|TestRewriteAttributeReferences|TestConfigStoreApplyAttributeEdit' -count=20
go test -race ./... -run 'TestConfigStoreApplyAttributeEdit' -count=5
go test ./... -count=1 -timeout=300s
```

Expected: all pass.

- [ ] **Step 9: Commit**

```bash
git add goserver/attribute_edits.go goserver/attribute_edits_test.go goserver/config_store.go goserver/formula.go
git commit -m "feat: apply attribute edits atomically"
```

---

### Task 2: Add Strict Session and Submit HTTP Adapters

**Files:**
- Modify: `goserver/attribute_edits.go`
- Modify: `goserver/attribute_edits_test.go`
- Modify: `goserver/attribute_edit_leases.go`
- Modify: `goserver/attribute_edit_leases_test.go`
- Modify: `goserver/main.go`

**Interfaces:**
- Produces: `(*attributeEditLeaseCoordinator).Has(attributeID, token string) bool`.
- Produces: `newAttributeEditService(store, leases, newID) *attributeEditService`.
- Produces: `newAttributeEditHandler(service *attributeEditService) http.Handler` for `/api/attribute-edits/session` and `/api/attribute-edits`.
- Produces session response `{code, attributeId, token, expiresAt, state}` and submit response `{code, target:{id,name,created}, state}`.
- Consumes: Task 1 `applyAttributeEdit` and the existing lease TTL/token validation.

- [ ] **Step 1: Write failing lease-ownership and session tests**

Cover exact-token ownership, expiry cleanup, stable-ID preparation, legacy unique-name ID backfill, ambiguous/missing legacy name, ID persistence failure, and token-generation failure.

```go
func TestAttributeEditSessionBackfillsLegacyIDBeforeLease(t *testing.T) {
    store := attributeEditLegacyFixtureStore(t, "积分")
    service := newAttributeEditService(store, newDefaultAttributeEditLeaseCoordinator(), fixedAttributeID)
    got, err := service.Prepare(attributeEditSessionRequest{LegacyName: "积分"})
    if err != nil { t.Fatal(err) }
    if got.AttributeID != "attribute-fixed" || !service.leases.Has(got.AttributeID, got.Token) {
        t.Fatalf("unexpected session: %#v", got)
    }
    state, _ := store.readState()
    if state.Attributes[0].ID != "attribute-fixed" { t.Fatal("ID was not persisted first") }
}
```

- [ ] **Step 2: Run session tests and verify RED**

```bash
go test ./... -run '^TestAttributeEditSession|^TestAttributeEditLeaseHas' -count=1
```

Expected: compile failure for missing service/ownership methods.

- [ ] **Step 3: Implement session preparation and token ownership**

Add `Has` with the same trim/lock/lazy-expiry discipline as `Renew`/`Release`. Add `configStore.ensureAttributeID(attributeID, legacyName, newID)` that uses the store lock and durable transaction, requiring exactly one selector and exactly one target. Persist first, then call `leases.Create` and return the authoritative state.

Generate IDs from exactly 16 `crypto/rand` bytes encoded with `base64.RawURLEncoding` and prefixed `attribute-`; inject the generator in tests.

- [ ] **Step 4: Write failing strict HTTP tests**

Test both fixed paths with real handlers:

- same-origin pass and `Sec-Fetch-Site: cross-site`/mismatched `Origin` 403;
- POST only, unknown path 404, other method 405 with `Allow: POST`;
- strict `Content-Type: application/json`, unknown fields, trailing JSON, invalid target discriminant, invalid token, and body over `maxConfigBytes`;
- session response includes persisted ID/token/state;
- submit rejects missing/expired/mismatched lease with `409 lease_lost`;
- last-write-wins accepts two valid sessions for the same ID;
- every response uses `Cache-Control: no-store` and a safe stable Chinese message.

- [ ] **Step 5: Implement HTTP adapters and error mapping**

Use one service and one handler; do not duplicate store logic in HTTP code. Validate the live lease immediately before Task 1's locked merge. Map typed errors to `400`, `404`, `409 lease_lost`, `409 name_conflict`, `409 mutations_blocked`, and safe `500` responses. After commit, call `store.notifyStateChanges(result.Previous, result.PreviousErr, result.State)` outside the store lock.

- [ ] **Step 6: Wire one production service**

Replace `registerAttributeEditLeaseRoute` with a helper that registers the heartbeat endpoint plus session/submit paths from the same `attributeEditLeaseCoordinator` and `configStore`. `main` creates exactly one coordinator and one edit service.

- [ ] **Step 7: Run Task 2 gates**

```bash
go test ./... -run 'TestAttributeEditSession|TestAttributeEditHTTP|TestAttributeEditLease|TestRegisterAttributeEdit' -count=20
go test -race ./... -run 'TestAttributeEditSession|TestAttributeEditHTTP|TestAttributeEditLease|TestRegisterAttributeEdit' -count=5
go test ./... -count=1 -timeout=300s
```

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add goserver/attribute_edits.go goserver/attribute_edits_test.go goserver/attribute_edit_leases.go goserver/attribute_edit_leases_test.go goserver/main.go
git commit -m "feat: expose atomic attribute edits"
```

---

### Task 3: Build the Frontend Atomic Edit Adapter

**Files:**
- Create: `src/ui/config/attribute-edit-api.ts`
- Create: `tests/attribute-edit-api.test.ts`
- Modify: `src/ui/config/attribute-edit-lease.ts`
- Modify: `tests/attribute-edit-lease.test.ts`
- Modify: `src/storage.ts`
- Modify: `tests/storage.test.ts`

**Interfaces:**
- Produces: `prepareAttributeEditSession(target, options?): Promise<PreparedAttributeEditSession>`.
- Produces: `submitAttributeEdit(input, options?): Promise<SubmittedAttributeEdit>`.
- Produces: `maintainAttributeEditLease(attributeId, token, options?): AttributeEditLeaseSession` without an acquire POST.
- Produces: `commitAuthoritativeStateMutation(mutation: () => Promise<AppState>): Promise<AppState>` in `storage.ts`.
- Consumes: existing `Attribute`, `GiftRule`, `TimerRule`, `GiftInfo`, `AppState`, and lease heartbeat/retry rules.

- [ ] **Step 1: Write failing strict adapter tests**

```ts
type AttributeEditTarget =
  | { kind: 'existing'; attributeId: string; leaseToken: string }
  | { kind: 'new' };

interface AttributeEditInput {
  target: AttributeEditTarget;
  attribute: Attribute;
  giftRules: GiftRule[];
  timerRules: TimerRule[];
  giftCatalogUpserts: GiftInfo[];
}
```

Verify exact endpoint/body/method, strict session/submit payload parsing, stable errors for non-2xx/malformed bodies, invalid target shapes, and authoritative state return.

- [ ] **Step 2: Run adapter tests and verify RED**

```bash
npm test -- tests/attribute-edit-api.test.ts --reporter=dot
```

Expected: module-not-found failure.

- [ ] **Step 3: Implement transport and prepared lease maintenance**

`prepareAttributeEditSession` posts either `{attributeId}` or `{legacyName}`, validates a 24-character base64url token and state response, and constructs `maintainAttributeEditLease` from the returned token. Reuse bounded heartbeat, 404 reacquire, retry warning, abort, latest-token DELETE, and one-listener behavior; do not issue the old acquire POST for a prepared session.

`submitAttributeEdit` posts the narrow aggregate and returns `{target,state}`. It does not automatically retry timeouts because the server may already have committed.

- [ ] **Step 4: Write failing storage-serialization tests**

Prove an authoritative command waits for pending ordinary saves, publishes the returned normalized state once, updates persisted snapshots, and prevents an earlier queued transaction/reset from republishing stale cache.

```ts
it('serializes an authoritative mutation and publishes only its response', async () => {
  const mutation = commitAuthoritativeStateMutation(async () => serverState);
  await expect(mutation).resolves.toMatchObject({ attributes: serverState.attributes });
  expect(loadState().attributes).toEqual(serverState.attributes);
});
```

- [ ] **Step 5: Implement authoritative mutation serialization**

```ts
export function commitAuthoritativeStateMutation(
  mutation: () => Promise<AppState>,
): Promise<AppState> {
  const transaction = persistQueue.catch(() => undefined).then(async () => {
    const authoritative = normalizeState(await mutation());
    persistedFieldSnapshots = snapshotStateFields(authoritative);
    forcePersistFields.clear();
    return publishCachedState(authoritative);
  });
  persistQueue = transaction.then(() => undefined, () => undefined);
  return transaction;
}
```

Preserve existing revision/supersession checks for ordinary transactions and cover interleavings before accepting the implementation.

- [ ] **Step 6: Run Task 3 gates**

```bash
npm test -- tests/attribute-edit-api.test.ts tests/attribute-edit-lease.test.ts tests/storage.test.ts --reporter=dot
npm run typecheck
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add src/ui/config/attribute-edit-api.ts src/ui/config/attribute-edit-lease.ts src/storage.ts tests/attribute-edit-api.test.ts tests/attribute-edit-lease.test.ts tests/storage.test.ts
git commit -m "feat: add atomic attribute edit client"
```

### Task 4: Migrate the Attribute Editor to Atomic Commands

**Files:**
- Modify: `src/ui/config/config.ts`
- Modify: `tests/wizard.test.ts`

**Depends on:** Tasks 2 and 3.

**Acceptance contract:** Existing attributes open from a server-prepared session and save through the narrow atomic endpoint. New attributes also use that endpoint. The editor must never send the complete `attributes`, `giftRules`, or `timerRules` collections through the legacy `/api/config` PATCH path. Existing time editing, random-range controls, identity conditions, lease warnings, dismissal guards, and guide lifecycle behavior remain unchanged.

- [ ] **Step 1: Add failing open-session tests**

Prove that opening an existing attribute sends exactly one session request using its stable ID; an ID-less legacy attribute uses `legacyName`, receives a server-generated ID, and mounts the authoritative returned value/name/rules; the authoritative response rather than stale cache supplies the draft; and session failure neither mounts the editor nor disposes the guide or leaves a heartbeat/listener.

```ts
expect(requests).toEqual([{
  method: 'POST',
  path: '/api/attribute-edits/session',
  body: { attributeId: 'attribute-countdown' },
}]);
expect(currentEditorValue()).toBe('00:01:30');
```

- [ ] **Step 2: Run the open-session slice and verify RED**

```bash
npm test -- tests/wizard.test.ts --reporter=dot -t "atomic attribute edit session"
```

Expected: the old editor calls the lease endpoint and a separate strict config GET instead of the session endpoint.

- [ ] **Step 3: Replace existing-attribute open with session preparation**

In `openAttributeEditor`, keep save-in-flight/open-generation guards; call `prepareAttributeEditSession` before mounting; resolve the target by returned stable ID; build the detached draft and original snapshot from that target; start maintenance with the prepared token while preserving health changes that precede warning-DOM creation; and dispose the guide only after a successful mount. Delete the old client-side ID persistence, lease POST, strict GET, and re-resolution sequence without a fallback.

- [ ] **Step 4: Add failing narrow-save and authoritative-adoption tests**

For existing and new attributes, assert the submit body contains only the target descriptor, edited attribute, target-owned gift rules, target-owned timer rules, and edited gift-catalog upserts. Assert peer attributes/rules are absent; no legacy config PATCH is sent; the authoritative response replaces local cache and drives the list; renamed references are adopted; server errors keep the dialog/draft alive; ambiguous network failure does not auto-submit; and cancel/X/overlay remain guarded while submit is in flight.

- [ ] **Step 5: Run the save slice and verify RED**

```bash
npm test -- tests/wizard.test.ts --reporter=dot -t "atomic attribute edit save"
```

Expected: the current editor performs a pre-save GET followed by a whole-field config PATCH.

- [ ] **Step 6: Implement the atomic submit path**

Build the narrow command from the detached draft. Existing targets use the session's current token getter so a recovered token is submitted. Call `submitAttributeEdit` inside `commitAuthoritativeStateMutation` and publish only its returned state. On success, close and release the current token. On failure, retain the lease/dialog/draft, show the stable Chinese save error, and permit explicit retry. Remove this editor's pre-save GET, client-side reference rewrite, whole-field candidate, and `saveStateTransaction`; other config editors keep existing storage APIs.

- [ ] **Step 7: Preserve editor behavior with focused regressions**

Cover time formatting/shortcuts, blank numeric validation and minimum zero, negative-to-positive random range, identity/fan-club conditions, lease timeout/404 recovery/warning/release, save-in-flight dismissal, failed-save isolation, guide preservation, and new/existing rename.

```bash
npm test -- tests/wizard.test.ts tests/attribute-edit-lease.test.ts tests/attribute-time.test.ts tests/simple-play.test.ts --reporter=dot
npm run typecheck
```

- [ ] **Step 8: Commit**

```bash
git add src/ui/config/config.ts tests/wizard.test.ts
git commit -m "refactor: save attributes atomically"
```

### Task 5: Prove Concurrent Runtime and Editor Semantics

**Files:**
- Modify: `goserver/attribute_edits_test.go`
- Modify: `goserver/attribute_edits_http_test.go`
- Modify: `tests/wizard.test.ts`
- Modify: `tests/storage.test.ts`

**Depends on:** Tasks 1–4.

**Acceptance contract:** Edits to unfrozen peer attributes that occur before the command enters the backend critical section survive. The existing lease freezes only its target. Two valid saves for the same target are serialized and the later save wins. Tests use channels or deferred promises, never sleeps, to establish order.

- [ ] **Step 1: Add a deterministic peer gift-update race test**

Prepare a lease for A; start its command and block before `configStore.mu`; apply a gift event that changes unfrozen B from 2 to 3; release the command; then assert A contains the edit, B remains 3, peer ordering is current, and one persisted state contains both results.

```bash
go test ./... -run '^TestAttributeEditPreservesConcurrentGiftPeerUpdate$' -count=20 -timeout=120s
```

Expected before atomic merge: the helper is absent or the old full-field save overwrites B. Expected after Tasks 1–4: pass 20/20.

- [ ] **Step 2: Add the timer peer-update race test**

Repeat the same barrier with the timer runtime changing B. Assert no catch-up is applied to frozen A and the peer timer result survives.

```bash
go test ./... -run '^TestAttributeEditPreservesConcurrentTimerPeerUpdate$' -count=20 -timeout=120s
```

- [ ] **Step 3: Add deterministic same-target last-write-wins tests**

Submit two ordered valid commands for the same target under the coordinator's real token rules. Hold command 1 at persistence and queue command 2. Assert command 1 returns its state; command 2 returns the later state; final disk/cache equals command 2 for the target and owned rules; unrelated peer changes between commands survive; and an invalid command 2 leaves command 1 authoritative. Do not introduce original-name conflicts or a global revision precondition.

- [ ] **Step 4: Prove target-only freeze boundaries**

While A has a live session, gift/timer changes targeting A are ignored without catch-up, changes targeting B continue, expiration/release unfreezes A, stale/replaced tokens cannot submit, and a new attribute becomes addressable by its returned stable ID.

```bash
go test ./... -run 'Test(AttributeEdit|AttributeLease|GiftRuleFrozen|TimerRuleFrozen)' -count=20 -timeout=180s
go test -race ./... -run 'Test(AttributeEdit|AttributeLease|GiftRuleFrozen|TimerRuleFrozen)' -count=5 -timeout=180s
```

- [ ] **Step 5: Prove the frontend sends no peer state**

Use A/B fixtures, mutate B in the mocked backend after session preparation, and save A. Assert the command contains only A and A-owned rules, and the returned authoritative B value/order is published. Repeat for new creation.

```bash
npm test -- tests/wizard.test.ts tests/storage.test.ts --reporter=dot -t "atomic attribute edit"
```

- [ ] **Step 6: Run complete Task 5 gates**

```bash
go test ./... -count=1 -timeout=300s
go test -race ./... -count=1 -timeout=300s
npm run typecheck
npm test -- --reporter=dot
git diff --check
```

Expected: all commands exit 0; only documented pre-existing skips remain.

- [ ] **Step 7: Commit**

```bash
git add goserver/attribute_edits_test.go goserver/attribute_edits_http_test.go tests/wizard.test.ts tests/storage.test.ts
git commit -m "test: cover atomic attribute edit races"
```

### Task 6: Verify Packaged UI, Build the EXE, and Record the Release Gate

**Files:**
- Create: `.superpowers/sdd/2026-08-14-atomic-attribute-edit/final-report.md`
- Modify only if a real closure regression is proven: `scripts/build-go.mjs`
- Modify only if a real closure regression is proven: `scripts/build-go.test.mjs`

**Depends on:** Tasks 1–5.

**Acceptance contract:** The executable embeds the complete current UI graph including the atomic edit client. This task does not change version, changelog, tags, remotes, release state, signing, FFmpeg payload, dependencies, or lockfile.

- [ ] **Step 1: Run fresh sequential source gates**

```bash
npm run typecheck
npm test -- --reporter=dot
go test ./... -count=1 -timeout=300s
go test -race ./... -count=1 -timeout=300s
```

Record exact test/pass/skip counts and durations.

- [ ] **Step 2: Build the UI and executable**

```bash
npm run build:ui
npm run build:exe
```

Expected: `dist/gift-panel.exe` is rebuilt from the current tree and the embedded manifest describes the same complete UI asset set.

- [ ] **Step 3: Verify manifest closure and module reachability**

Audit that the hashed module containing `/api/attribute-edits` occurs exactly once in `ui-assets.json` with matching size/SHA-256; the config entry transitively imports it; every manifest path receives HTTP 200 from the static handler; and no source map, Playwright browser, test fixture, FFmpeg test tool, report, or build scratch path enters the UI tree or EXE.

```bash
npm test -- scripts/build-go.test.mjs --reporter=dot
go test ./... -run 'TestEmbedded(UI|Dist|Assets|Manifest)' -count=1 -timeout=120s
```

If actual test names differ, use `rg -n "ui-assets|embedded.*dist|manifest" scripts goserver -g "*test*"` to select the real tests and record that exact command. Do not weaken assertions or add another packaging path.

- [ ] **Step 4: Inspect the final executable artifact**

```powershell
$artifact = Get-Item -LiteralPath dist\gift-panel.exe
$hash = Get-FileHash -Algorithm SHA256 -LiteralPath $artifact.FullName
$artifact.FullName
$artifact.Length
$hash.Hash.ToLowerInvariant()
```

Do not launch over a user-owned instance or stop a running app without fresh explicit authorization. Building and hashing is sufficient unless separately authorized.

- [ ] **Step 5: Complete scope and release-safety audits**

```bash
git diff --check
git status --short
git diff --name-only 1bc9a75..HEAD
git log --oneline --decorate 1bc9a75..HEAD
```

Require zero changes to dependencies/lockfiles, version files, changelog history, workflows, signing scripts, FFmpeg payload/build files, README, tags, remotes, or release state unless separately authorized.

- [ ] **Step 6: Write the final report**

Include fixed base/commit range; per-task RED signature and GREEN output; stable-ID merge/reference-rewrite table; session/lease/submit state machine; deterministic gift/timer peer-race evidence; same-target later-save-wins evidence; source/race/UI/EXE/manifest/SHA/scope/no-release results; and the residual boundary that unsupported external direct shard-file rewrites are outside `configStore` mutation guarantees.

- [ ] **Step 7: Commit only the report or a proven packaging regression fix**

```bash
git add -f .superpowers/sdd/2026-08-14-atomic-attribute-edit/final-report.md
git commit -m "docs: record atomic attribute edit verification"
```

If Step 3 proves a packaging defect, fix and commit it separately with its own RED/GREEN evidence. Otherwise do not touch packaging code.
