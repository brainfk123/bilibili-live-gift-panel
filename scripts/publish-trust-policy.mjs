import { createHash, createHmac, createPublicKey, verify as verifySignature } from 'node:crypto';
import { execFile } from 'node:child_process';
import { readFile as readLocalFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import {
  POLICY_RELEASE_ASSET_CONTRACT,
  policyReleaseAssetForRemoteName,
  policyReleaseAssetForRole,
} from './publisher-policy-release-contract.mjs';

const MAX_MACHINE_ENVELOPE_BYTES = 512 << 10;
const MAX_POLICY_BYTES = 256 << 10;
const MAX_AUDIT_BYTES = 64 << 10;
const MAX_REMOTE_JSON_BYTES = 512 << 10;
const POLICY_ASSET = policyReleaseAssetForRole('policy').remoteName;
const AUDIT_ASSET = policyReleaseAssetForRole('audit').remoteName;
const COMMIT_ASSET = policyReleaseAssetForRole('commit').remoteName;
const COS_POINTER = 'trust/publisher/latest.json';
const GITHUB_POINTER_REF = 'refs/heads/publisher-trust';
const GITHUB_POINTER_PATH = POLICY_ASSET;
const SHA256 = /^[0-9a-f]{64}$/;
const CANONICAL_TAG = /^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const KEY_ID = /^[A-Za-z0-9_-]{1,128}$/;
const REQUEST_ID = /^[A-Za-z0-9_.:@/-]{1,256}$/;
const CI_ACTOR = /^[A-Za-z0-9_.\[\]-]{1,100}$/;
const RFC3339_SECONDS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/;
const GITHUB_RELEASE_ASSET_HOST = 'release-assets.githubusercontent.com';
const execFileAsync = promisify(execFile);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

class ValidationFailure extends Error {
  constructor() { super('publisher policy validation failed'); }
}
class PublicationFailure extends Error {
  constructor() { super('publisher policy publication failed'); }
}
class PointerCASConflict extends PublicationFailure {
  constructor() {
    super();
    this.code = 'publisher-pointer-cas-conflict';
  }
}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

async function readBoundedResponse(response, maximum) {
  if (!Number.isSafeInteger(maximum) || maximum < 0) throw new PublicationFailure();
  const rejectEarly = async () => {
    try {
      if (response?.body && !response.body.locked) await response.body.cancel();
    } catch { /* fixed bounded failure */ }
    throw new PublicationFailure();
  };
  const contentEncoding = (response.headers.get('content-encoding') ?? '').trim().toLowerCase();
  const compressed = contentEncoding === 'gzip' || contentEncoding === 'deflate' || contentEncoding === 'br';
  if (contentEncoding !== '' && contentEncoding !== 'identity' && !compressed) await rejectEarly();
  const declaredText = response.headers.get('content-length');
  let declared = null;
  if (declaredText !== null) {
    if (!/^(?:0|[1-9][0-9]*)$/.test(declaredText)) await rejectEarly();
    declared = Number(declaredText);
    if (!Number.isSafeInteger(declared) || declared > maximum) await rejectEarly();
  }
  if (response.body === null) {
    await rejectEarly();
  }
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!(value instanceof Uint8Array)) throw new PublicationFailure();
      total += value.length;
      if (total > maximum) throw new PublicationFailure();
      chunks.push(Buffer.from(value));
    }
  } catch {
    try { await reader.cancel(); } catch { /* bounded failure */ }
    throw new PublicationFailure();
  }
  if (!compressed && declared !== null && declared !== total) throw new PublicationFailure();
  return Buffer.concat(chunks, total);
}

async function readRemoteJSON(response, maximum = MAX_REMOTE_JSON_BYTES) {
  const bytes = await readBoundedResponse(response, maximum);
  let text;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
    return JSON.parse(text);
  } catch {
    throw new PublicationFailure();
  }
}

function safeAdapterMethod(method) {
  return async (...args) => {
    try {
      return await method(...args);
    } catch (error) {
      if (error instanceof PointerCASConflict) throw error;
      throw new PublicationFailure();
    }
  };
}

function asPath(value) {
  return value instanceof URL ? fileURLToPath(value) : value;
}

function exactKeys(value, expected) {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return false;
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return actual.length === wanted.length && actual.every((name, index) => name === wanted[index]);
}

function parseCanonicalJSON(bytes, maximum) {
  if (!Buffer.isBuffer(bytes) || bytes.length === 0 || bytes.length > maximum) throw new ValidationFailure();
  let text;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    throw new ValidationFailure();
  }
  let value;
  try {
    value = JSON.parse(text);
  } catch {
    throw new ValidationFailure();
  }
  if (JSON.stringify(value) !== text) throw new ValidationFailure();
  return value;
}

function decodeCanonicalBase64(value) {
  if (typeof value !== 'string' || value.length === 0 || value.length % 4 !== 0 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)) {
    throw new ValidationFailure();
  }
  const decoded = Buffer.from(value, 'base64');
  if (decoded.toString('base64') !== value) throw new ValidationFailure();
  return decoded;
}

function validTime(value, now, mustBeFuture) {
  if (typeof value !== 'string' || !RFC3339_SECONDS.test(value)) return false;
  const parsed = new Date(value);
  if (!Number.isFinite(parsed.valueOf()) || parsed.toISOString().replace('.000Z', 'Z') !== value) return false;
  return !mustBeFuture || parsed > now;
}

