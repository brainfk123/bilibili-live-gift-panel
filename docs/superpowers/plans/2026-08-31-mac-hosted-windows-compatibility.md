# Mac Hosted Development and Windows Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Mac and Linux the fast, required Hosted development path while running Windows x64 validation only for changes that affect the retiring EXE, shared contracts, or its release chain.

**Architecture:** A tested change-scope classifier feeds one always-present CI workflow. Hosted validation runs for every pull request; MySQL and Windows jobs run only when scope outputs require them; a final gate evaluates every result without exposing release secrets. Separate versioned contracts define EXE/Hosted UI evidence and retirement-stage evidence, while existing migration and Hosted feature plans retain ownership of product parity.

**Tech Stack:** Node.js 22, TypeScript 5.5, Vitest 2.1, Playwright 1.62.1, Go 1.26, GitHub Actions, PowerShell on Windows x64, JSON/Markdown acceptance contracts.

**Spec:** `docs/superpowers/specs/2026-08-31-mac-hosted-windows-compatibility-design.md`

## Global Constraints

- Hosted is the required mainline; Windows is a conditional compatibility line.
- Hosted-only changes must not start a Windows runner.
- Unknown or ambiguous paths fail closed to the highest Windows level.
- Mac cross-compilation may be called a compile check, never Windows acceptance.
- Pull-request jobs receive no EVSign, COS, Hosted production, Bilibili, OBS, or user credentials.
- The existing protected `release` Environment and `.github/workflows/release.yml` remain the only EXE signing path.
- Windows ARM is reference and interaction evidence only; it is not x64 driver, GPU, OBS plugin, or hardware-encoding evidence.
- Every migrated EXE feature preserves complete content, structure, component hierarchy, spacing, controls, states, responsive behavior, and interactions.
- Every migrated state and viewport receives direct EXE-versus-Hosted screenshot comparison in addition to automated tests.
- Product migration version alignment remains owned by `docs/superpowers/plans/2026-08-25-gameplay-unit-migration.md`; this plan must not hide the current exporter/importer mismatch.
- Hosted feature completion remains owned by the existing user-workspace, gift-video-export, room-monitoring, analytics, and migration plans.
- Preserve the dirty worktree. Stage and commit only files named by the active task.
- Temporary comparisons, logs, screenshots, and diagnostics remain local unless a task explicitly promotes them to formal acceptance assets.

## File and Responsibility Map

| File | Responsibility |
| --- | --- |
| `scripts/ci-scope.mjs` / `.d.mts` | Parse Git diffs and classify Windows/MySQL requirements with fail-closed rules |
| `tests/ci-scope.test.ts` | Prove precedence, rename/delete handling, and unknown-path behavior |
| `scripts/ci-gate.mjs` / `.d.mts` | Convert job results and expected optional jobs into one deterministic decision |
| `tests/ci-gate.test.ts` | Prove allowed skips and required-job failures |
| `.github/workflows/ci.yml` | Run scope, Hosted, optional MySQL, optional Windows x64, and final gate jobs |
| `tests/ci-workflow.test.ts` | Enforce triggers, permissions, pins, commands, conditions, and secret isolation |
| `scripts/smoke-windows-exe.mjs` / `.d.mts` | Launch packaged EXE, probe embedded routes, request graceful exit, and write safe evidence |
| `tests/smoke-windows-exe.test.ts` | Test polling, routes, exit, timeout, cleanup, and evidence redaction |
| `docs/development/mac-hosted-workflow.md` | Mac workflow, CI failures, ARM VM use, and temporary x64 acceptance |
| `acceptance/exe-hosted-ui/requirements.json` | Versioned feature/state/viewport requirements without screenshots or sensitive data |
| `scripts/validate-ui-parity-requirements.mjs` | Strictly validate UI parity requirements |
| `docs/operations/exe-ui-baseline-capture.md` | Deterministic EXE capture and direct Hosted comparison |
| `docs/operations/exe-retirement-checklist.md` | Evidence-gated A/B/C/D support transitions |
| `tests/exe-retirement-contract.test.ts` | Prevent prechecked gates and CI-only production claims |

---

### Task 1: Build the fail-closed change-scope classifier

**Files:**
- Create: `scripts/ci-scope.mjs`
- Create: `scripts/ci-scope.d.mts`
- Create: `tests/ci-scope.test.ts`
- Modify: `package.json`

**Interfaces:**
- Produces: `WINDOWS_LEVELS = ['skip', 'shared', 'desktop', 'desktop-high-risk']`.
- Produces: `classifyChanges(changes: Change[]): ScopeDecision`.
- Produces: `readGitChanges(baseSHA: string, headSHA: string, cwd?: string): Change[]`.
- CLI outputs: `windows_level`, `run_windows`, `run_mysql`, and `reasons_json` through `GITHUB_OUTPUT`.

- [ ] **Step 1: Write failing precedence and path tests**

