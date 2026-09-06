export interface StableReleaseAsset { name: string; size: number; sha256: string }
export interface StableReleaseGitHub {
  listReleases(): Promise<unknown[]>;
  createDraft(input: { tag: string; targetCommit: string; title: string }): Promise<any>;
  getReleaseById(id: number): Promise<any>;
  uploadAssetById(id: number, name: string, bytes: Buffer): Promise<any>;
  publishById(id: number): Promise<any>;
  getReleaseByTag(tag: string): Promise<any>;
  getLatest(): Promise<any>;
  downloadAsset(id: number, name: string, maximumBytes: number): Promise<Buffer>;
}
export interface StableReleaseTransactionInput {
  github: StableReleaseGitHub;
  repository: string;
  tag: string;
  targetCommit: string;
  title: string;
  assetDirectory: string;
  requiredAssets: StableReleaseAsset[];
}
export function planStableDraft(input: { releases: unknown[]; tag: string; targetCommit: string; title: string; requiredAssets: StableReleaseAsset[] }):
  { action: 'create'; missingAssets: string[] } | { action: 'resume'; releaseId: number; missingAssets: string[] };
export function runStableReleaseTransaction(input: StableReleaseTransactionInput): Promise<{ schemaVersion: 1; releaseId: number; tag: string; uploadedAssets: string[]; verifiedAssets: string[] }>;
export function createGitHubStableReleaseAdapter(environment: Record<string, string | undefined>, fetchImpl?: typeof fetch): StableReleaseGitHub;
