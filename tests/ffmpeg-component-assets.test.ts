import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';
import {
  REQUIRED_COMPONENT_ASSETS,
  buildChecksumManifest,
  prepareComponentAssets,
  writePreparedComponentAssets,
  verifyChecksumManifest,
  verifyComponentMetadata,
  verifyPinnedSourceAssets,
  verifyGitHubReleaseMetadata,
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
  it('resolves the reviewed checked-in component without a Subject environment variable', () => {
    const projectRoot = fileURLToPath(new URL('..', import.meta.url));
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('../scripts/ffmpeg-component-assets.mjs', import.meta.url)), 'identity'], {
      cwd: projectRoot,
      encoding: 'utf8',
      env: { ...process.env, EVSIGN_EXPECTED_SUBJECT: '' },
    });

    expect(result.status, result.stderr).toBe(0);
    expect(JSON.parse(result.stdout)).toMatchObject({ schema: 2 });
    expect(`${result.stdout}${result.stderr}`).not.toContain('EVSIGN_EXPECTED_SUBJECT');
  });

  it('rejects mismatched signed metadata before preparing publishable component assets', async () => {
    const root = temporaryRoot();
    for (const relative of [
      'third_party/ffmpeg/configure.flags',
      'third_party/ffmpeg/toolchain-lock.json',
      'third_party/ffmpeg/NOTICE.md',
      'third_party/ffmpeg/COPYING.LGPLv2.1',
    ]) {
      const target = join(root, relative);
      mkdirSync(dirname(target), { recursive: true });
      copyFileSync(fileURLToPath(new URL(`../${relative}`, import.meta.url)), target);
    }
    const manifest = {
      schema: 1,
      component_fingerprint: '0'.repeat(64),
      descriptor: 'wrong identity\n',
      descriptor_sha256: '0'.repeat(64),
      authenticode: true,
      signer_subject: 'CN=Wrong',
      source_release_commit: '1'.repeat(40),
      component_gate: 'gate\n',
    };
    const sources = new Map([
      ['goserver/ffmpeg/ffmpeg.zip', Buffer.from('zip')],
      ['goserver/ffmpeg/manifest.json', Buffer.from(JSON.stringify(manifest))],
      ['dist/gift-clip-test-tools.zip', Buffer.from('tools')],
      ['dist/ffmpeg-source/ffmpeg-9.0.tar.xz', Buffer.from('source')],
      ['dist/ffmpeg-source/ffmpeg-9.0.tar.xz.asc', Buffer.from('signature')],
      ['dist/ffmpeg-build-config.txt', Buffer.from('config')],
      ['dist/ffmpeg-component-gate.txt', Buffer.from('gate\n')],
    ]);
    for (const [relative, bytes] of sources) {
      const target = join(root, relative);
      mkdirSync(dirname(target), { recursive: true });
      writeFileSync(target, bytes);
    }

    await expect(prepareComponentAssets({
      projectRoot: root,
      outputDirectory: join(root, 'dist', 'component'),
      expectedSigner: 'CN=Expected',
    })).rejects.toThrow(/identity|signer/);
  });

  it('writes the exact canonical bytes used by the checksum manifest', async () => {
    const root = temporaryRoot();
    const files = new Map(REQUIRED_COMPONENT_ASSETS.map((name) => [name, Buffer.from(`asset:${name}`)]));
    files.set('ffmpeg-component-gate.txt', Buffer.from('canonical\nbytes\n'));

    await writePreparedComponentAssets(root, files);

    expect(readFileSync(join(root, 'ffmpeg-component-gate.txt'))).toEqual(files.get('ffmpeg-component-gate.txt'));
    await expect(verifyChecksumManifest(root)).resolves.toBeUndefined();
  });

  it('uses one fixed compliance closure and canonical checksum order', () => {
    expect(REQUIRED_COMPONENT_ASSETS).toEqual([
      'ffmpeg.zip', 'manifest.json', 'gift-clip-test-tools.zip', 'ffmpeg-9.0.tar.xz', 'ffmpeg-9.0.tar.xz.asc',
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

  it('binds source and detached signature assets to pinned local hashes', () => {
    const archive = Buffer.from('source archive');
    const signature = Buffer.from('detached signature');
    const policy = {
      sourceSha256: createHash('sha256').update(archive).digest('hex'),
      sourceSignatureSha256: createHash('sha256').update(signature).digest('hex'),
    };
    expect(() => verifyPinnedSourceAssets(archive, signature, policy)).not.toThrow();
    expect(() => verifyPinnedSourceAssets(Buffer.from('other archive'), signature, policy)).toThrow(/source archive/);
    expect(() => verifyPinnedSourceAssets(archive, Buffer.from('other signature'), policy)).toThrow(/detached signature/);
  });

  it('binds published GitHub asset size and available digest to downloaded bytes', async () => {
    const root = temporaryRoot();
    const files = new Map(REQUIRED_COMPONENT_ASSETS.map((name) => [name, Buffer.from(`asset:${name}`)]));
    for (const [name, value] of files) writeFileSync(join(root, name), value);
    writeFileSync(join(root, 'SHA256SUMS.txt'), buildChecksumManifest(files));
    const allFiles = new Map([...files, ['SHA256SUMS.txt', readFileSync(join(root, 'SHA256SUMS.txt'))]]);
    const metadata = {
      tag_name: `ffmpeg-component-v1-${'a'.repeat(64)}`,
      draft: false,
      prerelease: false,
      published_at: '2026-08-22T00:00:00Z',
      assets: [...allFiles].map(([name, bytes]) => ({ name, size: bytes.length, digest: `sha256:${createHash('sha256').update(bytes).digest('hex')}` })),
    };
    await expect(verifyGitHubReleaseMetadata(metadata, root, metadata.tag_name)).resolves.toBeUndefined();
    metadata.assets[0]!.digest = `sha256:${'0'.repeat(64)}`;
    await expect(verifyGitHubReleaseMetadata(metadata, root, metadata.tag_name)).rejects.toThrow(/GitHub asset digest/);
  });
});
