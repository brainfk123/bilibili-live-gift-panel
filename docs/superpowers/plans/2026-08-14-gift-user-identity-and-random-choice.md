# Gift User Identity and Random Choice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make each gift sender's named identity available to gift formulas and rule conditions, expose identity conditions in beginner mode, and add lazy variadic `RANDOMCHOICE(...)` evaluation.

**Architecture:** Go remains the sole formula evaluator and identity authority. A focused gift-formula-context module maps membership to the ordered 0–4 domain and constructs the shared environment used by validation, preview, and live gift execution; `GiftRule.condition` is the only stored condition seam. Frontend helpers only translate beginner selections to/from that condition string and send a strict preview request; they never interpret Bilibili membership strings or evaluate formulas.

**Tech Stack:** Go 1.26, `net/http`, JSON config store, TypeScript 5, Vitest, Vite, existing Go formula parser/evaluator.

## Global Constraints

- Formula names and values are exact: `用户身份` is runtime-valued; `普通用户=0`, `粉丝团=1`, `舰长=2`, `提督=3`, `总督=4`.
- Formula equality stays the existing single `=` operator. Do not add or emit `==`.
- Membership mapping is exact: empty/unknown→0, `fan`→1, `captain`→2, `admiral`→3, `governor`→4.
- Identity comes only from the gift event's membership; never infer it from price, gift name, or special-gift ID.
- The six formula names above are reserved attribute names and cannot be overridden.
- Gift rules gain one optional `condition`; missing/blank means unconditional and preserves old configs.
- A false, non-finite, or invalid condition skips that rule occurrence without consuming a daily trigger or writing a successful change log; processing continues.
- Timer formulas do not receive gift identity or `price`.
- Beginner mode emits only blank, `用户身份=<身份常量>`, or `用户身份>=<身份常量>` and preserves every other advanced condition verbatim until the user explicitly selects a beginner condition.
- `RANDOMCHOICE` requires at least one argument, selects uniformly by index, evaluates only the selected argument, uses `formulaRandomIntn`, and does not call randomness for one argument.
- Do not add strings, weights, sampling without replacement, or a frontend formula evaluator.
- Use TDD for every task: capture a meaningful RED before production edits, then run focused GREEN and fresh broader gates.
- Each task owns only the files listed in that task and ends in an independent commit. Preserve unrelated and untracked files.
- Do not push, tag, publish, or create a release.

---

## File Structure Map

### New focused modules

- `goserver/gift_formula_context.go`: sole membership→identity mapping, identity constants, reserved-name predicate, and gift formula environment builder.
- `goserver/gift_formula_context_test.go`: mapping, environment precedence, reserved names, and unknown-membership tests.
- `src/gift-rule-conditions.ts`: stable TypeScript domain values and pure beginner condition build/detect helpers.
- `tests/gift-rule-conditions.test.ts`: exact round-trip and advanced-preservation tests.

### Existing modules modified

- `goserver/formula.go`, `goserver/formula_test.go`: variadic lazy `RANDOMCHOICE` only.
- `goserver/state_transaction_test.go`: prove the new random function does not re-evaluate on transaction recovery.
- `goserver/state.go`: add `giftRule.Condition`.
- `goserver/background_runtime.go`, `goserver/background_runtime_semantics_test.go`: live gift condition semantics and shared environment.
- `goserver/config_store.go`, `goserver/config_store_test.go`: reserved attribute names and gift-condition validation.
- `goserver/main.go`, `goserver/main_test.go`: strict gift-rule preview request/response while preserving timer formula preview behavior.
- `src/types.ts`, `src/backend.ts`, `tests/backend.test.ts`: frontend wire types and strict preview adapter.
- `src/ui/config/config.ts`, `src/ui/config/config.css`, `tests/wizard.test.ts`: beginner selectors, advanced condition, simulated identity, save/reopen/preview UX, and help copy.
- `src/formula-presets.ts`, `tests/formula-presets.test.ts`: make system-name preservation explicit during attribute renaming.
- `docs/superpowers/specs/2026-08-14-gift-user-identity-and-random-choice-design.md`: already committed design; only correct contradictions discovered during implementation, never expand scope silently.

---

### Task 1: Gift Formula Context and Lazy `RANDOMCHOICE`

**Files:**
- Create: `goserver/gift_formula_context.go`
- Create: `goserver/gift_formula_context_test.go`
- Modify: `goserver/formula.go`
- Modify: `goserver/formula_test.go`
- Modify: `goserver/state_transaction_test.go`

