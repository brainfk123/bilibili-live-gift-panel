export const REQUIRED_COMPONENT_ASSETS: readonly string[];
export function buildChecksumManifest(files: Map<string, Buffer>): Buffer;
export function writePreparedComponentAssets(outputDirectory: string, files: Map<string, Buffer>): Promise<void>;
export function verifyChecksumManifest(directory: string): Promise<void>;
export function verifyGitHubReleaseMetadata(metadata: Record<string, unknown>, directory: string, expectedTag: string): Promise<void>;
export function verifyComponentMetadata(manifest: Record<string, unknown>, identity: { descriptor: Buffer; descriptorSha256: string; fingerprint: string }, expectedSigner: string): void;
export function verifyPinnedSourceAssets(archive: Buffer, signature: Buffer, policy: { sourceSha256: string; sourceSignatureSha256: string }): void;
export interface SealedFFmpegEvidence {
  schemaVersion: number;
  archiveSha256: string;
  archiveSize: number;
  manifestSha256: string;
  manifestSize: number;
}
export type SealFFmpegClosure = (options: { archivePath: string; manifestPath: string; sealedDirectory: string }) => SealedFFmpegEvidence | Promise<SealedFFmpegEvidence>;
export function prepareComponentAssets(options: { projectRoot?: string; toolRoot?: string; outputDirectory: string; sealedOutputDirectory?: string; expectedSigner?: string; sealFFmpegClosure?: SealFFmpegClosure }): Promise<{ fingerprint: string; tag: string }>;
export interface FFmpegComponentVerificationOptions {
  projectRoot?: string;
  toolRoot?: string;
  inputDirectory: string;
  manifestOutputPath?: string;
	sealedOutputDirectory?: string;
  expectedSigner?: string;
  verifyPayload?: (directory: string, expectedSigner: string) => void | Promise<void>;
  loadPolicy?: (projectRoot: string) => Promise<Record<string, unknown>>;
  sourceSignatureSHA256?: string;
	sealFFmpegClosure?: SealFFmpegClosure;
}
export function verifyComponentAssets(options: FFmpegComponentVerificationOptions): Promise<{ fingerprint: string; tag: string }>;
export function installComponentAssets(options: FFmpegComponentVerificationOptions & {
  publishPair?: (directory: string, archive: Buffer, manifest: Buffer) => void | Promise<void>;
}): Promise<{ fingerprint: string; tag: string }>;
