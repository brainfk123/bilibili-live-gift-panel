import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';
import {
  REQUIRED_COMPONENT_ASSETS,
  buildChecksumManifest,
  installComponentAssets,
  prepareComponentAssets,
  writePreparedComponentAssets,
  verifyChecksumManifest,
  verifyComponentMetadata,
  verifyPinnedSourceAssets,
  verifyGitHubReleaseMetadata,
  verifyComponentAssets,
} from '../scripts/ffmpeg-component-assets.mjs';
import { FFMPEG_FIXED_COMPONENT_SIGNER_SUBJECT, FFMPEG_FIXED_COMPONENT_TAG, ffmpegComponentIdentity, loadFFmpegPolicy } from '../scripts/ffmpeg-policy.mjs';

const roots: string[] = [];
afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function temporaryRoot() {
  const root = mkdtempSync(join(tmpdir(), 'ffmpeg-component-assets-'));
  roots.push(root);
  return root;
}

function gitShow(projectRoot: string, revisionPath: string): Buffer {
  const safeRoot = projectRoot.replace(/[\\/]$/, '').replace(/\\/g, '/');
  const shown = spawnSync('git', ['-c', `safe.directory=${safeRoot}`, 'show', revisionPath], {
    cwd: projectRoot,
    encoding: null,
    maxBuffer: 8 * 1024 * 1024,
  });
  expect(shown.status, shown.stderr?.toString()).toBe(0);
  return shown.stdout;
}

function gitRevParse(projectRoot: string, revision: string): string {
  const safeRoot = projectRoot.replace(/[\\/]$/, '').replace(/\\/g, '/');
  const parsed = spawnSync('git', ['-c', `safe.directory=${safeRoot}`, 'rev-parse', revision], {
    cwd: projectRoot,
    encoding: 'utf8',
  });
  expect(parsed.status, parsed.stderr).toBe(0);
  return parsed.stdout.trim();
}

