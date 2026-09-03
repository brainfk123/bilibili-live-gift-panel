import { createHash } from 'node:crypto';
import { lstat, readFile, realpath } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';

const SHA256 = /^[0-9a-f]{64}$/;
const TAG = /^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$/;
const REPOSITORY = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
const ASSET_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const MAX_JSON_BYTES = 512 << 10;
const MAX_RELEASE_PAGES = 5;
const MAX_ASSET_BYTES = 128 << 20;
const RELEASE_ASSET_HOST = 'release-assets.githubusercontent.com';

const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const invalid = () => { throw new Error('stable release transaction is invalid'); };
const failed = () => { throw new Error('stable release transaction failed'); };

function validRequiredAssets(requiredAssets) {
  if (!Array.isArray(requiredAssets) || requiredAssets.length === 0 || requiredAssets.length > 32) return false;
  const names = new Set();
  for (const asset of requiredAssets) {
    if (!asset || Object.getPrototypeOf(asset) !== Object.prototype || Object.keys(asset).sort().join(',') !== 'name,sha256,size' ||
        typeof asset.name !== 'string' || !ASSET_NAME.test(asset.name) || names.has(asset.name) ||
        !Number.isSafeInteger(asset.size) || asset.size <= 0 || asset.size > MAX_ASSET_BYTES ||
        typeof asset.sha256 !== 'string' || !SHA256.test(asset.sha256)) return false;
    names.add(asset.name);
  }
  return true;
}

function exactAssetClosure(release, requiredAssets) {
  if (!Array.isArray(release.assets) || release.assets.length > requiredAssets.length) invalid();
  const found = new Map();
  for (const asset of release.assets) {
    if (!asset || !Number.isSafeInteger(asset.id) || asset.id <= 0 || typeof asset.name !== 'string' || found.has(asset.name) ||
        !Number.isSafeInteger(asset.size) || asset.size <= 0 || typeof asset.digest !== 'string') invalid();
    const expected = requiredAssets.find((candidate) => candidate.name === asset.name);
    if (!expected || asset.size !== expected.size || asset.digest !== `sha256:${expected.sha256}`) invalid();
    found.set(asset.name, asset);
  }
  return found;
}

function validTransactionIdentity(input) {
  return input && Object.getPrototypeOf(input) === Object.prototype && Array.isArray(input.releases) && input.releases.length <= 500 &&
    typeof input.tag === 'string' && TAG.test(input.tag) && typeof input.targetCommit === 'string' && /^[0-9a-f]{40}$/.test(input.targetCommit) &&
    typeof input.title === 'string' && input.title === input.tag && validRequiredAssets(input.requiredAssets);
}

export function planStableDraft(input) {
  if (!validTransactionIdentity(input)) invalid();
  const matches = input.releases.filter((release) => release && release.tag_name === input.tag);
  if (matches.length === 0) return { action: 'create', missingAssets: input.requiredAssets.map((asset) => asset.name) };
  if (matches.length !== 1) invalid();
  const release = matches[0];
  if (!Number.isSafeInteger(release.id) || release.id <= 0 || release.target_commitish !== input.targetCommit || release.name !== input.title ||
      release.draft !== true || release.prerelease !== false || release.published_at !== null) invalid();
  const found = exactAssetClosure(release, input.requiredAssets);
  return { action: 'resume', releaseId: release.id, missingAssets: input.requiredAssets.filter((asset) => !found.has(asset.name)).map((asset) => asset.name) };
}

async function loadRequiredAssets(assetDirectory, requiredAssets) {
  let root;
  try { root = await realpath(resolve(assetDirectory)); } catch { invalid(); }
  const loaded = new Map();
  for (const asset of requiredAssets) {
    const path = resolve(root, asset.name);
    if (dirname(path) !== root) invalid();
    let stat;
    let resolved;
    let bytes;
    try {
      stat = await lstat(path, { bigint: true });
      resolved = await realpath(path);
      bytes = await readFile(path);
    } catch { invalid(); }
    if (!stat.isFile() || stat.isSymbolicLink() || resolved !== path || Number(stat.size) !== asset.size || bytes.length !== asset.size || hash(bytes) !== asset.sha256) invalid();
    loaded.set(asset.name, bytes);
  }
  return loaded;
}

