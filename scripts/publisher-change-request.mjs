import { writeFile } from 'node:fs/promises';

const SHA256 = /^[0-9a-f]{64}$/;
const CANONICAL_TAG = /^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const ORGANIZATION_ID = /^[0-9A-Z]{8,32}$/;
const RUN_ID = /^[1-9][0-9]{0,19}$/;
const INPUT_KEYS = ['artifactSha256', 'certificateDerSha256', 'currentPolicyEpoch', 'identity', 'runAttempt', 'runId', 'tag'];
const IDENTITY_KEYS = ['country', 'organization', 'organizationId'];

function exactObject(value, keys) {
  if (value === null || typeof value !== 'object' || Array.isArray(value) || Object.getPrototypeOf(value) !== Object.prototype) return false;
  const actual = Object.keys(value).sort();
  return actual.length === keys.length && actual.every((key, index) => key === keys[index]);
}

function invalid() {
  throw new Error('publisher change request is invalid');
}

export function buildPublisherChangeRequest(input) {
  if (!exactObject(input, INPUT_KEYS) || !exactObject(input.identity, IDENTITY_KEYS) ||
      typeof input.tag !== 'string' || !CANONICAL_TAG.test(input.tag) ||
      typeof input.artifactSha256 !== 'string' || !SHA256.test(input.artifactSha256) ||
      typeof input.certificateDerSha256 !== 'string' || !SHA256.test(input.certificateDerSha256) ||
      typeof input.identity.country !== 'string' || !/^[A-Z]{2}$/.test(input.identity.country) ||
      typeof input.identity.organization !== 'string' || input.identity.organization.length === 0 ||
      input.identity.organization !== input.identity.organization.trim() || Buffer.byteLength(input.identity.organization, 'utf8') > 256 || /\p{C}/u.test(input.identity.organization) ||
      typeof input.identity.organizationId !== 'string' || !ORGANIZATION_ID.test(input.identity.organizationId) ||
      !Number.isSafeInteger(input.currentPolicyEpoch) || input.currentPolicyEpoch < 1 ||
      typeof input.runId !== 'string' || !RUN_ID.test(input.runId) ||
      !Number.isSafeInteger(input.runAttempt) || input.runAttempt < 1 || input.runAttempt > 1000) {
    invalid();
  }
  return Buffer.from(`${JSON.stringify({
    schemaVersion: 1,
    tag: input.tag,
    artifactSha256: input.artifactSha256,
    certificateDerSha256: input.certificateDerSha256,
    identity: {
      country: input.identity.country,
      organization: input.identity.organization,
      organizationId: input.identity.organizationId,
    },
    currentPolicyEpoch: input.currentPolicyEpoch,
    runId: input.runId,
    runAttempt: input.runAttempt,
  })}\n`);
}

export async function writePublisherChangeRequest(path, input) {
  try {
    if (typeof path !== 'string' || path.length === 0) invalid();
    await writeFile(path, buildPublisherChangeRequest(input), { flag: 'wx', mode: 0o600 });
  } catch {
    throw new Error('publisher change request could not be created');
  }
}
