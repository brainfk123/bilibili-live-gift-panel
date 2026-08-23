import { createAdminLoginController, type AdminLoginAPI, type AdminLoginState } from './admin/login-controller';
import { mountVerificationInput, type VerificationInputControl } from './admin/verification-input';

export { createAdminLoginController, createAdminLoginFlow } from './admin/login-controller';
export type { AdminLoginState } from './admin/login-controller';

export function mountAdminLogin(root: HTMLElement, api: AdminLoginAPI, options: { onSignedIn(): void; onExit?(): void }) {
  const document = root.ownerDocument;
  let disposed = false;
  let codeControl: VerificationInputControl | undefined;
  let enteredCode = '';
  let focusCode = false;
  const action = (label: string, run: () => void): HTMLButtonElement => {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = label; button.addEventListener('click', run); return button;
  };
  let flow: ReturnType<typeof createAdminLoginController>;
  const render = (state: AdminLoginState): void => {
    if (disposed) return;
    if (codeControl?.value()) enteredCode = codeControl.value();
    codeControl?.dispose(); codeControl = undefined; focusCode = false;
    const panel = document.createElement('main'); panel.className = 'hosted-admin-login';
    const eyebrow = document.createElement('p'); eyebrow.className = 'hosted-admin-eyebrow'; eyebrow.textContent = 'GIFT PANEL · HOSTED';
    const title = document.createElement('h1'); title.textContent = '管理员登录';
    const status = document.createElement('p'); status.className = 'hosted-admin-login-status'; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    panel.append(eyebrow, title, status);
    if (state.kind === 'checking-session') status.textContent = '正在恢复管理员会话…';
    if (state.kind === 'restore-timeout') {
      status.textContent = '暂时无法确认登录状态';
      panel.append(action('重新检查', () => { void flow.start(); }), action('发送邮箱验证码', () => { void flow.startEmail(); }));
    }
    if (state.kind === 'ready') { status.textContent = '使用邮箱验证码登录'; panel.append(action('发送邮箱验证码', () => { void flow.startEmail(); })); }
    if (state.kind === 'requesting-email') { enteredCode = ''; status.textContent = '正在发送邮箱验证码…'; }
    if (state.kind === 'awaiting-email-code' || state.kind === 'verifying-email' || state.kind === 'network-error') {
      status.textContent = state.kind === 'verifying-email' ? '正在验证验证码…' : state.kind === 'network-error' ? '网络连接失败，请检查网络后重试' : '验证码已发送到管理员邮箱，有效期 5 分钟';
      const codeRoot = document.createElement('div'); codeRoot.className = 'hosted-code-control'; panel.append(codeRoot);
      codeControl = mountVerificationInput(codeRoot, { label: '六位邮箱验证码', initialValue: state.kind === 'network-error' ? enteredCode : undefined, onComplete: (code) => {
        enteredCode = code; codeControl?.setBusy(true); void flow.submitEmailCode(code).catch(() => undefined);
      } });
      codeControl.setBusy(state.kind === 'verifying-email'); focusCode = state.kind === 'awaiting-email-code';
      if (state.kind === 'verifying-email') { const spinner = document.createElement('span'); spinner.className = 'hosted-admin-spinner'; spinner.setAttribute('aria-hidden', 'true'); panel.append(spinner); }
      if (state.kind === 'awaiting-email-code') panel.append(action('重新发送邮箱验证码', () => { void flow.startEmail(); }));
      if (state.kind === 'network-error') panel.append(action('重新验证', () => { codeControl?.setBusy(true); void flow.submitEmailCode(enteredCode).catch(() => undefined); }));
    }
    if (state.kind === 'rate-limited') status.textContent = '操作过于频繁，请稍后重试';
    if (state.kind === 'email-error') {
      enteredCode = ''; status.textContent = state.reason === 'invalid-or-expired-code' ? '验证码错误或已失效' : '邮件服务暂时不可用';
      panel.append(action('重新发送邮箱验证码', () => { void flow.startEmail(); }));
    }
    root.replaceChildren(panel); if (focusCode) codeControl?.focus();
    if (state.kind === 'signed-in') { enteredCode = ''; options.onSignedIn(); }
  };
  flow = createAdminLoginController(api, render); void flow.start();
  return Object.freeze({ dispose: async (): Promise<void> => { if (disposed) return; disposed = true; codeControl?.dispose(); codeControl = undefined; await flow.dispose(); } });
}
