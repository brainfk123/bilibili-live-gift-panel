import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { signFileWithRetry } from '../scripts/sign-evsign.mjs';

const roots: string[] = [];
afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'sign-evsign-'));
  roots.push(root);
  const inputPath = join(root, 'input.exe');
  const outputPath = join(root, 'output.exe');
  writeFileSync(inputPath, Buffer.from('unsigned-input'));
  writeFileSync(outputPath, Buffer.from('old-output'));
  return { inputPath, outputPath };
}

function failure(properties: Record<string, unknown>) {
  return Object.assign(new Error('synthetic signing failure'), properties);
}

describe('EV Sign retry orchestration', () => {
  it.each([
    ['timeout', failure({ code: 'ETIMEDOUT' })],
    ['HTTP 408', failure({ statusCode: 408 })],
    ['HTTP 429', failure({ statusCode: 429 })],
    ['HTTP 503', failure({ statusCode: 503 })],
  ])('retries %s using the original bytes', async (_label, firstFailure) => {
    const { inputPath, outputPath } = fixture();
    const bodies: Buffer[] = [];
    const delays: number[] = [];
    let attempt = 0;
    await signFileWithRetry({ inputPath, outputPath, endpoint: 'https://example.invalid/v1', headers: {}, maxAttempts: 3, attemptTimeoutMs: 600_000, retryDelaysMs: [15_000, 45_000] }, {
      request: async (body) => {
        bodies.push(Buffer.from(body));
        if (attempt++ === 0) throw firstFailure;
        return Buffer.from('signed-output');
      },
      sleep: async (milliseconds) => { delays.push(milliseconds); },
      log: () => {},
    });
    expect(bodies.map((body) => body.toString())).toEqual(['unsigned-input', 'unsigned-input']);
    expect(delays).toEqual([15_000]);
    expect(readFileSync(outputPath, 'utf8')).toBe('signed-output');
  });

  it.each([400, 401, 403, 404, 409, 422])('does not retry terminal HTTP %s', async (statusCode) => {
    const { inputPath, outputPath } = fixture();
    let attempts = 0;
    await expect(signFileWithRetry({ inputPath, outputPath, endpoint: 'https://example.invalid/v1', headers: {}, maxAttempts: 3, attemptTimeoutMs: 600_000, retryDelaysMs: [15_000, 45_000] }, {
      request: async () => { attempts += 1; throw failure({ statusCode }); },
      sleep: async () => {},
      log: () => {},
    })).rejects.toThrow(/HTTP/);
    expect(attempts).toBe(1);
    expect(readFileSync(outputPath, 'utf8')).toBe('old-output');
  });

  it('exhausts three retryable attempts and preserves the old output', async () => {
    const { inputPath, outputPath } = fixture();
    const delays: number[] = [];
    let attempts = 0;
    await expect(signFileWithRetry({ inputPath, outputPath, endpoint: 'https://example.invalid/v1', headers: {}, maxAttempts: 3, attemptTimeoutMs: 600_000, retryDelaysMs: [15_000, 45_000] }, {
      request: async () => { attempts += 1; throw failure({ statusCode: 503 }); },
      sleep: async (milliseconds) => { delays.push(milliseconds); },
      log: () => {},
    })).rejects.toThrow(/3 attempts/);
    expect(attempts).toBe(3);
    expect(delays).toEqual([15_000, 45_000]);
    expect(readFileSync(outputPath, 'utf8')).toBe('old-output');
  });

  it('rejects empty responses without retrying or replacing output', async () => {
    const { inputPath, outputPath } = fixture();
    let attempts = 0;
    await expect(signFileWithRetry({ inputPath, outputPath, endpoint: 'https://example.invalid/v1', headers: {}, maxAttempts: 3, attemptTimeoutMs: 600_000, retryDelaysMs: [15_000, 45_000] }, {
      request: async () => { attempts += 1; return Buffer.alloc(0); },
      sleep: async () => {},
      log: () => {},
    })).rejects.toThrow(/empty/);
    expect(attempts).toBe(1);
    expect(readFileSync(outputPath, 'utf8')).toBe('old-output');
  });
});