**Interfaces:**
- Consumes: `giftEvent.Membership string`, `appState.Attributes`, existing `formulaRandomIntn func(int) int`, and `evaluateFormula(string, map[string]float64)`.
- Produces:

```go
const (
    giftFormulaUserIdentity = "用户身份"
    giftIdentityOrdinary    = 0
    giftIdentityFan         = 1
    giftIdentityCaptain     = 2
    giftIdentityAdmiral     = 3
    giftIdentityGovernor    = 4
)

func giftIdentityLevel(membership string) float64
func isReservedFormulaName(name string) bool
func buildGiftFormulaEnvironment(state appState, attributeName string, attributeValue, giftPrice float64, membership string) map[string]float64
func buildGiftFormulaEnvironmentWithIdentity(state appState, attributeName string, attributeValue, giftPrice, identityLevel float64) map[string]float64
```

- `buildGiftFormulaEnvironment` maps membership and delegates to `buildGiftFormulaEnvironmentWithIdentity`.
- To preserve existing `price` compatibility, the identity-level builder starts with `price`, then inserts attributes and the edited target attribute, then inserts only the six reserved identity names last. This keeps the historical behavior of a legacy attribute literally named `price` while preventing identity names from being shadowed.
- Adds `RANDOMCHOICE` to `callNode.evaluate`; no parser change is required because calls already hold `[]formulaNode`.

- [ ] **Step 1: Write failing identity-context tests**

Create table-driven tests that require the exact mapping and environment precedence:

```go
func TestGiftIdentityLevelUsesProductOrdering(t *testing.T) {
    tests := map[string]float64{
        "": 0, "unknown": 0, "fan": 1,
        "captain": 2, "admiral": 3, "governor": 4,
    }
    for membership, want := range tests {
        if got := giftIdentityLevel(membership); got != want {
            t.Fatalf("membership %q = %v, want %v", membership, got, want)
        }
    }
}

func TestBuildGiftFormulaEnvironmentReservesIdentityNames(t *testing.T) {
    state := defaultAppState()
    state.Attributes = []attributeState{
        {Name: "积分", Value: 7},
        {Name: "用户身份", Value: 99},
        {Name: "舰长", Value: 99},
    }
    env := buildGiftFormulaEnvironment(state, "积分", 8, 5200, "captain")
    want := map[string]float64{
        "积分": 8, "price": 5200, "用户身份": 2,
        "普通用户": 0, "粉丝团": 1, "舰长": 2, "提督": 3, "总督": 4,
    }
    for name, value := range want {
        if env[name] != value { t.Fatalf("%s = %v, want %v", name, env[name], value) }
    }
}
```

Also assert all six exact names return true from `isReservedFormulaName`, while `积分`, `price`, and `用户身份等级` return false.

- [ ] **Step 2: Run identity tests and verify RED**

Run:

```powershell
cd goserver
go test ./... -run 'TestGiftIdentityLevel|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames' -count=1
```

Expected: compilation fails because the new constants/functions do not exist.

- [ ] **Step 3: Implement the focused identity context module**

Use a switch over `strings.TrimSpace(membership)` and a literal system-value map. Keep Bilibili's raw 3/2/1 guard encoding out of this module. Return a fresh environment map on every call.

```go
func giftIdentityLevel(membership string) float64 {
    switch strings.TrimSpace(membership) {
    case "fan": return giftIdentityFan
    case "captain": return giftIdentityCaptain
    case "admiral": return giftIdentityAdmiral
    case "governor": return giftIdentityGovernor
    default: return giftIdentityOrdinary
    }
}
```

- [ ] **Step 4: Run identity tests and verify GREEN**

Run the Step 2 command again. Expected: PASS.

- [ ] **Step 5: Write failing `RANDOMCHOICE` tests**

Add tests with a cleanup-restored injected seam:

