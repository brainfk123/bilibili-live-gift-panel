import { createHash } from 'node:crypto';

export const POLICY_RELEASE_ASSET_CONTRACT = Object.freeze([
  Object.freeze({
    role: 'policy',
    remoteName: 'gift-panel-publisher-policy.json',
    localName: 'policy.json',
    contentType: 'application/json',
    maximumBytes: 256 << 10,
  }),
  Object.freeze({
    role: 'audit',
    remoteName: 'gift-panel-publisher-policy.audit.json',
    localName: 'audit.json',
    contentType: 'application/json',
    maximumBytes: 64 << 10,
  }),
  Object.freeze({
    role: 'commit',
    remoteName: 'gift-panel-publisher-policy.commit.json',
    localName: 'commit.json',
    contentType: 'application/json',
    maximumBytes: 4 << 10,
  }),
]);

export function policyReleaseAssetForRemoteName(name) {
  return POLICY_RELEASE_ASSET_CONTRACT.find((entry) => entry.remoteName === name) ?? null;
}

export function policyReleaseAssetForRole(role) {
  return POLICY_RELEASE_ASSET_CONTRACT.find((entry) => entry.role === role) ?? null;
}

export function exactPolicyReleaseRemoteNames() {
  return POLICY_RELEASE_ASSET_CONTRACT.map((entry) => entry.remoteName);
}

export function mapPolicyReleaseToLocalBundle(release, downloadedByRemoteName) {
  if (!release || typeof release !== 'object' || release.draft !== false || release.prerelease !== false ||
    !/^publisher-policy-epoch-[0-9]{8}$/.test(release.tag_name) || !Number.isSafeInteger(release.id) || release.id <= 0 ||
    !Array.isArray(release.assets) || release.assets.length !== POLICY_RELEASE_ASSET_CONTRACT.length || !(downloadedByRemoteName instanceof Map)) {
    throw new Error('publisher policy Release contract is invalid');
  }
  const result = {};
  for (const contract of POLICY_RELEASE_ASSET_CONTRACT) {
    const matches = release.assets.filter((asset) => asset?.name === contract.remoteName);
    const asset = matches[0];
    const bytes = downloadedByRemoteName.get(contract.remoteName);
    if (matches.length !== 1 || !Buffer.isBuffer(bytes) || bytes.length <= 0 || bytes.length > contract.maximumBytes ||
      !Number.isSafeInteger(asset.size) || asset.size !== bytes.length || asset.content_type !== contract.contentType ||
      typeof asset.digest !== 'string' || asset.digest !== `sha256:${createHash('sha256').update(bytes).digest('hex')}` ||
      typeof asset.url !== 'string' || !/^https:\/\/api\.github\.com\/repos\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\/releases\/assets\/[1-9][0-9]*$/.test(asset.url)) {
      throw new Error('publisher policy Release contract is invalid');
    }
    result[contract.role] = Buffer.from(bytes);
  }
  return result;
}
