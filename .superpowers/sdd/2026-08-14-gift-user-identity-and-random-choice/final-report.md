# Gift identity conditions and `RANDOMCHOICE` — final verification

## Fixed range and task commits

- Complete feature start: `a320590cec6bbdaff2a0e7a2e521f9a6279f2058`; Task-5 verification base / verified implementation end: `5a0711d873c3ef10569a2a60b5f2af4c0e9d129c` (`fix: rename cross-attribute gift references`).
- Task 1: `0f0dfce79cafb3f8469b4de71bf028a548d454c3` (`feat: add gift identity formula context`). Task 2: `ef6df174391addc16b726aec80fa509d87f79403` (`feat: condition gift rules by user identity`).
- Task 3: `9ffd0e5` (`feat: add gift identity condition client`) and `ba41cd6` (`fix: recognize empty gift conditions`). Task 4: `b29466e` (`feat: edit gift identity conditions`), `e7ee09a` (`fix: preserve gift identity previews`), `5a0711d` (`fix: rename cross-attribute gift references`).
- Evidence-report parent commit: `3ffc323743c200d11b96f077d9f12a5c89a3f2f8` (`test: verify gift identity rule conditions`). This correction’s commit cannot truthfully self-identify inside its own content; Git history identifies the successor with parent `3ffc323`.
- Task 5 changes evidence only. Controller-owned fixed-base review is outside this task. The no-push/tag/release/publish statement below is an operator/controller action audit, not a claim that repository files prove remote history.

## Prior RED → GREEN evidence

Each cycle uses exact report wording where captured. `retrospective fixed-base reproduction` means the original report lacked test stdout, so the exact command was replayed in an isolated temporary detached worktree at the parent commit with only the corresponding final test restored; it never modified this branch. Original GREEN durations that were not preserved are explicitly marked, not invented.

### Task 1 — identity context

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestGiftIdentityLevel|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames' -count=1`
- **RED output (verbatim excerpt):** `Exit 1 as expected: build failed because giftIdentityLevel, both environment builders, giftIdentityCaptain, and isReservedFormulaName did not exist.`
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestGiftIdentityLevel|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames' -count=1`
- **GREEN output (verbatim excerpt):** `Exit 0: ok bilibili-live-gift-panel.` Duration: not captured in original task report.
- **Source:** `task-1-report.md`, “TDD evidence / RED: identity context” and “GREEN: identity context”.

### Task 1 — lazy `RANDOMCHOICE`

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaRandomChoice' -count=1`
- **RED output (verbatim excerpt):** `Exit 1 as expected: all new cases failed with 未知函数 "RANDOMCHOICE".`
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaRandomChoice' -count=1`
- **GREEN output (verbatim excerpt):** `Exit 0: ok bilibili-live-gift-panel.` Duration: not captured in original task report. High-count focus was `ok` in 2.144s; race focus was `ok` in 3.050s.
- **Source:** `task-1-report.md`, “TDD evidence / RED: RANDOMCHOICE”, “GREEN: RANDOMCHOICE”, and “Final verification”.

### Task 2 — runtime conditions and joint random choice

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestApplyGiftEvent.*Identity|TestApplyGiftEventReevaluatesCondition|TestApplyGiftEventSkipsInvalidCondition|TestApplyGiftEventCombinesIdentityConditionAndRandomChoice' -count=1`
- **RED output (verbatim excerpt):** `unknown field Condition in struct literal of type giftRule` (retrospective fixed-base reproduction: `background_runtime_semantics_test.go:40:3`, plus eight same-symbol failures; exit 1).
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestApplyGiftEvent' -count=10`
- **GREEN output (verbatim excerpt):** `GREEN runtime: the same focused tests and TestApplyGiftEvent -count=10 passed.` Duration/count detail: not captured in original task report.
- **Source:** `task-2-report.md`, “RED runtime” / “GREEN runtime”; RED stdout reproduced at detached parent `0f0dfce`.