function validatePublisher(publisher, index) {
  const required = ['id', 'role', 'country', 'organization', 'organizationId', 'allowedChannel', 'allowedTags'];
  const allowed = publisher && Object.hasOwn(publisher, 'manifestSha256') ? [...required, 'manifestSha256'] : required;
  if (!exactKeys(publisher, allowed) || !Array.isArray(publisher.allowedTags) || publisher.allowedTags.length === 0 ||
    publisher.allowedTags.some((tag) => typeof tag !== 'string' || !CANONICAL_TAG.test(tag)) ||
    publisher.allowedTags.some((tag, tagIndex) => tagIndex > 0 && tag <= publisher.allowedTags[tagIndex - 1])) {
    throw new ValidationFailure();
  }
  if (Object.hasOwn(publisher, 'manifestSha256') && !SHA256.test(publisher.manifestSha256)) throw new ValidationFailure();
  if (index === 0) {
    if (publisher.id !== 'naisnet-primary' || publisher.role !== 'primary' || publisher.country !== 'CN' ||
      publisher.organization !== 'NaisNet Technology Co., Ltd.' || publisher.organizationId !== '91210103MA7CJ3C094' ||
      publisher.allowedChannel !== 'stable' || publisher.allowedTags.includes('v0.4.11')) {
      throw new ValidationFailure();
    }
    return;
  }
  if (index !== 1 || publisher.id !== 'rushrush-bridge' || publisher.role !== 'bridge' || publisher.country !== 'CN' ||
    publisher.organization !== 'RushRush Network Technology Ltd' || publisher.organizationId !== '91450900MADM3GLG5P' ||
    publisher.allowedChannel !== 'legacy-rushrush' || publisher.allowedTags.length !== 1 || publisher.allowedTags[0] !== 'v0.4.11' ||
    Object.hasOwn(publisher, 'manifestSha256')) {
    throw new ValidationFailure();
  }
}

function validatePolicyBytes(policy, reviewedSPKI, expectedSPKISHA256, expectedPreviousEpoch, now, requireFresh = true) {
  const document = parseCanonicalJSON(policy, MAX_POLICY_BYTES);
  if (!exactKeys(document, ['signed', 'signatures']) || !exactKeys(document.signed, ['epoch', 'expiresAt', 'publishers']) ||
    !Number.isSafeInteger(document.signed.epoch) || document.signed.epoch <= 0 ||
    !Number.isSafeInteger(expectedPreviousEpoch) || expectedPreviousEpoch < 0 || document.signed.epoch !== expectedPreviousEpoch + 1 ||
    !validTime(document.signed.expiresAt, now, requireFresh) || !Array.isArray(document.signed.publishers) ||
    document.signed.publishers.length < 1 || document.signed.publishers.length > 2 ||
    !Array.isArray(document.signatures) || document.signatures.length !== 1 ||
    !exactKeys(document.signatures[0], ['algorithm', 'signature']) ||
    document.signatures[0].algorithm !== 'ecdsa-p256-sha256') {
    throw new ValidationFailure();
  }
  document.signed.publishers.forEach(validatePublisher);
  if (!Buffer.isBuffer(reviewedSPKI) || reviewedSPKI.length === 0 || !SHA256.test(expectedSPKISHA256) || sha256(reviewedSPKI) !== expectedSPKISHA256) {
    throw new ValidationFailure();
  }
  let publicKey;
  try {
    publicKey = createPublicKey({ key: reviewedSPKI, format: 'der', type: 'spki' });
  } catch {
    throw new ValidationFailure();
  }
  if (publicKey.asymmetricKeyType !== 'ec' || publicKey.asymmetricKeyDetails?.namedCurve !== 'prime256v1') throw new ValidationFailure();
  const signature = decodeCanonicalBase64(document.signatures[0].signature);
  if (!verifySignature('sha256', Buffer.from(JSON.stringify(document.signed)), publicKey, signature)) throw new ValidationFailure();
  return { document, epoch: document.signed.epoch };
}

function validateAuditBytes(audit, epoch, policySHA256) {
  const document = parseCanonicalJSON(audit, MAX_AUDIT_BYTES);
  if (!exactKeys(document, ['keyId', 'epoch', 'policySha256', 'requestId', 'utc', 'ciActor']) ||
    !KEY_ID.test(document.keyId) || document.epoch !== epoch || document.policySha256 !== policySHA256 ||
    !REQUEST_ID.test(document.requestId) || !validTime(document.utc, new Date(0), false) || !CI_ACTOR.test(document.ciActor)) {
    throw new ValidationFailure();
  }
  return document;
}

function validateArtifact(artifact, role, commit) {
  if (!exactKeys(artifact, ['name', 'length', 'sha256', 'bytesBase64']) || artifact.name !== `${role}.json` ||
    !Number.isSafeInteger(artifact.length) || artifact.length <= 0 || !SHA256.test(artifact.sha256) ||
    !exactKeys(commit, ['name', 'length', 'sha256']) || commit.name !== artifact.name ||
    commit.length !== artifact.length || commit.sha256 !== artifact.sha256) {
    throw new ValidationFailure();
  }
  const bytes = decodeCanonicalBase64(artifact.bytesBase64);
  if (bytes.length !== artifact.length || sha256(bytes) !== artifact.sha256) throw new ValidationFailure();
  return bytes;
}

