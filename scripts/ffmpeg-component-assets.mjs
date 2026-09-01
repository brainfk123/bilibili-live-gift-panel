import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmod, lstat, mkdir, mkdtemp, open, readFile, readdir, realpath, rmdir, unlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { FFMPEG_FIXED_COMPONENT_SIGNER_SUBJECT, FFMPEG_SIGNED_COMPONENT_SIGNER, FFMPEG_SOURCE_SIGNATURE_SHA256, ffmpegComponentIdentity, loadFFmpegPolicy, reviewedFixedFFmpegComponentIdentity, reviewedSignedFFmpegComponentIdentity } from './ffmpeg-policy.mjs';

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
const COMPONENT_ASSET_MAXIMUMS = Object.freeze({
  'ffmpeg.zip': 40 << 20,
  'manifest.json': 64 << 10,
  'gift-clip-test-tools.zip': 128 << 20,
  'ffmpeg-9.0.tar.xz': 128 << 20,
  'ffmpeg-9.0.tar.xz.asc': 1 << 20,
  'ffmpeg-build-config.txt': 1 << 20,
  'ffmpeg-component-gate.txt': 64 << 10,
  'toolchain-lock.json': 1 << 20,
  'NOTICE.md': 1 << 20,
  'COPYING.LGPLv2.1': 1 << 20,
  [CHECKSUM_ASSET]: 64 << 10,
});
const root = process.env.RELEASE_TARGET_ROOT ? resolve(process.env.RELEASE_TARGET_ROOT) : resolve(dirname(fileURLToPath(import.meta.url)), '..');
const moduleToolRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const defaultToolRoot = process.env.RELEASE_TOOL_ROOT ? resolve(process.env.RELEASE_TOOL_ROOT) : moduleToolRoot;

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
    await writeFile(join(outputDirectory, name), bytes, { flag: 'wx', mode: 0o600 });
  }
  await writeFile(join(outputDirectory, CHECKSUM_ASSET), buildChecksumManifest(files), { flag: 'wx', mode: 0o600 });
}

export async function verifyChecksumManifest(directory) {
  await readComponentSnapshot(directory);
}

