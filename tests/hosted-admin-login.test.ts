import { describe, expect, it, vi } from 'vitest';

import { HostedAPIError } from '../src/hosted/api';
import { createAdminLoginFlow, mountAdminLogin } from '../src/hosted/admin-login';

const emailChallenge = { challengeId: 'email-proof', expiresAt: '2030-01-01T00:00:00Z' };

function api(overrides: Record<string, unknown> = {}) {
  return {
    adminSession: vi.fn(async () => { throw new HostedAPIError('authentication_failed', 401); }),
    beginAdminEmailLogin: vi.fn(async () => emailChallenge),
    adminEmailLogin: vi.fn(async () => undefined),
    adminLogout: vi.fn(async () => undefined),
    ...overrides,
  };
}

describe('email-only administrator login flow', () => {
  it('uses a valid seven-day session without requesting an email code', async () => {
    const client = api({ adminSession: vi.fn(async () => undefined) });
    const states: string[] = [];
    const flow = createAdminLoginFlow(client, (state) => states.push(state.kind));
    await flow.start();
    expect(states).toEqual(['checking-session', 'signed-in']);
    expect(client.beginAdminEmailLogin).not.toHaveBeenCalled();
  });

  it('submits six email digits directly and signs in without a login TOTP step', async () => {
    const client = api(); const states: string[] = [];
    const flow = createAdminLoginFlow(client, (state) => states.push(state.kind));
    await flow.start(); await Promise.all([flow.startEmail(), flow.startEmail()]);
    expect(client.beginAdminEmailLogin).toHaveBeenCalledTimes(1);
    expect(states.at(-1)).toBe('awaiting-email-code');
    expect(flow.state()).toEqual({ kind: 'awaiting-email-code' });
    await Promise.all([flow.submitEmailCode('654321'), flow.submitEmailCode('654321')]);
    expect(client.adminEmailLogin).toHaveBeenCalledTimes(1);
    expect(client.adminEmailLogin).toHaveBeenCalledWith('email-proof', '654321');
    expect(states).not.toContain('awaiting-email-totp');
    expect(states.at(-1)).toBe('signed-in');
  });

  it('erases a rejected code and requires a new challenge rather than reusing its ID', async () => {
    const client = api({ adminEmailLogin: vi.fn(async () => { throw new HostedAPIError('authentication_failed', 401); }) });
    const flow = createAdminLoginFlow(client, vi.fn());
    await flow.start(); await flow.startEmail();
    await expect(flow.submitEmailCode('654321')).rejects.toMatchObject({ status: 401 });
    expect(flow.state()).toMatchObject({ kind: 'email-error', reason: 'invalid-or-expired-code' });
    await expect(flow.submitEmailCode('654321')).rejects.toMatchObject({ code: 'invalid_request' });
    await flow.startEmail();
    expect(client.beginAdminEmailLogin).toHaveBeenCalledTimes(2);
  });

  it('maps rate limiting and email unavailability to their distinct stable states', async () => {
    const rateLimited = api({ beginAdminEmailLogin: vi.fn(async () => { throw new HostedAPIError('rate_limited', 429); }) });
    const rateFlow = createAdminLoginFlow(rateLimited, vi.fn());
    await rateFlow.start(); await rateFlow.startEmail();
    expect(rateFlow.state()).toEqual({ kind: 'rate-limited' });

    const unavailable = api({ beginAdminEmailLogin: vi.fn(async () => { throw new HostedAPIError('temporarily_unavailable', 503); }) });
    const unavailableFlow = createAdminLoginFlow(unavailable, vi.fn());
    await unavailableFlow.start(); await unavailableFlow.startEmail();
    expect(unavailableFlow.state()).toMatchObject({ kind: 'email-error', reason: 'email-unavailable' });
  });

  it('holds a 429 for the backend one-minute window then restores ready', async () => {
    vi.useFakeTimers();
    try {
      const beginAdminEmailLogin = vi.fn()
        .mockRejectedValueOnce(new HostedAPIError('rate_limited', 429))
        .mockResolvedValueOnce(emailChallenge);
      const flow = createAdminLoginFlow(api({ beginAdminEmailLogin }), vi.fn());
      await flow.start(); await flow.startEmail();
      expect(flow.state()).toEqual({ kind: 'rate-limited' });
      await expect(flow.startEmail()).rejects.toMatchObject({ code: 'invalid_request' });
      await vi.advanceTimersByTimeAsync(59_999);
      expect(flow.state()).toEqual({ kind: 'rate-limited' });
      await vi.advanceTimersByTimeAsync(1);
      expect(flow.state()).toEqual({ kind: 'ready' });
      await flow.startEmail();
      expect(beginAdminEmailLogin).toHaveBeenCalledTimes(2);
      expect(flow.state()).toEqual({ kind: 'awaiting-email-code' });
      await flow.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it('cancels the 429 cooldown so disposal cannot publish ready later', async () => {
    vi.useFakeTimers();
    try {
      const states: string[] = [];
      const flow = createAdminLoginFlow(api({ beginAdminEmailLogin: vi.fn(async () => { throw new HostedAPIError('rate_limited', 429); }) }), (state) => states.push(state.kind));
      await flow.start(); await flow.startEmail(); await flow.dispose();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(flow.state()).toEqual({ kind: 'disposed' });
      expect(states.at(-1)).toBe('rate-limited');
    } finally {
      vi.useRealTimers();
    }
  });

  it('rejects B while A is pending, compensates late A, then lets B retry successfully', async () => {
    let finishA!: () => void; const pendingA = new Promise<void>((resolve) => { finishA = resolve; });
    const client = api({
      beginAdminEmailLogin: vi.fn().mockResolvedValueOnce({ challengeId: 'challenge-a', expiresAt: emailChallenge.expiresAt }).mockResolvedValueOnce({ challengeId: 'challenge-b', expiresAt: emailChallenge.expiresAt }),
      adminEmailLogin: vi.fn().mockImplementationOnce(() => pendingA).mockResolvedValueOnce(undefined),
    });
    const flow = createAdminLoginFlow(client, vi.fn());
    await flow.start(); await flow.startEmail();
    const submitA = flow.submitEmailCode('111111');
    await flow.startEmail();
    const blockedB = flow.submitEmailCode('222222');
    expect(blockedB).not.toBe(submitA);
    await expect(blockedB).rejects.toMatchObject({ code: 'operation_conflict', status: 409 });
    expect(client.adminEmailLogin).toHaveBeenCalledTimes(1);
    expect(flow.state()).toEqual({ kind: 'awaiting-email-code' });
    finishA(); await submitA;
    expect(client.adminLogout).toHaveBeenCalledTimes(1);
    await flow.submitEmailCode('333333');
    expect(client.adminEmailLogin.mock.calls).toEqual([['challenge-a', '111111'], ['challenge-b', '333333']]);
    expect(flow.state()).toEqual({ kind: 'signed-in' });
  });

  it('erases internal challenge ownership on dispose and compensates late success with admin logout', async () => {
    let finish!: () => void; const pending = new Promise<void>((resolve) => { finish = resolve; });
    const client = api({ adminEmailLogin: vi.fn(async () => pending) });
    const flow = createAdminLoginFlow(client, vi.fn());
    await flow.start(); await flow.startEmail();
    const submitting = flow.submitEmailCode('654321'); const disposing = flow.dispose();
    finish(); await Promise.all([submitting, disposing]);
    expect(client.adminLogout).toHaveBeenCalledTimes(1);
    expect(flow.state()).toEqual({ kind: 'disposed' });
    await expect(flow.submitEmailCode('654321')).rejects.toMatchObject({ code: 'invalid_request' });
  });
});

describe('email-only administrator login view', () => {
  it('offers one email action, omits Bilibili QR/mobile/TOTP UI, and sends six digits directly', async () => {
    class Element {
      children: Element[] = []; parent?: Element; connected = false; textContent = ''; className = ''; type = ''; value = ''; disabled = false; inputMode = ''; autocomplete = ''; src = ''; alt = ''; href = ''; target = ''; rel = ''; selectionStart: number | null = null;
      attributes = new Map<string, string>(); listeners = new Map<string, () => void>(); classList = { toggle: vi.fn() };
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element; activeElement?: Element }) {}
      get isConnected(): boolean { return this.connected || this.parent?.isConnected === true; }
      append(...nodes: Element[]) { for (const node of nodes) { node.parent = this; this.children.push(node); } }
      replaceChildren(...nodes: Element[]) { for (const child of this.children) child.parent = undefined; this.children = []; this.append(...nodes); }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); }
      addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
      removeEventListener(name: string) { this.listeners.delete(name); }
      focus() { if (this.isConnected) { this.ownerDocument.activeElement = this; this.selectionStart = this.value.length; } }
    }
    const document = { createElement: (tag: string): Element => new Element(tag, document), activeElement: undefined as Element | undefined };
    const root = new Element('div', document); root.connected = true;
    const client = api();
    const mounted = mountAdminLogin(root as unknown as HTMLElement, client, { onSignedIn: vi.fn() });
    await vi.waitFor(() => expect(root.children[0]?.children.filter((child) => child.tagName === 'button').map((child) => child.textContent)).toEqual(['发送邮箱验证码']));
    expect(root.children[0].children.map((child) => child.textContent).join('')).not.toContain('B站');
    expect(root.children[0].children.some((child) => child.tagName === 'img' || child.tagName === 'a')).toBe(false);
    root.children[0].children[3]?.listeners.get('click')?.();
    await vi.waitFor(() => expect(root.children[0]?.children.some((child) => child.className === 'hosted-code-control')).toBe(true));
    const codeRoot = root.children[0].children.find((child) => child.className === 'hosted-code-control');
    const codeInput = codeRoot?.children[0];
    expect(document.activeElement?.attributes.get('aria-label')).toBe('六位邮箱验证码');
    expect(document.activeElement?.isConnected).toBe(true);
    expect(document.activeElement?.selectionStart).toBe(0);
    if (codeInput) { codeInput.value = '654321'; codeInput.listeners.get('input')?.(); }
    await vi.waitFor(() => expect(client.adminEmailLogin).toHaveBeenCalledWith('email-proof', '654321'));
    await mounted.dispose();

    const rateRoot = new Element('div', document); rateRoot.connected = true;
    const rateClient = api({ beginAdminEmailLogin: vi.fn(async () => { throw new HostedAPIError('rate_limited', 429); }) });
    const rateMount = mountAdminLogin(rateRoot as unknown as HTMLElement, rateClient, { onSignedIn: vi.fn() });
    await vi.waitFor(() => expect(rateRoot.children[0]?.children.some((child) => child.textContent === '发送邮箱验证码')).toBe(true));
    rateRoot.children[0].children.find((child) => child.tagName === 'button')?.listeners.get('click')?.();
    await vi.waitFor(() => expect(rateRoot.children[0]?.children.some((child) => child.textContent === '操作过于频繁，请稍后重试')).toBe(true));
    expect(rateRoot.children[0].children.filter((child) => child.tagName === 'button')).toHaveLength(0);
    await rateMount.dispose();
  });
});
