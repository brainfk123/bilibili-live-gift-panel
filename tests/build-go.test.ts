import { Buffer } from 'node:buffer';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { resolveGoLdflags } from '../scripts/build-go.mjs';
import { resolveUpdateAPIBaseURLHex } from '../scripts/update-api-build-config.mjs';

const testTrustRootSPKIBase64 = readFileSync(
  new URL('../goserver/testdata/update-trust/root-epoch-1-spki.der', import.meta.url),
).toString('base64');
const testTrustBootstrapPolicyBase64 = readFileSync(
  new URL('../goserver/testdata/update-trust/policy-epoch-1.json', import.meta.url),
).toString('base64');

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
      },
    });

    const output = `${result.stdout}${result.stderr}`;
    expect(result.status).toBe(0);
    expect(output).toContain(createHash('sha256').update(Buffer.from(testTrustRootSPKIBase64, 'base64')).digest('hex'));
    expect(output).not.toContain(testTrustRootSPKIBase64);
    expect(output).not.toContain(recognizablePolicyBase64);
    expect(output).toContain('WORK=');
  }, 15_000);

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