```ts
import { describe, expect, it } from 'vitest';
import { classifyChanges, parseNameStatusZ } from '../scripts/ci-scope.mjs';

describe('CI scope classification', () => {
  it.each([
    [['src/hosted/main.ts'], 'skip'],
    [['goserver/internal/hosted/runtime/manager.go'], 'skip'],
    [['goserver/internal/gameplay/engine.go'], 'shared'],
    [['src/ui/config/config.ts'], 'desktop'],
    [['goserver/auth_protection_windows.go'], 'desktop-high-risk'],
    [['scripts/build-go.mjs'], 'desktop-high-risk'],
    [['updateapi/server.go'], 'desktop-high-risk'],
    [['future/unknown.file'], 'desktop-high-risk'],
  ])('classifies %j as %s', (paths, windowsLevel) => {
    expect(classifyChanges(paths.map((path) => ({ status: 'M', path })))).toMatchObject({ windowsLevel });
  });

  it('uses the highest level and enables MySQL only for persistence inputs', () => {
    expect(classifyChanges([
      { status: 'M', path: 'src/hosted/main.ts' },
      { status: 'M', path: 'goserver/internal/gameplay/model.go' },
      { status: 'M', path: 'goserver/internal/hosted/store/mysqlstore/store.go' },
    ])).toMatchObject({ windowsLevel: 'shared', runWindows: true, runMySQL: true });
  });

  it('parses rename and deletion records without dropping either path', () => {
    expect(parseNameStatusZ('R100\\0src/main.ts\\0src/hosted/main.ts\\0D\\0goserver/tray_windows.go\\0'))
      .toEqual([
        { status: 'R100', path: 'src/main.ts', destination: 'src/hosted/main.ts' },
        { status: 'D', path: 'goserver/tray_windows.go' },
      ]);
  });
});
```

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/ci-scope.test.ts`

Expected: FAIL because `scripts/ci-scope.mjs` does not exist.

- [ ] **Step 3: Implement explicit rules and precedence**

```js
export const WINDOWS_LEVELS = Object.freeze(['skip', 'shared', 'desktop', 'desktop-high-risk']);
const rank = new Map(WINDOWS_LEVELS.map((level, index) => [level, index]));

