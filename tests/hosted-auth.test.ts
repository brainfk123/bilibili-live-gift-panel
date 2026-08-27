import { describe, expect, it, vi } from 'vitest';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { createAuthController, createAuthFlow, mountAuthView } from '../src/hosted/auth';
import { createBiliChallengePoller, type BiliChallengeTimerPort } from '../src/hosted/bili-challenge-poller';
import { createAdminRecoveryFlow } from '../src/hosted/admin';
import { createHostedViewHost } from '../src/hosted/shell';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

class AuthControlledTimers implements BiliChallengeTimerPort {
  private clock = 0;
  private nextID = 1;
  private readonly scheduled = new Map<number, { callback: () => void; dueAt: number }>();

  setTimeout(callback: () => void, milliseconds: number): number {
    const id = this.nextID++;
    this.scheduled.set(id, { callback, dueAt: this.clock + milliseconds });
    return id;
  }

  clearTimeout(id: number): void { this.scheduled.delete(id); }
  now(): number { return this.clock; }
  count(): number { return this.scheduled.size; }
  advance(milliseconds: number): void { this.clock += milliseconds; }

  async fireNext(): Promise<void> {
    const next = [...this.scheduled.entries()].sort((left, right) => left[1].dueAt - right[1].dueAt)[0];
    if (!next) throw new Error('No controlled timer is scheduled.');
    const [id, task] = next;
    this.scheduled.delete(id);
    this.clock = task.dueAt;
    task.callback();
    for (let turn = 0; turn < 5; turn++) await Promise.resolve();
  }
}

class AuthElement {
  children: AuthElement[] = [];
  textContent = '';
  className = '';
  id = '';
  type = '';
  src = '';
  alt = '';
  href = '';
  hidden = false;
  disabled = false;
  dataset: Record<string, string> = {};
  parent?: AuthElement;
  readonly attributes = new Map<string, string>();
  readonly listeners = new Map<string, Array<() => void>>();

  constructor(readonly tagName: string, readonly ownerDocument: AuthDocument) {}

  append(...nodes: AuthElement[]): void {
    for (const node of nodes) {
      node.parent = this;
      this.children.push(node);
    }
  }

  replaceChildren(...nodes: AuthElement[]): void {
    for (const child of this.children) child.parent = undefined;
    this.children = [];
    this.append(...nodes);
  }

  setAttribute(name: string, value: string): void {
    this.attributes.set(name, value);
  }

  getAttribute(name: string): string | null {
    return this.attributes.get(name) ?? null;
  }

  removeAttribute(name: string): void {
    this.attributes.delete(name);
    if (name === 'src') this.src = '';
    if (name === 'href') this.href = '';
  }

  remove(): void {
    if (!this.parent) return;
    this.parent.children = this.parent.children.filter((child) => child !== this);
    this.parent = undefined;
  }

  addEventListener(name: string, listener: () => void): void {
    this.listeners.set(name, [...(this.listeners.get(name) ?? []), listener]);
  }

  click(): void {
    if (this.disabled) return;
    for (const listener of this.listeners.get('click') ?? []) listener();
  }
}

interface AuthDocument {
  createElement(tag: string): AuthElement;
}

function authDOM(): { document: AuthDocument; root: AuthElement } {
  const document = { createElement: (tag: string): AuthElement => new AuthElement(tag, document) };
  return { document, root: new AuthElement('div', document) };
}

function descendants(root: AuthElement): AuthElement[] {
  return [root, ...root.children.flatMap(descendants)];
}

function text(root: AuthElement): string {
  return descendants(root).map((element) => element.textContent).join(' ');
}

function hasClass(root: AuthElement, name: string): boolean {
  return descendants(root).some((element) => element.className.split(/\s+/).includes(name));
}

function status(root: AuthElement): AuthElement {
  const match = descendants(root).find((element) => element.getAttribute('role') === 'status');
  if (!match) throw new Error('Missing login status region.');
  return match;
}

function link(root: AuthElement, label: string): AuthElement {
  const match = descendants(root).find((element) => element.tagName === 'a' && text(element).includes(label));
  if (!match) throw new Error(`Missing link: ${label}`);
  return match;
}

