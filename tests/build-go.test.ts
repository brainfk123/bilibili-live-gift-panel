import { Buffer } from 'node:buffer';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { resolveGoLdflags, runGoCompilerCandidates } from '../scripts/build-go.mjs';
import { resolveBuildGoPolicy } from '../scripts/build-go-policy.mjs';
import { resolveUpdateAPIBaseURLHex } from '../scripts/update-api-build-config.mjs';

const testTrustRootSPKIBase64 = readFileSync(
  new URL('../goserver/testdata/update-trust/root-epoch-1-spki.der', import.meta.url),
).toString('base64');
const testTrustBootstrapPolicyBase64 = readFileSync(
  new URL('../goserver/testdata/update-trust/policy-epoch-1.json', import.meta.url),
).toString('base64');

const validCIWindowsSmokeEnvironment = {
  APP_BUILD_PROFILE: 'ci-windows-smoke',
  CI: 'true',
  APP_VERSION: '0.0.0',
  APP_COMMIT: 'A1'.repeat(20),
  APP_UPDATE_API_URL: 'https://updates.example.test',
  APP_UPDATE_PUBLISHER: 'CN=CI Smoke',
};

describe('build-go policy', () => {
  it('allows payload-only verification for the exact CI Windows smoke sentinels', () => {
    expect(resolveBuildGoPolicy(validCIWindowsSmokeEnvironment)).toMatchObject({
      profile: 'ci-windows-smoke',
      appVersion: '0.0.0',
      appCommit: 'A1'.repeat(20),
      updateAPIURL: 'https://updates.example.test',
      updatePublisher: 'CN=CI Smoke',
      requireAuthenticode: false,
      verificationAppVersion: 'dev',
      verifyPayloadOnly: true,
    });
  });

  it.each([
    ['unknown profile', { APP_BUILD_PROFILE: 'ci' }],
    ['CI marker', { CI: 'false' }],
    ['version', { APP_VERSION: 'v0.0.0' }],
    ['short commit', { APP_COMMIT: 'a'.repeat(39) }],
    ['non-hex commit', { APP_COMMIT: 'g'.repeat(40) }],
    ['update URL', { APP_UPDATE_API_URL: 'https://updates.example.test/' }],
    ['publisher', { APP_UPDATE_PUBLISHER: 'CN=CI Smoke ' }],
  ])('fails closed when the %s sentinel differs', (_label, override) => {
    expect(() => resolveBuildGoPolicy({ ...validCIWindowsSmokeEnvironment, ...override }))
      .toThrow(/APP_BUILD_PROFILE|ci-windows-smoke/);
  });

  it('preserves development and release defaults when no profile is selected', () => {
    expect(resolveBuildGoPolicy({ APP_VERSION: 'dev' })).toMatchObject({
      profile: 'default',
      requireAuthenticode: false,
      verificationAppVersion: 'dev',
      verifyPayloadOnly: true,
    });
    expect(resolveBuildGoPolicy({
      ...validCIWindowsSmokeEnvironment,
      APP_BUILD_PROFILE: undefined,
    })).toMatchObject({
      profile: 'default',
      appVersion: '0.0.0',
      requireAuthenticode: true,
      verificationAppVersion: '0.0.0',
      verifyPayloadOnly: false,
    });
    expect(resolveBuildGoPolicy({
      APP_VERSION: '1.2.3',
      APP_COMMIT: 'release-commit',
      APP_UPDATE_API_URL: 'https://updates.example.test',
      APP_UPDATE_PUBLISHER: ' CN=Release ',
    })).toMatchObject({
      profile: 'default',
      appVersion: '1.2.3',
      appCommit: 'release-commit',
      updatePublisher: 'CN=Release',
      requireAuthenticode: true,
      verificationAppVersion: '1.2.3',
      verifyPayloadOnly: false,
    });
  });
});