function verifyPublishedRelease(release, identity, releaseId) {
  if (!release || release.id !== releaseId || release.tag_name !== identity.tag || release.target_commitish !== identity.targetCommit || release.name !== identity.title ||
      release.draft !== false || release.prerelease !== false || typeof release.published_at !== 'string' || Number.isNaN(Date.parse(release.published_at))) invalid();
  const found = exactAssetClosure(release, identity.requiredAssets);
  if (found.size !== identity.requiredAssets.length) invalid();
}

export async function runStableReleaseTransaction(input) {
  if (!input || Object.getPrototypeOf(input) !== Object.prototype || !input.github || !REPOSITORY.test(input.repository ?? '') || typeof input.assetDirectory !== 'string') invalid();
  const identity = { releases: [], tag: input.tag, targetCommit: input.targetCommit, title: input.title, requiredAssets: input.requiredAssets };
  if (!validTransactionIdentity(identity)) invalid();
  const bytes = await loadRequiredAssets(input.assetDirectory, input.requiredAssets);
  const github = input.github;
  const methods = ['listReleases', 'createDraft', 'getReleaseById', 'uploadAssetById', 'publishById', 'getReleaseByTag', 'getLatest', 'downloadAsset'];
  if (methods.some((method) => typeof github[method] !== 'function')) invalid();

  let releases;
  try { releases = await github.listReleases(); } catch { failed(); }
  let plan = planStableDraft({ ...identity, releases });
  let releaseId;
  if (plan.action === 'create') {
    let created;
    try { created = await github.createDraft({ tag: input.tag, targetCommit: input.targetCommit, title: input.title }); } catch { failed(); }
    if (!Number.isSafeInteger(created?.id) || created.id <= 0) invalid();
    releaseId = created.id;
  } else {
    releaseId = plan.releaseId;
  }

  let release;
  try { release = await github.getReleaseById(releaseId); } catch { failed(); }
  plan = planStableDraft({ ...identity, releases: [release] });
  if (plan.action !== 'resume' || plan.releaseId !== releaseId) invalid();
  const uploadedAssets = [];
  for (const name of plan.missingAssets) {
    try { await github.uploadAssetById(releaseId, name, bytes.get(name)); } catch { failed(); }
    uploadedAssets.push(name);
  }

  try { release = await github.getReleaseById(releaseId); } catch { failed(); }
  plan = planStableDraft({ ...identity, releases: [release] });
  if (plan.action !== 'resume' || plan.releaseId !== releaseId || plan.missingAssets.length !== 0) invalid();
  try { await github.publishById(releaseId); } catch { failed(); }

  let byID;
  let byTag;
  let latest;
  try {
    byID = await github.getReleaseById(releaseId);
    byTag = await github.getReleaseByTag(input.tag);
    latest = await github.getLatest();
  } catch { failed(); }
  verifyPublishedRelease(byID, identity, releaseId);
  verifyPublishedRelease(byTag, identity, releaseId);
  verifyPublishedRelease(latest, identity, releaseId);

  for (const asset of input.requiredAssets) {
    let downloaded;
    try { downloaded = await github.downloadAsset(releaseId, asset.name, asset.size); } catch { failed(); }
    if (!Buffer.isBuffer(downloaded) || downloaded.length !== asset.size || hash(downloaded) !== asset.sha256) invalid();
  }
  return { schemaVersion: 1, releaseId, tag: input.tag, uploadedAssets, verifiedAssets: input.requiredAssets.map((asset) => asset.name) };
}

