import { HostedAPIError, type EmailLoginChallenge } from './api';
import { mountVerificationCode, type VerificationCodeControl } from './verification-code';

export type AdminLoginState =
  | { kind: 'checking-session' }
  | { kind: 'ready' }
  | { kind: 'requesting-email' }
  | { kind: 'awaiting-email-code' }
  | { kind: 'verifying-email' }
  | { kind: 'rate-limited' }
  | { kind: 'email-error'; reason: 'email-unavailable' | 'invalid-or-expired-code' }
  | { kind: 'signed-in' }
  | { kind: 'disposed' };

interface AdminLoginAPI {
  adminSession(): Promise<void>;
  beginAdminEmailLogin(): Promise<EmailLoginChallenge>;
  adminEmailLogin(challengeId: string, emailCode: string): Promise<void>;
  adminLogout(): Promise<void>;
}

interface AdminLoginTimerPort {
  setTimeout(callback: () => void, milliseconds: number): unknown;
  clearTimeout(timer: unknown): void;
}

const adminEmailRateLimitCooldownMilliseconds = 60_000;
const defaultAdminLoginTimers: AdminLoginTimerPort = {
  setTimeout: (callback, milliseconds) => globalThis.setTimeout(callback, milliseconds),
  clearTimeout: (timer) => globalThis.clearTimeout(timer as ReturnType<typeof globalThis.setTimeout>),
};

function emailError(error: unknown): AdminLoginState {
  if (error instanceof HostedAPIError && error.status === 429) return { kind: 'rate-limited' };
  if (error instanceof HostedAPIError && error.status === 401) return { kind: 'email-error', reason: 'invalid-or-expired-code' };
  return { kind: 'email-error', reason: 'email-unavailable' };
}

export function createAdminLoginFlow(
  api: AdminLoginAPI,
  render: (state: AdminLoginState) => void,
  timers: AdminLoginTimerPort = defaultAdminLoginTimers,
) {
  let emailChallenge: EmailLoginChallenge | undefined;
  let state: AdminLoginState = { kind: 'checking-session' };
  let disposed = false;
  let generation = 0;
  let startOperation: Promise<void> | undefined;
  let emailOperation: Promise<void> | undefined;
  const submitOperations = new Map<string, Promise<void>>();
  let cooldownTimer: unknown;
  const publish = (next: AdminLoginState): void => { state = next; if (!disposed) render(next); };
  const unavailable = (): HostedAPIError => new HostedAPIError('operation_failed', 0);
  const eraseChallenge = (): void => { emailChallenge = undefined; };
  const clearCooldown = (): void => {
    if (cooldownTimer !== undefined) timers.clearTimeout(cooldownTimer);
    cooldownTimer = undefined;
  };
  const publishError = (error: unknown, current: number): void => {
    const next = emailError(error);
    publish(next);
    if (next.kind !== 'rate-limited') return;
    clearCooldown();
    cooldownTimer = timers.setTimeout(() => {
      cooldownTimer = undefined;
      if (!disposed && current === generation && state.kind === 'rate-limited') publish({ kind: 'ready' });
    }, adminEmailRateLimitCooldownMilliseconds);
  };

  const start = (): Promise<void> => {
    if (disposed) return Promise.reject(unavailable());
    if (startOperation) return startOperation;
    if (state.kind === 'rate-limited') return Promise.reject(new HostedAPIError('invalid_request', 400));
    const current = ++generation;
    clearCooldown(); eraseChallenge();
    publish({ kind: 'checking-session' });
    const operation = (async () => {
      try {
        await api.adminSession();
        if (!disposed && current === generation) publish({ kind: 'signed-in' });
      } catch (error) {
        if (!disposed && current === generation) publish(error instanceof HostedAPIError && error.status === 401
          ? { kind: 'ready' }
          : { kind: 'email-error', reason: 'email-unavailable' });
      }
    })().finally(() => { if (startOperation === operation) startOperation = undefined; });
    startOperation = operation;
    return operation;
  };

  const startEmail = (): Promise<void> => {
    if (emailOperation) return emailOperation;
    if (disposed || !['ready', 'awaiting-email-code', 'verifying-email', 'email-error'].includes(state.kind)) return Promise.reject(new HostedAPIError('invalid_request', 400));
    const current = ++generation;
    eraseChallenge();
    publish({ kind: 'requesting-email' });
    const operation = (async () => {
      try {
        const created = await api.beginAdminEmailLogin();
        if (disposed || current !== generation) return;
        emailChallenge = created;
        publish({ kind: 'awaiting-email-code' });
      } catch (error) {
        if (!disposed && current === generation) publishError(error, current);
      }
    })().finally(() => { if (emailOperation === operation) emailOperation = undefined; });
    emailOperation = operation;
    return operation;
  };

  const submitEmailCode = (code: string): Promise<void> => {
    if (disposed || !emailChallenge || !/^[0-9]{6}$/.test(code)) return Promise.reject(new HostedAPIError('invalid_request', 400));
    const id = emailChallenge.challengeId;
    const current = generation;
    const owner = `${current}\u0000${id}`;
    const existing = submitOperations.get(owner);
    if (existing) return existing;
    if (submitOperations.size > 0) return Promise.reject(new HostedAPIError('operation_conflict', 409));
    if (state.kind !== 'awaiting-email-code') return Promise.reject(new HostedAPIError('invalid_request', 400));
    const ownsChallenge = (): boolean => current === generation && emailChallenge?.challengeId === id;
    publish({ kind: 'verifying-email' });
    const operation = (async () => {
      try {
        await api.adminEmailLogin(id, code);
      } catch (error) {
        if (!disposed && ownsChallenge()) { eraseChallenge(); publishError(error, current); }
        throw error;
      }
      if (disposed || !ownsChallenge()) { await api.adminLogout(); return; }
      eraseChallenge();
      publish({ kind: 'signed-in' });
    })().finally(() => { if (submitOperations.get(owner) === operation) submitOperations.delete(owner); });
    submitOperations.set(owner, operation);
    return operation;
  };

  return Object.freeze({
    start, startEmail, submitEmailCode,
    state(): AdminLoginState { return { ...state }; },
    async dispose(): Promise<void> {
      if (disposed) return;
      disposed = true; generation += 1; clearCooldown(); eraseChallenge(); state = { kind: 'disposed' };
      await Promise.allSettled([
        ...[startOperation, emailOperation].filter((value): value is Promise<void> => Boolean(value)),
        ...submitOperations.values(),
      ]);
    },
  });
}

