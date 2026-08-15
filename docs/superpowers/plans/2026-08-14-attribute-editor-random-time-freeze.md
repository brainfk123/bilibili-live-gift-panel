# Attribute Editor Random Range, Time Input, and Rule Freeze Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify beginner random add/subtract rules into a signed range, make timer values editable as validated day/hour/minute/second text with shortcuts, and pause only the currently edited attribute's automatic gift and timer rules through a transient backend lease.

**Architecture:** Keep formula conversion, time parsing, HTTP lease state, and editor lifecycle as separate modules with narrow interfaces. The backend owns an in-memory per-attribute lease coordinator that is injected into gift and timer rule evaluation; the frontend owns a heartbeat client and keeps the lease until a save is confirmed or the editor closes. Existing persisted configuration remains compatible: timer values stay numeric seconds and old random formulas are detected without a schema migration.

**Tech Stack:** TypeScript, Vitest, DOM test harness, Go 1.26, `net/http`, Go race detector, Vite, existing Go-embedded UI build.

## Global Constraints

- Random change inputs are integers and must satisfy `rangeMin <= rangeMax`.
- Every beginner random result is clamped to a minimum of `0`.
- Existing optional attribute caps remain supported.
- Old random-add and random-subtract formulas are recognized without changing stored configuration on load.
- Timer configuration continues to persist total seconds; no config schema or OBS wire-format change is allowed.
- Accept `H:MM:SS` with unbounded non-negative hours and `D:HH:MM:SS` with hours `0–23`; minutes and seconds are always `0–59`.
- Timer shortcut deltas are exactly `-3600`, `-600`, `-30`, `+30`, `+600`, and `+3600` seconds, clamped at zero.
- Only automatic gift and timer rules targeting the edited existing attribute are paused.
- Gifts, receipts, contribution data, blind-box data, and other attributes continue processing while one attribute is frozen.
- Frozen gift/timer executions are skipped and never replayed.
- Lease heartbeat is every 5 seconds and expiry is 15 seconds after the last successful create/renew.
- Freeze state is transient memory state and must never enter `config.json`.
- No package version, changelog, release workflow, FFmpeg payload, tag, push, or release changes are in scope.
- Final verification must build the embedded UI and Windows EXE.

---

## File Map

### Create

- `src/ui/config/attribute-time-value.ts` — pure parser, formatter, and shortcut adjustment for timer values.
- `tests/attribute-time-value.test.ts` — boundary and invalid-input coverage for the time helpers.
- `src/ui/config/attribute-edit-lease.ts` — frontend create/renew/release lifecycle and health callback.
- `tests/attribute-edit-lease.test.ts` — deterministic heartbeat, retry, release, and unload tests.
- `goserver/attribute_edit_leases.go` — in-memory coordinator plus strict same-origin HTTP handler.
- `goserver/attribute_edit_leases_test.go` — coordinator and handler contract tests.
- `.superpowers/sdd/2026-08-14-attribute-editor-random-time-freeze/final-report.md` — exact RED/GREEN commands, final gates, EXE evidence, and scope audit.

### Modify

- `src/gift-rule-operations.ts` — replace separate random operations with one signed-range draft and canonical formula conversion.
- `tests/quick-gift-rules.test.ts` — new/legacy random formula tests and validation behavior.
- `src/ui/config/config.ts` — render random range inputs and timer value controls; acquire and release edit leases.
- `src/ui/config/config.css` — responsive range controls, time shortcuts, and persistent lease warning.
- `tests/wizard.test.ts` — real editor DOM behavior, old formula migration, time save behavior, and lease lifecycle.
- `goserver/background_runtime.go` — inject the freeze checker into automatic gift and timer rule execution.
- `goserver/background_runtime_semantics_test.go` — frozen gift semantics while receipts/statistics remain live.
- `goserver/background_runtime_test.go` — timer scheduling, no catch-up, and runtime wiring coverage.
- `goserver/main.go` — instantiate one coordinator, inject it into the runtime, and register its HTTP route.
- `goserver/main_test.go` — production route registration and shared-coordinator wiring regression.

---

### Task 1: Replace Random Add/Subtract with a Signed Range

**Files:**
- Modify: `src/gift-rule-operations.ts`
- Modify: `tests/quick-gift-rules.test.ts`
- Modify: `src/ui/config/config.ts`
- Modify: `src/ui/config/config.css`
- Modify: `tests/wizard.test.ts`

**Interfaces:**
- Produces: `QuickGiftOperation` containing `'randomRange'` and no `'randomSubtract'`.
- Produces: discriminated `QuickGiftRuleDraft` with `{ operation: 'randomRange'; rangeMin: number; rangeMax: number; maximum?: number }`.
- Produces: `buildQuickGiftFormula(draft: QuickGiftRuleDraft, attributeName: string): string | null`.
- Produces: `validateQuickGiftRuleDraft(draft: QuickGiftRuleDraft): string | null`, returning a stable Chinese error or `null`.
- Produces: `quickGiftOperationUsesRange(operation: QuickGiftOperation): boolean`.
- Preserves: `buildOvertimeGiftFormula(operation, attributeName, seconds, maximum)` for gameplay-template callers.

- [ ] **Step 1: Write failing signed-range and legacy-detection tests**