describe('build-go update API configuration', () => {
  it('allows a blank update API URL only for development builds', () => {
    expect(resolveUpdateAPIBaseURLHex('dev', '')).toBe('');
    expect(() => resolveUpdateAPIBaseURLHex('1.2.3', '')).toThrow(/requires APP_UPDATE_API_URL/);
  });

  it('embeds the validated canonical origin as UTF-8 hex', () => {
    const expected = Buffer.from('https://updates.example.test:8443', 'utf8').toString('hex');
    expect(resolveUpdateAPIBaseURLHex('1.2.3', 'https://updates.example.test:8443/')).toBe(expected);
    expect(resolveUpdateAPIBaseURLHex('dev', 'https://123.updates.example.test'))
      .toBe(Buffer.from('https://123.updates.example.test', 'utf8').toString('hex'));
  });

  it.each([
    ['non-HTTPS URL', 'http://updates.example.test'],
    ['credentials', 'https://user:password@updates.example.test'],
    ['empty userinfo', 'https://@updates.example.test'],
    ['query', 'https://updates.example.test?channel=stable'],
    ['empty query', 'https://updates.example.test?'],
    ['fragment', 'https://updates.example.test#stable'],
    ['path', 'https://updates.example.test/releases'],
    ['dot path', 'https://updates.example.test/.'],
    ['dot segments', 'https://updates.example.test/releases/../'],
    ['escaped dot path', 'https://updates.example.test/%2e'],
    ['escaped slash path', 'https://updates.example.test/%2F'],
    ['hostless port', 'https://:443'],
    ['empty port', 'https://updates.example.test:'],
    ['non-numeric port', 'https://updates.example.test:invalid'],
    ['zero port', 'https://updates.example.test:0'],
    ['out of range port', 'https://updates.example.test:65536'],
    ['default port spelling', 'https://updates.example.test:443'],
    ['noncanonical port spelling', 'https://updates.example.test:065535'],
    ['uppercase host', 'https://UPDATES.example.test'],
    ['Unicode host', 'https://例子.测试'],
    ['canonical IPv4 host', 'https://127.0.0.1'],
    ['noncanonical IPv4 host', 'https://127.1'],
    ['expanded IPv6 host', 'https://[0:0:0:0:0:0:0:1]'],
    ['compressed IPv6 host', 'https://[::1]'],
  ])('rejects %s', (_label, value) => {
    expect(() => resolveUpdateAPIBaseURLHex('dev', value)).toThrow(/APP_UPDATE_API_URL/);
  });
});

