# Frontend Display-Only Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the dead TypeScript rule/formula runtime and make Go the only implementation of blind-box aggregation while preserving current OBS/config behavior.

**Architecture:** First lock the migrated rule and formula semantics in Go tests, then delete the unused TypeScript evaluator and engine. Add a deep Go leaderboard module behind one read-only HTTP seam, expose it through a strict `src/backend.ts` adapter, and migrate both frontend consumers through a shared abort/generation resource so UI code only formats and renders authoritative snapshots.

**Tech Stack:** Go 1.26, `net/http`, `golang.org/x/text/collate` v0.41.0, TypeScript, Vite, Vitest, browser DOM test harness.

## Global Constraints

- Scope is limited to design stages 0, 1, and 2.1; do not modify config-import semantics or add SSE/state-push behavior.
- Go HTTP is the only authoritative external seam; UI modules access it only through `src/backend.ts`.
- Preserve gameplay templates, formula presets, training, simple-play draft validation, gift-clip studio, display formatting/themes/scenes, and broadcast queue behavior.
- Keep `src/formula/tokenizer.ts`, `parser.ts`, and `errors.ts`; `src/formula/index.ts` must retain only `collectVars` and `FormulaError`.
- Do not keep a TypeScript fallback for blind-box summary, scope aggregation, per-scope projection, or viewer ranking.
- `limit` truncates only viewer rows; summary and scopes always cover the full eligible ledger.
- Preserve viewer ordering: profit, value, count, and last-gift time descending, then stable ledger order.
- Preserve scope ordering: total count and latest time descending, then Chinese collation by name and gift ID ascending.
- Every task uses TDD, has an independent commit, and receives a read-only spec/quality review before the next task.
- Do not touch version metadata, release workflow, FFmpeg/gift-clip files, configuration-import code, or the optional 750ms polling-to-SSE work.

---

### Task 1: Lock legacy rule and formula semantics in Go

**Files:**
- Create: `goserver/background_runtime_semantics_test.go`
- Modify: `goserver/formula_test.go`
- Modify only if a RED test exposes a real parity defect: `goserver/background_runtime.go`, `goserver/formula.go`, `goserver/state.go`

**Interfaces:**
- Consumes: `applyGiftEvent(*appState, giftEvent)`, `applyTimerRules(*appState, []string, time.Time) int`, `evaluateFormula(string, map[string]float64) (float64, error)`.
- Produces: a complete Go-side semantic safety net that permits deletion of `tests/engine.test.ts` and evaluator-specific `tests/formula.test.ts` cases.

- [ ] **Step 1: Add focused rule-parity tests**

Create `goserver/background_runtime_semantics_test.go` with deterministic helpers and individual tests. Use `time.Now().Format("2006-01-02")` only to locate the bucket produced by `todayStats`; use fixed gift timestamps for log ordering.

```go
package main

import (
    "fmt"
    "testing"
    "time"
)

func float64Pointer(value float64) *float64 { return &value }
func intPointer(value int) *int             { return &value }

func semanticState(rule giftRule, initial float64) appState {
    state := defaultAppState()
    state.Attributes = []attributeState{{Name: "积分", Value: initial}}
    state.Rules = []giftRule{rule}
    return state
}

func TestApplyGiftEventHonorsMinimumPrice(t *testing.T) {
    state := semanticState(giftRule{
        ID: "priced", GiftID: 1, AttributeName: "积分",
        Formula: "积分+1", MinPrice: float64Pointer(100),
    }, 0)
    applyGiftEvent(&state, giftEvent{GiftID: 1, Price: 99, Num: 1, Rnd: "low"})
    applyGiftEvent(&state, giftEvent{GiftID: 1, Price: 100, Num: 1, Rnd: "equal"})
    if got := state.Attributes[0].Value; got != 1 {
        t.Fatalf("value = %v, want 1", got)
    }
}

func TestApplyGiftEventRepeatsQuantityWithoutExceedingDailyLimit(t *testing.T) {
    state := semanticState(giftRule{
        ID: "limited", GiftID: 1, AttributeName: "积分",
        Formula: "积分+1", DailyLimit: intPointer(2),
    }, 0)
    applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 3, Rnd: "batch"})
    today := time.Now().Format("2006-01-02")
    if got := state.Attributes[0].Value; got != 2 {
        t.Fatalf("value = %v, want 2", got)
    }
    if got := state.Stats[today].GiftTotals["1"]; got != 3 {
        t.Fatalf("gift total = %d, want 3", got)
    }
    if got := state.Stats[today].RuleTriggers["limited"]; got != 2 {
        t.Fatalf("rule triggers = %d, want 2", got)
    }
}
```

Add separate tests named exactly:

- `TestApplyGiftEventCapsGrowthButAllowsDecrease`
- `TestApplyGiftEventSkipsInvalidFormulaAndContinues`
- `TestApplyGiftEventKeepsNewestTwoHundredGiftLogs`
- `TestApplyTimerRulesKeepsNewestTwoHundredTimerLogs`
- `TestApplyGiftEventCreatesTodayBucketWithoutMutatingHistoricalBucket`