Replace the random rows in `tests/quick-gift-rules.test.ts` and add explicit legacy cases:

```ts
it.each([
  [{ operation: 'randomRange', rangeMin: -60, rangeMax: 60 }, 'MAX(积分+RANDBETWEEN(-60,60),0)'],
  [{ operation: 'randomRange', rangeMin: -60, rangeMax: -1 }, 'MAX(积分+RANDBETWEEN(-60,-1),0)'],
  [{ operation: 'randomRange', rangeMin: 1, rangeMax: 60 }, 'MAX(积分+RANDBETWEEN(1,60),0)'],
  [{ operation: 'randomRange', rangeMin: 0, rangeMax: 0 }, 'MAX(积分+RANDBETWEEN(0,0),0)'],
  [{ operation: 'randomRange', rangeMin: -60, rangeMax: 60, maximum: 100 }, 'MIN(MAX(积分+RANDBETWEEN(-60,60),0),100)'],
] as const)('builds a signed random range %#', (draft, expected) => {
  expect(buildQuickGiftFormula(draft, '积分')).toBe(expected);
});

it.each([
  ['积分+RANDBETWEEN(1,60)', { operation: 'randomRange', rangeMin: 1, rangeMax: 60 }],
  ['MAX(积分-RANDBETWEEN(1,60),0)', { operation: 'randomRange', rangeMin: -60, rangeMax: -1 }],
  ['MAX(积分+RANDBETWEEN(-60,60),0)', { operation: 'randomRange', rangeMin: -60, rangeMax: 60 }],
  ['MIN(MAX(积分+RANDBETWEEN(-60,60),0),100)', {
    operation: 'randomRange', rangeMin: -60, rangeMax: 60, maximum: 100,
  }],
] as const)('detects legacy and canonical random formula %s', (formula, expected) => {
  expect(detectQuickGiftRule(formula, '积分')).toEqual(expected);
});

it.each([
  [{ operation: 'randomRange', rangeMin: 2.5, rangeMax: 5 }, '随机范围必须使用整数'],
  [{ operation: 'randomRange', rangeMin: 10, rangeMax: -10 }, '随机范围的最小变化不能大于最大变化'],
] as const)('rejects invalid random range %#', (draft, message) => {
  expect(validateQuickGiftRuleDraft(draft)).toBe(message);
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `npm test -- tests/quick-gift-rules.test.ts --reporter=dot`

Expected: FAIL because `'randomRange'`, `rangeMin`, `rangeMax`, the draft-based builder, and validation function do not exist.

- [ ] **Step 3: Implement the discriminated draft and canonical parser/builder**

Use these exact public shapes in `src/gift-rule-operations.ts`:

```ts
export type QuickGiftOperation =
  | OvertimeGiftOperation
  | 'price'
  | 'priceSubtract'
  | 'set'
  | 'randomRange'
  | 'advanced';

type AmountQuickGiftOperation = Exclude<QuickGiftOperation, 'randomRange'>;

export type QuickGiftRuleDraft =
  | { operation: 'randomRange'; rangeMin: number; rangeMax: number; maximum?: number }
  | { operation: AmountQuickGiftOperation; amount: number; maximum?: number };

export function validateQuickGiftRuleDraft(draft: QuickGiftRuleDraft): string | null {
  if (draft.operation !== 'randomRange') return null;
  if (!Number.isInteger(draft.rangeMin) || !Number.isInteger(draft.rangeMax)) {
    return '随机范围必须使用整数';
  }
  if (draft.rangeMin > draft.rangeMax) return '随机范围的最小变化不能大于最大变化';
  return null;
}
```

Parse the canonical `MAX(attribute+RANDBETWEEN(min,max),0)` form before generic add/subtract detection. Continue stripping whitespace and using top-level argument splitting instead of a broad regular expression. Map the two legacy forms exactly as the tests require. Return `advanced` for malformed, non-finite, or ambiguous ranges.

Build random formulas only after `validateQuickGiftRuleDraft` returns `null`. Apply the existing outer `MIN` cap to `randomRange`, `add`, `double`, and `price`; do not add caps to decreasing operations.

Update `QUICK_GIFT_OPERATION_GROUPS` so “更多玩法” contains only `randomRange`. Set its label to `让“属性”随机变化（最低为 0）`. Make `quickGiftOperationUsesAmount('randomRange')` false, `quickGiftOperationUsesRange('randomRange')` true, and `quickGiftOperationSupportsMaximum('randomRange')` true. Remove every user-visible and internal reference to `randomSubtract` after legacy formula detection.

- [ ] **Step 4: Adapt non-random call sites and preserve the overtime helper**

Change `buildOvertimeGiftFormula` to construct a non-random draft:

```ts
export function buildOvertimeGiftFormula(
  operation: OvertimeGiftOperation,
  attributeName: string,
  seconds = 60,
  maximum?: number,
): string {
  return buildQuickGiftFormula({ operation, amount: seconds, maximum }, attributeName) ?? '0';
}
```

Update all non-random tests to call `buildQuickGiftFormula({ operation, amount, maximum }, attributeName)`.

- [ ] **Step 5: Write failing beginner-mode DOM tests**

Add `wizard.test.ts` cases that open an existing gift rule and assert:

```ts
const operation = root.querySelector('.quick-rule-operation') as HTMLSelectElement;
expect(Array.from(operation.options).map((option) => option.value)).toContain('randomRange');
expect(Array.from(operation.options).map((option) => option.value)).not.toContain('randomSubtract');
operation.value = 'randomRange';
operation.dispatchEvent(new Event('change'));

