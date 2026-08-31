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
