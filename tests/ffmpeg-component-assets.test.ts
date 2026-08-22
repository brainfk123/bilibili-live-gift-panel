import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  REQUIRED_COMPONENT_ASSETS,
  buildChecksumManifest,
  verifyChecksumManifest,
  verifyComponentMetadata,
} from '../scripts/ffmpeg-component-assets.mjs';

const roots: string[] = [];
afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function temporaryRoot() {
  const root = mkdtempSync(join(tmpdir(), 'ffmpeg-component-assets-'));
  roots.push(root);
  return root;
}

describe('FFmpeg component assets', () => {
  it('uses one fixed compliance closure and canonical checksum order', () => {
    expect(REQUIRED_COMPONENT_ASSETS).toEqual([
      'ffmpeg.zip', 'manifest.json', 'ffmpeg-9.0.tar.xz', 'ffmpeg-9.0.tar.xz.asc',
      'ffmpeg-build-config.txt', 'ffmpeg-component-gate.txt', 'toolchain-lock.json',
      'NOTICE.md', 'COPYING.LGPLv2.1',
    ]);
    const files = new Map(REQUIRED_COMPONENT_ASSETS.map((name) => [name, Buffer.from(`asset:${name}`)]));
    const checksums = buildChecksumManifest(files);
    expect(checksums.toString('utf8').split('\n').filter(Boolean).map((line) => line.slice(66)))
      .toEqual(REQUIRED_COMPONENT_ASSETS);
    expect(checksums.toString('utf8')).toMatch(/\n$/);
  });

  it('rejects missing, unexpected, reordered, or mutated checksum assets', async () => {
    const root = temporaryRoot();
    const files = new Map(REQUIRED_COMPONENT_ASSETS.map((name) => [name, Buffer.from(`asset:${name}`)]));
    for (const [name, value] of files) writeFileSync(join(root, name), value);
    writeFileSync(join(root, 'SHA256SUMS.txt'), buildChecksumManifest(files));
    await expect(verifyChecksumManifest(root)).resolves.toBeUndefined();

    writeFileSync(join(root, 'ffmpeg.zip'), Buffer.from('mutated'));
    await expect(verifyChecksumManifest(root)).rejects.toThrow(/digest/);
    writeFileSync(join(root, 'ffmpeg.zip'), files.get('ffmpeg.zip')!);

    const lines = readFileSync(join(root, 'SHA256SUMS.txt'), 'utf8').trimEnd().split('\n');
    writeFileSync(join(root, 'SHA256SUMS.txt'), `${[lines[1], lines[0], ...lines.slice(2)].join('\n')}\n`);
    await expect(verifyChecksumManifest(root)).rejects.toThrow(/order/);
  });

  it('binds signed metadata to the exact local identity and signer', () => {
    const descriptor = 'schema=1\nfixture=true\n';
    const fingerprint = createHash('sha256').update(descriptor).digest('hex');
    const manifest = {
      schema: 1,
      component_fingerprint: fingerprint,
      descriptor,
      descriptor_sha256: fingerprint,
      authenticode: true,
      signer_subject: 'CN=Release Test',
      source_release_commit: '1'.repeat(40),
    };
    expect(() => verifyComponentMetadata(manifest, {
      descriptor: Buffer.from(descriptor), descriptorSha256: fingerprint, fingerprint,
    }, 'CN=Release Test')).not.toThrow();
    expect(() => verifyComponentMetadata({ ...manifest, signer_subject: 'CN=Other' }, {
      descriptor: Buffer.from(descriptor), descriptorSha256: fingerprint, fingerprint,
    }, 'CN=Release Test')).toThrow(/signer/);
    expect(() => verifyComponentMetadata({ ...manifest, component_fingerprint: '0'.repeat(64) }, {
      descriptor: Buffer.from(descriptor), descriptorSha256: fingerprint, fingerprint,
    }, 'CN=Release Test')).toThrow(/identity/);
  });
});
