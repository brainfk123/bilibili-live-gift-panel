export interface SignFileOptions {
  inputPath: string;
  outputPath: string;
  endpoint: string;
  headers: Record<string, string>;
  maxAttempts?: number;
  attemptTimeoutMs?: number;
  retryDelaysMs?: number[];
}
export interface SignDependencies {
  request?: (source: Buffer, request: { endpoint: string; headers: Record<string, string>; attemptTimeoutMs: number; maximumResponseBytes: number }) => Promise<Buffer>;
  sleep?: (milliseconds: number) => Promise<void>;
  log?: (message: string) => void;
}
export function requestSignedBytes(source: Buffer, request: { endpoint: string; headers: Record<string, string>; attemptTimeoutMs: number; maximumResponseBytes: number }): Promise<Buffer>;
export function signFileWithRetry(options: SignFileOptions, dependencies?: SignDependencies): Promise<void>;
