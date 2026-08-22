export interface FFmpegToolchainPackage {
  name: string;
  version: string;
  url: string;
  sha256: string;
  signature_url: string;
  signature_sha256: string;
}

export interface FFmpegToolchainLock {
  schema: 1;
  source: 'https://repo.msys2.org';
  packages: FFmpegToolchainPackage[];
  executables: { gcc: string; ld: string; make: string };
}

export interface FFmpegPolicy {
  schema: 1;
  version: string;
  sourceSha256: string;
  sourceDateEpoch: string;
  configureFlags: string[];
  configureSha256: string;
  toolchainLock: FFmpegToolchainLock;
  toolchainLockBytes: Buffer;
  toolchainLockSha256: string;
  components: string[];
  infrastructure: string[];
}

export function loadFFmpegPolicy(root: string): Promise<FFmpegPolicy>;
export function serializeFFmpegDescriptor(policy: FFmpegPolicy): Buffer;
export function ffmpegComponentIdentity(policy: FFmpegPolicy): {
  descriptor: Buffer;
  descriptorSha256: string;
  fingerprint: string;
  tag: string;
};
export function componentGateRecord(policy: FFmpegPolicy, binary: Buffer): Buffer;
export function canonicalToolchainLock(lock: FFmpegToolchainLock): Buffer;
export function validateToolchainLock(lock: FFmpegToolchainLock): void;
export function authenticodeContentHash(binary: Buffer): string;
