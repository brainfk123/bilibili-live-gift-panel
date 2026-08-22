export interface FFmpegComponentIdentity {
  descriptor: Buffer;
  descriptorSha256: string;
  fingerprint: string;
}

export interface FFmpegPackageManifest {
  schema: 1;
  component_fingerprint: string;
  descriptor: string;
  descriptor_sha256: string;
  version: string;
  sha256: string;
  archive_sha256: string;
  component_gate: string;
  component_gate_sha256: string;
  size: number;
  authenticode: boolean;
  signer_subject: string;
  source_release_commit: string;
}

export function buildPackageManifest(options: {
  identity: FFmpegComponentIdentity;
  binary: Buffer;
  archive: Buffer;
  componentGate: Buffer;
  authenticode: boolean;
  signerSubject: string;
  sourceReleaseCommit: string;
}): FFmpegPackageManifest;

export function publishPairTransactionally(
  directory: string,
  archive: Buffer,
  manifest: Buffer,
  options?: Record<string, unknown>,
): Promise<void>;
