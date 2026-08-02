export type RuntimeConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error';

export interface RuntimeStatus {
  state: RuntimeConnectionState;
  roomId: string;
  lastError?: string;
  lastGiftAt?: number;
}

interface RuntimeResponse {
  code: number;
  runtime: RuntimeStatus;
}

interface FormulaPreviewResponse {
  code: number;
  result?: number;
  message?: string;
}

export async function getRuntimeStatus(): Promise<RuntimeStatus> {
  const response = await fetch('/api/runtime', { cache: 'no-store' });
  if (!response.ok) throw new Error(`后台状态读取失败：HTTP ${response.status}`);
  const payload = await response.json() as RuntimeResponse;
  if (payload.code !== 0 || !payload.runtime) throw new Error('后台状态响应无效');
  return payload.runtime;
}

export async function previewFormula(
  formula: string,
  attributeName: string,
  attributeValue: number,
  context: 'gift' | 'timer' = 'gift',
): Promise<number> {
  const response = await fetch('/api/formula/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ formula, attributeName, attributeValue, context }),
  });
  const payload = await response.json() as FormulaPreviewResponse;
  if (!response.ok || payload.code !== 0 || typeof payload.result !== 'number') {
    throw new Error(payload.message || `公式计算失败：HTTP ${response.status}`);
  }
  return payload.result;
}
