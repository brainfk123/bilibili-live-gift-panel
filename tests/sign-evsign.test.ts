import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import * as evsign from '../scripts/sign-evsign.mjs';
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

describe('EV Sign signer profile resolution', () => {
  const resolveProfile = () => (evsign as typeof evsign & {
    resolveEVSignSignerProfile: (environment: Record<string, string | undefined>) => {
      schema: number;
      source: string;
      profile: string;
      cert: string;
      subject: string;
    };
  }).resolveEVSignSignerProfile;

  it('selects the active profile as one atomic certificate and subject pair', () => {
    const result = resolveProfile()({
      EVSIGN_ACTIVE_PROFILE: 'naisnet',
      EVSIGN_SIGNER_PROFILES_JSON: JSON.stringify([
        { name: 'rushrush', cert: 'cert-old', subject: 'CN=RushRush' },
        { name: 'naisnet', cert: 'cert-new', subject: 'CN=NaisNet' },
      ]),
      EVSIGN_CERT: 'stale-cert',
      EVSIGN_EXPECTED_SUBJECT: 'CN=Stale',
    });

    expect(result).toEqual({
      schema: 1,
      source: 'profiles',
      profile: 'naisnet',
      cert: 'cert-new',
      subject: 'CN=NaisNet',
    });
  });

  it('keeps the exact legacy pair when no profile configuration exists', () => {
    expect(resolveProfile()({
      EVSIGN_CERT: 'legacy-cert',
      EVSIGN_EXPECTED_SUBJECT: 'CN=Legacy',
    })).toEqual({
      schema: 1,
      source: 'legacy',
      profile: 'legacy',
      cert: 'legacy-cert',
      subject: 'CN=Legacy',
    });
  });

  it('emits the selected profile as strict JSON for the release workflow', () => {
    const result = spawnSync(process.execPath, [resolve('scripts/sign-evsign.mjs'), '--resolve-profile'], {
      cwd: resolve('.'),
      encoding: 'utf8',
      env: {
        ...process.env,
        EVSIGN_ACTIVE_PROFILE: 'naisnet',
        EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":"cert-new","subject":"CN=NaisNet"}]',
        EVSIGN_CERT: '',
        EVSIGN_EXPECTED_SUBJECT: '',
      },
    });

    expect(result.status, result.stderr).toBe(0);
    expect(JSON.parse(result.stdout)).toEqual({
      schema: 1,
      source: 'profiles',
      profile: 'naisnet',
      cert: 'cert-new',
      subject: 'CN=NaisNet',
    });
  });

  it.each([
    ['unknown active profile', { EVSIGN_ACTIVE_PROFILE: 'missing', EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":"","subject":"CN=NaisNet"}]' }, /active EVSign signer profile does not exist/],
    ['profiles without an active name', { EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":"","subject":"CN=NaisNet"}]' }, /must be configured together/],
    ['active name without profiles', { EVSIGN_ACTIVE_PROFILE: 'naisnet', EVSIGN_EXPECTED_SUBJECT: 'CN=Legacy' }, /must be configured together/],
    ['duplicate profile names', { EVSIGN_ACTIVE_PROFILE: 'naisnet', EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":"a","subject":"CN=A"},{"name":"naisnet","cert":"b","subject":"CN=B"}]' }, /profile name is duplicated/],
    ['unknown profile property', { EVSIGN_ACTIVE_PROFILE: 'naisnet', EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":"","subject":"CN=NaisNet","acceptAny":true}]' }, /unknown properties/],
    ['subject containing a newline', { EVSIGN_ACTIVE_PROFILE: 'naisnet', EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":"","subject":"CN=NaisNet\\nO=Injected"}]' }, /subject is invalid/],
    ['certificate selector with surrounding whitespace', { EVSIGN_ACTIVE_PROFILE: 'naisnet', EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":" cert-new ","subject":"CN=NaisNet"}]' }, /cert is invalid/],
    ['subject with surrounding whitespace', { EVSIGN_ACTIVE_PROFILE: 'naisnet', EVSIGN_SIGNER_PROFILES_JSON: '[{"name":"naisnet","cert":"","subject":" CN=NaisNet "}]' }, /subject is invalid/],
    ['missing legacy subject', { EVSIGN_CERT: 'legacy-cert' }, /EVSIGN_EXPECTED_SUBJECT is required/],
  ])('rejects %s', (_label, environment, expected) => {
    expect(() => resolveProfile()(environment)).toThrow(expected);
  });
});
