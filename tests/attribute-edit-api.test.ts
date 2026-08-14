import { describe, expect, it, vi } from 'vitest';
import {
  prepareAttributeEditSession,
  submitAttributeEdit,
} from '../src/ui/config/attribute-edit-api';
import { defaultState } from '../src/storage';

const token = 'A'.repeat(24);

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
});