For log tests, generate `maxLogEntries+5` distinct events/rules, assert length 200, newest timestamp first, and oldest retained timestamp equals the sixth inserted event. For the invalid-formula test, put a bad rule before a valid rule and assert only the valid attribute changes and only its log entry is produced. For cap behavior, start at 9 with cap 10 and formula `积分+5`, then change the formula to `积分-3` while value is 10 and assert the result is 7.

- [ ] **Step 2: Run the rule tests and record RED or parity evidence**

Run:

```powershell
Push-Location goserver
go test ./... -run 'TestApplyGiftEventHonorsMinimumPrice|TestApplyGiftEventRepeatsQuantityWithoutExceedingDailyLimit|TestApplyGiftEventCapsGrowthButAllowsDecrease|TestApplyGiftEventSkipsInvalidFormulaAndContinues|TestApplyGiftEventKeepsNewestTwoHundredGiftLogs|TestApplyTimerRulesKeepsNewestTwoHundredTimerLogs|TestApplyGiftEventCreatesTodayBucketWithoutMutatingHistoricalBucket' -count=1
Pop-Location
```

Expected: existing production behavior may already make these tests GREEN. If so, record that this task is test migration rather than a product fix. If any test is RED, change only the smallest relevant production function and rerun the single failing test before proceeding.

- [ ] **Step 3: Expand formula parity and error coverage**

Extend `goserver/formula_test.go` with table-driven success/error tests:

```go
func TestFormulaLegacyEvaluatorCoverage(t *testing.T) {
    env := map[string]float64{"price": 1000, "加班时间": 100}
    successes := map[string]float64{
        "IF(price>1000,10,1)":             1,
        "MAX(1,5,3)":                      5,
        "MAX(IF(price>500,100,0),50)":     100,
    }
    for formula, want := range successes {
        got, err := evaluateFormula(formula, env)
        if err != nil || math.Abs(got-want) > 0.000001 {
            t.Fatalf("%s = %v, %v; want %v", formula, got, err, want)
        }
    }
    for _, formula := range []string{"1/0", "foo+1", "count+1", "(1+2", "1+2 abc", "1 +"} {
        if _, err := evaluateFormula(formula, env); err == nil {
            t.Fatalf("%s unexpectedly accepted", formula)
        }
    }
}

func TestFormulaRandIsHalfOpenUnitInterval(t *testing.T) {
    for range 200 {
        got, err := evaluateFormula("RAND()", map[string]float64{})
        if err != nil || got < 0 || got >= 1 {
            t.Fatalf("RAND() = %v, %v", got, err)
        }
    }
}
```

Keep the existing `RANDBETWEEN` integer/range test and `FormulaError`-equivalent position/error assertions already provided by Go parser errors. Do not compare localized error strings character-for-character across languages.

- [ ] **Step 4: Run focused and full Go verification**

Run:

```powershell
Push-Location goserver
go test ./... -run 'TestApplyGiftEvent|TestApplyTimerRules|TestFormula' -count=10
go test -race ./... -run 'TestApplyGiftEvent|TestApplyTimerRules|TestFormula' -count=3
go test ./... -count=1 -timeout=300s
Pop-Location
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit Task 1**

```powershell
git add -- goserver/background_runtime_semantics_test.go goserver/formula_test.go goserver/background_runtime.go goserver/formula.go goserver/state.go
git diff --cached --check
git commit -m "test: lock backend rule semantics"
```

Stage production files only if Step 2 required a verified parity fix.

---

### Task 2: Delete the dead TypeScript engine and evaluator

**Files:**
- Delete: `src/engine/index.ts`
- Delete: `src/engine/rules.ts`
- Delete: `src/formula/evaluator.ts`
- Delete: `tests/engine.test.ts`
- Modify: `src/formula/index.ts`
- Modify: `tests/formula.test.ts`
- Modify: `tests/gameplay-templates.test.ts`

**Interfaces:**
- Consumes: Task 1 Go safety net; `collectVars(string): string[]`; `FormulaError`.
- Produces: no frontend runtime rule evaluation; template tests verify parseability and allowed variable references without evaluating formulas.

- [ ] **Step 1: Write a failing public-interface test before deletion**

At the top of `tests/formula.test.ts`, import the real module namespace and add:

```ts
import { describe, expect, it } from 'vitest';
import * as formulaModule from '../src/formula';

describe('formula public interface', () => {
  it('exposes draft reference collection without a runtime evaluator', () => {
    expect(Object.keys(formulaModule).sort()).toEqual(['FormulaError', 'collectVars']);
  });
});
```

This exercises the runtime module interface rather than grepping source text. File deletion remains a typecheck/build/`rg` gate, not a shipped change-detector test.

- [ ] **Step 2: Run the public-interface test to verify RED**

Run:

```powershell
npx vitest run tests/formula.test.ts --reporter=dot
```

Expected: FAIL because the actual module namespace still exports `evalFormula`.

- [ ] **Step 3: Remove evaluator exports and preserve `collectVars`**

Replace `src/formula/index.ts` with:

```ts
import { AstNode, parse } from './parser';