function button(root: AuthElement, label: string): AuthElement {
  const match = descendants(root).find((element) => element.tagName === 'button' && text(element).includes(label));
  if (!match) throw new Error(`Missing button: ${label}`);
  return match;
}

function image(root: AuthElement): AuthElement {
  const match = descendants(root).find((element) => element.tagName === 'img');
  if (!match) throw new Error('Missing QR image.');
  return match;
}

function challenge(number = 1) {
  return {
    challengeId: `secret-challenge-${number}`,
    qrImage: `https://qr.invalid/secret-${number}`,
    verificationUrl: `https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=public-key-${number}`,
    expiresAt: '2030-01-01T00:00:00Z',
  };
}

function authAPI(overrides: Partial<HostedAPI> = {}) {
  return {
    beginLogin: vi.fn(async () => challenge()),
    pollLogin: vi.fn(async () => ({ status: 'pending' as const, expiresAt: challenge().expiresAt })),
    createSession: vi.fn(async () => undefined),
    cancelLogin: vi.fn(async () => undefined),
    logout: vi.fn(async () => undefined),
    ...overrides,
  };
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

  it('accepts only an allowlisted Bilibili mobile verification URL', async () => {
    const verificationUrl = 'https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=public-key';
    const connect = async (url: string) => HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ challengeId: 'challenge', qrImage: 'qr', verificationUrl: url, expiresAt: '2030-01-01T00:00:00Z' }, 201));
    await expect((await connect(verificationUrl)).beginLogin()).resolves.toEqual({
      challengeId: 'challenge', qrImage: 'qr', verificationUrl, expiresAt: '2030-01-01T00:00:00Z',
    });
    const currentMobileUrl = 'https://account.bilibili.com/h5/account-h5/auth/scan-web?navhide=1&callback=close&from=&qrcode_key=public-key';
    await expect((await connect(currentMobileUrl)).beginLogin()).resolves.toMatchObject({ verificationUrl: currentMobileUrl });
    for (const invalid of [
      'http://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key',
      'https://user@passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key',
      'https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key#fragment',
      'https://example.test/h5-app/passport/login/scan?qrcode_key=key',
      'https://passport.bilibili.com/other?qrcode_key=key',
      'https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=one&qrcode_key=two',
      'https://account.bilibili.com/h5/account-h5/auth/scan-web?navhide=1&callback=other&qrcode_key=key',
      'https://account.bilibili.com/h5/account-h5/auth/scan-web?navhide=1&callback=close&from=other&qrcode_key=key',
    ]) await expect((await connect(invalid)).beginLogin()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('maps stable JSON error codes without retaining the response payload', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ error: 'recent_totp_required' }, 403));
    await expect(api.disableAccount(7, 'security review')).rejects.toEqual(
      new HostedAPIError('recent_totp_required', 403),
    );
  });

  it('checks an existing administrator session without a request body', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      return input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : new Response(null, { status: 204 });
    });
    await api.adminSession();
    expect(requests[1]).toEqual(['/api/admin/session', {
      credentials: 'same-origin', headers: { Accept: 'application/json' }, method: 'GET',
    }]);
  });

  it('logs out only the administrator session through its exact DELETE route', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      return input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : new Response(null, { status: 204 });
    });
    await api.adminLogout();
    expect(requests[1]).toEqual(['/api/admin/session', {
      credentials: 'same-origin', headers: { Accept: 'application/json', 'X-CSRF-Token': 'csrf' }, method: 'DELETE',
    }]);
    expect(requests.map(([input]) => input)).not.toContain('/api/auth/session');
  });

  it('uses an opaque email challenge and submits only its six email digits', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      if (input === '/api/admin/auth/email/challenges') return json({ challengeId: 'email-proof', expiresAt: '2030-01-01T00:00:00Z' }, 201);
      return new Response(null, { status: 204 });
    });
    await expect(api.beginAdminEmailLogin()).resolves.toEqual({ challengeId: 'email-proof', expiresAt: '2030-01-01T00:00:00Z' });
    await api.adminEmailLogin('email-proof', '654321');
    expect(requests[1]?.[0]).toBe('/api/admin/auth/email/challenges');
    expect(requests[2]).toEqual(['/api/admin/session/email', { credentials: 'same-origin', headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf' }, method: 'POST', body: '{"challengeId":"email-proof","emailCode":"654321"}' }]);
  });

  it('prepares administrator recovery with only the recovery code', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      return json({ totpUri: 'otpauth://totp/panel?secret=NEWSECRET', recoveryPassword: '12345678901234567890', handoffToken: 'opaque-handoff' });
    });
    await api.prepareRecovery('old-recovery-code');
    expect(requests[1]).toEqual(['/api/admin/recovery/prepare', {
      credentials: 'same-origin', headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf' }, method: 'POST', body: '{"recoveryCode":"old-recovery-code"}',
    }]);
  });

  it('does not retain administrator Bilibili login methods while preserving service replacement', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : new Response(null, { status: 204 }));
    expect(api).not.toHaveProperty('beginAdminProof');
    expect(api).not.toHaveProperty('pollAdminProof');
    expect(api).not.toHaveProperty('cancelAdminProof');
    expect(api).not.toHaveProperty('adminLogin');
    expect(api).toHaveProperty('beginBiliServiceChallenge');
    expect(api).toHaveProperty('replaceBiliServiceCredential');
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

  it('accepts only the exact scanned poll envelope', async () => {
    const expiresAt = '2030-01-01T00:00:00Z';
    let responseBody: unknown = { status: 'scanned', expiresAt };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json(responseBody));

    await expect(api.pollLogin('challenge')).resolves.toEqual({ status: 'scanned', expiresAt });
    for (const invalid of [
      { status: 'scanned' },
      { status: 'scanned', expiresAt, uid: 'must-not-cross' },
      { status: 'scanned', expiresAt, cookie: 'must-not-cross' },
      { status: 'scanned', expiresAt, qrcode_key: 'must-not-cross' },
      { status: 'scanned', expiresAt, rawPayload: 'must-not-cross' },
      { status: 'scanned', expiresAt, challengeId: 'must-not-cross' },
      { status: 'scanned', expiresAt, unexpected: 'must-not-cross' },
    ]) {
      responseBody = invalid;
      await expect(api.pollLogin('challenge')).rejects.toMatchObject({ code: 'invalid_response' });
    }
  });

});

