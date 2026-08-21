import { describe, expect, expectTypeOf, it, vi } from 'vitest';
import { HostedAPI } from '../src/hosted/api';
import { createInvitationFlow, mountInvitationView } from '../src/hosted/invitations';
import { createAdminRecoveryFlow } from '../src/hosted/admin';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

describe('streamer invitation view lifecycle', () => {
  it('normalizes an omitted final streamer quota to zero and rejects non-masked code hints', async () => {
    const generated = { id: 6, codeHint: '****LAST', code: 'FULL-LAST-CODE', status: 'active', createdAt: '2026-08-16T00:00:00Z', expiresAt: '2026-08-17T00:00:00Z' };
    let payload: unknown = generated;
    const api = await HostedAPI.connect(async (input, init) => input === '/api/bootstrap'
      ? new Response(JSON.stringify({ csrfToken: 'csrf' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      : new Response(JSON.stringify(payload), { status: input === '/api/invitations' && init?.method === 'POST' ? 201 : 200, headers: { 'Content-Type': 'application/json' } }));
    await expect(api.generateInvitation()).resolves.toEqual(expect.objectContaining({ remainingQuota: 0 }));
    const { code: _code, ...record } = generated;
    payload = { remainingQuota: 0, invitations: [{ ...record, codeHint: 'notmask!', status: 'revoked' }] };
    await expect(api.listInvitations()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('displays and enforces the final zero quota when generate omits it', async () => {
    class Element {
      children: Element[] = []; textContent = ''; className = ''; type = ''; disabled = false; open = false; id = '';
      listeners = new Map<string, () => void>(); attributes = new Map<string, string>();
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element }) {}
      append(...nodes: Element[]) { this.children.push(...nodes); }
      replaceChildren(...nodes: Element[]) { this.children = nodes; }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); }
      addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
    }
    const document = { createElement: (tag: string): Element => new Element(tag, document) };
    const root = new Element('div', document) as unknown as HTMLElement;
    const generated = { id: 6, codeHint: '****LAST', code: 'FULL-LAST-CODE', status: 'active', createdAt: '2026-08-16T00:00:00Z', expiresAt: '2026-08-17T00:00:00Z' };
    const api = await HostedAPI.connect(async (input, init) => {
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      if (input === '/api/invitations' && init?.method === 'GET') return json({ remainingQuota: 1, invitations: [] });
      return json(generated, 201);
    });
    const mounted = mountInvitationView(root, api); await mounted.ready;
    let panel = (root as unknown as Element).children[0];
    panel.children.find((child) => child.tagName === 'button' && child.textContent === '生成邀请码')?.listeners.get('click')?.();
    await vi.waitFor(() => {
      panel = (root as unknown as Element).children[0];
      expect(panel.children.find((child) => child.tagName === 'p' && child.textContent.startsWith('剩余邀请码额度'))?.textContent).toBe('剩余邀请码额度：0');
    });
    expect(panel.children.find((child) => child.tagName === 'button' && child.textContent === '生成邀请码')?.disabled).toBe(true);
    mounted.dispose();
  });
  it('renders quota and masked permanent history including revoked rows', async () => {
    const render = vi.fn();
    const api = { listInvitations: vi.fn(async () => ({ remainingQuota: 2, invitations: [{ id: 9, codeHint: '••••WXYZ', status: 'revoked' as const, createdAt: '2026-08-16T00:00:00Z', expiresAt: '2026-08-17T00:00:00Z', revokedAt: '2026-08-16T01:00:00Z' }] })) };
    const flow = createInvitationFlow(api, render, { writeText: vi.fn() });
    await flow.refresh();
    expect(render).toHaveBeenLastCalledWith(expect.objectContaining({ remainingQuota: 2, invitations: [expect.objectContaining({ codeHint: '••••WXYZ', status: 'revoked' })] }));
  });

  it('keeps a complete generated code only until copy/close and masks it after close', async () => {
    const secret = 'FULL-INVITATION-CODE';
    const snapshots: unknown[] = [];
    const clipboard = { writeText: vi.fn(async () => undefined) };
    const api = { generateInvitation: vi.fn(async () => ({ id: 3, codeHint: '••••CODE', code: secret, status: 'active' as const, createdAt: '2026-08-16T00:00:00Z', expiresAt: '2026-08-17T00:00:00Z', remainingQuota: 1 })), listInvitations: vi.fn(async () => ({ remainingQuota: 1, invitations: [] })) };
    const flow = createInvitationFlow(api, (state) => snapshots.push(structuredClone(state)), clipboard);
    await flow.generate();
    await flow.copy();
    flow.closeReveal();
    expect(clipboard.writeText).toHaveBeenCalledWith(secret);
    expect(JSON.stringify(snapshots.at(-1))).not.toContain(secret);
    expect(JSON.stringify(flow)).not.toContain(secret);
  });

  it('redeems an invitation using the registration intent without persistence or URL secrets', async () => {
    const redeem = vi.fn(async () => undefined);
    const flow = createInvitationFlow({ redeemInvitation: redeem }, vi.fn(), { writeText: vi.fn() }, 'intent-secret');
    await flow.redeem('invite-secret');
    expect(redeem).toHaveBeenCalledWith('invite-secret', 'intent-secret');
    expect(globalThis.location?.href ?? '').not.toContain('invite-secret');
  });

  it('does not transition a registration view when redeem completes after unmount', async () => {
    class Element {
      children: Element[] = []; textContent = ''; className = ''; type = ''; autocomplete = ''; required = false; value = '';
      listeners = new Map<string, () => void>(); attributes = new Map<string, string>();
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element }) {}
      append(...nodes: Element[]) { this.children.push(...nodes); }
      replaceChildren(...nodes: Element[]) { this.children = nodes; }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); }
      addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
    }
    const document = { createElement: (tag: string): Element => new Element(tag, document) };
    const root = new Element('div', document) as unknown as HTMLElement;
    let release!: () => void;
    const pending = new Promise<void>((resolve) => { release = resolve; });
    const redeem = vi.fn(() => pending); const registered = vi.fn();
    const mounted = mountInvitationView(root, { redeemInvitation: redeem } as unknown as HostedAPI, 'registration-intent', registered);
    const panel = (root as unknown as Element).children[0];
    const input = panel.children.find((child) => child.tagName === 'label')?.children[0]; if (input) input.value = 'invite-code';
    panel.children.find((child) => child.tagName === 'button')?.listeners.get('click')?.();
    await vi.waitFor(() => expect(redeem).toHaveBeenCalledTimes(1)); mounted.dispose(); release(); await pending; await Promise.resolve();
    expect(registered).not.toHaveBeenCalled();
    expect((root as unknown as Element).children).toEqual([]);
  });

  it('discards a generated full code that arrives after unmount', async () => {
    let release!: (value: { id: number; codeHint: string; code: string; status: 'active'; createdAt: string; expiresAt: string; remainingQuota: number }) => void;
    const pending = new Promise<{ id: number; codeHint: string; code: string; status: 'active'; createdAt: string; expiresAt: string; remainingQuota: number }>((resolve) => { release = resolve; });
    const states: unknown[] = [];
    const flow = createInvitationFlow({ generateInvitation: () => pending }, (state) => states.push(structuredClone(state)), { writeText: vi.fn() });
    const generating = flow.generate(); flow.dispose();
    release({ id: 4, codeHint: '••••LATE', code: 'LATE-FULL-SECRET', status: 'active', createdAt: '2026-08-16T00:00:00Z', expiresAt: '2026-08-17T00:00:00Z', remainingQuota: 0 });
    await generating;
    expect(JSON.stringify(states)).not.toContain('LATE-FULL-SECRET');
    expect(JSON.stringify(flow)).not.toContain('LATE-FULL-SECRET');
  });

  it('allows only one generation in flight and immediately records its masked history', async () => {
    let release!: (value: { id: number; codeHint: string; code: string; status: 'active'; createdAt: string; expiresAt: string; remainingQuota: number }) => void;
    const pending = new Promise<{ id: number; codeHint: string; code: string; status: 'active'; createdAt: string; expiresAt: string; remainingQuota: number }>((resolve) => { release = resolve; });
    const generate = vi.fn(() => pending); const states: Array<{ invitations: Array<{ codeHint: string }> }> = [];
    const flow = createInvitationFlow({ generateInvitation: generate }, (state) => states.push(structuredClone(state)), { writeText: vi.fn() });
    const first = flow.generate(); const second = flow.generate();
    expect(generate).toHaveBeenCalledTimes(1);
    release({ id: 5, codeHint: '••••MASK', code: 'ONLY-FULL-CODE', status: 'active', createdAt: '2026-08-16T00:00:00Z', expiresAt: '2026-08-17T00:00:00Z', remainingQuota: 0 });
    await first; await second; flow.closeReveal();
    expect(states.at(-1)).toEqual(expect.objectContaining({ remainingQuota: 0, invitations: [expect.objectContaining({ codeHint: '••••MASK' })] }));
  });
});

