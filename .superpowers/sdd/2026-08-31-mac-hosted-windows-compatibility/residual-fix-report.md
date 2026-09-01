# Residual CI scope summary fix

## Scope

- Files changed: `scripts/ci-scope.mjs`, `tests/ci-scope.test.ts`.
- Git paths in the summary table are now rendered as inert text using direct output only for ASCII letters, digits, spaces, dots, underscores, hyphens, and slashes; every other code point is emitted as a decimal HTML numeric entity.
- Git paths remain data passed to the file API and are not entered into a shell.

## RED

Added a regression using `![probe](https://example.test/pixel) @owner #123 | \u0001 中文`.

Command: `npm test -- tests/ci-scope.test.ts`

Result: failed as expected (1 failed, 34 passed). The received summary still contained active image/link syntax, mention syntax, and issue syntax, proving the prior path escaping was insufficient.

## GREEN

Command: `npm test -- tests/ci-scope.test.ts`

Result: passed (35 tests).

The path-only encoder preserves the existing escaping semantics for the other summary cells while making every unsafe path code point inert.

## Self-review

- Confirmed the allowlist is ASCII-only and includes exactly letters, digits, spaces, `.`, `_`, `-`, and `/`.
- Confirmed Unicode iteration uses code points, so non-BMP characters would receive one decimal entity per code point.
- Confirmed the existing JSON output path is unchanged.
- Confirmed no shell interpolation was introduced.

## Verification / concerns

The full requested verification is run after this report is written: the CI scope and workflow tests, typecheck, and `git diff --check`.

Concern: GitHub's renderer may display control-code numeric references according to its HTML sanitization behavior; this is intentional and follows the required `&#N;` encoding contract.