```go
func TestFormulaRandomChoiceSelectsOneLazyArgument(t *testing.T) {
    original := formulaRandomIntn
    t.Cleanup(func() { formulaRandomIntn = original })
    calls := 0
    formulaRandomIntn = func(limit int) int {
        calls++
        if limit != 3 { t.Fatalf("limit = %d, want 3", limit) }
        return 1
    }
    got, err := evaluateFormula("RANDOMCHOICE(1/0,舰长+3,missing)", map[string]float64{"舰长": 2})
    if err != nil || got != 5 || calls != 1 { t.Fatalf("got=%v err=%v calls=%d", got, err, calls) }
}

func TestFormulaRandomChoiceSingleArgumentDoesNotDraw(t *testing.T) {
    original := formulaRandomIntn
    t.Cleanup(func() { formulaRandomIntn = original })
    formulaRandomIntn = func(int) int { t.Fatal("single argument drew randomness"); return 0 }
    got, err := evaluateFormula("RANDOMCHOICE(7)", nil)
    if err != nil || got != 7 { t.Fatalf("got=%v err=%v", got, err) }
}

func TestFormulaRandomChoiceRejectsZeroArguments(t *testing.T) {
    _, err := evaluateFormula("RANDOMCHOICE()", nil)
    if err == nil || !strings.Contains(err.Error(), "RANDOMCHOICE 至少需要 1 个参数") {
        t.Fatalf("error = %v", err)
    }
}

func TestFormulaRandomChoiceReturnsSelectedArgumentError(t *testing.T) {
    original := formulaRandomIntn
    t.Cleanup(func() { formulaRandomIntn = original })
    formulaRandomIntn = func(int) int { return 1 }
    _, err := evaluateFormula("RANDOMCHOICE(10,1/0)", nil)
    if err == nil || !strings.Contains(err.Error(), "除数为零") { t.Fatalf("error = %v", err) }
}
```

Test first, middle, and last indices across a small table. Do not assert statistical frequencies; deterministic index selection proves the uniform index contract without flaky probability tests.

Extend `formula_test.go` imports with `strings` for the stable error-text assertions.

- [ ] **Step 6: Run random-choice tests and verify RED**

Run:

```powershell
cd goserver
go test ./... -run 'TestFormulaRandomChoice' -count=1
```

Expected: FAIL with `未知函数 "RANDOMCHOICE"`.

- [ ] **Step 7: Implement minimal lazy `RANDOMCHOICE`**

Add a dedicated `case` before eager ordinary functions:

```go
case "RANDOMCHOICE":
    if len(n.args) == 0 {
        return 0, fmt.Errorf("RANDOMCHOICE 至少需要 1 个参数")
    }
    if len(n.args) == 1 {
        return eval(0)
    }
    return eval(formulaRandomIntn(len(n.args)))
```

Do not pre-evaluate arguments and do not call `rand.Intn` directly.

- [ ] **Step 8: Extend the transaction recovery regression**

Change the existing recovery fixture formula from `RANDBETWEEN(10, 60)` to `RANDOMCHOICE(10,37,60)` and require `limit == 3`, returning index 1. Keep the existing injected post-prepare failure and assert recovery leaves value 37 with exactly one random draw.

- [ ] **Step 9: Run focused and broader Go tests**

Run:

```powershell
cd goserver
go test ./... -run 'TestGiftIdentity|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames|TestFormulaRandomChoice|TestPendingStateTransactionRecoversGiftFormulaRandomResultWithoutReevaluation' -count=20
go test -race ./... -run 'TestFormulaRandomChoice|TestPendingStateTransactionRecoversGiftFormulaRandomResultWithoutReevaluation' -count=5
go test ./... -count=1 -timeout=300s
```

Expected: all PASS. Then run `gofmt` on the five owned Go files and `git diff --check`.

- [ ] **Step 10: Commit Task 1**

```powershell
git add -- goserver/gift_formula_context.go goserver/gift_formula_context_test.go goserver/formula.go goserver/formula_test.go goserver/state_transaction_test.go
git diff --cached --check
git commit -m "feat: add gift identity formula context"
```

---

### Task 2: Backend Gift Rule Conditions and Preview Contract

**Files:**
- Modify: `goserver/state.go`
- Modify: `goserver/background_runtime.go`
- Modify: `goserver/background_runtime_semantics_test.go`
- Modify: `goserver/config_store.go`
- Modify: `goserver/config_store_test.go`
- Modify: `goserver/main.go`
- Modify: `goserver/main_test.go`

**Interfaces:**
- Consumes Task 1's `buildGiftFormulaEnvironment`, `giftIdentityLevel`, and `isReservedFormulaName`.
- Produces:

```go
type giftRulePreviewResult struct {
    Triggered bool    `json:"triggered"`
    Result    float64 `json:"result"`
}

func previewGiftRule(state appState, condition, formula, attributeName string, attributeValue, giftPrice float64, identityLevel int) (giftRulePreviewResult, error)
```

- `/api/formula/preview` keeps timer requests compatible. Gift requests accept optional `condition` and `userIdentity`; gift success returns `{code:0, triggered:boolean, result:number}`. For backward compatibility, old gift callers without `condition` still receive `triggered:true` and the same `result`.

- [ ] **Step 1: Write failing runtime condition tests**

Add tests that construct rules with `Condition` and events with membership:

