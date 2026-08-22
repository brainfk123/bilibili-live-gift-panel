import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { signFileWithCliRetry } from '../scripts/sign-evsign-cli.mjs';

const roots: string[] = [];
afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'sign-evsign-cli-'));
  roots.push(root);
  const inputPath = join(root, 'app.exe');
  writeFileSync(inputPath, Buffer.from('unsigned-input'));
  return { inputPath };
}

describe('EVSign CLI retry orchestration', () => {
  it('restores unsigned bytes before retry and keeps the successful signed output', async () => {
    const { inputPath } = fixture();
    const inputs: string[] = [];
    const delays: number[] = [];
    let attempt = 0;

    await signFileWithCliRetry({ cliPath: 'evsign-client.exe', inputPath, key: 'secret', maxAttempts: 3, attemptTimeoutMs: 600_000, retryDelaysMs: [15_000, 45_000] }, {
      run: async (_cli, args) => {
        expect(args).toEqual([inputPath, '-key', 'secret', '-sha256']);
        inputs.push(readFileSync(inputPath, 'utf8'));
        writeFileSync(inputPath, Buffer.from(attempt++ === 0 ? 'partial-output' : 'signed-output'));
        return { exitCode: attempt === 1 ? 1 : 0, timedOut: false };
      },
      sleep: async (milliseconds) => { delays.push(milliseconds); },
      log: () => {},
    });

    expect(inputs).toEqual(['unsigned-input', 'unsigned-input']);
    expect(delays).toEqual([15_000]);
    expect(readFileSync(inputPath, 'utf8')).toBe('signed-output');
  });

  it('restores the unsigned input after exhausting retries without leaking credentials', async () => {
    const { inputPath } = fixture();
    let attempts = 0;
    await expect(signFileWithCliRetry({ cliPath: 'evsign-client.exe', inputPath, key: 'secret-key', password: 'secret-password', maxAttempts: 2, attemptTimeoutMs: 1_000, retryDelaysMs: [0] }, {
      run: async () => {
        attempts += 1;
        writeFileSync(inputPath, Buffer.from('partial-output'));
        return { exitCode: null, timedOut: true };
      },
      sleep: async () => {},
      log: () => {},
    })).rejects.toThrow(/2 attempts/);
    expect(attempts).toBe(2);
    expect(readFileSync(inputPath, 'utf8')).toBe('unsigned-input');
  });
});