export function mountAdminLogin(
  root: HTMLElement,
  api: AdminLoginAPI,
  options: { onSignedIn(): void; onExit?(): void },
) {
  const document = root.ownerDocument;
  let disposed = false;
  let codeControl: VerificationCodeControl | undefined;
  let focusCode = false;
  const action = (label: string, run: () => void): HTMLButtonElement => {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = label; button.addEventListener('click', run); return button;
  };
  let flow: ReturnType<typeof createAdminLoginFlow>;
  const render = (state: AdminLoginState): void => {
    if (disposed) return;
    codeControl?.dispose(); codeControl = undefined; focusCode = false;
    const panel = document.createElement('main'); panel.className = 'hosted-admin-login';
    const eyebrow = document.createElement('p'); eyebrow.className = 'hosted-admin-eyebrow'; eyebrow.textContent = 'GIFT PANEL · HOSTED';
    const title = document.createElement('h1'); title.textContent = '管理员登录';
    const status = document.createElement('p'); status.className = 'hosted-admin-login-status'; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    panel.append(eyebrow, title, status);
    if (state.kind === 'checking-session') status.textContent = '正在恢复管理员会话…';
    if (state.kind === 'ready') {
      status.textContent = '使用邮箱验证码登录';
      panel.append(action('发送邮箱验证码', () => { void flow.startEmail(); }));
    }
    if (state.kind === 'requesting-email') status.textContent = '正在发送邮箱验证码…';
    if (state.kind === 'awaiting-email-code' || state.kind === 'verifying-email') {
      status.textContent = state.kind === 'verifying-email' ? '正在验证…' : '验证码已发送到管理员邮箱，有效期 5 分钟';
      const codeRoot = document.createElement('div'); codeRoot.className = 'hosted-code-control'; panel.append(codeRoot);
      codeControl = mountVerificationCode(codeRoot, { label: '六位邮箱验证码', onComplete: (code) => {
        codeControl?.setBusy(true);
        void flow.submitEmailCode(code).catch(() => { codeControl?.setBusy(false); codeControl?.clear(); });
      } });
      codeControl.setBusy(state.kind === 'verifying-email');
      focusCode = state.kind !== 'verifying-email';
      if (state.kind === 'awaiting-email-code') panel.append(action('重新发送邮箱验证码', () => { void flow.startEmail(); }));
    }
    if (state.kind === 'rate-limited') {
      status.textContent = '操作过于频繁，请稍后重试';
    }
    if (state.kind === 'email-error') {
      status.textContent = state.reason === 'invalid-or-expired-code' ? '验证码错误或已失效' : '邮件服务暂时不可用';
      panel.append(action('重新发送邮箱验证码', () => { void flow.startEmail(); }));
    }
    root.replaceChildren(panel);
    if (focusCode) codeControl?.focus();
    if (state.kind === 'signed-in') options.onSignedIn();
  };
  flow = createAdminLoginFlow(api, render);
  void flow.start();
  return Object.freeze({ dispose: async (): Promise<void> => { if (disposed) return; disposed = true; codeControl?.dispose(); codeControl = undefined; await flow.dispose(); } });
}