```go
func TestApplyGiftEventUsesNamedUserIdentityCondition(t *testing.T) {
    state := semanticState(giftRule{
        ID: "guard", GiftID: 1, AttributeName: "积分",
        Condition: "用户身份>=舰长", Formula: "积分+1",
    }, 0)
    applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Membership: "fan", Rnd: "fan"})
    applyGiftEvent(&state, giftEvent{GiftID: 1, Num: 1, Membership: "captain", Rnd: "captain"})
    if got := state.Attributes[0].Value; got != 1 {
        t.Fatalf("value = %v, want 1", got)
    }
}
```

Add separate assertions for exact `用户身份=粉丝团`, governor satisfying `>=舰长`, unknown membership acting ordinary, and a formula body using `IF(用户身份>=提督,积分+10,积分+1)`.

Add a joint numeric condition `(用户身份>=舰长)*(price>=1000)*(积分<2)` to prove identity, gift price, and current attributes coexist in the condition environment.

Add a batch test where condition `积分<2` with `Num: 4` applies twice, proving per-occurrence re-evaluation. Assert `RuleTriggers` is 2, one aggregated log change is present, and the receipt/contribution reports only two applied rule triggers.

Add invalid, false, and non-finite condition cases followed by a valid second rule. Build the non-finite condition as `huge := strings.Repeat("9", 200)` and `Condition: huge + "*" + huge`; each literal parses as finite while multiplication overflows to positive infinity. Assert no trigger/log for each skipped rule and continued processing.

Add `TestApplyGiftEventCombinesIdentityConditionAndRandomChoice`. Inject `formulaRandomIntn` to return index 1 and assert the exact limit is 2. Use condition `用户身份>=舰长` and formula `RANDOMCHOICE(积分+1,积分+10)`; apply one fan gift and one captain gift; require final value 10, one random draw, one rule trigger, one effect/log entry, and receipt/contribution data showing only the captain application.

- [ ] **Step 2: Run runtime tests and verify RED**

Run:

```powershell
cd goserver
go test ./... -run 'TestApplyGiftEvent.*Identity|TestApplyGiftEventReevaluatesCondition|TestApplyGiftEventSkipsInvalidCondition|TestApplyGiftEventCombinesIdentityConditionAndRandomChoice' -count=1
```

Expected: compilation fails because `giftRule.Condition` is undefined.

- [ ] **Step 3: Implement live condition evaluation through the shared environment**

Add `Condition` to `giftRule`. In `applyGiftEvent`, replace the ad hoc price/attribute environment with:

```go
environment := buildGiftFormulaEnvironment(*state, attribute.Name, attribute.Value, gift.Price, gift.Membership)
if strings.TrimSpace(rule.Condition) != "" {
    condition, err := evaluateFormula(rule.Condition, environment)
    if err != nil || condition == 0 || math.IsInf(condition, 0) || math.IsNaN(condition) {
        continue
    }
}
```

Evaluate the formula only after this block. Keep the daily-limit increment and success log exactly where they currently occur after a valid result.

Update `formulaPreviewWithPrice` to call the shared builder with ordinary identity, preserving its existing signature for old internal callers.

- [ ] **Step 4: Run runtime tests and verify GREEN**

Run the Step 2 command again plus existing semantic tests:

```powershell
cd goserver
go test ./... -run 'TestApplyGiftEvent' -count=10
```

Expected: PASS.

- [ ] **Step 5: Write failing config validation tests**

Add table-driven PUT tests for all six reserved attribute names, requiring HTTP 400 and a message containing `系统公式名称不能作为属性名`. Add one allowed near-match (`用户身份等级`).

Add imported formula-preset coverage with `sourceAttributeName:"用户身份"`; require HTTP 400 and `系统公式名称不能作为预设来源属性`. This prevents a historical/imported preset from replacing a system variable when applied to another attribute.

Add a rule condition payload:

```json
{
  "attributes":[{"name":"积分","value":0}],
  "rules":[{
    "id":"r1","giftId":1,"attributeName":"积分",
    "formulaName":"舰长规则","condition":"用户身份>=舰长","formula":"积分+1"
  }]
}
```

Require success and persisted condition. Add invalid condition `用户身份>=不存在身份` requiring 400 with `运行条件无效`. Add a timer condition using `用户身份` requiring the existing undefined-variable rejection.

- [ ] **Step 6: Run config tests and verify RED**

Run:

```powershell
cd goserver
go test ./... -run 'TestConfigStore.*ReservedFormulaName|TestConfigStore.*GiftRuleCondition|TestConfigStoreRejectsGiftIdentityInTimer' -count=1
```

Expected: reserved names are accepted or condition is not validated/persisted as required.

- [ ] **Step 7: Implement reserved-name and gift-condition validation**

