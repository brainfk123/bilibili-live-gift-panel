# Formula Assignment Final Fix Report

## Scope

This pass addresses every issue listed in the final formula-assignment review package without changing `GiftRule`, localStorage keys, APIs, or protocol structures.

## Review issue resolution

1. Display toasts now format delta signs explicitly. Positive deltas use `+`, negative deltas use `-`, and zero deltas have no sign. Time deltas are formatted from their absolute value, so a negative forty-second assignment is shown as `-00:00:40` instead of being clamped to zero.
2. Formula examples are generated from the currently selected attribute. Examples are rebuilt after target selection changes, and clicking an example generates its formula from the current `selectedIndex`.
3. Formula preview applies the same `min(result, cap)` rule as runtime assignment and refreshes when the cap input changes.
4. Added direct-assignment examples for `RANDBETWEEN(10,60)` and conditional increment/doubling.
5. Updated the README opening sentence to describe writing formula results to attributes rather than increasing values.
6. Updated README migration guidance to say existing rules should be checked individually and rebuilt when necessary.

## Regression coverage

- Positive, negative, and zero display delta formatting.
- Example regeneration and click-to-fill after changing the target attribute.
- Cap application and preview refresh for cap changes.
- Existing engine assignment, cap, conditional, logging, and limit coverage remains passing.

## Verification

- `npx vitest run tests/wizard.test.ts`: PASS — 23 tests.
- `npm test`: PASS — 8 test files, 101 tests.
- `npm run typecheck`: PASS.
- `npm run build`: PASS — Vite frontend and `dist/gift-panel.exe` built.
- `git diff --check`: PASS.

## Constraints checked

- No changes were made to `GiftRule` fields, localStorage keys, API contracts, Bilibili protocol handling, or OBS URL modes.
- Pre-existing unrelated working-tree changes were left untouched.
