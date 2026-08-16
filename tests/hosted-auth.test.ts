import { describe, expect, it, vi } from 'vitest';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { createAuthFlow, mountAuthView } from '../src/hosted/auth';
import { createAdminAccountFlow, createAdminFlow } from '../src/hosted/admin';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

describe('HostedAPI authentication contract', () => {
  it('loads runtime bootstrap and supplies same-origin credentials and CSRF on mutations', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'runtime-csrf' });
      return new Response(null, { status: 204 });
    });

    await api.logout();

    expect(requests).toEqual([
      ['/api/bootstrap', { credentials: 'same-origin', headers: { Accept: 'application/json' }, method: 'GET' }],
      ['/api/auth/session', expect.objectContaining({
        credentials: 'same-origin', method: 'DELETE', headers: expect.objectContaining({ 'X-CSRF-Token': 'runtime-csrf' }),
      })],
    ]);
  });

  it('rejects non-JSON and shape-invalid responses without putting body data in errors or console', async () => {
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : new Response('private-uid-123', { status: 503, headers: { 'Content-Type': 'text/plain' } }));

    await expect(api.beginLogin()).rejects.toMatchObject({ code: 'invalid_response' });
    try {
      await api.beginLogin();
    } catch (error) {
      expect(String(error)).not.toContain('private-uid-123');
    }
    expect(consoleSpy).not.toHaveBeenCalled();
    consoleSpy.mockRestore();
  });

  it('rejects success payloads containing unexpected identity fields', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ challengeId: 'challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z', uid: 'must-not-cross' }, 201));
    await expect(api.beginLogin()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('maps stable JSON error codes without retaining the response payload', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ error: 'recent_totp_required' }, 403));
    await expect(api.disableAccount(7, 'security review')).rejects.toEqual(
      new HostedAPIError('recent_totp_required', 403),
    );
  });

  it('treats HTTP 202 verification_pending as an error state rather than login success', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ error: 'verification_pending' }, 202));
    await expect(api.adminLogin('challenge', '123456')).rejects.toMatchObject({ code: 'verification_pending', status: 202 });
  });

  it('requires exact 204 void success and exact 410 expiry envelopes', async () => {
    let mode: 'void' | 'expired' = 'void';
    const api = await HostedAPI.connect(async (input) => {
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      return mode === 'void' ? json({ uid: 'must-not-cross' }, 200) : json({ uid: 'must-not-cross' }, 410);
    });
    await expect(api.createSession('challenge')).rejects.toMatchObject({ code: 'invalid_response' });
    mode = 'expired';
    await expect(api.pollLogin('challenge')).rejects.toMatchObject({ code: 'invalid_response' });
  });
});