describe('Bilibili authentication lifecycle', () => {
  it('login view renders the compact two-column structure and only the allowlisted same-device URL', async () => {
    const { root } = authDOM();
    const api = authAPI();
    const mounted = mountAuthView(root as unknown as HTMLElement, api, {
      onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(), onExit: vi.fn(),
    }, new AuthControlledTimers());

    await mounted.ready;

    expect(text(root)).toContain('使用 B 站账号登录');
    expect(status(root).textContent).toBe('请使用 B 站客户端扫码');
    expect(hasClass(root, 'hosted-auth-page')).toBe(true);
    expect(hasClass(root, 'hosted-auth-card')).toBe(true);
    expect(hasClass(root, 'hosted-auth-copy')).toBe(true);
    expect(hasClass(root, 'hosted-auth-qr-column')).toBe(true);
    expect(hasClass(root, 'hosted-auth-status')).toBe(true);
    expect(hasClass(root, 'hosted-auth-actions')).toBe(true);
    expect(hasClass(root, 'hosted-auth-mobile-link')).toBe(true);
    expect(link(root, '在本机打开 B 站确认').href).toBe(challenge().verificationUrl);
    expect(image(root).src).toBe(challenge().qrImage);

    await mounted.dispose();
  });

  it('login view exposes an observable busy creating state with a fixed QR placeholder', async () => {
    let release!: (value: ReturnType<typeof challenge>) => void;
    const creating = new Promise<ReturnType<typeof challenge>>((resolve) => { release = resolve; });
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({ beginLogin: vi.fn(() => creating) }), {
      onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(),
    }, new AuthControlledTimers());

    expect(status(root).textContent).toBe('正在创建二维码');
    expect(status(root).dataset.kind).toBe('creating');
    expect(text(root)).toContain('正在创建');
    expect(image(root).src).toBe('');
    expect(image(root).hidden).toBe(true);
    expect(button(root, '正在创建').disabled).toBe(true);
    expect(button(root, '正在创建').getAttribute('aria-busy')).toBe('true');

    release(challenge());
    await mounted.ready;
    await mounted.dispose();
  });

  it('login view keeps a failed creation idle until the user explicitly tries again', async () => {
    const beginLogin = vi.fn()
      .mockRejectedValueOnce(new Error('service unavailable'))
      .mockResolvedValueOnce(challenge(2));
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({ beginLogin }), {
      onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(),
    }, new AuthControlledTimers());

    await mounted.ready;

    expect(status(root).textContent).toBe('无法创建二维码，请再次尝试');
    expect(status(root).dataset.kind).toBe('error');
    expect(image(root).src).toBe('');
    expect(link(root, '在本机打开 B 站确认').href).toBe('');
    expect(button(root, '再次尝试').disabled).toBe(false);
    expect(beginLogin).toHaveBeenCalledTimes(1);

    button(root, '再次尝试').click();
    await vi.waitFor(() => expect(status(root).textContent).toBe('请使用 B 站客户端扫码'));
    expect(beginLogin).toHaveBeenCalledTimes(2);
    expect(image(root).src).toBe(challenge(2).qrImage);
    await mounted.dispose();
  });

  it('login view preserves the QR and link after scan while showing success state', async () => {
    const timers = new AuthControlledTimers();
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({
      pollLogin: vi.fn(async () => ({ status: 'scanned' as const, expiresAt: challenge().expiresAt })),
    }), { onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() }, timers);

    await mounted.ready;
    await timers.fireNext();

    expect(status(root).textContent).toBe('已扫码，请在手机确认');
    expect(status(root).dataset.kind).toBe('success');
    expect(image(root).src).toBe(challenge().qrImage);
    expect(link(root, '在本机打开 B 站确认').href).toBe(challenge().verificationUrl);
    await mounted.dispose();
  });

  it.each([
    {
      name: 'network',
      error: new Error('offline'),
      message: '网络暂不可用，2 秒后自动重试',
    },
    {
      name: 'rate-limited',
      error: new HostedAPIError('rate_limited', 429),
      message: '请求较频繁，稍后自动重试',
    },
  ])('login view maps $name polling snapshots without discarding the active challenge', async ({ error, message }) => {
    const timers = new AuthControlledTimers();
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({
      pollLogin: vi.fn(async () => { throw error; }),
    }), { onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() }, timers);

    await mounted.ready;
    await timers.fireNext();

    expect(status(root).textContent).toBe(message);
    expect(status(root).dataset.kind).toBe('warning');
    expect(image(root).src).toBe(challenge().qrImage);
    expect(link(root, '在本机打开 B 站确认').href).toBe(challenge().verificationUrl);
    expect(button(root, '立即重试').disabled).toBe(true);
    await mounted.dispose();
  });

  it('login view delegates an enabled manual retry to the shared poller once', async () => {
    let rejectPoll!: (reason: Error) => void;
    const delayedFailure = new Promise<never>((_resolve, reject) => { rejectPoll = reject; });
    const pollLogin = vi.fn()
      .mockImplementationOnce(() => delayedFailure)
      .mockResolvedValueOnce({ status: 'pending', expiresAt: challenge().expiresAt });
    const timers = new AuthControlledTimers();
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({ pollLogin }), {
      onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(),
    }, timers);

    await mounted.ready;
    const polling = timers.fireNext();
    await vi.waitFor(() => expect(pollLogin).toHaveBeenCalledTimes(1));
    timers.advance(2_000);
    rejectPoll(new Error('offline'));
    await polling;

    await vi.waitFor(() => expect(status(root).textContent).toBe('网络暂不可用，2 秒后自动重试'));
    expect(button(root, '立即重试').disabled).toBe(false);
    button(root, '立即重试').click();
    await vi.waitFor(() => expect(pollLogin).toHaveBeenCalledTimes(2));
    expect(timers.count()).toBe(1);
    await mounted.dispose();
  });

  it('login view expires in place, removes old QR data, and regenerates only after explicit action', async () => {
    let releaseReplacement!: (value: ReturnType<typeof challenge>) => void;
    const replacement = new Promise<ReturnType<typeof challenge>>((resolve) => { releaseReplacement = resolve; });
    const beginLogin = vi.fn()
      .mockResolvedValueOnce(challenge())
      .mockImplementationOnce(() => replacement);
    const timers = new AuthControlledTimers();
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({
      beginLogin,
      pollLogin: vi.fn(async () => ({ status: 'expired' as const })),
    }), { onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() }, timers);

    await mounted.ready;
    await timers.fireNext();

    expect(status(root).textContent).toBe('二维码已过期');
    expect(status(root).dataset.kind).toBe('expired');
    expect(image(root).src).toBe('');
    expect(image(root).hidden).toBe(true);
    expect(link(root, '在本机打开 B 站确认').href).toBe('');
    expect(text(root)).toContain('二维码已过期');
    expect(beginLogin).toHaveBeenCalledTimes(1);

    button(root, '重新生成').click();
    await vi.waitFor(() => expect(beginLogin).toHaveBeenCalledTimes(2));
    expect(status(root).textContent).toBe('正在创建二维码');
    expect(button(root, '正在创建').disabled).toBe(true);
    expect(button(root, '正在创建').getAttribute('aria-busy')).toBe('true');
    releaseReplacement(challenge(2));
    await vi.waitFor(() => expect(status(root).textContent).toBe('请使用 B 站客户端扫码'));
    expect(image(root).src).toBe(challenge(2).qrImage);
    expect(link(root, '在本机打开 B 站确认').href).toBe(challenge(2).verificationUrl);
    await mounted.dispose();
  });

  it('login view announces verification before handing off and removes terminal QR data', async () => {
    const timers = new AuthControlledTimers();
    const { root } = authDOM();
    let announced = '';
    const onSignedIn = vi.fn(() => { announced = status(root).textContent; });
    const api = authAPI({
      pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: challenge().expiresAt })),
    });
    const mounted = mountAuthView(root as unknown as HTMLElement, api, {
      onSignedIn, onRegistrationRequired: vi.fn(),
    }, timers);

    await mounted.ready;
    await timers.fireNext();

    expect(announced).toBe('验证成功');
    expect(status(root).dataset.kind).toBe('success');
    expect(image(root).src).toBe('');
    expect(link(root, '在本机打开 B 站确认').href).toBe('');
    expect(api.createSession).toHaveBeenCalledWith(challenge().challengeId);
    await mounted.dispose();
  });

  it('login view stops on a fatal poll response and waits for explicit regeneration', async () => {
    const beginLogin = vi.fn(async () => challenge());
    const timers = new AuthControlledTimers();
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({
      beginLogin,
      pollLogin: vi.fn(async () => { throw new HostedAPIError('invalid_response', 200); }),
    }), { onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() }, timers);

    await mounted.ready;
    await timers.fireNext();

    expect(status(root).textContent).toBe('登录响应无效，请重新生成二维码');
    expect(status(root).dataset.kind).toBe('error');
    expect(image(root).src).toBe('');
    expect(link(root, '在本机打开 B 站确认').href).toBe('');
    expect(button(root, '重新生成').disabled).toBe(false);
    expect(beginLogin).toHaveBeenCalledTimes(1);
    await mounted.dispose();
  });

  it('login view cancellation removes challenge data and exits exactly once', async () => {
    const { root } = authDOM();
    const cancelLogin = vi.fn(async () => undefined);
    const onExit = vi.fn();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({ cancelLogin }), {
      onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(), onExit,
    }, new AuthControlledTimers());
    await mounted.ready;

    button(root, '取消').click();

    await vi.waitFor(() => expect(onExit).toHaveBeenCalledTimes(1));
    expect(cancelLogin).toHaveBeenCalledWith(challenge().challengeId);
    expect(JSON.stringify(root)).not.toContain('secret-challenge');
    expect(JSON.stringify(root)).not.toContain('qr.invalid/secret');
  });

  it('removes QR secrets immediately and offers a safe retry when cancellation fails', async () => {
    let rejectCleanup!: (reason: Error) => void;
    const cleanupPending = new Promise<void>((_resolve, reject) => { rejectCleanup = reject; });
    const rawFailure = new Error('RAW DELETE secret-challenge-1 public-key-1');
    const cancelLogin = vi.fn()
      .mockImplementationOnce(() => cleanupPending)
      .mockResolvedValue(undefined);
    const onExit = vi.fn();
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({ cancelLogin }), {
      onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(), onExit,
    }, new AuthControlledTimers());
    await mounted.ready;

    button(root, '取消').click();

    expect(image(root).src).toBe('');
    expect(link(root, '在本机打开 B 站确认').href).toBe('');
    expect(onExit).not.toHaveBeenCalled();
    rejectCleanup(rawFailure);
    await vi.waitFor(() => expect(status(root).textContent).toBe('二维码清理失败，请重试返回'));

    expect(status(root).dataset.kind).toBe('error');
    expect(button(root, '重试返回').disabled).toBe(false);
    const rendered = descendants(root).flatMap((element) => [
      element.textContent,
      element.src,
      element.href,
      ...element.attributes.values(),
      ...Object.values(element.dataset),
    ]).join('|');
    expect(rendered).not.toContain(challenge().challengeId);
    expect(rendered).not.toContain(challenge().qrImage);
    expect(rendered).not.toContain(challenge().verificationUrl);
    expect(rendered).not.toContain(rawFailure.message);

    button(root, '重试返回').click();
    await vi.waitFor(() => expect(onExit).toHaveBeenCalledTimes(1));

    expect(cancelLogin).toHaveBeenCalledTimes(2);
    expect(cancelLogin).toHaveBeenNthCalledWith(1, challenge().challengeId);
    expect(cancelLogin).toHaveBeenNthCalledWith(2, challenge().challengeId);
    expect(root.children).toHaveLength(0);
  });

  it('rejects failed public disposal, clears local secrets in finally, and retries the retained challenge', async () => {
    const cancelLogin = vi.fn()
      .mockRejectedValueOnce(new Error('RAW PUBLIC DISPOSE secret-challenge-1'))
      .mockResolvedValue(undefined);
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({ cancelLogin }), {
      onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(), onExit: vi.fn(),
    }, new AuthControlledTimers());
    await mounted.ready;

    await expect(mounted.dispose()).rejects.toMatchObject({ code: 'operation_failed' });

    expect(root.children).toHaveLength(0);
    await mounted.dispose();

    expect(cancelLogin).toHaveBeenCalledTimes(2);
    expect(cancelLogin).toHaveBeenNthCalledWith(1, challenge().challengeId);
    expect(cancelLogin).toHaveBeenNthCalledWith(2, challenge().challengeId);
  });

  it('keeps an ordinary auth disposer reachable through the root host after cleanup failure', async () => {
    const cancelLogin = vi.fn()
      .mockRejectedValueOnce(new Error('RAW ROOT AUTH DISPOSE secret-challenge-1'))
      .mockResolvedValue(undefined);
    const { root } = authDOM();
    const host = createHostedViewHost();
    let mounted!: ReturnType<typeof mountAuthView>;
    await host.replace(() => {
      mounted = mountAuthView(root as unknown as HTMLElement, authAPI({ cancelLogin }), {
        onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(), onExit: vi.fn(),
      }, new AuthControlledTimers());
      return mounted;
    });
    await mounted.ready;

    await expect(host.dispose()).rejects.toMatchObject({ code: 'operation_failed' });
    await host.dispose();

    expect(cancelLogin).toHaveBeenCalledTimes(2);
    expect(cancelLogin).toHaveBeenNthCalledWith(1, challenge().challengeId);
    expect(cancelLogin).toHaveBeenNthCalledWith(2, challenge().challengeId);
    expect(root.children).toHaveLength(0);
  });

  it('joins concurrent disposal with late challenge cleanup without duplicate DELETE requests', async () => {
    let releaseBegin!: (value: ReturnType<typeof challenge>) => void;
    let releaseCleanup!: () => void;
    const creating = new Promise<ReturnType<typeof challenge>>((resolve) => { releaseBegin = resolve; });
    const cleanupPending = new Promise<void>((resolve) => { releaseCleanup = resolve; });
    const cancelLogin = vi.fn(() => cleanupPending);
    const beginLogin = vi.fn(() => creating);
    const { root } = authDOM();
    const mounted = mountAuthView(root as unknown as HTMLElement, authAPI({
      beginLogin,
      cancelLogin,
    }), { onSignedIn: vi.fn(), onRegistrationRequired: vi.fn(), onExit: vi.fn() }, new AuthControlledTimers());
    await vi.waitFor(() => expect(beginLogin).toHaveBeenCalledTimes(1));

    const first = mounted.dispose();
    let secondSettled = false;
    const second = mounted.dispose().finally(() => { secondSettled = true; });
    releaseBegin(challenge());
    await vi.waitFor(() => expect(cancelLogin).toHaveBeenCalledTimes(1));
    await Promise.resolve();

    expect(secondSettled).toBe(false);
    releaseCleanup();
    await Promise.all([mounted.ready, first, second]);

    expect(cancelLogin).toHaveBeenCalledTimes(1);
    expect(cancelLogin).toHaveBeenCalledWith(challenge().challengeId);
    expect(root.children).toHaveLength(0);
  });

  it('polls pending and scanned then creates a site session and forgets the terminal challenge', async () => {
    const cancel = vi.fn(async () => undefined);
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'challenge', qrImage: 'https://qr.invalid/x', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn()
        .mockResolvedValueOnce({ status: 'pending', expiresAt: '2030-01-01T00:00:00Z' })
        .mockResolvedValueOnce({ status: 'scanned', expiresAt: '2030-01-01T00:00:00Z' })
        .mockResolvedValueOnce({ status: 'verified', expiresAt: '2030-01-01T00:00:00Z' }),
      createSession: vi.fn(async () => undefined), cancelLogin: cancel, logout: vi.fn(async () => undefined),
    };
    const statuses: string[] = [];
    const flow = createAuthFlow(api, { onStatus: (status) => statuses.push(status), onSignedIn: vi.fn(), onRegistrationRequired: vi.fn() });

    await flow.start();
    await flow.poll();
    await flow.poll();
    await flow.poll();
    await flow.dispose();

    expect(statuses).toEqual(['pending', 'pending', 'scanned', 'verified']);
    expect(api.createSession).toHaveBeenCalledWith('challenge');
    expect(cancel).not.toHaveBeenCalled();
  });

  it('integrates ordinary authentication with the shared single-timer poller', async () => {
    const timers = new AuthControlledTimers();
    const statuses: string[] = [];
    const signedIn = vi.fn();
    const flow = createAuthFlow({
      beginLogin: vi.fn(async () => ({ challengeId: 'challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' })),
      pollLogin: vi.fn()
        .mockResolvedValueOnce({ status: 'scanned', expiresAt: '2030-01-01T00:00:00Z' })
        .mockResolvedValueOnce({ status: 'verified', expiresAt: '2030-01-01T00:00:00Z' }),
      createSession: vi.fn(async () => undefined),
      cancelLogin: vi.fn(async () => undefined),
      logout: vi.fn(async () => undefined),
    }, {
      onStatus: (status) => statuses.push(status),
      onSignedIn: signedIn,
      onRegistrationRequired: vi.fn(),
    });
    const controller = createAuthController(
      flow,
      (port, render) => createBiliChallengePoller(port, timers, render),
      vi.fn(),
    );

    await controller.start();
    expect(timers.count()).toBe(1);
    await timers.fireNext();
    expect(statuses).toEqual(['pending', 'scanned']);
    expect(timers.count()).toBe(1);
    await timers.fireNext();

    expect(statuses).toEqual(['pending', 'scanned', 'verified']);
    expect(signedIn).toHaveBeenCalledTimes(1);
    expect(timers.count()).toBe(0);
    await controller.dispose();
  });

  it('starts a fresh poller lifecycle when an expired login is regenerated', async () => {
    const timers = new AuthControlledTimers();
    const statuses: string[] = [];
    let challengeNumber = 0;
    const flow = createAuthFlow({
      beginLogin: vi.fn(async () => ({
        challengeId: `challenge-${++challengeNumber}`,
        qrImage: 'qr',
        expiresAt: '2030-01-01T00:00:00Z',
      })),
      pollLogin: vi.fn()
        .mockResolvedValueOnce({ status: 'expired' })
        .mockResolvedValueOnce({ status: 'scanned', expiresAt: '2030-01-01T00:00:00Z' }),
      createSession: vi.fn(async () => undefined),
      cancelLogin: vi.fn(async () => undefined),
      logout: vi.fn(async () => undefined),
    }, {
      onStatus: (status) => statuses.push(status),
      onSignedIn: vi.fn(),
      onRegistrationRequired: vi.fn(),
    });
    const controller = createAuthController(
      flow,
      (port, render) => createBiliChallengePoller(port, timers, render),
      vi.fn(),
    );

    await controller.start();
    await timers.fireNext();
    expect(timers.count()).toBe(0);

    await controller.start();
    expect(timers.count()).toBe(1);
    await timers.fireNext();

    expect(statuses).toEqual(['pending', 'expired', 'pending', 'scanned']);
    await controller.dispose();
  });

  it('cancels an active challenge before regenerating after a fatal poll failure', async () => {
    const timers = new AuthControlledTimers();
    const cancelLogin = vi.fn(async () => undefined);
    let challengeNumber = 0;
    const flow = createAuthFlow({
      beginLogin: vi.fn(async () => ({
        challengeId: `challenge-${++challengeNumber}`,
        qrImage: 'qr',
        expiresAt: '2030-01-01T00:00:00Z',
      })),
      pollLogin: vi.fn(async () => { throw new HostedAPIError('invalid_response', 200); }),
      createSession: vi.fn(async () => undefined),
      cancelLogin,
      logout: vi.fn(async () => undefined),
    }, {
      onStatus: vi.fn(),
      onSignedIn: vi.fn(),
      onRegistrationRequired: vi.fn(),
    });
    const controller = createAuthController(
      flow,
      (port, render) => createBiliChallengePoller(port, timers, render),
      vi.fn(),
    );

    await controller.start();
    await timers.fireNext();
    expect(timers.count()).toBe(0);

    await controller.start();

    expect(cancelLogin).toHaveBeenCalledTimes(1);
    expect(cancelLogin).toHaveBeenCalledWith('challenge-1');
    expect(timers.count()).toBe(1);
    await controller.dispose();
  });

  it('retries regeneration cleanup when dispose wins and the in-flight cancellation rejects', async () => {
    const timers = new AuthControlledTimers();
    let rejectCancellation!: (reason: Error) => void;
    const firstCancellation = new Promise<void>((_resolve, reject) => { rejectCancellation = reject; });
    const cancelLogin = vi.fn()
      .mockImplementationOnce(() => firstCancellation)
      .mockResolvedValue(undefined);
    const beginLogin = vi.fn(async () => ({
      challengeId: 'challenge-1',
      qrImage: 'qr',
      expiresAt: '2030-01-01T00:00:00Z',
    }));
    const flow = createAuthFlow({
      beginLogin,
      pollLogin: vi.fn(async () => { throw new HostedAPIError('invalid_response', 200); }),
      createSession: vi.fn(async () => undefined),
      cancelLogin,
      logout: vi.fn(async () => undefined),
    }, {
      onStatus: vi.fn(),
      onSignedIn: vi.fn(),
      onRegistrationRequired: vi.fn(),
    });
    const controller = createAuthController(
      flow,
      (port, render) => createBiliChallengePoller(port, timers, render),
      vi.fn(),
    );

    await controller.start();
    await timers.fireNext();
    const regenerating = controller.start();
    await vi.waitFor(() => expect(cancelLogin).toHaveBeenCalledTimes(1));
    const disposing = controller.dispose();
    rejectCancellation(new Error('first cancellation failed'));
    const completions = await Promise.allSettled([regenerating, disposing]);

    expect(cancelLogin).toHaveBeenCalledTimes(1);
    expect(cancelLogin).toHaveBeenNthCalledWith(1, 'challenge-1');
    expect(beginLogin).toHaveBeenCalledTimes(1);
    expect(completions.map((result) => result.status)).toEqual(['rejected', 'rejected']);
    expect(completions.every((result) => result.status === 'rejected'
      && result.reason instanceof HostedAPIError
      && result.reason.code === 'operation_failed')).toBe(true);

    await controller.dispose();

    expect(cancelLogin).toHaveBeenCalledTimes(2);
    expect(cancelLogin).toHaveBeenNthCalledWith(2, 'challenge-1');
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

describe('administrator recovery flow', () => {
  it('prepares recovery without carrying a Bilibili challenge through the admin flow', async () => {
    const prepareRecovery = vi.fn(async () => ({ totpUri: 'otpauth://new', recoveryPassword: '12345678901234567890', handoffToken: 'handoff' }));
    const flow = createAdminRecoveryFlow({ prepareRecovery, confirmRecovery: vi.fn() }, vi.fn());
    await flow.prepare('old-recovery-code');
    expect(prepareRecovery).toHaveBeenCalledWith('old-recovery-code');
  });

});
