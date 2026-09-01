import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { POLICY_RELEASE_ASSET_CONTRACT } from './publisher-policy-release-contract.mjs';

const KNOWN_TEST_ROOT_SHA256 = '5cd252fb0ce8932436faf8ccd1040981b89ee4ad6b9fe9e2a2b7e71aacb27cd3';
const KNOWN_TEST_POLICY_SHA256 = '205b8ea9bf7e79d55292d63a1266a4882ab01fa5edb3eb79421724ddb9265d0e';
const SEVEN_DAYS = 7 * 24 * 60 * 60 * 1_000;
const SHA256 = /^[0-9a-f]{64}$/;
const hash = (value) => createHash('sha256').update(value).digest('hex');
const fail = () => { throw new Error('bridge readiness evidence is invalid'); };

export function verifyBridgeReadiness(options) {
  const rootHash = hash(options.rootSPKI);
  const bootstrapHash = hash(options.bootstrapPolicyBytes);
  const authorizationHash = hash(options.authorizationPolicyBytes);
  if (rootHash === KNOWN_TEST_ROOT_SHA256 || authorizationHash === KNOWN_TEST_POLICY_SHA256) {
    throw new Error('known Task 1 test fixture trust material is forbidden');
  }
  if (!Number.isSafeInteger(options.bootstrapPolicyEpoch) || options.bootstrapPolicyEpoch <= 0 ||
    !SHA256.test(options.expectedBootstrapPolicySHA256) || bootstrapHash !== options.expectedBootstrapPolicySHA256 ||
    bootstrapHash === authorizationHash) fail();

  const stableRelease = parseExactJSON(options.stableReleaseBytes, ['id', 'tag_name', 'draft', 'prerelease', 'published_at', 'assets']);
  const observation = parseExactJSON(options.observationEvidenceBytes, ['schemaVersion', 'stableRelease', 'observation', 'reviewedAt']);
  const attestation = parseExactJSON(options.trustAttestationBytes, ['schemaVersion', 'rootSpkiSha256', 'authorizationPolicy', 'kms', 'policyRelease', 'reviewedAt']);
  const policyRelease = parseExactJSON(options.authorizationPolicyReleaseBytes, ['id', 'tag_name', 'draft', 'prerelease', 'published_at', 'assets']);
  const audit = parseExactJSON(options.authorizationAuditBytes, ['keyId', 'epoch', 'policySha256', 'requestId', 'utc', 'ciActor']);
  const authorizationEvidence = parseExactJSON(options.authorizationEvidenceBytes, ['policyEpoch', 'policySha256', 'stableTag', 'stableChannel', 'stableArtifactSha256', 'stableIdentity']);
  const now = options.now instanceof Date && Number.isFinite(options.now.getTime()) ? options.now.getTime() : fail();

  if (!Number.isSafeInteger(stableRelease.id) || stableRelease.id <= 0 || stableRelease.tag_name !== 'v0.4.12' || stableRelease.draft !== false || stableRelease.prerelease !== false) fail();
  const stablePublishedAt = exactTime(stableRelease.published_at);
  exactObject(observation.stableRelease, ['id', 'tag', 'publishedAt', 'executableSha256']);
  exactObject(observation.observation, ['endedAt', 'result']);
  const observationEnd = exactTime(observation.observation.endedAt);
  const observationReview = exactTime(observation.reviewedAt);
  if (!Array.isArray(stableRelease.assets) || stableRelease.assets.length !== 2) fail();
  const stableAssets = new Map();
  for (const asset of stableRelease.assets) {
    exactObject(asset, ['name', 'size', 'digest', 'content_type', 'url']);
    if (stableAssets.has(asset.name)) fail();
    stableAssets.set(asset.name, asset);
  }
  const stableExecutable = stableAssets.get('gift-panel-windows-x64.exe');
  const stableChecksum = stableAssets.get('gift-panel-windows-x64.exe.sha256');
  if (!stableExecutable || !stableChecksum || !Buffer.isBuffer(options.stableArtifactBytes) ||
    stableExecutable.size !== options.stableArtifactBytes.length || stableExecutable.digest !== `sha256:${hash(options.stableArtifactBytes)}` || stableExecutable.content_type !== 'application/octet-stream' ||
    stableChecksum.content_type !== 'text/plain' || stableChecksum.size !== options.stableChecksumBytes.length || stableChecksum.digest !== `sha256:${hash(options.stableChecksumBytes)}`) fail();
  const stableArtifactSHA256 = stableExecutable.digest.slice(7);
  if (options.stableChecksumBytes.toString('ascii') !== `${stableArtifactSHA256}  gift-panel-windows-x64.exe`) fail();
  if (observation.schemaVersion !== 1 || observation.stableRelease.id !== stableRelease.id || observation.stableRelease.tag !== stableRelease.tag_name ||
    observation.stableRelease.executableSha256 !== stableArtifactSHA256 || observation.stableRelease.publishedAt !== stableRelease.published_at || observation.observation.result !== 'passed' ||
    observationEnd < stablePublishedAt + SEVEN_DAYS || observationReview < observationEnd || now < observationReview) fail();
  if (!SHA256.test(options.expectedObservationSHA256) || hash(options.observationEvidenceBytes) !== options.expectedObservationSHA256) fail();

  exactObject(authorizationEvidence.stableIdentity, ['country', 'organization', 'organizationId']);
  if (!Number.isSafeInteger(authorizationEvidence.policyEpoch) || authorizationEvidence.policyEpoch <= options.bootstrapPolicyEpoch ||
    authorizationEvidence.policySha256 !== authorizationHash || authorizationEvidence.stableTag !== 'v0.4.12' || authorizationEvidence.stableChannel !== 'stable' ||
    authorizationEvidence.stableArtifactSha256 !== stableArtifactSHA256 || authorizationEvidence.stableIdentity.country !== 'CN' ||
    authorizationEvidence.stableIdentity.organization !== 'NaisNet Technology Co., Ltd.' || authorizationEvidence.stableIdentity.organizationId !== '91210103MA7CJ3C094') fail();
  const authorizationEpoch = authorizationEvidence.policyEpoch;

  exactObject(attestation.authorizationPolicy, ['epoch', 'sha256']);
  exactObject(attestation.kms, ['keyId', 'auditSha256', 'requestId']);
  exactObject(attestation.policyRelease, ['id', 'tag', 'publishedAt', 'policyAsset', 'auditAsset', 'commitAsset']);
  if (attestation.schemaVersion !== 1 || attestation.rootSpkiSha256 !== rootHash || attestation.authorizationPolicy.epoch !== authorizationEpoch ||
    attestation.authorizationPolicy.sha256 !== authorizationHash || !/^[A-Za-z0-9_-]{1,128}$/.test(attestation.kms.keyId) ||
    !/^[A-Za-z0-9_.:@/-]{1,256}$/.test(attestation.kms.requestId) || attestation.kms.auditSha256 !== hash(options.authorizationAuditBytes) ||
    attestation.kms.requestId !== audit.requestId || audit.keyId !== attestation.kms.keyId || audit.epoch !== authorizationEpoch || audit.policySha256 !== authorizationHash) fail();
  if (!/^[A-Za-z0-9_.:@/-]{1,256}$/.test(audit.requestId) || !/^[A-Za-z0-9_.\[\]-]{1,100}$/.test(audit.ciActor)) fail();

  const fixedEpoch = String(authorizationEpoch).padStart(8, '0');
  const policyTag = `publisher-policy-epoch-${fixedEpoch}`;
  if (!Number.isSafeInteger(policyRelease.id) || policyRelease.id <= 0 || policyRelease.tag_name !== policyTag || policyRelease.draft !== false || policyRelease.prerelease !== false) fail();
  const policyPublishedAt = exactTime(policyRelease.published_at);
  const trustReviewedAt = exactTime(attestation.reviewedAt);
  if (trustReviewedAt < policyPublishedAt || trustReviewedAt < exactTime(audit.utc) || now < trustReviewedAt) fail();
  if (attestation.policyRelease.id !== policyRelease.id || attestation.policyRelease.tag !== policyTag || attestation.policyRelease.publishedAt !== policyRelease.published_at) fail();
  const releaseAssets = validatePolicyReleaseAssets(policyRelease.assets);
  for (const contract of POLICY_RELEASE_ASSET_CONTRACT) {
    if (attestation.policyRelease[`${contract.role}Asset`] !== contract.remoteName) fail();
  }
  const actualByRole = {
    policy: options.authorizationPolicyBytes,
    audit: options.authorizationAuditBytes,
    commit: Buffer.from(options.authorizationVerifiedBundleBytes.length ? exactCommitBytes(options.authorizationVerifiedBundleBytes) : []),
  };
  for (const contract of POLICY_RELEASE_ASSET_CONTRACT) {
    const asset = releaseAssets.get(contract.remoteName);
    const actual = actualByRole[contract.role];
    if (asset.size !== actual.length || asset.digest !== `sha256:${hash(actual)}`) fail();
  }
  verifyMachineBundle(options.authorizationVerifiedBundleBytes, options.authorizationPolicyBytes, options.authorizationAuditBytes, rootHash, authorizationEpoch, releaseAssets);
  if (!SHA256.test(options.expectedTrustAttestationSHA256) || hash(options.trustAttestationBytes) !== options.expectedTrustAttestationSHA256) fail();

  return {
    schemaVersion: 2,
    stableReleaseId: stableRelease.id,
    stablePublishedAt: stableRelease.published_at,
    stableArtifactSha256: stableArtifactSHA256,
    observationEndedAt: observation.observation.endedAt,
    observationEvidenceSha256: options.expectedObservationSHA256,
    rootSpkiSha256: rootHash,
    bootstrapPolicyEpoch: options.bootstrapPolicyEpoch,
    bootstrapPolicySha256: bootstrapHash,
    authorizationPolicyReleaseId: policyRelease.id,
    authorizationPolicyEpoch: authorizationEpoch,
    authorizationPolicySha256: authorizationHash,
    authorizationAuditSha256: hash(options.authorizationAuditBytes),
    authorizationCommitSha256: hash(actualByRole.commit),
    kmsKeyId: audit.keyId,
    kmsRequestId: audit.requestId,
    trustAttestationSha256: options.expectedTrustAttestationSHA256,
  };
}