Inside the existing attribute-name loop, reject `isReservedFormulaName(name)`. In the formula-preset loop, reject `isReservedFormulaName(sourceAttributeName)`. In the gift-rule loop:

```go
if strings.TrimSpace(rule.Condition) != "" {
    if _, err := formulaPreview(state, rule.Condition, attribute.Name, attribute.Value); err != nil {
        return fmt.Errorf("规则 %q 的运行条件无效：%w", rule.FormulaName, err)
    }
}
```

Because `formulaPreview` now uses Task 1's shared ordinary-identity environment, named constants validate. Timer validation remains on `timerFormulaPreview`, so gift-only variables remain unavailable.

- [ ] **Step 8: Write failing preview API tests**

Add requests for:

- `userIdentity: 2`, `condition: "用户身份>=舰长"`, formula `积分+10` → `triggered:true,result:10`.
- `userIdentity: 1` with the same condition → `triggered:false,result:0` (the submitted attribute value).
- values `-1`, `5`, `1.5`, and a JSON string → HTTP 400, stable `用户身份必须是 0 到 4 的整数`.
- invalid condition → HTTP 400 and formula error, not `triggered:false`.
- omitted identity/condition → current selected-price behavior and `triggered:true`.
- timer context with a submitted identity still does not expose `用户身份`.

- [ ] **Step 9: Run preview tests and verify RED**

Run:

```powershell
cd goserver
go test ./... -run 'TestFormulaPreview.*Identity|TestFormulaPreview.*Condition|TestFormulaPreviewUsesSelectedGiftPrice' -count=1
```

Expected: new requests either ignore condition/identity or return the old response shape.

- [ ] **Step 10: Implement strict gift-rule preview**

Extend the decoded request with pointer identity so omission can be distinguished from explicit zero:

```go
Condition    string `json:"condition"`
UserIdentity *int  `json:"userIdentity"`
```

Reject a non-integer through JSON decoding/type mismatch; reject integers outside 0–4. Implement `previewGiftRule` with Task 1's `buildGiftFormulaEnvironmentWithIdentity`, passing the validated integer as `float64`. Do not invent a fake membership string in the HTTP handler.

For a false condition, return the submitted `attributeValue` as `result`. For timer context, keep the existing `{code:0,result}` behavior and do not add gift identity to its environment.

- [ ] **Step 11: Run Task 2 gates**

Run:

```powershell
cd goserver
gofmt -w state.go background_runtime.go background_runtime_semantics_test.go config_store.go config_store_test.go main.go main_test.go
go test ./... -run 'TestApplyGiftEvent|TestConfigStore.*Formula|TestConfigStore.*Identity|TestFormulaPreview' -count=20
go test -race ./... -run 'TestApplyGiftEvent.*Identity|TestFormulaPreview.*Identity' -count=5
go test ./... -count=1 -timeout=300s
git diff --check
```

Expected: all PASS.

- [ ] **Step 12: Commit Task 2**

```powershell
git add -- goserver/state.go goserver/background_runtime.go goserver/background_runtime_semantics_test.go goserver/config_store.go goserver/config_store_test.go goserver/main.go goserver/main_test.go
git diff --cached --check
git commit -m "feat: condition gift rules by user identity"
```

---

### Task 3: Frontend Condition Helpers and Preview Adapter

**Files:**
- Create: `src/gift-rule-conditions.ts`
- Create: `tests/gift-rule-conditions.test.ts`
- Modify: `src/types.ts`
- Modify: `src/backend.ts`
- Modify: `tests/backend.test.ts`
- Modify: `src/formula-presets.ts`
- Modify: `tests/formula-presets.test.ts`
- Modify: `src/simple-play.ts`
- Modify: `tests/simple-play.test.ts`

**Interfaces:**
- Consumes Task 2's preview endpoint.
- Produces:

```ts
export const GIFT_USER_IDENTITIES = [
  { value: 0, name: '普通用户' },
  { value: 1, name: '粉丝团' },
  { value: 2, name: '舰长' },
  { value: 3, name: '提督' },
  { value: 4, name: '总督' },
] as const;

export type GiftUserIdentity = 0 | 1 | 2 | 3 | 4;
export type QuickGiftConditionMode = 'any' | 'equal' | 'atLeast' | 'advanced';
export interface QuickGiftConditionDraft { mode: QuickGiftConditionMode; identity: Exclude<GiftUserIdentity, 0>; }

export function buildQuickGiftCondition(mode: QuickGiftConditionMode, identity: Exclude<GiftUserIdentity, 0>): string | null;
export function detectQuickGiftCondition(condition: string): QuickGiftConditionDraft;
export function isGiftFormulaSystemName(name: string): boolean;

export interface GiftRulePreview {
  triggered: boolean;
  result: number;
}

export async function previewGiftRule(options: {
  condition?: string;
  formula: string;
  attributeName: string;
  attributeValue: number;
  giftPrice?: number;
  userIdentity?: GiftUserIdentity;
}): Promise<GiftRulePreview>;
```