const minimum = root.querySelector('.quick-rule-range-min') as HTMLInputElement;
const maximum = root.querySelector('.quick-rule-range-max') as HTMLInputElement;
minimum.value = '-60';
minimum.dispatchEvent(new Event('input'));
maximum.value = '60';
maximum.dispatchEvent(new Event('input'));
expect((root.querySelector('.formula-input') as HTMLInputElement).value)
  .toBe('MAX(积分+RANDBETWEEN(-60,60),0)');
```

Add a legacy rule case for each old formula and assert the two range inputs show `1/60` and `-60/-1`. Add an invalid `10/-10` case and assert `.quick-rule-error` contains `随机范围的最小变化不能大于最大变化`, the formula is not overwritten, and save does not send a config request.

- [ ] **Step 6: Run the DOM tests and verify RED**

Run: `npm test -- tests/wizard.test.ts -t "random range|legacy random" --reporter=dot`

Expected: FAIL because the editor still renders one amount input and two random operations.

- [ ] **Step 7: Render a discriminated quick draft in the editor**

Replace the separate `quickOperation`, `quickAmount`, and random-only implicit state in `SelectedGiftRule` with:

```ts
quickDraft?: QuickGiftRuleDraft;
quickMaximumEnabled?: boolean;
```

On first render, assign `item.quickDraft = detectQuickGiftRule(item.formula, attributeName)`. Render the existing amount input only when `quickGiftOperationUsesAmount` is true. Render a `.quick-rule-range` containing `.quick-rule-range-min`, a literal `到`, and `.quick-rule-range-max` only when `quickGiftOperationUsesRange` is true. Both range inputs use `type="number"` and `step="1"`; they do not set a positive minimum.

Every control change constructs a complete discriminated draft, preserving the optional cap. Call `validateQuickGiftRuleDraft` before calling `buildQuickGiftFormula`. On validation failure, render `.quick-rule-error`, retain the last valid formula text, and record the invalid draft so save validation cannot be bypassed. On success, clear the error and update the formula.

Before backend formula validation in `saveAttributeEditorAsync`, call `validateQuickGiftRuleDraft` for every non-advanced quick draft and stop with its stable message if invalid.

Update `config.css` so `.quick-rule-range` is a three-column grid on wide layouts and stacks without horizontal overflow at the existing narrow breakpoints.

- [ ] **Step 8: Run focused tests and commit**

Run:

```bash
npm test -- tests/quick-gift-rules.test.ts tests/wizard.test.ts -t "quick gift rule|random range|legacy random" --reporter=dot
npm run typecheck
```

Expected: PASS with all module and real DOM random-range cases green, and typecheck exits 0.

Commit:

```bash
git add src/gift-rule-operations.ts src/ui/config/config.ts src/ui/config/config.css tests/quick-gift-rules.test.ts tests/wizard.test.ts
git commit -m "feat: unify beginner random ranges"
```

---

### Task 2: Add Strict Timer Text Parsing and Shortcut Controls

**Files:**
- Create: `src/ui/config/attribute-time-value.ts`
- Create: `tests/attribute-time-value.test.ts`
- Modify: `src/ui/config/config.ts`
- Modify: `src/ui/config/config.css`
- Modify: `tests/wizard.test.ts`

**Interfaces:**
- Produces: `parseAttributeTimeValue(input: string): AttributeTimeParseResult`.
- Produces: `formatAttributeTimeValue(seconds: number): string`.
- Produces: `adjustAttributeTimeValue(input: string, deltaSeconds: number): AttributeTimeParseResult`.
- Consumes: existing numeric `Attribute.value` seconds and returns numeric seconds to existing save/preview paths.

- [ ] **Step 1: Write failing pure-function parser tests**

Create `tests/attribute-time-value.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import {
  adjustAttributeTimeValue,
  formatAttributeTimeValue,
  parseAttributeTimeValue,
} from '../src/ui/config/attribute-time-value';

