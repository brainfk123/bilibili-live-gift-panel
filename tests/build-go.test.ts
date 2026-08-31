import { Buffer } from 'node:buffer';
import { describe, expect, it } from 'vitest';
import { resolveBuildGoPolicy } from '../scripts/build-go-policy.mjs';
import { resolveUpdateAPIBaseURLHex } from '../scripts/update-api-build-config.mjs';

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