describe('build-go update trust configuration', () => {
  it('rejects a trust-enabled release without root and bootstrap policy', async () => {
    await expect(resolveGoLdflags({
      APP_UPDATE_TRUST_REQUIRED: '1',
      APP_UPDATE_TRUST_ROOT_SPKI_B64: '',
      APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64: '',
    })).rejects.toThrow('update trust root and bootstrap policy are required');
  });

  it('rejects invalid trust public inputs before invoking Go', async () => {
    await expect(resolveGoLdflags({
      APP_UPDATE_TRUST_REQUIRED: '1',
      APP_UPDATE_TRUST_ROOT_SPKI_B64: 'not base64',
      APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64: testTrustBootstrapPolicyBase64,
    })).rejects.toThrow('APP_UPDATE_TRUST_ROOT_SPKI_B64 must be canonical Base64 P-256 SPKI');
    await expect(resolveGoLdflags({
      APP_UPDATE_TRUST_REQUIRED: '1',
      APP_UPDATE_TRUST_ROOT_SPKI_B64: testTrustRootSPKIBase64,
      APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64: 'not base64',
    })).rejects.toThrow('APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64 must be canonical Base64');
  });

  it('embeds valid test-only trust values with decoded SHA-256 audit material', async () => {
    const resolved = await resolveGoLdflags({
      APP_UPDATE_TRUST_REQUIRED: '1',
      APP_UPDATE_TRUST_ROOT_SPKI_B64: testTrustRootSPKIBase64,
      APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64: testTrustBootstrapPolicyBase64,
    });

    expect(resolved.ldflags).toContain(`-X main.updateTrustRootSPKIBase64=${testTrustRootSPKIBase64}`);
    expect(resolved.ldflags).toContain(`-X main.updateTrustBootstrapPolicyBase64=${testTrustBootstrapPolicyBase64}`);
    expect(resolved.trustDigests).toEqual({
      rootSPKISHA256: createHash('sha256').update(Buffer.from(testTrustRootSPKIBase64, 'base64')).digest('hex'),
      bootstrapPolicySHA256: createHash('sha256').update(Buffer.from(testTrustBootstrapPolicyBase64, 'base64')).digest('hex'),
    });
  });

  it('does not print Base64 public inputs in Go trace output', () => {
    const recognizablePolicyBase64 = Buffer.from('task3-fix1-recognizable-bootstrap-policy', 'utf8').toString('base64');
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('../scripts/build-go.mjs', import.meta.url))], {
      cwd: fileURLToPath(new URL('..', import.meta.url)),
      encoding: 'utf8',
      env: {
        ...process.env,
        APP_UPDATE_TRUST_REQUIRED: '1',
        APP_UPDATE_TRUST_ROOT_SPKI_B64: testTrustRootSPKIBase64,
        APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64: recognizablePolicyBase64,
        GOFLAGS: '-x -buildvcs=false',
        GOCACHE: fileURLToPath(new URL('../.cache/go-build', import.meta.url)),
        GOMODCACHE: fileURLToPath(new URL('../.cache/go-mod', import.meta.url)),
      },
    });

    const output = `${result.stdout}${result.stderr}`;
    expect(result.status).toBe(0);
    expect(output).toContain(createHash('sha256').update(Buffer.from(testTrustRootSPKIBase64, 'base64')).digest('hex'));
    expect(output).not.toContain(testTrustRootSPKIBase64);
    expect(output).not.toContain(recognizablePolicyBase64);
    expect(output).toContain('WORK=');
  }, 15_000);

  it('sanitizes every compiler failure attempt before returning the final error', () => {
    const secretRoot = 'recognizable-root-input';
    const secretPolicy = 'recognizable-policy-input';
    let output = '';
    expect(() => runGoCompilerCandidates({
      candidates: ['first-go', 'second-go'],
      args: ['build'],
      cwd: fileURLToPath(new URL('../goserver', import.meta.url)),
      trustInputs: { rootSPKIBase64: secretRoot, bootstrapPolicyBase64: secretPolicy },
      spawn: (candidate: string) => ({ status: 2, stdout: `${candidate}:${secretRoot}`, stderr: `${candidate}:${secretPolicy}` }),
      writeStdout: (value: string) => { output += value; },
      writeStderr: (value: string) => { output += value; },
    })).toThrow(/Go compiler failed/);
    expect(output).toContain('first-go');
    expect(output).toContain('second-go');
    expect(output).not.toContain(secretRoot);
    expect(output).not.toContain(secretPolicy);
    expect(output.match(/\[redacted update trust input\]/g)).toHaveLength(4);
  });

  it('does not require trust material for ordinary local builds', async () => {
    const resolved = await resolveGoLdflags({});

    expect(resolved.trustDigests).toBeNull();
    expect(resolved.ldflags).toContain('-X main.updateTrustRootSPKIBase64=');
    expect(resolved.ldflags).toContain('-X main.updateTrustBootstrapPolicyBase64=');
  });

  it('rejects unreviewed release publishers before compilation', async () => {
    await expect(resolveGoLdflags({
      APP_VERSION: '1.2.3',
      APP_UPDATE_API_URL: 'https://updates.example.test',
      APP_UPDATE_PUBLISHER: 'Unreviewed Publisher',
    })).rejects.toThrow('APP_UPDATE_PUBLISHER is not a reviewed update publisher');
  });
});
