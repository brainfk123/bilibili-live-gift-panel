import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { evaluateGate } from '../scripts/ci-gate.mjs';

describe('CI final gate', () => {
  it.each([
    [{ scope: 'success', hosted: 'success', mysql: 'skipped', windows: 'skipped', windowsLevel: 'skip', expectMySQL: false, expectWindows: false }, true],
    [{ scope: 'success', hosted: 'success', mysql: 'success', windows: 'success', windowsLevel: 'shared', expectMySQL: true, expectWindows: true }, true],
    [{ scope: 'success', hosted: 'success', mysql: 'skipped', windows: 'success', windowsLevel: 'desktop', expectMySQL: false, expectWindows: true }, true],
    [{ scope: 'success', hosted: 'success', mysql: 'skipped', windows: 'success', windowsLevel: 'desktop-high-risk', expectMySQL: false, expectWindows: true }, true],
    [{ scope: 'success', hosted: 'failure', mysql: 'skipped', windows: 'skipped', windowsLevel: 'skip', expectMySQL: false, expectWindows: false }, false],
    [{ scope: 'success', hosted: 'success', mysql: 'skipped', windows: 'success', windowsLevel: 'desktop', expectMySQL: true, expectWindows: true }, false],
    [{ scope: 'success', hosted: 'success', mysql: 'success', windows: 'cancelled', windowsLevel: 'shared', expectMySQL: true, expectWindows: true }, false],
    [{ scope: 'failure', hosted: 'skipped', mysql: 'skipped', windows: 'skipped', windowsLevel: 'skip', expectMySQL: false, expectWindows: false }, false],
  ])('evaluates %j', (input, ok) => expect(evaluateGate(input).ok).toBe(ok));

  it('fails closed with a stable diagnostic for an invalid Windows level', () => {
    const decision = evaluateGate({
      scope: 'success', hosted: 'success', mysql: 'skipped', windows: 'success',
      windowsLevel: 'unknown', expectMySQL: false, expectWindows: true,
    });
    expect(decision.ok).toBe(false);
    expect(decision.failures).toContain('windows-level:invalid');
  });

  it.each([
    ['skip', true],
    ['shared', false],
  ])('rejects an expectation inconsistent with Windows level %s', (windowsLevel, expectWindows) => {
    const decision = evaluateGate({
      scope: 'success', hosted: 'success', mysql: 'skipped', windows: expectWindows ? 'success' : 'skipped',
      windowsLevel, expectMySQL: false, expectWindows,
    });
    expect(decision.ok).toBe(false);
    expect(decision.failures).toContain('windows-level:inconsistent');
  });

  it('reads CI_WINDOWS_LEVEL in CLI mode', () => {
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('../scripts/ci-gate.mjs', import.meta.url))], {
      encoding: 'utf8',
      env: {
        ...process.env,
        CI_SCOPE_RESULT: 'success',
        CI_HOSTED_RESULT: 'success',
        CI_MYSQL_RESULT: 'skipped',
        CI_WINDOWS_RESULT: 'success',
        CI_WINDOWS_LEVEL: 'skip',
        CI_EXPECT_MYSQL: 'false',
        CI_EXPECT_WINDOWS: 'true',
      },
    });
    expect(result.status).toBe(1);
    expect(JSON.parse(result.stdout)).toMatchObject({
      ok: false,
      failures: expect.arrayContaining(['windows-level:inconsistent']),
    });
  });

  it('keeps CLI booleans restricted to exact lowercase true', () => {
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('../scripts/ci-gate.mjs', import.meta.url))], {
      encoding: 'utf8',
      env: {
        ...process.env,
        CI_SCOPE_RESULT: 'success',
        CI_HOSTED_RESULT: 'success',
        CI_MYSQL_RESULT: 'skipped',
        CI_WINDOWS_RESULT: 'skipped',
        CI_WINDOWS_LEVEL: 'skip',
        CI_EXPECT_MYSQL: 'TRUE',
        CI_EXPECT_WINDOWS: 'TRUE',
      },
    });
    expect(result.status).toBe(0);
    expect(JSON.parse(result.stdout)).toEqual({ ok: true, failures: [] });
  });
});