describe('attribute timer value input', () => {
  it.each([
    ['02:30:45', 9045],
    ['36:00:00', 129600],
    ['1:02:30:45', 95445],
    ['0:23:59:59', 86399],
  ])('parses %s', (input, seconds) => {
    expect(parseAttributeTimeValue(input)).toEqual({ ok: true, seconds });
  });

  it.each([
    ['', '请输入时间'],
    ['1:2', '时间格式应为 时:分:秒 或 天:时:分:秒'],
    ['1::02', '时间每一段都必须是非负整数'],
    ['-1:00:00', '时间每一段都必须是非负整数'],
    ['1.5:00:00', '时间每一段都必须是非负整数'],
    ['1:60:00', '分钟必须在 0–59 之间'],
    ['1:00:60', '秒必须在 0–59 之间'],
    ['1:24:00:00', '四段时间中的小时必须在 0–23 之间'],
    ['999999999999999999:00:00', '时间超出支持范围'],
  ])('rejects %s', (input, message) => {
    expect(parseAttributeTimeValue(input)).toEqual({ ok: false, message });
  });

  it.each([
    [0, '00:00:00'],
    [9045, '02:30:45'],
    [129600, '1:12:00:00'],
  ])('formats %d seconds', (seconds, text) => {
    expect(formatAttributeTimeValue(seconds)).toBe(text);
  });

  it('applies shortcuts and clamps at zero', () => {
    expect(adjustAttributeTimeValue('00:00:20', -30)).toEqual({ ok: true, seconds: 0 });
    expect(adjustAttributeTimeValue('00:00:20', 600)).toEqual({ ok: true, seconds: 620 });
    expect(adjustAttributeTimeValue('bad', 30)).toEqual({ ok: false, message: '时间格式应为 时:分:秒 或 天:时:分:秒' });
  });
});
```

- [ ] **Step 2: Run the parser test and verify RED**

Run: `npm test -- tests/attribute-time-value.test.ts --reporter=dot`

Expected: FAIL because `attribute-time-value.ts` does not exist.

- [ ] **Step 3: Implement strict parser, formatter, and adjustment**

Use the exact result type:

```ts
export type AttributeTimeParseResult =
  | { ok: true; seconds: number }
  | { ok: false; message: string };
```

Implementation rules:

```ts
const INTEGER_SEGMENT = /^\d+$/;
const MAX_SAFE_SECONDS = Number.MAX_SAFE_INTEGER;
```

Trim only the complete input, then split on `:`. Require exactly three or four segments and require every raw segment to match `INTEGER_SEGMENT`. Validate minute/second and four-part hour ranges before calculating. Reject totals that exceed `MAX_SAFE_SECONDS`. Format totals below 86400 as `H:MM:SS` with at least two hour digits; format totals at or above 86400 as `D:HH:MM:SS`.

- [ ] **Step 4: Write failing editor DOM tests for timer values and shortcuts**

Add focused cases to `tests/wizard.test.ts` that mount a state with an existing `hhmmss` attribute, open its editor, and assert:

```ts
expect((root.querySelector('.attribute-current-value') as HTMLInputElement).value).toBe('01:01:01');
expect(root.querySelectorAll('.attribute-time-shortcut')).toHaveLength(6);
```

Click `+10分`, assert `01:11:01`; click `-1时`, assert `00:11:01`; save and assert the persisted attribute value is `661`.

Add invalid-input cases for `1:60:00` and `1:24:00:00`, assert the editor stays open, the save request is not sent, and the stable Chinese error is rendered/toasted. Add a format-switch case showing that numeric seconds become canonical timer text and timer text is parsed before switching back to number.

- [ ] **Step 5: Run the editor tests and verify RED**

Run: `npm test -- tests/wizard.test.ts -t "edits timer values|rejects invalid timer values|uses timer shortcuts" --reporter=dot`

Expected: FAIL because the editor still displays raw seconds and has no shortcut controls.

- [ ] **Step 6: Integrate one numeric-value reader into the attribute editor**

In `src/ui/config/config.ts`, add one local helper used by simulation, preview, format switching, validation, and save:

```ts
const readEditableAttributeValue = (): AttributeTimeParseResult => {
  if (formatSelect.value === 'hhmmss') return parseAttributeTimeValue(valueInput.value);
  const value = Number(valueInput.value);
  return Number.isFinite(value)
    ? { ok: true, seconds: value }
    : { ok: false, message: '当前值必须是数字' };
};
```

Do not leave any direct `Number(valueInput.value)` reads inside this editor path. Initialize `valueInput` with `formatAttributeTimeValue` when the selected format is `hhmmss`, add class `attribute-current-value`, and keep simulations numeric by assigning `simulationDraftValue = parsed.seconds` only after a successful parse.

Render the six buttons from this exact data:

```ts
const TIME_SHORTCUTS = [
  ['-1时', -3600],
  ['-10分', -600],
  ['-30秒', -30],
  ['+30秒', 30],
  ['+10分', 600],
  ['+1时', 3600],
] as const;
```

Each shortcut calls `adjustAttributeTimeValue`, writes `formatAttributeTimeValue(result.seconds)`, and invokes the existing simulation invalidation and overview preview refresh. Hide the shortcut row unless the current format is `hhmmss`.

On save, if parsing fails, toast the exact parser message, focus the value input, and return before rule validation or state mutation.

Track the last accepted format. When switching from `hhmmss` to a numeric format, parse first; on failure restore the select to `hhmmss`, keep the text untouched, and show the parser message. When switching from numeric to `hhmmss`, require a finite non-negative integer number of seconds and replace the input with `formatAttributeTimeValue`. Update `inputMode` to `numeric` for timer text and `decimal` for numeric formats.

- [ ] **Step 7: Add responsive styles and run tests**

Add `.attribute-time-shortcuts` and `.attribute-time-shortcut` layout rules to `src/ui/config/config.css`. On narrow screens, allow shortcut buttons to wrap; do not introduce horizontal overflow in `.attribute-workbench`.

Run:

```bash
npm test -- tests/attribute-time-value.test.ts tests/wizard.test.ts --reporter=dot
npm run typecheck
```

Expected: both commands PASS.

- [ ] **Step 8: Commit**

```bash
git add src/ui/config/attribute-time-value.ts src/ui/config/config.ts src/ui/config/config.css tests/attribute-time-value.test.ts tests/wizard.test.ts
git commit -m "feat: edit timer attributes as time"
```

---

### Task 3: Build the In-Memory Attribute Edit Lease API

**Files:**
- Create: `goserver/attribute_edit_leases.go`
- Create: `goserver/attribute_edit_leases_test.go`

**Interfaces:**
- Produces: `type attributeFreezeChecker interface { IsFrozen(attributeID string) bool }`.
- Produces: `newAttributeEditLeaseCoordinator(ttl time.Duration, now func() time.Time, token func() (string, error)) *attributeEditLeaseCoordinator`.
- Produces: `newDefaultAttributeEditLeaseCoordinator() *attributeEditLeaseCoordinator` using the exact 15-second TTL and cryptographic token source.
- Produces: `newAttributeEditLeaseHandler(store *configStore, leases *attributeEditLeaseCoordinator) http.Handler`.
- HTTP endpoint contract at `/api/attribute-edit-lease`: `POST` create, `PUT` renew, `DELETE` release.

- [ ] **Step 1: Write failing coordinator tests with an injected clock**

Create tests covering independent attributes, multiple sessions on one attribute, expiry, renew, and exact-token release:

```go
func TestAttributeEditLeasesKeepAttributeFrozenUntilLastSessionEnds(t *testing.T) {
    now := time.Unix(100, 0)
    tokens := []string{strings.Repeat("a", 24), strings.Repeat("b", 24)}
    leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
        token := tokens[0]
        tokens = tokens[1:]
        return token, nil
    })

    first, _, err := leases.Create("attribute-1")
    if err != nil { t.Fatal(err) }
    second, _, err := leases.Create("attribute-1")
    if err != nil { t.Fatal(err) }
    if !leases.IsFrozen("attribute-1") || leases.IsFrozen("attribute-2") { t.Fatal("unexpected freeze set") }
    if !leases.Release("attribute-1", first) || !leases.IsFrozen("attribute-1") { t.Fatal("first release thawed live peer") }
    if !leases.Release("attribute-1", second) || leases.IsFrozen("attribute-1") { t.Fatal("last release did not thaw") }
}

