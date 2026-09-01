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
export const FFMPEG_SOURCE_SIGNATURE_SHA256: string;
export const FFMPEG_SIGNED_COMPONENT_SIGNER: 'C=CN;O=NaisNet Technology Co., Ltd.;SERIALNUMBER=91210103MA7CJ3C094';
export const FFMPEG_FIXED_COMPONENT_SIGNER_SUBJECT: string;
export const FFMPEG_FIXED_COMPONENT_FINGERPRINT: '2603fa9f68855ead324fe3b4ee13c9daaed984b151aae338eeca15aaee71a9c4';
export const FFMPEG_FIXED_COMPONENT_TAG: 'ffmpeg-component-v2-2603fa9f68855ead324fe3b4ee13c9daaed984b151aae338eeca15aaee71a9c4';

export function loadFFmpegPolicy(root: string): Promise<FFmpegPolicy>;
export function serializeFFmpegDescriptor(policy: FFmpegPolicy, signerSubject: string): Buffer;
export function ffmpegComponentIdentity(policy: FFmpegPolicy, signerSubject: string): {
  descriptor: Buffer;
  descriptorSha256: string;
  fingerprint: string;
  tag: string;
};
export function reviewedSignedFFmpegComponentIdentity(toolRoot: string): Promise<{
  descriptor: Buffer;
  descriptorSha256: string;
  fingerprint: string;
  tag: string;
}>;
export function reviewedFixedFFmpegComponentIdentity(toolRoot: string): Promise<{
  descriptor: Buffer;
  descriptorSha256: string;
  fingerprint: string;
  tag: string;
}>;
export function componentGateRecord(policy: FFmpegPolicy, binary: Buffer): Buffer;
export function canonicalToolchainLock(lock: FFmpegToolchainLock): Buffer;
export function validateToolchainLock(lock: FFmpegToolchainLock): void;
export function authenticodeContentHash(binary: Buffer): string;
