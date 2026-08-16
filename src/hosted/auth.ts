import type { Challenge, PollResult } from './api';

interface AuthAPI {
  beginLogin(): Promise<Challenge>;
  pollLogin(id: string): Promise<PollResult>;
  createSession(id: string): Promise<void>;
  cancelLogin(id: string): Promise<void>;
  logout(): Promise<void>;
}

export type AuthStatus = 'pending' | 'verified' | 'registration_required' | 'expired';

export interface AuthCallbacks {
  onStatus(status: AuthStatus, challenge?: Challenge): void;
  onSignedIn(): void;
  onRegistrationRequired(intent: string): void;
}

interface TimerPort {
  setInterval(callback: () => void, milliseconds: number): number;
  clearInterval(id: number): void;
}

export function mountAuthView(
  root: HTMLElement,
  api: AuthAPI,
  callbacks: Pick<AuthCallbacks, 'onSignedIn' | 'onRegistrationRequired'> & { onExit?: () => void },
  timers: TimerPort = window,
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
  let interval: number | undefined;
  const flow = createAuthFlow(api, {
    onStatus(next, challenge) {
      status.textContent = next === 'pending' ? '请使用 B 站客户端扫码确认' : next === 'expired' ? '二维码已过期，请重新开始' : '验证成功';
      if (challenge) qr.src = challenge.qrImage;
      if (next !== 'pending') {
        if (interval !== undefined) timers.clearInterval(interval);
        interval = undefined; qr.removeAttribute('src');
        if (next === 'expired') cancel.textContent = '返回登录入口';
      }
    },
    onSignedIn: callbacks.onSignedIn,
    onRegistrationRequired: callbacks.onRegistrationRequired,
  });
  let disposed = false;
  const ready = flow.start().then(() => {
    if (!disposed) interval = timers.setInterval(() => {
      void flow.poll().catch(() => { status.textContent = '网络暂不可用，正在保留本次登录以便重试'; });
    }, 2000);
  }).catch(() => { status.textContent = '登录服务暂不可用'; });
  const dispose = async (): Promise<void> => {
    if (disposed) return;
    disposed = true;
    if (interval !== undefined) timers.clearInterval(interval);
    interval = undefined;
    await flow.dispose();
    qr.removeAttribute('src');
    root.replaceChildren();
  };
  cancel.addEventListener('click', () => { void dispose().then(() => callbacks.onExit?.()); });
  return Object.freeze({ ready, dispose });
}

export function createAuthFlow(api: AuthAPI, callbacks: AuthCallbacks) {
  let activeChallenge: Challenge | undefined;
  let disposed = false;
  let polling = false;
  let sessionCompletion: Promise<void> | undefined;

  return Object.freeze({
    async start(): Promise<void> {
      if (disposed) return;
      const created = await api.beginLogin();
      if (disposed) {
        await api.cancelLogin(created.challengeId);
        return;
      }
      activeChallenge = created;
      callbacks.onStatus('pending', activeChallenge);
    },
    async poll(): Promise<void> {
      const current = activeChallenge;
      if (!current || disposed || polling || sessionCompletion) return;
      polling = true;
      let result: PollResult;
      try { result = await api.pollLogin(current.challengeId); } finally { polling = false; }
      if (disposed) return;
      if (result.status === 'pending') {
        callbacks.onStatus('pending', current);
        return;
      }
      if (result.status === 'verified') {
        let completeSession!: () => void;
        const ownedCompletion = new Promise<void>((resolve) => { completeSession = resolve; });
        sessionCompletion = ownedCompletion;
        let established = false;
        try {
          await api.createSession(current.challengeId);
          if (disposed) { await api.logout(); return; }
          established = true;
        } finally {
          if (sessionCompletion === ownedCompletion) sessionCompletion = undefined;
          completeSession();
        }
        if (!established) return;
        activeChallenge = undefined;
        callbacks.onStatus('verified');
        callbacks.onSignedIn();
      } else if (result.status === 'registration_required') {
        activeChallenge = undefined;
        const intent = result.registrationIntent;
        callbacks.onStatus('registration_required');
        callbacks.onRegistrationRequired(intent);
      } else {
        activeChallenge = undefined;
        callbacks.onStatus('expired');
      }
    },
    async dispose(): Promise<void> {
      if (disposed) return;
      disposed = true;
      const current = activeChallenge;
      const pendingSession = sessionCompletion;
      activeChallenge = undefined;
      if (current) await api.cancelLogin(current.challengeId);
      await pendingSession;
    },
  });
}