func TestAttributeEditLeaseExpiresAndRenewExtendsIt(t *testing.T) {
    now := time.Unix(100, 0)
    leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
        return strings.Repeat("c", 24), nil
    })
    token, _, err := leases.Create("attribute-1")
    if err != nil { t.Fatal(err) }
    now = time.Unix(110, 0)
    if _, ok := leases.Renew("attribute-1", token); !ok { t.Fatal("renew failed") }
    now = time.Unix(124, 0)
    if !leases.IsFrozen("attribute-1") { t.Fatal("renewed lease expired early") }
    now = time.Unix(126, 0)
    if leases.IsFrozen("attribute-1") { t.Fatal("expired lease remained frozen") }
}
```

- [ ] **Step 2: Write failing strict HTTP tests**

Use a temporary `configStore` containing an attribute with ID `attribute-1`. Cover:

- Cross-site `Sec-Fetch-Site` and mismatched `Origin` return 403 without creating a lease.
- Unknown methods return 405 with an exact `Allow` header.
- Unknown JSON fields, trailing JSON, oversized bodies, empty IDs, unknown IDs, malformed tokens, and token/attribute mismatches are rejected.
- `POST {"attributeId":"attribute-1"}` returns a 24-character base64url token and `expiresAt`.
- `PUT {"attributeId":"attribute-1","token":"..."}` renews.
- A valid `DELETE` is idempotent and returns success even when its session is already absent; a mismatched pair never releases another session.
- Responses use `Cache-Control: no-store`.

- [ ] **Step 3: Run Go tests and verify RED**

Run from `goserver`:

```bash
go test ./... -run '^TestAttributeEditLease' -count=1
```

Expected: FAIL because coordinator and handler types do not exist.

- [ ] **Step 4: Implement coordinator with lazy expiry cleanup**

Use these exact internal records:

```go
type attributeEditLease struct {
    attributeID string
    expiresAt   time.Time
}

