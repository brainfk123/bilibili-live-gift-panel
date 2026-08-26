import { HostedAPIError, type BiliServiceChallengeStage, type BiliServiceStatus, type Challenge, type HostedAPI } from '../api';
import {
  createBiliChallengePoller,
  type AuthPollOutcome,
  type BiliChallengePoller,
  type BiliChallengePollSnapshot,
  type BiliChallengeTimerPort,
} from '../bili-challenge-poller';
import { authorizeAdminOperation } from './operation-authorization';

export type BiliServicePhase = 'idle' | 'checking' | 'creating' | 'cleaning' | 'cleanup_failed' | 'qr' | 'authorizing' | 'replacing';
export type BiliServiceNotice = { kind: 'success' | 'error'; message: string };

export interface BiliServiceSnapshot {
  phase: BiliServicePhase;
  status?: BiliServiceStatus;
  challenge?: Pick<Challenge, 'qrImage' | 'expiresAt'>;
  challengeStatus?: BiliServiceChallengeStage;
  notice?: BiliServiceNotice;
}

type BiliServiceAPI = Pick<HostedAPI, 'biliServiceStatus' | 'checkBiliService' | 'beginBiliServiceChallenge' | 'pollBiliServiceChallenge' | 'cancelBiliServiceChallenge' | 'replaceBiliServiceCredential' | 'authorizeAdminOperation'>;

export interface BiliServiceController {
  load(): Promise<void>;
  check(): Promise<void>;
  beginReplacement(): Promise<void>;
  enterAuthorization(): void;
  cancelReplacement(): Promise<void>;
  authorizeAndReplace(totp: string): Promise<void>;
  dispose(): Promise<void>;
}

function browserTimers(): BiliChallengeTimerPort {
  return {
    setTimeout: (callback, milliseconds) => window.setTimeout(callback, milliseconds),
    clearTimeout: (id) => window.clearTimeout(id),
    now: () => performance.now(),
  };
}

