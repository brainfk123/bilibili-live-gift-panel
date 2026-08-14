import { afterEach, describe, expect, it, vi } from 'vitest';
import { acquireAttributeEditLease } from '../src/ui/config/attribute-edit-lease';

const attributeId = 'attribute-1';
const token = 'A'.repeat(24);

function success(payload: Record<string, unknown> = {}): Response {
  return Response.json({ code: 0, ...payload });
}

function responseError(message = '属性不存在'): Response {
  return Response.json({ code: -1, message }, { status: 404 });
}

function requestBody(call: readonly unknown[]): Record<string, unknown> {
  return JSON.parse(String((call[1] as RequestInit | undefined)?.body));
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('attribute edit lease client', () => {
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
      .mockResolvedValueOnce(responseError())
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

  it('synchronously clears owned timers before one keepalive release request', async () => {
    vi.useFakeTimers();
    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval');
    const clearTimeoutSpy = vi.spyOn(globalThis, 'clearTimeout');
    const fetchImpl = vi.fn()
      .mockResolvedValueOnce(success({ token }))
      .mockResolvedValueOnce(responseError())
      .mockResolvedValueOnce(success());
    const session = await acquireAttributeEditLease(attributeId, { fetchImpl });

    await vi.advanceTimersByTimeAsync(5_000);
    const release = session.release();
    expect(clearIntervalSpy).toHaveBeenCalledTimes(1);
    expect(clearTimeoutSpy).toHaveBeenCalledTimes(1);
    expect(fetchImpl.mock.calls[2][1]).toMatchObject({ method: 'DELETE', keepalive: true });
    expect(requestBody(fetchImpl.mock.calls[2] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ attributeId, token });
    await release;
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