- Keep the existing exported `previewFormula(...)` for timer, preset, and legacy callers. Its request/return behavior must not change.

- [ ] **Step 1: Write failing pure condition-helper tests**

Test exact generation:

```ts
expect(buildQuickGiftCondition('any', 2)).toBe('');
expect(buildQuickGiftCondition('equal', 1)).toBe('用户身份=粉丝团');
expect(buildQuickGiftCondition('atLeast', 2)).toBe('用户身份>=舰长');
expect(buildQuickGiftCondition('advanced', 2)).toBeNull();
```

Test detection with surrounding whitespace for the exact standard forms. Require `用户身份>=普通用户`, reversed operands, attribute-dependent conditions, `==`, and combined expressions to return `{mode:'advanced', identity:2}` without rewriting the input. Test exact identity array order and `isGiftFormulaSystemName` parity with the six Go names.

- [ ] **Step 2: Run helper tests and verify RED**

Run:

```powershell
npm test -- --run tests/gift-rule-conditions.test.ts
```

Expected: module-not-found failure.

- [ ] **Step 3: Implement the pure helper module**

Use escaped exact regular expressions over trimmed condition strings:

```ts
const EQUAL = /^用户身份=(粉丝团|舰长|提督|总督)$/u;
const AT_LEAST = /^用户身份>=(粉丝团|舰长|提督|总督)$/u;
```

Map names through the one exported identity array. `advanced` must return `null` from the builder so the caller knows not to overwrite the current string.

- [ ] **Step 4: Run helper tests and verify GREEN**

Run the Step 2 command. Expected: PASS.

- [ ] **Step 5: Write failing frontend preview adapter tests**

Import `previewGiftRule`. Assert its POST body is exactly:

```json
{
  "condition":"用户身份>=舰长",
  "formula":"积分+1",
  "attributeName":"积分",
  "attributeValue":0,
  "context":"gift",
  "giftPrice":5200,
  "userIdentity":2
}
```

Require strict parsing: `triggered` must be boolean and `result` must be finite number. Reject missing/invalid fields and preserve backend error messages. Add a test that omitted optional values sends `condition:""` and `userIdentity:0`, establishing ordinary-user defaults.

- [ ] **Step 6: Run adapter tests and verify RED**

Run:

```powershell
npm test -- --run tests/backend.test.ts
```

Expected: `previewGiftRule` is not exported.

- [ ] **Step 7: Implement types and adapter**

Add `condition?: string` to `GiftRule`. Export/import `GiftUserIdentity` without duplicating its union. Implement `previewGiftRule` as a dedicated strict adapter. Keep `previewFormula` unchanged so timer callers and existing tests retain their contract.

- [ ] **Step 8: Make renaming preservation explicit**

Add tests proving:

```ts
replaceFormulaVariable('用户身份>=舰长', '积分', '能量') === '用户身份>=舰长'
replaceFormulaVariable('IF(用户身份>=舰长,积分+1,积分)', '积分', '能量')
  === 'IF(用户身份>=舰长,能量+1,能量)'
```

Also require `saveFormulaPreset` and `applyFormulaPreset` to reject a system `sourceAttributeName` with `系统公式名称不能作为预设来源属性`. Implement that guard by importing `isGiftFormulaSystemName`; keep the generic `replaceFormulaVariable` token-boundary helper independent of UI state.

Add simple-play reference tests where a gift rule references the managed attribute only through `condition`. Require that changing the condition changes the managed fingerprint and that replacing the simple-play template cleans that dependent rule. Update both gift-rule calls to `referencesAttribute` so they pass `rule.condition` alongside `attributeName` and `formula`; object spreading already preserves the field.

- [ ] **Step 9: Run Task 3 gates**

Run:

```powershell
npm test -- --run tests/gift-rule-conditions.test.ts tests/backend.test.ts tests/formula-presets.test.ts tests/simple-play.test.ts
npm run typecheck
npm test -- --reporter=dot
git diff --check
```

Expected: all PASS.

- [ ] **Step 10: Commit Task 3**