export { FormulaError } from './errors';

export function collectVars(input: string): string[] {
  const ast = parse(input);
  const vars = new Set<string>();
  const walk = (node: AstNode): void => {
    if (node.kind === 'var') vars.add(node.name);
    else if (node.kind === 'call') node.args.forEach(walk);
    else if (node.kind === 'binary') { walk(node.left); walk(node.right); }
    else if (node.kind === 'unary') walk(node.operand);
  };
  walk(ast);
  return [...vars];
}
```

Delete the three production files and `tests/engine.test.ts` listed above.

- [ ] **Step 4: Rewrite the surviving formula/template tests**

Reduce `tests/formula.test.ts` to parser-backed `collectVars` and `FormulaError` cases:

```ts
import { describe, expect, it } from 'vitest';
import { collectVars, FormulaError } from '../src/formula';

describe('formula draft references', () => {
  it('collects unique variables from nested expressions', () => {
    expect(collectVars('MAX(IF(price>500, 加班时间, 0), 加班时间)').sort())
      .toEqual(['price', '加班时间'].sort());
  });

  it('keeps parser position errors for draft feedback', () => {
    let caught: unknown;
    try { collectVars('1 +'); } catch (error) { caught = error; }
    expect(caught).toBeInstanceOf(FormulaError);
    expect((caught as FormulaError).pos).toBe(3);
  });
});
```

In `tests/gameplay-templates.test.ts`, replace the `evalFormula` import with `collectVars`. Rename the test to `builds every template without hard-coded gift IDs and with parseable formulas`. For every generated gift/timer formula and condition:

```ts
const allowedVariables = new Set(['price', ...result.attributes.map((attribute) => attribute.name)]);
for (const formula of [
  ...result.rules.map((rule) => rule.formula),
  ...result.timerRules.flatMap((rule) => [rule.formula, ...(rule.condition ? [rule.condition] : [])]),
]) {
  expect(collectVars(formula).every((name) => allowedVariables.has(name))).toBe(true);
}
```

This still parses every generated expression while leaving execution semantics solely in Go.

- [ ] **Step 5: Run the deletion gate**

Run:

```powershell
npx vitest run tests/formula.test.ts tests/gameplay-templates.test.ts --reporter=dot
npm run typecheck
npm test -- --reporter=dot
rg -n "evalFormula|applyGiftToState|recordGiftTotals|resetTodayStats" src tests
git diff --check
```

Expected: Vitest/typecheck/full tests exit 0 and `rg` returns no matches.

- [ ] **Step 6: Commit Task 2**

```powershell
git add -A -- src/engine src/formula tests/engine.test.ts tests/formula.test.ts tests/gameplay-templates.test.ts
git diff --cached --check
git commit -m "refactor: remove frontend rule evaluator"
```

---

### Task 3: Add the pure Go blind-box leaderboard module

**Files:**
- Create: `goserver/blind_box_leaderboard.go`
- Create: `goserver/blind_box_leaderboard_test.go`
- Modify: `goserver/go.mod`
- Modify: `goserver/go.sum`

**Interfaces:**
- Consumes: `contributionLedgerState`, `viewerContribution`, `blindBoxContribution` from `goserver/state.go`.
- Produces: `buildBlindBoxLeaderboard(contributionLedgerState, blindBoxLeaderboardQuery) blindBoxLeaderboardSnapshot` and the JSON result types used by Task 4.

- [ ] **Step 1: Write the migrated leaderboard tests**

Create `goserver/blind_box_leaderboard_test.go` with a fixture equivalent to the deleted TypeScript test and these exact test names:

```go
func TestBuildBlindBoxLeaderboardSummarizesAllBoxes(t *testing.T)
func TestBuildBlindBoxLeaderboardLimitDoesNotChangeSummaryOrScopes(t *testing.T)
func TestBuildBlindBoxLeaderboardProjectsOneGift(t *testing.T)
func TestBuildBlindBoxLeaderboardHandlesUnpricedAndEmptyRows(t *testing.T)
func TestBuildBlindBoxLeaderboardSortsViewersStably(t *testing.T)
func TestBuildBlindBoxLeaderboardSortsScopesWithChineseCollation(t *testing.T)
func TestBuildBlindBoxLeaderboardUsesLatestScopeName(t *testing.T)
func TestBuildBlindBoxLeaderboardNormalizesInvalidNumbers(t *testing.T)
```

The first three assertions must preserve the existing values:

```go
if got := snapshot.Summary; got != (blindBoxLeaderboardSummary{
    ViewerCount: 2, BlindBoxCount: 3, Cost: 27000,
    Value: 29000, Profit: 2000, UnpricedCount: 1,
}) {
    t.Fatalf("summary = %#v", got)
}
if names := []string{snapshot.Viewers[0].Uname, snapshot.Viewers[1].Uname}; !reflect.DeepEqual(names, []string{"盈利观众", "亏损观众"}) {
    t.Fatalf("viewer order = %#v", names)
}
```

Use `Limit: 1, HasLimit: true` to prove the summary still has two viewers. Use `GiftID: 35800` to assert scoped totals 2/18000/16000/-2000 and projected viewer profits 3000 then -5000. Use equal viewer metrics to prove `sort.SliceStable` preserves ledger order. Use Chinese names whose `collate.New(language.Chinese)` order differs from raw UTF-8 byte order, and add gift ID as the final deterministic tie-breaker.

- [ ] **Step 2: Run the pure-module test to verify RED**

Run:

```powershell
Push-Location goserver
go test ./... -run '^TestBuildBlindBoxLeaderboard' -count=1
Pop-Location
```

Expected: compile failure because the leaderboard types/function do not exist.

- [ ] **Step 3: Pin the Chinese-collation dependency**

Run:

```powershell
Push-Location goserver
go get golang.org/x/text@v0.41.0
Pop-Location
```

Review `go.mod` and `go.sum`; the direct requirement must be exactly `golang.org/x/text v0.41.0`. Do not use a floating version.

- [ ] **Step 4: Implement the deep pure module**

Create `goserver/blind_box_leaderboard.go` with these result types:

```go
type blindBoxLeaderboardQuery struct {
    GiftID   int
    Limit    int
    HasLimit bool
}

