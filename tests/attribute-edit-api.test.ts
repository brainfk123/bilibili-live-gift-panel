import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  prepareAttributeEditSession,
  submitAttributeEdit,
} from '../src/ui/config/attribute-edit-api';
import { defaultState } from '../src/storage';

const token = 'A'.repeat(24);

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function sessionPayload(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    code: 0,
    attributeId: 'attribute-1',
    token,
    expiresAt: '2026-08-14T00:00:15Z',
    state: defaultState(),
    ...overrides,
  };
}

function requestBody(call: readonly unknown[]): Record<string, unknown> {
  return JSON.parse(String((call[1] as RequestInit | undefined)?.body));
}

describe('atomic attribute edit API', () => {
  it('prepares an existing attribute with the exact session endpoint and returns its authoritative state', async () => {
    const state = defaultState();
    state.attributes = [{ id: 'attribute-1', name: '积分', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '' }];
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => Response.json({
      code: 0,
      attributeId: 'attribute-1',
      token,
      expiresAt: '2026-08-14T00:00:15Z',
      state,
    }));

    const prepared = await prepareAttributeEditSession({ attributeId: 'attribute-1' }, { fetchImpl });

    expect(fetchImpl.mock.calls[0][0]).toBe('/api/attribute-edits/session');
    expect(fetchImpl.mock.calls[0][1]).toMatchObject({ method: 'POST' });
    expect(requestBody(fetchImpl.mock.calls[0] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ attributeId: 'attribute-1' });
    expect(prepared).toMatchObject({ attributeId: 'attribute-1', token, state });
    await prepared.lease.release();
  });

  it('prepares a legacy attribute by name without sending an alternate field', async () => {
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => Response.json({
      code: 0,
      attributeId: 'attribute-1',
      token,
      expiresAt: '2026-08-14T00:00:15Z',
      state: defaultState(),
    }));

    const prepared = await prepareAttributeEditSession({ legacyName: '积分' }, { fetchImpl });

    expect(requestBody(fetchImpl.mock.calls[0] as unknown as [RequestInfo | URL, RequestInit?])).toEqual({ legacyName: '积分' });
    await prepared.lease.release();
  });

  it('submits only the atomic edit aggregate and returns the server-normalized state', async () => {
    const state = defaultState();
    state.attributes = [{ id: 'attribute-1', name: '新积分', value: 2, unit: 'none', format: 'number', decimals: 0, suffix: '' }];
    const input = {
      target: { kind: 'existing' as const, attributeId: 'attribute-1', leaseToken: token },
      attribute: state.attributes[0],
      giftRules: [],
      timerRules: [],
      giftCatalogUpserts: [],
    };
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => Response.json({
      code: 0,
      target: { id: 'attribute-1', name: '新积分', created: false },
      state,
    }));

    const submitted = await submitAttributeEdit(input, { fetchImpl });

    expect(fetchImpl.mock.calls[0][0]).toBe('/api/attribute-edits');
    expect(fetchImpl.mock.calls[0][1]).toMatchObject({ method: 'POST' });
    expect(requestBody(fetchImpl.mock.calls[0] as unknown as [RequestInfo | URL, RequestInit?])).toEqual(input);
    expect(submitted).toEqual({ target: { id: 'attribute-1', name: '新积分', created: false }, state });
  });

  it.each([
    ['a non-success response', new Response(null, { status: 409 }), '属性编辑请求失败'],
    ['a malformed session payload', Response.json({ code: 0, attributeId: 'attribute-1', token, state: {} }), '属性编辑响应无效'],
  ])('rejects %s with a stable error', async (_name, response, message) => {
    await expect(prepareAttributeEditSession({ attributeId: 'attribute-1' }, {
      fetchImpl: vi.fn(async () => response),
    })).rejects.toThrow(message);
  });

  it('rejects invalid runtime targets before issuing an unsafe submit request', async () => {
    const fetchImpl = vi.fn();
    await expect(submitAttributeEdit({
      target: { kind: 'existing', attributeId: 'attribute-1', leaseToken: 'invalid' },
      attribute: { name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      giftRules: [], timerRules: [], giftCatalogUpserts: [],
    }, { fetchImpl })).rejects.toThrow('属性编辑目标无效');
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it.each([
    ['an extra session response key', sessionPayload({ extra: true })],
    ['an invalid timestamp', sessionPayload({ expiresAt: 'not-a-timestamp' })],
    ['an impossible calendar date', sessionPayload({ expiresAt: '2026-02-30T00:00:15Z' })],
    ['an invalid timezone offset', sessionPayload({ expiresAt: '2026-08-14T00:00:15+24:00' })],
    ['a mismatched prepared attribute', sessionPayload({ attributeId: 'attribute-2' })],
    ['a null attribute member', sessionPayload({ state: { ...defaultState(), attributes: [null] } })],
    ['an incomplete attribute member', sessionPayload({ state: { ...defaultState(), attributes: [{ name: '积分' }] } })],
  ])('rejects %s', async (_name, payload) => {
    await expect(prepareAttributeEditSession({ attributeId: 'attribute-1' }, {
      fetchImpl: vi.fn(async () => Response.json(payload)),
    })).rejects.toThrow('属性编辑响应无效');
  });

  it.each([
    '2024-02-29T23:59:59.123456789Z',
    '2026-08-14T00:00:15-23:59',
  ])('accepts a calendar-valid RFC3339 session expiry %s', async (expiresAt) => {
    const fetchImpl = vi.fn(async () => Response.json(sessionPayload({ expiresAt })));
    const prepared = await prepareAttributeEditSession({ attributeId: 'attribute-1' }, { fetchImpl });
    expect(prepared.expiresAt).toBe(expiresAt);
    await prepared.lease.release();
  });

  it.each([
    [{ attributeId: 'attribute-1', extra: true }],
    [{ attributeId: 'attribute-1', legacyName: '积分' }],
  ])('rejects non-exact session request target %j before fetch', async (target) => {
    const fetchImpl = vi.fn();
    await expect(prepareAttributeEditSession(target as { attributeId: string }, { fetchImpl }))
      .rejects.toThrow('属性编辑目标无效');
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it.each([
    [{ code: 0, target: { id: 'attribute-1', name: '积分', created: false }, state: defaultState(), extra: true }],
    [{ code: 0, target: { id: 'attribute-1', name: '积分', created: false, extra: true }, state: defaultState() }],
    [{ code: 0, target: { id: 'attribute-1', name: '积分', created: false }, state: { ...defaultState(), rules: [null] } }],
    [{
      code: 0, target: { id: 'attribute-1', name: '积分', created: false },
      state: {
        ...defaultState(),
        attributes: [{
          name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '',
          display: { variant: 'number', themeId: 'not-a-theme' },
        }],
      },
    }],
    [{
      code: 0, target: { id: 'attribute-1', name: '积分', created: false },
      state: {
        ...defaultState(),
        settings: {
          ...defaultState().settings,
          tutorialCompletedLessons: ['not-a-lesson'],
          trainingCompletedTopics: ['not-a-topic'],
        },
      },
    }],
  ])('rejects malformed submit payload %j', async (payload) => {
    await expect(submitAttributeEdit({
      target: { kind: 'new' },
      attribute: { name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      giftRules: [], timerRules: [], giftCatalogUpserts: [],
    }, { fetchImpl: vi.fn(async () => Response.json(payload)) })).rejects.toThrow('属性编辑响应无效');
  });

  it('rejects extra submit request keys before fetch', async () => {
    const fetchImpl = vi.fn();
    await expect(submitAttributeEdit({
      target: { kind: 'new' },
      attribute: { name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      giftRules: [], timerRules: [], giftCatalogUpserts: [], extra: true,
    } as never, { fetchImpl })).rejects.toThrow('属性编辑请求无效');
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it('times out a stalled submit fetch once and cleans its timer', async () => {
    vi.useFakeTimers();
    let signal: AbortSignal | undefined;
    const fetchImpl = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      signal = init?.signal ?? undefined;
      return new Promise<Response>((_resolve, reject) => {
        signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
      });
    });
    const submit = submitAttributeEdit({
      target: { kind: 'new' },
      attribute: { name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      giftRules: [], timerRules: [], giftCatalogUpserts: [],
    }, { fetchImpl, requestTimeoutMs: 100 });
    const rejected = expect(submit).rejects.toThrow('属性编辑请求失败');

    await vi.advanceTimersByTimeAsync(100);

    await rejected;
    expect(signal?.aborted).toBe(true);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('keeps the timeout active through a stalled submit JSON body', async () => {
    vi.useFakeTimers();
    let signal: AbortSignal | undefined;
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      signal = init?.signal ?? undefined;
      return {
        ok: true,
        status: 200,
        json: () => new Promise((_resolve, reject) => {
          signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true });
        }),
      } as Response;
    });
    const submit = submitAttributeEdit({
      target: { kind: 'new' },
      attribute: { name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      giftRules: [], timerRules: [], giftCatalogUpserts: [],
    }, { fetchImpl, requestTimeoutMs: 100 });
    const rejected = expect(submit).rejects.toThrow('属性编辑请求失败');
    await Promise.resolve();

    await vi.advanceTimersByTimeAsync(100);

    await rejected;
    expect(signal?.aborted).toBe(true);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('reports a stable network error without retrying submit', async () => {
    const fetchImpl = vi.fn(async () => { throw new TypeError('socket secret'); });

    await expect(submitAttributeEdit({
      target: { kind: 'new' },
      attribute: { name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      giftRules: [], timerRules: [], giftCatalogUpserts: [],
    }, { fetchImpl })).rejects.toThrow('属性编辑请求失败');
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });
});
