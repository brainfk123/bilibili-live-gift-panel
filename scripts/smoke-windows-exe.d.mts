import type { ChildProcess, SpawnOptions } from 'node:child_process';

export interface PanelProbe { port: number; version: string }
export interface SmokeEvidence {
  schema: 1;
  version: string;
  port: number;
  routes: string[];
  sha256: string;
  startedAt: string;
  completedAt: string;
}
export interface SmokeWindowsExecutableOptions {
  platform?: string;
  cwd?: string;
  executablePath?: string;
  fetchImpl?: typeof fetch;
  spawnImpl?: (command: string, args: string[], options: SpawnOptions) => Pick<ChildProcess, 'exitCode' | 'kill'>;
  createTemporaryDirectory?: () => Promise<string>;
  readExecutable?: (path: string) => Promise<Buffer>;
  readinessTimeoutMs?: number;
  exitTimeoutMs?: number;
  pollIntervalMs?: number;
}
export function probePanel(fetchImpl: typeof fetch, ports: number[], deadline: number): Promise<PanelProbe>;
export function validatePanelRoutes(fetchImpl: typeof fetch, port: number): Promise<string[]>;
export function requestGracefulExit(fetchImpl: typeof fetch, port: number, takeoverVersion: string): Promise<void>;
export function smokeWindowsExecutable(options?: SmokeWindowsExecutableOptions): Promise<SmokeEvidence>;