async function v0410ComponentFixture() {
  const projectRoot = fileURLToPath(new URL('..', import.meta.url));
  const targetRoot = temporaryRoot();
  const toolRoot = temporaryRoot();
  const inputDirectory = join(temporaryRoot(), 'component');
  mkdirSync(inputDirectory, { recursive: true });

  for (const relative of [
    'goserver/ffmpeg/ffmpeg.zip',
    'goserver/ffmpeg/manifest.json',
    'third_party/ffmpeg/configure.flags',
    'third_party/ffmpeg/toolchain-lock.json',
    'third_party/ffmpeg/NOTICE.md',
    'third_party/ffmpeg/COPYING.LGPLv2.1',
  ]) {
    const target = join(targetRoot, relative);
    mkdirSync(dirname(target), { recursive: true });
    writeFileSync(target, gitShow(projectRoot, `v0.4.10:${relative}`));
  }

  const reviewedLog = join(toolRoot, 'reviewed-verifier.log');
  const inspectorLog = join(toolRoot, 'shared-inspector.log');
  const inspectorPath = join(toolRoot, 'artifact-inspector-fake.mjs');
  const targetLog = join(targetRoot, 'target-verifier.log');
  mkdirSync(join(toolRoot, 'scripts'), { recursive: true });
  mkdirSync(join(toolRoot, 'third_party', 'ffmpeg'), { recursive: true });
  mkdirSync(join(targetRoot, 'scripts'), { recursive: true });
  for (const relative of ['third_party/ffmpeg/configure.flags', 'third_party/ffmpeg/toolchain-lock.json']) {
    copyFileSync(join(projectRoot, relative), join(toolRoot, relative));
  }
  writeFileSync(inspectorPath, `
import { appendFileSync } from 'node:fs';
const args = process.argv.slice(2);
if (args.join('\0') !== ['authenticode','--file','fixture-ffmpeg.exe','--country','CN','--organization','NaisNet Technology Co., Ltd.','--organization-id','91210103MA7CJ3C094'].join('\0')) process.exit(92);
appendFileSync(${JSON.stringify(inspectorLog)}, 'inspected\\n');
`);
  writeFileSync(join(toolRoot, 'scripts', 'verify-ffmpeg.mjs'), `
import { appendFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
if (!process.env.AUTHENTICODE_INSPECTOR_PATH) process.exit(93);
execFileSync(process.execPath, [process.env.AUTHENTICODE_INSPECTOR_PATH, 'authenticode', '--file', 'fixture-ffmpeg.exe', '--country', 'CN', '--organization', 'NaisNet Technology Co., Ltd.', '--organization-id', '91210103MA7CJ3C094']);
appendFileSync(${JSON.stringify(reviewedLog)}, 'reviewed\\n');
`);
  writeFileSync(join(targetRoot, 'scripts', 'verify-ffmpeg.mjs'),
    `import { appendFileSync } from 'node:fs'; appendFileSync(${JSON.stringify(targetLog)}, 'target\\n'); process.exitCode = 91;\n`);

  const source = Buffer.from('bounded source fixture derived for v0.4.10 target verification');
  const signature = Buffer.from('bounded detached signature fixture');
  const policy = {
    ...(await loadFFmpegPolicy(toolRoot)),
    sourceSha256: createHash('sha256').update(source).digest('hex'),
  };
  const expectedSigner = FFMPEG_FIXED_COMPONENT_SIGNER_SUBJECT;
  const identity = ffmpegComponentIdentity(policy, expectedSigner);
  const commit = gitRevParse(projectRoot, `refs/tags/${FFMPEG_FIXED_COMPONENT_TAG}`);
  const componentArchive = Buffer.from('separate fixed signed component archive fixture');
  const componentGate = Buffer.from('separate fixed signed component gate fixture\n');
  const manifest = Buffer.from(`${JSON.stringify({
    schema: 1,
    component_fingerprint: identity.fingerprint,
    descriptor: identity.descriptor.toString('utf8'),
    descriptor_sha256: identity.descriptorSha256,
    version: '9.0',
    sha256: '1'.repeat(64),
    archive_sha256: createHash('sha256').update(componentArchive).digest('hex'),
    component_gate: componentGate.toString('utf8'),
    component_gate_sha256: createHash('sha256').update(componentGate).digest('hex'),
    size: 1,
    authenticode: true,
    signer_subject: expectedSigner,
    source_release_commit: commit,
  }, null, 2)}\n`);
  const files = new Map<string, Buffer>([
    ['ffmpeg.zip', componentArchive],
    ['manifest.json', manifest],
    ['gift-clip-test-tools.zip', Buffer.from('v0.4.10 test tools fixture')],
    ['ffmpeg-9.0.tar.xz', source],
    ['ffmpeg-9.0.tar.xz.asc', signature],
    ['ffmpeg-build-config.txt', Buffer.from('v0.4.10 reviewed build config fixture\n')],
    ['ffmpeg-component-gate.txt', componentGate],
    ['toolchain-lock.json', readFileSync(join(targetRoot, 'third_party', 'ffmpeg', 'toolchain-lock.json'))],
    ['NOTICE.md', readFileSync(join(targetRoot, 'third_party', 'ffmpeg', 'NOTICE.md'))],
    ['COPYING.LGPLv2.1', readFileSync(join(targetRoot, 'third_party', 'ffmpeg', 'COPYING.LGPLv2.1'))],
  ]);
  await writePreparedComponentAssets(inputDirectory, files);

  return {
    projectRoot,
    targetRoot,
    inputDirectory,
    identity,
    reviewedLog,
    inspectorLog,
    inspectorPath,
    targetLog,
    options: {
      projectRoot: targetRoot,
      toolRoot,
      inputDirectory,
      expectedSigner,
      loadPolicy: async () => policy,
      sourceSignatureSHA256: createHash('sha256').update(signature).digest('hex'),
      publishPair: async (directory: string, archive: Buffer, installedManifest: Buffer) => {
        mkdirSync(directory, { recursive: true });
        writeFileSync(join(directory, 'ffmpeg.zip'), archive);
        writeFileSync(join(directory, 'manifest.json'), installedManifest);
      },
    },
  };
}

