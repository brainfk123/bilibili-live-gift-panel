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

const expectedSigner = 'CN=Release Test';

describe('FFmpeg component policy', () => {
  it('serializes a stable canonical descriptor and tag', () => {
    expect(serializeFFmpegDescriptor(fixture, expectedSigner).toString('utf8')).toBe(
      'schema=2\nffmpeg_version=9.0\n' +
      `source_sha256=${'a'.repeat(64)}\nsource_date_epoch=123\n` +
      `configure_sha256=${'b'.repeat(64)}\n` +
      `toolchain_lock_sha256=${'c'.repeat(64)}\n` +
      'signer_subject_sha256=bbc8caef63880ad52a5a71f7c0e1b7d9fbde18719e0265c1cedeaac6c8cf121b\n' +
      '[components]\nA\nB\n[infrastructure]\nD3D11VA\nMEDIAFOUNDATION\n',
    );
    expect(ffmpegComponentIdentity(fixture, expectedSigner)).toMatchObject({
      descriptorSha256: '83802c2b1499c904a4ffcf771845875e9c7c6513e02cf8381a7b6aa3d76c3a50',
      fingerprint: '83802c2b1499c904a4ffcf771845875e9c7c6513e02cf8381a7b6aa3d76c3a50',
      tag: 'ffmpeg-component-v2-83802c2b1499c904a4ffcf771845875e9c7c6513e02cf8381a7b6aa3d76c3a50',
    });
  });

  it('changes identity when the expected Authenticode signer changes', () => {
    expect(ffmpegComponentIdentity(fixture, 'CN=NaisNet').fingerprint)
      .not.toBe(ffmpegComponentIdentity(fixture, 'CN=RushRush').fingerprint);
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
    expect(ffmpegComponentIdentity({ ...fixture, ...change }, expectedSigner).fingerprint)
      .not.toBe(ffmpegComponentIdentity(fixture, expectedSigner).fingerprint);
  });

  it('rejects non-canonical or malformed policy values', () => {
    expect(() => serializeFFmpegDescriptor({ ...fixture, components: ['B', 'A'] }, expectedSigner)).toThrow(/sorted/);
    expect(() => serializeFFmpegDescriptor({ ...fixture, components: ['A', 'A'] }, expectedSigner)).toThrow(/duplicated/);
    expect(() => serializeFFmpegDescriptor({ ...fixture, sourceSha256: 'bad' }, expectedSigner)).toThrow(/source SHA-256/);
    expect(() => serializeFFmpegDescriptor({ ...fixture, sourceDateEpoch: '1.5' }, expectedSigner)).toThrow(/epoch/);
    expect(() => serializeFFmpegDescriptor(fixture, 'CN=Bad\nSigner')).toThrow(/signer subject/);
  });
});