type attributeEditLeaseCoordinator struct {
    mu       sync.Mutex
    ttl      time.Duration
    now      func() time.Time
    newToken func() (string, error)
    sessions map[string]attributeEditLease
}
```

Production token generation reads exactly 18 bytes from `crypto/rand` and uses `base64.RawURLEncoding`, producing 24 characters. Every public operation trims inputs, takes the mutex, removes expired sessions, and then performs its action. `Renew` and `Release` require both token and attribute ID. `IsFrozen` compares only unexpired sessions and never persists state.

- [ ] **Step 5: Implement strict fixed-path HTTP handler**

Use one request shape:

```go
type attributeEditLeaseRequest struct {
    AttributeID string `json:"attributeId"`
    Token       string `json:"token,omitempty"`
}
```

Read at most 4 KiB, call `DisallowUnknownFields`, require EOF after the first object, apply `isSameOriginGiftReceiptRequest`, and verify the attribute ID exists in a fresh `store.readState()` snapshot before create or renew. Return stable Chinese messages without filesystem paths or internal errors. Set `Cache-Control: no-store` on every API response.

Accept trimmed attribute IDs from 1 through 160 UTF-8 bytes, but require exactly one current attribute to carry the requested ID; reject missing or duplicate IDs rather than freezing an ambiguous target. Validate tokens by decoding exactly 24 base64url characters into 18 bytes.

- [ ] **Step 6: Run focused tests, race tests, and commit**

Run from `goserver`:

```bash
go test ./... -run '^TestAttributeEditLease' -count=20
go test -race ./... -run '^TestAttributeEditLease' -count=5
```

Expected: both commands PASS.

Commit:

```bash
git add goserver/attribute_edit_leases.go goserver/attribute_edit_leases_test.go
git commit -m "feat: add attribute edit leases"
```

---

### Task 4: Apply Lease Checks to Gift and Timer Rule Execution

**Files:**
- Modify: `goserver/background_runtime.go`
- Modify: `goserver/background_runtime_semantics_test.go`
- Modify: `goserver/background_runtime_test.go`

**Interfaces:**
- Consumes: `attributeFreezeChecker.IsFrozen(attributeID string) bool` from Task 3.
- Produces: `(*backgroundRuntime).setAttributeFreezeChecker(attributeFreezeChecker)`.
- Preserves: existing `applyGiftEvent(state, gift)` and `applyTimerRules(state, dueIDs, now)` wrappers for current unit tests.
- Adds: guarded internal variants used by the real runtime.

- [ ] **Step 1: Write failing frozen-gift semantic tests**

In `background_runtime_semantics_test.go`, build two ID-bearing attributes and matching gift rules. Use a fake checker that freezes only `attribute-a`, then assert after one gift:

```go
if got := state.findAttribute("A").Value; got != 0 { t.Fatalf("frozen A = %v", got) }
if got := state.findAttribute("B").Value; got != 1 { t.Fatalf("live B = %v", got) }
if len(state.GiftReceipts) != 1 { t.Fatalf("receipts = %d", len(state.GiftReceipts)) }
if state.todayStats().GiftTotals[giftKey(1)] != 1 { t.Fatal("gift total was dropped") }
if len(state.Contributions.Viewers) != 1 { t.Fatal("contribution was dropped") }
```

Also assert the receipt contains the B change but no A change, and the frozen rule's daily trigger count does not increment.

- [ ] **Step 2: Write failing timer no-catch-up tests**

Drive `backgroundRuntime.handleTimerTick` with an injected tick sequence:

1. First tick establishes the schedule.
2. The due tick occurs while the attribute is frozen and leaves its value unchanged.
3. Release the checker and tick before the next scheduled time; value remains unchanged.
4. Tick at the next scheduled time; exactly one timer application occurs.

Add a second attribute whose timer remains live during the first due tick.

- [ ] **Step 3: Run focused tests and verify RED**

Run from `goserver`:

```bash
go test ./... -run 'TestApplyGiftEventSkipsOnlyFrozenAttribute|TestBackgroundRuntimeFrozenTimerDoesNotCatchUp' -count=1
```

Expected: FAIL because rule evaluation has no freeze checker.

- [ ] **Step 4: Add guarded variants without weakening existing pure helpers**

Use this pattern:

```go
func applyGiftEvent(state *appState, gift giftEvent) {
    applyGiftEventWithFreeze(state, gift, nil)
}

func applyGiftEventWithFreeze(state *appState, gift giftEvent, freezes attributeFreezeChecker) {
    // Existing normalization, receipt, KPI, and contribution flow remains here.
    // Immediately after finding an attribute and before condition/formula evaluation:
    if freezes != nil && attribute.ID != "" && freezes.IsFrozen(attribute.ID) {
        continue
    }
}
```

Use the same wrapper/internal pattern for `applyTimerRules`. Keep schedule advancement in `dueTimerRuleIDs`; only skip the actual rule application, which guarantees no catch-up.

Add `attributeFreezes attributeFreezeChecker` to `backgroundRuntime` and a setter. Real gift settlement calls `applyGiftEventWithFreeze`; real timer ticks call the guarded timer variant.

- [ ] **Step 5: Run focused, full, and race tests; then commit**

Run from `goserver`:

```bash
go test ./... -run 'TestApplyGiftEventSkipsOnlyFrozenAttribute|TestBackgroundRuntimeFrozenTimerDoesNotCatchUp' -count=20
go test -race ./... -run 'TestApplyGiftEventSkipsOnlyFrozenAttribute|TestBackgroundRuntimeFrozenTimerDoesNotCatchUp|TestAttributeEditLease' -count=5
go test ./... -count=1 -timeout=300s
```

Expected: all commands PASS.

Commit:

```bash
git add goserver/background_runtime.go goserver/background_runtime_semantics_test.go goserver/background_runtime_test.go
git commit -m "feat: pause rules for edited attributes"
```

---

### Task 5: Add the Frontend Lease Client and Editor Lifecycle

**Files:**
- Create: `src/ui/config/attribute-edit-lease.ts`
- Create: `tests/attribute-edit-lease.test.ts`
- Modify: `src/ui/config/config.ts`
- Modify: `src/ui/config/config.css`
- Modify: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: Task 3 endpoint `/api/attribute-edit-lease`.
- Produces: `acquireAttributeEditLease(attributeId, options?): Promise<AttributeEditLeaseSession>`.
- Produces: `AttributeEditLeaseSession.release(): Promise<void>` that synchronously stops heartbeats/listeners before starting the best-effort DELETE.
- Produces: health callback states `'healthy' | 'retrying'` for the editor warning.

- [ ] **Step 1: Write failing deterministic client tests**

Create `tests/attribute-edit-lease.test.ts` with fake timers and an injected fetch implementation. Use these public types:

```ts
export type AttributeEditLeaseHealth = 'healthy' | 'retrying';