describe('Bilibili authentication lifecycle', () => {
  it('mounts an accessible QR view and cancellation removes the challenge secret from DOM', async () => {
    class Element {
      children: Element[] = []; textContent = ''; className = ''; id = ''; type = ''; src = ''; alt = '';
      attributes = new Map<string, string>(); listeners = new Map<string, () => void>();
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element }) {}
      append(...nodes: Element[]) { this.children.push(...nodes); }
      replaceChildren(...nodes: Element[]) { this.children = nodes; }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); }
      removeAttribute(name: string) { this.attributes.delete(name); if (name === 'src') this.src = ''; }
      addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
    }
    const document = { createElement: (tag: string): Element => new Element(tag, document) };
    const root = new Element('div', document) as unknown as HTMLElement;
    const cancel = vi.fn(async () => undefined);
    const onExit = vi.fn();
    const mounted = mountAuthView(root, {
      beginLogin: vi.fn(async () => ({ challengeId: 'secret-challenge', qrImage: 'https://qr.invalid/secret', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'pending' as const })), createSession: vi.fn(), cancelLogin: cancel,
    }, { onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(), onExit }, { setInterval: () => 1, clearInterval: vi.fn() });
    await mounted.ready;
    expect(JSON.stringify(root)).toContain('https://qr.invalid/secret');
    const panel = (root as unknown as Element).children[0];
    panel.children.find((child) => child.tagName === 'button')?.listeners.get('click')?.();
    await vi.waitFor(() => expect(onExit).toHaveBeenCalledTimes(1));
    expect(cancel).toHaveBeenCalledWith('secret-challenge');
    expect(JSON.stringify(root)).not.toContain('secret-challenge');
    expect(JSON.stringify(root)).not.toContain('qr.invalid/secret');
  });

  it('polls pending then creates a site session and forgets the terminal challenge', async () => {
    const cancel = vi.fn(async () => undefined);
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'challenge', qrImage: 'https://qr.invalid/x', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn().mockResolvedValueOnce({ status: 'pending' }).mockResolvedValueOnce({ status: 'verified' }),
      createSession: vi.fn(async () => undefined), cancelLogin: cancel,
    };
    const statuses: string[] = [];
    const flow = createAuthFlow(api, { onStatus: (status) => statuses.push(status), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });

    await flow.start();
    await flow.poll();
    await flow.poll();
    await flow.dispose();

    expect(statuses).toEqual(['pending', 'pending', 'verified']);
    expect(api.createSession).toHaveBeenCalledWith('challenge');
    expect(cancel).not.toHaveBeenCalled();
  });

  it('passes a one-shot registration intent only through the registration callback', async () => {
    const registration = vi.fn();
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'registration_required' as const, registrationIntent: 'one-shot-intent' })),
      createSession: vi.fn(), cancelLogin: vi.fn(async () => undefined),
    };
    const flow = createAuthFlow(api, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: registration });
    await flow.start();
    await flow.poll();
    await flow.dispose();
    expect(registration).toHaveBeenCalledWith('one-shot-intent');
    expect(JSON.stringify(flow)).not.toContain('one-shot-intent');
  });

  it('reports expiry and idempotently cancels an active challenge on unmount', async () => {
    const cancel = vi.fn(async () => undefined);
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'expired' as const })), createSession: vi.fn(), cancelLogin: cancel,
    };
    const statuses: string[] = [];
    const flow = createAuthFlow(api, { onStatus: (status) => statuses.push(status), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    await flow.start();
    await flow.poll();
    await flow.dispose();
    await flow.dispose();
    expect(statuses).toEqual(['pending', 'expired']);
    expect(cancel).not.toHaveBeenCalled();

    const active = createAuthFlow({ ...api, pollLogin: vi.fn(async () => ({ status: 'pending' as const })) }, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    await active.start();
    await active.dispose();
    await active.dispose();
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it('cancels a challenge created while the view is unmounting', async () => {
    let release!: (challenge: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const begin = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { release = resolve; });
    const cancel = vi.fn(async () => undefined);
    const flow = createAuthFlow({ beginLogin: () => begin, pollLogin: vi.fn(), createSession: vi.fn(), cancelLogin: cancel }, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    const starting = flow.start();
    const disposing = flow.dispose();
    release({ challengeId: 'late-challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' });
    await starting; await disposing;
    expect(cancel).toHaveBeenCalledWith('late-challenge');
  });

  it('keeps a verified challenge cancellable when site-session creation fails', async () => {
    const cancel = vi.fn(async () => undefined);
    const flow = createAuthFlow({
      beginLogin: vi.fn(async () => ({ challengeId: 'verified-but-not-consumed', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'verified' as const })),
      createSession: vi.fn(async () => { throw new Error('network unavailable'); }), cancelLogin: cancel,
    }, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    await flow.start(); await expect(flow.poll()).rejects.toThrow('network unavailable'); await flow.dispose();
    expect(cancel).toHaveBeenCalledWith('verified-but-not-consumed');
  });
});

describe('administrator flow', () => {
  it('supports Bilibili proof plus TOTP and recent-TOTP retry', async () => {
    const api = { beginAdminProof: vi.fn(async () => ({ challengeId: 'proof', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })), adminLogin: vi.fn(async () => undefined), verifyRecentTOTP: vi.fn(async () => undefined), cancelAdminProof: vi.fn(async () => undefined) };
    const flow = createAdminFlow(api);
    await flow.beginProof();
    await flow.login('123456');
    let attempts = 0;
    await flow.runWithRecentTOTP('654321', async () => {
      attempts += 1;
      if (attempts === 1) throw new HostedAPIError('recent_totp_required', 403);
    });
    expect(api.adminLogin).toHaveBeenCalledWith('proof', '123456');
    expect(api.verifyRecentTOTP).toHaveBeenCalledWith('654321');
    expect(attempts).toBe(2);
  });

  it('drives disable, enable, quota, and fresh-proof reasoned rebind without caller UID', async () => {
    const api = {
      disableAccount: vi.fn(async () => ({ accountId: 7, status: 'disabled' as const })),
      enableAccount: vi.fn(async () => ({ accountId: 7, status: 'active' as const })),
      adjustQuota: vi.fn(async () => undefined),
      beginAdminProof: vi.fn(async () => ({ challengeId: 'fresh-proof', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      cancelAdminProof: vi.fn(async () => undefined),
      rebindAccount: vi.fn(async () => ({ accountId: 7, status: 'active' as const })),
    };
    const flow = createAdminAccountFlow(api);
    await flow.disable(7, 'security'); await flow.enable(7, 'appeal'); await flow.adjustQuota(7, 3, 'pilot');
    await flow.beginRebind(); await flow.rebind(7, 'verified ownership');
    expect(api.disableAccount).toHaveBeenCalledWith(7, 'security');
    expect(api.enableAccount).toHaveBeenCalledWith(7, 'appeal');
    expect(api.adjustQuota).toHaveBeenCalledWith(7, 3, 'pilot');
    expect(api.rebindAccount).toHaveBeenCalledWith(7, 'fresh-proof', 'verified ownership');
    expect(JSON.stringify(api.rebindAccount.mock.calls)).not.toContain('uid');
  });

  it('forgets the previous administrator proof before replacing it', async () => {
    const cancel = vi.fn(async () => undefined);
    const api = {
      beginAdminProof: vi.fn().mockResolvedValueOnce({ challengeId: 'old', qrImage: 'old-qr', expiresAt: '2030-01-01T00:00:00Z' }).mockResolvedValueOnce({ challengeId: 'fresh', qrImage: 'fresh-qr', expiresAt: '2030-01-01T00:00:00Z' }),
      adminLogin: vi.fn(), verifyRecentTOTP: vi.fn(), cancelAdminProof: cancel,
    };
    const flow = createAdminFlow(api);
    await flow.beginProof(); await flow.beginProof(); await flow.dispose();
    expect(cancel.mock.calls).toEqual([['old'], ['fresh']]);
  });
});
