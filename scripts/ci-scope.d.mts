export type WindowsLevel = 'skip' | 'shared' | 'desktop' | 'desktop-high-risk';
export interface Change { status: string; path: string; destination?: string }
export interface ScopeReason { path: string; level: WindowsLevel; reason: 'matched-explicit-rule' | 'unknown-path-fail-closed' }
export interface ScopeDecision { windowsLevel: WindowsLevel; runWindows: boolean; runMySQL: boolean; reasons: ScopeReason[] }
export const WINDOWS_LEVELS: readonly WindowsLevel[];
export function parseNameStatusZ(output: string): Change[];
export function readGitChanges(baseSHA: string, headSHA: string, cwd?: string): Change[];
export function classifyChanges(changes: Change[]): ScopeDecision;
