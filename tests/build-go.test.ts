import { Buffer } from 'node:buffer';
import { describe, expect, it } from 'vitest';
import { resolveUpdateAPIBaseURLHex } from '../scripts/update-api-build-config.mjs';

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
