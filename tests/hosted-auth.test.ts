import { describe, expect, it, vi } from 'vitest';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { createAuthFlow, mountAuthView } from '../src/hosted/auth';
import { createAdminAccountFlow, createAdminFlow, createAdminOneTimeSecretFlow, mountAdminView } from '../src/hosted/admin';
import { createHostedViewHost } from '../src/hosted/shell';

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

  it('accepts the real verified poll envelope with its expiry and rejects status-inconsistent account mutations', async () => {
    const expiresAt = '2030-01-01T00:00:00Z';
    let responseBody: unknown = { status: 'verified', expiresAt };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json(responseBody));
    await expect(api.pollLogin('challenge')).resolves.toEqual({ status: 'verified', expiresAt });
    responseBody = { accountId: 7, status: 'active' };
    await expect(api.disableAccount(7, 'security')).rejects.toMatchObject({ code: 'invalid_response' });
    responseBody = { accountId: 7, status: 'disabled' };
    await expect(api.enableAccount(7, 'appeal')).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('accepts a disabled account returned by a committed rebind', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ accountId: 7, status: 'disabled' }));
    await expect(api.rebindAccount(7, 'fresh-proof', 'security hold')).resolves.toEqual({ accountId: 7, status: 'disabled' });
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
      pollLogin: vi.fn(async () => ({ status: 'pending' as const, expiresAt: '2030-01-01T00:00:00Z' })), createSession: vi.fn(), cancelLogin: cancel, logout: vi.fn(async () => undefined),
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
      pollLogin: vi.fn().mockResolvedValueOnce({ status: 'pending', expiresAt: '2030-01-01T00:00:00Z' }).mockResolvedValueOnce({ status: 'verified', expiresAt: '2030-01-01T00:00:00Z' }),
      createSession: vi.fn(async () => undefined), cancelLogin: cancel, logout: vi.fn(async () => undefined),
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
      pollLogin: vi.fn(async () => ({ status: 'registration_required' as const, registrationIntent: 'one-shot-intent', expiresAt: '2030-01-01T00:00:00Z' })),
      createSession: vi.fn(), cancelLogin: vi.fn(async () => undefined), logout: vi.fn(async () => undefined),
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
      pollLogin: vi.fn(async () => ({ status: 'expired' as const })), createSession: vi.fn(), cancelLogin: cancel, logout: vi.fn(async () => undefined),
    };
    const statuses: string[] = [];
    const flow = createAuthFlow(api, { onStatus: (status) => statuses.push(status), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    await flow.start();
    await flow.poll();
    await flow.dispose();
    await flow.dispose();
    expect(statuses).toEqual(['pending', 'expired']);
    expect(cancel).not.toHaveBeenCalled();

    const active = createAuthFlow({ ...api, pollLogin: vi.fn(async () => ({ status: 'pending' as const, expiresAt: '2030-01-01T00:00:00Z' })) }, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    await active.start();
    await active.dispose();
    await active.dispose();
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it('cancels a challenge created while the view is unmounting', async () => {
    let release!: (challenge: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const begin = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { release = resolve; });
    const cancel = vi.fn(async () => undefined);
    const flow = createAuthFlow({ beginLogin: () => begin, pollLogin: vi.fn(), createSession: vi.fn(), cancelLogin: cancel, logout: vi.fn(async () => undefined) }, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
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
      pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-01T00:00:00Z' })),
      createSession: vi.fn(async () => { throw new Error('network unavailable'); }), cancelLogin: cancel, logout: vi.fn(async () => undefined),
    }, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    await flow.start(); await expect(flow.poll()).rejects.toThrow('network unavailable'); await flow.dispose();
    expect(cancel).toHaveBeenCalledWith('verified-but-not-consumed');
  });

  it('logs out a site session that finishes after unmount and never announces sign-in', async () => {
    let finishSession!: () => void;
    const createSession = new Promise<void>((resolve) => { finishSession = resolve; });
    const createSessionCall = vi.fn(() => createSession);
    const logout = vi.fn(async () => undefined); const signedIn = vi.fn();
    const flow = createAuthFlow({
      beginLogin: vi.fn(async () => ({ challengeId: 'late-session', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-01T00:00:00Z' })),
      createSession: createSessionCall, cancelLogin: vi.fn(async () => undefined), logout,
    }, { onStatus: vi.fn(), onSignedIn: signedIn, onRegistrationRequired: vi.fn() });
    await flow.start(); const polling = flow.poll(); await vi.waitFor(() => expect(createSessionCall).toHaveBeenCalledTimes(1));
    const disposing = flow.dispose(); finishSession(); await polling; await disposing;
    expect(logout).toHaveBeenCalledTimes(1);
    expect(signedIn).not.toHaveBeenCalled();
  });

  it('finishes old session compensation before a replacement view can mount', async () => {
    const events: string[] = [];
    let finishCreate!: () => void; let finishLogout!: () => void;
    const createPending = new Promise<void>((resolve) => { finishCreate = resolve; });
    const logoutPending = new Promise<void>((resolve) => { finishLogout = resolve; });
    const logout = vi.fn(async () => { events.push('old-logout-start'); await logoutPending; events.push('old-logout-finish'); });
    const cancel = vi.fn(async () => undefined);
    const flow = createAuthFlow({
      beginLogin: vi.fn(async () => ({ challengeId: 'old-challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-01T00:00:00Z' })),
      createSession: vi.fn(async () => { events.push('old-create-start'); await createPending; events.push('old-create-finish'); }),
      cancelLogin: cancel, logout,
    }, { onStatus: vi.fn(), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });
    const host = createHostedViewHost();
    await host.replace(() => ({ dispose: flow.dispose })); await flow.start();
    const polling = flow.poll(); await vi.waitFor(() => expect(events).toContain('old-create-start'));
    const duplicatePoll = flow.poll(); await Promise.resolve();
    expect(events.filter((event) => event === 'old-create-start')).toHaveLength(1);
    const replacing = host.replace(() => { events.push('new-mount'); return { dispose: vi.fn() }; });
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledWith('old-challenge'));
    expect(events).not.toContain('new-mount');
    finishCreate(); await vi.waitFor(() => expect(logout).toHaveBeenCalledTimes(1));
    expect(events).not.toContain('new-mount');
    finishLogout(); await Promise.all([polling, duplicatePoll, replacing]);
    expect(events).toEqual(['old-create-start', 'old-create-finish', 'old-logout-start', 'old-logout-finish', 'new-mount']);
  });

  it('detaches normal session establishment before signed-in replaces the auth view', async () => {
    const host = createHostedViewHost(); let replacement: Promise<void> | undefined; let flow!: ReturnType<typeof createAuthFlow>;
    flow = createAuthFlow({
      beginLogin: vi.fn(async () => ({ challengeId: 'normal-challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-01T00:00:00Z' })),
      createSession: vi.fn(async () => undefined), cancelLogin: vi.fn(async () => undefined), logout: vi.fn(async () => undefined),
    }, { onStatus: vi.fn(), onRegistrationRequired: vi.fn(), onSignedIn: () => { replacement = host.replace(() => ({ dispose: vi.fn() })); } });
    await host.replace(() => ({ dispose: flow.dispose })); await flow.start(); await flow.poll();
    await expect(replacement).resolves.toBeUndefined();
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

  it('serializes one-time admin secrets and drops late results after close or dispose', async () => {
    let release!: (value: { title: string; copyLabel: string; value: string }) => void;
    const pending = new Promise<{ title: string; copyLabel: string; value: string }>((resolve) => { release = resolve; });
    const states: unknown[] = [];
    const flow = createAdminOneTimeSecretFlow((state) => states.push(structuredClone(state)));
    const load = vi.fn(() => pending);
    const first = flow.run(load); const duplicate = flow.run(load);
    expect(load).toHaveBeenCalledTimes(1);
    flow.dispose();
    release({ title: '一次性秘密', copyLabel: '复制', value: 'LATE-ADMIN-SECRET' });
    await first; await duplicate;
    expect(JSON.stringify(states)).not.toContain('LATE-ADMIN-SECRET');
  });

  it('wipes an old admin secret before replacing it and clears the replacement on close', async () => {
    const states: unknown[] = [];
    const flow = createAdminOneTimeSecretFlow((state) => states.push(structuredClone(state)));
    await flow.run(async () => ({ title: '旧秘密', copyLabel: '复制', value: 'OLD-ADMIN-SECRET' }));
    let release!: (value: { title: string; copyLabel: string; value: string }) => void;
    const pending = new Promise<{ title: string; copyLabel: string; value: string }>((resolve) => { release = resolve; });
    const replacing = flow.run(() => pending);
    expect(JSON.stringify(states.at(-1))).not.toContain('OLD-ADMIN-SECRET');
    release({ title: '新秘密', copyLabel: '复制', value: 'NEW-ADMIN-SECRET' }); await replacing;
    expect(JSON.stringify(states.at(-1))).toContain('NEW-ADMIN-SECRET');
    flow.close();
    expect(JSON.stringify(states.at(-1))).not.toContain('NEW-ADMIN-SECRET');
  });

  it('erases an open admin secret before an earlier proof rerenders the dashboard', async () => {
    class Element {
      children: Element[] = []; parent?: Element; textContent = ''; className = ''; id = ''; type = ''; value = ''; autocomplete = ''; inputMode = ''; src = ''; alt = ''; disabled = false; open = false;
      attributes = new Map<string, string>(); listeners = new Map<string, () => void>();
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element }) {}
      get firstElementChild() { return this.children[0]; }
      append(...nodes: Element[]) { for (const node of nodes) { node.parent = this; this.children.push(node); } }
      replaceChildren(...nodes: Element[]) { for (const child of this.children) child.parent = undefined; this.children = []; this.append(...nodes); }
      remove() { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); this.parent = undefined; }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); }
      addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
    }
    const document = { createElement: (tag: string): Element => new Element(tag, document) };
    const root = new Element('div', document) as unknown as HTMLElement;
    const findButton = (label: string): Element | undefined => {
      const visit = (node: Element): Element | undefined => node.tagName === 'button' && node.textContent === label
        ? node : node.children.map(visit).find(Boolean);
      return visit(root as unknown as Element);
    };
    let releaseRebind!: (challenge: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const pendingRebind = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { releaseRebind = resolve; });
    const beginAdminProof = vi.fn()
      .mockResolvedValueOnce({ challengeId: 'login-proof', qrImage: 'login-qr', expiresAt: '2030-01-01T00:00:00Z' })
      .mockImplementationOnce(() => pendingRebind);
    const api = {
      beginAdminProof, cancelAdminProof: vi.fn(async () => undefined), adminLogin: vi.fn(async () => undefined), verifyRecentTOTP: vi.fn(async () => undefined),
      disableAccount: vi.fn(), enableAccount: vi.fn(), adjustQuota: vi.fn(), rebindAccount: vi.fn(),
      generateInvitation: vi.fn(async () => ({ id: 8, codeHint: '****LAST', code: 'ONE-TIME-ADMIN-CODE', status: 'active' as const, createdAt: '2026-08-16T00:00:00Z', expiresAt: '2026-08-17T00:00:00Z' })),
      sendRecoveryArchive: vi.fn(), prepareRecovery: vi.fn(), confirmRecovery: vi.fn(),
    };
    const writeText = vi.fn(async () => undefined);
    const previousNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
    Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { clipboard: { writeText } } });
    try {
      const mounted = mountAdminView(root, api as unknown as HostedAPI);
      findButton('创建 B 站验证二维码')?.listeners.get('click')?.(); await vi.waitFor(() => expect(beginAdminProof).toHaveBeenCalledTimes(1));
      findButton('登录管理员控制台')?.listeners.get('click')?.(); await vi.waitFor(() => expect(findButton('创建新的 B 站身份验证')).toBeDefined());
      findButton('创建新的 B 站身份验证')?.listeners.get('click')?.(); await vi.waitFor(() => expect(beginAdminProof).toHaveBeenCalledTimes(2));
      findButton('生成不限额度邀请码')?.listeners.get('click')?.(); await vi.waitFor(() => expect(findButton('复制邀请码')).toBeDefined());
      const staleCopy = findButton('复制邀请码');
      releaseRebind({ challengeId: 'rebind-proof', qrImage: 'rebind-qr', expiresAt: '2030-01-01T00:00:00Z' });
      await vi.waitFor(() => expect(findButton('复制邀请码')).toBeUndefined());
      staleCopy?.listeners.get('click')?.(); await Promise.resolve();
      expect(writeText).not.toHaveBeenCalled();
      await mounted.dispose();
    } finally {
      if (previousNavigator) Object.defineProperty(globalThis, 'navigator', previousNavigator);
      else Reflect.deleteProperty(globalThis, 'navigator');
    }
  });
});