function validateMachineEnvelope(raw, reviewedSPKI, expectedSPKISHA256, expectedPreviousEpoch, now) {
  const bytes = Buffer.isBuffer(raw) ? raw : Buffer.from(raw ?? '');
  if (bytes.length <= 1 || bytes.length > MAX_MACHINE_ENVELOPE_BYTES || bytes.at(-1) !== 0x0a) throw new ValidationFailure();
  const withoutNewline = bytes.subarray(0, -1);
  const envelope = parseCanonicalJSON(withoutNewline, MAX_MACHINE_ENVELOPE_BYTES);
  if (!exactKeys(envelope, ['schemaVersion', 'verification', 'commit', 'policy', 'audit', 'commitArtifact']) || envelope.schemaVersion !== 2 ||
    !exactKeys(envelope.verification, ['epoch', 'expectedPreviousEpoch', 'spkiSha256']) ||
    envelope.verification.expectedPreviousEpoch !== expectedPreviousEpoch || envelope.verification.spkiSha256 !== expectedSPKISHA256 ||
    !exactKeys(envelope.commit, ['schemaVersion', 'policy', 'audit']) || envelope.commit.schemaVersion !== 1) {
    throw new ValidationFailure();
  }
  const policy = validateArtifact(envelope.policy, 'policy', envelope.commit.policy);
  const audit = validateArtifact(envelope.audit, 'audit', envelope.commit.audit);
  if (!exactKeys(envelope.commitArtifact, ['name', 'length', 'sha256', 'bytesBase64']) ||
    envelope.commitArtifact.name !== 'commit.json' || !Number.isSafeInteger(envelope.commitArtifact.length) ||
    envelope.commitArtifact.length <= 0 || !SHA256.test(envelope.commitArtifact.sha256)) {
    throw new ValidationFailure();
  }
  const commit = decodeCanonicalBase64(envelope.commitArtifact.bytesBase64);
  const canonicalCommit = Buffer.from(JSON.stringify(envelope.commit));
  if (commit.length !== envelope.commitArtifact.length || sha256(commit) !== envelope.commitArtifact.sha256 ||
    !equalBytes(commit, canonicalCommit)) {
    throw new ValidationFailure();
  }
  const policyValidation = validatePolicyBytes(policy, reviewedSPKI, expectedSPKISHA256, expectedPreviousEpoch, now);
  if (envelope.verification.epoch !== policyValidation.epoch) throw new ValidationFailure();
  validateAuditBytes(audit, policyValidation.epoch, envelope.policy.sha256);
  return {
    epoch: policyValidation.epoch,
    expectedPreviousEpoch,
    policy,
    audit,
    commit,
    policySHA256: envelope.policy.sha256,
    auditSHA256: envelope.audit.sha256,
    commitSHA256: envelope.commitArtifact.sha256,
    reviewedSPKI: Buffer.from(reviewedSPKI),
    expectedSPKISHA256,
    now,
  };
}

export function publisherTargets(epoch) {
  if (!Number.isSafeInteger(epoch) || epoch < 1 || epoch > 99_999_999) throw new Error('publisher policy validation failed');
  const fixed = String(epoch).padStart(8, '0');
  return {
    cosImmutableKey: `trust/publisher/epochs/${fixed}.json`,
    githubReleaseTag: `publisher-policy-epoch-${fixed}`,
    githubPolicyAsset: POLICY_ASSET,
    githubAuditAsset: AUDIT_ASSET,
    githubCommitAsset: COMMIT_ASSET,
    cosPointerKey: COS_POINTER,
    githubPointerRef: GITHUB_POINTER_REF,
    githubPointerPath: GITHUB_POINTER_PATH,
  };
}

function summaryFor(bundle, advanceDiscovery) {
  return {
    schemaVersion: 1,
    epoch: bundle.epoch,
    expectedPreviousEpoch: bundle.expectedPreviousEpoch,
    policySHA256: bundle.policySHA256,
    auditSHA256: bundle.auditSHA256,
    commitSHA256: bundle.commitSHA256,
    ...publisherTargets(bundle.epoch),
    advanceDiscovery,
  };
}

export function formatPublisherSummary(summary) {
  return `${JSON.stringify(summary)}\n`;
}

async function captureVerifiedBundle(options, adapters) {
  const policyInput = asPath(options.policyPath);
  const auditInput = asPath(options.auditPath);
  const reviewedSPKIInput = asPath(options.reviewedSPKIPath);
  if (typeof policyInput !== 'string' || policyInput.length === 0 || typeof auditInput !== 'string' || auditInput.length === 0 ||
    typeof reviewedSPKIInput !== 'string' || reviewedSPKIInput.length === 0 || !SHA256.test(options.expectedSPKISHA256) ||
    !Number.isSafeInteger(options.expectedPreviousEpoch) || options.expectedPreviousEpoch < 0 || !(options.now instanceof Date) || !Number.isFinite(options.now.valueOf())) {
    throw new ValidationFailure();
  }
  const policyPath = resolve(policyInput);
  const auditPath = resolve(auditInput);
  const reviewedSPKIPath = resolve(reviewedSPKIInput);
  const result = await adapters.process.run('go', [
    'run', './cmd/trustpolicy', 'verify-bundle',
    '--policy', policyPath,
    '--audit', auditPath,
    '--reviewed-spki', reviewedSPKIPath,
    '--expected-spki-sha256', options.expectedSPKISHA256,
    '--expected-previous-epoch', String(options.expectedPreviousEpoch),
  ], { cwd: 'updateapi', capture: true });
  if (!result || result.code !== 0 || typeof result.stderr !== 'string' || result.stderr !== '') throw new ValidationFailure();
  const reviewedSPKI = Buffer.from(await adapters.files.readFile(options.reviewedSPKIPath));
  return validateMachineEnvelope(result.stdout, reviewedSPKI, options.expectedSPKISHA256, options.expectedPreviousEpoch, options.now);
}