type blindBoxLeaderboardSummary struct {
    ViewerCount  int     `json:"viewerCount"`
    BlindBoxCount int    `json:"blindBoxCount"`
    Cost         float64 `json:"cost"`
    Value        float64 `json:"value"`
    Profit       float64 `json:"profit"`
    UnpricedCount int    `json:"unpricedCount"`
}

type blindBoxLeaderboardScope struct {
    GiftID     int    `json:"giftId"`
    GiftName   string `json:"giftName"`
    Count      int    `json:"count"`
    LastGiftAt int64  `json:"lastGiftAt"`
}

type blindBoxLeaderboardSnapshot struct {
    UpdatedAt int64                       `json:"updatedAt"`
    Summary   blindBoxLeaderboardSummary  `json:"summary"`
    Viewers   []viewerContribution        `json:"viewers"`
    Scopes    []blindBoxLeaderboardScope   `json:"scopes"`
}
```

Implement `buildBlindBoxLeaderboard` by:

1. Building all scopes from normalized positive breakdowns.
2. Projecting each eligible viewer either from global blind-box totals or one matching breakdown.
3. Recomputing projected profit as `value-cost`; never trust persisted `Profit` fields.
4. Summing the complete eligible set before limiting rows.
5. Using `sort.SliceStable` for viewers.
6. Creating a local `collate.New(language.Chinese)` for scope-name comparison so concurrent HTTP calls do not share mutable collation state.
7. Returning non-nil empty `viewers` and `scopes` slices.

Use helpers that normalize NaN, infinities, negative amounts, negative counts, and negative timestamps to zero. Do not mutate the input ledger or nested slices/maps.

- [ ] **Step 5: Run focused, race, and full Go tests**

Run:

```powershell
Push-Location goserver
go test ./... -run '^TestBuildBlindBoxLeaderboard' -count=20
go test -race ./... -run '^TestBuildBlindBoxLeaderboard' -count=5
go test ./... -count=1 -timeout=300s
Pop-Location
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit Task 3**

```powershell
git add -- goserver/blind_box_leaderboard.go goserver/blind_box_leaderboard_test.go goserver/go.mod goserver/go.sum
git diff --cached --check
git commit -m "feat: derive blind box leaderboard in backend"
```

---

### Task 4: Expose the leaderboard through one read-only HTTP seam

**Files:**
- Create: `goserver/blind_box_leaderboard_http.go`
- Create: `goserver/blind_box_leaderboard_http_test.go`
- Modify: `goserver/main.go`

**Interfaces:**
- Consumes: Task 3 `buildBlindBoxLeaderboard`; `configStore.readState()`; `diagnosticLogger.Error`.
- Produces: `GET /api/blind-box/leaderboard?giftId=&limit=` with `{"code":0,"leaderboard":...}`.

- [ ] **Step 1: Write handler contract tests**

Create table-driven tests for:

```go
func TestBlindBoxLeaderboardHTTPReturnsAuthoritativeSnapshot(t *testing.T)
func TestBlindBoxLeaderboardHTTPAcceptsZeroLimitWithoutTruncatingSummary(t *testing.T)
func TestBlindBoxLeaderboardHTTPRejectsInvalidQueries(t *testing.T)
func TestBlindBoxLeaderboardHTTPRejectsDuplicateQueries(t *testing.T)
func TestBlindBoxLeaderboardHTTPAllowsOnlyGet(t *testing.T)
func TestBlindBoxLeaderboardHTTPHidesStoreErrorsAndRecordsCause(t *testing.T)
```

For invalid query tests, cover:

```go
[]string{
    "?giftId=", "?giftId=0", "?giftId=-1", "?giftId=1.5",
    "?limit=", "?limit=-1", "?limit=2001", "?limit=1.5",
    "?giftId=1&giftId=2", "?limit=1&limit=2",
}
```

