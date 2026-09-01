import { createHash, createHmac, createPublicKey, verify as verifySignature } from 'node:crypto';
import { execFile } from 'node:child_process';
import { appendFile, readFile as readLocalFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const MAX_MACHINE_ENVELOPE_BYTES = 512 << 10;
const MAX_POLICY_BYTES = 256 << 10;
const MAX_AUDIT_BYTES = 64 << 10;
const POLICY_ASSET = 'gift-panel-publisher-policy.json';
const AUDIT_ASSET = 'gift-panel-publisher-policy.audit.json';
const COS_POINTER = 'trust/publisher/latest.json';
const GITHUB_POINTER_REF = 'refs/heads/publisher-trust';
const GITHUB_POINTER_PATH = POLICY_ASSET;
const SHA256 = /^[0-9a-f]{64}$/;
const CANONICAL_TAG = /^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const KEY_ID = /^[A-Za-z0-9_-]{1,128}$/;
const REQUEST_ID = /^[A-Za-z0-9_.:@/-]{1,256}$/;
const CI_ACTOR = /^[A-Za-z0-9_.\[\]-]{1,100}$/;
const RFC3339_SECONDS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/;
const execFileAsync = promisify(execFile);
const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

class ValidationFailure extends Error {}
class PublicationFailure extends Error {}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
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

function validatePolicyBytes(policy, reviewedSPKI, expectedSPKISHA256, expectedPreviousEpoch, now) {
  const document = parseCanonicalJSON(policy, MAX_POLICY_BYTES);
  if (!exactKeys(document, ['signed', 'signatures']) || !exactKeys(document.signed, ['epoch', 'expiresAt', 'publishers']) ||
    !Number.isSafeInteger(document.signed.epoch) || document.signed.epoch <= 0 ||
    !Number.isSafeInteger(expectedPreviousEpoch) || expectedPreviousEpoch < 0 || document.signed.epoch !== expectedPreviousEpoch + 1 ||
    !validTime(document.signed.expiresAt, now, true) || !Array.isArray(document.signed.publishers) ||
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
  if (!exactKeys(envelope, ['schemaVersion', 'verification', 'commit', 'policy', 'audit']) || envelope.schemaVersion !== 2 ||
    !exactKeys(envelope.verification, ['epoch', 'expectedPreviousEpoch', 'spkiSha256']) ||
    envelope.verification.expectedPreviousEpoch !== expectedPreviousEpoch || envelope.verification.spkiSha256 !== expectedSPKISHA256 ||
    !exactKeys(envelope.commit, ['schemaVersion', 'policy', 'audit']) || envelope.commit.schemaVersion !== 1) {
    throw new ValidationFailure();
  }
  const policy = validateArtifact(envelope.policy, 'policy', envelope.commit.policy);
  const audit = validateArtifact(envelope.audit, 'audit', envelope.commit.audit);
  const policyValidation = validatePolicyBytes(policy, reviewedSPKI, expectedSPKISHA256, expectedPreviousEpoch, now);
  if (envelope.verification.epoch !== policyValidation.epoch) throw new ValidationFailure();
  validateAuditBytes(audit, policyValidation.epoch, envelope.policy.sha256);
  return {
    epoch: policyValidation.epoch,
    expectedPreviousEpoch,
    policy,
    audit,
    policySHA256: envelope.policy.sha256,
    auditSHA256: envelope.audit.sha256,
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

async function verifyImmutableReadback(bundle, adapters) {
  const targets = publisherTargets(bundle.epoch);
  const cos = await adapters.cos.read(targets.cosImmutableKey);
  if (!cos || !equalBytes(Buffer.from(cos.bytes), bundle.policy)) throw new PublicationFailure();
  const githubPolicy = Buffer.from(await adapters.github.downloadReleaseAsset(targets.githubReleaseTag, targets.githubPolicyAsset));
  const githubAudit = Buffer.from(await adapters.github.downloadReleaseAsset(targets.githubReleaseTag, targets.githubAuditAsset));
  if (!equalBytes(githubPolicy, bundle.policy) || !equalBytes(githubAudit, bundle.audit) ||
    sha256(githubPolicy) !== bundle.policySHA256 || sha256(githubAudit) !== bundle.auditSHA256) {
    throw new PublicationFailure();
  }
}

async function publishImmutable(bundle, adapters) {
  const targets = publisherTargets(bundle.epoch);
  await adapters.cos.putImmutable(targets.cosImmutableKey, bundle.policy, bundle.policySHA256);
  const cos = await adapters.cos.read(targets.cosImmutableKey);
  if (!cos || !equalBytes(Buffer.from(cos.bytes), bundle.policy) || sha256(Buffer.from(cos.bytes)) !== bundle.policySHA256) throw new PublicationFailure();
  await adapters.github.publishImmutableRelease({
    tag: targets.githubReleaseTag,
    title: targets.githubReleaseTag,
    assets: [
      { name: targets.githubPolicyAsset, bytes: bundle.policy, sha256: bundle.policySHA256 },
      { name: targets.githubAuditAsset, bytes: bundle.audit, sha256: bundle.auditSHA256 },
    ],
  });
  await verifyImmutableReadback(bundle, adapters);
}

function validatePriorPointer(prior, bundle) {
  if (bundle.expectedPreviousEpoch === 0) {
    if (prior !== null) throw new PublicationFailure();
    return;
  }
  if (!prior || typeof prior.version !== 'string' || prior.version.length === 0) throw new PublicationFailure();
  validatePolicyBytes(Buffer.from(prior.bytes), bundle.reviewedSPKI, bundle.expectedSPKISHA256, bundle.expectedPreviousEpoch - 1, bundle.now);
}

async function advanceDiscovery(bundle, adapters) {
  await verifyImmutableReadback(bundle, adapters);
  const targets = publisherTargets(bundle.epoch);
  const [priorCOS, priorGitHub] = await Promise.all([
    adapters.cos.read(targets.cosPointerKey),
    adapters.github.readPointer(targets.githubPointerRef, targets.githubPointerPath),
  ]);
  validatePriorPointer(priorCOS, bundle);
  validatePriorPointer(priorGitHub, bundle);
  await adapters.cos.compareAndSwapPointer(targets.cosPointerKey, bundle.policy, priorCOS?.version ?? null, bundle.policySHA256);
  const cosReadback = await adapters.cos.read(targets.cosPointerKey);
  if (!cosReadback || !equalBytes(Buffer.from(cosReadback.bytes), bundle.policy)) throw new PublicationFailure();
  await adapters.github.compareAndSwapPointer({
    ref: targets.githubPointerRef,
    path: targets.githubPointerPath,
    bytes: bundle.policy,
    expectedVersion: priorGitHub?.version ?? null,
  });
  const githubReadback = await adapters.github.readPointer(targets.githubPointerRef, targets.githubPointerPath);
  if (!githubReadback || !equalBytes(Buffer.from(githubReadback.bytes), bundle.policy)) throw new PublicationFailure();
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

function createCOSAdapter(environment, fetchImpl = fetch, now = () => new Date()) {
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
    const headers = new Headers({ host: endpoint, 'x-cos-security-token': sessionToken, ...extraHeaders });
    if (body !== undefined) {
      headers.set('content-length', String(body.length));
      headers.set('content-type', 'application/json');
    }
    headers.set('authorization', cosAuthorization(method, url, headers, secretID, secretKey, now()));
    return fetchImpl(url, { method, headers, body, redirect: 'error' });
  }
  return {
    putImmutable: async (key, bytes, digest) => {
      const response = await request('PUT', key, bytes, { 'x-cos-forbid-overwrite': 'true', 'x-cos-meta-sha256': digest });
      if (!response.ok) throw new PublicationFailure();
    },
    read: async (key) => {
      const response = await request('GET', key);
      if (response.status === 404) return null;
      if (!response.ok) throw new PublicationFailure();
      const bytes = Buffer.from(await response.arrayBuffer());
      return { bytes, version: response.headers.get('etag') ?? '' };
    },
    compareAndSwapPointer: async (key, bytes, expectedVersion, digest) => {
      const condition = expectedVersion === null ? { 'if-none-match': '*' } : { 'if-match': expectedVersion };
      const response = await request('PUT', key, bytes, { ...condition, 'x-cos-meta-sha256': digest });
      if (!response.ok) throw new PublicationFailure();
    },
  };
}

function createGitHubAdapter(environment, fetchImpl = fetch) {
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
    return fetchImpl(url, { redirect: 'error', ...options, headers: { ...headers, ...(options.headers ?? {}) } });
  }
  async function releaseForTag(tag) {
    const response = await request(`${api}/releases/tags/${safeEncode(tag)}`);
    if (response.status === 404) return null;
    if (!response.ok) throw new PublicationFailure();
    return response.json();
  }
  async function assetBytes(tag, name) {
    const release = await releaseForTag(tag);
    const assets = Array.isArray(release?.assets) ? release.assets : [];
    const asset = assets.find((candidate) => candidate?.name === name);
    if (!asset || typeof asset.url !== 'string') throw new PublicationFailure();
    const response = await request(asset.url, { headers: { accept: 'application/octet-stream' } });
    if (!response.ok) throw new PublicationFailure();
    return Buffer.from(await response.arrayBuffer());
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
    const reference = await inspect.json();
    if (reference?.ref !== `refs/tags/${tag}` || reference?.object?.type !== 'commit' || reference?.object?.sha !== commit) throw new PublicationFailure();
  }
  return {
    publishImmutableRelease: async ({ tag, title, assets }) => {
      if (!/^publisher-policy-epoch-[0-9]{8}$/.test(tag) || title !== tag || assets.length !== 2) throw new PublicationFailure();
      await ensureImmutableTag(tag);
      let release = await releaseForTag(tag);
      if (release === null) {
        const create = await request(`${api}/releases`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ tag_name: tag, target_commitish: commit, name: title, draft: true, prerelease: false }),
        });
        if (!create.ok) throw new PublicationFailure();
        release = await create.json();
      }
      if (release?.tag_name !== tag || typeof release?.draft !== 'boolean' || release?.prerelease !== false ||
        typeof release?.upload_url !== 'string' || !Number.isSafeInteger(release?.id)) throw new PublicationFailure();
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
        const uploadURL = release.upload_url.replace(/\{.*$/, '');
        const response = await request(`${uploadURL}?name=${safeEncode(asset.name)}`, {
          method: 'POST',
          headers: { accept: 'application/vnd.github+json', 'content-type': 'application/json', 'content-length': String(asset.bytes.length) },
          body: asset.bytes,
        });
        if (!response.ok) throw new PublicationFailure();
      }
      if (release.draft === true) {
        const publish = await request(`${api}/releases/${release.id}`, {
          method: 'PATCH',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ draft: false, prerelease: false, make_latest: 'false' }),
        });
        if (!publish.ok) throw new PublicationFailure();
      }
      const finalRelease = await releaseForTag(tag);
      const finalAssets = Array.isArray(finalRelease?.assets) ? finalRelease.assets : [];
      if (finalRelease?.draft !== false || finalRelease?.prerelease !== false || finalRelease?.tag_name !== tag ||
        finalAssets.length !== assets.length || assets.some((expected) => finalAssets.filter((asset) => asset?.name === expected.name).length !== 1)) {
        throw new PublicationFailure();
      }
    },
    downloadReleaseAsset: assetBytes,
    readPointer: async (ref, path) => {
      if (ref !== GITHUB_POINTER_REF || path !== GITHUB_POINTER_PATH) throw new PublicationFailure();
      const branch = ref.replace(/^refs\/heads\//, '');
      const response = await request(`${api}/contents/${safeEncode(path)}?ref=${safeEncode(branch)}`);
      if (response.status === 404) return null;
      if (!response.ok) throw new PublicationFailure();
      const document = await response.json();
      if (document?.encoding !== 'base64' || typeof document?.content !== 'string' || typeof document?.sha !== 'string') throw new PublicationFailure();
      return { bytes: decodeCanonicalBase64(document.content.replace(/\s/g, '')), version: document.sha };
    },
    compareAndSwapPointer: async ({ ref, path, bytes, expectedVersion }) => {
      if (ref !== GITHUB_POINTER_REF || path !== GITHUB_POINTER_PATH) throw new PublicationFailure();
      const branch = ref.replace(/^refs\/heads\//, '');
      if (expectedVersion === null) {
        const inspect = await request(`${api}/git/ref/heads/${safeEncode(branch)}`);
        if (inspect.status === 404) {
          const create = await request(`${api}/git/refs`, {
            method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ ref, sha: commit }),
          });
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
      if (!response.ok) throw new PublicationFailure();
    },
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

function validSessionValue(value, maximum) {
  return typeof value === 'string' && value.length > 0 && value.length <= maximum && !/[\s\0]/.test(value);
}

export async function exchangeTencentSession(environment, adapters = { fetch, appendFile }) {
  const requestURL = environment.ACTIONS_ID_TOKEN_REQUEST_URL ?? '';
  const requestToken = environment.ACTIONS_ID_TOKEN_REQUEST_TOKEN ?? '';
  const audience = environment.PUBLISHER_TENCENT_OIDC_AUDIENCE ?? '';
  const roleARN = environment.PUBLISHER_TENCENT_ROLE_ARN ?? '';
  const providerID = environment.PUBLISHER_TENCENT_OIDC_PROVIDER_ID ?? '';
  const outputPath = environment.GITHUB_OUTPUT ?? '';
  const sessionName = `publisher-${environment.GITHUB_RUN_ID ?? ''}-${environment.GITHUB_RUN_ATTEMPT ?? ''}`;
  if (!requestURL.startsWith('https://') || !validSessionValue(requestToken, 4096) || !validSessionValue(audience, 256) ||
    !/^qcs::cam::uin\/[1-9][0-9]*:roleName\/[A-Za-z0-9+=,.@_-]{1,64}$/.test(roleARN) ||
    !/^[A-Za-z0-9_.:@/-]{1,256}$/.test(providerID) || !/^publisher-[0-9]+-[0-9]+$/.test(sessionName) || !outputPath) {
    throw new Error('publisher session exchange failed');
  }
  try {
    const oidcURL = new URL(requestURL);
    oidcURL.searchParams.set('audience', audience);
    const oidcResponse = await adapters.fetch(oidcURL, { headers: { authorization: `Bearer ${requestToken}` }, redirect: 'error' });
    if (!oidcResponse.ok) throw new Error();
    const oidc = await oidcResponse.json();
    if (!validSessionValue(oidc?.value, 16 << 10)) throw new Error();
    const stsResponse = await adapters.fetch('https://sts.tencentcloudapi.com', {
      method: 'POST',
      redirect: 'error',
      headers: {
        'content-type': 'application/json',
        'x-tc-action': 'AssumeRoleWithWebIdentity',
        'x-tc-version': '2018-08-13',
        'x-tc-region': 'ap-shanghai',
      },
      body: JSON.stringify({
        ProviderId: providerID,
        WebIdentityToken: oidc.value,
        RoleArn: roleARN,
        RoleSessionName: sessionName,
        DurationSeconds: 900,
      }),
    });
    if (!stsResponse.ok) throw new Error();
    const document = await stsResponse.json();
    const credentials = document?.Response?.Credentials;
    const secretID = credentials?.TmpSecretId;
    const secretKey = credentials?.TmpSecretKey;
    const sessionToken = credentials?.Token;
    if (!validSessionValue(secretID, 256) || !validSessionValue(secretKey, 256) || !validSessionValue(sessionToken, 8192)) throw new Error();
    await adapters.appendFile(outputPath,
      `secret-id<<PUBLISHER_EOF\n${secretID}\nPUBLISHER_EOF\n` +
      `secret-key<<PUBLISHER_EOF\n${secretKey}\nPUBLISHER_EOF\n` +
      `session-token<<PUBLISHER_EOF\n${sessionToken}\nPUBLISHER_EOF\n`,
      { encoding: 'utf8', mode: 0o600 });
    const mask = adapters.mask ?? ((value) => process.stdout.write(`::add-mask::${value}\n`));
    mask(secretID);
    mask(secretKey);
    mask(sessionToken);
    return { requestId: document?.Response?.RequestId ?? '' };
  } catch {
    throw new Error('publisher session exchange failed');
  }
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
    adapters.cos = createCOSAdapter(environment);
    adapters.github = createGitHubAdapter(environment);
  }
  return publishTrustPolicy(options, adapters);
}

async function main() {
  try {
    if (process.argv[2] === 'exchange-session' && process.argv.length === 3) {
      await exchangeTencentSession(process.env);
      return;
    }
    if (process.argv[2] !== 'run' || process.argv.length !== 3) throw new Error('publisher policy command failed');
    const summary = await runCommand(process.env);
    process.stdout.write(formatPublisherSummary(summary));
  } catch (error) {
    const message = error instanceof Error && /^(?:publisher policy (?:validation|publication)|publisher session exchange|publisher policy command) failed$/.test(error.message)
      ? error.message
      : 'publisher policy command failed';
    process.stderr.write(`${message}\n`);
    process.exitCode = 1;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) await main();
