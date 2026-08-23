import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

const roots: string[] = [];
afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

describe('verify-ffmpeg CLI', () => {
  it('resolves an explicit payload directory before reading its manifest', () => {
    const payloadDirectory = mkdtempSync(join(tmpdir(), 'verify-ffmpeg-cli-'));
    roots.push(payloadDirectory);

    const result = spawnSync(process.execPath, [
      resolve('scripts/verify-ffmpeg.mjs'),
      '--payload-only',
      '--payload-directory', payloadDirectory,
    ], { encoding: 'utf8' });

    expect(result.status).not.toBe(0);
    expect(result.stderr).not.toContain('ReferenceError');
    expect(result.stderr).toContain('ENOENT');
    expect(result.stderr).toContain('manifest.json');
  });
});
