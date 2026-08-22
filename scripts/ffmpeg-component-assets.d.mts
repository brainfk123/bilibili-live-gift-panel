export const REQUIRED_COMPONENT_ASSETS: readonly string[];
export function buildChecksumManifest(files: Map<string, Buffer>): Buffer;
export function verifyChecksumManifest(directory: string): Promise<void>;
export function verifyGitHubReleaseMetadata(metadata: Record<string, unknown>, directory: string, expectedTag: string): Promise<void>;
export function verifyComponentMetadata(manifest: Record<string, unknown>, identity: { descriptor: Buffer; descriptorSha256: string; fingerprint: string }, expectedSigner: string): void;
export function verifyPinnedSourceAssets(archive: Buffer, signature: Buffer, policy: { sourceSha256: string; sourceSignatureSha256: string }): void;
export function prepareComponentAssets(options: { projectRoot?: string; outputDirectory: string }): Promise<{ fingerprint: string; tag: string }>;
export function verifyComponentAssets(options: { projectRoot?: string; inputDirectory: string; expectedSigner: string; verifyPayload?: (directory: string, expectedSigner: string) => void | Promise<void> }): Promise<{ fingerprint: string; tag: string }>;
export function installComponentAssets(options: { projectRoot?: string; inputDirectory: string; expectedSigner: string }): Promise<{ fingerprint: string; tag: string }>;
