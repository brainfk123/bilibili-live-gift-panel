import type { BuildGoPolicy } from './build-go-policy.mjs';

export interface GoTrustDigests {
  rootSPKISHA256: string;
  bootstrapPolicySHA256: string;
}

export interface GoTrustInputs {
  rootSPKIBase64: string;
  bootstrapPolicyBase64: string;
}

export interface GoLdflagsResult {
  appVersion: string;
  appCommit: string;
  buildPolicy: BuildGoPolicy;
  trustDigests: GoTrustDigests | null;
  trustInputs: GoTrustInputs | null;
  ldflags: string;
}

export interface GoCompilerResult {
  status?: number | null;
  stdout?: string;
  stderr?: string;
  error?: unknown;
}

export interface GoCompilerOptions {
  candidates: string[];
  args: string[];
  cwd: string;
  trustInputs?: GoTrustInputs | null;
  spawn?: (command: string, args: string[], options: Record<string, unknown>) => GoCompilerResult;
  writeStdout?: (value: string) => void;
  writeStderr?: (value: string) => void;
}

export function resolveGoLdflags(
  environment?: Readonly<Record<string, string | undefined>>,
): Promise<GoLdflagsResult>;

export function runGoCompilerCandidates(options: GoCompilerOptions): void;
