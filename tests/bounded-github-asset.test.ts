import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { downloadBoundedGitHubAsset } from '../scripts/bounded-github-asset.mjs';

const digest = (value: Buffer) => createHash('sha256').update(value).digest('hex');

describe('bounded GitHub Release asset downloader', () => {
  it('validates metadata first, strips authorization on 302, streams within cap, and post-hashes', async () => {
    const body = Buffer.from('{"policy":true}');
    const calls: Array<{ url: string; authorization?: string }> = [];
    const fetchImpl: typeof fetch = async (input, init) => {
      const url=String(input);
      calls.push({ url, authorization: new Headers(init?.headers).get('Authorization') ?? undefined });
      if (calls.length === 1) return new Response(null, { status: 302, headers: { location: 'https://objects.githubusercontent.com/release/policy.json' } });
      return new Response(body, { status: 200, headers: { 'content-type': 'application/json', 'content-length': String(body.length) } });
    };
    const downloaded = await downloadBoundedGitHubAsset({
      apiURL: 'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/1001', token: 'secret',
      expectedSize: body.length, expectedSHA256: digest(body), expectedContentType: 'application/json', maximumBytes: 256 << 10, fetchImpl,
    });
    expect(downloaded).toEqual(body);
    expect(calls).toEqual([
      { url: expect.stringContaining('api.github.com/'), authorization: 'Bearer secret' },
      { url: expect.stringContaining('objects.githubusercontent.com/'), authorization: undefined },
    ]);
  });

  const invalidCases: Array<[string, Partial<{ expectedSize:number; expectedSHA256:string; expectedContentType:'application/json'|'application/octet-stream'|'text/plain' }>]> = [
    ['oversize metadata', { expectedSize: (256 << 10) + 1 }],
    ['wrong digest', { expectedSHA256: '0'.repeat(64) }],
    ['wrong content type', { expectedContentType: 'text/plain' }],
  ];
  it.each(invalidCases)('rejects %s', async (_name, override) => {
    const body = Buffer.from('{}');
    await expect(downloadBoundedGitHubAsset({
      apiURL: 'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/1001', token: 'secret',
      expectedSize: body.length, expectedSHA256: digest(body), expectedContentType: 'application/json', maximumBytes: 256 << 10,
      fetchImpl: async () => new Response(body, { status: 200, headers: { 'content-type': 'application/json', 'content-length': String(body.length) } }),
      ...override,
    })).rejects.toThrow(/bounded GitHub asset download failed/);
  });
});