Assert success has `Cache-Control: no-store`. Assert POST gets 405 and `Allow: GET`. For store failure, use a config path whose parent is a regular file, assert the body is exactly the stable public message `排行榜读取失败，请重试。`, does not contain the path/cause, and the temporary diagnostic log contains event `blind_box_leaderboard_read_failed`.

- [ ] **Step 2: Run the handler tests to verify RED**

Run:

```powershell
Push-Location goserver
go test ./... -run '^TestBlindBoxLeaderboardHTTP' -count=1
Pop-Location
```

Expected: compile failure because `handleBlindBoxLeaderboard` does not exist.

- [ ] **Step 3: Implement strict query parsing and handler**

Use this internal interface:

```go
func handleBlindBoxLeaderboard(store *configStore, diagnostics *diagnosticLogger) http.HandlerFunc
```

Implement a helper that checks `r.URL.Query()[name]` length directly so absent, empty, and repeated parameters are distinct. Parse base-10 integers with `strconv.Atoi`; `giftId` must be positive when present; `limit` must be 0–2000 when present.

On store failure:

```go
if diagnostics != nil {
    diagnostics.Error("blind_box_leaderboard_read_failed", "error", err)
}
writeJSON(w, http.StatusInternalServerError, map[string]any{
    "code": -1, "message": "排行榜读取失败，请重试。",
})
```

On success, set `Cache-Control: no-store`, build from the one state snapshot, and return the Task 3 snapshot. Do not accept request bodies or client-provided ledgers.

- [ ] **Step 4: Register the route**

In `goserver/main.go`, immediately after `/api/contributions`, add:

```go
mux.HandleFunc("/api/blind-box/leaderboard", handleBlindBoxLeaderboard(store, diagnostics))
```

Do not alter the existing `/api/blind-box` metadata endpoint.

- [ ] **Step 5: Verify handler and main integration**

Run:

```powershell
Push-Location goserver
go test ./... -run '^TestBlindBoxLeaderboardHTTP' -count=20
go test -race ./... -run '^TestBlindBoxLeaderboardHTTP' -count=5
go test ./... -count=1 -timeout=300s
Pop-Location
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit Task 4**

```powershell
git add -- goserver/blind_box_leaderboard_http.go goserver/blind_box_leaderboard_http_test.go goserver/main.go
git diff --cached --check
git commit -m "feat: expose blind box leaderboard snapshot"
```

---

### Task 5: Add the strict frontend adapter and shared request resource

**Files:**
- Modify: `src/backend.ts`
- Create: `src/blind-box-leaderboard-resource.ts`
- Modify: `tests/backend.test.ts`
- Create: `tests/blind-box-leaderboard-resource.test.ts`

**Interfaces:**
- Consumes: Task 4 wire response.
- Produces: `getBlindBoxLeaderboard(options)` and `createBlindBoxLeaderboardResource(load?)` for both UI consumers.

- [ ] **Step 1: Write adapter RED tests**

Add `getBlindBoxLeaderboard` imports/tests to `tests/backend.test.ts`. Verify:

```ts
const signal = new AbortController().signal;
await getBlindBoxLeaderboard({ giftId: 35800, limit: 100, signal });
expect(fetchMock).toHaveBeenCalledWith(
  '/api/blind-box/leaderboard?giftId=35800&limit=100',
  { cache: 'no-store', signal },
);
```

Add invalid payload cases for unknown top-level fields, non-array viewers/scopes, negative/non-finite counts/cost/value/timestamps, non-finite profit, odd viewer shapes, invalid gift IDs, and non-finite/negative `updatedAt`. Negative profit is valid and must be accepted. Assert the adapter rejects invalid payloads with `盲盒排行榜响应无效` rather than returning partially normalized data.

- [ ] **Step 2: Write request-resource RED tests**

Create `tests/blind-box-leaderboard-resource.test.ts` with deferred promises. Define expected behavior:

```ts
const resource = createBlindBoxLeaderboardResource(load);
const first = resource.refresh({ giftId: 1 });
const second = resource.refresh({ giftId: 2 });
resolveSecond(snapshot2);
await expect(second).resolves.toEqual({ status: 'applied', snapshot: snapshot2 });
resolveFirst(snapshot1);
await expect(first).resolves.toEqual({ status: 'stale' });
expect(resource.current()).toBe(snapshot2);
```

Also test:

- the second refresh aborts the first signal;
- a network failure returns `{status:'failed', error, snapshot:lastSuccess}`;
- abort caused by replacement returns `stale`, not a visible failure;
- a later request carrying a lower `updatedAt` returns `stale` and keeps the current snapshot;
- `cancel()` aborts the active request, increments generation, and prevents later application;
- `clear()` forgets the last snapshot only when the caller explicitly requests it.

- [ ] **Step 3: Run focused tests to verify RED**

Run:

```powershell
npx vitest run tests/backend.test.ts tests/blind-box-leaderboard-resource.test.ts --reporter=dot
```

Expected: compile/test failure because the adapter and resource do not exist.

- [ ] **Step 4: Implement strict wire types and validation**

In `src/backend.ts`, export:

```ts
export interface BlindBoxLeaderboardSummary {
  viewerCount: number;
  blindBoxCount: number;
  cost: number;
  value: number;
  profit: number;
  unpricedCount: number;
}

