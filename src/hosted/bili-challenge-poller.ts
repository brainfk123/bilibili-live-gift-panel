import { HostedAPIError } from './api';

const HEALTHY_POLL_DELAY_MS = 6_000;
const NETWORK_RETRY_DELAYS_MS = [2_000, 4_000, 8_000, 15_000] as const;
const RATE_LIMIT_DELAY_MS = 15_000;
const MANUAL_RETRY_COOLDOWN_MS = 2_000;

export type AuthPollOutcome = 'pending' | 'scanned' | 'terminal' | 'inactive';

export type BiliChallengePollFailureKind =
  | 'network'
  | 'rate_limited'
  | 'temporarily_unavailable'
  | 'fatal';

export interface BiliChallengePollSnapshot {
  busy: boolean;
  failureKind?: BiliChallengePollFailureKind;
  retryInSeconds?: number;
  canRetryNow: boolean;
}

export interface BiliChallengePollPort {
  poll(): Promise<AuthPollOutcome>;
}

export interface BiliChallengeTimerPort {
  setTimeout(callback: () => void, milliseconds: number): number;
  clearTimeout(id: number): void;
  now(): number;
}

export interface BiliChallengePoller {
  start(): void;
  retryNow(): void;
  stop(): Promise<void>;
}

function failureKind(error: unknown): BiliChallengePollFailureKind {
  if (!(error instanceof HostedAPIError)) return 'network';
  if (error.code === 'rate_limited') return 'rate_limited';
  if (error.code === 'temporarily_unavailable') return 'temporarily_unavailable';
  return 'fatal';
}

export function createBiliChallengePoller(
  port: BiliChallengePollPort,
  timers: BiliChallengeTimerPort,
  render: (snapshot: BiliChallengePollSnapshot) => void,
): BiliChallengePoller {
  let started = false;
  let stopped = false;
  let timer: number | undefined;
  let inFlight: Promise<void> | undefined;
  let lastAttemptAt: number | undefined;
  let manualRetryAt: number | undefined;
  let consecutiveFailures = 0;

  const clearTimer = (): void => {
    if (timer === undefined) return;
    timers.clearTimeout(timer);
    timer = undefined;
  };

  const publish = (
    busy: boolean,
    kind?: BiliChallengePollFailureKind,
    retryDelay?: number,
  ): void => {
    if (stopped) return;
    const canRetryNow = kind !== undefined
      && kind !== 'fatal'
      && !busy
      && manualRetryAt !== undefined
      && timers.now() >= manualRetryAt;
    render({
      busy,
      ...(kind ? { failureKind: kind } : {}),
      ...(retryDelay === undefined ? {} : { retryInSeconds: Math.ceil(retryDelay / 1_000) }),
      canRetryNow,
    });
  };

  const schedule = (delay: number, kind?: BiliChallengePollFailureKind): void => {
    if (stopped) return;
    clearTimer();
    publish(false, kind, kind ? delay : undefined);
    timer = timers.setTimeout(() => {
      timer = undefined;
      attempt();
    }, delay);
  };

  const handleSuccess = (outcome: AuthPollOutcome): void => {
    if (stopped) return;
    consecutiveFailures = 0;
    manualRetryAt = undefined;
    if (outcome === 'pending' || outcome === 'scanned') {
      schedule(HEALTHY_POLL_DELAY_MS);
      return;
    }
    publish(false);
    stopped = true;
    clearTimer();
  };

  const handleFailure = (error: unknown): void => {
    if (stopped) return;
    const kind = failureKind(error);
    if (kind === 'fatal') {
      publish(false, kind);
      stopped = true;
      clearTimer();
      return;
    }
    const delay = kind === 'rate_limited'
      ? RATE_LIMIT_DELAY_MS
      : NETWORK_RETRY_DELAYS_MS[Math.min(consecutiveFailures, NETWORK_RETRY_DELAYS_MS.length - 1)];
    consecutiveFailures++;
    manualRetryAt = kind === 'rate_limited'
      ? timers.now() + RATE_LIMIT_DELAY_MS
      : (lastAttemptAt ?? timers.now()) + MANUAL_RETRY_COOLDOWN_MS;
    schedule(delay, kind);
  };

  const attempt = (): void => {
    if (stopped || inFlight) return;
    clearTimer();
    lastAttemptAt = timers.now();
    manualRetryAt = lastAttemptAt + MANUAL_RETRY_COOLDOWN_MS;
    publish(true);
    const poll = Promise.resolve().then(() => port.poll());
    let owned!: Promise<void>;
    owned = poll.then(handleSuccess, handleFailure).finally(() => {
      if (inFlight === owned) inFlight = undefined;
    });
    inFlight = owned;
  };

  return Object.freeze({
    start(): void {
      if (started || stopped) return;
      started = true;
      schedule(HEALTHY_POLL_DELAY_MS);
    },

    retryNow(): void {
      if (!started || stopped || inFlight || manualRetryAt === undefined || timers.now() < manualRetryAt) return;
      clearTimer();
      attempt();
    },

    async stop(): Promise<void> {
      if (!stopped) {
        stopped = true;
        clearTimer();
      }
      await inFlight;
    },
  });
}