function validatePolicyReleaseAssets(assets) {
  if (!Array.isArray(assets) || assets.length !== POLICY_RELEASE_ASSET_CONTRACT.length) fail();
  const result = new Map();
  for (const asset of assets) {
    exactObject(asset, ['name', 'size', 'digest', 'content_type', 'url']);
    const contract = POLICY_RELEASE_ASSET_CONTRACT.find((candidate) => candidate.remoteName === asset.name);
    if (!contract || result.has(asset.name) || !Number.isSafeInteger(asset.size) || asset.size <= 0 || asset.size > contract.maximumBytes ||
      !/^sha256:[0-9a-f]{64}$/.test(asset.digest) || asset.content_type !== contract.contentType ||
      !/^https:\/\/api\.github\.com\/repos\/brainfk123\/bilibili-live-gift-panel\/releases\/assets\/[1-9][0-9]*$/.test(asset.url)) fail();
    result.set(asset.name, asset);
  }
  return result;
}

function exactCommitBytes(machineBytes) {
  const machine = parseExactJSON(machineBytes, ['schemaVersion', 'verification', 'commit', 'policy', 'audit', 'commitArtifact']);
  exactObject(machine.commitArtifact, ['name', 'length', 'sha256', 'bytesBase64']);
  const bytes = decodeCanonicalBase64(machine.commitArtifact.bytesBase64);
  if (machine.commitArtifact.name !== 'commit.json' || machine.commitArtifact.length !== bytes.length || machine.commitArtifact.sha256 !== hash(bytes) ||
    !bytes.equals(Buffer.from(JSON.stringify(machine.commit)))) fail();
  return bytes;
}

