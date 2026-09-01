export interface ProcessResult {
  code: number;
  stdout: string | Buffer;
  stderr: string;
}

export interface PublisherObject {
  bytes: Uint8Array;
  version: string;
}

export interface PublisherAdapters {
  process: {
    run(command: string, args: string[], options?: { cwd?: string; capture?: boolean }): Promise<ProcessResult>;
  };
  files: {
    readFile(path: string | URL): Promise<Uint8Array>;
  };
  cos: {
    putImmutable(key: string, bytes: Uint8Array, sha256: string): Promise<void>;
    read(key: string): Promise<PublisherObject | null>;
    compareAndSwapPointer(key: string, bytes: Uint8Array, expectedVersion: string | null, sha256: string): Promise<void>;
  };
  github: {
    publishImmutableRelease(input: {
      tag: string;
      title: string;
      assets: Array<{ name: string; bytes: Uint8Array; sha256: string }>;
    }): Promise<void>;
    downloadReleaseAsset(tag: string, name: string): Promise<Uint8Array>;
    readPointer(ref: string, path: string): Promise<PublisherObject | null>;
    compareAndSwapPointer(input: {
      ref: string;
      path: string;
      bytes: Uint8Array;
      expectedVersion: string | null;
    }): Promise<void>;
  };
}

export interface PublisherOptions {
  mode: 'dry-run' | 'publish-immutable' | 'advance-discovery' | 'publish';
  policyPath: string;
  auditPath: string;
  reviewedSPKIPath: string | URL;
  expectedSPKISHA256: string;
  expectedPreviousEpoch: number;
  advanceDiscovery: boolean;
  now: Date;
}

export interface PublisherSummary {
  schemaVersion: 1;
  epoch: number;
  expectedPreviousEpoch: number;
  policySHA256: string;
  auditSHA256: string;
  cosImmutableKey: string;
  githubReleaseTag: string;
  githubPolicyAsset: string;
  githubAuditAsset: string;
  cosPointerKey: string;
  githubPointerRef: string;
  githubPointerPath: string;
  advanceDiscovery: boolean;
}

export function publisherTargets(epoch: number): Pick<PublisherSummary,
  'cosImmutableKey' | 'githubReleaseTag' | 'githubPolicyAsset' | 'githubAuditAsset' |
  'cosPointerKey' | 'githubPointerRef' | 'githubPointerPath'>;
export function formatPublisherSummary(summary: PublisherSummary): string;
export function publishTrustPolicy(options: PublisherOptions, adapters: PublisherAdapters): Promise<PublisherSummary>;
export function exchangeTencentSession(environment: Record<string, string | undefined>, adapters?: {
  fetch: typeof fetch;
  appendFile: typeof import('node:fs/promises').appendFile;
  mask?: (value: string) => void;
}): Promise<{ requestId: string }>;
export const defaultFiles: PublisherAdapters['files'];