### Task 2 — config validation

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestConfigStore.*ReservedFormulaName|TestConfigStore.*GiftRuleCondition|TestConfigStoreRejectsGiftIdentityInTimer' -count=1`
- **RED output (verbatim excerpt):** Original RED output was not captured. Retrospective fixed-base reproduction at `0f0dfce` with final `config_store_test.go`: `FAIL bilibili-live-gift-panel [build failed]`; `config_store_test.go:928:45: state.Rules[0].Condition undefined (type giftRule has no field or method Condition)`; exit 1.
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestConfigStore.*ReservedFormulaName|TestConfigStore.*GiftRuleCondition|TestConfigStoreRejectsGiftIdentityInTimer' -count=1`
- **GREEN output (verbatim excerpt):** `GREEN config: TestConfigStore.*ReservedFormulaName|TestConfigStore.*GiftRuleCondition|TestConfigStoreRejectsGiftIdentityInTimer passed.` Duration: not captured in original task report.
- **Source:** `task-2-report.md`, “RED config” / “GREEN config”; command spelling from `task-2-brief.md`, Step 6/7.

### Task 2 — gift preview contract

- **RED command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaPreview.*Identity|TestFormulaPreview.*Condition|TestFormulaPreviewUsesSelectedGiftPrice' -count=1`
- **RED output (verbatim excerpt):** Original RED output was not captured. Retrospective fixed-base reproduction at `0f0dfce`: `--- FAIL: TestFormulaPreviewUsesGiftRuleIdentity`; `preview status = 200, body = {"code":0,"result":10}`; false condition returned `{..."result":14}`; invalid identities and invalid condition also returned HTTP 200; exit 1.
- **GREEN command (verbatim):** `cd goserver; go test ./... -run 'TestFormulaPreview.*Identity|TestFormulaPreview.*Condition|TestFormulaPreviewUsesSelectedGiftPrice' -count=1`
- **GREEN output (verbatim excerpt):** `GREEN preview: TestFormulaPreview.*Identity|TestFormulaPreview.*Condition|TestFormulaPreviewUsesSelectedGiftPrice passed.` Duration: not captured in original task report.
- **Source:** `task-2-report.md`, “RED preview” / “GREEN preview”; command spelling from `task-2-brief.md`, Step 9/10.

### Task 3 — helper, adapter, and simple-play focus

- **RED command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts`
- **RED output (verbatim excerpt):** Retrospective fixed-base reproduction at `ef6df17`: `FAIL tests/gift-rule-conditions.test.ts`; `Failed to load url ../src/gift-rule-conditions ... Does the file exist?`; `0 test`; exit 1.
- **GREEN command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts tests/backend.test.ts tests/formula-presets.test.ts tests/simple-play.test.ts`
- **GREEN output (verbatim excerpt):** `GREEN: focused helpers, backend adapter, formula presets, and simple-play tests all pass (55 tests).` Duration: not captured in original task report.
- **Source:** `task-3-report.md`, initial “RED” / “GREEN”; focused command from `task-3-brief.md`, Step 9.

### Task 3 — preview adapter

- **RED command (verbatim):** `npm test -- --run tests/backend.test.ts`
- **RED output (verbatim excerpt):** Retrospective fixed-base reproduction at `ef6df17`: `7 failed | 27 passed (34)`; test `posts the gift condition and identity context and returns the trigger result`; `TypeError: previewGiftRule is not a function`; exit 1.
- **GREEN command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts tests/backend.test.ts tests/formula-presets.test.ts tests/simple-play.test.ts`
- **GREEN output (verbatim excerpt):** `55 tests` passed; duration: not captured in original task report.
- **Source:** `task-3-report.md`, initial “RED” / “GREEN”; command from `task-3-brief.md`, Step 6/9.

### Task 3 — empty-condition review fix

- **RED command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts`
- **RED output (verbatim excerpt):** `failed: empty condition returned advanced instead of any.` Original detailed assertion output was not captured.
- **GREEN command (verbatim):** `npm test -- --run tests/gift-rule-conditions.test.ts`
- **GREEN output (verbatim excerpt):** `the same focused command passed (8 tests)`. Duration: not captured in original task report.
- **Source:** `task-3-report.md`, “Review Fix Round 1”.

