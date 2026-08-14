# Gift identity conditions and `RANDOMCHOICE` — final verification

## Fixed range and task commits

- Complete feature start: `a320590cec6bbdaff2a0e7a2e521f9a6279f2058`; Task-5 verification base / verified implementation end: `5a0711d873c3ef10569a2a60b5f2af4c0e9d129c` (`fix: rename cross-attribute gift references`).
- Task 1: `0f0dfce79cafb3f8469b4de71bf028a548d454c3` (`feat: add gift identity formula context`). Task 2: `ef6df174391addc16b726aec80fa509d87f79403` (`feat: condition gift rules by user identity`).
- Task 3: `9ffd0e5` (`feat: add gift identity condition client`) and `ba41cd6` (`fix: recognize empty gift conditions`). Task 4: `b29466e` (`feat: edit gift identity conditions`), `e7ee09a` (`fix: preserve gift identity previews`), `5a0711d` (`fix: rename cross-attribute gift references`).
- Evidence-report parent commit: `3ffc323743c200d11b96f077d9f12a5c89a3f2f8` (`test: verify gift identity rule conditions`). This correction’s commit cannot truthfully self-identify inside its own content; Git history identifies the successor with parent `3ffc323`.
- Task 5 changes evidence only. Controller-owned fixed-base review is outside this task. The no-push/tag/release/publish statement below is an operator/controller action audit, not a claim that repository files prove remote history.

## Prior RED → GREEN evidence

Each RED entry quotes only the original task report. Some reports captured a failure signature but not raw test stdout; those entries say so rather than claiming a later reconstruction. Every GREEN entry marked “fresh” was rerun on the current implementation with the exact command shown.

### Task 1 — identity context

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestGiftIdentityLevel|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames' -count=1`
- **RED output (verbatim excerpt):** `Exit 1 as expected: build failed because giftIdentityLevel, both environment builders, giftIdentityCaptain, and isReservedFormulaName did not exist.`
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestGiftIdentityLevel|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames' -count=1`
- **GREEN output (verbatim excerpt):** Original: `Exit 0: ok bilibili-live-gift-panel.` Original duration: not captured. Fresh: `ok bilibili-live-gift-panel 1.475s` (exit 0).
- **Source:** `task-1-report.md`, “TDD evidence / RED: identity context” and “GREEN: identity context”; fresh GREEN run in this Task-5 correction.

### Task 1 — lazy `RANDOMCHOICE`

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaRandomChoice' -count=1`
- **RED output (verbatim excerpt):** `Exit 1 as expected: all new cases failed with 未知函数 "RANDOMCHOICE".`
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaRandomChoice' -count=1`
- **GREEN output (verbatim excerpt):** Original: `Exit 0: ok bilibili-live-gift-panel.` Original duration: not captured; high-count focus was `ok` in 2.144s and race focus `ok` in 3.050s. Fresh: `ok bilibili-live-gift-panel 1.330s` (exit 0).
- **Source:** `task-1-report.md`, “TDD evidence / RED: RANDOMCHOICE”, “GREEN: RANDOMCHOICE”, and “Final verification”; fresh GREEN run in this Task-5 correction.

### Task 2 — runtime conditions and joint random choice

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestApplyGiftEvent.*Identity|TestApplyGiftEventReevaluatesCondition|TestApplyGiftEventSkipsInvalidCondition|TestApplyGiftEventCombinesIdentityConditionAndRandomChoice' -count=1`
- **RED output (verbatim excerpt):** `failed with unknown field Condition in struct literal of type giftRule` (original report captured no raw test stdout).
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestApplyGiftEvent.*Identity|TestApplyGiftEventReevaluatesCondition|TestApplyGiftEventSkipsInvalidCondition|TestApplyGiftEventCombinesIdentityConditionAndRandomChoice' -count=1`
- **GREEN output (verbatim excerpt):** Fresh: `ok bilibili-live-gift-panel 1.452s` (exit 0).
- **Source:** `task-2-report.md`, “RED runtime” / “GREEN runtime”; fresh GREEN run in this Task-5 correction.

