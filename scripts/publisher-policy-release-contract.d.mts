export interface PolicyReleaseAssetContract {
  role: 'policy' | 'audit' | 'commit';
  remoteName: string;
  localName: string;
  contentType: 'application/json';
  maximumBytes: number;
}

export const POLICY_RELEASE_ASSET_CONTRACT: readonly PolicyReleaseAssetContract[];
export function policyReleaseAssetForRemoteName(name: string): PolicyReleaseAssetContract | null;
export function policyReleaseAssetForRole(role: PolicyReleaseAssetContract['role']): PolicyReleaseAssetContract | null;
export function exactPolicyReleaseRemoteNames(): string[];
export function mapPolicyReleaseToLocalBundle(
  release: {
    id: number;
    tag_name: string;
    draft: boolean;
    prerelease: boolean;
    assets: Array<{ name: string; size: number; digest: string; content_type: string; url: string }>;
  },
  downloadedByRemoteName: Map<string, Uint8Array>,
): { policy: Buffer; audit: Buffer; commit: Buffer };