describe('administrator recovery secret lifecycle', () => {
  it('separates emailed archive from password and gates confirm on all acknowledgements', async () => {
    const secretURI = 'otpauth://totp/panel?secret=NEWSECRET';
    const password = '12345678901234567890';
    const states: unknown[] = [];
    const api = { prepareRecovery: vi.fn(async () => ({ totpUri: secretURI, recoveryPassword: password, handoffToken: 'opaque-handoff' })), confirmRecovery: vi.fn(async () => undefined) };
    const flow = createAdminRecoveryFlow(api, (state) => states.push(structuredClone(state)));
    expectTypeOf(flow.prepare).parameters.toEqualTypeOf<[recoveryCode: string]>();
    await flow.prepare('old-recovery-code');
    expect(states.at(-1)).toEqual(expect.objectContaining({ totpUri: secretURI, recoveryPassword: password, archiveDelivery: 'email', canConfirm: false }));
    flow.acknowledge('totp'); flow.acknowledge('password');
    expect(states.at(-1)).toEqual(expect.objectContaining({ canConfirm: false }));
    flow.acknowledge('archive');
    expect(states.at(-1)).toEqual(expect.objectContaining({ canConfirm: true }));
    flow.acknowledge('archive', false);
    expect(states.at(-1)).toEqual(expect.objectContaining({ canConfirm: false, acknowledged: expect.objectContaining({ archive: false }) }));
    flow.acknowledge('archive');
    await flow.confirm('123456');
    expect(api.confirmRecovery).toHaveBeenCalledWith('opaque-handoff', '123456');
    expect(JSON.stringify(states.at(-1))).not.toContain('NEWSECRET');
    expect(JSON.stringify(states.at(-1))).not.toContain(password);
    expect(JSON.stringify(flow)).not.toContain('opaque-handoff');
  });

  it('can retry prepare with the same old code without a Bilibili proof', async () => {
    const prepare = vi.fn(async () => ({ totpUri: 'otpauth://new', recoveryPassword: '12345678901234567890', handoffToken: 'same-handoff' }));
    const flow = createAdminRecoveryFlow({ prepareRecovery: prepare, confirmRecovery: vi.fn() }, vi.fn());
    await flow.prepare('same-old-code');
    flow.close();
    await flow.prepare('same-old-code');
    expect(prepare).toHaveBeenNthCalledWith(2, 'same-old-code');
  });

  it('discards recovery handoff secrets that arrive after close', async () => {
    let release!: (value: { totpUri: string; recoveryPassword: string; handoffToken: string }) => void;
    const pending = new Promise<{ totpUri: string; recoveryPassword: string; handoffToken: string }>((resolve) => { release = resolve; });
    const states: unknown[] = [];
    const flow = createAdminRecoveryFlow({ prepareRecovery: () => pending, confirmRecovery: vi.fn() }, (state) => states.push(structuredClone(state)));
    const preparing = flow.prepare('old-code'); flow.close();
    release({ totpUri: 'otpauth://late-secret', recoveryPassword: '12345678901234567890', handoffToken: 'late-token' });
    await preparing;
    expect(JSON.stringify(states)).not.toContain('late-secret');
    expect(JSON.stringify(states)).not.toContain('late-token');
  });

  it('publishes a visible generic recovery confirmation error without exposing secrets', async () => {
    const states: unknown[] = [];
    const flow = createAdminRecoveryFlow({
      prepareRecovery: vi.fn(async () => ({ totpUri: 'otpauth://new-secret', recoveryPassword: '12345678901234567890', handoffToken: 'private-handoff' })),
      confirmRecovery: vi.fn(async () => { throw new Error('private backend detail'); }),
    }, (state) => states.push(structuredClone(state)));
    await flow.prepare('old-code');
    flow.acknowledge('totp'); flow.acknowledge('password'); flow.acknowledge('archive');
    await expect(flow.confirm('123456')).rejects.toThrow();
    expect(states.at(-1)).toEqual(expect.objectContaining({ error: '确认失败，请重试' }));
    expect(JSON.stringify(states.at(-1))).not.toContain('private backend detail');
    expect(JSON.stringify(states.at(-1))).not.toContain('private-handoff');
  });
});