```powershell
git add -- src/gift-rule-conditions.ts tests/gift-rule-conditions.test.ts src/types.ts src/backend.ts tests/backend.test.ts src/formula-presets.ts tests/formula-presets.test.ts src/simple-play.ts tests/simple-play.test.ts
git diff --cached --check
git commit -m "feat: add gift identity condition client"
```

---

### Task 4: Beginner Identity Conditions, Advanced Editing, and Simulation UX

**Files:**
- Modify: `src/ui/config/config.ts`
- Modify: `src/ui/config/config.css`
- Modify: `tests/wizard.test.ts`

**Interfaces:**
- Consumes Task 3's `GIFT_USER_IDENTITIES`, `GiftUserIdentity`, `QuickGiftConditionMode`, `buildQuickGiftCondition`, `detectQuickGiftCondition`, `isGiftFormulaSystemName`, and `previewGiftRule`.
- Produces persisted `GiftRule.condition`, beginner controls, advanced condition input, and simulated identity UI. No new persistence store.

- [ ] **Step 1: Write failing save/reopen and beginner-condition tests**

Mount a configured rule with `condition: '用户身份>=舰长'`, open the gift-rule workspace, and require:

- condition mode select value `atLeast`;
- identity select value `2` and visible copy `舰长`;
- advanced condition input value `用户身份>=舰长`.

Change mode to `equal`, identity to `1`, save, and assert persisted `condition === '用户身份=粉丝团'`. Reopen and assert it is detected as equal/fan.

Add a rule with `condition: '用户身份>=舰长+积分'`; require mode `advanced`, the advanced input unchanged, and a no-op re-render/save preserves it. Then explicitly choose `any` and require the saved condition to become empty.

- [ ] **Step 2: Run UI condition tests and verify RED**

Run:

```powershell
npm test -- --run tests/wizard.test.ts -t 'gift identity condition|advanced gift condition'
```

Expected: selectors/condition input do not exist and condition is dropped on save.

- [ ] **Step 3: Extend `SelectedGiftRule` and save/rename paths**

Add:

```ts
condition: string;
quickConditionMode?: QuickGiftConditionMode;
quickConditionIdentity?: 1 | 2 | 3 | 4;
simulationIdentity?: GiftUserIdentity;
simulationPreview?: { currentValue: number; result: number; triggered: boolean };
```

Initialize from `rule.condition ?? ''` and default new rules to `condition:''`, `simulationIdentity:0`. Normalize/rename the condition alongside the formula. Include `condition: item.condition.trim()` in replacement rules; preserve unrelated rules exactly.

Before accepting an attribute name in the editor, reject `isGiftFormulaSystemName(name)` with `系统公式名称不能作为属性名：<name>` and focus the name input. Server validation remains authoritative. In the existing save preflight, validate each normalized gift condition through `previewGiftRule` and keep the existing independent formula validation so a false condition cannot hide an invalid result formula.

- [ ] **Step 4: Build beginner and advanced condition controls**

Add stable classes:

- `.quick-rule-condition-mode`
- `.quick-rule-condition-identity`
- `.gift-rule-condition-input`
- `.gift-rule-simulation-identity`

The mode select options are `不限`, `身份等于`, `身份至少`, `高级条件`. The identity select contains `粉丝团`, `舰长`, `提督`, `总督` only. Hide/disable identity for `any` and `advanced`.

On load, call `detectQuickGiftCondition`. On beginner changes, call `buildQuickGiftCondition`; overwrite the advanced input only when the builder returns a string. On manual advanced input, set mode to `advanced` and keep the exact string.

Put the independent condition input in the advanced details before the assignment field. Update responsive CSS so the new condition row collapses to one column at existing mobile breakpoints.

- [ ] **Step 5: Run save/reopen tests and verify GREEN**

Run the Step 2 command again. Expected: PASS.

- [ ] **Step 6: Write failing simulation identity/status tests**

Stub the preview endpoint, choose simulated identity `提督` (3), and click simulate. Assert the request contains:

```json
{
  "condition":"用户身份>=舰长",
  "userIdentity":3
}
```

For `{triggered:false,result:10}`, require preview text `本次不会触发` and do not advance the shared simulation draft. Click again after returning `{triggered:true,result:11}` and require `10 → 11` plus draft advancement. Verify the selected identity is UI-only and is absent from the saved `GiftRule`.

Add stale response coverage: an older response for another identity/condition cannot overwrite a newer preview or advance the draft.

- [ ] **Step 7: Run simulation tests and verify RED**

Run:

```powershell
npm test -- --run tests/wizard.test.ts -t 'simulated gift identity|identity condition preview'
```

Expected: current code calls `previewFormula`, has no identity, and always advances on a numeric result.

- [ ] **Step 8: Migrate gift editor preview to `previewGiftRule`**

