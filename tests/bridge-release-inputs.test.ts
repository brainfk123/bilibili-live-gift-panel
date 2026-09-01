import { createHash, generateKeyPairSync, sign, type KeyObject } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { verifyBridgeReadiness } from '../scripts/bridge-release-inputs.mjs';
import { publishTrustPolicy } from '../scripts/publish-trust-policy.mjs';
import { mapPolicyReleaseToLocalBundle } from '../scripts/publisher-policy-release-contract.mjs';

const sha256 = (value: Buffer) => createHash('sha256').update(value).digest('hex');

function signedPolicy(privateKey: KeyObject, signed: Record<string, unknown>) {
  const signature = sign('sha256', Buffer.from(JSON.stringify(signed)), privateKey).toString('base64');
  return Buffer.from(JSON.stringify({ signed, signatures: [{ algorithm: 'ecdsa-p256-sha256', signature }] }));
}

function readinessFixture(manifestScoped = true) {
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const rootSPKI = Buffer.from(publicKey.export({ format: 'der', type: 'spki' }));
  const stableArtifactBytes = Buffer.from('actual immutable v0.4.12 executable fixture');
  const stableArtifactSHA256 = sha256(stableArtifactBytes);
  const bootstrapPolicyBytes = signedPolicy(privateKey, {
    epoch: 1, expiresAt: '2030-01-01T00:00:00Z', publishers: [{
      id: 'rushrush-bridge', role: 'bridge', country: 'CN', organization: 'RushRush Network Technology Ltd', organizationId: '91450900MADM3GLG5P', allowedChannel: 'legacy-rushrush', allowedTags: ['v0.4.11'],
    }],
  });
  const authorizationPublisher: Record<string, unknown> = {
    id: 'naisnet-primary', role: 'primary', country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094', allowedChannel: 'stable', allowedTags: ['v0.4.12'],
  };
  if (manifestScoped) authorizationPublisher.manifestSha256 = stableArtifactSHA256;
  const authorizationPolicyBytes = signedPolicy(privateKey, {
    epoch: 2, expiresAt: '2030-01-01T00:00:00Z', publishers: [authorizationPublisher],
  });
  const authorizationAuditBytes = Buffer.from(JSON.stringify({
    keyId: 'kms-production-key', epoch: 2, policySha256: sha256(authorizationPolicyBytes), requestId: 'kms-request-2', utc: '2026-08-09T00:00:00Z', ciActor: 'release-reviewer',
  }));
  const commitBytes = Buffer.from(JSON.stringify({
    schemaVersion: 1,
    policy: { name: 'policy.json', length: authorizationPolicyBytes.length, sha256: sha256(authorizationPolicyBytes) },
    audit: { name: 'audit.json', length: authorizationAuditBytes.length, sha256: sha256(authorizationAuditBytes) },
  }));
  const checksumBytes = Buffer.from(`${stableArtifactSHA256}  gift-panel-windows-x64.exe`);
  const stableRelease = {
    id: 412, tag_name: 'v0.4.12', draft: false, prerelease: false, published_at: '2026-08-01T00:00:00Z',
    assets: [
      { name: 'gift-panel-windows-x64.exe', size: stableArtifactBytes.length, digest: `sha256:${stableArtifactSHA256}`, content_type: 'application/octet-stream', url: 'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/4101' },
      { name: 'gift-panel-windows-x64.exe.sha256', size: checksumBytes.length, digest: `sha256:${sha256(checksumBytes)}`, content_type: 'text/plain', url: 'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/4102' },
    ],
  };
  const observation = {
    schemaVersion: 1,
    stableRelease: { id: 412, tag: 'v0.4.12', publishedAt: '2026-08-01T00:00:00Z', executableSha256: stableArtifactSHA256 },
    observation: { endedAt: '2026-08-08T00:00:00Z', result: 'passed' }, reviewedAt: '2026-08-08T01:00:00Z',
  };
  const authorizationPolicyRelease = {
    id: 502, tag_name: 'publisher-policy-epoch-00000002', draft: false, prerelease: false, published_at: '2026-08-09T00:00:00Z',
    assets: [
      { name: 'gift-panel-publisher-policy.json', size: authorizationPolicyBytes.length, digest: `sha256:${sha256(authorizationPolicyBytes)}`, content_type: 'application/json', url: 'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/5001' },
      { name: 'gift-panel-publisher-policy.audit.json', size: authorizationAuditBytes.length, digest: `sha256:${sha256(authorizationAuditBytes)}`, content_type: 'application/json', url: 'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/5002' },
      { name: 'gift-panel-publisher-policy.commit.json', size: commitBytes.length, digest: `sha256:${sha256(commitBytes)}`, content_type: 'application/json', url: 'https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/5003' },
    ],
  };
  const attestation = {
    schemaVersion: 1, rootSpkiSha256: sha256(rootSPKI),
    authorizationPolicy: { epoch: 2, sha256: sha256(authorizationPolicyBytes) },
    kms: { keyId: 'kms-production-key', auditSha256: sha256(authorizationAuditBytes), requestId: 'kms-request-2' },
    policyRelease: { id: 502, tag: 'publisher-policy-epoch-00000002', publishedAt: '2026-08-09T00:00:00Z', policyAsset: 'gift-panel-publisher-policy.json', auditAsset: 'gift-panel-publisher-policy.audit.json', commitAsset: 'gift-panel-publisher-policy.commit.json' },
    reviewedAt: '2026-08-09T01:00:00Z',
  };
  const observationEvidenceBytes = Buffer.from(JSON.stringify(observation));
  const trustAttestationBytes = Buffer.from(JSON.stringify(attestation));
  const authorizationVerifiedBundleBytes = Buffer.from(JSON.stringify({
    schemaVersion: 2, verification: { epoch: 2, expectedPreviousEpoch: 1, spkiSha256: sha256(rootSPKI) }, commit: JSON.parse(commitBytes.toString()),
    policy: { name: 'policy.json', length: authorizationPolicyBytes.length, sha256: sha256(authorizationPolicyBytes), bytesBase64: authorizationPolicyBytes.toString('base64') },
    audit: { name: 'audit.json', length: authorizationAuditBytes.length, sha256: sha256(authorizationAuditBytes), bytesBase64: authorizationAuditBytes.toString('base64') },
    commitArtifact: { name: 'commit.json', length: commitBytes.length, sha256: sha256(commitBytes), bytesBase64: commitBytes.toString('base64') },
  }));
  const authorizationEvidenceBytes = Buffer.from(JSON.stringify({
    policyEpoch: 2, policySha256: sha256(authorizationPolicyBytes), stableTag: 'v0.4.12', stableChannel: 'stable', stableArtifactSha256: stableArtifactSHA256,
    stableIdentity: { country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094' },
  }));
  return {
    now: new Date('2026-08-09T02:00:00Z'), stableReleaseBytes: Buffer.from(JSON.stringify(stableRelease)), stableArtifactBytes, stableChecksumBytes: checksumBytes,
    observationEvidenceBytes, expectedObservationSHA256: sha256(observationEvidenceBytes), rootSPKI,
    bootstrapPolicyBytes, bootstrapPolicyEpoch: 1, expectedBootstrapPolicySHA256: sha256(bootstrapPolicyBytes),
    authorizationPolicyBytes, authorizationAuditBytes, authorizationVerifiedBundleBytes,
    authorizationPolicyReleaseBytes: Buffer.from(JSON.stringify(authorizationPolicyRelease)), authorizationEvidenceBytes,
    commitBytes, trustAttestationBytes, expectedTrustAttestationSHA256: sha256(trustAttestationBytes),
  };
}

describe('bridge readiness reviewed evidence', () => {
  it('adapts the actual Task9 publisher closure into the strict bridge bundle and readiness gate', async () => {
    const fixture = readinessFixture();
    let published: { tag: string; assets: Array<{ name: string; bytes: Buffer; sha256: string }> } | undefined;
    const cos = new Map<string, Buffer>();
    await publishTrustPolicy({
      mode: 'publish-immutable', policyPath: 'bundle/policy.json', auditPath: 'bundle/audit.json', reviewedSPKIPath: 'root.der',
      expectedSPKISHA256: sha256(fixture.rootSPKI), expectedPreviousEpoch: 1, advanceDiscovery: false, now: fixture.now,
    }, {
      process: { run: async () => ({ code: 0, stdout: `${fixture.authorizationVerifiedBundleBytes.toString()}\n`, stderr: '' }) },
      files: { readFile: async () => fixture.rootSPKI },
      cos: {
        putImmutable: async (key: string, bytes: Buffer) => { cos.set(key, Buffer.from(bytes)); },
        read: async (key: string) => { const bytes = cos.get(key); return bytes ? { bytes, version: 'v1', sha256: sha256(bytes), contentType: 'application/json' } : null; },
        compareAndSwapPointer: async () => {},
      },
      github: {
        publishImmutableRelease: async (release: typeof published) => { published = release; },
        downloadReleaseAsset: async (tag: string, name: string) => Buffer.from(published!.tag === tag ? published!.assets.find((asset) => asset.name === name)!.bytes : []),
        readPointer: async () => null,
        compareAndSwapPointer: async () => {},
      },
    });
    expect(published).toBeDefined();
    const release = {
      id: 502, tag_name: published!.tag, draft: false, prerelease: false, published_at: '2026-08-09T00:00:00Z',
      assets: published!.assets.map((asset, index) => ({ name: asset.name, size: asset.bytes.length, digest: `sha256:${asset.sha256}`, content_type: 'application/json', url: `https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/assets/${5001 + index}` })),
    };
    const local = mapPolicyReleaseToLocalBundle(release, new Map(published!.assets.map((asset) => [asset.name, asset.bytes])));
    expect(Object.keys(local).sort()).toEqual(['audit', 'commit', 'policy']);
    fixture.authorizationPolicyBytes = local.policy;
    fixture.authorizationAuditBytes = local.audit;
    fixture.commitBytes = local.commit;
    fixture.authorizationPolicyReleaseBytes = Buffer.from(JSON.stringify(release));
    expect(verifyBridgeReadiness(fixture)).toMatchObject({ authorizationPolicyEpoch: 2, authorizationPolicyReleaseId: 502 });
  });

  it('binds embedded bootstrap and a higher exact-hash authorization policy', () => {
    const fixture = readinessFixture();
    expect(verifyBridgeReadiness(fixture)).toMatchObject({
      stableReleaseId: 412, bootstrapPolicyEpoch: 1, authorizationPolicyReleaseId: 502, authorizationPolicyEpoch: 2,
      authorizationPolicySha256: sha256(fixture.authorizationPolicyBytes), stableArtifactSha256: sha256(fixture.stableArtifactBytes),
    });
  });

  it.each([
    ['bootstrap reused as authorization', (fixture: ReturnType<typeof readinessFixture>) => { fixture.bootstrapPolicyBytes = fixture.authorizationPolicyBytes; fixture.expectedBootstrapPolicySHA256 = sha256(fixture.bootstrapPolicyBytes); fixture.bootstrapPolicyEpoch = 2; }],
    ['non-advancing authorization epoch', (fixture: ReturnType<typeof readinessFixture>) => { fixture.bootstrapPolicyEpoch = 2; }],
    ['wrong candidate hash in shared authorization evidence', (fixture: ReturnType<typeof readinessFixture>) => { const evidence = JSON.parse(fixture.authorizationEvidenceBytes.toString()); evidence.stableArtifactSha256 = '0'.repeat(64); fixture.authorizationEvidenceBytes = Buffer.from(JSON.stringify(evidence)); }],
    ['changed stable executable bytes', (fixture: ReturnType<typeof readinessFixture>) => { fixture.stableArtifactBytes = Buffer.from('substituted stable executable'); }],
    ['changed authorization policy bytes', (fixture: ReturnType<typeof readinessFixture>) => { fixture.authorizationPolicyBytes = Buffer.from('changed-policy'); }],
    ['different immutable policy Release', (fixture: ReturnType<typeof readinessFixture>) => { const release = JSON.parse(fixture.authorizationPolicyReleaseBytes.toString()); release.id = 777; fixture.authorizationPolicyReleaseBytes = Buffer.from(JSON.stringify(release)); }],
  ])('rejects %s', (_name, mutate) => { const fixture = readinessFixture(); mutate(fixture); expect(() => verifyBridgeReadiness(fixture)).toThrow(/bridge readiness evidence is invalid/); });

  it('rejects the known Task 1 test root and policy digests', () => {
    const fixture = readinessFixture();
    fixture.rootSPKI = readFileSync(new URL('../goserver/testdata/update-trust/root-epoch-1-spki.der', import.meta.url));
    fixture.authorizationPolicyBytes = readFileSync(new URL('../goserver/testdata/update-trust/policy-epoch-1.json', import.meta.url));
    expect(() => verifyBridgeReadiness(fixture)).toThrow(/test fixture/);
  });

  it('executes the two-stage workflow filesystem layout', () => {
    const fixture = readinessFixture(); const root = mkdtempSync(join(tmpdir(), 'bridge-readiness-layout-'));
    try {
      const readiness = join(root, 'bridge-readiness'); const bundle = join(readiness, 'private-bundle', 'bundle'); mkdirSync(bundle, { recursive: true });
      const writes = new Map<string, Buffer>([
        [join(readiness, 'stable-release.json'), fixture.stableReleaseBytes], [join(readiness, 'stable-artifact.exe'), fixture.stableArtifactBytes], [join(readiness, 'stable-checksum.txt'), fixture.stableChecksumBytes],
        [join(readiness, 'observation-evidence.json'), fixture.observationEvidenceBytes], [join(readiness, 'root-spki.der'), fixture.rootSPKI], [join(readiness, 'bootstrap-policy.json'), fixture.bootstrapPolicyBytes],
        [join(bundle, 'policy.json'), fixture.authorizationPolicyBytes], [join(bundle, 'audit.json'), fixture.authorizationAuditBytes], [join(bundle, 'commit.json'), fixture.commitBytes],
        [join(readiness, 'verified-bundle.json'), fixture.authorizationVerifiedBundleBytes], [join(readiness, 'policy-release.json'), fixture.authorizationPolicyReleaseBytes],
        [join(readiness, 'authorization-evidence.json'), fixture.authorizationEvidenceBytes], [join(readiness, 'trust-attestation.json'), fixture.trustAttestationBytes],
      ]);
      for (const [path, bytes] of writes) writeFileSync(path, bytes);
      const result = spawnSync(process.execPath, [
        fileURLToPath(new URL('../scripts/bridge-release-inputs.mjs', import.meta.url)), 'verify',
        '--stable-release', join(readiness, 'stable-release.json'), '--stable-artifact', join(readiness, 'stable-artifact.exe'), '--stable-checksum', join(readiness, 'stable-checksum.txt'),
        '--observation-evidence', join(readiness, 'observation-evidence.json'), '--observation-sha256', fixture.expectedObservationSHA256,
        '--root-spki', join(readiness, 'root-spki.der'), '--bootstrap-policy', join(readiness, 'bootstrap-policy.json'), '--bootstrap-policy-sha256', fixture.expectedBootstrapPolicySHA256, '--bootstrap-policy-epoch', String(fixture.bootstrapPolicyEpoch),
        '--authorization-policy', join(bundle, 'policy.json'), '--authorization-audit', join(bundle, 'audit.json'), '--authorization-verified-bundle', join(readiness, 'verified-bundle.json'),
        '--authorization-policy-release', join(readiness, 'policy-release.json'), '--authorization-evidence', join(readiness, 'authorization-evidence.json'),
        '--trust-attestation', join(readiness, 'trust-attestation.json'), '--trust-attestation-sha256', fixture.expectedTrustAttestationSHA256,
      ], { cwd: root, encoding: 'utf8' });
      expect(result.status, `${result.stdout}\n${result.stderr}`).toBe(0); expect(JSON.parse(result.stdout)).toMatchObject({ bootstrapPolicyEpoch: 1, authorizationPolicyEpoch: 2 });
    } finally { rmSync(root, { recursive: true, force: true }); }
  });
});