function equalBytes(left, right) {
  return Buffer.isBuffer(left) && left.length === right.length && left.equals(right);
}

function verifyCOSPolicyObject(object, bundle) {
  if (!object) throw new PublicationFailure();
  const bytes = Buffer.from(object.bytes);
  if (!equalBytes(bytes, bundle.policy) || sha256(bytes) !== bundle.policySHA256 ||
    object.sha256 !== bundle.policySHA256 || object.contentType !== 'application/json' ||
    typeof object.version !== 'string' || object.version.length === 0) {
    throw new PublicationFailure();
  }
}

async function verifyImmutableReadback(bundle, adapters) {
  const targets = publisherTargets(bundle.epoch);
  const cos = await adapters.cos.read(targets.cosImmutableKey);
  verifyCOSPolicyObject(cos, bundle);
  const githubPolicy = Buffer.from(await adapters.github.downloadReleaseAsset(targets.githubReleaseTag, targets.githubPolicyAsset));
  const githubAudit = Buffer.from(await adapters.github.downloadReleaseAsset(targets.githubReleaseTag, targets.githubAuditAsset));
  const githubCommit = Buffer.from(await adapters.github.downloadReleaseAsset(targets.githubReleaseTag, targets.githubCommitAsset));
  if (!equalBytes(githubPolicy, bundle.policy) || !equalBytes(githubAudit, bundle.audit) || !equalBytes(githubCommit, bundle.commit) ||
    sha256(githubPolicy) !== bundle.policySHA256 || sha256(githubAudit) !== bundle.auditSHA256 || sha256(githubCommit) !== bundle.commitSHA256) {
    throw new PublicationFailure();
  }
}

async function publishImmutable(bundle, adapters) {
  const targets = publisherTargets(bundle.epoch);
  await adapters.cos.putImmutable(targets.cosImmutableKey, bundle.policy, bundle.policySHA256);
  const cos = await adapters.cos.read(targets.cosImmutableKey);
  verifyCOSPolicyObject(cos, bundle);
  await adapters.github.publishImmutableRelease({
    tag: targets.githubReleaseTag,
    title: targets.githubReleaseTag,
    assets: [
      { name: targets.githubPolicyAsset, bytes: bundle.policy, sha256: bundle.policySHA256 },
      { name: targets.githubAuditAsset, bytes: bundle.audit, sha256: bundle.auditSHA256 },
      { name: targets.githubCommitAsset, bytes: bundle.commit, sha256: bundle.commitSHA256 },
    ],
  });
  await verifyImmutableReadback(bundle, adapters);
}

function classifyPointer(pointer, bundle) {
  if (pointer === null) {
    if (bundle.expectedPreviousEpoch !== 0) throw new PublicationFailure();
    return { kind: 'absent', version: null };
  }
  if (typeof pointer.version !== 'string' || pointer.version.length === 0) throw new PublicationFailure();
  const bytes = Buffer.from(pointer.bytes);
  if (equalBytes(bytes, bundle.policy)) return { kind: 'candidate', version: pointer.version };
  if (bundle.expectedPreviousEpoch === 0) throw new PublicationFailure();
  try {
    const verified = validatePolicyBytes(
      bytes,
      bundle.reviewedSPKI,
      bundle.expectedSPKISHA256,
      bundle.expectedPreviousEpoch - 1,
      bundle.now,
      false,
    );
    if (verified.epoch !== bundle.expectedPreviousEpoch) throw new PublicationFailure();
  } catch {
    throw new PublicationFailure();
  }
  return { kind: 'previous', version: pointer.version };
}

function isPointerCASConflict(error) {
  return error instanceof PointerCASConflict || error?.code === 'publisher-pointer-cas-conflict';
}

async function completePointer(readPointer, compareAndSwap, initial, bundle) {
  if (initial.kind === 'candidate') {
    const confirmed = classifyPointer(await readPointer(), bundle);
    if (confirmed.kind !== 'candidate') throw new PublicationFailure();
    return;
  }
  try {
    await compareAndSwap(initial.version);
  } catch (error) {
    if (!isPointerCASConflict(error)) throw error;
    const afterConflict = classifyPointer(await readPointer(), bundle);
    if (afterConflict.kind === 'candidate') return;
    throw new PublicationFailure();
  }
  const readback = classifyPointer(await readPointer(), bundle);
  if (readback.kind !== 'candidate') throw new PublicationFailure();
}