const rules = [
  { level: 'skip', patterns: [/^(?:docs\/|acceptance\/|.*\.md$)/, /^(?:src\/hosted\/|goserver\/(?:cmd\/hosted|internal\/hosted)\/|deploy\/hosted\/)/, /^tests\/hosted-.*\.test\.ts$/, /^tests\/(?:development-workflow|ui-parity-requirements|exe-retirement-contract)\.test\.ts$/, /^scripts\/(?:build-hosted|test-hosted-mysql)\.mjs$/, /^(?:hosted|obs)\.html$/, /^vite\.hosted\.config\.ts$/] },
  { level: 'shared', patterns: [/^goserver\/internal\/gameplay\//, /^goserver\/gameplay_adapter(?:_test)?\.go$/, /^src\/migration(?:-gameplay-units)?\.ts$/, /^tests\/(?:migration|migration-gameplay-units)\.test\.ts$/, /^tests\/fixtures\/online-migration-/] },
  { level: 'desktop', patterns: [/^src\/(?!hosted\/|migration(?:-gameplay-units)?\.ts$)/, /^tests\/(?!hosted-|(?:migration|migration-gameplay-units)\.test\.ts$|fixtures\/online-migration-)/, /^goserver\/(?!gameplay_adapter(?:_test)?\.go$)[^/]+\.go$/, /^(?:index\.html|vite\.config\.ts)$/] },
  { level: 'desktop-high-risk', patterns: [/^\.github\/workflows\/(?:ci|release)\.yml$/, /^updateapi\//, /^deploy\/update-api\//, /^goserver\/(?:ffmpeg\/|.*_windows(?:_test)?\.go$|auto_update|gift_clip|tray_|atomic_replace|auth_protection)/, /^scripts\/(?:build-go|build-ffmpeg|package-ffmpeg|verify-ffmpeg|sign-evsign|ffmpeg-)/, /^(?:package|package-lock|tsconfig)\.json$/] },
];

export function classifyChanges(changes) {
  let windowsLevel = 'skip';
  let runMySQL = false;
  const reasons = [];
  for (const change of changes) {
    for (const path of [change.path, change.destination].filter(Boolean)) {
      const matches = rules.filter((rule) => rule.patterns.some((pattern) => pattern.test(path)));
      const level = matches.reduce(
        (best, item) => rank.get(item.level) > rank.get(best) ? item.level : best,
        matches.length ? 'skip' : 'desktop-high-risk',
      );
      if (rank.get(level) > rank.get(windowsLevel)) windowsLevel = level;
      if (/^goserver\/internal\/hosted\/store\/mysqlstore\/|^deploy\/hosted\/docker-compose\.test\.yml$|^scripts\/test-hosted-mysql\.mjs$/.test(path)) runMySQL = true;
      reasons.push({ path, level, reason: matches.length ? 'matched-explicit-rule' : 'unknown-path-fail-closed' });
    }
  }
  return { windowsLevel, runWindows: windowsLevel !== 'skip', runMySQL, reasons };
}
```

Implement `readGitChanges` with:

```js
execFileSync('git', ['diff', '--name-status', '-z', '--find-renames', baseSHA + '...' + headSHA], {
  cwd, encoding: 'utf8',
});
```

Reject missing/non-hex SHAs. An empty or unparseable diff returns one synthetic unknown path so classification becomes `desktop-high-risk` rather than silently skipping. Write JSON-escaped outputs directly to the exact `GITHUB_OUTPUT` file; never build a shell command from a changed path.

- [ ] **Step 4: Add declarations and scripts**

```json
\"test:ci-scope\": \"vitest run tests/ci-scope.test.ts\",
\"ci:scope\": \"node scripts/ci-scope.mjs\"
```

Declare `Change`, `WindowsLevel`, `ScopeReason`, `ScopeDecision`, `parseNameStatusZ`, `readGitChanges`, and `classifyChanges` in `scripts/ci-scope.d.mts`.

- [ ] **Step 5: Run GREEN**

Run: `npm test -- tests/ci-scope.test.ts`

Run: `npm run typecheck`

Expected: PASS, including rename, deletion, mixed-level, MySQL, docs-only, and unknown-path cases.

- [ ] **Step 6: Commit**

```powershell
git add -- scripts/ci-scope.mjs scripts/ci-scope.d.mts tests/ci-scope.test.ts package.json
git commit -m \"ci: classify Hosted and Windows changes\"
```

### Task 2: Build the deterministic final gate evaluator

**Files:**
- Create: `scripts/ci-gate.mjs`
- Create: `scripts/ci-gate.d.mts`
- Create: `tests/ci-gate.test.ts`
- Modify: `package.json`

**Interfaces:**
- Consumes: `{ scope, hosted, mysql, windows, expectMySQL, expectWindows }`.
- Produces: `evaluateGate(input): { ok: boolean; failures: string[] }`.
- CLI exits 0 only when required jobs succeeded and optional jobs were successful or correctly skipped.

- [ ] **Step 1: Write the failing truth-table test**

```ts
import { describe, expect, it } from 'vitest';
import { evaluateGate } from '../scripts/ci-gate.mjs';

describe('CI final gate', () => {
  it.each([
    [{ scope: 'success', hosted: 'success', mysql: 'skipped', windows: 'skipped', expectMySQL: false, expectWindows: false }, true],
    [{ scope: 'success', hosted: 'success', mysql: 'success', windows: 'success', expectMySQL: true, expectWindows: true }, true],
    [{ scope: 'success', hosted: 'failure', mysql: 'skipped', windows: 'skipped', expectMySQL: false, expectWindows: false }, false],
    [{ scope: 'success', hosted: 'success', mysql: 'skipped', windows: 'success', expectMySQL: true, expectWindows: true }, false],
    [{ scope: 'success', hosted: 'success', mysql: 'success', windows: 'cancelled', expectMySQL: true, expectWindows: true }, false],
    [{ scope: 'failure', hosted: 'skipped', mysql: 'skipped', windows: 'skipped', expectMySQL: false, expectWindows: false }, false],
  ])('evaluates %j', (input, ok) => expect(evaluateGate(input).ok).toBe(ok));
});
```

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/ci-gate.test.ts`

Expected: FAIL because the evaluator is absent.

- [ ] **Step 3: Implement exact allowed results**

```js
const allowedResults = new Set(['success', 'failure', 'cancelled', 'skipped']);

export function evaluateGate(input) {
  const failures = [];
  for (const name of ['scope', 'hosted', 'mysql', 'windows']) {
    if (!allowedResults.has(input[name])) failures.push(name + ':invalid-result');
  }
  if (input.scope !== 'success') failures.push('scope:' + input.scope);
  if (input.hosted !== 'success') failures.push('hosted:' + input.hosted);
  if (input.expectMySQL ? input.mysql !== 'success' : !['success', 'skipped'].includes(input.mysql)) failures.push('mysql:' + input.mysql);
  if (input.expectWindows ? input.windows !== 'success' : !['success', 'skipped'].includes(input.windows)) failures.push('windows:' + input.windows);
  return { ok: failures.length === 0, failures: [...new Set(failures)] };
}
```

The CLI reads `CI_SCOPE_RESULT`, `CI_HOSTED_RESULT`, `CI_MYSQL_RESULT`, `CI_WINDOWS_RESULT`, `CI_EXPECT_MYSQL`, and `CI_EXPECT_WINDOWS`. Only lowercase `true` enables an expectation. Print one JSON result without environment contents and set `process.exitCode = 1` when `ok` is false.

- [ ] **Step 4: Add declarations, scripts, verify, and commit**

```json
\"test:ci-gate\": \"vitest run tests/ci-gate.test.ts\",
\"ci:gate\": \"node scripts/ci-gate.mjs\"
```

Run: `npm test -- tests/ci-gate.test.ts`

Run: `npm run typecheck`

Expected: PASS.

```powershell
git add -- scripts/ci-gate.mjs scripts/ci-gate.d.mts tests/ci-gate.test.ts package.json
git commit -m \"ci: add deterministic final gate\"
```

### Task 3: Add the always-required Hosted CI path

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `tests/ci-workflow.test.ts`

**Interfaces:**
- Consumes: Task 1 scope outputs.
- Produces jobs: `scope`, `hosted`, `hosted-mysql`, `windows-compat`, and `ci-gate`.
- This task uses an independently passing transitional Windows compile/test job on every PR; Task 5 replaces it with the final scope-conditioned packaged-EXE job.

- [ ] **Step 1: Write the failing workflow contract**

```ts
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { parseDocument } from 'yaml';

function ciWorkflow() {
  const document = parseDocument(readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8'));
  expect(document.errors).toEqual([]);
  return document.toJS() as { permissions?: Record<string, string>; jobs?: Record<string, any> };
}

describe('mainline CI workflow', () => {
  it('uses read-only permissions and immutable Actions', () => {
    const workflow = ciWorkflow();
    expect(workflow.permissions).toEqual({ contents: 'read' });
    const uses = Object.values(workflow.jobs ?? {}).flatMap((job: any) => job.steps ?? []).flatMap((step: any) => step.uses ? [step.uses] : []);
    expect(uses.every((value: string) => /^[^@\\s]+@[0-9a-f]{40}$/.test(value))).toBe(true);
  });

  it('always runs Hosted and gates optional jobs from scope output', () => {
    const jobs = ciWorkflow().jobs ?? {};
    expect(Object.keys(jobs)).toEqual(expect.arrayContaining(['scope', 'hosted', 'hosted-mysql', 'windows-compat', 'ci-gate']));
    expect(jobs.hosted.needs).toContain('scope');
    expect(jobs['hosted-mysql'].if).toContain(\"needs.scope.outputs.run_mysql == 'true'\");
    expect(jobs['ci-gate'].if).toBe('always()');
  });
});
```

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/ci-workflow.test.ts`

Expected: FAIL because the workflow is absent.

- [ ] **Step 3: Create scope, Hosted, and MySQL jobs**

Use exactly these immutable Actions:

```yaml
permissions:
  contents: read

jobs:
  scope:
    runs-on: ubuntu-24.04
    outputs:
      windows_level: ${{ steps.scope.outputs.windows_level }}
      run_windows: ${{ steps.scope.outputs.run_windows }}
      run_mysql: ${{ steps.scope.outputs.run_mysql }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with: { fetch-depth: 0, persist-credentials: false }
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020
        with: { node-version: 22, cache: npm }
      - run: npm ci
      - id: scope
        env:
          CI_BASE_SHA: ${{ github.event.pull_request.base.sha || github.event.before }}
          CI_HEAD_SHA: ${{ github.event.pull_request.head.sha || github.sha }}
        run: npm run ci:scope

  hosted:
    needs: [scope]
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with: { persist-credentials: false }
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020
        with: { node-version: 22, cache: npm }
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
        with: { go-version-file: goserver/go.mod, cache-dependency-path: \"goserver/go.sum\\nupdateapi/go.sum\" }
      - run: npm ci
      - run: npm exec -- playwright install --with-deps chromium
      - run: npm test -- --reporter=dot --minWorkers=2 --maxWorkers=2
      - run: npm run typecheck
      - run: npm run build:hosted
      - run: go -C goserver test ./... -race -count=1
      - run: go -C goserver vet ./...
      - run: npm run test:update-api
      - run: go -C goserver build -trimpath -o \"${{ runner.temp }}/gift-panel-hosted\" ./cmd/hosted

  hosted-mysql:
    needs: [scope, hosted]
    if: needs.scope.outputs.run_mysql == 'true'
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with: { persist-credentials: false }
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020
        with: { node-version: 22, cache: npm }
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
        with: { go-version-file: goserver/go.mod, cache-dependency-path: goserver/go.sum }
      - run: npm ci
      - run: npm run test:hosted-mysql

  windows-compat:
    needs: [scope, hosted]
    runs-on: windows-2025
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with: { persist-credentials: false }
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
        with: { go-version-file: goserver/go.mod, cache-dependency-path: goserver/go.sum }
      - run: go -C goserver test ./... -race -count=1
```

Trigger on `pull_request` plus pushes to `master` and `codex/hosted-service`. Add a workflow/ref concurrency group with `cancel-in-progress: true`; it must not alter the production release concurrency group.

- [ ] **Step 4: Add the final gate**

```yaml
  ci-gate:
    if: always()
    needs: [scope, hosted, hosted-mysql, windows-compat]
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with: { persist-credentials: false }
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020
        with: { node-version: 22, cache: npm }
      - run: npm ci
      - env:
          CI_SCOPE_RESULT: ${{ needs.scope.result }}
          CI_HOSTED_RESULT: ${{ needs.hosted.result }}
          CI_MYSQL_RESULT: ${{ needs.hosted-mysql.result }}
          CI_WINDOWS_RESULT: ${{ needs.windows-compat.result }}
          CI_EXPECT_MYSQL: ${{ needs.scope.outputs.run_mysql }}
          CI_EXPECT_WINDOWS: ${{ needs.scope.outputs.run_windows }}
        run: npm run ci:gate
```

- [ ] **Step 5: Verify and commit**

Run: `npm test -- tests/ci-workflow.test.ts tests/ci-scope.test.ts tests/ci-gate.test.ts`

Run: `npm run typecheck`

Run: `npm run build:hosted`

Expected: PASS.

```powershell
git add -- .github/workflows/ci.yml tests/ci-workflow.test.ts
git commit -m \"ci: require Hosted mainline validation\"
```

### Task 4: Add a packaged Windows EXE smoke harness

**Files:**
- Create: `scripts/smoke-windows-exe.mjs`
- Create: `scripts/smoke-windows-exe.d.mts`
- Create: `tests/smoke-windows-exe.test.ts`
- Modify: `package.json`

**Interfaces:**
- Produces: `probePanel(fetchImpl, ports, deadline): Promise<PanelProbe>`.
- Produces: `validatePanelRoutes(fetchImpl, port): Promise<string[]>`.
- Produces: `requestGracefulExit(fetchImpl, port, takeoverVersion): Promise<void>`.
- Produces: `smokeWindowsExecutable(options): Promise<SmokeEvidence>`.
- Writes only `dist/ci-smoke-evidence.json` with schema, version, port, routes, SHA-256, startedAt, and completedAt.

- [ ] **Step 1: Write failing HTTP and redaction tests**

```ts
import { createServer } from 'node:http';
import { expect, it } from 'vitest';
import { probePanel, requestGracefulExit, validatePanelRoutes } from '../scripts/smoke-windows-exe.mjs';

it('probes pages, config API, and graceful takeover exit', async () => {
  let exited = false;
  const server = createServer((request, response) => {
    if (request.url === '/health') return void response.end('{\"name\":\"bilibili-live-gift-panel\",\"version\":\"0.0.0\"}');
    if (request.url === '/api/instance/exit') {
      exited = request.headers['x-bilibili-panel-takeover'] === '0.0.1';
      response.statusCode = 202;
      return void response.end('{\"code\":0}');
    }
    if (request.url === '/api/config') {
      response.setHeader('content-type', 'application/json');
      return void response.end('{\"schemaVersion\":12}');
    }
    response.setHeader('content-type', 'text/html');
    response.end('<!doctype html><title>panel</title>');
  }).listen(0, '127.0.0.1');
  const port = (server.address() as { port: number }).port;
  try {
    expect(await probePanel(fetch, [port], Date.now() + 2_000)).toMatchObject({ port, version: '0.0.0' });
    expect(await validatePanelRoutes(fetch, port)).toEqual(['config', 'display', 'api-config']);
    await requestGracefulExit(fetch, port, '0.0.1');
    expect(exited).toBe(true);
  } finally {
    server.close();
  }
});
```

Add tests for foreign marker, non-200 route, timeout, exit status other than 202, child exit before readiness, and evidence containing none of `cookie`, `token`, `uid`, `nickname`, `LOCALAPPDATA`, or raw response bodies.

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/smoke-windows-exe.test.ts`

Expected: FAIL because the smoke module is absent.

- [ ] **Step 3: Implement strict route probes**

```js
const expectedRoutes = Object.freeze([
  ['config', '/?mode=config', 'text/html'],
  ['display', '/?mode=display', 'text/html'],
  ['api-config', '/api/config', 'application/json'],
]);

export async function requestGracefulExit(fetchImpl, port, takeoverVersion) {
  const response = await fetchImpl('http://127.0.0.1:' + port + '/api/instance/exit', {
    method: 'POST',
    headers: { 'X-Bilibili-Panel-Takeover': takeoverVersion },
  });
  if (response.status !== 202) throw new Error('Windows EXE smoke exit failed with status ' + response.status);
}

export async function validatePanelRoutes(fetchImpl, port) {
  const passed = [];
  for (const [name, path, contentType] of expectedRoutes) {
    const response = await fetchImpl('http://127.0.0.1:' + port + path, { redirect: 'error' });
    if (response.status !== 200 || !response.headers.get('content-type')?.toLowerCase().startsWith(contentType)) {
      throw new Error('Windows EXE smoke route ' + name + ' failed');
    }
    await response.arrayBuffer();
    passed.push(name);
  }
  return passed;
}
```

`smokeWindowsExecutable` rejects non-Windows hosts, creates a private temporary `LOCALAPPDATA`, spawns `dist/gift-panel.exe` with `windowsHide: true`, polls ports 12450-12459 for 30 seconds, validates the three routes, calculates EXE SHA-256, requests takeover with `0.0.1`, waits 10 seconds for exit, and always terminates the child plus removes the temporary directory in `finally`. Never copy `runtime.log`; emit only allowlisted evidence fields.

- [ ] **Step 4: Add declarations and scripts**

```json
\"test:windows-smoke\": \"vitest run tests/smoke-windows-exe.test.ts\",
\"smoke:windows-exe\": \"node scripts/smoke-windows-exe.mjs\"
```

- [ ] **Step 5: Verify and commit**

Run: `npm test -- tests/smoke-windows-exe.test.ts`

Run: `npm run typecheck`

Expected: PASS. Do not run the full executable smoke on non-Windows.

```powershell
git add -- scripts/smoke-windows-exe.mjs scripts/smoke-windows-exe.d.mts tests/smoke-windows-exe.test.ts package.json
git commit -m \"test: add packaged Windows EXE smoke\"
```

### Task 5: Wire conditional Windows x64 without weakening release

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `tests/ci-workflow.test.ts`
- Modify: `tests/release-workflow.test.ts`

**Interfaces:**
- Consumes: `needs.scope.outputs.run_windows` and `windows_level`.
- Produces: unsigned CI EXE plus safe smoke evidence retained for 7 days.
- Preserves: `.github/workflows/release.yml` as the only signed publication workflow.

- [ ] **Step 1: Extend the failing contract**

```ts
it('runs unsigned Windows x64 only when scope requires it', () => {
  const jobs = ciWorkflow().jobs ?? {};
  const windows = jobs['windows-compat'];
  expect(windows['runs-on']).toBe('windows-2025');
  expect(windows.if).toContain(\"needs.scope.outputs.run_windows == 'true'\");
  expect(JSON.stringify(windows)).not.toMatch(/EVSIGN|COS_|HOSTED_.*(?:KEY|SECRET|TOKEN)|secrets\\./i);
  expect(JSON.stringify(windows)).toContain('npm run build:exe');
  expect(JSON.stringify(windows)).toContain('npm run smoke:windows-exe');
  expect(JSON.stringify(windows)).toContain('retention-days: 7');
});
```

Extend the release test to assert that `release.yml` still has `environment: release`, `cancel-in-progress: false`, signer verification before publication, and no `pull_request` trigger.

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/ci-workflow.test.ts tests/release-workflow.test.ts`

Expected: FAIL because the real Windows job is absent.

- [ ] **Step 3: Implement the job**

```yaml
  windows-compat:
    needs: [scope, hosted]
    if: needs.scope.outputs.run_windows == 'true'
    runs-on: windows-2025
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with: { persist-credentials: false }
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020
        with: { node-version: 22, cache: npm }
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
        with: { go-version-file: goserver/go.mod, cache-dependency-path: goserver/go.sum }
      - run: npm ci
      - run: npm exec -- playwright install chromium
      - run: npm test -- --reporter=dot --minWorkers=2 --maxWorkers=2
      - run: go -C goserver test ./... -race -count=1
      - run: npm run build:ui
      - name: Build unsigned CI executable
        env:
          APP_VERSION: 0.0.0
          APP_COMMIT: ${{ github.sha }}
          APP_UPDATE_API_URL: https://updates.example.test
          APP_UPDATE_PUBLISHER: CN=CI Smoke
        run: npm run build:exe
      - run: npm run smoke:windows-exe
      - if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: windows-compat-${{ github.sha }}
          path: |
            dist/gift-panel.exe
            dist/ci-smoke-evidence.json
          if-no-files-found: error
          retention-days: 7
```

The EXE is unsigned and keeps the non-production filename `gift-panel.exe`. Never upload it to a Release or mirror. The reserved `.test` update URL cannot become a production fallback. The embedded FFmpeg payload still passes existing integrity checks.

- [ ] **Step 4: Verify and commit**

Run: `npm test -- tests/ci-workflow.test.ts tests/release-workflow.test.ts tests/smoke-windows-exe.test.ts`

Run: `npm run typecheck`

Expected: PASS.

```powershell
git add -- .github/workflows/ci.yml tests/ci-workflow.test.ts tests/release-workflow.test.ts
git commit -m \"ci: add conditional Windows compatibility\"
```

### Task 6: Document the Mac and Windows ARM operating loop

**Files:**
- Create: `docs/development/mac-hosted-workflow.md`
- Create: `tests/development-workflow.test.ts`
- Modify: `README.md`

**Interfaces:**
- Produces one maintainer entrypoint for Mac setup, local commands, CI failure feedback, ARM snapshot use, and temporary x64 evidence.
- Links rather than duplicates EXE release and Hosted deployment runbooks.

- [ ] **Step 1: Write the failing contract**

```ts
import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

it('documents Hosted-first Mac development without claiming ARM is x64 acceptance', () => {
  const text = readFileSync(new URL('../docs/development/mac-hosted-workflow.md', import.meta.url), 'utf8');
  for (const command of ['npm ci', 'npm test', 'npm run typecheck', 'npm run build:hosted', 'go -C goserver test ./...']) expect(text).toContain(command);
  expect(text).toContain('交叉编译不等于 Windows 验收');
  expect(text).toContain('Windows 11 ARM');
  expect(text).toContain('不作为 x64 驱动、显卡或硬件编码证据');
  expect(text).toContain('5a0bbfb');
});
```

Do not hard-code future CI run IDs, credentials, local user paths, or a retirement date. `5a0bbfb` anchors the approved design commit, not deployment state.

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/development-workflow.test.ts`

Expected: FAIL because the runbook is absent.

- [ ] **Step 3: Write exact sections**

```markdown
## Mac prerequisites
## Daily Hosted loop
## What runs in pull-request CI
## When Windows x64 runs
## Downloading and reproducing a Windows failure
## Windows 11 ARM snapshot rules
## Temporary real x64 acceptance
## EXE release remains separate
## Retirement-stage changes
```

The failure section requires commit SHA, runner image, artifact SHA-256, failed test, coverage boundary, and rerun result. Fixes happen in the Mac checkout and are rebuilt by CI, never edited in the VM. ARM snapshots contain no release credentials.

- [ ] **Step 4: Link from README**

Add one paragraph under development build linking the new runbook. Keep existing EXE release documentation authoritative for signing and publication.

- [ ] **Step 5: Verify and commit**

Run: `npm test -- tests/development-workflow.test.ts`

Run: `npm run typecheck`

Expected: PASS.

```powershell
git add -- docs/development/mac-hosted-workflow.md tests/development-workflow.test.ts README.md
git commit -m \"docs: add Hosted-first Mac workflow\"
```

### Task 7: Version EXE-to-Hosted UI parity requirements

**Files:**
- Create: `acceptance/exe-hosted-ui/requirements.json`
- Create: `scripts/validate-ui-parity-requirements.mjs`
- Create: `scripts/validate-ui-parity-requirements.d.mts`
- Create: `tests/ui-parity-requirements.test.ts`
- Create: `docs/operations/exe-ui-baseline-capture.md`
- Modify: `package.json`

**Interfaces:**
- Produces: `validateUIParityRequirements(value): UIParityRequirements`.
- Viewports: `desktop-1440x900`, `narrow-1024x768`, `mobile-390x844`.
- Workspaces: `overview`, `attributes`, `activities`, `gift-targets`, `obs`, `analytics`.
- Future captures may reference only `acceptance/exe-hosted-ui/captures/<exe-version>/` and never tokenized URLs or absolute paths.

- [ ] **Step 1: Write failing strict-schema tests**

```ts
import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { validateUIParityRequirements } from '../scripts/validate-ui-parity-requirements.mjs';

it('requires all workspaces, states, comparisons, and viewports', () => {
  const value = JSON.parse(readFileSync(new URL('../acceptance/exe-hosted-ui/requirements.json', import.meta.url), 'utf8'));
  const contract = validateUIParityRequirements(value);
  expect(contract.viewports.map((item) => item.id)).toEqual(['desktop-1440x900', 'narrow-1024x768', 'mobile-390x844']);
  expect(contract.features.map((item) => item.id)).toEqual(['overview', 'attributes', 'activities', 'gift-targets', 'obs', 'analytics']);
  for (const feature of contract.features) {
    expect(feature.states).toEqual(expect.arrayContaining(['empty', 'populated', 'loading', 'error']));
    expect(feature.compare).toEqual(['structure', 'hierarchy', 'spacing', 'controls', 'states', 'responsive', 'interactions']);
  }
});
```

Add mutation cases rejecting unknown keys, duplicate IDs, missing viewports, absolute paths, `http://localhost`, `token=`, empty states, and shell/header-only comparison.

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/ui-parity-requirements.test.ts`

Expected: FAIL because contract and validator are absent.

- [ ] **Step 3: Create the exact requirements**

```json
{
  \"schema\": 1,
  \"reference\": { \"product\": \"gift-panel-exe\", \"minimumVersion\": \"0.4.10\" },
  \"viewports\": [
    { \"id\": \"desktop-1440x900\", \"width\": 1440, \"height\": 900, \"deviceScaleFactor\": 1 },
    { \"id\": \"narrow-1024x768\", \"width\": 1024, \"height\": 768, \"deviceScaleFactor\": 1 },
    { \"id\": \"mobile-390x844\", \"width\": 390, \"height\": 844, \"deviceScaleFactor\": 1 }
  ],
  \"features\": [
    { \"id\": \"overview\", \"states\": [\"empty\", \"populated\", \"loading\", \"error\"], \"interactions\": [\"navigate\", \"refresh\", \"open-settings\"] },
    { \"id\": \"attributes\", \"states\": [\"empty\", \"populated\", \"loading\", \"error\", \"editing\", \"validation-error\"], \"interactions\": [\"create\", \"edit\", \"save\", \"cancel\", \"delete-confirm\"] },
    { \"id\": \"activities\", \"states\": [\"empty\", \"populated\", \"loading\", \"error\", \"active\", \"locked\", \"settled\"], \"interactions\": [\"create\", \"start\", \"lock\", \"settle\", \"cancel\"] },
    { \"id\": \"gift-targets\", \"states\": [\"empty\", \"populated\", \"loading\", \"error\", \"editing\"], \"interactions\": [\"create\", \"edit\", \"save\", \"cancel\", \"delete-confirm\"] },
    { \"id\": \"obs\", \"states\": [\"empty\", \"populated\", \"loading\", \"error\", \"preview\"], \"interactions\": [\"create\", \"preview\", \"copy\", \"reset-link\", \"delete-confirm\"] },
    { \"id\": \"analytics\", \"states\": [\"empty\", \"populated\", \"loading\", \"error\", \"filtered\"], \"interactions\": [\"filter\", \"paginate\", \"open-viewer\", \"clear-history\"] }
  ]
}
```

The validator injects the same `compare` array for every feature: `structure`, `hierarchy`, `spacing`, `controls`, `states`, `responsive`, `interactions`. It reconstructs an allowlisted result rather than returning caller data.

- [ ] **Step 4: Write deterministic capture operations**

The runbook requires EXE version and commit, clean VM snapshot, fixture hash, browser version, 100% zoom, viewport, state, interaction steps, screenshot SHA-256, Hosted commit, direct side-by-side result, and accepted Hosted-only content. Include exactly:

```text
只对齐外壳、标题栏或部分卡片不算完成。
每个相关状态和视口都必须直接对比 EXE 与 Hosted 截图。
临时比较图默认保留本地；被验收记录引用的基准、状态清单和生成规则才进入版本控制。
```

- [ ] **Step 5: Add scripts, verify, and commit**

```json
\"test:ui-parity-contract\": \"vitest run tests/ui-parity-requirements.test.ts\",
\"validate:ui-parity\": \"node scripts/validate-ui-parity-requirements.mjs acceptance/exe-hosted-ui/requirements.json\"
```

Run: `npm test -- tests/ui-parity-requirements.test.ts`

Run: `npm run validate:ui-parity`

Run: `npm run typecheck`

Expected: PASS.

```powershell
git add -- acceptance/exe-hosted-ui/requirements.json scripts/validate-ui-parity-requirements.mjs scripts/validate-ui-parity-requirements.d.mts tests/ui-parity-requirements.test.ts docs/operations/exe-ui-baseline-capture.md package.json
git commit -m \"test: define EXE Hosted UI parity contract\"
```

### Task 8: Add evidence-gated EXE retirement operations

**Files:**
- Create: `docs/operations/exe-retirement-checklist.md`
- Create: `tests/exe-retirement-contract.test.ts`
- Modify: `README.md`

**Interfaces:**
- Produces stages: `A migration-development`, `B closed-pilot-and-voluntary-migration`, `C exe-feature-freeze`, `D maintenance-ended`.
- Consumes Hosted pilot, UI parity, gameplay migration, media parity, backup/restore, real Bilibili connections, and user migration evidence.
- Does not change migration protocol code or mark gates complete.

- [ ] **Step 1: Write the failing lifecycle contract**

```ts
import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

it('keeps EXE retirement evidence-gated and initially unchecked', () => {
  const text = readFileSync(new URL('../docs/operations/exe-retirement-checklist.md', import.meta.url), 'utf8');
  for (const heading of ['Stage A: migration-development', 'Stage B: closed-pilot-and-voluntary-migration', 'Stage C: exe-feature-freeze', 'Stage D: maintenance-ended']) expect(text).toContain(heading);
  for (const gate of ['media parity', 'EXE-versus-Hosted screenshots', 'migration export', 'seven-day rollback', 'backup restore', 'real Bilibili connections', 'user notification']) expect(text).toContain(gate);
  expect(text).not.toMatch(/^- \\[x\\]/m);
  expect(text).toContain('CI success is not production acceptance');
});
```

Assert links to `hosted-pilot-checklist.md`, the approved design, `2026-08-25-gameplay-unit-migration.md`, `2026-08-25-hosted-user-workspaces.md`, and `2026-08-25-hosted-gift-video-export.md`.

- [ ] **Step 2: Run RED**

Run: `npm test -- tests/exe-retirement-contract.test.ts`

Expected: FAIL because checklist is absent.

- [ ] **Step 3: Write exact stage entry and rollback sections**

Each stage uses this structure:

```markdown
## Stage B: closed-pilot-and-voluntary-migration

### Entry evidence
- [ ] Core parity evidence pointer:
- [ ] media parity evidence pointer:
- [ ] EXE-versus-Hosted screenshots manifest and SHA-256:
- [ ] migration export, preview, apply, and seven-day rollback evidence:
- [ ] Hong Kong backup restore evidence:
- [ ] real Bilibili connections and seven-day pilot decision:

### Windows policy after entry
- Shared core, migration, update security, and critical EXE defects only.
- No new EXE product capabilities.

### Return to previous stage when
- Any declared migration content is lost, any parity state is missing, rollback fails, or pilot go/no-go returns no-go.
```

Stage C additionally requires user notification and support policy. Stage D additionally requires archived signed EXE, SHA-256, signer subject, source commit, build instructions, migration instructions, and explicit old-package policy. Dates alone never satisfy entry.

- [ ] **Step 4: Link from README without claiming compatibility**

Add a README link. State that TypeScript exporter v2 and Hosted decoder compatibility must be proven by the gameplay-unit migration plan before Stage B. Do not change `src/migration.ts` or `goserver/internal/hosted/migration/envelope.go` in this task.

- [ ] **Step 5: Verify and commit**

Run: `npm test -- tests/exe-retirement-contract.test.ts tests/hosted-deploy.test.ts`

Expected: PASS without marking production evidence complete.

```powershell
git add -- docs/operations/exe-retirement-checklist.md tests/exe-retirement-contract.test.ts README.md
git commit -m \"docs: gate EXE retirement on Hosted evidence\"
```

### Task 9: Run the cross-platform completion gate

**Files:**
- Modify only if verification exposes a defect in Tasks 1-8; stage each repair with its owning task files.

**Interfaces:**
- Produces a reviewed commit sequence without unrelated dirty-worktree files.
- Produces Hosted Linux and conditional Windows x64 CI evidence.

- [ ] **Step 1: Verify focused contracts**

Run:

```powershell
npm test -- tests/ci-scope.test.ts tests/ci-gate.test.ts tests/ci-workflow.test.ts tests/smoke-windows-exe.test.ts tests/development-workflow.test.ts tests/ui-parity-requirements.test.ts tests/exe-retirement-contract.test.ts tests/release-workflow.test.ts
```

Expected: PASS.

- [ ] **Step 2: Verify the full Mac/Linux-compatible suite**

Run:

```powershell
npm test -- --reporter=dot --minWorkers=2 --maxWorkers=2
npm run typecheck
npm run build:hosted
go -C goserver test ./... -race -count=1
go -C goserver vet ./...
npm run test:update-api
```

Expected: every command exits 0. Run `npm run test:hosted-mysql` only when Docker is available; never describe it as passed if skipped.

- [ ] **Step 3: Observe real scope behavior**

Use temporary review branches with diffs limited to:

1. `src/hosted/`: expect `windows_level=skip` and no Windows runner.
2. `goserver/internal/gameplay/`: expect `windows_level=shared` and Windows x64.
3. A Windows high-risk fixture mutation under `goserver/*_windows_test.go`: expect `desktop-high-risk` and Windows x64.

Do not merge temporary mutations. Retain run URLs and summaries as CI evidence, then delete the temporary branch through normal repository workflow.

- [ ] **Step 4: Verify exact Windows evidence**

Confirm:

- runner is `windows-2025` x64;
- EXE used the stable, non-production test version `APP_VERSION=0.0.0` and no secrets;
- `ci-smoke-evidence.json` lists config, display, and config API routes;
- evidence SHA-256 matches uploaded `gift-panel.exe`;
- graceful exit completed;
- retention is 7 days;
- no Release, tag, signature, COS object, or update pointer was created.

- [ ] **Step 5: Re-run release contract without publication**

Run: `npm test -- tests/release-workflow.test.ts`

Expected: PASS. Do not dispatch `release.yml`, tag, sign, or publish.

- [ ] **Step 6: Audit committed scope**

Run:

```powershell
git status --short
git log --oneline --decorate -12
git diff --check HEAD~8..HEAD
git diff --name-only HEAD~8..HEAD
```

Expected: task commits contain only named CI, test, acceptance, runbook, README, and package files. Existing user modifications and untracked files remain untouched.

## Plan Completion Gate

The plan is complete only when Hosted validation is the stable required gate, Hosted-only changes demonstrably skip Windows, shared/desktop/high-risk changes select the correct Windows level, the packaged unsigned EXE starts and exits through its real local HTTP contract on Windows x64, release signing remains isolated, Mac and ARM limitations are documented, UI parity requirements are versioned, and retirement stages remain unchecked until external evidence exists. This plan does not claim Hosted production readiness, migration protocol compatibility, UI parity completion, or EXE retirement; those claims require referenced product and operational evidence.