export interface AttributeEditLeaseSession {
  readonly attributeId: string;
  readonly token: string;
  release(): Promise<void>;
}

export interface AttributeEditLeaseOptions {
  fetchImpl?: typeof fetch;
  heartbeatMs?: number;
  retryMs?: number;
  onHealthChange?: (health: AttributeEditLeaseHealth) => void;
}
```

Tests must prove:

- Acquire sends `POST` with only `attributeId` and accepts a 24-character token.
- A successful heartbeat sends `PUT` at 5 seconds and reports healthy.
- A failed heartbeat reports retrying and schedules a 1-second retry; success returns to healthy.
- Release clears heartbeat and retry timers before sending one `DELETE` with `keepalive: true`.
- Repeated release is idempotent.
- `beforeunload` triggers the same one-time release and removes its listener.
- Malformed success payloads and non-2xx responses reject with stable Chinese errors.

- [ ] **Step 2: Run client tests and verify RED**

Run: `npm test -- tests/attribute-edit-lease.test.ts --reporter=dot`

Expected: FAIL because the client module does not exist.

- [ ] **Step 3: Implement the lease client as an owned resource**

Implement `acquireAttributeEditLease` with:

```ts
const ENDPOINT = '/api/attribute-edit-lease';
const DEFAULT_HEARTBEAT_MS = 5_000;
const DEFAULT_RETRY_MS = 1_000;
```

The session owns exactly one heartbeat timer, at most one retry timer, and one `beforeunload` listener. `release()` flips a `released` flag and clears/removes all three synchronously, then performs one best-effort `DELETE`. Heartbeat/retry completions must re-check `released` before scheduling more work. Do not throw an unhandled rejection from unload cleanup.

- [ ] **Step 4: Write failing real-editor lifecycle tests**

Add `wizard.test.ts` cases that use the actual edit button and mocked endpoint:

1. Existing attribute without an ID is assigned an ID and that config save completes before lease acquisition.
2. Existing attribute opens only after a successful lease `POST`.
3. New attribute creation opens without lease traffic.
4. Cancel, close button, overlay close, and successful save release exactly once.
5. Save failure keeps the modal open and heartbeats active.
6. Lease acquisition failure leaves the modal closed and shows an error.
7. Heartbeat failure shows `.attribute-lease-warning`; recovery hides it.
8. Renaming the attribute keeps renew/release requests bound to the original stable ID.

- [ ] **Step 5: Run editor lifecycle tests and verify RED**

Run:

```bash
npm test -- tests/attribute-edit-lease.test.ts tests/wizard.test.ts -t "attribute edit lease|lease warning|stable attribute id" --reporter=dot
```

Expected: FAIL because `openAttributeEditor` is synchronous and does not own a lease.

- [ ] **Step 6: Integrate acquisition before opening and release after confirmed save**

Make `openAttributeEditor` async and guard concurrent openings. For an existing attribute:

```ts
const original = state.attributes[index];
const previousId = original.id;
if (!original.id) {
  original.id = createAttributeId();
  try {
    await saveAndWait();
  } catch (error) {
    original.id = previousId;
    throw error;
  }
}
const lease = await acquireAttributeEditLease(original.id, {
  onHealthChange: renderLeaseHealth,
});
```

Only set `editorOpen = true` and append the overlay after the lease succeeds. Store a single `closeAttributeEditor(reason)` closure that every close path uses; no direct `overlay.remove()` may bypass release.

On successful save, keep the lease through `await saveAndWait()`, then remove the overlay and call `void lease.release()`. On failed save, do neither. New attributes use `lease = null`.

Render a persistent `.attribute-lease-warning` inside the workbench header when health is `retrying`, with copy: `属性规则冻结状态正在重连，请暂时不要关闭此页面。`

- [ ] **Step 7: Run frontend tests and commit**

Run:

```bash
npm test -- tests/attribute-edit-lease.test.ts tests/wizard.test.ts --reporter=dot
npm run typecheck
```

Expected: both commands PASS.

Commit:

```bash
git add src/ui/config/attribute-edit-lease.ts src/ui/config/config.ts src/ui/config/config.css tests/attribute-edit-lease.test.ts tests/wizard.test.ts
git commit -m "feat: freeze rules while editing attributes"
```

---

### Task 6: Wire the Lease Coordinator into the Production Server

**Files:**
- Modify: `goserver/main.go`
- Modify: `goserver/main_test.go` if the existing main route harness supports isolated registration
- Modify: `goserver/attribute_edit_leases_test.go`

**Interfaces:**
- Consumes: Task 3 coordinator/handler and Task 4 runtime setter.
- Produces: the production `/api/attribute-edit-lease` route backed by the same coordinator queried by gift/timer execution.

- [ ] **Step 1: Write a failing shared-instance wiring test**

Use a small route-registration helper if `main` cannot be safely invoked in a test:

```go
func registerAttributeEditLeaseRoute(
    mux *http.ServeMux,
    store *configStore,
    background *backgroundRuntime,
    leases *attributeEditLeaseCoordinator,
) {
    background.setAttributeFreezeChecker(leases)
    mux.Handle("/api/attribute-edit-lease", newAttributeEditLeaseHandler(store, leases))
}
```

The test creates a lease through the HTTP handler, then runs a real guarded gift application through the configured runtime and proves the same attribute is frozen. Release via HTTP and prove the next event applies.

- [ ] **Step 2: Run the wiring test and verify RED**

Run from `goserver`:

```bash
go test ./... -run '^TestRegisterAttributeEditLeaseRouteSharesCoordinatorWithRuntime$' -count=1
```

Expected: FAIL because production wiring does not exist.

- [ ] **Step 3: Add one production coordinator and route**

In `main.go`, immediately after constructing `background`:

```go
attributeEdits := newDefaultAttributeEditLeaseCoordinator()
background.setAttributeFreezeChecker(attributeEdits)
```

After creating the mux and before starting the server:

```go
mux.Handle("/api/attribute-edit-lease", newAttributeEditLeaseHandler(store, attributeEdits))
```

If the registration helper was introduced for testing, call it from production instead of duplicating these two lines. Do not instantiate a second coordinator for the HTTP handler.

- [ ] **Step 4: Run Go gates and commit**

Run from `goserver`:

```bash
go test ./... -run 'TestRegisterAttributeEditLeaseRoute|TestAttributeEditLease|TestApplyGiftEventSkipsOnlyFrozenAttribute|TestBackgroundRuntimeFrozenTimerDoesNotCatchUp' -count=20
go test -race ./... -run 'TestRegisterAttributeEditLeaseRoute|TestAttributeEditLease|TestApplyGiftEventSkipsOnlyFrozenAttribute|TestBackgroundRuntimeFrozenTimerDoesNotCatchUp' -count=5
go test ./... -count=1 -timeout=300s
```

Expected: all commands PASS.

Commit:

```bash
git add goserver/main.go goserver/main_test.go goserver/attribute_edit_leases_test.go
git commit -m "feat: wire attribute edit freeze service"
```

If `goserver/main_test.go` was not changed, omit it from `git add` rather than touching it artificially.

---

### Task 7: Run Integrated Regression Gates and Build the EXE

**Files:**
- Modify only if a gate exposes a real regression in an already-owned file from Tasks 1–6.
- Verify: generated `dist/` outputs remain ignored and are not committed.

**Interfaces:**
- Consumes: every task deliverable.
- Produces: verified embedded UI and Windows executable containing the new modules.

- [ ] **Step 1: Run all focused frontend tests together**

Run:

```bash
npm test -- tests/quick-gift-rules.test.ts tests/attribute-time-value.test.ts tests/attribute-edit-lease.test.ts tests/wizard.test.ts --reporter=dot
```

Expected: PASS with no new skipped tests.

- [ ] **Step 2: Run complete frontend and type gates**

Run:

```bash
npm run typecheck
npm test -- --reporter=dot
npm run build:ui
```

Expected: typecheck exits 0, all Vitest files pass with only pre-existing skips, and Vite emits the config graph successfully.

- [ ] **Step 3: Run complete Go and race gates**

Run from `goserver`:

```bash
go test ./... -count=1 -timeout=300s
go test -race ./... -count=1 -timeout=300s
```

Expected: both commands PASS.

- [ ] **Step 4: Build the embedded Windows executable**

Return to the repository root and run:

```bash
npm run build:exe
```

Expected: PASS; the UI asset manifest contains exactly one emitted module whose logical path contains `attribute-edit-lease` and exactly one containing `attribute-time-value`, every manifest entry exists under `goserver/dist`, and `dist/gift-panel.exe` is produced.

- [ ] **Step 5: Verify packaged UI closure and repository scope**

Run:

```bash
git diff --check
git status --short
```

Inspect the generated UI manifest used by `scripts/build-go.mjs` and assert every referenced asset exists in the embedded asset tree. Confirm no Playwright browser, MSYS2 toolchain, FFmpeg test tool, temporary lease state, or generated EXE is staged.

- [ ] **Step 6: Record final evidence and commit the report**

Create `.superpowers/sdd/2026-08-14-attribute-editor-random-time-freeze/final-report.md`. Record exact commands, RED failure signatures, GREEN pass counts/durations, EXE path/size/SHA-256, modified-file scope, and any expected pre-existing skips. Explicitly state that no version/tag/push/release operation was performed.

If any gate exposed a product regression, return to the owning Task 1–6 test cycle, capture a focused RED, fix it there, and rerun the complete Task 7 sequence before creating this report.

Commit only the report:

```bash
git add -f .superpowers/sdd/2026-08-14-attribute-editor-random-time-freeze/final-report.md
git commit -m "docs: record attribute editor verification"
```
