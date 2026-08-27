import {
  createAuthController,
  createAuthFlow,
  type AuthAPI,
  type AuthCallbacks,
} from './auth-controller';
import { HostedAPIError } from './api';
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
  const page = document.createElement('main'); page.className = 'hosted-auth-page'; page.setAttribute('aria-labelledby', 'bili-login-title');
  const card = document.createElement('section'); card.className = 'hosted-auth-card';
  const copy = document.createElement('div'); copy.className = 'hosted-auth-copy';
  const eyebrow = document.createElement('p'); eyebrow.className = 'hosted-auth-eyebrow'; eyebrow.textContent = 'BILIBILI 登录';
  const title = document.createElement('h1'); title.id = 'bili-login-title'; title.textContent = '使用 B 站账号登录';
  const description = document.createElement('p'); description.className = 'hosted-auth-description'; description.textContent = '扫码确认后即可进入主播工作区。';
  const steps = document.createElement('ol'); steps.className = 'hosted-auth-steps';
  for (const instruction of ['打开 B 站客户端扫描二维码', '在手机上确认本次登录']) {
    const step = document.createElement('li'); step.textContent = instruction; steps.append(step);
  }
  const status = document.createElement('p'); status.className = 'hosted-auth-status'; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const expiry = document.createElement('p'); expiry.className = 'hosted-auth-expiry'; expiry.hidden = true;
  copy.append(eyebrow, title, description, steps, status, expiry);

  const qrColumn = document.createElement('div'); qrColumn.className = 'hosted-auth-qr-column';
  const qrFrame = document.createElement('div'); qrFrame.className = 'hosted-auth-qr-frame';
  const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = 'B 站登录二维码'; qr.hidden = true;
  const placeholder = document.createElement('div'); placeholder.className = 'hosted-auth-qr-placeholder';
  qrFrame.append(qr, placeholder);
  const mobileLink = document.createElement('a'); mobileLink.className = 'hosted-auth-mobile-link'; mobileLink.textContent = '在本机打开 B 站确认'; mobileLink.hidden = true;
  mobileLink.setAttribute('target', '_blank');
  mobileLink.setAttribute('rel', 'noopener noreferrer');
  const actions = document.createElement('div'); actions.className = 'hosted-auth-actions';
  const primary = document.createElement('button'); primary.type = 'button'; primary.dataset.variant = 'primary';
  const cancel = document.createElement('button'); cancel.type = 'button'; cancel.dataset.variant = 'secondary'; cancel.textContent = '取消';
  actions.append(primary, cancel);
  qrColumn.append(qrFrame, mobileLink, actions);
  card.append(copy, qrColumn); page.append(card); root.replaceChildren(page);

  type PrimaryMode = 'none' | 'retry' | 'regenerate';
  let primaryMode: PrimaryMode = 'none';
  let disposed = false;
  let cancellationRequested = false;
  let cancellationOperation: Promise<void> | undefined;

  const setStatus = (message: string, kind: string, busy = false): void => {
    status.textContent = message;
    status.dataset.kind = kind;
    status.setAttribute('aria-busy', String(busy));
  };
  const showPlaceholder = (message: string, busy = false): void => {
    qr.hidden = true;
    placeholder.hidden = false;
    placeholder.replaceChildren();
    placeholder.textContent = message;
    if (busy) {
      const spinner = document.createElement('span');
      spinner.className = 'hosted-admin-action-spinner hosted-auth-spinner';
      spinner.setAttribute('aria-hidden', 'true');
      placeholder.append(spinner);
    }
  };
  const clearChallenge = (placeholderMessage: string): void => {
    qr.removeAttribute('src');
    mobileLink.removeAttribute('href');
    mobileLink.hidden = true;
    expiry.hidden = true;
    showPlaceholder(placeholderMessage);
  };
  const showPrimary = (label: string, mode: Exclude<PrimaryMode, 'none'>, busy = false, disabled = false): void => {
    primaryMode = mode;
    primary.hidden = false;
    primary.replaceChildren();
    primary.textContent = label;
    primary.disabled = busy || disabled;
    primary.setAttribute('aria-busy', String(busy));
    if (busy) {
      const spinner = document.createElement('span');
      spinner.className = 'hosted-admin-action-spinner';
      spinner.setAttribute('aria-hidden', 'true');
      primary.append(spinner);
    }
  };
  const hidePrimary = (): void => {
    primaryMode = 'none';
    primary.hidden = true;
    primary.disabled = false;
    primary.setAttribute('aria-busy', 'false');
  };
  const showCancel = (label = '取消', busy = false): void => {
    cancel.textContent = label;
    cancel.disabled = busy;
    cancel.setAttribute('aria-busy', String(busy));
  };
  const renderCleanupFailure = (action: 'cancel' | 'regenerate'): void => {
    clearChallenge('二维码清理失败');
    if (action === 'cancel') {
      setStatus('二维码清理失败，请重试返回', 'error');
      hidePrimary();
      showCancel('重试返回');
      return;
    }
    setStatus('二维码清理失败，请先重试清理', 'error');
    showPrimary('重试清理并重新生成', 'regenerate');
    showCancel();
  };
  const renderCreating = (): void => {
    clearChallenge('二维码生成中');
    showPlaceholder('二维码生成中', true);
    setStatus('正在创建二维码', 'creating', true);
    showPrimary('正在创建', 'regenerate', true);
    showCancel();
  };
  const renderChallenge = (next: 'pending' | 'scanned', challenge: Parameters<AuthCallbacks['onStatus']>[1]): void => {
    if (!challenge) return;
    qr.src = challenge.qrImage;
    qr.hidden = false;
    placeholder.hidden = true;
    if (challenge.verificationUrl) {
      mobileLink.href = challenge.verificationUrl;
      mobileLink.hidden = false;
    } else {
      mobileLink.removeAttribute('href');
      mobileLink.hidden = true;
    }
    const expires = new Date(challenge.expiresAt);
    expiry.textContent = Number.isNaN(expires.getTime())
      ? '二维码将在稍后失效'
      : `二维码有效期至 ${expires.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}`;
    expiry.hidden = false;
    setStatus(next === 'pending' ? '请使用 B 站客户端扫码' : '已扫码，请在手机确认', next === 'pending' ? 'pending' : 'success');
    hidePrimary();
    showCancel();
  };
  const flow = createAuthFlow(api, {
    onStatus(next, challenge) {
      if (next === 'pending' || next === 'scanned') { renderChallenge(next, challenge); return; }
      if (next === 'expired') {
        clearChallenge('二维码已过期');
        setStatus('二维码已过期', 'expired');
        showPrimary('重新生成', 'regenerate');
        return;
      }
      clearChallenge(next === 'verified' ? '登录已确认' : '正在进入邀请码兑换');
      setStatus(next === 'verified' ? '验证成功' : '请继续完成账号注册', 'success');
      hidePrimary();
    },
    onSignedIn: callbacks.onSignedIn,
    onRegistrationRequired: callbacks.onRegistrationRequired,
  });
  const renderPoll = (snapshot: BiliChallengePollSnapshot): void => {
    if (disposed) return;
    if (!snapshot.failureKind) {
      status.setAttribute('aria-busy', String(snapshot.busy));
      if (primaryMode === 'retry') showPrimary('立即重试', 'retry', snapshot.busy, !snapshot.canRetryNow);
      return;
    }
    if (snapshot.failureKind === 'fatal') {
      clearChallenge('此二维码无法继续使用');
      setStatus('登录响应无效，请重新生成二维码', 'error');
      showPrimary('重新生成', 'regenerate');
      return;
    }
    const message = snapshot.failureKind === 'rate_limited'
      ? '请求较频繁，稍后自动重试'
      : snapshot.failureKind === 'temporarily_unavailable'
        ? `登录服务暂不可用，${snapshot.retryInSeconds} 秒后自动重试`
        : `网络暂不可用，${snapshot.retryInSeconds} 秒后自动重试`;
    setStatus(message, 'warning', snapshot.busy);
    showPrimary('立即重试', 'retry', snapshot.busy, !snapshot.canRetryNow);
  };
  const controller = createAuthController(
    flow,
    (port, render) => createBiliChallengePoller(port, timers, render),
    renderPoll,
  );
  const startChallenge = (): Promise<void> => {
    if (disposed) return Promise.resolve();
    renderCreating();
    return controller.start().catch((error) => {
      if (disposed) return;
      if (cancellationRequested) return;
      if (error instanceof HostedAPIError && error.code === 'operation_failed') {
        renderCleanupFailure('regenerate');
        return;
      }
      clearChallenge('二维码创建失败');
      setStatus('无法创建二维码，请再次尝试', 'error');
      showPrimary('再次尝试', 'regenerate');
    });
  };
  primary.addEventListener('click', () => {
    if (primaryMode === 'retry') { controller.retryNow(); return; }
    if (primaryMode === 'regenerate') void startChallenge();
  });
  const ready = startChallenge();
  const dispose = async (): Promise<void> => {
    disposed = true;
    try {
      await controller.dispose();
    } finally {
      qr.removeAttribute('src');
      mobileLink.removeAttribute('href');
      root.replaceChildren();
    }
  };
  const cancelAndExit = (): Promise<void> => {
    if (disposed) return Promise.resolve();
    if (cancellationOperation) return cancellationOperation;
    cancellationRequested = true;
    clearChallenge('正在清理二维码');
    setStatus('正在清理二维码', 'creating', true);
    hidePrimary();
    showCancel('正在返回…', true);
    let owned!: Promise<void>;
    owned = (async () => {
      try {
        await controller.dispose();
      } catch {
        if (!disposed) renderCleanupFailure('cancel');
        return;
      }
      if (disposed) return;
      disposed = true;
      root.replaceChildren();
      callbacks.onExit?.();
    })().finally(() => {
      if (cancellationOperation === owned) cancellationOperation = undefined;
    });
    cancellationOperation = owned;
    return owned;
  };
  cancel.addEventListener('click', () => {
    void cancelAndExit();
  });
  return Object.freeze({ ready, dispose });
}
