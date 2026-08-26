import {
  createAuthController,
  createAuthFlow,
  type AuthAPI,
  type AuthCallbacks,
} from './auth-controller';
import {
  createBiliChallengePoller,
  type BiliChallengePollSnapshot,
  type BiliChallengeTimerPort,
} from './bili-challenge-poller';

export { createAuthController, createAuthFlow } from './auth-controller';
export type { AuthAPI, AuthCallbacks, AuthController, AuthFlow, AuthStatus } from './auth-controller';

function browserTimers(): BiliChallengeTimerPort {
  return {
    setTimeout: (callback, milliseconds) => window.setTimeout(callback, milliseconds),
    clearTimeout: (id) => window.clearTimeout(id),
    now: () => performance.now(),
  };
}

export function mountAuthView(
  root: HTMLElement,
  api: AuthAPI,
  callbacks: Pick<AuthCallbacks, 'onSignedIn' | 'onRegistrationRequired'> & { onExit?: () => void },
  timers: BiliChallengeTimerPort = browserTimers(),
) {
  const document = root.ownerDocument;
  const panel = document.createElement('main');
  panel.className = 'hosted-shell hosted-panel';
  panel.setAttribute('aria-labelledby', 'bili-login-title');
  const title = document.createElement('h1'); title.id = 'bili-login-title'; title.textContent = '使用 B 站账号登录';
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite'); status.textContent = '正在创建二维码…';
  const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = 'B 站登录二维码';
  const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = '取消登录';
  panel.append(title, status, qr, cancel); root.replaceChildren(panel);
  const flow = createAuthFlow(api, {
    onStatus(next, challenge) {
      status.textContent = next === 'pending'
        ? '请使用 B 站客户端扫码确认'
        : next === 'scanned'
          ? '已扫码，请在手机确认'
          : next === 'expired'
            ? '二维码已过期，请重新开始'
            : '验证成功';
      if (challenge) qr.src = challenge.qrImage;
      if (next !== 'pending' && next !== 'scanned') {
        qr.removeAttribute('src');
        if (next === 'expired') cancel.textContent = '返回登录入口';
      }
    },
    onSignedIn: callbacks.onSignedIn,
    onRegistrationRequired: callbacks.onRegistrationRequired,
  });
  let disposed = false;
  const renderPoll = (snapshot: BiliChallengePollSnapshot): void => {
    if (disposed || !snapshot.failureKind) return;
    status.textContent = snapshot.failureKind === 'rate_limited'
      ? '请求较频繁，稍后自动重试'
      : snapshot.failureKind === 'temporarily_unavailable'
        ? '登录服务暂不可用，正在保留本次登录以便重试'
        : snapshot.failureKind === 'network'
          ? '网络暂不可用，正在保留本次登录以便重试'
          : '登录响应无效，请重新开始';
    if (snapshot.failureKind === 'fatal') qr.removeAttribute('src');
  };
  const controller = createAuthController(
    flow,
    (port, render) => createBiliChallengePoller(port, timers, render),
    renderPoll,
  );
  const ready = controller.start().catch(() => { if (!disposed) status.textContent = '登录服务暂不可用'; });
  const dispose = async (): Promise<void> => {
    if (disposed) return;
    disposed = true;
    await controller.dispose();
    qr.removeAttribute('src');
    root.replaceChildren();
  };
  cancel.addEventListener('click', () => { void dispose().then(() => callbacks.onExit?.()); });
  return Object.freeze({ ready, dispose });
}
