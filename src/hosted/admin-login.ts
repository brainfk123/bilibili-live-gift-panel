import { HostedAPIError, type AdminProofStatus, type Challenge } from './api';
import { mountVerificationCode, type VerificationCodeControl } from './verification-code';

export type AdminLoginState =
  | { kind: 'checking-session' }
  | { kind: 'service-unavailable' }
  | { kind: 'awaiting-bilibili'; challenge: Challenge }
  | { kind: 'awaiting-totp' }
  | { kind: 'verifying-totp' }
  | { kind: 'totp-error'; retryable: boolean }
  | { kind: 'signed-in' };

interface AdminLoginAPI {
  adminSession(): Promise<void>;
  beginAdminProof(): Promise<Challenge>;
  pollAdminProof(id: string): Promise<AdminProofStatus>;
  adminLogin(challengeId: string, totp: string): Promise<void>;
  cancelAdminProof(id: string): Promise<void>;
  logout(): Promise<void>;
}

export function createAdminLoginFlow(api: AdminLoginAPI, render: (state: AdminLoginState) => void) {
  let challenge: Challenge | undefined;
  let state: AdminLoginState = { kind: 'checking-session' };
  let disposed = false;
  let generation = 0;
  let startOperation: Promise<void> | undefined;
  let pollOperation: Promise<void> | undefined;
  let submitOperation: Promise<void> | undefined;
  const publish = (next: AdminLoginState): void => { state = next; if (!disposed) render(next); };
  const unavailable = (): HostedAPIError => new HostedAPIError('operation_failed', 0);

  const start = (): Promise<void> => {
    if (disposed) return Promise.reject(unavailable());
    if (startOperation) return startOperation;
    const current = ++generation;
    publish({ kind: 'checking-session' });
    const operation = (async () => {
      try {
        await api.adminSession();
        if (!disposed && current === generation) publish({ kind: 'signed-in' });
        return;
      } catch (error) {
        if (!(error instanceof HostedAPIError) || error.status !== 401) {
          if (!disposed && current === generation) publish({ kind: 'service-unavailable' });
          return;
        }
      }
      const created = await api.beginAdminProof();
      if (disposed || current !== generation) { await api.cancelAdminProof(created.challengeId); return; }
      challenge = created;
      publish({ kind: 'awaiting-bilibili', challenge: { ...created } });
    })().finally(() => { if (startOperation === operation) startOperation = undefined; });
    startOperation = operation;
    return operation;
  };

  const poll = (): Promise<void> => {
    if (disposed || !challenge || state.kind !== 'awaiting-bilibili') return Promise.reject(new HostedAPIError('invalid_request', 400));
    if (pollOperation) return pollOperation;
    const id = challenge.challengeId; const current = generation;
    const operation = (async () => {
      try {
        const result = await api.pollAdminProof(id);
        if (disposed || current !== generation || challenge?.challengeId !== id) return;
        if (result.status === 'verified') publish({ kind: 'awaiting-totp' });
        if (result.status === 'expired') { challenge = undefined; publish({ kind: 'service-unavailable' }); }
      } catch (error) {
        if (disposed || current !== generation) return;
        if (!(error instanceof HostedAPIError && error.code === 'temporarily_unavailable')) throw error;
      }
    })().finally(() => { if (pollOperation === operation) pollOperation = undefined; });
    pollOperation = operation;
    return operation;
  };

  const submit = (totp: string): Promise<void> => {
    if (submitOperation) return submitOperation;
    if (disposed || !challenge || (state.kind !== 'awaiting-totp' && state.kind !== 'totp-error')) return Promise.reject(new HostedAPIError('invalid_request', 400));
    const id = challenge.challengeId; const current = generation;
    publish({ kind: 'verifying-totp' });
    const operation = (async () => {
      try {
        await api.adminLogin(id, totp);
      } catch (error) {
        if (!disposed && current === generation) publish({ kind: 'totp-error', retryable: error instanceof HostedAPIError && error.status === 401 });
        throw error;
      }
      if (disposed || current !== generation) { await api.logout(); return; }
      challenge = undefined;
      publish({ kind: 'signed-in' });
    })().finally(() => { if (submitOperation === operation) submitOperation = undefined; });
    submitOperation = operation;
    return operation;
  };

  return Object.freeze({
    start, poll, submit,
    state(): AdminLoginState { return state.kind === 'awaiting-bilibili' ? { ...state, challenge: { ...state.challenge } } : { ...state }; },
    async dispose(): Promise<void> {
      if (disposed) return;
      disposed = true; generation += 1;
      const owned = challenge; challenge = undefined;
      if (owned) await api.cancelAdminProof(owned.challengeId);
      await Promise.allSettled([startOperation, pollOperation, submitOperation].filter((value): value is Promise<void> => Boolean(value)));
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
  let pollTimer: ReturnType<typeof setTimeout> | undefined;
  let codeControl: VerificationCodeControl | undefined;
  const stopPolling = (): void => { if (pollTimer !== undefined) clearTimeout(pollTimer); pollTimer = undefined; };
  const action = (label: string, run: () => void): HTMLButtonElement => {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = label; button.addEventListener('click', run); return button;
  };
  let flow: ReturnType<typeof createAdminLoginFlow>;
  const schedulePoll = (): void => {
    stopPolling();
    pollTimer = setTimeout(() => { void flow.poll().finally(() => { if (!disposed && flow.state().kind === 'awaiting-bilibili') schedulePoll(); }); }, 1200);
  };
  const render = (state: AdminLoginState): void => {
    if (disposed) return;
    stopPolling(); codeControl?.dispose(); codeControl = undefined;
    const panel = document.createElement('main'); panel.className = 'hosted-admin-login';
    const eyebrow = document.createElement('p'); eyebrow.className = 'hosted-admin-eyebrow'; eyebrow.textContent = 'GIFT PANEL · HOSTED';
    const title = document.createElement('h1'); title.textContent = '管理员登录';
    const status = document.createElement('p'); status.className = 'hosted-admin-login-status'; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    panel.append(eyebrow, title, status);
    if (state.kind === 'checking-session') status.textContent = '正在恢复管理员会话…';
    if (state.kind === 'service-unavailable') {
      status.textContent = '登录服务暂时不可用，请稍后重试。';
      panel.append(action('重新检查', () => { void flow.start(); }));
    }
    if (state.kind === 'awaiting-bilibili') {
      status.textContent = '请先完成 B 站身份验证';
      if (state.challenge.verificationUrl) {
        const open = document.createElement('a'); open.className = 'hosted-admin-mobile-action'; open.textContent = '打开 B站完成验证'; open.href = state.challenge.verificationUrl; open.target = '_blank'; open.rel = 'noopener noreferrer'; panel.append(open);
      }
      const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = '管理员 B 站身份验证二维码'; qr.src = state.challenge.qrImage;
      const fallback = document.createElement('p'); fallback.textContent = '也可以保存二维码后，在 B 站扫码页从相册识别。';
      panel.append(qr, fallback, action('我已完成验证', () => { void flow.poll(); }), action('取消', () => { void flow.dispose().then(() => options.onExit?.()); }));
      schedulePoll();
    }
    if (state.kind === 'awaiting-totp' || state.kind === 'totp-error' || state.kind === 'verifying-totp') {
      status.textContent = state.kind === 'totp-error' ? '验证码不正确，请重新输入。' : state.kind === 'verifying-totp' ? '正在验证…' : '输入身份验证器中的 6 位动态验证码';
      const codeRoot = document.createElement('div'); codeRoot.className = 'hosted-code-control'; panel.append(codeRoot);
      codeControl = mountVerificationCode(codeRoot, { label: '六位动态验证码', onComplete: (code) => { codeControl?.setBusy(true); void flow.submit(code).catch(() => { codeControl?.setBusy(false); codeControl?.clear(); }); } });
      codeControl.setBusy(state.kind === 'verifying-totp');
      if (state.kind !== 'verifying-totp') codeControl.focus();
    }
    root.replaceChildren(panel);
    if (state.kind === 'signed-in') options.onSignedIn();
  };
  flow = createAdminLoginFlow(api, render);
  void flow.start();
  return Object.freeze({ dispose: async (): Promise<void> => { if (disposed) return; disposed = true; stopPolling(); codeControl?.dispose(); codeControl = undefined; await flow.dispose(); } });
}
