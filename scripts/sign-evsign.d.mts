export interface SignFileOptions {
  inputPath: string;
  outputPath: string;
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
export const EVSIGN_API_ENDPOINT: 'https://api.evsign.cn/v1';
export interface EVSignPublisherIdentity {
  country: 'CN';
  organization: string;
  organizationId: string;
}
export interface EVSignSignerProfile {
  schema: 2;
  profile: 'stable' | 'bridge';
  certificate: string;
  identity: EVSignPublisherIdentity;
}
export function resolveEVSignSignerProfile(profile: string, environment?: Record<string, string | undefined>): EVSignSignerProfile;
export function signWithProfile(options: {
  profile: string;
  environment?: Record<string, string | undefined>;
  inputPath: string;
  outputPath?: string;
}, dependencies?: SignDependencies): Promise<void>;
export function requestSignedBytes(source: Buffer, request: { headers: Record<string, string>; attemptTimeoutMs: number; maximumResponseBytes: number; fetchImpl?: typeof fetch }): Promise<Buffer>;
export function signFileWithRetry(options: SignFileOptions, dependencies?: SignDependencies): Promise<void>;
