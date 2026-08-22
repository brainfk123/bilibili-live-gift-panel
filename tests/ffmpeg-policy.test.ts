import { describe, expect, it } from 'vitest';
import {
  ffmpegComponentIdentity,
  serializeFFmpegDescriptor,
  type FFmpegPolicy,
} from '../scripts/ffmpeg-policy.mjs';

const fixture: FFmpegPolicy = {
  schema: 1,
  version: '9.0',
  sourceSha256: 'a'.repeat(64),
  sourceDateEpoch: '123',
  configureFlags: ['--disable-everything'],
  configureSha256: 'b'.repeat(64),
  toolchainLock: {
    schema: 1,
    source: 'https://repo.msys2.org',
    packages: [],
    executables: { gcc: 'gcc fixture', ld: 'ld fixture', make: 'make fixture' },
  },
  toolchainLockBytes: Buffer.from('fixture\n'),
  toolchainLockSha256: 'c'.repeat(64),
  components: ['A', 'B'],
  infrastructure: ['D3D11VA', 'MEDIAFOUNDATION'],
};

describe('FFmpeg component policy', () => {
  it('serializes a stable canonical descriptor and tag', () => {
    expect(serializeFFmpegDescriptor(fixture).toString('utf8')).toBe(
      'schema=1\nffmpeg_version=9.0\n' +
      `source_sha256=${'a'.repeat(64)}\nsource_date_epoch=123\n` +
      `configure_sha256=${'b'.repeat(64)}\n` +
      `toolchain_lock_sha256=${'c'.repeat(64)}\n` +
      '[components]\nA\nB\n[infrastructure]\nD3D11VA\nMEDIAFOUNDATION\n',
    );
    expect(ffmpegComponentIdentity(fixture)).toMatchObject({
      descriptorSha256: '256712f90a56797f830e67e698d3c49c4b0e1cb47299de80f062f9aef0e5b81c',
      fingerprint: '256712f90a56797f830e67e698d3c49c4b0e1cb47299de80f062f9aef0e5b81c',
      tag: 'ffmpeg-component-v1-256712f90a56797f830e67e698d3c49c4b0e1cb47299de80f062f9aef0e5b81c',
    });
  });

  it.each([
    ['version', { version: '9.1' }],
    ['source hash', { sourceSha256: 'd'.repeat(64) }],
    ['source epoch', { sourceDateEpoch: '124' }],
    ['configure hash', { configureSha256: 'e'.repeat(64) }],
    ['toolchain hash', { toolchainLockSha256: 'f'.repeat(64) }],
    ['components', { components: ['A', 'C'] }],
    ['infrastructure', { infrastructure: ['MEDIAFOUNDATION'] }],
  ])('changes identity when %s changes', (_label, change) => {
    expect(ffmpegComponentIdentity({ ...fixture, ...change }).fingerprint)
      .not.toBe(ffmpegComponentIdentity(fixture).fingerprint);
  });

  it('rejects non-canonical or malformed policy values', () => {
    expect(() => serializeFFmpegDescriptor({ ...fixture, components: ['B', 'A'] })).toThrow(/sorted/);
    expect(() => serializeFFmpegDescriptor({ ...fixture, components: ['A', 'A'] })).toThrow(/duplicated/);
    expect(() => serializeFFmpegDescriptor({ ...fixture, sourceSha256: 'bad' })).toThrow(/source SHA-256/);
    expect(() => serializeFFmpegDescriptor({ ...fixture, sourceDateEpoch: '1.5' })).toThrow(/epoch/);
  });
});