export interface BlindBoxLeaderboardScope {
  giftId: number;
  giftName: string;
  count: number;
  lastGiftAt: number;
}

export interface BlindBoxLeaderboardSnapshot {
  updatedAt: number;
  summary: BlindBoxLeaderboardSummary;
  viewers: ViewerContribution[];
  scopes: BlindBoxLeaderboardScope[];
}

export async function getBlindBoxLeaderboard(options: {
  giftId?: number;
  limit?: number;
  signal?: AbortSignal;
} = {}): Promise<BlindBoxLeaderboardSnapshot>
```

Import `ViewerContribution` from `src/types.ts`. Use strict plain-object/key-set helpers; do not call the permissive storage hydration normalizer. Reject extra fields on summary/scope/snapshot wire objects. Validate viewer fields required by the renderer, including nested blind-box projections, as finite non-negative values except profit, which may be negative. Build query parameters in fixed `giftId`, then `limit` order.

- [ ] **Step 5: Implement the shared generation/abort resource**

In `src/blind-box-leaderboard-resource.ts`, export:

```ts
export type BlindBoxLeaderboardLoadResult =
  | { status: 'applied'; snapshot: BlindBoxLeaderboardSnapshot }
  | { status: 'stale' }
  | { status: 'failed'; error: unknown; snapshot?: BlindBoxLeaderboardSnapshot };

export interface BlindBoxLeaderboardResource {
  refresh(options?: { giftId?: number; limit?: number }): Promise<BlindBoxLeaderboardLoadResult>;
  current(): BlindBoxLeaderboardSnapshot | undefined;
  clear(): void;
  cancel(): void;
}

export function createBlindBoxLeaderboardResource(
  load: typeof getBlindBoxLeaderboard = getBlindBoxLeaderboard,
): BlindBoxLeaderboardResource
```

Each `refresh` increments a generation, aborts the previous controller, and passes the new signal to `load`. Apply a result only when its generation still matches. Treat only replacement/cancel aborts as stale; a caller-visible abort not initiated by the resource remains a failure. `cancel()` must not discard `current()`; `clear()` must cancel and discard it.

- [ ] **Step 6: Run focused, type, and full frontend gates**

Run:

```powershell
npx vitest run tests/backend.test.ts tests/blind-box-leaderboard-resource.test.ts --reporter=dot
npm run typecheck
npm test -- --reporter=dot
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 7: Commit Task 5**

```powershell
git add -- src/backend.ts src/blind-box-leaderboard-resource.ts tests/backend.test.ts tests/blind-box-leaderboard-resource.test.ts
git diff --cached --check
git commit -m "feat: add blind box leaderboard client"
```

---

### Task 6: Migrate OBS and config consumers, then delete TS aggregation

**Files:**
- Modify: `src/ui/display/blind-box-display.ts`
- Modify: `src/ui/config/config.ts`
- Delete: `src/blind-box-leaderboard.ts`
- Delete: `tests/blind-box-leaderboard.test.ts`
- Modify: `tests/display-layout.test.ts`
- Modify: `tests/blind-box-display-scroll.test.ts`
- Modify: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: Task 5 `BlindBoxLeaderboardResource` and authoritative snapshot.
- Produces: both UI surfaces render only backend-derived blind-box data; no TypeScript blind-box aggregation remains.

- [ ] **Step 1: Add OBS async-state RED tests**

Extend `tests/blind-box-display-scroll.test.ts` with a sibling describe using fake timers and a fake resource adapter. Cover:

- first successful snapshot renders backend summary/viewer order;
- a failed refresh preserves the last successful rows and marks connection error;
- a later success clears the error;
- changing `giftId`/late completion cannot overwrite the current scope;
- `beforeunload` calls `cancel()` and clears scroll timers;
- local `state.contributions` values that disagree with the backend snapshot never appear in blind-box output.

Preserve the existing two-argument production call while adding an optional test seam:

```ts
export interface BlindBoxDisplayDependencies {
  createLeaderboardResource: typeof createBlindBoxLeaderboardResource;
}

export function mountBlindBoxDisplay(
  root: HTMLElement,
  blindBoxGiftId?: number,
  dependencies: BlindBoxDisplayDependencies = {
    createLeaderboardResource: createBlindBoxLeaderboardResource,
  },
): void
```

The test passes a fake factory through this third argument; production callers remain unchanged.

Run the contradictory local-ledger/backend-snapshot test before production edits. Expected: FAIL because the current display has no injected resource and derives rows from `state.contributions`.

- [ ] **Step 2: Migrate `blind-box-display.ts`**

Replace imports of the deleted module with Task 5 resource/types. Create one resource per mount. The initial render uses a non-authoritative empty visual state with the requested scope name fallback and no calculated totals; every 750ms cycle performs both existing config/runtime refresh and a leaderboard resource refresh with `{giftId: blindBoxGiftId, limit: 100}`.