### Task 4 — save/reopen and advanced preservation

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'gift identity condition|advanced gift condition'`
- **RED output (verbatim excerpt):** Retrospective fixed-base reproduction at `ba41cd6`: `2 failed | 151 skipped (153)`; `gift identity condition saves, reopens, and updates through beginner controls` and `advanced gift condition stays exact until beginner mode explicitly replaces it`; both `Cannot read properties of null (reading 'value')`; exit 1.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'gift identity condition|advanced gift condition'`
- **GREEN output (verbatim excerpt):** `same command → 2 passed.` Duration: not captured in original task report.
- **Source:** `task-4-report.md`, “TDD C1”; RED stdout reproduced at parent `ba41cd6`.

### Task 4 — simulation identity and stale preview

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'simulated gift identity|identity condition preview'`
- **RED output (verbatim excerpt):** Retrospective fixed-base reproduction at `ba41cd6`: `2 failed | 151 skipped (153)`; both named simulation/stale tests: `Cannot set properties of null (setting 'value')`; exit 1.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'simulated gift identity|identity condition preview'`
- **GREEN output (verbatim excerpt):** `same command → 2 passed.` Duration: not captured in original task report.
- **Source:** `task-4-report.md`, “TDD C2”; RED stdout reproduced at parent `ba41cd6`.

### Task 4 — formula help and reserved names

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'formula help explains|rejects reserved gift formula names'`
- **RED output (verbatim excerpt):** Retrospective fixed-base reproduction at `ba41cd6`: `2 failed | 151 skipped (153)`; `expected ... to contain '用户身份'`; and `expected ... to contain '系统公式名称不能作为属性名：用户身份'`; exit 1.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'formula help explains|rejects reserved gift formula names'`
- **GREEN output (verbatim excerpt):** `same command → 2 passed.` Duration: not captured in original task report.
- **Source:** `task-4-report.md`, “TDD C3”; RED stdout reproduced at parent `ba41cd6`.

### Task 4 review fix 1 — false snapshot, optional shape, stale guard

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'skipped gift simulation|identity condition preview|unrelated gift rule'`
- **RED output (verbatim excerpt):** Retrospective fixed-base reproduction at `b29466e`: `2 failed | 1 passed | 152 skipped (155)`; `expected '预览收到 1 个 大航海·舰长：0 → 11' to contain '本次不会触发'`; unrelated rule received `"condition": ""`; stale test passed.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'skipped gift simulation|identity condition preview|unrelated gift rule'`
- **GREEN output (verbatim excerpt):** `same command → 3 passed (false snapshot retained without draft advancement; unrelated optional condition remains absent; post-stale request uses attributeValue:11).` Duration: not captured in original task report.
- **Source:** `task-4-report.md`, “Review fix round 1”; RED stdout reproduced at parent `b29466e`.

### Task 4 review fix 2 — cross-attribute formula and condition rename

- **RED command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'unrelated gift rule'`
- **RED output (verbatim excerpt):** Retrospective fixed-base reproduction at `e7ee09a`: `1 failed | 154 skipped (155)`; expected `formula: '积分+倒计时', condition: '用户身份>=舰长*(倒计时>0)'`, received `formula: '积分+加班时间', condition: '用户身份>=舰长*(加班时间>0)'`; exit 1.
- **GREEN command (verbatim):** `npm test -- --run tests/wizard.test.ts -t 'unrelated gift rule'`
- **GREEN output (verbatim excerpt):** `same command → 1 passed; all gift rule formulas are rewritten for cross-attribute references, existing own condition fields are likewise rewritten, and missing condition fields remain absent.` Duration: not captured in original task report.
- **Source:** `task-4-report.md`, “Fix round 2”; RED stdout reproduced at parent `e7ee09a`.

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
