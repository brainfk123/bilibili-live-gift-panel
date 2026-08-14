import { afterEach, describe, expect, it, vi } from 'vitest';
import { acquireAttributeEditLease, maintainAttributeEditLease } from '../src/ui/config/attribute-edit-lease';
import { defaultState } from '../src/storage';

const attributeId = 'attribute-1';
const token = 'A'.repeat(24);

function success(payload: Record<string, unknown> = {}): Response {
  return Response.json({ code: 0, ...payload });
}

function responseError(message = '属性不存在'): Response {
  return Response.json({ code: -1, message }, { status: 404 });
}

function preparedSession(replacementToken: string, overrides: Record<string, unknown> = {}): Response {
  return Response.json({
    code: 0,
    attributeId,
    token: replacementToken,
    expiresAt: '2026-08-14T00:00:15Z',
    state: defaultState(),
    ...overrides,
  });
}

function requestBody(call: readonly unknown[]): Record<string, unknown> {
  return JSON.parse(String((call[1] as RequestInit | undefined)?.body));
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('attribute edit lease client', () => {
  it('maintains a prepared token and recovers a lost lease through the atomic session endpoint', async () => {
    vi.useFakeTimers();
    const replacementToken = 'B'.repeat(24);
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') return responseError('编辑租约不存在');
      if (init?.method === 'POST') return preparedSession(replacementToken);
      return success();
    });
    const session = maintainAttributeEditLease(attributeId, token, { fetchImpl });

    expect(fetchImpl).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(5_000);

    expect(fetchImpl.mock.calls[0][0]).toBe('/api/attribute-edit-lease');
    expect(requestBody(fetchImpl.mock.calls[0] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ attributeId, token });
    expect(fetchImpl.mock.calls[1][0]).toBe('/api/attribute-edits/session');
    expect(requestBody(fetchImpl.mock.calls[1] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ attributeId });
    expect(session.token).toBe(replacementToken);
    await session.release();
    expect(requestBody(fetchImpl.mock.calls[2] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ attributeId, token: replacementToken });
  });

  it('does not replace the current token from a malformed recovery session', async () => {
    vi.useFakeTimers();
    const health = vi.fn();
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT') return responseError('编辑租约不存在');
      if (init?.method === 'POST') return Response.json({
        code: 0, attributeId, token: 'B'.repeat(24), expiresAt: '2026-08-14T00:00:15Z',
      });
      return success();
    });
    const session = maintainAttributeEditLease(attributeId, token, { fetchImpl, onHealthChange: health });

    await vi.advanceTimersByTimeAsync(5_000);

    expect(session.token).toBe(token);
    expect(health).toHaveBeenLastCalledWith('retrying');
    await session.release();
    expect(requestBody(fetchImpl.mock.calls.at(-1) as unknown as [RequestInfo | URL, RequestInit?]))
      .toEqual({ attributeId, token });
  });

  it('deletes a replacement token that arrives after release and waits for cleanup', async () => {
    vi.useFakeTimers();
    const replacementToken = 'B'.repeat(24);
    let resolveReacquire: ((response: Response) => void) | undefined;
    const deletedTokens: string[] = [];
    const fetchImpl = vi.fn((_input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      if (init?.method === 'PUT') return Promise.resolve(responseError('编辑租约不存在'));
      if (init?.method === 'POST') {
        return new Promise<Response>((resolve) => { resolveReacquire = resolve; });
      }
      if (init?.method === 'DELETE') {
        deletedTokens.push(String(requestBody([_input, init]).token));
      }
      return Promise.resolve(success());
    });
    const session = maintainAttributeEditLease(attributeId, token, { fetchImpl });
    await vi.advanceTimersByTimeAsync(5_000);
    expect(resolveReacquire).toBeTypeOf('function');

    let releaseSettled = false;
    const release = session.release().then(() => { releaseSettled = true; });
    await Promise.resolve();
    expect(releaseSettled).toBe(false);
    resolveReacquire?.(preparedSession(replacementToken));
    await release;

    expect(deletedTokens).toEqual([token, replacementToken]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('finishes release without a second delete when an aborted recovery rejects', async () => {
    vi.useFakeTimers();
    const deletedTokens: string[] = [];
    const fetchImpl = vi.fn((_input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      if (init?.method === 'PUT') return Promise.resolve(responseError('编辑租约不存在'));
      if (init?.method === 'POST') return new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
      });
      if (init?.method === 'DELETE') deletedTokens.push(String(requestBody([_input, init]).token));
      return Promise.resolve(success());
    });
    const session = maintainAttributeEditLease(attributeId, token, { fetchImpl });
    await vi.advanceTimersByTimeAsync(5_000);

    await session.release();

    expect(deletedTokens).toEqual([token]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('acquires with only the attribute ID and accepts a 24-character token', async () => {
    const fetchImpl = vi.fn(async () => success({ token }));

    const session = await acquireAttributeEditLease(attributeId, { fetchImpl });

    expect(session.attributeId).toBe(attributeId);
    expect(session.token).toBe(token);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    const acquireCall = fetchImpl.mock.calls[0] as unknown as [RequestInfo | URL, RequestInit?];
    expect(acquireCall[0]).toBe('/api/attribute-edit-lease');
    expect(acquireCall[1]).toMatchObject({ method: 'POST' });
    expect(requestBody(acquireCall)).toEqual({ attributeId });
  });

  it('renews a healthy lease on its heartbeat interval', async () => {
    vi.useFakeTimers();
    const health = vi.fn();
    const fetchImpl = vi.fn()
      .mockResolvedValueOnce(success({ token }))
      .mockResolvedValueOnce(success());
    const session = await acquireAttributeEditLease(attributeId, { fetchImpl, onHealthChange: health });

    await vi.advanceTimersByTimeAsync(5_000);

    expect(fetchImpl.mock.calls[1][1]).toMatchObject({ method: 'PUT' });
    expect(requestBody(fetchImpl.mock.calls[1] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ attributeId, token });
    expect(health).toHaveBeenLastCalledWith('healthy');
    await session.release();
  });

  it('reports retrying after a heartbeat failure and becomes healthy after its retry succeeds', async () => {
    vi.useFakeTimers();
    const health = vi.fn();
    const fetchImpl = vi.fn()
      .mockResolvedValueOnce(success({ token }))
      .mockResolvedValueOnce(new Response(null, { status: 500 }))
      .mockResolvedValueOnce(success());
    const session = await acquireAttributeEditLease(attributeId, {
      fetchImpl,
      onHealthChange: health,
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(health).toHaveBeenLastCalledWith('retrying');
    await vi.advanceTimersByTimeAsync(1_000);

    expect(fetchImpl).toHaveBeenCalledTimes(3);
    expect(health).toHaveBeenLastCalledWith('healthy');
    await session.release();
  });

  it('times out a hung renewal and reacquires after the original lease actually expires', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const health = vi.fn();
    let activeToken = '';
    let expiresAt = 0;
    let creates = 0;
    let renews = 0;
    let hungSignal: AbortSignal | undefined;
    const fetchImpl = vi.fn((_: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const method = init?.method;
      if (method === 'POST') {
        creates += 1;
        activeToken = String.fromCharCode(64 + creates).repeat(24);
        expiresAt = Date.now() + 15_000;
        return Promise.resolve(success({ token: activeToken }));
      }
      if (method === 'PUT') {
        renews += 1;
        if (renews === 1) {
          hungSignal = init?.signal ?? undefined;
          return new Promise<Response>((_resolve, reject) => {
            hungSignal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
          });
        }
        const body = JSON.parse(String(init?.body)) as { token: string };
        if (Date.now() >= expiresAt || body.token !== activeToken) return Promise.resolve(responseError('编辑租约不存在'));
        expiresAt = Date.now() + 15_000;
        return Promise.resolve(success());
      }
      return Promise.resolve(success());
    });
    const session = await acquireAttributeEditLease(attributeId, {
      fetchImpl,
      heartbeatMs: 5_000,
      retryMs: 1_000,
      requestTimeoutMs: 11_000,
      onHealthChange: health,
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(hungSignal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(11_000);
    expect(hungSignal?.aborted).toBe(true);
    expect(health).toHaveBeenLastCalledWith('retrying');
    await vi.advanceTimersByTimeAsync(1_000);

    expect(creates).toBe(2);
    expect(session.token).toBe('B'.repeat(24));
    expect(health).toHaveBeenLastCalledWith('healthy');
    await session.release();
  });

  it('keeps the timeout active while reading a stalled renewal response body', async () => {
    vi.useFakeTimers();
    const health = vi.fn();
    let renewalSignal: AbortSignal | undefined;
    const fetchImpl = vi.fn((_: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      if (init?.method === 'POST') return Promise.resolve(success({ token }));
      if (init?.method === 'PUT') {
        renewalSignal = init.signal ?? undefined;
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => new Promise((_resolve, reject) => {
            renewalSignal?.addEventListener(
              'abort',
              () => reject(new DOMException('aborted', 'AbortError')),
              { once: true },
            );
          }),
        } as Response);
      }
      return Promise.resolve(success());
    });
    const session = await acquireAttributeEditLease(attributeId, {
      fetchImpl,
      requestTimeoutMs: 2_000,
      onHealthChange: health,
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(renewalSignal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(2_000);

    expect(renewalSignal?.aborted).toBe(true);
    expect(health).toHaveBeenLastCalledWith('retrying');
    await session.release();
    const requestCountAfterRelease = fetchImpl.mock.calls.length;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fetchImpl).toHaveBeenCalledTimes(requestCountAfterRelease);
  });

  it('releases exactly the replacement token after a not-found renewal reacquires', async () => {
    vi.useFakeTimers();
    const replacementToken = 'B'.repeat(24);
    let creates = 0;
    const fetchImpl = vi.fn(async (_: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') {
        creates += 1;
        return success({ token: creates === 1 ? token : replacementToken });
      }
      if (init?.method === 'PUT') return responseError('编辑租约不存在');
      return success();
    });
    const session = await acquireAttributeEditLease(attributeId, { fetchImpl });

    await vi.advanceTimersByTimeAsync(5_000);

    expect(creates).toBe(2);
    expect(session.token).toBe(replacementToken);
    await session.release();
    const releaseCall = fetchImpl.mock.calls.at(-1) as unknown as [RequestInfo | URL, RequestInit?];
    expect(releaseCall[1]).toMatchObject({ method: 'DELETE', keepalive: true });
    expect(requestBody(releaseCall)).toEqual({ attributeId, token: replacementToken });
  });

  it('reports retrying throughout not-found lease reacquisition', async () => {
    vi.useFakeTimers();
    const health = vi.fn();
    const replacementToken = 'B'.repeat(24);
    let creates = 0;
    let resolveReacquire: ((response: Response) => void) | undefined;
    const fetchImpl = vi.fn((_: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      if (init?.method === 'POST') {
        creates += 1;
        if (creates === 1) return Promise.resolve(success({ token }));
        return new Promise<Response>((resolve) => { resolveReacquire = resolve; });
      }
      if (init?.method === 'PUT') return Promise.resolve(responseError('编辑租约不存在'));
      return Promise.resolve(success());
    });
    const session = await acquireAttributeEditLease(attributeId, { fetchImpl, onHealthChange: health });

    await vi.advanceTimersByTimeAsync(5_000);

    expect(resolveReacquire).toBeTypeOf('function');
    expect(health).toHaveBeenLastCalledWith('retrying');
    resolveReacquire?.(success({ token: replacementToken }));
    await vi.advanceTimersByTimeAsync(0);
    expect(session.token).toBe(replacementToken);
    expect(health).toHaveBeenLastCalledWith('healthy');
    await session.release();
  });

  it('synchronously clears owned timers before one keepalive release request', async () => {
    vi.useFakeTimers();
    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval');
    const fetchImpl = vi.fn()
      .mockResolvedValueOnce(success({ token }))
      .mockResolvedValueOnce(new Response(null, { status: 500 }))
      .mockResolvedValueOnce(success());
    const session = await acquireAttributeEditLease(attributeId, { fetchImpl });

    await vi.advanceTimersByTimeAsync(5_000);
    const release = session.release();
    expect(clearIntervalSpy).toHaveBeenCalledTimes(1);
    expect(fetchImpl.mock.calls[2][1]).toMatchObject({ method: 'DELETE', keepalive: true });
    expect(requestBody(fetchImpl.mock.calls[2] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ attributeId, token });
    await release;
    const requestCountAfterRelease = fetchImpl.mock.calls.length;
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fetchImpl).toHaveBeenCalledTimes(requestCountAfterRelease);
  });

  it('makes repeated release calls idempotent', async () => {
    vi.useFakeTimers();
    const fetchImpl = vi.fn()
      .mockResolvedValueOnce(success({ token }))
      .mockResolvedValueOnce(success());
    const session = await acquireAttributeEditLease(attributeId, { fetchImpl });

    await Promise.all([session.release(), session.release()]);

    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('uses the same one-time release when the page unloads', async () => {
    vi.useFakeTimers();
    let unloadListener: (() => void) | undefined;
    const addEventListener = vi.fn((type: string, listener: () => void) => {
      if (type === 'beforeunload') unloadListener = listener;
    });
    const removeEventListener = vi.fn((type: string, listener: () => void) => {
      if (type === 'beforeunload' && unloadListener === listener) unloadListener = undefined;
    });
    vi.stubGlobal('addEventListener', addEventListener);
    vi.stubGlobal('removeEventListener', removeEventListener);
    const fetchImpl = vi.fn()
      .mockResolvedValueOnce(success({ token }))
      .mockResolvedValueOnce(success());
    await acquireAttributeEditLease(attributeId, { fetchImpl });

    unloadListener?.();
    await Promise.resolve();

    expect(removeEventListener).toHaveBeenCalledTimes(1);
    expect(fetchImpl.mock.calls[1][1]).toMatchObject({ method: 'DELETE', keepalive: true });
  });

  it.each([
    ['a malformed successful response', success({ token: 'short' }), '属性编辑租约响应无效'],
    ['a non-successful response', responseError(), '属性编辑租约请求失败'],
  ])('rejects %s with a stable Chinese error', async (_name, response, message) => {
    await expect(acquireAttributeEditLease(attributeId, {
      fetchImpl: vi.fn(async () => response),
    })).rejects.toThrow(message);
  });
});
