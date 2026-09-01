import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import * as evsign from '../scripts/sign-evsign.mjs';
import { signFileWithRetry, signWithProfile } from '../scripts/sign-evsign.mjs';

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
    resolveEVSignSignerProfile: (profile: string, environment: Record<string, string | undefined>) => {
      schema: number;
      profile: string;
      certificate: string;
      identity: { country: string; organization: string; organizationId: string };
    };
  }).resolveEVSignSignerProfile;

  it('binds stable to its reviewed NaisNet certificate selector and structured identity', () => {
    const result = resolveProfile()('stable', {
      EVSIGN_CERTIFICATE: 'naisnet-certificate-selector',
      EVSIGN_PUBLISHER_IDENTITY: JSON.stringify({
        country: 'CN',
        organization: 'NaisNet Technology Co., Ltd.',
        organizationId: '91210103MA7CJ3C094',
      }),
    });

    expect(result).toEqual({
      schema: 2,
      profile: 'stable',
      certificate: 'naisnet-certificate-selector',
      identity: {
        country: 'CN',
        organization: 'NaisNet Technology Co., Ltd.',
        organizationId: '91210103MA7CJ3C094',
      },
    });
  });

  it('binds bridge to its reviewed RushRush certificate selector and structured identity', () => {
    expect(resolveProfile()('bridge', {
      EVSIGN_BRIDGE_CERTIFICATE: 'rushrush-certificate-selector',
      EVSIGN_BRIDGE_PUBLISHER_IDENTITY: JSON.stringify({
        country: 'CN',
        organization: 'RushRush Network Technology Ltd',
        organizationId: '91450900MADM3GLG5P',
      }),
    })).toEqual({
      schema: 2,
      profile: 'bridge',
      certificate: 'rushrush-certificate-selector',
      identity: {
        country: 'CN',
        organization: 'RushRush Network Technology Ltd',
        organizationId: '91450900MADM3GLG5P',
      },
    });
  });

  it('emits only redacted profile metadata for workflow preflight', () => {
    const certificate = 'recognizable-rushrush-certificate-selector';
    const result = spawnSync(process.execPath, [resolve('scripts/sign-evsign.mjs'), '--resolve-profile', 'bridge'], {
      cwd: resolve('.'),
      encoding: 'utf8',
      env: {
        ...process.env,
        EVSIGN_BRIDGE_CERTIFICATE: certificate,
        EVSIGN_BRIDGE_PUBLISHER_IDENTITY: JSON.stringify({
          country: 'CN',
          organization: 'RushRush Network Technology Ltd',
          organizationId: '91450900MADM3GLG5P',
        }),
        EVSIGN_CERTIFICATE: '',
        EVSIGN_PUBLISHER_IDENTITY: '',
      },
    });

    expect(result.status, result.stderr).toBe(0);
    expect(JSON.parse(result.stdout)).toEqual({
      schema: 2,
      profile: 'bridge',
      certificateConfigured: true,
      identity: {
        country: 'CN',
        organization: 'RushRush Network Technology Ltd',
        organizationId: '91450900MADM3GLG5P',
      },
    });
    expect(`${result.stdout}${result.stderr}`).not.toContain(certificate);
  });

  it.each([
    ['unknown profile', 'naisnet', {}, /unknown EVSign signer profile/],
    ['missing stable certificate', 'stable', { EVSIGN_PUBLISHER_IDENTITY: '{"country":"CN"}' }, /stable EVSign profile is not configured/],
    ['missing bridge identity', 'bridge', { EVSIGN_BRIDGE_CERTIFICATE: 'selector' }, /bridge EVSign profile is not configured/],
    ['legacy free-form configuration', 'stable', { EVSIGN_CERT: 'legacy', EVSIGN_EXPECTED_SUBJECT: 'CN=Legacy' }, /stable EVSign profile is not configured/],
    ['cross-profile bridge values in stable', 'stable', {
      EVSIGN_CERTIFICATE: 'stable',
      EVSIGN_PUBLISHER_IDENTITY: '{"country":"CN","organization":"NaisNet Technology Co., Ltd.","organizationId":"91210103MA7CJ3C094"}',
      EVSIGN_BRIDGE_CERTIFICATE: 'bridge',
    }, /cross-profile EVSign configuration/],
    ['wrong stable legal identity', 'stable', {
      EVSIGN_CERTIFICATE: 'selector',
      EVSIGN_PUBLISHER_IDENTITY: '{"country":"CN","organization":"RushRush Network Technology Ltd","organizationId":"91450900MADM3GLG5P"}',
    }, /stable EVSign publisher identity is not the reviewed identity/],
    ['wrong bridge legal identity', 'bridge', {
      EVSIGN_BRIDGE_CERTIFICATE: 'selector',
      EVSIGN_BRIDGE_PUBLISHER_IDENTITY: '{"country":"CN","organization":"NaisNet Technology Co., Ltd.","organizationId":"91210103MA7CJ3C094"}',
    }, /bridge EVSign publisher identity is not the reviewed identity/],
    ['identity with unknown property', 'bridge', {
      EVSIGN_BRIDGE_CERTIFICATE: 'selector',
      EVSIGN_BRIDGE_PUBLISHER_IDENTITY: '{"country":"CN","organization":"RushRush Network Technology Ltd","organizationId":"91450900MADM3GLG5P","subject":"free-form"}',
    }, /bridge EVSign publisher identity is invalid/],
    ['certificate selector with surrounding whitespace', 'bridge', {
      EVSIGN_BRIDGE_CERTIFICATE: ' selector ',
      EVSIGN_BRIDGE_PUBLISHER_IDENTITY: '{"country":"CN","organization":"RushRush Network Technology Ltd","organizationId":"91450900MADM3GLG5P"}',
    }, /bridge EVSign profile is not configured/],
  ])('rejects %s without exposing configuration', (_label, profile, environment, expected) => {
    let error: unknown;
    try {
      resolveProfile()(profile, environment);
    } catch (caught) {
      error = caught;
    }
    expect(error).toBeInstanceOf(Error);
    expect((error as Error).message).toMatch(expected);
    for (const value of Object.values(environment)) {
      if (value) expect((error as Error).message).not.toContain(value);
    }
  });
});

