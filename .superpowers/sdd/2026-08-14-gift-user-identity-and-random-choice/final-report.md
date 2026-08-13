# Gift identity conditions and `RANDOMCHOICE` — final verification

## Fixed range and task commits

- Verification start and verified feature end: `5a0711d873c3ef10569a2a60b5f2af4c0e9d129c` (`fix: rename cross-attribute gift references`); the working tree was clean before this report.
- Task 1: `0f0dfce79cafb3f8469b4de71bf028a548d454c3` (`feat: add gift identity formula context`).
- Task 2: `ef6df174391addc16b726aec80fa509d87f79403` (`feat: condition gift rules by user identity`).
- Task 3: `9ffd0e5` (`feat: add gift identity condition client`) and review fix `ba41cd6` (`fix: recognize empty gift conditions`).
- Task 4: `b29466e` (`feat: edit gift identity conditions`), review fixes `e7ee09a` (`fix: preserve gift identity previews`) and `5a0711d` (`fix: rename cross-attribute gift references`).
- Task 5 made no implementation correction; this report is the only intended change. Controller-owned fixed-base review remains outside this task, per dispatch instruction.

## Prior RED → GREEN evidence

- Task 1 identity RED: `go test ./... -run 'TestGiftIdentityLevel|TestBuildGiftFormulaEnvironment|TestReservedFormulaNames' -count=1` exited 1 because `giftIdentityLevel`, both environment builders, `giftIdentityCaptain`, and `isReservedFormulaName` were undefined; GREEN exited 0. `TestFormulaRandomChoice` RED exited 1 with `未知函数 "RANDOMCHOICE"`; GREEN exited 0.
- Task 2 runtime RED: `go test ./... -run 'TestApplyGiftEvent.*Identity|...' -count=1` failed with `unknown field Condition in struct literal of type giftRule`; config RED accepted reserved names/preset source/invalid conditions; preview RED returned old `{code,result}` and ignored input. The corresponding focused runtime, config, and preview GREEN gates passed.
- Task 3 RED: `npm test -- --run tests/gift-rule-conditions.test.ts` reported a missing module and `tests/backend.test.ts` reported `previewGiftRule is not a function`; GREEN focused suite passed 55 tests. Review RED: empty condition was detected as `advanced`, not `any`; GREEN passed 8 helper tests.
- Task 4 REDs: missing condition controls (2 failures), old simulation preview/no identity payload (2), and absent identity help (1); all paired focused GREEN gates passed. Review REDs: false snapshot lost/unrelated rule gained `condition:''` (2), then cross-attribute formula/condition references survived rename (1); paired GREEN gates passed (3, then 1).
- No Task 5 RED occurred: fresh compatibility/gate evidence did not show an owned regression, so no code change was authorized or made.

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

The gates above were run strictly in the listed order; no tests/builds were run concurrently. The package output has the existing dynamic module graph, and the EXE build’s 80 embedded UI assets plus clean tracked-file status show no source/test artifacts leaked into `goserver/dist` or the package.

## Contract evidence

- Identity names are reserved and mapped as `普通用户=0`, `粉丝团=1`, `舰长=2`, `提督=3`, `总督=4`; user identity is `用户身份`. The context is built afresh per gift event, identity constants override attributes/target, and non-gift timer context rejects identity use.
- `TestApplyGiftEventSkipsInvalidConditionAndContinues` asserts invalid, false, and non-finite conditions leave the skipped attribute at 0, create zero triggers, and create no logs; the valid peer rule still reaches 1 and is the sole log.
- Existing Task-2 integration test `TestApplyGiftEventCombinesIdentityConditionAndRandomChoice` is sufficient: a fan is skipped before the draw; captain selects branch 2 of 2, value becomes 10, exactly one draw/trigger/log/effect/contribution is recorded.
- `RANDOMCHOICE` rejects zero arguments, avoids a random draw for one argument, calls `formulaRandomIntn` for variadic calls, evaluates exactly the chosen branch, and recovery uses the persisted selected result without re-evaluation (Task-1 focused/race evidence).
- Current full Vitest includes wizard assertions for save/reopen/beginner update, exact advanced-condition preservation until explicit replacement, simulated identity request plus skipped-preview draft protection, rerender retention, stale response suppression, and optional-condition preservation during rename.

## Scope, status, and prohibited-action audit

- Feature range `a320590..5a0711d` contains only the Task1–4 Go/frontend files and tests; no unrelated file was added. Pre-report `git status --short` was empty; build artifacts remain ignored.
- No push, tag, release, publish, branch switch, `progress.md` edit, plan-workspace edit, or ignored `dist` artifact was staged. No implementation file or design document was modified in Task 5.
- Concern (non-load-bearing): the brief’s literal “no `用户身份==` exists” expectation conflicts with the deliberate negative test that proves this syntax is rejected. It is confined to that test and does not represent accepted product syntax.