describe('FFmpeg component assets', () => {
  it('resolves the reviewed checked-in component without a Subject environment variable', () => {
    const projectRoot = fileURLToPath(new URL('..', import.meta.url));
    const environment = { ...process.env };
    delete environment.EVSIGN_EXPECTED_SUBJECT;
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('../scripts/ffmpeg-component-assets.mjs', import.meta.url)), 'identity', '--kind', 'fixed'], {
      cwd: projectRoot,
      encoding: 'utf8',
      env: environment,
    });

    expect(result.status, result.stderr).toBe(0);
    expect(JSON.parse(result.stdout)).toEqual({
      schema: 2,
      fingerprint: '2603fa9f68855ead324fe3b4ee13c9daaed984b151aae338eeca15aaee71a9c4',
      tag: 'ffmpeg-component-v2-2603fa9f68855ead324fe3b4ee13c9daaed984b151aae338eeca15aaee71a9c4',
    });
    expect(gitRevParse(projectRoot, 'refs/tags/ffmpeg-component-v2-2603fa9f68855ead324fe3b4ee13c9daaed984b151aae338eeca15aaee71a9c4'))
      .toBe('423a26e08c22eb651252d186a7c5a8ea748cece7');
    expect(`${result.stdout}${result.stderr}`).not.toContain('EVSIGN_EXPECTED_SUBJECT');
  });

  it('uses reviewed tooling to verify and install a real v0.4.10 target fixture without target scripts or Subject env', async () => {
    const fixture = await v0410ComponentFixture();
    const untouchedHistoricalManifest = gitShow(fixture.projectRoot, 'v0.4.10:goserver/ffmpeg/manifest.json');
    expect(readFileSync(join(fixture.targetRoot, 'goserver', 'ffmpeg', 'manifest.json')))
      .toEqual(untouchedHistoricalManifest);
    expect(JSON.parse(untouchedHistoricalManifest.toString('utf8'))).toMatchObject({
      authenticode: false,
      signer_subject: '',
    });
    const priorSubject = process.env.EVSIGN_EXPECTED_SUBJECT;
    const priorInspector = process.env.AUTHENTICODE_INSPECTOR_PATH;
    delete process.env.EVSIGN_EXPECTED_SUBJECT;
    process.env.AUTHENTICODE_INSPECTOR_PATH = fixture.inspectorPath;
    try {
      const identityCLI = spawnSync(process.execPath, [
        join(fixture.projectRoot, 'scripts', 'ffmpeg-component-assets.mjs'),
        'identity', '--kind', 'fixed', '--tool-root', fixture.projectRoot,
      ], {
        encoding: 'utf8',
        env: { ...process.env, RELEASE_TARGET_ROOT: fixture.targetRoot, RELEASE_TOOL_ROOT: fixture.projectRoot },
      });
      expect(identityCLI.status, identityCLI.stderr).toBe(0);
      expect(JSON.parse(identityCLI.stdout)).toEqual({
        schema: 2,
        fingerprint: '2603fa9f68855ead324fe3b4ee13c9daaed984b151aae338eeca15aaee71a9c4',
        tag: 'ffmpeg-component-v2-2603fa9f68855ead324fe3b4ee13c9daaed984b151aae338eeca15aaee71a9c4',
      });
      await expect(verifyComponentAssets(fixture.options)).resolves.toMatchObject({ fingerprint: fixture.identity.fingerprint });
      expect(readFileSync(join(fixture.targetRoot, 'goserver', 'ffmpeg', 'manifest.json')))
        .toEqual(untouchedHistoricalManifest);
      await expect(installComponentAssets(fixture.options)).resolves.toMatchObject({ fingerprint: fixture.identity.fingerprint });
    } finally {
      if (priorSubject === undefined) delete process.env.EVSIGN_EXPECTED_SUBJECT;
      else process.env.EVSIGN_EXPECTED_SUBJECT = priorSubject;
      if (priorInspector === undefined) delete process.env.AUTHENTICODE_INSPECTOR_PATH;
      else process.env.AUTHENTICODE_INSPECTOR_PATH = priorInspector;
    }

    expect(readFileSync(fixture.reviewedLog, 'utf8')).toBe('reviewed\nreviewed\n');
    expect(readFileSync(fixture.inspectorLog, 'utf8')).toBe('inspected\ninspected\n');
    expect(existsSync(fixture.targetLog)).toBe(false);
    expect(readFileSync(join(fixture.targetRoot, 'goserver', 'ffmpeg', 'ffmpeg.zip')))
      .toEqual(readFileSync(join(fixture.inputDirectory, 'ffmpeg.zip')));
    expect(readFileSync(join(fixture.targetRoot, 'goserver', 'ffmpeg', 'manifest.json')))
      .toEqual(readFileSync(join(fixture.inputDirectory, 'manifest.json')));
  });

  it.each(['ffmpeg.zip', 'manifest.json'] as const)(
    'installs the exact verified %s snapshot when the downloaded source is swapped after verification',
    async (swappedAsset) => {
      const fixture = await v0410ComponentFixture();
      const verifiedArchive = readFileSync(join(fixture.inputDirectory, 'ffmpeg.zip'));
      const verifiedManifest = readFileSync(join(fixture.inputDirectory, 'manifest.json'));
      let installedArchive: Buffer | undefined;
      let installedManifest: Buffer | undefined;
      const options = {
        ...fixture.options,
        verifyPayload: async (snapshotDirectory: string) => {
          expect(snapshotDirectory).not.toBe(fixture.inputDirectory);
          expect(readFileSync(join(snapshotDirectory, 'ffmpeg.zip'))).toEqual(verifiedArchive);
          expect(readFileSync(join(snapshotDirectory, 'manifest.json'))).toEqual(verifiedManifest);
          writeFileSync(join(fixture.inputDirectory, swappedAsset), Buffer.from(`attacker ${swappedAsset}`));
        },
        publishPair: async (directory: string, archive: Buffer, manifest: Buffer) => {
          installedArchive = Buffer.from(archive);
          installedManifest = Buffer.from(manifest);
          writeFileSync(join(directory, 'ffmpeg.zip'), archive);
          writeFileSync(join(directory, 'manifest.json'), manifest);
        },
      };

      await installComponentAssets(options);
      expect(installedArchive).toEqual(verifiedArchive);
      expect(installedManifest).toEqual(verifiedManifest);
    },
    20_000,
  );

  it('writes the exact verified manifest snapshot when the downloaded manifest changes after verification', async () => {
    const fixture = await v0410ComponentFixture();
    const verifiedManifest = readFileSync(join(fixture.inputDirectory, 'manifest.json'));
    const outputDirectory = join(fixture.targetRoot, 'dist');
    const manifestOutputPath = join(outputDirectory, 'standalone-component-manifest.json');
    mkdirSync(outputDirectory, { recursive: true });
    await verifyComponentAssets({
      ...fixture.options,
      manifestOutputPath,
      verifyPayload: async () => {
        writeFileSync(join(fixture.inputDirectory, 'manifest.json'), Buffer.from('attacker manifest'));
      },
    });
    expect(readFileSync(manifestOutputPath)).toEqual(verifiedManifest);
  });

  it.each(['project', 'goserver', 'destination'] as const)(
    'rejects a junction at the %s installation boundary before publishing',
    async (boundary) => {
      const fixture = await v0410ComponentFixture();
      let projectRoot = fixture.targetRoot;
      const outside = temporaryRoot();
      let link: string;
      let target: string;
      if (boundary === 'project') {
        link = join(temporaryRoot(), 'project-link');
        target = fixture.targetRoot;
        projectRoot = link;
      } else if (boundary === 'goserver') {
        link = join(fixture.targetRoot, 'goserver');
        target = join(outside, 'goserver');
        mkdirSync(join(target, 'ffmpeg'), { recursive: true });
        rmSync(link, { recursive: true, force: true });
      } else {
        link = join(fixture.targetRoot, 'goserver', 'ffmpeg');
        target = join(outside, 'ffmpeg');
        mkdirSync(target, { recursive: true });
        rmSync(link, { recursive: true, force: true });
      }
      try {
        symlinkSync(target, link, process.platform === 'win32' ? 'junction' : 'dir');
      } catch {
        return;
      }
      let published = false;
      await expect(installComponentAssets({
        ...fixture.options,
        projectRoot,
        verifyPayload: async () => undefined,
        publishPair: async () => { published = true; },
      })).rejects.toThrow(/destination|reparse|symbolic/i);
      expect(published).toBe(false);
    },
  );

  it('rejects lexical traversal in the installation project before publishing', async () => {
    const fixture = await v0410ComponentFixture();
    let published = false;
    await expect(installComponentAssets({
      ...fixture.options,
      projectRoot: `${fixture.targetRoot}${process.platform === 'win32' ? '\\' : '/'}goserver${process.platform === 'win32' ? '\\' : '/'}..`,
      verifyPayload: async () => undefined,
      publishPair: async () => { published = true; },
    })).rejects.toThrow(/traversal/i);
    expect(published).toBe(false);
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
