import type { Challenge, PollResult } from './api';
import type {
  AuthPollOutcome,
  BiliChallengePoller,
  BiliChallengePollPort,
  BiliChallengePollSnapshot,
} from './bili-challenge-poller';

export interface AuthAPI {
  beginLogin(): Promise<Challenge>;
  pollLogin(id: string): Promise<PollResult>;
  createSession(id: string): Promise<void>;
  cancelLogin(id: string): Promise<void>;
  logout(): Promise<void>;
}

export type AuthStatus = 'pending' | 'scanned' | 'verified' | 'registration_required' | 'expired';

export interface AuthCallbacks {
  onStatus(status: AuthStatus, challenge?: Challenge): void;
  onSignedIn(): void;
  onRegistrationRequired(intent: string): void;
}

export interface AuthFlow {
  start(): Promise<void>;
  poll(): Promise<AuthPollOutcome>;
  dispose(): Promise<void>;
}

export type BiliChallengePollerFactory = (
  port: BiliChallengePollPort,
  render: (snapshot: BiliChallengePollSnapshot) => void,
) => BiliChallengePoller;

export interface AuthController {
  start(): Promise<void>;
  retryNow(): void;
  dispose(): Promise<void>;
}

export function createAuthController(
  flow: AuthFlow,
  pollerFactory: BiliChallengePollerFactory,
  render: (snapshot: BiliChallengePollSnapshot) => void,
): AuthController {
  let disposed = false;
  let poller: BiliChallengePoller | undefined;
  let starting: Promise<void> | undefined;

  return Object.freeze({
    start(): Promise<void> {
      if (disposed) return Promise.resolve();
      if (starting) return starting;
      let owned!: Promise<void>;
      owned = (async () => {
        const previous = poller;
        poller = undefined;
        await previous?.stop();
        if (disposed) return;
        await flow.start();
        if (disposed) return;
        const next = pollerFactory({ poll: () => flow.poll() }, render);
        if (disposed) { await next.stop(); return; }
        poller = next;
        next.start();
      })().finally(() => {
        if (starting === owned) starting = undefined;
      });
      starting = owned;
      return owned;
    },

    retryNow(): void {
      if (!disposed) poller?.retryNow();
    },

    async dispose(): Promise<void> {
      if (disposed) return;
      disposed = true;
      const current = poller;
      poller = undefined;
      await Promise.all([flow.dispose(), current?.stop(), starting]);
    },
  });
}

export function createAuthFlow(api: AuthAPI, callbacks: AuthCallbacks): AuthFlow {
  let activeChallenge: Challenge | undefined;
  let disposed = false;
  let polling = false;
  let sessionCompletion: Promise<void> | undefined;

  return Object.freeze({
    async start(): Promise<void> {
      if (disposed) return;
      const previous = activeChallenge;
      activeChallenge = undefined;
      if (previous) {
        try {
          await api.cancelLogin(previous.challengeId);
        } catch (error) {
          if (!disposed && !activeChallenge) activeChallenge = previous;
          throw error;
        }
      }
      if (disposed) return;
      const created = await api.beginLogin();
      if (disposed) {
        await api.cancelLogin(created.challengeId);
        return;
      }
      activeChallenge = created;
      callbacks.onStatus('pending', activeChallenge);
    },

    async poll(): Promise<AuthPollOutcome> {
      const current = activeChallenge;
      if (!current || disposed || polling || sessionCompletion) return 'inactive';
      polling = true;
      let result: PollResult;
      try { result = await api.pollLogin(current.challengeId); } finally { polling = false; }
      if (disposed) return 'inactive';
      if (result.status === 'pending' || result.status === 'scanned') {
        callbacks.onStatus(result.status, current);
        return result.status;
      }
      if (result.status === 'verified') {
        let completeSession!: () => void;
        const ownedCompletion = new Promise<void>((resolve) => { completeSession = resolve; });
        sessionCompletion = ownedCompletion;
        let established = false;
        try {
          await api.createSession(current.challengeId);
          if (disposed) { await api.logout(); return 'inactive'; }
          established = true;
        } finally {
          if (sessionCompletion === ownedCompletion) sessionCompletion = undefined;
          completeSession();
        }
        if (!established) return 'inactive';
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
      return 'terminal';
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