Apply only `status:'applied'`. For `status:'failed'`, keep `resource.current()` and set the connection indicator to error. Do not derive from `state.contributions`. Include snapshot `updatedAt`, summary, scopes, and viewer identity in `displaySignature` so new authoritative data triggers render. On `beforeunload`, cancel the resource and stop existing timers.

- [ ] **Step 3: Add config-page race and failure RED tests**

Update the contribution section tests in `tests/wizard.test.ts` to use fetch responses for:

1. the unscoped snapshot and two scopes;
2. a deferred scope A response followed by immediate scope B success;
3. scope A resolving late without changing B text;
4. request failure preserving the last successful summary and showing retry feedback;
5. clear-ledger success followed by a fresh empty leaderboard request;
6. a contradictory `state.contributions` fixture proving blind-box rows come from the response.

Keep existing assertions for scope icons, summary text, CSS structure, contribution mode, and rule-hit mode.

Run the contradictory local-ledger/backend-snapshot test before changing `config.ts`. Expected: FAIL because the current page renders the local ledger instead of the backend snapshot.

- [ ] **Step 4: Migrate `config.ts`**

Create one leaderboard resource in config-page lifetime state and cancel it during page teardown. `renderContributionLeaderboard` keeps contribution/rule tab behavior unchanged, but reads blind-box summary/scopes/viewers only from the resource snapshot.

When opening the section, request the unscoped complete snapshot. When selecting a scope, call `refresh({giftId})`, render a loading class/accessible status while retaining the last successful content, and apply only the resource's `applied` result. After `clearContributionLedger`, call `resource.clear()` and request a fresh unscoped snapshot; do not manually create a blind-box summary. On failure, render stable retry text and keep the previous snapshot.

Move no config-import, gift-clip, save, or general state-refresh code in this step.

- [ ] **Step 5: Delete the old aggregation module and migrate tests**

Delete `src/blind-box-leaderboard.ts` and `tests/blind-box-leaderboard.test.ts`. Their semantic assertions now live in Task 3 Go tests; do not preserve a private copy under another name.

Remove the old blind-box source-string expectations from `tests/display-layout.test.ts`; do not replace them with new source-string assertions. Runtime adapter/resource/UI behavior tests and the `rg` gate provide the verification.

- [ ] **Step 6: Run focused frontend RED/GREEN gates**

Run:

```powershell
npx vitest run tests/display-layout.test.ts tests/blind-box-display-scroll.test.ts tests/wizard.test.ts --reporter=dot
npm run typecheck
npm test -- --reporter=dot
npm run build:ui
rg -n "buildBlindBoxLeaderboard|listBlindBoxLeaderboardScopes|evalFormula|applyGiftToState|recordGiftTotals|resetTodayStats" src tests
git diff --check
```

Expected: all test/build commands exit 0 and `rg` returns no matches.

- [ ] **Step 7: Commit Task 6**

```powershell
git add -A -- src/ui/display/blind-box-display.ts src/ui/config/config.ts src/blind-box-leaderboard.ts tests/blind-box-leaderboard.test.ts tests/display-layout.test.ts tests/blind-box-display-scroll.test.ts tests/wizard.test.ts
git diff --cached --check
git commit -m "refactor: render backend blind box leaderboard"
```

---

### Task 7: Final integration, packaged UI, and behavioral review

**Files:**
- Create: `goserver/frontend_display_only_e2e_test.go`
- Create: `scripts/verify-frontend-display-only.mjs`
- Modify: `package.json`
- Create: `.superpowers/sdd/2026-08-14-frontend-display-only-refactor/final-report.md` (force-add only if the user requires the report committed)

**Interfaces:**
- Consumes: all prior task commits.
- Produces: verified branch ready for final two-axis code review and user integration decision.

- [ ] **Step 1: Add a real packaged-UI harness**

Create `goserver/frontend_display_only_e2e_test.go` with `TestFrontendDisplayOnlyHarnessServer`. The test runs only when `FRONTEND_DISPLAY_E2E_PORT_FILE` and `FRONTEND_DISPLAY_E2E_STOP_FILE` are set; otherwise it calls `t.Skip`. It must:

1. create a `configStore` under `t.TempDir()` and persist a fixture containing two viewers, two blind-box scopes, one attribute, one gift target, one activity, and one timer rule;
2. bind `tcp4` on `127.0.0.1:0`, rejecting any allocated port in 12450–12459 and retrying until outside that protected range;
3. register the real `store.handle`, `handleContributionLedger`, and `handleBlindBoxLeaderboard` handlers;
4. serve the already mirrored `goserver/dist` tree for `/` and use stable minimal JSON handlers for `/api/runtime`, `/api/auth/status`, `/api/changelog`, and `/api/update` so packaged config/OBS startup has no 404s;
5. write the selected port and current PID to the private port file;
6. stop only after the private stop file appears or the test context is canceled;
7. call `server.Shutdown`, close the listener, and leave all temp paths to `t.TempDir` cleanup.

