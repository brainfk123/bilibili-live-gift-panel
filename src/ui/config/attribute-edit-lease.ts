const ENDPOINT = '/api/attribute-edit-lease';
const SESSION_ENDPOINT = '/api/attribute-edits/session';
const DEFAULT_HEARTBEAT_MS = 5_000;
const DEFAULT_RETRY_MS = 1_000;
const DEFAULT_REQUEST_TIMEOUT_MS = 4_000;

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
  requestTimeoutMs?: number;
  onHealthChange?: (health: AttributeEditLeaseHealth) => void;
}

type LeasePayload = { code?: unknown; token?: unknown; attributeId?: unknown };

export async function acquireAttributeEditLease(
  attributeId: string,
  options: AttributeEditLeaseOptions = {},
): Promise<AttributeEditLeaseSession> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const payload = await requestLeasePayloadWithTimeout(
    fetchImpl,
    'POST',
    { attributeId },
    options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS,
  );
  const token = readLeaseToken(payload);

  return createLeaseSession(attributeId, token, fetchImpl, options);
}

/** Starts heartbeats for a token returned by an atomic edit session. */
export function maintainAttributeEditLease(
  attributeId: string,
  token: string,
  options: AttributeEditLeaseOptions = {},
): AttributeEditLeaseSession {
  if (!attributeId.trim() || !/^[A-Za-z0-9_-]{24}$/.test(token)) throw new Error('属性编辑租约响应无效');
  return createLeaseSession(attributeId, token, options.fetchImpl ?? fetch, options, true);
}

function createLeaseSession(
  attributeId: string,
  token: string,
  fetchImpl: typeof fetch,
  options: AttributeEditLeaseOptions,
  reacquireThroughSession = false,
): AttributeEditLeaseSession {
  const heartbeatMs = options.heartbeatMs ?? DEFAULT_HEARTBEAT_MS;
  const retryMs = options.retryMs ?? DEFAULT_RETRY_MS;
  const requestTimeoutMs = options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS;
  let currentToken = token;
  let released = false;
  let renewalActive = false;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let activeRequest: { controller: AbortController; timer: ReturnType<typeof setTimeout> } | undefined;
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

  const abortActiveRequest = (): void => {
    if (!activeRequest) return;
    const request = activeRequest;
    activeRequest = undefined;
    clearTimeout(request.timer);
    request.controller.abort();
  };

  const requestRenewal = async (
    method: 'POST' | 'PUT',
    payload: Record<string, string>,
    endpoint = ENDPOINT,
  ): Promise<{ response: Response; payload?: LeasePayload }> => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), requestTimeoutMs);
    const ownedRequest = { controller, timer };
    activeRequest = ownedRequest;
    try {
      const response = await requestLease(fetchImpl, method, payload, false, controller.signal, endpoint);
      return {
        response,
        ...(response.status === 404 ? {} : { payload: await readSuccessPayload(response) }),
      };
    } finally {
      clearTimeout(timer);
      if (activeRequest === ownedRequest) activeRequest = undefined;
    }
  };

  const renew = async (): Promise<void> => {
    if (released || renewalActive) return;
    renewalActive = true;
    try {
      const { response } = await requestRenewal('PUT', { attributeId, token: currentToken });
      if (response.status === 404) {
        if (released) return;
        reportHealth('retrying');
        const reacquired = await requestRenewal(
          'POST',
          { attributeId },
          reacquireThroughSession ? SESSION_ENDPOINT : ENDPOINT,
        );
        if (reacquireThroughSession && reacquired.payload?.attributeId !== attributeId) {
          throw new Error('属性编辑租约响应无效');
        }
        const replacementToken = readLeaseToken(reacquired.payload ?? {});
        if (released) return;
        currentToken = replacementToken;
      }
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
    abortActiveRequest();
    globalThis.removeEventListener?.('beforeunload', beforeUnload);
    releasePromise = requestLeaseWithTimeout(
      fetchImpl,
      'DELETE',
      { attributeId, token: currentToken },
      requestTimeoutMs,
      true,
    )
      .then(() => undefined)
      .catch(() => undefined);
    return releasePromise;
  };

  return {
    attributeId,
    get token() { return currentToken; },
    release,
  };
}

async function requestLease(
  fetchImpl: typeof fetch,
  method: 'POST' | 'PUT' | 'DELETE',
  payload: Record<string, string>,
  keepalive = false,
  signal?: AbortSignal,
  endpoint = ENDPOINT,
): Promise<Response> {
  return fetchImpl(endpoint, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
    ...(keepalive ? { keepalive: true } : {}),
    ...(signal ? { signal } : {}),
  });
}

async function requestLeaseWithTimeout(
  fetchImpl: typeof fetch,
  method: 'POST' | 'PUT' | 'DELETE',
  payload: Record<string, string>,
  timeoutMs: number,
  keepalive = false,
): Promise<Response> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await requestLease(fetchImpl, method, payload, keepalive, controller.signal);
  } finally {
    clearTimeout(timer);
  }
}

async function requestLeasePayloadWithTimeout(
  fetchImpl: typeof fetch,
  method: 'POST' | 'PUT',
  payload: Record<string, string>,
  timeoutMs: number,
): Promise<LeasePayload> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await requestLease(fetchImpl, method, payload, false, controller.signal);
    return await readSuccessPayload(response);
  } finally {
    clearTimeout(timer);
  }
}

function readLeaseToken(payload: LeasePayload): string {
  if (typeof payload.token !== 'string' || !/^[A-Za-z0-9_-]{24}$/.test(payload.token)) {
    throw new Error('属性编辑租约响应无效');
  }
  return payload.token;
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
