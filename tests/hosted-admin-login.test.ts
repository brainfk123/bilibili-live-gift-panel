import { describe, expect, it, vi } from 'vitest';

import { HostedAPIError } from '../src/hosted/api';
import { createAdminLoginFlow, mountAdminLogin } from '../src/hosted/admin-login';

const challenge = { challengeId: 'admin-proof', qrImage: 'data:image/png;base64,qr', verificationUrl: 'https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=safe', expiresAt: '2030-01-01T00:00:00Z' };

function api(overrides: Record<string, unknown> = {}) {
  return {
    adminSession: vi.fn(async () => { throw new HostedAPIError('authentication_failed', 401); }),
    beginAdminProof: vi.fn(async () => challenge),
    pollAdminProof: vi.fn(async () => ({ status: 'pending' as const, expiresAt: challenge.expiresAt })),
    adminLogin: vi.fn(async () => undefined),
    cancelAdminProof: vi.fn(async () => undefined),
    logout: vi.fn(async () => undefined),
    ...overrides,
  };
}

describe('progressive administrator login flow', () => {
  it('uses a valid seven-day session without creating a proof', async () => {
    const client = api({ adminSession: vi.fn(async () => undefined) });
    const states: string[] = [];
    const flow = createAdminLoginFlow(client, (state) => states.push(state.kind));
    await flow.start();
    expect(states).toEqual(['checking-session', 'signed-in']);
    expect(client.beginAdminProof).not.toHaveBeenCalled();
  });

  it('creates one proof, waits for Bilibili, then consumes one six-digit code', async () => {
    const client = api({ pollAdminProof: vi.fn().mockResolvedValueOnce({ status: 'pending', expiresAt: challenge.expiresAt }).mockResolvedValueOnce({ status: 'verified', expiresAt: challenge.expiresAt }) });
    const states: string[] = [];
    const flow = createAdminLoginFlow(client, (state) => states.push(state.kind));
    await Promise.all([flow.start(), flow.start()]);
    expect(client.beginAdminProof).toHaveBeenCalledTimes(1);
    await flow.poll(); await flow.poll();
    expect(states.at(-1)).toBe('awaiting-totp');
    await Promise.all([flow.submit('123456'), flow.submit('123456')]);
    expect(client.adminLogin).toHaveBeenCalledTimes(1);
    expect(client.adminLogin).toHaveBeenCalledWith('admin-proof', '123456');
    expect(states.at(-1)).toBe('signed-in');
  });

  it('keeps service failure retryable without creating a proof', async () => {
    const client = api({ adminSession: vi.fn(async () => { throw new HostedAPIError('temporarily_unavailable', 503); }) });
    const states: string[] = [];
    const flow = createAdminLoginFlow(client, (state) => states.push(state.kind));
    await flow.start();
    expect(states.at(-1)).toBe('service-unavailable');
    expect(client.beginAdminProof).not.toHaveBeenCalled();
  });

  it('makes a failed Bilibili proof request retryable instead of leaving session recovery pending', async () => {
    const client = api({ beginAdminProof: vi.fn(async () => { throw new HostedAPIError('temporarily_unavailable', 503); }) });
    const states: string[] = [];
    const flow = createAdminLoginFlow(client, (state) => states.push(state.kind));
    await flow.start();
    expect(states.at(-1)).toBe('service-unavailable');
  });

  it('cancels its proof on disposal and compensates a late completed login', async () => {
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    const client = api({ pollAdminProof: vi.fn(async () => ({ status: 'verified', expiresAt: challenge.expiresAt })), adminLogin: vi.fn(async () => pending) });
    const flow = createAdminLoginFlow(client, vi.fn());
    await flow.start(); await flow.poll();
    const submitting = flow.submit('123456');
    const disposing = flow.dispose();
    finish(); await Promise.all([submitting, disposing]);
    expect(client.cancelAdminProof).toHaveBeenCalledWith('admin-proof');
    expect(client.logout).toHaveBeenCalledTimes(1);
  });
});

describe('progressive administrator login view', () => {
  it('offers the validated mobile action and removes QR before the six-cell TOTP step', async () => {
    class Element {
      children: Element[] = []; textContent = ''; className = ''; type = ''; value = ''; disabled = false; inputMode = ''; autocomplete = ''; src = ''; alt = ''; href = ''; target = ''; rel = '';
      attributes = new Map<string, string>(); listeners = new Map<string, () => void>();
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element; activeElement?: Element }) {}
      append(...nodes: Element[]) { this.children.push(...nodes); }
      replaceChildren(...nodes: Element[]) { this.children = nodes; }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); }
      addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
      removeEventListener(name: string) { this.listeners.delete(name); }
      focus() { this.ownerDocument.activeElement = this; }
    }
    const document = { createElement: (tag: string): Element => new Element(tag, document), activeElement: undefined as Element | undefined };
    const root = new Element('div', document);
    const client = api({ pollAdminProof: vi.fn(async () => ({ status: 'verified', expiresAt: challenge.expiresAt })) });
    const mounted = mountAdminLogin(root as unknown as HTMLElement, client, { onSignedIn: vi.fn() });
    await vi.waitFor(() => expect(root.children[0]?.children.some((child) => child.tagName === 'img')).toBe(true));
    const panel = root.children[0]; const link = panel.children.find((child) => child.tagName === 'a');
    expect(link).toMatchObject({ href: challenge.verificationUrl, target: '_blank', rel: 'noopener noreferrer' });
    panel.children.find((child) => child.tagName === 'button' && child.textContent === '我已完成验证')?.listeners.get('click')?.();
    await vi.waitFor(() => expect(root.children[0]?.children.some((child) => child.className === 'hosted-code-control')).toBe(true));
    expect(root.children[0].children.some((child) => child.tagName === 'img')).toBe(false);
    expect(document.activeElement?.attributes.get('aria-label')).toBe('六位动态验证码');
    await mounted.dispose();
  });
});