describe('closed-profile signing entry point', () => {
  it('runs the stable signing fake with only the stable certificate selector', async () => {
    const { inputPath, outputPath } = fixture();
    const seenHeaders: Record<string, string>[] = [];
    await signWithProfile({
      profile: 'stable',
      environment: {
        EVSIGN_CERTIFICATE: 'stable-selector',
        EVSIGN_PUBLISHER_IDENTITY: JSON.stringify({
          country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094',
        }),
        EVSIGN_KEY: 'synthetic-key',
      },
      inputPath,
      outputPath,
    }, {
      request: async (_source, request) => {
        seenHeaders.push(request.headers);
        return Buffer.from('stable-signed-output');
      },
      sleep: async () => {},
      log: () => {},
    });
    expect(seenHeaders).toHaveLength(1);
    expect(seenHeaders[0]).toMatchObject({ 'X-Cert': 'stable-selector', 'X-Key': 'synthetic-key' });
    expect(JSON.stringify(seenHeaders[0])).not.toContain('bridge');
    expect(readFileSync(outputPath, 'utf8')).toBe('stable-signed-output');
  });

  it('rejects bridge-only configuration before the stable signing fake runs', async () => {
    const { inputPath, outputPath } = fixture();
    let requests = 0;
    await expect(signWithProfile({
      profile: 'stable',
      environment: {
        EVSIGN_BRIDGE_CERTIFICATE: 'bridge-selector',
        EVSIGN_BRIDGE_PUBLISHER_IDENTITY: JSON.stringify({
          country: 'CN', organization: 'RushRush Network Technology Ltd', organizationId: '91450900MADM3GLG5P',
        }),
        EVSIGN_KEY: 'synthetic-key',
      },
      inputPath,
      outputPath,
    }, {
      request: async () => { requests += 1; return Buffer.from('must-not-sign'); },
      sleep: async () => {},
      log: () => {},
    })).rejects.toThrow(/stable EVSign profile is not configured|cross-profile EVSign configuration/);
    expect(requests).toBe(0);
  });
});