function verifyMachineBundle(bytes, policyBytes, auditBytes, rootHash, epoch, releaseAssets) {
  const bundle = parseExactJSON(bytes, ['schemaVersion', 'verification', 'commit', 'policy', 'audit', 'commitArtifact']);
  exactObject(bundle.verification, ['epoch', 'expectedPreviousEpoch', 'spkiSha256']);
  exactObject(bundle.commit, ['schemaVersion', 'policy', 'audit']);
  for (const name of ['policy', 'audit']) {
    exactObject(bundle.commit[name], ['name', 'length', 'sha256']);
    exactObject(bundle[name], ['name', 'length', 'sha256', 'bytesBase64']);
  }
  if (bundle.schemaVersion !== 2 || bundle.verification.epoch !== epoch || bundle.verification.expectedPreviousEpoch !== epoch - 1 || bundle.verification.spkiSha256 !== rootHash || bundle.commit.schemaVersion !== 1) fail();
  for (const [name, actual] of [['policy', policyBytes], ['audit', auditBytes]]) {
    const artifact = bundle[name]; const committed = bundle.commit[name];
    if (artifact.name !== `${name}.json` || artifact.length !== actual.length || artifact.sha256 !== hash(actual) || artifact.bytesBase64 !== actual.toString('base64') ||
      JSON.stringify(committed) !== JSON.stringify({ name: `${name}.json`, length: actual.length, sha256: hash(actual) })) fail();
  }
  const commit = exactCommitBytes(bytes);
  const contract = POLICY_RELEASE_ASSET_CONTRACT.find((entry) => entry.role === 'commit');
  const asset = releaseAssets.get(contract.remoteName);
  if (asset.size !== commit.length || asset.digest !== `sha256:${hash(commit)}`) fail();
}

