const ENDPOINT = '/api/attribute-edit-lease';
const DEFAULT_HEARTBEAT_MS = 5_000;
const DEFAULT_RETRY_MS = 1_000;

export type AttributeEditLeaseHealth = 'healthy' | 'retrying';

export interface AttributeEditLeaseSession {
  readonly attributeId: string;
  readonly token: string;
  release(): Promise<void>;
}

export interface AttributeEditLeaseOptions {
  fetchImpl?: typeof fetch;
  heartbeatMs?: number;
  retryMs?: number;
  onHealthChange?: (health: AttributeEditLeaseHealth) => void;
}

type LeasePayload = { code?: unknown; token?: unknown };

export async function acquireAttributeEditLease(
  attributeId: string,
  options: AttributeEditLeaseOptions = {},
): Promise<AttributeEditLeaseSession> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const response = await requestLease(fetchImpl, 'POST', { attributeId });
  const payload = await readSuccessPayload(response);
  if (typeof payload.token !== 'string' || !/^[A-Za-z0-9_-]{24}$/.test(payload.token)) {
    throw new Error('属性编辑租约响应无效');
  }

  return createLeaseSession(attributeId, payload.token, fetchImpl, options);
}

function createLeaseSession(
  attributeId: string,
  token: string,
  fetchImpl: typeof fetch,
  options: AttributeEditLeaseOptions,
): AttributeEditLeaseSession {
  const heartbeatMs = options.heartbeatMs ?? DEFAULT_HEARTBEAT_MS;
  const retryMs = options.retryMs ?? DEFAULT_RETRY_MS;
  let released = false;
  let renewalActive = false;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let releasePromise: Promise<void> | undefined;
  let health: AttributeEditLeaseHealth = 'healthy';

  const reportHealth = (next: AttributeEditLeaseHealth, force = false): void => {
    if (!force && health === next) return;
    health = next;
    options.onHealthChange?.(next);
  };

  const clearRetry = (): void => {
    if (retryTimer === undefined) return;
    clearTimeout(retryTimer);
    retryTimer = undefined;
  };

  const renew = async (): Promise<void> => {
    if (released || renewalActive) return;
    renewalActive = true;
    try {
      const response = await requestLease(fetchImpl, 'PUT', { attributeId, token });
      await readSuccessPayload(response);
      if (released) return;
      clearRetry();
      reportHealth('healthy', true);
    } catch {
      if (released) return;
      reportHealth('retrying');
      if (retryTimer === undefined) {
        retryTimer = setTimeout(() => {
          retryTimer = undefined;
          void renew();
        }, retryMs);
      }
    } finally {
      renewalActive = false;
    }
  };

  const heartbeatTimer = setInterval(() => { void renew(); }, heartbeatMs);
  const beforeUnload = (): void => { void release(); };
  globalThis.addEventListener?.('beforeunload', beforeUnload);

  const release = (): Promise<void> => {
    if (releasePromise) return releasePromise;
    released = true;
    clearInterval(heartbeatTimer);
    clearRetry();
    globalThis.removeEventListener?.('beforeunload', beforeUnload);
    releasePromise = requestLease(fetchImpl, 'DELETE', { attributeId, token }, true)
      .then(() => undefined)
      .catch(() => undefined);
    return releasePromise;
  };

  return { attributeId, token, release };
}

async function requestLease(
  fetchImpl: typeof fetch,
  method: 'POST' | 'PUT' | 'DELETE',
  payload: Record<string, string>,
  keepalive = false,
): Promise<Response> {
  return fetchImpl(ENDPOINT, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
    ...(keepalive ? { keepalive: true } : {}),
  });
}

async function readSuccessPayload(response: Response): Promise<LeasePayload> {
  if (!response.ok) throw new Error('属性编辑租约请求失败');
  try {
    const payload = await response.json() as LeasePayload;
    if (!payload || payload.code !== 0) throw new Error('属性编辑租约响应无效');
    return payload;
  } catch (error) {
    if (error instanceof Error && error.message === '属性编辑租约响应无效') throw error;
    throw new Error('属性编辑租约响应无效');
  }
}