async function advanceDiscovery(bundle, adapters) {
  await verifyImmutableReadback(bundle, adapters);
  const targets = publisherTargets(bundle.epoch);
  const [priorCOS, priorGitHub] = await Promise.all([
    adapters.cos.read(targets.cosPointerKey),
    adapters.github.readPointer(targets.githubPointerRef, targets.githubPointerPath),
  ]);
  const cosState = classifyPointer(priorCOS, bundle);
  const githubState = classifyPointer(priorGitHub, bundle);
  await completePointer(
    () => adapters.cos.read(targets.cosPointerKey),
    (expectedVersion) => adapters.cos.compareAndSwapPointer(targets.cosPointerKey, bundle.policy, expectedVersion, bundle.policySHA256),
    cosState,
    bundle,
  );
  await completePointer(
    () => adapters.github.readPointer(targets.githubPointerRef, targets.githubPointerPath),
    (expectedVersion) => adapters.github.compareAndSwapPointer({
      ref: targets.githubPointerRef,
      path: targets.githubPointerPath,
      bytes: bundle.policy,
      expectedVersion,
    }),
    githubState,
    bundle,
  );
  const [finalCOS, finalGitHub] = await Promise.all([
    adapters.cos.read(targets.cosPointerKey),
    adapters.github.readPointer(targets.githubPointerRef, targets.githubPointerPath),
  ]);
  if (classifyPointer(finalCOS, bundle).kind !== 'candidate' || classifyPointer(finalGitHub, bundle).kind !== 'candidate') {
    throw new PublicationFailure();
  }
}

export async function publishTrustPolicy(options, adapters) {
  try {
    if (!adapters?.process?.run || !adapters?.files?.readFile) throw new ValidationFailure();
    const bundle = await captureVerifiedBundle(options, adapters);
    if (options.mode === 'dry-run') return summaryFor(bundle, false);
    if (!adapters?.cos?.putImmutable || !adapters?.cos?.read || !adapters?.cos?.compareAndSwapPointer ||
      !adapters?.github?.publishImmutableRelease || !adapters?.github?.downloadReleaseAsset ||
      !adapters?.github?.readPointer || !adapters?.github?.compareAndSwapPointer) {
      throw new PublicationFailure();
    }
    if (options.mode === 'publish-immutable' || options.mode === 'publish') await publishImmutable(bundle, adapters);
    if (options.mode === 'advance-discovery' || (options.mode === 'publish' && options.advanceDiscovery === true)) {
      if (options.advanceDiscovery !== true) throw new PublicationFailure();
      await advanceDiscovery(bundle, adapters);
    } else if (options.mode !== 'publish-immutable' && options.mode !== 'publish') {
      throw new PublicationFailure();
    }
    return summaryFor(bundle, options.mode === 'advance-discovery' || options.advanceDiscovery === true);
  } catch (error) {
    if (error instanceof PublicationFailure) throw new Error('publisher policy publication failed');
    if (error instanceof ValidationFailure) throw new Error('publisher policy validation failed');
    if (options?.mode === 'dry-run') throw new Error('publisher policy validation failed');
    throw new Error('publisher policy publication failed');
  }
}

export const defaultFiles = { readFile: readLocalFile };

