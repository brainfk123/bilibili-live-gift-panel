import { createHash, createPublicKey, verify } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const KNOWN_TEST_ROOT_SHA256 = '5cd252fb0ce8932436faf8ccd1040981b89ee4ad6b9fe9e2a2b7e71aacb27cd3';
const KNOWN_TEST_POLICY_SHA256 = '205b8ea9bf7e79d55292d63a1266a4882ab01fa5edb3eb79421724ddb9265d0e';
const SECOND = 1_000;
const SEVEN_DAYS = 7 * 24 * 60 * 60 * SECOND;
const SHA256 = /^[0-9a-f]{64}$/;

const hash = (value) => createHash('sha256').update(value).digest('hex');
const fail = () => { throw new Error('bridge readiness evidence is invalid'); };

export function verifyBridgeReadiness(options) {
  const rootHash = hash(options.rootSPKI);
  const policyHash = hash(options.policyBytes);
  if (rootHash === KNOWN_TEST_ROOT_SHA256 || policyHash === KNOWN_TEST_POLICY_SHA256) {
    throw new Error('known Task 1 test fixture trust material is forbidden');
  }
  const observationHash = hash(options.observationEvidenceBytes);
  const attestationHash = hash(options.trustAttestationBytes);
  if (!SHA256.test(options.expectedObservationSHA256) || observationHash !== options.expectedObservationSHA256 ||
      !SHA256.test(options.expectedTrustAttestationSHA256) || attestationHash !== options.expectedTrustAttestationSHA256) fail();
  const stableRelease = parseExactJSON(options.stableReleaseBytes, ['id', 'tag_name', 'draft', 'prerelease', 'published_at']);
  const observation = parseExactJSON(options.observationEvidenceBytes, ['schemaVersion', 'stableRelease', 'observation', 'reviewedAt']);
  const attestation = parseExactJSON(options.trustAttestationBytes, ['schemaVersion', 'rootSpkiSha256', 'policy', 'kms', 'policyRelease', 'reviewedAt']);
  const policyRelease = parseExactJSON(options.policyReleaseBytes, ['id', 'tag_name', 'draft', 'prerelease', 'published_at', 'assets']);
  const audit = parseExactJSON(options.auditBytes, ['keyId', 'epoch', 'policySha256', 'requestId', 'utc', 'ciActor']);
  const now = options.now instanceof Date && Number.isFinite(options.now.getTime()) ? options.now.getTime() : fail();

  if (!Number.isSafeInteger(stableRelease.id) || stableRelease.id <= 0 || stableRelease.tag_name !== 'v0.4.12' || stableRelease.draft !== false || stableRelease.prerelease !== false) fail();
  const publishedAt = exactTime(stableRelease.published_at);
  exactObject(observation.stableRelease, ['id', 'tag', 'publishedAt']);
  exactObject(observation.observation, ['endedAt', 'result']);
  const observationEnd = exactTime(observation.observation.endedAt);
  const observationReview = exactTime(observation.reviewedAt);
  if (observation.schemaVersion !== 1 || observation.stableRelease.id !== stableRelease.id || observation.stableRelease.tag !== stableRelease.tag_name ||
      observation.stableRelease.publishedAt !== stableRelease.published_at || observation.observation.result !== 'passed' ||
      observationEnd < publishedAt + SEVEN_DAYS || observationReview < observationEnd || now < publishedAt + SEVEN_DAYS || now < observationReview) fail();

  exactObject(attestation.policy, ['epoch', 'sha256']);
  exactObject(attestation.kms, ['keyId', 'auditSha256', 'requestId']);
  exactObject(attestation.policyRelease, ['id', 'tag', 'publishedAt', 'policyAsset', 'auditAsset']);
  if (attestation.schemaVersion !== 1 || attestation.rootSpkiSha256 !== rootHash || attestation.policy.epoch !== 1 || attestation.policy.sha256 !== policyHash ||
      !/^[A-Za-z0-9_-]{1,128}$/.test(attestation.kms.keyId) || attestation.kms.auditSha256 !== hash(options.auditBytes) ||
      attestation.kms.requestId !== audit.requestId || audit.keyId !== attestation.kms.keyId || audit.epoch !== 1 || audit.policySha256 !== policyHash) fail();
  const trustReviewedAt = exactTime(attestation.reviewedAt);
  const auditTime = exactTime(audit.utc);

  if (!Number.isSafeInteger(policyRelease.id) || policyRelease.id <= 0 || policyRelease.tag_name !== 'publisher-policy-epoch-00000001' || policyRelease.draft !== false || policyRelease.prerelease !== false) fail();
  const policyPublishedAt = exactTime(policyRelease.published_at);
  if (trustReviewedAt < policyPublishedAt || trustReviewedAt < auditTime || now < trustReviewedAt) fail();
  if (attestation.policyRelease.id !== policyRelease.id || attestation.policyRelease.tag !== policyRelease.tag_name ||
      attestation.policyRelease.publishedAt !== policyRelease.published_at || attestation.policyRelease.policyAsset !== 'policy.json' || attestation.policyRelease.auditAsset !== 'audit.json') fail();
  const releaseAssets = new Map();
  if (!Array.isArray(policyRelease.assets) || policyRelease.assets.length !== 2) fail();
  for (const asset of policyRelease.assets) {
    exactObject(asset, ['name', 'size', 'digest']);
    if (!['policy.json', 'audit.json'].includes(asset.name) || releaseAssets.has(asset.name) || !Number.isSafeInteger(asset.size) || asset.size <= 0 || !/^sha256:[0-9a-f]{64}$/.test(asset.digest)) fail();
    releaseAssets.set(asset.name, asset);
  }
  const policyAsset = releaseAssets.get('policy.json');
  const auditAsset = releaseAssets.get('audit.json');
  if (policyAsset.size !== options.policyBytes.length || policyAsset.digest !== `sha256:${policyHash}` ||
      auditAsset.size !== options.auditBytes.length || auditAsset.digest !== `sha256:${hash(options.auditBytes)}`) fail();

  verifyPolicy(options.policyBytes, options.rootSPKI);
  return {
    schemaVersion: 1,
    stableReleaseId: stableRelease.id,
    stablePublishedAt: stableRelease.published_at,
    observationEndedAt: observation.observation.endedAt,
    observationEvidenceSha256: observationHash,
    policyReleaseId: policyRelease.id,
    policyEpoch: 1,
    policySha256: policyHash,
    rootSpkiSha256: rootHash,
    kmsKeyId: audit.keyId,
    kmsRequestId: audit.requestId,
    trustAttestationSha256: attestationHash,
  };
}

