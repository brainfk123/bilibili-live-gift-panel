import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';
import {
  classifyChanges,
  formatGitHubSummary,
  parseNameStatusZ,
  readGitChanges,
} from '../scripts/ci-scope.mjs';

const temporaryRoots: string[] = [];

afterEach(() => {
  for (const root of temporaryRoots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function git(cwd: string, args: string[]): string {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

function createDiffFixture(): { base: string; head: string; root: string } {
  const root = mkdtempSync(join(tmpdir(), 'gift-panel-ci-scope-'));
  temporaryRoots.push(root);
  git(root, ['init', '--initial-branch=main']);
  git(root, ['config', 'core.autocrlf', 'false']);
  git(root, ['config', 'user.email', 'ci-scope@example.test']);
  git(root, ['config', 'user.name', 'CI Scope Test']);
  writeFileSync(join(root, 'README.md'), 'baseline\n');
  git(root, ['add', 'README.md']);
  git(root, ['commit', '-m', 'baseline']);
  const base = git(root, ['rev-parse', 'HEAD']);
  mkdirSync(join(root, 'src', 'hosted'), { recursive: true });
  writeFileSync(join(root, 'src', 'hosted', 'main.ts'), 'export {};\n');
  git(root, ['add', 'src/hosted/main.ts']);
  git(root, ['commit', '-m', 'hosted change']);
  return { base, head: git(root, ['rev-parse', 'HEAD']), root };
}

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

  it.each([
    'docs/development/mac-hosted-workflow.md',
    'acceptance/exe-hosted-ui/requirements.json',
    'CHANGELOG.md',
    'src/hosted/main.ts',
    'goserver/cmd/hosted/main.go',
    'goserver/internal/hosted/runtime/manager.go',
    'deploy/hosted/README.md',
    'tests/hosted-runtime.test.ts',
    'tests/development-workflow.test.ts',
    'tests/ui-parity-requirements.test.ts',
    'tests/exe-retirement-contract.test.ts',
    'scripts/build-hosted.mjs',
    'scripts/test-hosted-mysql.mjs',
    'hosted.html',
    'obs.html',
    'vite.hosted.config.ts',
  ])('terminates classification at the explicit skip rule for %s', (path) => {
    expect(classifyChanges([{ status: 'M', path }])).toMatchObject({
      windowsLevel: 'skip',
      runWindows: false,
    });
  });

  it.each([
    [['tests/development-workflow.test.ts', 'goserver/internal/gameplay/model.go'], 'shared'],
    [['tests/ui-parity-requirements.test.ts', 'updateapi/server.go'], 'desktop-high-risk'],
    [['tests/exe-retirement-contract.test.ts', 'goserver/internal/gameplay/model.go', 'scripts/build-go.mjs'], 'desktop-high-risk'],
  ])('preserves %s precedence across mixed paths', (paths, windowsLevel) => {
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
    expect(parseNameStatusZ('R100\0src/main.ts\0src/hosted/main.ts\0D\0goserver/tray_windows.go\0'))
      .toEqual([
        { status: 'R100', path: 'src/main.ts', destination: 'src/hosted/main.ts' },
        { status: 'D', path: 'goserver/tray_windows.go' },
      ]);
  });

  it('classifies both sides of a rename and keeps the higher level', () => {
    expect(classifyChanges([{
      status: 'R100',
      path: 'tests/development-workflow.test.ts',
      destination: 'updateapi/server.go',
    }])).toMatchObject({ windowsLevel: 'desktop-high-risk', runWindows: true });
  });

  it('rejects malformed SHAs before invoking Git', () => {
    expect(() => readGitChanges('not-a-sha', 'abcd')).toThrow('must be hexadecimal git SHAs');
  });

  it('fails closed for empty diffs and Git comparison failures', () => {
    const fixture = createDiffFixture();
    expect(readGitChanges(fixture.head, fixture.head, fixture.root))
      .toEqual([{ status: 'M', path: '<unknown-git-diff>' }]);
    expect(readGitChanges('deadbeef', 'feedface', fixture.root))
      .toEqual([{ status: 'M', path: '<unknown-git-diff>' }]);
  });

  it('escapes every Markdown-breaking path character in the summary table', () => {
    const summary = formatGitHubSummary(classifyChanges([{
      status: 'M',
      path: 'danger|`line\r\n<unsafe>&\\path',
    }]));
    expect(summary).toContain('| Overall level | Run Windows | Run MySQL | Path | Classified level | Reason |');
    expect(summary).toContain('| desktop-high-risk | true | false |');
    expect(summary).toContain('danger&#124;&#96;line&#13;&#10;&lt;unsafe&gt;&amp;&#92;path');
    expect(summary).not.toContain('danger|`line');
  });

  it('writes all CLI outputs and the human-readable summary without shell interpolation', () => {
    const root = mkdtempSync(join(tmpdir(), 'gift-panel-ci-scope-cli-'));
    temporaryRoots.push(root);
    const projectRoot = resolve(fileURLToPath(new URL('..', import.meta.url)));
    const head = execFileSync('git', [
      '-c',
      `safe.directory=${projectRoot.replaceAll('\\', '/')}`,
      '-C',
      projectRoot,
      'rev-parse',
      'HEAD',
    ], { encoding: 'utf8' }).trim();
    const output = join(root, 'github-output.txt');
    const summary = join(root, 'github-summary.md');
    writeFileSync(output, '');
    writeFileSync(summary, '');
    const result = spawnSync(process.execPath, [
      fileURLToPath(new URL('../scripts/ci-scope.mjs', import.meta.url)),
      head,
      head,
    ], {
      cwd: projectRoot,
      encoding: 'utf8',
      env: { ...process.env, GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: summary },
    });

    expect(result.status, result.stderr).toBe(0);
    const lines = readFileSync(output, 'utf8').trim().split(/\r?\n/);
    expect(lines.map((line) => line.slice(0, line.indexOf('=')))).toEqual([
      'windows_level',
      'run_windows',
      'run_mysql',
      'reasons_json',
    ]);
    expect(lines).toContain('windows_level=desktop-high-risk');
    expect(lines).toContain('run_windows=true');
    expect(JSON.parse(lines.find((line) => line.startsWith('reasons_json='))!.slice('reasons_json='.length)))
      .toEqual([{ path: '<unknown-git-diff>', level: 'desktop-high-risk', reason: 'unknown-path-fail-closed' }]);
    expect(readFileSync(summary, 'utf8'))
      .toContain('| desktop-high-risk | true | false | &lt;unknown-git-diff&gt; | desktop-high-risk | unknown-path-fail-closed |');
  });
});