function verifyChecksumSnapshot(files, actual) {
  const expected = buildChecksumManifest(files);
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
  const snapshot = await readComponentSnapshot(directory);
  const allFiles = new Map([...snapshot.files, [CHECKSUM_ASSET, snapshot.checksum]]);
  const seen = new Set();
  for (const asset of metadata.assets) {
    if (!asset || Object.getPrototypeOf(asset) !== Object.prototype || typeof asset.name !== 'string' || !requiredNames.includes(asset.name) || seen.has(asset.name) || !Number.isSafeInteger(asset.size) || asset.size <= 0) {
      throw new Error('GitHub FFmpeg component asset metadata is invalid or duplicated.');
    }
    seen.add(asset.name);
    const bytes = allFiles.get(asset.name);
    if (!Buffer.isBuffer(bytes)) throw new Error(`GitHub asset ${asset.name} is unavailable.`);
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

export async function prepareComponentAssets({ projectRoot = root, toolRoot = defaultToolRoot, outputDirectory, sealedOutputDirectory, expectedSigner = FFMPEG_SIGNED_COMPONENT_SIGNER, sealFFmpegClosure }) {
  requireReviewedSigner(expectedSigner);
  const sources = componentSourcePaths(projectRoot);
  assertContainedDirectory(resolve(projectRoot, 'dist'), outputDirectory, 'FFmpeg component output');
  const sealed = await createSealedFFmpegClosure({ projectRoot, archivePath: sources.get('ffmpeg.zip'), manifestPath: sources.get('manifest.json'), sealedOutputDirectory, sealFFmpegClosure });
  try {
    let manifest;
    try { manifest = JSON.parse(sealed.files.get('manifest.json').toString('utf8')); }
    catch { throw new Error('FFmpeg component source manifest.json is malformed.'); }
    const identity = ffmpegComponentIdentity(await loadFFmpegPolicy(toolRoot), expectedSigner);
    verifyComponentMetadata(manifest, identity, expectedSigner);
    await createExclusiveDirectory(outputDirectory, 'FFmpeg component output');
    const files = new Map();
    for (const name of REQUIRED_COMPONENT_ASSETS) {
      if (name === 'ffmpeg.zip' || name === 'manifest.json') {
        files.set(name, sealed.files.get(name));
        continue;
      }
      const source = sources.get(name);
      const info = await lstat(source);
      if (!info.isFile() || info.isSymbolicLink()) throw new Error(`FFmpeg component source ${name} is not a regular file.`);
      const bytes = name === 'ffmpeg-component-gate.txt' ? Buffer.from(manifest.component_gate, 'utf8') : await readFile(source);
      files.set(name, bytes);
    }
    await writePreparedComponentAssets(outputDirectory, files);
    return identity;
  } finally {
    await sealed.cleanup();
  }
}

export async function verifyComponentAssets({
  projectRoot = root,
  toolRoot = defaultToolRoot,
  inputDirectory,
  manifestOutputPath,
	sealedOutputDirectory,
  expectedSigner = FFMPEG_SIGNED_COMPONENT_SIGNER,
  verifyPayload,
  loadPolicy = loadFFmpegPolicy,
  sourceSignatureSHA256 = FFMPEG_SOURCE_SIGNATURE_SHA256,
	sealFFmpegClosure,
}) {
  const verified = await verifyComponentSnapshot({ projectRoot, toolRoot, inputDirectory, sealedOutputDirectory, expectedSigner, verifyPayload, loadPolicy, sourceSignatureSHA256, sealFFmpegClosure });
  try {
    if (manifestOutputPath) await writeVerifiedManifest(projectRoot, manifestOutputPath, verified.files.get('manifest.json'));
    return verified.identity;
  } finally {
    await verified.cleanup();
  }
}

async function verifyComponentSnapshot({
  projectRoot,
  toolRoot,
  inputDirectory,
	sealedOutputDirectory,
  expectedSigner,
  verifyPayload,
  loadPolicy,
  sourceSignatureSHA256,
	sealFFmpegClosure,
}) {
  requireReviewedSigner(expectedSigner);
  const sealed = await createSealedFFmpegClosure({ projectRoot, archivePath: join(inputDirectory, 'ffmpeg.zip'), manifestPath: join(inputDirectory, 'manifest.json'), sealedOutputDirectory, sealFFmpegClosure });
  let snapshot;
  try {
    snapshot = await readComponentSnapshot(inputDirectory, sealed.files);
  } catch (error) {
    await sealed.cleanup();
    throw error;
  }
  const policy = await loadPolicy(toolRoot);
  const identity = ffmpegComponentIdentity(policy, expectedSigner);
  let manifest;
  try { manifest = JSON.parse(snapshot.files.get('manifest.json').toString('utf8')); }
  catch { throw new Error('FFmpeg component manifest is malformed.'); }
  verifyComponentMetadata(manifest, identity, expectedSigner);
  verifyPinnedSourceAssets(
    snapshot.files.get('ffmpeg-9.0.tar.xz'),
    snapshot.files.get('ffmpeg-9.0.tar.xz.asc'),
    { sourceSha256: policy.sourceSha256, sourceSignatureSha256: sourceSignatureSHA256 },
  );
  for (const [asset, local] of [
    ['toolchain-lock.json', join(projectRoot, 'third_party', 'ffmpeg', 'toolchain-lock.json')],
    ['NOTICE.md', join(projectRoot, 'third_party', 'ffmpeg', 'NOTICE.md')],
    ['COPYING.LGPLv2.1', join(projectRoot, 'third_party', 'ffmpeg', 'COPYING.LGPLv2.1')],
  ]) {
    const localBytes = await readBoundedRegularFile(local, COMPONENT_ASSET_MAXIMUMS[asset], `FFmpeg target ${asset}`);
    if (!snapshot.files.get(asset).equals(localBytes)) throw new Error(`FFmpeg component ${asset} differs from checked-in policy.`);
  }
  if (!snapshot.files.get('ffmpeg-component-gate.txt').equals(Buffer.from(manifest.component_gate, 'utf8'))) {
    throw new Error('FFmpeg component gate asset differs from manifest.');
  }
  const payloadVerifier = verifyPayload ?? ((directory, signer) => verifyPayloadDirectory(directory, signer, { projectRoot, toolRoot }));
  await withPrivateComponentSnapshot(snapshot.files, async (directory) => payloadVerifier(directory, expectedSigner));
  return { identity, files: snapshot.files, sealed, cleanup: sealed.cleanup };
}

export async function installComponentAssets(options) {
  const { projectRoot = root, inputDirectory, publishPair } = options;
  const destination = await assertInstallDestination(projectRoot);
  const expectedSigner = options.expectedSigner ?? FFMPEG_SIGNED_COMPONENT_SIGNER;
  const verified = await verifyComponentSnapshot({
    projectRoot,
    toolRoot: options.toolRoot ?? defaultToolRoot,
    inputDirectory,
	sealedOutputDirectory: options.sealedOutputDirectory,
    expectedSigner,
    verifyPayload: options.verifyPayload,
    loadPolicy: options.loadPolicy ?? loadFFmpegPolicy,
    sourceSignatureSHA256: options.sourceSignatureSHA256 ?? FFMPEG_SOURCE_SIGNATURE_SHA256,
	sealFFmpegClosure: options.sealFFmpegClosure,
  });
  try {
    await assertInstallDestination(projectRoot);
    const archive = verified.files.get('ffmpeg.zip');
    const manifest = verified.files.get('manifest.json');
    if (publishPair) await publishPair(destination, archive, manifest);
    else publishSealedFFmpegClosure(destination, verified.sealed);
    await assertInstallDestination(projectRoot);
    const installedArchive = await readBoundedRegularFile(join(destination, 'ffmpeg.zip'), COMPONENT_ASSET_MAXIMUMS['ffmpeg.zip'], 'installed FFmpeg archive');
    const installedManifest = await readBoundedRegularFile(join(destination, 'manifest.json'), COMPONENT_ASSET_MAXIMUMS['manifest.json'], 'installed FFmpeg manifest');
    if (!installedArchive.equals(archive) || !installedManifest.equals(manifest)) {
      throw new Error('FFmpeg installation bytes differ from the verified snapshot.');
    }
    return verified.identity;
  } finally {
    await verified.cleanup();
  }
}

function verifyPayloadDirectory(directory, expectedSigner, { projectRoot, toolRoot }) {
  execFileSync(process.execPath, [join(toolRoot, 'scripts', 'verify-ffmpeg.mjs'), '--payload-only', '--payload-directory', directory, '--build-config', join(directory, 'ffmpeg-build-config.txt')], {
    cwd: projectRoot,
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

async function readComponentSnapshot(directory, sealedFiles) {
  const inputReal = await assertRealDirectory(directory, 'FFmpeg component input');
  const names = await readdir(directory);
  const expectedNames = [...REQUIRED_COMPONENT_ASSETS, CHECKSUM_ASSET].sort();
  if (names.length !== expectedNames.length || [...names].sort().some((name, index) => name !== expectedNames[index])) {
    throw new Error('FFmpeg component asset closure has missing or unexpected files.');
  }
  const files = new Map();
  for (const name of REQUIRED_COMPONENT_ASSETS) {
	if (sealedFiles?.has(name)) files.set(name, sealedFiles.get(name));
	else files.set(name, await readBoundedRegularFile(join(directory, name), COMPONENT_ASSET_MAXIMUMS[name], `FFmpeg component asset ${name}`));
  }
  const checksum = await readBoundedRegularFile(join(directory, CHECKSUM_ASSET), COMPONENT_ASSET_MAXIMUMS[CHECKSUM_ASSET], 'FFmpeg checksum asset');
  verifyChecksumSnapshot(files, checksum);
  const finalInputReal = await assertRealDirectory(directory, 'FFmpeg component input');
  if (!sameResolvedPath(inputReal, finalInputReal)) throw new Error('FFmpeg component input directory changed while snapshotting.');
  return { files, checksum };
}

async function createSealedFFmpegClosure({ projectRoot, archivePath, manifestPath, sealedOutputDirectory, sealFFmpegClosure }) {
	const temporary = !sealedOutputDirectory;
	const directory = temporary
	  ? await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-sealed-'))
	  : resolve(sealedOutputDirectory);
	if (temporary) await chmod(directory, 0o700);
	else {
	  assertContainedDirectory(join(resolve(projectRoot), 'dist'), directory, 'sealed FFmpeg output');
	  await createExclusiveDirectory(directory, 'sealed FFmpeg output');
	}
	await assertRealDirectory(directory, 'sealed FFmpeg output');
	let cleaned = false;
	const cleanup = async () => {
	  if (!temporary || cleaned) return;
	  cleaned = true;
	  for (const name of ['ffmpeg.zip', 'manifest.json']) await unlink(join(directory, name)).catch(() => undefined);
	  await rmdir(directory);
	};
	try {
	  const sealer = sealFFmpegClosure ?? runGoFFmpegSealer;
	  const evidence = await sealer({ archivePath, manifestPath, sealedDirectory: directory });
	  const names = await readdir(directory);
	  if (names.length !== 2 || [...names].sort().join(',') !== 'ffmpeg.zip,manifest.json') throw new Error('Go-sealed FFmpeg closure contains unexpected files.');
	  const archive = await readBoundedRegularFile(join(directory, 'ffmpeg.zip'), COMPONENT_ASSET_MAXIMUMS['ffmpeg.zip'], 'Go-sealed FFmpeg archive');
	  const manifest = await readBoundedRegularFile(join(directory, 'manifest.json'), COMPONENT_ASSET_MAXIMUMS['manifest.json'], 'Go-sealed FFmpeg manifest');
	  if (!evidence || evidence.schemaVersion !== 1 || evidence.archiveSha256 !== sha256(archive) || evidence.archiveSize !== archive.length || evidence.manifestSha256 !== sha256(manifest) || evidence.manifestSize !== manifest.length) {
		throw new Error('Go-sealed FFmpeg evidence does not bind the exact output bytes.');
	  }
	  return { directory, archivePath: join(directory, 'ffmpeg.zip'), manifestPath: join(directory, 'manifest.json'), files: new Map([['ffmpeg.zip', archive], ['manifest.json', manifest]]), evidence, cleanup };
	} catch (error) {
	  await cleanup();
	  throw error;
	}
}

function runGoFFmpegSealer({ archivePath, manifestPath, sealedDirectory }) {
	const inspector = process.env.AUTHENTICODE_INSPECTOR_PATH?.trim();
	if (!inspector) throw new Error('AUTHENTICODE_INSPECTOR_PATH is required for Go FFmpeg sealing.');
	const output = execFileSync(inspector, ['seal-ffmpeg', '--archive', archivePath, '--manifest', manifestPath, '--sealed-directory', sealedDirectory], { encoding: 'utf8', windowsHide: true });
	try { return JSON.parse(output); }
	catch { throw new Error('Go FFmpeg sealer output is invalid.'); }
}

function publishSealedFFmpegClosure(destination, sealed) {
	const inspector = process.env.AUTHENTICODE_INSPECTOR_PATH?.trim();
	if (!inspector) throw new Error('AUTHENTICODE_INSPECTOR_PATH is required for sealed FFmpeg publication.');
	execFileSync(inspector, ['publish-ffmpeg', '--archive', sealed.archivePath, '--manifest', sealed.manifestPath, '--destination', destination], { stdio: 'inherit', windowsHide: true });
}

async function createExclusiveDirectory(directory, label) {
	const parent = dirname(resolve(directory));
	await assertRealDirectory(parent, `${label} parent`);
	try { await mkdir(resolve(directory), { mode: 0o700 }); }
	catch (error) {
	  if (error?.code === 'EEXIST') throw new Error(`${label} already exists.`);
	  throw error;
	}
	await assertRealDirectory(directory, label);
}

async function readBoundedRegularFile(path, maximum, label) {
  if (!Number.isSafeInteger(maximum) || maximum <= 0) throw new Error(`${label} size policy is invalid.`);
  const first = await lstat(path, { bigint: true }).catch(() => undefined);
  if (!validRegular(first, maximum)) throw new Error(`${label} is not a bounded regular file.`);
  const handle = await open(path, 'r').catch(() => undefined);
  if (!handle) throw new Error(`${label} is unavailable.`);
  try {
    const opened = await handle.stat({ bigint: true }).catch(() => undefined);
    if (!validRegular(opened, maximum) || !sameFileIdentity(first, opened)) throw new Error(`${label} identity changed while opening.`);
    const expectedLength = Number(opened.size);
    const storage = Buffer.allocUnsafe(expectedLength + 1);
    let length = 0;
    while (length < storage.length) {
      const { bytesRead } = await handle.read(storage, length, storage.length - length, length);
      if (bytesRead === 0) break;
      length += bytesRead;
    }
    if (length !== expectedLength || length > maximum) throw new Error(`${label} bounded read is invalid.`);
    const bytes = Buffer.from(storage.subarray(0, length));
    const after = await handle.stat({ bigint: true }).catch(() => undefined);
    if (!validRegular(after, maximum) || !sameFileSnapshot(opened, after)) throw new Error(`${label} changed while reading.`);
    const final = await lstat(path, { bigint: true }).catch(() => undefined);
    if (!validRegular(final, maximum) || !sameFileSnapshot(opened, final)) throw new Error(`${label} path changed while reading.`);
    return bytes;
  } finally {
    await handle.close();
  }
}

function validRegular(info, maximum) {
  return info?.isFile() && !info.isSymbolicLink() && info.size > 0n && info.size <= BigInt(maximum);
}

function sameFileIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino;
}

function sameFileSnapshot(left, right) {
  return sameFileIdentity(left, right) && left.size === right.size && left.mtimeNs === right.mtimeNs && left.ctimeNs === right.ctimeNs;
}

async function withPrivateComponentSnapshot(files, callback) {
  const directory = await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-component-'));
  let callbackError;
  try {
    await chmod(directory, 0o700);
    await assertRealDirectory(directory, 'private FFmpeg component snapshot');
    await writePreparedComponentAssets(directory, files);
    return await callback(directory);
  } catch (error) {
    callbackError = error;
    throw error;
  } finally {
    try {
      for (const name of [...REQUIRED_COMPONENT_ASSETS, CHECKSUM_ASSET]) await unlink(join(directory, name));
      await rmdir(directory);
    } catch (cleanupError) {
      if (!callbackError) throw cleanupError;
    }
  }
}

async function assertInstallDestination(projectRoot) {
  if (String(projectRoot).split(/[\\/]+/).includes('..')) throw new Error('FFmpeg target project traversal is forbidden.');
  const project = resolve(projectRoot);
  const goserver = join(project, 'goserver');
  const destination = join(goserver, 'ffmpeg');
  const projectReal = await assertRealDirectory(project, 'FFmpeg target project');
  const goserverReal = await assertRealDirectory(goserver, 'FFmpeg target goserver');
  const destinationReal = await assertRealDirectory(destination, 'FFmpeg installation destination');
  if (!sameResolvedPath(goserverReal, join(projectReal, 'goserver')) || !sameResolvedPath(destinationReal, join(goserverReal, 'ffmpeg'))) {
    throw new Error('FFmpeg installation destination escapes the real project tree.');
  }
  return destination;
}

async function writeVerifiedManifest(projectRoot, outputPath, manifest) {
  const project = resolve(projectRoot);
  const dist = join(project, 'dist');
  const distReal = await assertRealDirectory(dist, 'verified manifest output parent');
  const output = resolve(outputPath);
  if (!sameResolvedPath(dirname(output), distReal) || sameResolvedPath(output, distReal)) {
    throw new Error('Verified manifest output must be a direct file in the real target dist directory.');
  }
  if (await lstat(output).then(() => true, (error) => error?.code === 'ENOENT' ? false : Promise.reject(error))) {
    throw new Error('Verified manifest output already exists.');
  }
  await writeFile(output, manifest, { flag: 'wx', mode: 0o600 });
  const written = await readBoundedRegularFile(output, COMPONENT_ASSET_MAXIMUMS['manifest.json'], 'verified manifest output');
  if (!written.equals(manifest)) {
    await unlink(output).catch(() => undefined);
    throw new Error('Verified manifest output differs from the verified snapshot.');
  }
}

async function assertRealDirectory(path, label) {
  const absolute = resolve(path);
  const info = await lstat(absolute).catch(() => undefined);
  if (!info?.isDirectory() || info.isSymbolicLink()) throw new Error(`${label} is not a real non-reparse directory.`);
  const resolvedReal = await realpath(absolute).catch(() => undefined);
  if (!resolvedReal || !sameResolvedPath(resolvedReal, absolute)) throw new Error(`${label} contains a symbolic link, junction, or reparse traversal.`);
  return resolvedReal;
}

function sameResolvedPath(left, right) {
  const normalize = (value) => process.platform === 'win32' ? resolve(value).toLowerCase() : resolve(value);
  return normalize(left) === normalize(right);
}

function requireReviewedSigner(expectedSigner) {
  if (expectedSigner !== FFMPEG_SIGNED_COMPONENT_SIGNER && expectedSigner !== FFMPEG_FIXED_COMPONENT_SIGNER_SUBJECT) {
    throw new Error('Reviewed FFmpeg component signer is invalid.');
  }
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
  const toolRoot = process.argv.includes('--tool-root') ? argument('--tool-root') : defaultToolRoot;
  if (toolRoot !== moduleToolRoot) throw new Error('Reviewed FFmpeg tool root does not match the executing tool checkout.');
  const kind = command === 'verify-metadata' ? '' : stringArgument('--kind');
  if (kind && kind !== 'fixed' && kind !== 'current') throw new Error('FFmpeg component kind must be fixed or current.');
  const component = kind === 'fixed'
    ? { identity: await reviewedFixedFFmpegComponentIdentity(toolRoot), signer: FFMPEG_FIXED_COMPONENT_SIGNER_SUBJECT }
    : kind === 'current'
      ? { identity: await reviewedSignedFFmpegComponentIdentity(toolRoot), signer: FFMPEG_SIGNED_COMPONENT_SIGNER }
      : undefined;
  if (command === 'identity') {
    const identity = component.identity;
    process.stdout.write(`${JSON.stringify({ schema: 2, fingerprint: identity.fingerprint, tag: identity.tag })}\n`);
    return;
  }
  if (command === 'prepare') {
    if (kind !== 'current') throw new Error('Only the current reviewed component may be prepared.');
	await prepareComponentAssets({ toolRoot, outputDirectory: argument('--output'), sealedOutputDirectory: argument('--sealed-output'), expectedSigner: component.signer });
  }
  else if (command === 'verify') await verifyComponentAssets({
    toolRoot,
    inputDirectory: argument('--input'),
    expectedSigner: component.signer,
    manifestOutputPath: process.argv.includes('--manifest-output') ? argument('--manifest-output') : undefined,
	sealedOutputDirectory: argument('--sealed-output'),
  });
  else if (command === 'install') await installComponentAssets({ toolRoot, inputDirectory: argument('--input'), sealedOutputDirectory: argument('--sealed-output'), expectedSigner: component.signer });
  else if (command === 'verify-metadata') await verifyGitHubReleaseMetadata(JSON.parse(await readFile(argument('--metadata'), 'utf8')), argument('--input'), stringArgument('--tag'));
  else throw new Error('Usage: node scripts/ffmpeg-component-assets.mjs <mode> --tool-root <reviewed-root> [mode arguments]');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main();
