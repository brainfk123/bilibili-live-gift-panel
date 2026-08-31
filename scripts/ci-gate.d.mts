export type GateResult = 'success' | 'failure' | 'cancelled' | 'skipped';
export interface GateInput {
  scope: string;
  hosted: string;
  mysql: string;
  windows: string;
  expectMySQL: boolean;
  expectWindows: boolean;
}
export interface GateDecision { ok: boolean; failures: string[] }
export function evaluateGate(input: GateInput): GateDecision;
