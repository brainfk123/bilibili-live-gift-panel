import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { verifyBridgeReadiness } from '../scripts/bridge-release-inputs.mjs';

const sha256 = (value: Buffer) => createHash('sha256').update(value).digest('hex');

function readinessFixture(manifestScoped = true) {
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const rootSPKI = publicKey.export({ format: 'der', type: 'spki' });
  const signed = {
    epoch: 1,
    expiresAt: '2030-01-01T00:00:00Z',
    publishers: [{
      id: 'naisnet-primary', role: 'primary', country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094', allowedChannel: 'stable', allowedTags: ['v0.4.12'],
    }],
  };
  const stableArtifactBytes = Buffer.from('actual immutable v0.4.12 executable fixture');
  const stableArtifactSHA256 = sha256(stableArtifactBytes);
  if (manifestScoped) Object.assign(signed.publishers[0], { manifestSha256: stableArtifactSHA256 });
  const signedBytes = Buffer.from(JSON.stringify(signed));
  const signature = sign('sha256', signedBytes, privateKey).toString('base64');
  const policyBytes = Buffer.from(JSON.stringify({ signed, signatures: [{ algorithm: 'ecdsa-p256-sha256', signature }] }));
  const auditBytes = Buffer.from(JSON.stringify({ keyId: 'kms-production-key', epoch: 1, policySha256: sha256(policyBytes), requestId: 'kms-request-1', utc: '2026-08-01T00:00:00Z', ciActor: 'release-reviewer' }));
  const commitBytes = Buffer.from(JSON.stringify({ schemaVersion:1, policy:{name:'policy.json',length:policyBytes.length,sha256:sha256(policyBytes)}, audit:{name:'audit.json',length:auditBytes.length,sha256:sha256(auditBytes)} }));
  const checksumBytes = Buffer.from(`${stableArtifactSHA256}  gift-panel-windows-x64.exe`);
  const stableRelease = { id: 412, tag_name: 'v0.4.12', draft: false, prerelease: false, published_at: '2026-08-01T00:00:00Z', assets: [
    { name:'gift-panel-windows-x64.exe',size:stableArtifactBytes.length,digest:`sha256:${stableArtifactSHA256}`,content_type:'application/octet-stream',url:'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/4101' },
    { name:'gift-panel-windows-x64.exe.sha256',size:checksumBytes.length,digest:`sha256:${sha256(checksumBytes)}`,content_type:'text/plain',url:'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/4102' },
  ] };
  const observation = {
    schemaVersion: 1,
    stableRelease: { id: 412, tag: 'v0.4.12', publishedAt: '2026-08-01T00:00:00Z', executableSha256: stableArtifactSHA256 },
    observation: { endedAt: '2026-08-08T00:00:00Z', result: 'passed' },
    reviewedAt: '2026-08-08T01:00:00Z',
  };
  const policyRelease = {
    id: 501,
    tag_name: 'publisher-policy-epoch-00000001',
    draft: false,
    prerelease: false,
    published_at: '2026-08-01T00:00:00Z',
    assets: [
      { name: 'policy.json', size: policyBytes.length, digest: `sha256:${sha256(policyBytes)}`,content_type:'application/json',url:'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/5001' },
      { name: 'audit.json', size: auditBytes.length, digest: `sha256:${sha256(auditBytes)}`,content_type:'application/json',url:'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/5002' },
      { name: 'commit.json', size: commitBytes.length, digest: `sha256:${sha256(commitBytes)}`,content_type:'application/json',url:'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/5003' },
    ],
  };
  const attestation = {
    schemaVersion: 1,
    rootSpkiSha256: sha256(rootSPKI),
    policy: { epoch: 1, sha256: sha256(policyBytes) },
    kms: { keyId: 'kms-production-key', auditSha256: sha256(auditBytes), requestId: 'kms-request-1' },
    policyRelease: { id: 501, tag: 'publisher-policy-epoch-00000001', publishedAt: '2026-08-01T00:00:00Z', policyAsset: 'policy.json', auditAsset: 'audit.json', commitAsset:'commit.json' },
    reviewedAt: '2026-08-08T01:00:00Z',
  };
  const observationBytes = Buffer.from(JSON.stringify(observation));
  const attestationBytes = Buffer.from(JSON.stringify(attestation));
  const verifiedBundleBytes=Buffer.from(JSON.stringify({schemaVersion:2,verification:{epoch:1,expectedPreviousEpoch:0,spkiSha256:sha256(rootSPKI)},commit:JSON.parse(commitBytes.toString()),policy:{name:'policy.json',length:policyBytes.length,sha256:sha256(policyBytes),bytesBase64:policyBytes.toString('base64')},audit:{name:'audit.json',length:auditBytes.length,sha256:sha256(auditBytes),bytesBase64:auditBytes.toString('base64')}}));
  return {
    now: new Date('2026-08-08T02:00:00Z'),
    stableReleaseBytes: Buffer.from(JSON.stringify(stableRelease)), stableArtifactBytes,
    stableChecksumBytes: checksumBytes,
    observationEvidenceBytes: observationBytes,
    expectedObservationSHA256: sha256(observationBytes),
    rootSPKI: Buffer.from(rootSPKI), policyBytes, auditBytes,
    verifiedBundleBytes, commitBytes,
    policyReleaseBytes: Buffer.from(JSON.stringify(policyRelease)),
    trustAttestationBytes: attestationBytes,
    expectedTrustAttestationSHA256: sha256(attestationBytes),
  };
}

