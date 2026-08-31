import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { parseDocument } from 'yaml';

interface WorkflowStep {
  env?: Record<string, string>;
  id?: string;
  if?: string;
  name?: string;
  run?: string;
  uses?: string;
  with?: Record<string, unknown>;
}

interface WorkflowJob {
  env?: Record<string, string>;
  if?: string;
  needs?: string[];
  outputs?: Record<string, string>;
  'runs-on'?: string;
  steps?: WorkflowStep[];
}

function ciWorkflow() {
  const source = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const document = parseDocument(source);
  expect(document.errors).toEqual([]);
  const workflow = document.toJS() as {
    concurrency?: { group?: string; 'cancel-in-progress'?: boolean };
    jobs?: Record<string, WorkflowJob>;
    on?: Record<string, unknown>;
    permissions?: Record<string, string>;
  };
  return { source, workflow };
}

function commands(job: WorkflowJob | undefined): string[] {
  return (job?.steps ?? []).flatMap((step) => step.run ? [step.run] : []);
}

describe('mainline CI workflow', () => {
  it('uses read-only permissions and immutable Actions', () => {
    const { workflow } = ciWorkflow();
    expect(workflow.permissions).toEqual({ contents: 'read' });
    const uses = Object.values(workflow.jobs ?? {})
      .flatMap((job) => job.steps ?? [])
      .flatMap((step) => step.uses ? [step.uses] : []);

    expect(uses.length).toBeGreaterThan(0);
    for (const value of uses) {
      expect(value).toMatch(/^[^@\s]+@[0-9a-f]{40}$/);
    }
  });

  it('runs for pull requests and the two mainline push branches without changing release concurrency', () => {
    const { workflow } = ciWorkflow();
    expect(workflow.on).toEqual({
      pull_request: null,
      push: { branches: ['master', 'codex/hosted-service'] },
    });
    expect(workflow.concurrency).toEqual({
      group: 'ci-${{ github.workflow }}-${{ github.ref }}',
      'cancel-in-progress': true,
    });
  });

  it('exports scope decisions from the complete comparison range', () => {
    const scope = ciWorkflow().workflow.jobs?.scope;
    expect(scope?.['runs-on']).toBe('ubuntu-24.04');
    expect(scope?.outputs).toEqual({
      windows_level: '${{ steps.scope.outputs.windows_level }}',
      run_windows: '${{ steps.scope.outputs.run_windows }}',
      run_mysql: '${{ steps.scope.outputs.run_mysql }}',
    });
    expect(scope?.steps?.find((step) => step.uses?.startsWith('actions/checkout@'))?.with)
      .toMatchObject({ 'fetch-depth': 0, 'persist-credentials': false });
    expect(scope?.steps?.find((step) => step.id === 'scope')).toMatchObject({
      env: {
        CI_BASE_SHA: '${{ github.event.pull_request.base.sha || github.event.before }}',
        CI_HEAD_SHA: '${{ github.event.pull_request.head.sha || github.sha }}',
      },
      run: 'npm run ci:scope -- "$CI_BASE_SHA" "$CI_HEAD_SHA"',
    });
  });

  it('always runs the complete Hosted path and scope-conditions MySQL', () => {
    const jobs = ciWorkflow().workflow.jobs ?? {};
    expect(Object.keys(jobs)).toEqual(expect.arrayContaining([
      'scope', 'hosted', 'hosted-mysql', 'windows-compat', 'ci-gate',
    ]));
    expect(jobs.hosted?.needs).toEqual(['scope']);
    expect(jobs.hosted?.if).toBeUndefined();
    expect(commands(jobs.hosted)).toEqual(expect.arrayContaining([
      'npm test -- --reporter=dot --minWorkers=2 --maxWorkers=2',
      'npm run typecheck',
      'npm run build:hosted',
      'go -C goserver test ./... -race -count=1',
      'go -C goserver vet ./...',
      'npm run test:update-api',
      'go -C goserver build -trimpath -o "${{ runner.temp }}/gift-panel-hosted" ./cmd/hosted',
    ]));
    expect(jobs['hosted-mysql']?.needs).toEqual(['scope', 'hosted']);
    expect(jobs['hosted-mysql']?.if).toContain("needs.scope.outputs.run_mysql == 'true'");
    expect(commands(jobs['hosted-mysql'])).toContain('npm run test:hosted-mysql');
  });

  it('runs an unsigned Windows x64 package and smoke only when scope requires it', () => {
    const windows = ciWorkflow().workflow.jobs?.['windows-compat'];
    expect(windows?.['runs-on']).toBe('windows-2025');
    expect(windows?.needs).toEqual(['scope', 'hosted']);
    expect(windows?.if).toBe("needs.scope.outputs.run_windows == 'true'");
    expect(commands(windows)).toEqual(expect.arrayContaining([
      'npm run build:ui',
      'npm run build:exe',
      'npm run smoke:windows-exe',
    ]));
    const build = windows?.steps?.find((step) => step.name === 'Build unsigned CI executable');
    expect(build?.env).toEqual({
      APP_BUILD_PROFILE: 'ci-windows-smoke',
      APP_VERSION: '0.0.0',
      APP_COMMIT: '${{ github.sha }}',
      APP_UPDATE_API_URL: 'https://updates.example.test',
      APP_UPDATE_PUBLISHER: 'CN=CI Smoke',
      CI: 'true',
    });
    expect(windows?.steps?.find((step) => step.run === 'npm run smoke:windows-exe')?.env)
      .toEqual({ GIFT_PANEL_CI_SMOKE: 'true' });
    const upload = windows?.steps?.find((step) => step.uses?.startsWith('actions/upload-artifact@'));
    expect(upload).toMatchObject({
      if: 'always()',
      with: {
        name: 'windows-compat-${{ github.sha }}',
        path: 'dist/gift-panel.exe\ndist/ci-smoke-evidence.json\n',
        'if-no-files-found': 'error',
        'retention-days': 7,
      },
    });
  });

  it('keeps credentials and release-only publication inputs out of mainline CI', () => {
    const { source } = ciWorkflow();
    expect(source).not.toMatch(/secrets\s*\./i);
    expect(source).not.toMatch(/EVSIGN|COS_|HOSTED_.*(?:KEY|SECRET|TOKEN)/i);
    expect(source).not.toContain('gift-panel-windows-x64.exe');
    expect(source).not.toMatch(/\bgh\s+release\b/i);
  });

  it('always evaluates every required and optional job in the final gate', () => {
    const gate = ciWorkflow().workflow.jobs?.['ci-gate'];
    expect(gate?.if).toBe('always()');
    expect(gate?.needs).toEqual(['scope', 'hosted', 'hosted-mysql', 'windows-compat']);
    const gateStep = gate?.steps?.find((step) => step.run === 'npm run ci:gate');
    expect(gateStep?.env).toEqual({
      CI_SCOPE_RESULT: '${{ needs.scope.result }}',
      CI_HOSTED_RESULT: '${{ needs.hosted.result }}',
      CI_MYSQL_RESULT: '${{ needs.hosted-mysql.result }}',
      CI_WINDOWS_RESULT: '${{ needs.windows-compat.result }}',
      CI_WINDOWS_LEVEL: '${{ needs.scope.outputs.windows_level }}',
      CI_EXPECT_MYSQL: '${{ needs.scope.outputs.run_mysql }}',
      CI_EXPECT_WINDOWS: '${{ needs.scope.outputs.run_windows }}',
    });
  });
});
