export function signFileWithCliRetry(options: {
  cliPath: string;
  inputPath: string;
  key: string;
  cert?: string;
  password?: string;
  maxAttempts?: number;
  attemptTimeoutMs?: number;
  retryDelaysMs?: number[];
}, dependencies?: {
  run?: (cliPath: string, args: string[], timeoutMs: number) => Promise<{ exitCode: number | null; timedOut: boolean }>;
  sleep?: (milliseconds: number) => Promise<void>;
  log?: (message: string) => void;
}): Promise<void>;
