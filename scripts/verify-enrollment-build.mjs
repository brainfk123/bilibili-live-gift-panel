import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { lstat, open, realpath, writeFile } from 'node:fs/promises';
import { basename, dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const naisNetIdentity = Object.freeze({ country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094' });
const knownTestRootSHA256 = '5cd252fb0ce8932436faf8ccd1040981b89ee4ad6b9fe9e2a2b7e71aacb27cd3';
const knownTestPolicySHA256 = '205b8ea9bf7e79d55292d63a1266a4882ab01fa5edb3eb79421724ddb9265d0e';

export async function verifyEnrollmentBuild(options) {
  validateOptions(options);
  const [artifact, artifactInspectionBytes, artifactSidecar, standaloneFFmpeg, ffmpegSidecar, ffmpegArchive, ffmpegManifest, rootSPKI, bootstrapPolicy, authorizationPolicy] = await Promise.all([
    readBoundedRegular(options.artifactPath, 128 << 20, 'sealed artifact'),
    readBoundedRegular(options.artifactInspectionPath, 128 << 10, 'artifact inspection evidence'),
    readBoundedRegular(options.artifactSidecarPath, 1024, 'artifact checksum sidecar'),
    readBoundedRegular(options.standaloneFFmpegPath, 40 << 20, 'standalone FFmpeg'),
    readBoundedRegular(options.ffmpegSidecarPath, 1024, 'FFmpeg checksum sidecar'),
    readBoundedRegular(options.ffmpegArchivePath, 40 << 20, 'sealed FFmpeg archive'),
    readBoundedRegular(options.ffmpegManifestPath, 64 << 10, 'sealed FFmpeg manifest'),
    readBoundedRegular(options.rootSPKIPath, 4 << 10, 'reviewed root SPKI'),
    readBoundedRegular(options.bootstrapPolicyPath, 256 << 10, 'reviewed bootstrap policy'),
    readBoundedRegular(options.authorizationPolicyPath, 256 << 10, 'reviewed authorization policy'),
  ]);
  const artifactSHA256 = sha256(artifact);
  if (basename(options.artifactPath) !== `${artifactSHA256}.exe`) throw new Error('sealed artifact content-addressed filename is invalid');
  const standaloneFFmpegSHA256 = sha256(standaloneFFmpeg);
  const rootSHA256 = sha256(rootSPKI);
  const bootstrapSHA256 = sha256(bootstrapPolicy);
  const authorizationSHA256 = sha256(authorizationPolicy);
	const ffmpegManifestSHA256 = sha256(ffmpegManifest);
  if (rootSHA256 !== options.expectedRootSHA256 || bootstrapSHA256 !== options.expectedBootstrapPolicySHA256 || authorizationSHA256 !== options.expectedAuthorizationPolicySHA256 || ffmpegManifestSHA256 !== options.expectedFFmpegManifestSHA256 || rootSHA256 === knownTestRootSHA256 || bootstrapSHA256 === knownTestPolicySHA256 || authorizationSHA256 === knownTestPolicySHA256) fail();
  verifySidecar(artifactSidecar, artifactSHA256, 'gift-panel-windows-x64.exe');
  verifySidecar(ffmpegSidecar, standaloneFFmpegSHA256, 'ffmpeg-windows-x64.exe');
  const artifactInspection = parseObject(artifactInspectionBytes, 'artifact inspection evidence');
  const allowedArtifactInspection = new Set(['version', 'tag', 'commit', 'peContentSha256', 'signedFileSha256', 'signedFileSize', 'rootSpkiSha256', 'policySha256', 'policyEpoch', 'outerIdentity', 'ffmpegVersion', 'ffmpegSha256', 'ffmpegSize', 'ffmpegIdentity']);
  if (Object.keys(artifactInspection).some((key) => !allowedArtifactInspection.has(key))) fail();
  if (artifactInspection.version !== options.version || artifactInspection.commit !== options.commit || artifactInspection.signedFileSha256 !== artifactSHA256 || artifactInspection.signedFileSize !== artifact.length || !lowerHex(artifactInspection.peContentSha256, 64) || !exactIdentity(artifactInspection.outerIdentity, naisNetIdentity) || artifactInspection.ffmpegVersion !== '9.0' || artifactInspection.ffmpegSha256 !== standaloneFFmpegSHA256 || artifactInspection.ffmpegSize !== standaloneFFmpeg.length || !exactIdentity(artifactInspection.ffmpegIdentity, naisNetIdentity)) fail();

  const inspectorArguments = [
    'verify-enrollment', '--artifact', options.artifactPath, '--pe-content-sha256', artifactInspection.peContentSha256,
    '--version', options.version, '--tag', options.tag, '--commit', options.commit,
    '--root-spki', options.rootSPKIPath, '--root-sha256', options.expectedRootSHA256,
    '--bootstrap-policy', options.bootstrapPolicyPath, '--bootstrap-policy-sha256', options.expectedBootstrapPolicySHA256, '--bootstrap-policy-epoch', String(options.bootstrapPolicyEpoch),
    '--authorization-policy', options.authorizationPolicyPath, '--authorization-policy-sha256', options.expectedAuthorizationPolicySHA256, '--authorization-policy-epoch', String(options.authorizationPolicyEpoch),
    '--ffmpeg-archive', options.ffmpegArchivePath, '--ffmpeg-manifest', options.ffmpegManifestPath,
  ];
  let inspectorOutput;
  try {
    inspectorOutput = options.runInspector
      ? await options.runInspector(inspectorArguments)
      : execFileSync(options.inspectorPath, inspectorArguments, { encoding: 'utf8', maxBuffer: 256 << 10, windowsHide: true });
  } catch {
    fail();
  }
  const inspection = parseObject(Buffer.from(inspectorOutput, 'utf8'), 'enrollment inspector output');
  const inspectionKeys = [
    'schemaVersion', 'version', 'tag', 'commit', 'signedFileSha256', 'signedFileSize', 'peContentSha256', 'rootSpkiSha256',
    'bootstrapPolicySha256', 'bootstrapPolicyEpoch', 'bootstrapSignatureStatus', 'authorizationPolicySha256', 'authorizationPolicyEpoch',
    'authorizationSignatureStatus', 'authorizedChannel', 'authorizedTag', 'authorizedArtifactSha256', 'authorizedIdentity', 'outerIdentity',
    'authenticodeStatus', 'ffmpegVersion', 'ffmpegSha256', 'ffmpegSize', 'ffmpegArchiveSha256', 'ffmpegManifestSha256', 'ffmpegIdentity', 'ffmpegSignatureStatus',
  ].sort();
  if (Object.keys(inspection).sort().join(',') !== inspectionKeys.join(',')) fail();
  if (inspection.schemaVersion !== 1 || inspection.version !== options.version || inspection.tag !== options.tag || inspection.commit !== options.commit ||
      inspection.signedFileSha256 !== artifactSHA256 || inspection.signedFileSize !== artifact.length || inspection.peContentSha256 !== artifactInspection.peContentSha256 ||
      inspection.rootSpkiSha256 !== rootSHA256 || inspection.bootstrapPolicySha256 !== bootstrapSHA256 || inspection.bootstrapPolicyEpoch !== options.bootstrapPolicyEpoch || inspection.bootstrapSignatureStatus !== 'Valid' ||
      inspection.authorizationPolicySha256 !== authorizationSHA256 || inspection.authorizationPolicyEpoch !== options.authorizationPolicyEpoch || inspection.authorizationSignatureStatus !== 'Valid' ||
      inspection.authorizedChannel !== 'stable' || inspection.authorizedTag !== options.tag || inspection.authorizedArtifactSha256 !== artifactSHA256 || !exactIdentity(inspection.authorizedIdentity, naisNetIdentity) ||
      !exactIdentity(inspection.outerIdentity, naisNetIdentity) || inspection.authenticodeStatus !== 'Valid' || inspection.ffmpegVersion !== '9.0' || inspection.ffmpegSha256 !== standaloneFFmpegSHA256 || inspection.ffmpegSize !== standaloneFFmpeg.length ||
	  inspection.ffmpegArchiveSha256 !== sha256(ffmpegArchive) || inspection.ffmpegManifestSha256 !== ffmpegManifestSHA256 || !exactIdentity(inspection.ffmpegIdentity, naisNetIdentity) || inspection.ffmpegSignatureStatus !== 'Valid') fail();

  const evidence = {
    schemaVersion: 1,
    version: options.version,
    tag: options.tag,
    commit: options.commit,
	artifact: { sha256: artifactSHA256, peContentSha256: inspection.peContentSha256, signatureStatus: 'Valid', identity: naisNetIdentity },
	root: { spkiSha256: rootSHA256, rootKeyId: `sha256:${rootSHA256}` },
    bootstrapPolicy: { sha256: bootstrapSHA256, epoch: options.bootstrapPolicyEpoch, signatureStatus: 'Valid' },
	authorizationPolicy: { sha256: authorizationSHA256, epoch: options.authorizationPolicyEpoch, signatureStatus: 'Valid', tag: options.tag, artifactSha256: artifactSHA256, identity: naisNetIdentity },
	ffmpeg: { version: '9.0', sha256: standaloneFFmpegSHA256, archiveSha256: inspection.ffmpegArchiveSha256, manifestSha256: inspection.ffmpegManifestSha256, signatureStatus: 'Valid', identity: naisNetIdentity },
  };
  await writeEvidence(options.outputPath, evidence);
  return evidence;
}

function validateOptions(options) {
	if (options && ('rootKeyID' in options || 'rootKeyId' in options || 'keyId' in options)) fail();
  if (!options || Object.getPrototypeOf(options) !== Object.prototype || !isEnrollmentVersion(options.version) || options.tag !== `v${options.version}` || !lowerHex(options.commit, 40) ||
      !lowerHex(options.expectedRootSHA256, 64) || !lowerHex(options.expectedBootstrapPolicySHA256, 64) || !lowerHex(options.expectedAuthorizationPolicySHA256, 64) ||
	  !lowerHex(options.expectedFFmpegManifestSHA256, 64) ||
	  !Number.isSafeInteger(options.bootstrapPolicyEpoch) || options.bootstrapPolicyEpoch < 1 || !Number.isSafeInteger(options.authorizationPolicyEpoch) || options.authorizationPolicyEpoch <= options.bootstrapPolicyEpoch) fail();
  for (const name of ['inspectorPath', 'artifactPath', 'artifactInspectionPath', 'artifactSidecarPath', 'standaloneFFmpegPath', 'ffmpegSidecarPath', 'ffmpegArchivePath', 'ffmpegManifestPath', 'rootSPKIPath', 'bootstrapPolicyPath', 'authorizationPolicyPath', 'outputPath']) {
    if (typeof options[name] !== 'string' || options[name].length === 0) fail();
  }
}

function isEnrollmentVersion(value) {
	if (typeof value !== 'string' || !/^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$/.test(value)) return false;
	const parts = value.split('.').map(Number);
	if (parts.some((part) => !Number.isSafeInteger(part))) return false;
	return parts[0] > 0 || (parts[0] === 0 && (parts[1] > 4 || (parts[1] === 4 && parts[2] >= 12)));
}

async function readBoundedRegular(path, maximum, label) {
  const absolute = resolve(path);
  const first = await lstat(absolute, { bigint: true }).catch(() => undefined);
  if (!validRegular(first, maximum)) throw new Error(`${label} is invalid`);
  const resolved = await realpath(absolute).catch(() => undefined);
  if (!resolved || !samePath(resolved, absolute)) throw new Error(`${label} is invalid`);
  const handle = await open(absolute, 'r').catch(() => undefined);
  if (!handle) throw new Error(`${label} is invalid`);
  try {
    const opened = await handle.stat({ bigint: true }).catch(() => undefined);
    if (!validRegular(opened, maximum) || first.dev !== opened.dev || first.ino !== opened.ino) throw new Error(`${label} is invalid`);
    const length = Number(opened.size);
    const storage = Buffer.allocUnsafe(length + 1);
    let offset = 0;
    while (offset < storage.length) {
      const { bytesRead } = await handle.read(storage, offset, storage.length - offset, offset);
      if (bytesRead === 0) break;
      offset += bytesRead;
    }
    const after = await handle.stat({ bigint: true }).catch(() => undefined);
    const final = await lstat(absolute, { bigint: true }).catch(() => undefined);
    if (offset !== length || !validRegular(after, maximum) || !validRegular(final, maximum) || opened.dev !== after.dev || opened.ino !== after.ino || opened.size !== after.size || opened.mtimeNs !== after.mtimeNs || opened.ctimeNs !== after.ctimeNs || opened.dev !== final.dev || opened.ino !== final.ino || opened.size !== final.size || opened.mtimeNs !== final.mtimeNs || opened.ctimeNs !== final.ctimeNs) throw new Error(`${label} changed while read`);
    return Buffer.from(storage.subarray(0, length));
  } finally {
    await handle.close();
  }
}

function validRegular(info, maximum) {
  return info?.isFile() && !info.isSymbolicLink() && info.size > 0n && info.size <= BigInt(maximum);
}

function samePath(left, right) {
  return process.platform === 'win32' ? resolve(left).toLowerCase() === resolve(right).toLowerCase() : resolve(left) === resolve(right);
}

function sha256(contents) {
  return createHash('sha256').update(contents).digest('hex');
}

function lowerHex(value, length) {
  return typeof value === 'string' && value.length === length && /^[0-9a-f]+$/.test(value);
}

function verifySidecar(contents, digest, name) {
  if (contents.toString('ascii') !== `${digest}  ${name}`) fail();
}

function parseObject(contents, _label) {
  let value;
  try { value = JSON.parse(contents.toString('utf8')); }
  catch { fail(); }
  if (!value || Object.getPrototypeOf(value) !== Object.prototype) fail();
  return value;
}

function exactIdentity(actual, expected) {
  return actual && Object.getPrototypeOf(actual) === Object.prototype && Object.keys(actual).sort().join(',') === 'country,organization,organizationId' && actual.country === expected.country && actual.organization === expected.organization && actual.organizationId === expected.organizationId;
}

async function writeEvidence(path, evidence) {
  const output = resolve(path);
  const parent = dirname(output);
  const parentInfo = await lstat(parent).catch(() => undefined);
  const parentReal = await realpath(parent).catch(() => undefined);
  if (!parentInfo?.isDirectory() || parentInfo.isSymbolicLink() || !parentReal || !samePath(parentReal, parent)) fail();
  const bytes = Buffer.from(`${JSON.stringify(evidence, null, 2)}\n`, 'utf8');
  await writeFile(output, bytes, { flag: 'wx', mode: 0o600 }).catch(() => fail());
  const written = await readBoundedRegular(output, 128 << 10, 'enrollment evidence output');
  if (!written.equals(bytes)) fail();
}

function fail() {
  throw new Error('enrollment build verification failed');
}

function parseCLI(arguments_) {
	const names = new Set(['--inspector', '--artifact', '--artifact-inspection', '--artifact-sidecar', '--standalone-ffmpeg', '--ffmpeg-sidecar', '--ffmpeg-archive', '--ffmpeg-manifest', '--ffmpeg-manifest-sha256', '--root-spki', '--root-sha256', '--bootstrap-policy', '--bootstrap-policy-sha256', '--bootstrap-policy-epoch', '--authorization-policy', '--authorization-policy-sha256', '--authorization-policy-epoch', '--version', '--tag', '--commit', '--output']);
  const values = new Map();
  for (let index = 0; index < arguments_.length; index += 2) {
    const name = arguments_[index];
    const value = arguments_[index + 1];
    if (!names.has(name) || values.has(name) || !value || value.startsWith('--')) fail();
    values.set(name, value);
  }
  if (values.size !== names.size) fail();
  const number = (name) => {
    const value = values.get(name);
    if (!/^[1-9][0-9]*$/.test(value)) fail();
    const parsed = Number(value);
    if (!Number.isSafeInteger(parsed)) fail();
    return parsed;
  };
  return {
    inspectorPath: resolve(values.get('--inspector')), artifactPath: resolve(values.get('--artifact')), artifactInspectionPath: resolve(values.get('--artifact-inspection')), artifactSidecarPath: resolve(values.get('--artifact-sidecar')),
	standaloneFFmpegPath: resolve(values.get('--standalone-ffmpeg')), ffmpegSidecarPath: resolve(values.get('--ffmpeg-sidecar')), ffmpegArchivePath: resolve(values.get('--ffmpeg-archive')), ffmpegManifestPath: resolve(values.get('--ffmpeg-manifest')), expectedFFmpegManifestSHA256: values.get('--ffmpeg-manifest-sha256'),
	rootSPKIPath: resolve(values.get('--root-spki')), expectedRootSHA256: values.get('--root-sha256'),
    bootstrapPolicyPath: resolve(values.get('--bootstrap-policy')), expectedBootstrapPolicySHA256: values.get('--bootstrap-policy-sha256'), bootstrapPolicyEpoch: number('--bootstrap-policy-epoch'),
    authorizationPolicyPath: resolve(values.get('--authorization-policy')), expectedAuthorizationPolicySHA256: values.get('--authorization-policy-sha256'), authorizationPolicyEpoch: number('--authorization-policy-epoch'),
    version: values.get('--version'), tag: values.get('--tag'), commit: values.get('--commit'), outputPath: resolve(values.get('--output')),
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    await verifyEnrollmentBuild(parseCLI(process.argv.slice(2)));
  } catch {
    process.stderr.write('enrollment build verification failed\n');
    process.exitCode = 1;
  }
}