function verifyPolicy(policyBytes, rootSPKI) {
  const document = parseExactJSON(policyBytes, ['signed', 'signatures']);
  exactObject(document.signed, ['epoch', 'expiresAt', 'publishers']);
  if (document.signed.epoch !== 1 || !Array.isArray(document.signed.publishers) || !Array.isArray(document.signatures) || document.signatures.length !== 1) fail();
  const signature = document.signatures[0];
  exactObject(signature, ['algorithm', 'signature']);
  if (signature.algorithm !== 'ecdsa-p256-sha256') fail();
  const stable = document.signed.publishers.filter((publisher) => {
    exactPublisher(publisher);
    return publisher.role === 'primary' && publisher.country === 'CN' && publisher.organization === 'NaisNet Technology Co., Ltd.' &&
      publisher.organizationId === '91210103MA7CJ3C094' && publisher.allowedChannel === 'stable' &&
      Array.isArray(publisher.allowedTags) && publisher.allowedTags.filter((tag) => tag === 'v0.4.12').length === 1;
  });
  if (stable.length !== 1) fail();
  let key;
  try { key = createPublicKey({ key: rootSPKI, format: 'der', type: 'spki' }); } catch { fail(); }
  const jwk = key.export({ format: 'jwk' });
  if (key.asymmetricKeyType !== 'ec' || jwk.crv !== 'P-256') fail();
  let signatureBytes;
  try { signatureBytes = Buffer.from(signature.signature, 'base64'); } catch { fail(); }
  if (signatureBytes.length === 0 || signatureBytes.toString('base64') !== signature.signature || !verify('sha256', Buffer.from(JSON.stringify(document.signed)), key, signatureBytes)) fail();
}

function exactPublisher(publisher) {
  const required = ['id', 'role', 'country', 'organization', 'organizationId', 'allowedChannel', 'allowedTags'];
  const keys = Object.keys(publisher ?? {});
  if (!publisher || typeof publisher !== 'object' || Array.isArray(publisher) || required.some((key) => !Object.hasOwn(publisher, key)) ||
      keys.some((key) => !required.includes(key) && key !== 'manifestSha256') || keys.length < required.length || keys.length > required.length + 1 ||
      (Object.hasOwn(publisher, 'manifestSha256') && !SHA256.test(publisher.manifestSha256))) fail();
}

function parseExactJSON(bytes, keys) {
  let value;
  try { value = JSON.parse(bytes.toString('utf8')); } catch { fail(); }
  exactObject(value, keys);
  if (!Buffer.from(JSON.stringify(value)).equals(Buffer.from(bytes))) fail();
  return value;
}

function exactObject(value, keys) {
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).length !== keys.length || keys.some((key) => !Object.hasOwn(value, key))) fail();
}

function exactTime(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(value)) fail();
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed) || new Date(parsed).toISOString().replace('.000Z', 'Z') !== value) fail();
  return parsed;
}

function argument(name) {
  const index = process.argv.indexOf(name);
  if (index < 0 || !process.argv[index + 1]) fail();
  return process.argv[index + 1];
}

async function main() {
  if (process.argv[2] !== 'verify') fail();
  const summary = verifyBridgeReadiness({
    now: new Date(),
    stableReleaseBytes: await readFile(argument('--stable-release')),
    observationEvidenceBytes: await readFile(argument('--observation-evidence')),
    expectedObservationSHA256: argument('--observation-sha256'),
    rootSPKI: await readFile(argument('--root-spki')),
    policyBytes: await readFile(argument('--policy')),
    auditBytes: await readFile(argument('--audit')),
    policyReleaseBytes: await readFile(argument('--policy-release')),
    trustAttestationBytes: await readFile(argument('--trust-attestation')),
    expectedTrustAttestationSHA256: argument('--trust-attestation-sha256'),
  });
  process.stdout.write(`${JSON.stringify(summary)}\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(() => { console.error('bridge readiness verification failed'); process.exitCode = 1; });
}