function safeEncode(value) {
  return encodeURIComponent(value).replace(/[!'()*]/g, (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`);
}

function hmacSHA1(key, value, encoding = undefined) {
  return createHmac('sha1', key).update(value).digest(encoding);
}

function cosAuthorization(method, url, headers, secretID, secretKey, now) {
  const start = Math.floor(now.valueOf() / 1000);
  const keyTime = `${start};${start + 900}`;
  const signedHeaders = [...headers.entries()]
    .filter(([name]) => {
      const lower = name.toLowerCase();
      return lower === 'host' || lower === 'content-length' || lower === 'content-type' || lower === 'if-match' || lower === 'if-none-match' || lower.startsWith('x-cos-');
    })
    .map(([name, value]) => [safeEncode(name.toLowerCase()), safeEncode(value)])
    .sort(([leftName, leftValue], [rightName, rightValue]) => leftName.localeCompare(rightName) || leftValue.localeCompare(rightValue));
  const headerNames = signedHeaders.map(([name]) => name);
  const formattedHeaders = signedHeaders.map(([name, value]) => `${name}=${value}`).join('&');
  const parameters = [...url.searchParams.entries()]
    .map(([name, value]) => [safeEncode(name.toLowerCase()), safeEncode(value)])
    .sort(([leftName, leftValue], [rightName, rightValue]) => leftName.localeCompare(rightName) || leftValue.localeCompare(rightValue));
  const parameterNames = parameters.map(([name]) => name);
  const formattedParameters = parameters.map(([name, value]) => `${name}=${value}`).join('&');
  const formatString = `${method.toLowerCase()}\n${url.pathname}\n${formattedParameters}\n${formattedHeaders}\n`;
  const stringToSign = `sha1\n${keyTime}\n${createHash('sha1').update(formatString).digest('hex')}\n`;
  const signKey = hmacSHA1(secretKey, keyTime, 'hex');
  const signature = hmacSHA1(signKey, stringToSign, 'hex');
  return [
    'q-sign-algorithm=sha1',
    `q-ak=${secretID}`,
    `q-sign-time=${keyTime}`,
    `q-key-time=${keyTime}`,
    `q-header-list=${headerNames.join(';')}`,
    `q-url-param-list=${parameterNames.join(';')}`,
    `q-signature=${signature}`,
  ].join('&');
}

export function createCOSPublisherAdapter(environment, fetchImpl = fetch, now = () => new Date()) {
  const bucket = environment.COS_BUCKET ?? '';
  const region = environment.COS_REGION ?? '';
  const secretID = environment.TENCENTCLOUD_SECRET_ID ?? '';
  const secretKey = environment.TENCENTCLOUD_SECRET_KEY ?? '';
  const sessionToken = environment.TENCENTCLOUD_SESSION_TOKEN ?? '';
  if (!/^[a-z0-9](?:[a-z0-9-]{0,48}[a-z0-9])?-[1-9][0-9]{4,19}$/.test(bucket) ||
    !/^[a-z][a-z0-9-]{1,30}[a-z0-9]$/.test(region) || !secretID.trim() || !secretKey.trim() || !sessionToken.trim()) {
    throw new PublicationFailure();
  }
  const endpoint = `${bucket}.cos.${region}.myqcloud.com`;
  async function request(method, key, body, extraHeaders = {}) {
    if (!/^trust\/publisher\/(?:epochs\/[0-9]{8}\.json|latest\.json)$/.test(key)) throw new PublicationFailure();
    const url = new URL(`https://${endpoint}/${key.split('/').map(safeEncode).join('/')}`);
    const headers = new Headers({ host: endpoint, 'x-cos-security-token': sessionToken, 'accept-encoding': 'identity', ...extraHeaders });
    if (body !== undefined) {
      headers.set('content-length', String(body.length));
      headers.set('content-type', 'application/json');
    }
    headers.set('authorization', cosAuthorization(method, url, headers, secretID, secretKey, now()));
    return fetchImpl(url, { method, headers, body, redirect: 'manual' });
  }
  async function readObject(key) {
    const response = await request('GET', key);
    if (response.status === 404) return null;
    if (!response.ok) throw new PublicationFailure();
    const bytes = await readBoundedResponse(response, MAX_POLICY_BYTES);
    return {
      bytes,
      version: response.headers.get('etag') ?? '',
      sha256: response.headers.get('x-cos-meta-sha256') ?? '',
      contentType: response.headers.get('content-type') ?? '',
    };
  }
  return {
    putImmutable: safeAdapterMethod(async (key, bytes, digest) => {
      const response = await request('PUT', key, bytes, { 'x-cos-forbid-overwrite': 'true', 'x-cos-meta-sha256': digest });
      if (response.ok) return;
      if (response.status !== 409 && response.status !== 412) throw new PublicationFailure();
      const existing = await readObject(key);
      if (!existing || !equalBytes(existing.bytes, bytes) || sha256(existing.bytes) !== digest ||
        existing.sha256 !== digest || existing.contentType !== 'application/json' || existing.version.length === 0) {
        throw new PublicationFailure();
      }
    }),
    read: safeAdapterMethod(readObject),
    compareAndSwapPointer: safeAdapterMethod(async (key, bytes, expectedVersion, digest) => {
      const condition = expectedVersion === null ? { 'if-none-match': '*' } : { 'if-match': expectedVersion };
      const response = await request('PUT', key, bytes, { ...condition, 'x-cos-meta-sha256': digest });
      if (response.status === 409 || response.status === 412) throw new PointerCASConflict();
      if (!response.ok) throw new PublicationFailure();
    }),
  };
}

export function createGitHubPublisherAdapter(environment, fetchImpl = fetch) {
  const repository = environment.GITHUB_REPOSITORY ?? '';
  const token = environment.GH_TOKEN ?? '';
  const commit = environment.GITHUB_SHA ?? '';
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository) || !token.trim() || !/^[0-9a-f]{40}$/.test(commit)) throw new PublicationFailure();
  const api = `https://api.github.com/repos/${repository}`;
  const headers = {
    accept: 'application/vnd.github+json',
    authorization: `Bearer ${token}`,
    'x-github-api-version': '2022-11-28',
  };
  async function request(url, options = {}) {
    return fetchImpl(url, { redirect: 'manual', ...options, headers: { ...headers, 'accept-encoding': 'identity', ...(options.headers ?? {}) } });
  }
  async function releaseForTag(tag) {
    const response = await request(`${api}/releases/tags/${safeEncode(tag)}`);
    if (response.status === 404) return null;
    if (!response.ok) throw new PublicationFailure();
    return readRemoteJSON(response);
  }
  async function assetBytes(tag, name) {
    const contract = policyReleaseAssetForRemoteName(name);
    const maximum = contract?.maximumBytes ?? 0;
    if (maximum === 0) throw new PublicationFailure();
    const release = await releaseForTag(tag);
    const assets = Array.isArray(release?.assets) ? release.assets : [];
    const asset = assets.find((candidate) => candidate?.name === name);
    if (!asset || typeof asset.url !== 'string' || !Number.isSafeInteger(asset.id) || asset.id <= 0 ||
      !Number.isSafeInteger(asset.size) || asset.size < 0 || asset.size > maximum ||
      asset.content_type !== contract.contentType || asset.state !== 'uploaded' ||
      !/^sha256:[0-9a-f]{64}$/.test(asset.digest)) {
      throw new PublicationFailure();
    }
    let assetAPI;
    try { assetAPI = new URL(asset.url); } catch { throw new PublicationFailure(); }
    const expectedAssetPath = `/repos/${repository}/releases/assets/${asset.id}`;
    if (assetAPI.protocol !== 'https:' || assetAPI.host !== 'api.github.com' || assetAPI.pathname !== expectedAssetPath ||
      assetAPI.search !== '' || assetAPI.hash !== '' || assetAPI.username !== '' || assetAPI.password !== '') {
      throw new PublicationFailure();
    }
    let response = await request(assetAPI, { headers: { accept: 'application/octet-stream' }, redirect: 'manual' });
    if (response.status === 302) {
      const location = response.headers.get('location');
      let redirect;
      try { redirect = new URL(location ?? ''); } catch { throw new PublicationFailure(); }
      if (redirect.protocol !== 'https:' || redirect.hostname !== GITHUB_RELEASE_ASSET_HOST || redirect.port !== '' ||
        redirect.username !== '' || redirect.password !== '' || redirect.hash !== '' || redirect.pathname === '/') {
        throw new PublicationFailure();
      }
      response = await fetchImpl(redirect, {
        method: 'GET',
        redirect: 'manual',
        credentials: 'omit',
        headers: { accept: 'application/octet-stream', 'accept-encoding': 'identity' },
      });
    }
    if (response.status !== 200) throw new PublicationFailure();
    const bytes = await readBoundedResponse(response, maximum);
    if (bytes.length !== asset.size || sha256(bytes) !== asset.digest.slice('sha256:'.length)) throw new PublicationFailure();
    return bytes;
  }
  async function ensureImmutableTag(tag) {
    const inspect = await request(`${api}/git/ref/tags/${safeEncode(tag)}`);
    if (inspect.status === 404) {
      const create = await request(`${api}/git/refs`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ ref: `refs/tags/${tag}`, sha: commit }),
      });
      if (!create.ok) throw new PublicationFailure();
      return;
    }
    if (!inspect.ok) throw new PublicationFailure();
    const reference = await readRemoteJSON(inspect);
    if (reference?.ref !== `refs/tags/${tag}` || reference?.object?.type !== 'commit' || reference?.object?.sha !== commit) throw new PublicationFailure();
  }
  return {
    publishImmutableRelease: safeAdapterMethod(async ({ tag, title, assets }) => {
      if (!/^publisher-policy-epoch-[0-9]{8}$/.test(tag) || title !== tag || assets.length !== POLICY_RELEASE_ASSET_CONTRACT.length ||
        POLICY_RELEASE_ASSET_CONTRACT.some((contract) => assets.filter((asset) => asset?.name === contract.remoteName).length !== 1)) throw new PublicationFailure();
      await ensureImmutableTag(tag);
      let release = await releaseForTag(tag);
      if (release === null) {
        const create = await request(`${api}/releases`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ tag_name: tag, target_commitish: commit, name: title, draft: true, prerelease: false, make_latest: 'false' }),
        });
        if (!create.ok) throw new PublicationFailure();
        release = await readRemoteJSON(create);
      }
      if (release?.tag_name !== tag || typeof release?.draft !== 'boolean' || typeof release?.prerelease !== 'boolean' ||
        typeof release?.upload_url !== 'string' || !Number.isSafeInteger(release?.id)) throw new PublicationFailure();
      let uploadEndpoint;
      try { uploadEndpoint = new URL(release.upload_url.replace(/\{.*$/, '')); } catch { throw new PublicationFailure(); }
      if (uploadEndpoint.protocol !== 'https:' || uploadEndpoint.host !== 'uploads.github.com' ||
        uploadEndpoint.pathname !== `/repos/${repository}/releases/${release.id}/assets` || uploadEndpoint.search !== '' ||
        uploadEndpoint.hash !== '' || uploadEndpoint.username !== '' || uploadEndpoint.password !== '') {
        throw new PublicationFailure();
      }
      const existing = Array.isArray(release.assets) ? release.assets : [];
      if (existing.some((asset) => !assets.some((expected) => expected.name === asset?.name))) throw new PublicationFailure();
      if (release.draft === false && existing.length !== assets.length) throw new PublicationFailure();
      for (const asset of assets) {
        const found = existing.find((candidate) => candidate?.name === asset.name);
        if (found) {
          const bytes = await assetBytes(tag, asset.name);
          if (!equalBytes(bytes, asset.bytes) || sha256(bytes) !== asset.sha256) throw new PublicationFailure();
          continue;
        }
        const uploadURL = new URL(uploadEndpoint);
        uploadURL.searchParams.set('name', asset.name);
        const response = await request(uploadURL, {
          method: 'POST',
          headers: { accept: 'application/vnd.github+json', 'content-type': 'application/json', 'content-length': String(asset.bytes.length) },
          body: asset.bytes,
        });
        if (!response.ok) throw new PublicationFailure();
      }
      const publish = await request(`${api}/releases/${release.id}`, {
        method: 'PATCH',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ draft: false, prerelease: false, make_latest: 'false' }),
      });
      if (!publish.ok) throw new PublicationFailure();
      const finalRelease = await releaseForTag(tag);
      const finalAssets = Array.isArray(finalRelease?.assets) ? finalRelease.assets : [];
      if (finalRelease?.draft !== false || finalRelease?.prerelease !== false || finalRelease?.tag_name !== tag ||
        finalAssets.length !== assets.length || assets.some((expected) => finalAssets.filter((asset) => asset?.name === expected.name).length !== 1)) {
        throw new PublicationFailure();
      }
      for (const asset of assets) {
        const bytes = await assetBytes(tag, asset.name);
        if (!equalBytes(bytes, asset.bytes) || sha256(bytes) !== asset.sha256) throw new PublicationFailure();
      }
      const latest = await request(`${api}/releases/latest`);
      if (latest.status !== 404) {
        if (!latest.ok) throw new PublicationFailure();
        const latestRelease = await readRemoteJSON(latest);
        if (latestRelease?.tag_name === tag || latestRelease?.id === finalRelease?.id) throw new PublicationFailure();
      }
    }),
    downloadReleaseAsset: safeAdapterMethod(assetBytes),
    readPointer: safeAdapterMethod(async (ref, path) => {
      if (ref !== GITHUB_POINTER_REF || path !== GITHUB_POINTER_PATH) throw new PublicationFailure();
      const branch = ref.replace(/^refs\/heads\//, '');
      const response = await request(`${api}/contents/${safeEncode(path)}?ref=${safeEncode(branch)}`);
      if (response.status === 404) return null;
      if (!response.ok) throw new PublicationFailure();
      const document = await readRemoteJSON(response);
      if (document?.encoding !== 'base64' || typeof document?.content !== 'string' || !/^[0-9a-f]{40}$/.test(document?.sha)) throw new PublicationFailure();
      const bytes = decodeCanonicalBase64(document.content.replace(/\s/g, ''));
      if (bytes.length === 0 || bytes.length > MAX_POLICY_BYTES) throw new PublicationFailure();
      return { bytes, version: document.sha };
    }),
    compareAndSwapPointer: safeAdapterMethod(async ({ ref, path, bytes, expectedVersion }) => {
      if (ref !== GITHUB_POINTER_REF || path !== GITHUB_POINTER_PATH) throw new PublicationFailure();
      if (expectedVersion !== null && !/^[0-9a-f]{40}$/.test(expectedVersion)) throw new PublicationFailure();
      const branch = ref.replace(/^refs\/heads\//, '');
      if (expectedVersion === null) {
        const inspect = await request(`${api}/git/ref/heads/${safeEncode(branch)}`);
        if (inspect.status === 404) {
          const create = await request(`${api}/git/refs`, {
            method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ ref, sha: commit }),
          });
          if (create.status === 409 || create.status === 422) throw new PointerCASConflict();
          if (!create.ok) throw new PublicationFailure();
        } else if (!inspect.ok) {
          throw new PublicationFailure();
        }
      }
      const body = { message: 'Advance publisher policy discovery', content: bytes.toString('base64'), branch };
      if (expectedVersion !== null) body.sha = expectedVersion;
      const response = await request(`${api}/contents/${safeEncode(path)}`, {
        method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body),
      });
      if (response.status === 409 || response.status === 422) throw new PointerCASConflict();
      if (!response.ok) throw new PublicationFailure();
    }),
  };
}