Use the normalized current condition and `item.simulationIdentity ?? 0`. For passive previews and explicit simulation, render based on `triggered`; only explicit triggered simulations update `simulationDraftValue`. Keep existing request-version and simulation-generation stale guards.

Do not migrate timer preview calls. Update the default test fetch mock for gift preview to return `triggered:true` so old tests keep their intent.

- [ ] **Step 9: Update formula help and reserved-name tests**

Extend help with:

- `用户身份` and all named identity constants, explicitly labeled “仅礼物规则可用” so timer users are not misled;
- `RANDOMCHOICE(A,B,...)` described as randomly returning one argument;
- examples `用户身份>=舰长` and `RANDOMCHOICE(10,20,50)`;
- explicit copy that equality uses `=`.

Add UI tests rejecting each reserved name before save and accepting `用户身份等级`. Assert saved formulas/conditions retain identity constants during an unrelated attribute rename.

- [ ] **Step 10: Run Task 4 focused and full frontend gates**

Run:

```powershell
npm test -- --run tests/wizard.test.ts tests/gift-rule-conditions.test.ts tests/backend.test.ts
npm run typecheck
npm test -- --reporter=dot
npm run build:ui
git diff --check
```

Expected: all PASS with no new skipped tests.

- [ ] **Step 11: Commit Task 4**

```powershell
git add -- src/ui/config/config.ts src/ui/config/config.css tests/wizard.test.ts
git diff --cached --check
git commit -m "feat: edit gift identity conditions"
```

---

### Task 5: Integrated Compatibility and Release-Shape Verification

**Files:**
- Modify: `docs/superpowers/specs/2026-08-14-gift-user-identity-and-random-choice-design.md` only for confirmed implementation corrections.
- Create: `.superpowers/sdd/2026-08-14-gift-user-identity-and-random-choice/final-report.md` (force-add because `.superpowers/` is ignored).

**Interfaces:**
- Consumes all prior task commits.
- Produces a verified feature branch with one evidence report; no release, tag, push, or publish.

- [ ] **Step 1: Run architecture and compatibility searches**

Run:

```powershell
rg -n "用户身份|普通用户|粉丝团|舰长|提督|总督" goserver src tests
rg -n "用户身份==" goserver src tests
rg -n "RANDOMCHOICE\(\)" goserver src tests docs/superpowers
rg -n "formulaRandomIntn" goserver
```

Expected:

- identity mapping implementation is only in `gift_formula_context.go`;
- UI contains display values/builders but no membership-string mapping;
- no `用户身份==` exists;
- zero-argument `RANDOMCHOICE()` appears only in negative tests/docs;
- all variadic random selection routes through `formulaRandomIntn`.

- [ ] **Step 2: Run fresh sequential backend gates**

Run:

```powershell
cd goserver
go test ./... -count=1 -timeout=300s
go test -race ./... -count=1 -timeout=300s
```

Expected: PASS. Do not run these concurrently because tests and build gates may share generated assets/state.

- [ ] **Step 3: Run fresh sequential frontend and package gates**

From repository root:

```powershell
npm run typecheck
npm test -- --reporter=dot
npm run build:ui
npm run build:exe
git diff --check
```

Expected: PASS; packaged UI assets include the existing dynamic config graph and no source/test artifact leaks into `goserver/dist` or the EXE.

- [ ] **Step 4: Write the final report**

Record:

- fixed start/end commit IDs and each independent task commit;
- RED command and exact failure signature for every task;
- focused/full/race/typecheck/build results with pass counts and durations;
- final identity mapping and reserved names;
- false/error condition behavior and trigger/log evidence;
- `RANDOMCHOICE` lazy/recovery evidence;
- UI save/reopen/advanced-preservation/simulation evidence;
- `git status --short`, `git diff --check`, and prohibited-action audit (no push/tag/release).

Do not claim a command passed unless its fresh output was observed.

- [ ] **Step 5: Request fixed-base two-axis review**

Review Standards and Spec independently against the design document and this plan. Any Critical or Important finding must receive its own RED→GREEN fix commit and a fresh affected gate. Minor findings may be documented only when they are demonstrably non-load-bearing.

- [ ] **Step 6: Commit final report or review fixes**

If no code fix is required:

```powershell
git add -f -- .superpowers/sdd/2026-08-14-gift-user-identity-and-random-choice/final-report.md
git diff --cached --check
git commit -m "test: verify gift identity rule conditions"
```

If review fixes code, stage only the affected owned files plus the force-added report and use a precise `fix:` commit message. End with an empty index and a clean `git status --short`.
