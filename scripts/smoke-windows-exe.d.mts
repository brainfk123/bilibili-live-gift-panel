import type { SpawnOptions } from 'node:child_process';

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
export interface SmokeChild {
  exitCode: number | null | undefined;
  kill(): boolean;
  once?(event: string, listener: (error: unknown) => void): unknown;
  removeListener?(event: string, listener: (error: unknown) => void): unknown;
}
export interface SmokeWindowsExecutableOptions {
  platform?: string;
  cwd?: string;
  executablePath?: string;
  fetchImpl?: typeof fetch;
  spawnImpl?: (command: string, args: string[], options: SpawnOptions) => SmokeChild;
  createTemporaryDirectory?: () => Promise<string>;
  removeTemporaryDirectory?: (path: string) => Promise<void>;
  readExecutable?: (path: string) => Promise<Buffer>;
  readinessTimeoutMs?: number;
  exitTimeoutMs?: number;
  pollIntervalMs?: number;
  requestTimeoutMs?: number;
}
export function probePanel(fetchImpl: typeof fetch, ports: number[], deadline: number): Promise<PanelProbe>;
export function validatePanelRoutes(fetchImpl: typeof fetch, port: number, deadline?: number): Promise<string[]>;
export function requestGracefulExit(fetchImpl: typeof fetch, port: number, takeoverVersion: string, deadline?: number): Promise<void>;
export function smokeWindowsExecutable(options?: SmokeWindowsExecutableOptions): Promise<SmokeEvidence>;