async function readBounded(response, maximum) {
  if (!response.body) failed();
  const declared = response.headers.get('content-length');
  if (declared !== null && (!/^(?:0|[1-9][0-9]*)$/.test(declared) || Number(declared) > maximum)) failed();
  const chunks = [];
  let size = 0;
  const reader = response.body.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > maximum) { await reader.cancel(); failed(); }
    chunks.push(Buffer.from(value));
  }
  if (declared !== null && Number(declared) !== size) failed();
  return Buffer.concat(chunks, size);
}

async function readJSON(response) {
  const bytes = await readBounded(response, MAX_JSON_BYTES);
  try { return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)); } catch { failed(); }
}

export function createGitHubStableReleaseAdapter(environment, fetchImpl = fetch) {
  const repository = environment?.GITHUB_REPOSITORY ?? '';
  const token = environment?.GH_TOKEN ?? '';
  if (!REPOSITORY.test(repository) || typeof token !== 'string' || token.trim() === '') invalid();
  const api = `https://api.github.com/repos/${repository}`;
  const headers = { accept: 'application/vnd.github+json', authorization: `Bearer ${token}`, 'x-github-api-version': '2022-11-28', 'accept-encoding': 'identity' };
  const request = (url, options = {}) => fetchImpl(url, { ...options, redirect: 'manual', headers: { ...headers, ...(options.headers ?? {}) } });
  const expectJSON = async (url, options = {}) => {
    const response = await request(url, options);
    if (!response.ok || response.status >= 300) failed();
    return readJSON(response);
  };
  const safe = (value) => encodeURIComponent(value);
  return {
    async listReleases() {
      const releases = [];
      for (let page = 1; page <= MAX_RELEASE_PAGES; page++) {
        const batch = await expectJSON(`${api}/releases?per_page=100&page=${page}`);
        if (!Array.isArray(batch) || batch.length > 100) failed();
        releases.push(...batch);
        if (batch.length < 100) return releases;
      }
      failed();
    },
    createDraft: ({ tag, targetCommit, title }) => expectJSON(`${api}/releases`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ tag_name: tag, target_commitish: targetCommit, name: title, draft: true, prerelease: false, make_latest: 'false' }) }),
    getReleaseById: (id) => expectJSON(`${api}/releases/${id}`),
    uploadAssetById: (id, name, bytes) => expectJSON(`https://uploads.github.com/repos/${repository}/releases/${id}/assets?name=${safe(name)}`, { method: 'POST', headers: { 'content-type': 'application/octet-stream', 'content-length': String(bytes.length) }, body: bytes }),
    publishById: (id) => expectJSON(`${api}/releases/${id}`, { method: 'PATCH', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ draft: false, prerelease: false, make_latest: 'true' }) }),
    getReleaseByTag: (tag) => expectJSON(`${api}/releases/tags/${safe(tag)}`),
    getLatest: () => expectJSON(`${api}/releases/latest`),
    async downloadAsset(releaseId, name, maximumBytes) {
      const release = await expectJSON(`${api}/releases/${releaseId}`);
      const matches = Array.isArray(release?.assets) ? release.assets.filter((asset) => asset?.name === name) : [];
      if (matches.length !== 1 || !Number.isSafeInteger(matches[0].id) || matches[0].id <= 0 || matches[0].size !== maximumBytes) invalid();
      let response = await request(`${api}/releases/assets/${matches[0].id}`, { headers: { accept: 'application/octet-stream' } });
      if (response.status === 302) {
        let redirect;
        try { redirect = new URL(response.headers.get('location')); } catch { failed(); }
        if (redirect.protocol !== 'https:' || redirect.hostname !== RELEASE_ASSET_HOST || redirect.port || redirect.username || redirect.password || redirect.hash) failed();
        response = await fetchImpl(redirect, { method: 'GET', redirect: 'manual', credentials: 'omit', headers: { accept: 'application/octet-stream', 'accept-encoding': 'identity' } });
      }
      if (response.status !== 200) failed();
      return readBounded(response, maximumBytes);
    },
  };
}