describe('bridge readiness reviewed evidence', () => {
  it('binds real stable Release observation and immutable production policy evidence', () => {
    expect(verifyBridgeReadiness(readinessFixture())).toMatchObject({
      stableReleaseId: 412,
      stablePublishedAt: '2026-08-01T00:00:00Z',
      observationEndedAt: '2026-08-08T00:00:00Z',
      policyReleaseId: 501,
      policyEpoch: 1,
      kmsKeyId: 'kms-production-key',
      stableArtifactSha256: sha256(Buffer.from('actual immutable v0.4.12 executable fixture')),
    });
  });

  it.each([
    ['moved stable Release', (fixture: ReturnType<typeof readinessFixture>) => {
      const release = JSON.parse(fixture.stableReleaseBytes.toString()); release.id = 999; fixture.stableReleaseBytes = Buffer.from(JSON.stringify(release));
    }],
    ['operator timestamp before seven days', (fixture: ReturnType<typeof readinessFixture>) => {
      const evidence = JSON.parse(fixture.observationEvidenceBytes.toString()); evidence.observation.endedAt = '2026-08-07T23:59:59Z'; fixture.observationEvidenceBytes = Buffer.from(JSON.stringify(evidence)); fixture.expectedObservationSHA256 = sha256(fixture.observationEvidenceBytes);
    }],
    ['current time before seven days', (fixture: ReturnType<typeof readinessFixture>) => { fixture.now = new Date('2026-08-07T23:59:59Z'); }],
    ['changed policy bytes', (fixture: ReturnType<typeof readinessFixture>) => { fixture.policyBytes = Buffer.from('changed-policy'); }],
    ['changed stable executable bytes', (fixture: ReturnType<typeof readinessFixture>) => { fixture.stableArtifactBytes = Buffer.from('substituted stable executable'); }],
    ['changed KMS audit', (fixture: ReturnType<typeof readinessFixture>) => { fixture.auditBytes = Buffer.from('changed-audit'); }],
    ['different immutable policy Release', (fixture: ReturnType<typeof readinessFixture>) => {
      const release = JSON.parse(fixture.policyReleaseBytes.toString()); release.id = 777; fixture.policyReleaseBytes = Buffer.from(JSON.stringify(release));
    }],
    ['future trust review time', (fixture: ReturnType<typeof readinessFixture>) => {
      const attestation = JSON.parse(fixture.trustAttestationBytes.toString()); attestation.reviewedAt = '2026-08-09T00:00:00Z'; fixture.trustAttestationBytes = Buffer.from(JSON.stringify(attestation)); fixture.expectedTrustAttestationSHA256 = sha256(fixture.trustAttestationBytes);
    }],
  ])('rejects %s', (_name, mutate) => {
    const fixture = readinessFixture(); mutate(fixture);
    expect(() => verifyBridgeReadiness(fixture)).toThrow(/bridge readiness evidence is invalid/);
  });

  it('rejects the known Task 1 test root and policy digests even when reviewed inputs claim them', () => {
    const fixture = readinessFixture();
    fixture.rootSPKI = readFileSync(new URL('../goserver/testdata/update-trust/root-epoch-1-spki.der', import.meta.url));
    fixture.policyBytes = readFileSync(new URL('../goserver/testdata/update-trust/policy-epoch-1.json', import.meta.url));
    expect(() => verifyBridgeReadiness(fixture)).toThrow(/test fixture/);
  });

  it('executes the workflow filesystem layout with the exact private bundle paths', () => {
    const fixture = readinessFixture();
    const root = mkdtempSync(join(tmpdir(), 'bridge-readiness-layout-'));
    try {
      const readiness = join(root, 'bridge-readiness');
      const bundle = join(readiness, 'private-bundle', 'bundle');
      mkdirSync(bundle, { recursive: true });
      const writes = new Map<string, Buffer>([
        [join(readiness, 'stable-release.json'), fixture.stableReleaseBytes],
        [join(readiness, 'stable-artifact.exe'), fixture.stableArtifactBytes],
        [join(readiness, 'stable-checksum.txt'), fixture.stableChecksumBytes],
        [join(readiness, 'observation-evidence.json'), fixture.observationEvidenceBytes],
        [join(readiness, 'root-spki.der'), fixture.rootSPKI],
        [join(bundle, 'policy.json'), fixture.policyBytes],
        [join(bundle, 'audit.json'), fixture.auditBytes],
        [join(bundle, 'commit.json'), fixture.commitBytes],
        [join(readiness, 'verified-bundle.json'), fixture.verifiedBundleBytes],
        [join(readiness, 'policy-release.json'), fixture.policyReleaseBytes],
        [join(readiness, 'trust-attestation.json'), fixture.trustAttestationBytes],
      ]);
      for (const [path, bytes] of writes) writeFileSync(path, bytes);
      const result = spawnSync(process.execPath, [
        fileURLToPath(new URL('../scripts/bridge-release-inputs.mjs', import.meta.url)),
        'verify',
        '--stable-release', join(readiness, 'stable-release.json'),
        '--stable-artifact', join(readiness, 'stable-artifact.exe'),
        '--stable-checksum', join(readiness, 'stable-checksum.txt'),
        '--observation-evidence', join(readiness, 'observation-evidence.json'),
        '--observation-sha256', fixture.expectedObservationSHA256,
        '--root-spki', join(readiness, 'root-spki.der'),
        '--policy', join(bundle, 'policy.json'),
        '--audit', join(bundle, 'audit.json'),
        '--verified-bundle', join(readiness, 'verified-bundle.json'),
        '--policy-release', join(readiness, 'policy-release.json'),
        '--trust-attestation', join(readiness, 'trust-attestation.json'),
        '--trust-attestation-sha256', fixture.expectedTrustAttestationSHA256,
      ], { cwd: root, encoding: 'utf8' });
      expect(result.status, `${result.stdout}\n${result.stderr}`).toBe(0);
      expect(JSON.parse(result.stdout)).toMatchObject({ stableArtifactSha256: sha256(fixture.stableArtifactBytes) });
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