function decodeCanonicalBase64(value) {
  if (typeof value !== 'string' || value.length === 0 || value.length % 4 !== 0) fail();
  const decoded = Buffer.from(value, 'base64');
  if (decoded.toString('base64') !== value) fail();
  return decoded;
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
    stableReleaseBytes: await readFile(argument('--stable-release')), stableArtifactBytes: await readFile(argument('--stable-artifact')), stableChecksumBytes: await readFile(argument('--stable-checksum')),
    observationEvidenceBytes: await readFile(argument('--observation-evidence')), expectedObservationSHA256: argument('--observation-sha256'), rootSPKI: await readFile(argument('--root-spki')),
    bootstrapPolicyBytes: await readFile(argument('--bootstrap-policy')), expectedBootstrapPolicySHA256: argument('--bootstrap-policy-sha256'), bootstrapPolicyEpoch: Number(argument('--bootstrap-policy-epoch')),
    authorizationPolicyBytes: await readFile(argument('--authorization-policy')), authorizationAuditBytes: await readFile(argument('--authorization-audit')),
    authorizationVerifiedBundleBytes: await readFile(argument('--authorization-verified-bundle')), authorizationPolicyReleaseBytes: await readFile(argument('--authorization-policy-release')),
    authorizationEvidenceBytes: await readFile(argument('--authorization-evidence')), trustAttestationBytes: await readFile(argument('--trust-attestation')),
    expectedTrustAttestationSHA256: argument('--trust-attestation-sha256'),
  });
  process.stdout.write(`${JSON.stringify(summary)}\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch(() => { console.error('bridge readiness verification failed'); process.exitCode = 1; });
}
