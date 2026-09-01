import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';

export interface BashVersionProbeResult {
  error?: Error;
  status: number | null;
  stderr?: string;
  stdout?: string;
}

export type BashVersionProbe = (binary: string) => BashVersionProbeResult;

export function resolveBashBinary(
  platform: NodeJS.Platform = process.platform,
  environment: NodeJS.ProcessEnv = process.env,
  pathExists: (path: string) => boolean = existsSync,
): string {
  if (environment.BASH_BIN) return environment.BASH_BIN;
  if (platform === 'win32') {
    const candidates = [
      'C:\\Program Files\\Git\\usr\\bin\\bash.exe',
      'C:\\Program Files\\Git\\bin\\bash.exe',
    ];
    const binary = candidates.find(pathExists);
    if (!binary) throw new Error('Git Bash was not found; set BASH_BIN explicitly.');
    return binary;
  }
  if (platform === 'darwin') {
    const binary = ['/opt/homebrew/bin/bash', '/usr/local/bin/bash'].find(pathExists);
    if (!binary) {
      throw new Error('Homebrew Bash 4.2+ was not found; run "brew install bash" or set BASH_BIN explicitly. Stock /bin/bash is not supported.');
    }
    return binary;
  }
  return '/usr/bin/bash';
}

export function validateBashVersion(binary: string, probe?: BashVersionProbe): void {
  const result = (probe ?? probeBashVersion)(binary);
  const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`;
  const match = output.match(/\bversion\s+(\d+)\.(\d+)(?:\.\d+)?/i);
  if (result.error || result.status !== 0 || !match) {
    throw new Error(`Could not validate Bash version for ${binary}; Bash 4.2 or newer is required.`);
  }
  const major = Number(match[1]);
  const minor = Number(match[2]);
  if (major < 4 || (major === 4 && minor < 2)) {
    throw new Error(`Bash 4.2 or newer is required for deployment tests; ${binary} reported ${major}.${minor}.`);
  }
}

function probeBashVersion(binary: string): BashVersionProbeResult {
  const result = spawnSync(binary, ['--version'], {
    encoding: 'utf8',
    windowsHide: true,
  });
  return {
    error: result.error,
    status: result.status,
    stderr: result.stderr,
    stdout: result.stdout,
  };
}

export function resolveValidatedBashBinary(
  platform: NodeJS.Platform = process.platform,
  environment: NodeJS.ProcessEnv = process.env,
  pathExists: (path: string) => boolean = existsSync,
  probe?: BashVersionProbe,
): string {
  const binary = resolveBashBinary(platform, environment, pathExists);
  validateBashVersion(binary, probe);
  return binary;
}
