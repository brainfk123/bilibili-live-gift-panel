# Gift identity conditions and `RANDOMCHOICE` — final verification

## Fixed range and task commits

- Complete feature start: `a320590cec6bbdaff2a0e7a2e521f9a6279f2058`; Task-5 verification base / verified implementation end: `5a0711d873c3ef10569a2a60b5f2af4c0e9d129c` (`fix: rename cross-attribute gift references`).
- Task 1: `0f0dfce79cafb3f8469b4de71bf028a548d454c3` (`feat: add gift identity formula context`). Task 2: `ef6df174391addc16b726aec80fa509d87f79403` (`feat: condition gift rules by user identity`).
- Task 3: `9ffd0e5` (`feat: add gift identity condition client`) and `ba41cd6` (`fix: recognize empty gift conditions`). Task 4: `b29466e` (`feat: edit gift identity conditions`), `e7ee09a` (`fix: preserve gift identity previews`), `5a0711d` (`fix: rename cross-attribute gift references`).
- Evidence-report parent commit: `3ffc323743c200d11b96f077d9f12a5c89a3f2f8` (`test: verify gift identity rule conditions`). This correction’s commit cannot truthfully self-identify inside its own content; Git history identifies the successor with parent `3ffc323`.
- Task 5 changes evidence only. Controller-owned fixed-base review is outside this task. The no-push/tag/release/publish statement below is an operator/controller action audit, not a claim that repository files prove remote history.

## Prior RED → GREEN evidence

Every item below is transcribed from Task 1–4 reports and their briefs. “Duration not recorded” means the original task report did not record one; it is not reconstructed here.

- **Task 1 identity:** RED `cd goserver; go test ./... -run 'TestGiftIdentityLevel|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames' -count=1` exited 1: `giftIdentityLevel`, both environment builders, `giftIdentityCaptain`, and `isReservedFormulaName` did not exist. GREEN repeated that exact command, exit 0, `ok bilibili-live-gift-panel`; focused-command duration not recorded. Recorded focused verification: `go test ./... -run 'TestGiftIdentity|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames|TestFormulaRandomChoice|TestPendingStateTransactionRecoversGiftFormulaRandomResultWithoutReevaluation' -count=20` passed in 2.144s.
- **Task 1 `RANDOMCHOICE`:** RED `cd goserver; go test ./... -run 'TestFormulaRandomChoice' -count=1` exited 1, `未知函数 "RANDOMCHOICE"`. GREEN repeated that exact command, exit 0, `ok bilibili-live-gift-panel`; duration not recorded. Recorded race focus `go test -race ./... -run 'TestFormulaRandomChoice|TestPendingStateTransactionRecoversGiftFormulaRandomResultWithoutReevaluation' -count=5` passed in 3.050s.
- **Task 2 runtime:** RED `cd goserver; go test ./... -run 'TestApplyGiftEvent.*Identity|TestApplyGiftEventReevaluatesCondition|TestApplyGiftEventSkipsInvalidCondition|TestApplyGiftEventCombinesIdentityConditionAndRandomChoice' -count=1` exited 1, `unknown field Condition in struct literal of type giftRule`. GREEN repeated that command and `go test ./... -run 'TestApplyGiftEvent' -count=10`; both passed; focused counts/durations were not recorded.
- **Task 2 config:** RED `cd goserver; go test ./... -run 'TestConfigStore.*ReservedFormulaName|TestConfigStore.*GiftRuleCondition|TestConfigStoreRejectsGiftIdentityInTimer' -count=1` failed because reserved names, preset source, and invalid condition were accepted. GREEN repeated that exact command and passed; count/duration not recorded.
- **Task 2 preview:** RED `cd goserver; go test ./... -run 'TestFormulaPreview.*Identity|TestFormulaPreview.*Condition|TestFormulaPreviewUsesSelectedGiftPrice' -count=1` failed because the endpoint returned old `{code,result}` and ignored input. GREEN repeated that exact command and passed; count/duration not recorded. Final recorded Task-2 focus `go test ./... -run 'TestApplyGiftEvent|TestConfigStore.*Formula|TestConfigStore.*Identity|TestFormulaPreview' -count=20`, race `go test -race ./... -run 'TestApplyGiftEvent.*Identity|TestFormulaPreview.*Identity' -count=5`, and full Go gate passed; their focused counts/durations were not recorded.
- **Task 3 helper/adapter:** RED `npm test -- --run tests/gift-rule-conditions.test.ts` failed with missing module; RED `npm test -- --run tests/backend.test.ts` failed with `previewGiftRule is not a function`. The brief’s recorded focused GREEN gate is `npm test -- --run tests/gift-rule-conditions.test.ts tests/backend.test.ts tests/formula-presets.test.ts tests/simple-play.test.ts`; Task-3 report records 55 passing tests, exit 0, duration not recorded.
- **Task 3 review fix:** RED `npm test -- --run tests/gift-rule-conditions.test.ts` failed because empty condition returned `advanced` instead of `any`. GREEN repeated it: 8 passed, exit 0, duration not recorded; the same recorded four-file Task-3 focus remained 55 passed (duration not recorded).
- **Task 4 C1:** RED `npm test -- --run tests/wizard.test.ts -t 'gift identity condition|advanced gift condition'` had 2 failures because condition controls were absent. GREEN repeated it: 2 passed, exit 0, duration not recorded.
- **Task 4 C2:** RED `npm test -- --run tests/wizard.test.ts -t 'simulated gift identity|identity condition preview'` had 2 failures: old preview advanced the draft and sent no identity. GREEN repeated it: 2 passed, exit 0, duration not recorded.
- **Task 4 C3:** RED `npm test -- --run tests/wizard.test.ts -t 'formula help explains|rejects reserved gift formula names'` failed because identity help was absent. GREEN repeated it: 2 passed, exit 0, duration not recorded. Recorded initial focused gate `npm test -- --run tests/wizard.test.ts tests/gift-rule-conditions.test.ts tests/backend.test.ts` passed 195 with 31 skipped; duration not recorded.
- **Task 4 review fix 1:** RED `npm test -- --run tests/wizard.test.ts -t 'skipped gift simulation|identity condition preview|unrelated gift rule'` had 2 failures (false snapshot lost; unrelated rule acquired `condition:''`). GREEN repeated it: 3 passed, exit 0, duration not recorded. The recorded three-file focus passed 166 with 31 skipped; duration not recorded.
- **Task 4 review fix 2:** RED `npm test -- --run tests/wizard.test.ts -t 'unrelated gift rule'` had 1 failure: renamed attribute left `积分+加班时间` and `加班时间` condition references. GREEN repeated it: 1 passed, exit 0, duration not recorded. Recorded review focus `npm test -- --run tests/wizard.test.ts -t 'skipped gift simulation|identity condition preview|unrelated gift rule'` passed 3; duration not recorded.
- **Task 5:** no RED occurred. The fresh compatibility/gate evidence did not identify an owned Task1–4 regression, so no implementation change was authorized.

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