### Task 2 — config validation

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestConfigStore.*ReservedFormulaName|TestConfigStore.*GiftRuleCondition|TestConfigStoreRejectsGiftIdentityInTimer' -count=1`
- **RED output (verbatim excerpt):** `reserved names/preset source/invalid condition tests failed because all three were accepted.` Original RED output was not captured.
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestConfigStore.*ReservedFormulaName|TestConfigStore.*GiftRuleCondition|TestConfigStoreRejectsGiftIdentityInTimer' -count=1`
- **GREEN output (verbatim excerpt):** Fresh: `ok bilibili-live-gift-panel 1.381s` (exit 0).
- **Source:** `task-2-report.md`, “RED config” / “GREEN config”; command spelling from `task-2-brief.md`, Step 6/7; fresh GREEN run in this Task-5 correction.

### Task 2 — gift preview contract

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaPreview.*Identity|TestFormulaPreview.*Condition|TestFormulaPreviewUsesSelectedGiftPrice' -count=1`
- **RED output (verbatim excerpt):** `new identity/condition tests failed because the endpoint returned the old {code,result} response and ignored input.` Original RED output was not captured.
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaPreview.*Identity|TestFormulaPreview.*Condition|TestFormulaPreviewUsesSelectedGiftPrice' -count=1`
- **GREEN output (verbatim excerpt):** Fresh: `ok bilibili-live-gift-panel 1.433s` (exit 0).
- **Source:** `task-2-report.md`, “RED preview” / “GREEN preview”; command spelling from `task-2-brief.md`, Step 9/10; fresh GREEN run in this Task-5 correction.

### Task 3 — helper, adapter, and simple-play focus

- **RED command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts`
- **RED output (verbatim excerpt):** `missing module.` Original report did not capture the resolver/test stdout.
- **GREEN command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts tests/backend.test.ts tests/formula-presets.test.ts tests/simple-play.test.ts`
- **GREEN output (verbatim excerpt):** Fresh: `Test Files 4 passed (4)`; `Tests 55 passed (55)`; `Duration 882ms`; exit 0.
- **Source:** `task-3-report.md`, initial “RED” / “GREEN”; focused command from `task-3-brief.md`, Step 9; fresh GREEN run in this Task-5 correction.

### Task 3 — preview adapter

