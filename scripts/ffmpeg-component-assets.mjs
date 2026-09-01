import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { lstat, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { FFMPEG_SOURCE_SIGNATURE_SHA256, ffmpegComponentIdentity, loadFFmpegPolicy } from './ffmpeg-policy.mjs';
import { publishPairTransactionally } from './package-ffmpeg.mjs';

export const REQUIRED_COMPONENT_ASSETS = Object.freeze([
  'ffmpeg.zip',
  'manifest.json',
  'gift-clip-test-tools.zip',
  'ffmpeg-9.0.tar.xz',
  'ffmpeg-9.0.tar.xz.asc',
  'ffmpeg-build-config.txt',
  'ffmpeg-component-gate.txt',
  'toolchain-lock.json',
  'NOTICE.md',
  'COPYING.LGPLv2.1',
]);

const CHECKSUM_ASSET = 'SHA256SUMS.txt';
const root = process.env.RELEASE_TARGET_ROOT ? resolve(process.env.RELEASE_TARGET_ROOT) : resolve(dirname(fileURLToPath(import.meta.url)), '..');

export function buildChecksumManifest(files) {
  if (!(files instanceof Map)) throw new Error('FFmpeg component files must be a Map.');
  const actual = [...files.keys()];
  if (actual.length !== REQUIRED_COMPONENT_ASSETS.length || actual.some((name, index) => name !== REQUIRED_COMPONENT_ASSETS[index])) {
    throw new Error('FFmpeg component files are missing or out of canonical order.');
  }
  return Buffer.from(`${REQUIRED_COMPONENT_ASSETS.map((name) => `${sha256(files.get(name))}  ${name}`).join('\n')}\n`);
}

export async function writePreparedComponentAssets(outputDirectory, files) {
  if (!(files instanceof Map)) throw new Error('FFmpeg component files must be a Map.');
  for (const name of REQUIRED_COMPONENT_ASSETS) {
    const bytes = files.get(name);
    if (!Buffer.isBuffer(bytes)) throw new Error(`FFmpeg component asset ${name} is not binary data.`);
    await writeFile(join(outputDirectory, name), bytes, { flag: 'wx' });
  }
  await writeFile(join(outputDirectory, CHECKSUM_ASSET), buildChecksumManifest(files), { flag: 'wx' });
}

export async function verifyChecksumManifest(directory) {
  const names = await readdir(directory);
  const expectedNames = [...REQUIRED_COMPONENT_ASSETS, CHECKSUM_ASSET].sort();
  if (names.length !== expectedNames.length || [...names].sort().some((name, index) => name !== expectedNames[index])) {
    throw new Error('FFmpeg component asset closure has missing or unexpected files.');
  }
  const files = new Map();
  for (const name of REQUIRED_COMPONENT_ASSETS) {
    const path = join(directory, name);
    const info = await lstat(path);
    if (!info.isFile() || info.isSymbolicLink()) throw new Error(`FFmpeg component asset ${name} is not a regular file.`);
    files.set(name, await readFile(path));
  }
  const checksumInfo = await lstat(join(directory, CHECKSUM_ASSET));
  if (!checksumInfo.isFile() || checksumInfo.isSymbolicLink()) throw new Error('FFmpeg checksum asset is not a regular file.');
  const expected = buildChecksumManifest(files);
  const actual = await readFile(join(directory, CHECKSUM_ASSET));
  if (actual.equals(expected)) return;
  const expectedLines = expected.toString('utf8').trimEnd().split('\n');
  const actualLines = actual.toString('utf8').trimEnd().split('\n');
  const expectedSet = [...expectedLines].sort().join('\n');
  const actualSet = [...actualLines].sort().join('\n');
  if (expectedSet === actualSet) throw new Error('FFmpeg component checksum order is invalid.');
  throw new Error('FFmpeg component asset digest is invalid.');
}

export async function verifyGitHubReleaseMetadata(metadata, directory, expectedTag) {
  const requiredNames = [...REQUIRED_COMPONENT_ASSETS, CHECKSUM_ASSET];
  if (!metadata || Object.getPrototypeOf(metadata) !== Object.prototype || metadata.tag_name !== expectedTag || metadata.draft !== false || metadata.prerelease !== false || typeof metadata.published_at !== 'string' || Number.isNaN(Date.parse(metadata.published_at)) || !Array.isArray(metadata.assets) || metadata.assets.length !== requiredNames.length) {
    throw new Error('GitHub FFmpeg component Release metadata is invalid.');
  }
  const seen = new Set();
  for (const asset of metadata.assets) {
    if (!asset || Object.getPrototypeOf(asset) !== Object.prototype || typeof asset.name !== 'string' || !requiredNames.includes(asset.name) || seen.has(asset.name) || !Number.isSafeInteger(asset.size) || asset.size <= 0) {
      throw new Error('GitHub FFmpeg component asset metadata is invalid or duplicated.');
    }
    seen.add(asset.name);
    const bytes = await readFile(join(directory, asset.name));
    if (bytes.length !== asset.size) throw new Error(`GitHub asset size does not match ${asset.name}.`);
    if (asset.digest !== undefined && asset.digest !== null) {
      if (typeof asset.digest !== 'string' || asset.digest !== `sha256:${sha256(bytes)}`) throw new Error(`GitHub asset digest does not match ${asset.name}.`);
    }
  }
  if (requiredNames.some((name) => !seen.has(name))) throw new Error('GitHub FFmpeg component asset closure is incomplete.');
}

export function verifyComponentMetadata(manifest, identity, expectedSigner) {
  if (!manifest || Object.getPrototypeOf(manifest) !== Object.prototype || manifest.schema !== 1) {
    throw new Error('FFmpeg component manifest schema is invalid.');
  }
  if (!identity || !Buffer.isBuffer(identity.descriptor) || manifest.component_fingerprint !== identity.fingerprint || manifest.descriptor_sha256 !== identity.descriptorSha256 || manifest.descriptor !== identity.descriptor.toString('utf8')) {
    throw new Error('FFmpeg component identity does not match local policy.');
  }
  if (sha256(Buffer.from(manifest.descriptor, 'utf8')) !== manifest.descriptor_sha256) {
    throw new Error('FFmpeg component descriptor digest is invalid.');
  }
  if (manifest.authenticode !== true || typeof expectedSigner !== 'string' || expectedSigner.length === 0 || manifest.signer_subject !== expectedSigner) {
    throw new Error('FFmpeg component signer does not match the expected signer.');
  }
  if (!/^[0-9a-f]{40}$/.test(manifest.source_release_commit) || /^0{40}$/.test(manifest.source_release_commit)) {
    throw new Error('FFmpeg component source release commit is invalid.');
  }
}

export function verifyPinnedSourceAssets(archive, signature, policy) {
  if (sha256(archive) !== policy.sourceSha256) throw new Error('FFmpeg source archive does not match the pinned SHA-256.');
  if (sha256(signature) !== policy.sourceSignatureSha256) throw new Error('FFmpeg detached signature does not match the pinned SHA-256.');
}

export async function prepareComponentAssets({ projectRoot = root, outputDirectory, expectedSigner }) {
  const sources = componentSourcePaths(projectRoot);
  assertContainedDirectory(resolve(projectRoot, 'dist'), outputDirectory, 'FFmpeg component output');
  const manifestPath = sources.get('manifest.json');
  const manifestInfo = await lstat(manifestPath);
  if (!manifestInfo.isFile() || manifestInfo.isSymbolicLink()) throw new Error('FFmpeg component source manifest.json is not a regular file.');
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'));
  const identity = ffmpegComponentIdentity(await loadFFmpegPolicy(projectRoot), expectedSigner);
  verifyComponentMetadata(manifest, identity, expectedSigner);
  await rm(outputDirectory, { recursive: true, force: true });
  await mkdir(outputDirectory, { recursive: true });
  const files = new Map();
  for (const name of REQUIRED_COMPONENT_ASSETS) {
    const source = sources.get(name);
    const info = await lstat(source);
    if (!info.isFile() || info.isSymbolicLink()) throw new Error(`FFmpeg component source ${name} is not a regular file.`);
    const bytes = name === 'ffmpeg-component-gate.txt' ? Buffer.from(manifest.component_gate, 'utf8') : await readFile(source);
    files.set(name, bytes);
  }
  await writePreparedComponentAssets(outputDirectory, files);
  return identity;
}

export async function verifyComponentAssets({ projectRoot = root, inputDirectory, expectedSigner, verifyPayload = verifyPayloadDirectory }) {
  await verifyChecksumManifest(inputDirectory);
  const policy = await loadFFmpegPolicy(projectRoot);
  const identity = ffmpegComponentIdentity(policy, expectedSigner);
  let manifest;
  try { manifest = JSON.parse(await readFile(join(inputDirectory, 'manifest.json'), 'utf8')); }
  catch { throw new Error('FFmpeg component manifest is malformed.'); }
  verifyComponentMetadata(manifest, identity, expectedSigner);
  verifyPinnedSourceAssets(
    await readFile(join(inputDirectory, 'ffmpeg-9.0.tar.xz')),
    await readFile(join(inputDirectory, 'ffmpeg-9.0.tar.xz.asc')),
    { sourceSha256: policy.sourceSha256, sourceSignatureSha256: FFMPEG_SOURCE_SIGNATURE_SHA256 },
  );
  for (const [asset, local] of [
    ['toolchain-lock.json', join(projectRoot, 'third_party', 'ffmpeg', 'toolchain-lock.json')],
    ['NOTICE.md', join(projectRoot, 'third_party', 'ffmpeg', 'NOTICE.md')],
    ['COPYING.LGPLv2.1', join(projectRoot, 'third_party', 'ffmpeg', 'COPYING.LGPLv2.1')],
  ]) {
    if (!(await readFile(join(inputDirectory, asset))).equals(await readFile(local))) throw new Error(`FFmpeg component ${asset} differs from checked-in policy.`);
  }
  if (!(await readFile(join(inputDirectory, 'ffmpeg-component-gate.txt'))).equals(Buffer.from(manifest.component_gate, 'utf8'))) {
    throw new Error('FFmpeg component gate asset differs from manifest.');
  }
  await verifyPayload(inputDirectory, expectedSigner);
  return identity;
}

export async function installComponentAssets({ projectRoot = root, inputDirectory, expectedSigner }) {
  const identity = await verifyComponentAssets({ projectRoot, inputDirectory, expectedSigner });
  await publishPairTransactionally(
    join(projectRoot, 'goserver', 'ffmpeg'),
    await readFile(join(inputDirectory, 'ffmpeg.zip')),
    await readFile(join(inputDirectory, 'manifest.json')),
  );
  return identity;
}

function verifyPayloadDirectory(directory, expectedSigner) {
  execFileSync(process.execPath, [join(root, 'scripts', 'verify-ffmpeg.mjs'), '--payload-only', '--payload-directory', directory, '--build-config', join(directory, 'ffmpeg-build-config.txt')], {
    cwd: root,
    env: { ...process.env, APP_VERSION: 'component' },
    stdio: 'inherit',
    windowsHide: true,
  });
}

function componentSourcePaths(projectRoot) {
  return new Map([
    ['ffmpeg.zip', join(projectRoot, 'goserver', 'ffmpeg', 'ffmpeg.zip')],
    ['manifest.json', join(projectRoot, 'goserver', 'ffmpeg', 'manifest.json')],
    ['gift-clip-test-tools.zip', join(projectRoot, 'dist', 'gift-clip-test-tools.zip')],
    ['ffmpeg-9.0.tar.xz', join(projectRoot, 'dist', 'ffmpeg-source', 'ffmpeg-9.0.tar.xz')],
    ['ffmpeg-9.0.tar.xz.asc', join(projectRoot, 'dist', 'ffmpeg-source', 'ffmpeg-9.0.tar.xz.asc')],
    ['ffmpeg-build-config.txt', join(projectRoot, 'dist', 'ffmpeg-build-config.txt')],
    ['ffmpeg-component-gate.txt', join(projectRoot, 'dist', 'ffmpeg-component-gate.txt')],
    ['toolchain-lock.json', join(projectRoot, 'third_party', 'ffmpeg', 'toolchain-lock.json')],
    ['NOTICE.md', join(projectRoot, 'third_party', 'ffmpeg', 'NOTICE.md')],
    ['COPYING.LGPLv2.1', join(projectRoot, 'third_party', 'ffmpeg', 'COPYING.LGPLv2.1')],
  ]);
}

function sha256(value) {
  if (!Buffer.isBuffer(value)) throw new Error('FFmpeg component asset is not binary data.');
  return createHash('sha256').update(value).digest('hex');
}

function assertContainedDirectory(parent, child, label) {
  const resolvedParent = resolve(parent);
  const resolvedChild = resolve(child);
  if (resolvedChild === resolvedParent || !resolvedChild.startsWith(`${resolvedParent}${sep}`)) throw new Error(`${label} must be a child of ${resolvedParent}.`);
}

function argument(name) {
  return resolve(stringArgument(name));
}

function stringArgument(name) {
  const index = process.argv.indexOf(name);
  if (index < 0 || !process.argv[index + 1]) throw new Error(`${name} requires a value.`);
  return process.argv[index + 1];
}

async function main() {
  const command = process.argv[2];
  const signerManifestDirectory = command === 'verify' || command === 'install'
    ? argument('--input')
    : join(root, 'goserver', 'ffmpeg');
  const signerManifest = JSON.parse(await readFile(join(signerManifestDirectory, 'manifest.json'), 'utf8'));
  const expectedSigner = String(signerManifest.signer_subject || '');
  if (signerManifest.authenticode === true && !expectedSigner) throw new Error('Reviewed FFmpeg component signer metadata is missing.');
  if (command === 'identity') {
    const identity = ffmpegComponentIdentity(await loadFFmpegPolicy(root), expectedSigner);
    process.stdout.write(`${JSON.stringify({ schema: 2, fingerprint: identity.fingerprint, tag: identity.tag })}\n`);
    return;
  }
  if (command === 'prepare') await prepareComponentAssets({ outputDirectory: argument('--output'), expectedSigner });
  else if (command === 'verify') await verifyComponentAssets({ inputDirectory: argument('--input'), expectedSigner });
  else if (command === 'install') await installComponentAssets({ inputDirectory: argument('--input'), expectedSigner });
  else if (command === 'verify-metadata') await verifyGitHubReleaseMetadata(JSON.parse(await readFile(argument('--metadata'), 'utf8')), argument('--input'), stringArgument('--tag'));
  else throw new Error('Usage: node scripts/ffmpeg-component-assets.mjs identity|prepare --output <dir>|verify|install --input <dir>|verify-metadata --metadata <json> --input <dir> --tag <tag>');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main();
