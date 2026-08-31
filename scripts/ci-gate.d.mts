export type GateResult = 'success' | 'failure' | 'cancelled' | 'skipped';
export type WindowsLevel = 'skip' | 'shared' | 'desktop' | 'desktop-high-risk';
export interface GateInput {
  scope: string;
  hosted: string;
  mysql: string;
  windows: string;
  windowsLevel: string;
  expectMySQL: boolean;
  expectWindows: boolean;
}
export interface GateDecision { ok: boolean; failures: string[] }
export function evaluateGate(input: GateInput): GateDecision;