- **RED command (verbatim):** `npm test -- --run tests/backend.test.ts`
- **RED output (verbatim excerpt):** `previewGiftRule is not a function.` Original report did not capture the test count/stdout.
- **GREEN command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts tests/backend.test.ts tests/formula-presets.test.ts tests/simple-play.test.ts`
- **GREEN output (verbatim excerpt):** Fresh shared Task-3 focus: `Test Files 4 passed (4)`; `Tests 55 passed (55)`; `Duration 882ms`; exit 0.
- **Source:** `task-3-report.md`, initial “RED” / “GREEN”; command from `task-3-brief.md`, Step 6/9; fresh GREEN run in this Task-5 correction.

### Task 3 — empty-condition review fix

- **RED command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts`
- **RED output (verbatim excerpt):** `failed: empty condition returned advanced instead of any.` Original detailed assertion output was not captured.
- **GREEN command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts`
- **GREEN output (verbatim excerpt):** Fresh: `Test Files 1 passed (1)`; `Tests 8 passed (8)`; `Duration 208ms`; exit 0.
- **Source:** `task-3-report.md`, “Review Fix Round 1”; fresh GREEN run in this Task-5 correction.

### Task 4 — save/reopen and advanced preservation

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'gift identity condition|advanced gift condition'`
- **RED output (verbatim excerpt):** `2 failed (condition controls absent).` Original report did not capture test stdout.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'gift identity condition|advanced gift condition'`
- **GREEN output (verbatim excerpt):** Fresh: `Test Files 1 passed (1)`; `Tests 2 passed | 153 skipped (155)`; `Duration 948ms`; exit 0.
- **Source:** `task-4-report.md`, “TDD C1”; fresh GREEN run in this Task-5 correction.

### Task 4 — simulation identity and stale preview

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'simulated gift identity|identity condition preview'`
- **RED output (verbatim excerpt):** `2 failed (old preview advanced draft/no identity payload).` Original report did not capture test stdout.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'simulated gift identity|identity condition preview'`
- **GREEN output (verbatim excerpt):** Fresh: `Test Files 1 passed (1)`; `Tests 2 passed | 153 skipped (155)`; `Duration 1.02s`; exit 0.
- **Source:** `task-4-report.md`, “TDD C2”; fresh GREEN run in this Task-5 correction.

### Task 4 — formula help and reserved names

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'formula help explains|rejects reserved gift formula names'`
- **RED output (verbatim excerpt):** `help test failed (identity help absent).` Original report did not capture the reserved-name test stdout.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'formula help explains|rejects reserved gift formula names'`
- **GREEN output (verbatim excerpt):** Fresh: `Test Files 1 passed (1)`; `Tests 2 passed | 153 skipped (155)`; `Duration 811ms`; exit 0.
- **Source:** `task-4-report.md`, “TDD C3”; fresh GREEN run in this Task-5 correction.

### Task 4 review fix 1 — false snapshot, optional shape, stale guard

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'skipped gift simulation|identity condition preview|unrelated gift rule'`
- **RED output (verbatim excerpt):** `2 failed (false snapshot was lost on rerender; unrelated rule gained condition:'').` Original report records that stale-draft hardening already passed; raw test stdout was not captured.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'skipped gift simulation|identity condition preview|unrelated gift rule'`
- **GREEN output (verbatim excerpt):** Fresh: `Test Files 1 passed (1)`; `Tests 3 passed | 152 skipped (155)`; `Duration 1.00s`; exit 0.
- **Source:** `task-4-report.md`, “Review fix round 1”; fresh GREEN run in this Task-5 correction.

### Task 4 review fix 2 — cross-attribute formula and condition rename

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'unrelated gift rule'`
- **RED output (verbatim excerpt):** `1 failed because the 积分 rule retained 积分+加班时间 and its existing condition retained 加班时间 after renaming.` Original report did not capture assertion stdout.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'unrelated gift rule'`
- **GREEN output (verbatim excerpt):** Fresh: `Test Files 1 passed (1)`; `Tests 1 passed | 154 skipped (155)`; `Duration 800ms`; exit 0.
- **Source:** `task-4-report.md`, “Fix round 2”; fresh GREEN run in this Task-5 correction.

### Task 5

- No RED occurred: fresh compatibility/gate evidence did not identify an owned Task1–4 regression, so no implementation change was authorized.

## Fresh serial gates on `5a0711d`

| Gate | Fresh observed result |
|---|---|
| `rg -n "用户身份|普通用户|粉丝团|舰长|提督|总督" goserver src tests` | exit 0, 0.3s; backend membership-to-level mapping occurs only in `goserver/gift_formula_context.go`; UI supplies display values/builders, not membership-string mapping. |
| `rg -n "用户身份==" goserver src tests` | exit 0, 0.3s; one deliberate negative-test literal only: `tests/gift-rule-conditions.test.ts:28`; no product implementation accepts `==`. |
| `rg -n "RANDOMCHOICE\(\)" goserver src tests docs/superpowers` | exit 0, 0.3s; one Go negative test plus design/plan documentation only. |
| `rg -n "formulaRandomIntn" goserver` | exit 0, 0.3s; variadic selection is `formula.go:114` and all tests inject the same seam; `RANDINT` also uses the helper at `formula.go:185`. |
| `cd goserver; go test ./... -count=1 -timeout=300s` | exit 0; 1 package, 0 failures, 23.317s (`ok bilibili-live-gift-panel`). |
| `cd goserver; go test -race ./... -count=1 -timeout=300s` | exit 0; 1 package, 0 races/failures, 39.378s. |
| `npm run typecheck` | exit 0, 2.5s; `tsc --noEmit` reported 0 errors. |
| `npm test -- --reporter=dot` | exit 0; 43 files, 497 passed, 31 skipped, 0 failed; Vitest duration 7.07s. |
| `npm run build:ui` | exit 0; 88 transformed modules; built in 508ms. |
| `npm run build:exe` | exit 0, 3.9s; FFmpeg checksum verified, 80 UI assets embedded, `dist/gift-panel.exe` built. |
| `git diff --check` | exit 0, 0.3s; no whitespace errors. |

The gates above were run strictly in the listed order; no tests/builds were concurrent.

## Embedded UI input/output audit (after `build:exe`)

- `node --input-type=module -e "import { verifyUiAssetManifest } from './scripts/ui-assets.mjs'; const manifest = verifyUiAssetManifest('./goserver/dist'); console.log('verified manifest v' + manifest.version + ': ' + manifest.files.length + ' files');"` → exit 0, `verified manifest v1: 80 files`. `verifyUiAssetManifest` checks schema, exact complete target path set, and every file’s size/SHA-256 against `ui-assets.json`.
- `Get-ChildItem -File -Recurse 'goserver\\dist' | Measure-Object | Select-Object Count` → exit 0, `Count 81`: the 80 verified assets plus the one manifest. `Get-ChildItem -File -Recurse 'goserver\\dist' | ForEach-Object { $_.FullName.Replace((Resolve-Path '.').Path + '\\', '') } | Sort-Object` → exit 0 and enumerated those 81 paths (only `index.html`, hashed UI `.js`/`.css` modules, one `.webp`, and `ui-assets.json`).
- Exact read-only forbidden-name audit (raw-source extensions `.go/.ts/.tsx/.mjs/.c/.cc/.cpp/.h/.hpp/.map/.md/.ps1`, test/spec names, report name, excluded executables, and `ffmpeg`, `ffmpeg-source`, `gift-clip-test-tools`, `msys2-toolchain`, `msys2-toolchain-root` markers):

  ```powershell
  & {
    $auditRoot = (Resolve-Path 'goserver\dist').Path
    $forbidden = '(?i)(^|\\)(ffmpeg|ffmpeg-source|gift-clip-test-tools|msys2-toolchain|msys2-toolchain-root)(\\|$)|(^|\\)(gift-panel(?:-windows-x64)?\.exe(?:\.sha256)?|gift-panel-update\.json|ffmpeg-build-config\.txt|ffmpeg-component-gate\.txt|final-report\.md)$|\.(go|ts|tsx|mjs|c|cc|cpp|h|hpp|map|md|ps1)$|(^|\\)[^\\]*\.(test|spec)\.[^\\]+$'
    $hits = @(Get-ChildItem -File -Recurse 'goserver\dist' | ForEach-Object {
      $relative = $_.FullName.Substring($auditRoot.Length + 1)
      if ($relative -match $forbidden) { $relative }
    })
    if ($hits.Count -gt 0) { $hits; exit 1 }
    'forbidden source/test/toolchain/report markers: 0'
  }
  ```

  It exited 0 with exactly `forbidden source/test/toolchain/report markers: 0`.
- `node --input-type=module -e "import { readFileSync, statSync } from 'node:fs'; const bytes = readFileSync('./dist/gift-panel.exe'); const missing = ['dist/ui-assets.json','dist/index.html'].filter((marker) => !bytes.includes(Buffer.from(marker))); console.log('gift-panel.exe bytes=' + statSync('./dist/gift-panel.exe').size + '; embedded markers=' + (missing.length ? 'missing ' + missing.join(',') : 'ui-assets.json,index.html')); if (missing.length) process.exit(1);"` → exit 0, `gift-panel.exe bytes=13902848; embedded markers=ui-assets.json,index.html`.

Together, the build output, complete manifest/hash closure, all-file enumeration, rejected-marker audit, and binary markers prove the inspected embedded input and its produced EXE contain the intended UI graph without raw source, test, toolchain, or report leakage. No `dist` content was deleted or staged.

## Contract evidence

- Identity names are reserved and mapped as `普通用户=0`, `粉丝团=1`, `舰长=2`, `提督=3`, `总督=4`; user identity is `用户身份`. The context is built afresh per gift event, identity constants override attributes/target, and non-gift timer context rejects identity use.
- `TestApplyGiftEventSkipsInvalidConditionAndContinues` asserts invalid, false, and non-finite conditions leave the skipped attribute at 0, create zero triggers, and create no logs; the valid peer rule still reaches 1 and is the sole log.
- Existing Task-2 integration test `TestApplyGiftEventCombinesIdentityConditionAndRandomChoice` is sufficient: a fan is skipped before the draw; captain selects branch 2 of 2, value becomes 10, exactly one draw/trigger/log/effect/contribution is recorded.
- `RANDOMCHOICE` rejects zero arguments, avoids a random draw for one argument, calls `formulaRandomIntn` for variadic calls, evaluates exactly the chosen branch, and recovery uses the persisted selected result without re-evaluation (Task-1 focused/race evidence).
- Current full Vitest includes wizard assertions for save/reopen/beginner update, exact advanced-condition preservation until explicit replacement, simulated identity request plus skipped-preview draft protection, rerender retention, stale response suppression, and optional-condition preservation during rename.

## Scope, status, and prohibited-action audit

- Feature range `a320590..5a0711d` contains only the Task1–4 Go/frontend files and tests; pre-report `git status --short` was empty. This Task-5 correction modifies only this force-added report.
- Controller/operator audit: no push, tag, release, publish, branch switch, `progress.md` edit, plan-workspace edit, or ignored `dist` artifact staging was performed by this Task-5 agent. No implementation file or design document was modified in Task 5.
- Concern (non-load-bearing): the brief’s literal “no `用户身份==` exists” expectation conflicts with the deliberate negative test that proves this syntax is rejected. It is confined to that test and does not represent accepted product syntax.

## Final review fix: deterministic formula validation (2026-08-14)

### Finding and root cause

- Fixed base: `e196d51b9aac5fa00b009e029fd8f58ff75f9991`.
- `validateAppState` called `formulaPreview` / `timerFormulaPreview`, and the UI save preflight called runtime preview adapters. Both paths evaluated formulas while deciding whether they were structurally valid.
- Consequently, `RANDOMCHOICE(10,1/0)` could be accepted or rejected depending on the drawn branch, even though runtime `RANDOMCHOICE` correctly evaluates only the selected branch.

### RED evidence

- Go: `go test ./... -run TestConfigStoreValidationDoesNotEvaluateRandomChoiceBranches -count=1` failed deterministically when the injected choice selected branch 1: HTTP 400 with `规则 "惰性随机规则" 的运行条件无效：除数为零`.
- UI: `npm test -- --run tests/wizard.test.ts -t "uses deterministic validation when saving lazy random-choice rules"` failed with the editor still open and toast `规则有误：除数为零`.

### Implementation and behavioral evidence

- Parsing is shared by evaluation and validation. Structural validation recursively checks every AST branch for syntax, identifiers, known function names, and arity without evaluating arithmetic, drawing randomness, or checking runtime numeric outcomes.
- Function contracts covered: `IF(3)`, `RANDOMCHOICE(>=1)`, `MAX/MIN(>=1)`, `ROUND(1-2)`, `ABS/FLOOR(1)`, `RAND(0)`, and `RANDBETWEEN(2)`; unknown functions fail.
- Gift validation uses attributes plus `price` and identity system names. Timer validation uses attributes only, preserving the gift-only context boundary.
- Config saving uses structural validation. `/api/formula/preview` accepts backward-compatible `validateOnly:true`; UI save preflight uses it, while interactive preview and live execution retain runtime evaluation.
- `TestFormulaNestedLazyRandomCompositionUsesOnlySelectedExpressions` deterministically composes `IF`, `RANDOMCHOICE`, `RANDBETWEEN`, an attribute expression, and invalid unselected branches. It proves exactly two expected draws and a result of 13.

### GREEN and final gates

| Gate | Fresh observed result |
|---|---|
| Focused Go structural/config/API/nested suite | exit 0; PASS. |
| Affected Go stress (`-count=20`) | exit 0; PASS in 3.376s. |
| Focused race (`-count=5`) | exit 0; PASS in 1.623s. |
| `go test ./... -count=1 -timeout=300s` | exit 0; PASS in 22.367s. |
| `go test -race ./... -count=1 -timeout=300s` | exit 0; PASS in 38.850s. |
| `npm run typecheck` | exit 0; 0 TypeScript errors. |
| Focused backend/wizard validation suite | 3 passed, 189 skipped; exit 0. |
| `npm test -- --reporter=dot` | 43 files, 500 passed, 31 skipped, 0 failed; exit 0. |
| `git diff --check` | exit 0; no whitespace errors. |

No push, tag, release, package, version, or FFmpeg action was performed for this fix.

## Final review fix round 2: guaranteed formula semantics (2026-08-14)

### Finding and RED evidence

- Fixed base: `2677a4761966ab2f01c56161dbee162b1a692406`.
- The first deterministic validator correctly avoided random/lazy evaluation, but its structural-only pass also accepted formulas that are guaranteed to fail whenever executed.
- `go test ./... -run 'TestFormulaValidationRejectsGuaranteedRuntimeErrors|TestConfigStoreRejectsGuaranteedFormulaRuntimeErrors|TestFormulaPreviewValidateOnlyRejectsGuaranteedRuntimeErrors' -count=1` failed because `1/0`, a constant overflow expression, and `RANDBETWEEN(10,1)` all returned nil/HTTP 200.

### Implementation and coverage

- Structural validation still walks the complete AST first for syntax, names, function support, arity, and gift/timer context.
- A separate semantic pass propagates only deterministic literal results. Variables and genuine random results remain unknown and their current preview values are not treated as constants.
- Binary operands and ordinary function arguments are semantically checked because runtime always evaluates them. Guaranteed division by zero and a final known NaN/Inf are rejected.
- `IF` always checks its condition. A constant condition follows only its guaranteed chosen branch; a runtime-dependent condition semantically checks neither lazy branch.
- One-argument `RANDOMCHOICE` checks its guaranteed argument. Multi-argument `RANDOMCHOICE` checks no alternative semantically and never draws randomness; structural validation still checks every alternative.
- Constant `RANDBETWEEN` bounds enforce the runtime range rule without drawing. Runtime-dependent bounds remain saveable.
- Tests cover constant/variable divisors, constant overflow, constant/runtime ranges, guaranteed ordinary-function arguments, constant/unknown `IF`, one/multiple random choices in both alternative orders, and zero validation draws.

### Fresh gates

| Gate | Fresh observed result |
|---|---|
| Focused semantic/config/validate-only GREEN | exit 0; PASS. |
| Focused stress (`-count=20`) | exit 0; PASS in 2.531s. |
| Focused race (`-count=5`) | exit 0; PASS in 1.593s. |
| `go test ./... -count=1 -timeout=300s` | exit 0; PASS in 24.630s. |
| `go test -race ./... -count=1 -timeout=300s` | exit 0; PASS in 39.336s. |
| `npm run typecheck` | exit 0; 0 TypeScript errors. |
| `npm test -- --reporter=dot` | 43 files, 500 passed, 31 skipped, 0 failed; exit 0. |
| `git diff --check` | exit 0; no whitespace errors. |

No push, tag, release, package, version, or FFmpeg action was performed for round 2.

## Final review fix round 3: abstract nonfinite propagation (2026-08-14)

### Finding and RED evidence

- Fixed base: `4d9acec6e2960f0d6d7ba05c43dd1febd42e5bfb`.
- Round 2's `{known,value}` analysis lost guaranteed nonfinite information as soon as an exact nonfinite subtree was combined with a finite runtime variable.
- The focused RED suite showed unit, config PUT, and validation-only HTTP acceptance of `积分*(constant overflow)`, `积分/ROUND(1,309)` (finite divided by NaN), and `(constant overflow)/积分`.

### Abstract domain and contract

- Runtime formula variables are modeled as finite unknowns. UI attribute saving rejects nonfinite values, JSON numeric decoding cannot persist IEEE NaN/Inf, live formula results are persisted only after explicit NaN/Inf rejection, and identity/product price values are finite numeric inputs by product contract.
- The semantic lattice is a bitset of `{zero, finite-nonzero, infinity, NaN}` plus exact-value refinement. Binary transfer enumerates possible class pairs and separately excludes evaluator-error paths such as division by zero.
- Validation rejects a final expression only when no valid finite result class remains. This proves `finite*Inf`, `finite/NaN`, and `Inf/finite` invalid for every finite variable value, including zero/error cases.
- Conservative negative controls remain accepted when at least one finite outcome exists: `finite/Inf` (zero), finite-variable arithmetic/division, comparisons involving nonfinite values (numeric booleans), variable-dependent ranges, unknown `IF` branches, and multi-argument `RANDOMCHOICE` alternatives.
- Exact constants retain round-2 division, overflow, `IF`, one-argument choice, and constant `RANDBETWEEN` behavior. Runtime evaluation and random selection remain unchanged.

### Fresh gates

| Gate | Fresh observed result |
|---|---|
| Focused lattice/config/validate-only/no-draw GREEN | exit 0; PASS. |
| Focused stress (`-count=20`) | exit 0; PASS in 2.362s. |
| Focused race (`-count=5`) | exit 0; PASS in 2.173s. |
| `go test ./... -count=1 -timeout=300s` | exit 0; PASS in 23.208s. |
| `go test -race ./... -count=1 -timeout=300s` | exit 0; PASS in 39.540s. |
| `npm run typecheck` | exit 0; 0 TypeScript errors. |
| `npm test -- --reporter=dot` | 43 files, 500 passed, 31 skipped, 0 failed; exit 0. |
| `git diff --check` | exit 0; no whitespace errors. |

No push, tag, release, package, version, or FFmpeg action was performed for round 3.

## Final review fix round 4: supported-function abstract transfers (2026-08-14)

### Finding and RED evidence

- Fixed base: `6107973062907791b710c5615d1c6ea25dce64da`.
- The round-3 lattice handled non-exact binary operators, but returned an overly broad top result for non-exact `MAX`, `MIN`, and `ROUND` calls.
- Focused unit, config PUT, and validation-only HTTP RED tests showed `MAX(积分,+Inf)`, `MIN(积分,-Inf)`, and `ROUND(积分,309)` were incorrectly accepted.

### Function-transfer audit

- Infinity was refined into positive and negative classes so extrema dominance is expressible without false rejection.
- `MAX`/`MIN` now fold every eager argument through explicit class pairs, including NaN propagation and signed-infinity dominance. Required dominant-infinity formulas reject; `MAX(积分,-Inf)`, `MIN(积分,+Inf)`, and finite extrema retain finite outcomes.
- `ROUND` with exact digits classifies its `10^digits` scale. Zero, infinity, or NaN scale guarantees NaN for a finite unknown value and rejects; ordinary scales retain finite outcomes while conservatively including possible overflow. Unknown digits remain top.
- `ABS` maps negative infinity to positive infinity and otherwise preserves zero/finite/NaN classes. `FLOOR` preserves classes. Exact calls still use the runtime math operations.
- `IF`, one/multi-argument `RANDOMCHOICE`, `RAND`, and exact/non-exact `RANDBETWEEN` retain the prior guaranteed/lazy/random rules. All ordinary eager function arguments are recursively checked before transfer.

### Fresh gates

| Gate | Fresh observed result |
|---|---|
| Focused function-transfer/config/validate-only/no-draw GREEN | exit 0; PASS. |
| Focused stress (`-count=20`) | exit 0; PASS in 2.404s. |
| Focused race (`-count=5`) | exit 0; PASS in 1.627s. |
| `go test ./... -count=1 -timeout=300s` | exit 0; PASS in 23.100s. |
| `go test -race ./... -count=1 -timeout=300s` | exit 0; PASS in 39.654s. |
| `npm run typecheck` | exit 0; 0 TypeScript errors. |
| `npm test -- --reporter=dot` | 43 files, 500 passed, 31 skipped, 0 failed; exit 0. |
| `git diff --check` | exit 0; no whitespace errors. |

No push, tag, release, package, version, or FFmpeg action was performed for round 4.

## Final review fix round 5: ROUND value-first transfer (2026-08-14)

### Finding and fix

- Fixed base: `df30aac8db9337a5f7550da3bd51a1a454608f69`.
- `ROUND` returned top immediately when its digits argument was non-exact, losing the stronger fact that an already guaranteed-nonfinite value cannot become finite for any finite digits.
- Unit, config PUT, and validation-only HTTP RED tests captured `ROUND(+Inf,积分)`; unit tests also covered negative infinity and NaN values.
- The transfer now checks the value class first. If it has no finite member, the result remains guaranteed nonfinite before digits precision is considered. Values with a possible finite member still use conservative top for unknown digits.
- Negative controls include `ROUND(积分,price)`, `ROUND(积分+10,price)`, and a multi-choice maybe-finite value with variable digits. Lazy/no-random behavior remains unchanged.

### Fresh gates

| Gate | Fresh observed result |
|---|---|
| Focused ROUND/config/validate-only/no-draw GREEN | exit 0; PASS. |
| Focused stress (`-count=20`) | exit 0; PASS in 2.359s. |
| Focused race (`-count=5`) | exit 0; PASS in 1.664s. |
| `go test ./... -count=1 -timeout=300s` | exit 0; PASS in 23.032s. |
| `go test -race ./... -count=1 -timeout=300s` | exit 0; PASS in 39.530s. |
| `npm run typecheck` | exit 0; 0 TypeScript errors. |
| `npm test -- --reporter=dot` | 43 files, 500 passed, 31 skipped, 0 failed; exit 0. |
| `git diff --check` | exit 0; no whitespace errors. |

No push, tag, release, package, version, or FFmpeg action was performed for round 5.