function defaultProcessAdapter() {
  return {
    run: async (command, args, options) => {
      try {
        const result = await execFileAsync(command, args, {
          cwd: options?.cwd ? resolve(projectRoot, options.cwd) : projectRoot,
          encoding: 'utf8',
          windowsHide: true,
          maxBuffer: MAX_MACHINE_ENVELOPE_BYTES,
          env: process.env,
        });
        return { code: 0, stdout: result.stdout, stderr: result.stderr };
      } catch (error) {
        return { code: typeof error?.code === 'number' ? error.code : 1, stdout: error?.stdout ?? '', stderr: error?.stderr ?? '' };
      }
    },
  };
}

function commandOptions(environment) {
  const previous = environment.PUBLISHER_EXPECTED_PREVIOUS_EPOCH ?? '';
  if (!/^(?:0|[1-9][0-9]*)$/.test(previous)) throw new ValidationFailure();
  const advance = environment.PUBLISHER_ADVANCE_DISCOVERY;
  if (advance !== 'true' && advance !== 'false') throw new ValidationFailure();
  if (environment.PUBLISHER_ROTATION_SPKI_PATH !== 'publisher/rotation-root-spki.der') throw new ValidationFailure();
  return {
    mode: environment.PUBLISHER_MODE,
    policyPath: environment.PUBLISHER_POLICY_PATH,
    auditPath: environment.PUBLISHER_AUDIT_PATH,
    reviewedSPKIPath: environment.PUBLISHER_ROTATION_SPKI_PATH,
    expectedSPKISHA256: environment.PUBLISHER_ROTATION_SPKI_SHA256,
    expectedPreviousEpoch: Number(previous),
    advanceDiscovery: advance === 'true',
    now: new Date(),
  };
}

async function runCommand(environment) {
  const options = commandOptions(environment);
  const adapters = { process: defaultProcessAdapter(), files: defaultFiles };
  if (options.mode !== 'dry-run') {
    adapters.cos = createCOSPublisherAdapter(environment);
    adapters.github = createGitHubPublisherAdapter(environment);
  }
  return publishTrustPolicy(options, adapters);
}

async function main() {
  try {
    if (process.argv[2] !== 'run' || process.argv.length !== 3) throw new Error('publisher policy command failed');
    const summary = await runCommand(process.env);
    process.stdout.write(formatPublisherSummary(summary));
  } catch (error) {
    const message = error instanceof Error && /^(?:publisher policy (?:validation|publication)|publisher policy command) failed$/.test(error.message)
      ? error.message
      : 'publisher policy command failed';
    process.stderr.write(`${message}\n`);
    process.exitCode = 1;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) await main();