export function createBiliServiceController(
  api: Partial<BiliServiceAPI>,
  render: (snapshot: BiliServiceSnapshot) => void,
  timers: BiliChallengeTimerPort = browserTimers(),
): BiliServiceController {
  let phase: BiliServicePhase = 'idle';
  let status: BiliServiceStatus | undefined;
  let challenge: Challenge | undefined;
  let challengeStatus: BiliServiceChallengeStage | undefined;
  let notice: BiliServiceNotice | undefined;
  let poller: BiliChallengePoller | undefined;
  let beginOperation: Promise<void> | undefined;
  let cleanupOperation: Promise<void> | undefined;
  let generation = 0;
  let disposed = false;
  const publish = (): void => {
    if (disposed) return;
    const revealChallenge = phase === 'qr' || phase === 'cleanup_failed' || phase === 'authorizing' || phase === 'replacing';
    render({
      phase,
      ...(status ? { status } : {}),
      ...(challenge && revealChallenge ? { challenge: { qrImage: challenge.qrImage, expiresAt: challenge.expiresAt } } : {}),
      ...(challengeStatus ? { challengeStatus } : {}),
      ...(notice ? { notice: { ...notice } } : {}),
    });
  };
  const complete = (current: number): boolean => !disposed && current === generation;
  const clearChallenge = (): void => { challenge = undefined; challengeStatus = undefined; };
  const stopPolling = (): Promise<void> => {
    const current = poller;
    poller = undefined;
    return current?.stop() ?? Promise.resolve();
  };
  const cleanupFailure = (): HostedAPIError => new HostedAPIError('operation_failed', 0);
  const publishCleanupFailure = (): void => {
    if (disposed) return;
    phase = 'cleanup_failed';
    notice = { kind: 'error', message: '二维码清理失败，请重试取消或重新生成' };
    publish();
  };
  const cleanupChallenge = (): Promise<void> => {
    if (!challenge) return Promise.resolve();
    if (cleanupOperation) return cleanupOperation;
    const active = challenge;
    let owned!: Promise<void>;
    owned = (async () => {
      if (!api.cancelBiliServiceChallenge) throw cleanupFailure();
      try {
        await api.cancelBiliServiceChallenge(active.challengeId);
      } catch {
        throw cleanupFailure();
      }
      if (challenge?.challengeId === active.challengeId) clearChallenge();
    })().finally(() => { if (cleanupOperation === owned) cleanupOperation = undefined; });
    cleanupOperation = owned;
    return owned;
  };
  const failureNotice = (snapshot: BiliChallengePollSnapshot): BiliServiceNotice | undefined => {
    if (!snapshot.failureKind) return undefined;
    if (snapshot.failureKind === 'fatal') return { kind: 'error', message: '登录响应无效，请重新生成二维码' };
    if (snapshot.failureKind === 'rate_limited') return { kind: 'error', message: '请求较频繁，稍后将自动重试' };
    if (snapshot.failureKind === 'temporarily_unavailable') return { kind: 'error', message: '登录服务暂不可用，稍后将自动重试' };
    return { kind: 'error', message: '网络暂不可用，稍后将自动重试' };
  };
  const startPolling = (current: number): void => {
    const active = challenge;
    if (!active || !api.pollBiliServiceChallenge || !complete(current)) return;
    const ownsChallenge = (): boolean => complete(current) && challenge?.challengeId === active.challengeId;
    const next = createBiliChallengePoller({
      async poll(): Promise<AuthPollOutcome> {
        if (!ownsChallenge()) return 'inactive';
        try {
          const result = await api.pollBiliServiceChallenge!(active.challengeId);
          if (!ownsChallenge()) return 'inactive';
          challengeStatus = result.status;
          notice = undefined;
          publish();
          return result.status === 'verified' ? 'terminal' : result.status;
        } catch (error) {
          if (error instanceof HostedAPIError && error.code === 'expired') {
            if (ownsChallenge()) {
              clearChallenge();
              phase = 'idle';
              notice = { kind: 'error', message: '二维码已过期，请重新生成' };
              publish();
            }
            return 'terminal';
          }
          throw error;
        }
      },
    }, timers, (snapshot) => {
      if (!ownsChallenge()) return;
      const nextNotice = failureNotice(snapshot);
      if (!nextNotice) return;
      if (snapshot.failureKind === 'fatal') {
        clearChallenge();
        phase = 'idle';
      }
      notice = nextNotice;
      publish();
    });
    if (!ownsChallenge()) { void next.stop(); return; }
    poller = next;
    next.start();
  };

  return {
    async load(): Promise<void> {
      if (!api.biliServiceStatus || disposed || phase !== 'idle') return;
      const current = ++generation;
      try {
        const next = await api.biliServiceStatus();
        if (!complete(current)) return;
        status = next;
      } catch {
        if (!complete(current)) return;
        notice = { kind: 'error', message: '服务账号状态暂不可用，请重试' };
      }
      if (complete(current)) publish();
    },

    async check(): Promise<void> {
      if (!api.checkBiliService || disposed || phase !== 'idle') return;
      const current = ++generation;
      phase = 'checking'; notice = undefined; publish();
      try {
        const next = await api.checkBiliService();
        if (!complete(current)) return;
        status = next;
        notice = { kind: 'success', message: next.health === 'healthy' ? '检查完成，服务账号运行正常' : '检查完成，服务账号暂不可用' };
      } catch {
        if (!complete(current)) return;
        notice = { kind: 'error', message: '检查失败，旧凭据未变更，请重试' };
      }
      if (complete(current)) { phase = 'idle'; publish(); }
    },

    beginReplacement(): Promise<void> {
      if (!api.beginBiliServiceChallenge || disposed) return Promise.resolve();
      if (beginOperation) return beginOperation;
      const current = ++generation;
      phase = 'creating'; notice = undefined; publish();
      let owned!: Promise<void>;
      owned = (async () => {
        await stopPolling();
        try {
          await cleanupChallenge();
        } catch {
          if (complete(current)) publishCleanupFailure();
          throw cleanupFailure();
        }
        if (!complete(current)) return;
        let created: Challenge;
        try {
          created = await api.beginBiliServiceChallenge!();
        } catch {
          if (!complete(current)) return;
          phase = 'idle';
          notice = { kind: 'error', message: '无法创建服务账号登录二维码，请重试' };
          publish();
          return;
        }
        if (!complete(current)) {
          challenge = { ...created };
          challengeStatus = 'pending';
          await cleanupChallenge();
          return;
        }
        challenge = { ...created };
        challengeStatus = 'pending';
        phase = 'qr';
        publish();
        startPolling(current);
      })().finally(() => { if (beginOperation === owned) beginOperation = undefined; });
      beginOperation = owned;
      return owned;
    },

    enterAuthorization(): void {
      if (disposed || !challenge || challengeStatus !== 'verified' || phase !== 'qr') return;
      phase = 'authorizing';
      notice = undefined;
      void stopPolling();
      publish();
    },

    async cancelReplacement(): Promise<void> {
      if (disposed) return;
      generation++;
      const starting = beginOperation;
      phase = 'cleaning'; notice = undefined; publish();
      try {
        await stopPolling();
        if (starting) await starting;
        await cleanupChallenge();
        phase = 'idle'; notice = undefined; publish();
      } catch {
        publishCleanupFailure();
        throw cleanupFailure();
      }
    },

    async authorizeAndReplace(totp: string): Promise<void> {
      if (!api.authorizeAdminOperation || !api.replaceBiliServiceCredential || !challenge || challengeStatus !== 'verified' || phase !== 'authorizing' || disposed) return;
      const current = ++generation;
      const challengeId = challenge.challengeId;
      phase = 'authorizing'; notice = undefined; publish();
      try {
        const token = await authorizeAdminOperation(api as Pick<HostedAPI, 'authorizeAdminOperation'>, { purpose: 'bili_service_replace', target: 'global', totp });
        if (!complete(current)) return;
        phase = 'replacing'; publish();
        await api.replaceBiliServiceCredential(challengeId, token);
        if (!complete(current)) return;
        clearChallenge(); phase = 'idle'; notice = { kind: 'success', message: '服务账号已替换，正在刷新状态' }; publish();
        await this.load();
      } catch {
        if (!complete(current)) return;
        phase = 'qr';
        notice = { kind: 'error', message: '替换失败，旧服务账号仍然有效，请重试' };
        publish();
      }
    },

    async dispose(): Promise<void> {
      if (!disposed) {
        disposed = true;
        generation++;
      }
      const starting = beginOperation;
      try {
        await stopPolling();
        if (starting) await starting;
        await cleanupChallenge();
      } catch {
        throw cleanupFailure();
      }
    },
  };
}