Create `scripts/verify-frontend-display-only.mjs` by following the process ownership pattern in `scripts/verify-gift-clip-export.mjs`: use randomized temp port/stop files, start exactly one Go harness process, launch the repository-pinned Playwright Chromium, and in `finally` stop only recorded child PIDs/process trees and delete only the randomized temp root.

The browser assertions are exact:

- `/?mode=blind-box` renders the global backend summary and server-provided viewer order;
- `/?mode=blind-box&giftId=35800` renders only that scope and its projected values;
- `/?mode=config` opens the contribution section, lists both scopes, changes to gift 35800, and shows the scoped summary;
- clearing the contribution ledger through the real DELETE handler causes the next leaderboard GET to show zero viewers/boxes;
- packaged config dynamic chunk loads, all HTTP responses are below 400, console error count is zero, and document/dialog horizontal overflow is zero;
- screenshots are saved for global OBS, scoped OBS, config global, config scoped, and config cleared states.

Add to `package.json`:

```json
"verify:frontend-display-only": "node scripts/verify-frontend-display-only.mjs"
```

Do not modify `package-lock.json`; this task adds no dependency.

- [ ] **Step 2: Run the harness tests RED then GREEN**

Before implementing the Go harness/script, run:

```powershell
Push-Location goserver
go test ./... -run '^TestFrontendDisplayOnlyHarnessServer$' -count=1
Pop-Location
node scripts/verify-frontend-display-only.mjs
```

Expected RED: test/script missing. After implementation, do not run the browser verifier until `build:exe` has mirrored the packaged asset tree; first run only the Go compile gate:

```powershell
Push-Location goserver
go test ./... -run '^TestFrontendDisplayOnlyHarnessServer$' -count=1
Pop-Location
node --check scripts/verify-frontend-display-only.mjs
```

Expected GREEN: the env-gated Go test skips cleanly and Node syntax exits 0.

- [ ] **Step 3: Run the complete sequential gate**

Run in this exact order:

```powershell
npm run typecheck
npm test -- --reporter=dot
Push-Location goserver
go test ./... -count=1 -timeout=300s
go test -race ./... -count=1 -timeout=300s
Pop-Location
npm run build:ui
npm run build:exe
npm run verify:frontend-display-only
git diff --check
git status --short
```

Do not parallelize `build:ui`, Go tests, or `build:exe`; the repository intentionally mirrors UI assets into ignored `goserver/dist`.

- [ ] **Step 4: Audit the authority boundary**

Run:

```powershell
rg -n "evalFormula|applyGiftToState|recordGiftTotals|resetTodayStats|buildBlindBoxLeaderboard|listBlindBoxLeaderboardScopes" src tests
rg -n "/api/blind-box/leaderboard" src
rg -n "fetch\(" src/ui/display/blind-box-display.ts src/ui/config/config.ts
```

Expected:

- forbidden-symbol searches return no matches;
- the new URL appears only in `src/backend.ts` and tests;
- neither UI file calls `fetch` directly.

- [ ] **Step 5: Inspect packaged-browser evidence**

Read the verifier output and saved screenshots. Record the dynamic port, exact Go/browser PIDs, five screenshot paths, HTTP/console/overflow counts, and the `finally` cleanup result in the final report. The verifier must report all child PIDs gone. If any assertion fails, stop and use systematic debugging; edit only files already owned by Tasks 1–7 and add a focused regression before rerunning the full gate.

- [ ] **Step 6: Commit the packaged verifier**

```powershell
git add -- goserver/frontend_display_only_e2e_test.go scripts/verify-frontend-display-only.mjs package.json
git diff --cached --check
git commit -m "test: verify packaged display-only flow"
```

Do not stage generated screenshots, `dist`, `goserver/dist`, temp port/stop files, or `package-lock.json`.

- [ ] **Step 7: Request fixed-base Spec and Quality review**

Set the fixed base to `2d92c7a` and review the complete branch along both axes. Reviewers must explicitly inspect:

- Task 0 semantic coverage against deleted TS tests;
- absence of duplicate blind-box authority;
- query parser and error privacy;
- Chinese collation and deterministic tie-breaking;
- strict frontend payload validation;
- abort/generation handling in both consumers;
- no config-import/SSE/release/gift-clip scope creep.

Address every Critical/Important finding with a new RED→GREEN fix commit and rerun the affected focused/full gates. Re-review from the same fixed base until both axes are clean.

- [ ] **Step 8: Finalize the report and branch state**

The report must list:

- every task commit and exact files;
- RED and GREEN evidence;
- final test counts and command exit codes;
- deleted files and remaining allowed frontend logic;
- real browser results and cleanup;
- deferred configuration-import and SSE work;
- any residual Minor concern.

Then run:

```powershell
git status --short
git log --oneline 2d92c7a..HEAD
git diff --stat 2d92c7a..HEAD
git diff --check 2d92c7a..HEAD
```

Expected: no uncommitted tracked changes, only intentional commits, and no release/tag/push action.
