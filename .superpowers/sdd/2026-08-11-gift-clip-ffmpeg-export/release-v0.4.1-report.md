# v0.4.1 Release Metadata Report

Prepared from `ee6c1de4837e990b05950abf774cd4d351793348` on branch `codex/fix-gift-clip-stutter`.

## Scope

- Bumped the package version from `0.4.0` to `0.4.1` with `npm version 0.4.1 --no-git-tag-version --ignore-scripts`.
- Confirmed `package-lock.json` changed only its two root `version` fields; all dependency entries remain unchanged.
- Replaced the single bundled changelog entry with `0.4.1` dated `2026-08-13`. It has four Chinese reliability highlights, no visuals, and does not repeat the v0.4.0 clip-cropping feature.
- Updated changelog and wizard assertions for the new latest version and its persisted seen-version behavior.
- Checked the release workflow contract without changing it: it requires `v$packageVersion` to equal the release tag and merges older changelog history at release time. `.github/changelog-history.json` remains unchanged.

## TDD evidence

- RED: `npm test -- tests/changelog.test.ts tests/wizard.test.ts` failed against the old `0.4.0` metadata: the expected v0.4.1 release was absent, the old clip-cropping text remained, and the installed-v0.4.1 dialog did not open.
- GREEN: the same focused command passed with 119 tests passed and 31 skipped.

## Verification

- `npm run typecheck` — passed.
- `npm test` — 43 test files passed; 498 tests passed and 31 skipped.
- `npm ls --depth=0` — passed, reporting `bilibili-live-gift-panel@0.4.1` and the expected top-level dependencies.
- `git diff --check` and `git diff --cached --check` — passed; the staged scope contained only the six files listed in this report.

## Deliberate exclusions

- Did not run `npm ci --ignore-scripts`: the existing dependency tree is healthy and `npm ls --depth=0` passed, avoiding unnecessary working-tree dependency churn.
- Did not run Go, race, build, FFmpeg, signing, tagging, publishing, or release workflows.
