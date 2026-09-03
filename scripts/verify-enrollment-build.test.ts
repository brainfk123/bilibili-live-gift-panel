import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { verifyEnrollmentBuild, type EnrollmentVerificationOptions } from './verify-enrollment-build.mjs';

const roots: string[] = [];
afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

const naisNet = { country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094' };

function sha256(contents: Buffer) {
  return createHash('sha256').update(contents).digest('hex');
}

function fixture() {
  const root = realpathSync(mkdtempSync(join(tmpdir(), 'enrollment-build-test-')));
  roots.push(root);
  const outputDirectory = join(root, 'output');
  mkdirSync(outputDirectory);
  const artifact = Buffer.from('final sealed NaisNet executable with a RushRush certificate-table decoy');
  const artifactHash = sha256(artifact);
  const artifactPath = join(root, `${artifactHash}.exe`);
  const standaloneFFmpeg = Buffer.from('standalone exact NaisNet FFmpeg');
  const ffmpegHash = sha256(standaloneFFmpeg);
  const ffmpegArchive = Buffer.from('sealed FFmpeg archive bytes');
  const ffmpegManifest = Buffer.from('{"sealed":"FFmpeg manifest bytes"}');
  const rootSPKI = Buffer.from('reviewed production P-256 SPKI');
  const bootstrapPolicy = Buffer.from('reviewed signed bootstrap policy');
  const authorizationPolicy = Buffer.from('reviewed signed final hash authorization');
  const commit = 'a'.repeat(40);
  const peContentSha256 = 'b'.repeat(64);
  const write = (name: string, contents: Buffer | string) => {
    const path = join(root, name);
    writeFileSync(path, contents);
    return path;
  };
  writeFileSync(artifactPath, artifact);
  const artifactInspection = {
    version: '0.4.12', tag: '', commit, signedFileSha256: artifactHash, signedFileSize: artifact.length,
    peContentSha256, outerIdentity: naisNet, ffmpegVersion: '9.0', ffmpegSha256: ffmpegHash,
    ffmpegSize: standaloneFFmpeg.length, ffmpegIdentity: naisNet,
    rootSpkiSha256: '', bootstrapPolicySha256: '', bootstrapPolicyEpoch: 0,
    authorizationPolicySha256: '', authorizationPolicyEpoch: 0,
  };
  const goEvidence = {
    schemaVersion: 1, version: '0.4.12', tag: 'v0.4.12', commit,
    signedFileSha256: artifactHash, signedFileSize: artifact.length, peContentSha256,
    rootSpkiSha256: sha256(rootSPKI), bootstrapPolicySha256: sha256(bootstrapPolicy), bootstrapPolicyEpoch: 1,
    bootstrapSignatureStatus: 'Valid', authorizationPolicySha256: sha256(authorizationPolicy), authorizationPolicyEpoch: 2,
    authorizationSignatureStatus: 'Valid', authorizationScope: 'artifact-sha256', authorizedChannel: 'stable', authorizedTag: 'v0.4.12', authorizedArtifactSha256: artifactHash,
    authorizedIdentity: naisNet, outerIdentity: naisNet, authenticodeStatus: 'Valid',
    ffmpegVersion: '9.0', ffmpegSha256: ffmpegHash, ffmpegSize: standaloneFFmpeg.length,
    ffmpegArchiveSha256: sha256(ffmpegArchive), ffmpegManifestSha256: sha256(ffmpegManifest), ffmpegIdentity: naisNet, ffmpegSignatureStatus: 'Valid',
  };
  const options: EnrollmentVerificationOptions = {
    inspectorPath: join(root, 'artifact-inspector.exe'), artifactPath,
    artifactInspectionPath: write('artifact-inspection.json', JSON.stringify(artifactInspection)),
    artifactSidecarPath: write('gift-panel-windows-x64.exe.sha256', `${artifactHash}  gift-panel-windows-x64.exe`),
    standaloneFFmpegPath: write('ffmpeg-windows-x64.exe', standaloneFFmpeg),
    ffmpegSidecarPath: write('ffmpeg-windows-x64.exe.sha256', `${ffmpegHash}  ffmpeg-windows-x64.exe`),
	ffmpegArchivePath: write('ffmpeg.zip', ffmpegArchive), ffmpegManifestPath: write('manifest.json', ffmpegManifest), expectedFFmpegManifestSHA256: sha256(ffmpegManifest),
	rootSPKIPath: write('root.der', rootSPKI), expectedRootSHA256: sha256(rootSPKI),
    bootstrapPolicyPath: write('bootstrap.json', bootstrapPolicy), expectedBootstrapPolicySHA256: sha256(bootstrapPolicy), bootstrapPolicyEpoch: 1,
    authorizationPolicyPath: write('authorization.json', authorizationPolicy), expectedAuthorizationPolicySHA256: sha256(authorizationPolicy), authorizationPolicyEpoch: 2,
    version: '0.4.12', tag: 'v0.4.12', commit, outputPath: join(outputDirectory, 'stable-release-evidence.json'),
    runInspector: async () => JSON.stringify(goEvidence),
  };
  return { root, artifact, artifactHash, artifactInspection, ffmpegHash, goEvidence, options };
}

describe('verify enrollment build', () => {
  it('cross-binds the final sealed artifact, policies, FFmpeg closure, and sidecars without leaking paths', async () => {
    const value = fixture();
    let inspectorArguments: string[] | undefined;
    value.options.runInspector = async (arguments_) => {
      inspectorArguments = arguments_;
      return JSON.stringify(value.goEvidence);
    };
    const evidence = await verifyEnrollmentBuild(value.options);
    expect(inspectorArguments).toContain('verify-enrollment');
    expect(inspectorArguments).toContain(value.options.artifactPath);
    expect(inspectorArguments).toContain(value.options.authorizationPolicyPath);
    expect(inspectorArguments).not.toContain('unsigned.exe');
    expect(evidence).toEqual({
      schemaVersion: 1, version: '0.4.12', tag: 'v0.4.12', commit: 'a'.repeat(40),
	  artifact: { sha256: value.artifactHash, peContentSha256: 'b'.repeat(64), signatureStatus: 'Valid', identity: naisNet },
	  root: { spkiSha256: value.options.expectedRootSHA256, rootKeyId: `sha256:${value.options.expectedRootSHA256}` },
      bootstrapPolicy: { sha256: value.options.expectedBootstrapPolicySHA256, epoch: 1, signatureStatus: 'Valid' },
	  authorizationPolicy: { sha256: value.options.expectedAuthorizationPolicySHA256, epoch: 2, signatureStatus: 'Valid', scope: 'artifact-sha256', tag: 'v0.4.12', artifactSha256: value.artifactHash, identity: naisNet },
	  ffmpeg: { version: '9.0', sha256: value.ffmpegHash, archiveSha256: value.goEvidence.ffmpegArchiveSha256, manifestSha256: value.goEvidence.ffmpegManifestSha256, signatureStatus: 'Valid', identity: naisNet },
    });
    expect(JSON.parse(readFileSync(value.options.outputPath, 'utf8'))).toEqual(evidence);
    const serialized = JSON.stringify(evidence);
    expect(serialized).not.toContain(value.root);
    expect(serialized).not.toMatch(/[A-Z]:\\|\/tmp\//i);
    expect(serialized).not.toMatch(/"(?:size|sidecars|channel)"/);
	expect(serialized).not.toContain('publisher-root-private-key');
    expect(basename(value.options.artifactPath)).toBe(`${value.artifactHash}.exe`);
  });

  it('records publisher identity scope for post-enrollment stable releases', async () => {
    const value = fixture();
    value.options.version = '0.4.13';
    value.options.tag = 'v0.4.13';
    value.artifactInspection.version = '0.4.13';
    writeFileSync(value.options.artifactInspectionPath, JSON.stringify(value.artifactInspection));
    value.goEvidence.version = '0.4.13';
    value.goEvidence.tag = 'v0.4.13';
    value.goEvidence.authorizedTag = 'v0.4.13';
    value.goEvidence.authorizationScope = 'publisher-identity';
    value.goEvidence.authorizedArtifactSha256 = '';

    const evidence = await verifyEnrollmentBuild(value.options);

    expect(evidence.authorizationPolicy).toEqual({
      sha256: value.options.expectedAuthorizationPolicySHA256,
      epoch: 2,
      signatureStatus: 'Valid',
      scope: 'publisher-identity',
      tag: 'v0.4.13',
      artifactSha256: '',
      identity: naisNet,
    });
    expect(evidence.artifact.sha256).toBe(value.artifactHash);
  });

  it('records a future primary identity only when the signed policy inspector authorizes it', async () => {
    const value = fixture();
    const future = { country: 'CN', organization: 'FutureCo Technology Co., Ltd.', organizationId: '91110000EXAMPLE01' };
    value.options.version = '0.4.33';
    value.options.tag = 'v0.4.33';
    value.artifactInspection.version = '0.4.33';
    value.artifactInspection.outerIdentity = future;
    writeFileSync(value.options.artifactInspectionPath, JSON.stringify(value.artifactInspection));
    value.goEvidence.version = '0.4.33';
    value.goEvidence.tag = 'v0.4.33';
    value.goEvidence.authorizedTag = 'v0.4.33';
    value.goEvidence.authorizationScope = 'publisher-identity';
    value.goEvidence.authorizedArtifactSha256 = '';
    value.goEvidence.authorizedIdentity = future;
    value.goEvidence.outerIdentity = future;

    const evidence = await verifyEnrollmentBuild(value.options);

    expect(evidence.artifact.identity).toEqual(future);
    expect(evidence.authorizationPolicy.identity).toEqual(future);
    expect(evidence.ffmpeg.identity).toEqual(naisNet);
  });

  it.each([
    ['changed seal evidence', (value: ReturnType<typeof fixture>) => { value.artifactInspection.signedFileSha256 = '0'.repeat(64); writeFileSync(value.options.artifactInspectionPath, JSON.stringify(value.artifactInspection)); }],
    ['changed EXE sidecar', (value: ReturnType<typeof fixture>) => { writeFileSync(value.options.artifactSidecarPath, `${'0'.repeat(64)}  gift-panel-windows-x64.exe`); }],
    ['changed FFmpeg sidecar', (value: ReturnType<typeof fixture>) => { writeFileSync(value.options.ffmpegSidecarPath, `${'0'.repeat(64)}  ffmpeg-windows-x64.exe`); }],
    ['static inspection claiming enrollment trust', (value: ReturnType<typeof fixture>) => {
      value.artifactInspection.rootSpkiSha256 = '0'.repeat(64);
      writeFileSync(value.options.artifactInspectionPath, JSON.stringify(value.artifactInspection));
    }],
    ['RushRush final identity', (value: ReturnType<typeof fixture>) => { value.goEvidence.outerIdentity = { country: 'CN', organization: 'RushRush Network Technology Ltd', organizationId: '91450900MADM3GLG5P' }; }],
    ['wrong authorization hash', (value: ReturnType<typeof fixture>) => { value.goEvidence.authorizedArtifactSha256 = '0'.repeat(64); }],
    ['identity scope retaining an artifact hash', (value: ReturnType<typeof fixture>) => { value.goEvidence.authorizationScope = 'publisher-identity'; }],
    ['artifact scope omitting its artifact hash', (value: ReturnType<typeof fixture>) => { value.goEvidence.authorizationScope = 'artifact-sha256'; value.goEvidence.authorizedArtifactSha256 = ''; }],
    ['wrong bootstrap digest', (value: ReturnType<typeof fixture>) => { value.goEvidence.bootstrapPolicySha256 = '0'.repeat(64); }],
    ['wrong reviewed FFmpeg manifest digest', (value: ReturnType<typeof fixture>) => { value.options.expectedFFmpegManifestSHA256 = '0'.repeat(64); }],
	['pre-enrollment stable version', (value: ReturnType<typeof fixture>) => {
	  value.options.version = '0.4.11'; value.options.tag = 'v0.4.11'; value.artifactInspection.version = '0.4.11';
	  writeFileSync(value.options.artifactInspectionPath, JSON.stringify(value.artifactInspection));
	  value.goEvidence.version = '0.4.11'; value.goEvidence.tag = 'v0.4.11'; value.goEvidence.authorizedTag = 'v0.4.11';
	}],
	['non-advancing authorization epoch', (value: ReturnType<typeof fixture>) => { value.options.authorizationPolicyEpoch = 1; value.goEvidence.authorizationPolicyEpoch = 1; }],
  ])('rejects %s', async (_name, mutate) => {
    const value = fixture();
    mutate(value);
    value.options.runInspector = async () => JSON.stringify(value.goEvidence);
    await expect(verifyEnrollmentBuild(value.options)).rejects.toThrow(/enrollment build verification failed/i);
  });

  it('rejects a sealed executable whose content-addressed filename does not bind its bytes', async () => {
    const value = fixture();
    const renamed = join(value.root, `${'0'.repeat(64)}.exe`);
    writeFileSync(renamed, value.artifact);
    value.options.artifactPath = renamed;
    await expect(verifyEnrollmentBuild(value.options)).rejects.toThrow(/content-addressed/i);
  });

  it('rejects a user-controlled root key ID instead of copying it into evidence', async () => {
	const value=fixture();
	(value.options as EnrollmentVerificationOptions & { rootKeyID:string }).rootKeyID='publisher-root-user-controlled';
	await expect(verifyEnrollmentBuild(value.options)).rejects.toThrow(/enrollment build verification failed/i);
  });
});
