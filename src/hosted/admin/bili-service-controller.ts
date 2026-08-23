import type { BiliServiceStatus, Challenge, HostedAPI } from '../api';
import { authorizeAdminOperation } from './operation-authorization';

export type BiliServicePhase = 'idle' | 'checking' | 'qr' | 'authorizing' | 'replacing';
export type BiliServiceNotice = { kind: 'success' | 'error'; message: string };

export interface BiliServiceSnapshot {
  phase: BiliServicePhase;
  status?: BiliServiceStatus;
  challenge?: Challenge;
  notice?: BiliServiceNotice;
}

type BiliServiceAPI = Pick<HostedAPI, 'biliServiceStatus' | 'checkBiliService' | 'beginBiliServiceChallenge' | 'replaceBiliServiceCredential' | 'authorizeAdminOperation'>;

export interface BiliServiceController {
  load(): Promise<void>;
  check(): Promise<void>;
  beginReplacement(): Promise<void>;
  enterAuthorization(): void;
  cancelReplacement(): void;
  authorizeAndReplace(totp: string): Promise<void>;
  dispose(): void;
}

export function createBiliServiceController(api: Partial<BiliServiceAPI>, render: (snapshot: BiliServiceSnapshot) => void): BiliServiceController {
  let phase: BiliServicePhase = 'idle';
  let status: BiliServiceStatus | undefined;
  let challenge: Challenge | undefined;
  let notice: BiliServiceNotice | undefined;
  let generation = 0;
  let disposed = false;
  const publish = (): void => render({ phase, ...(status ? { status } : {}), ...(challenge ? { challenge: { ...challenge } } : {}), ...(notice ? { notice: { ...notice } } : {}) });
  const complete = (current: number): boolean => !disposed && current === generation;
  const clearChallenge = (): void => { challenge = undefined; };

  return {
    async load(): Promise<void> {
      if (!api.biliServiceStatus || disposed) return;
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
      if (!api.checkBiliService || disposed) return;
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

    async beginReplacement(): Promise<void> {
      if (!api.beginBiliServiceChallenge || disposed) return;
      const current = ++generation;
      phase = 'idle'; notice = undefined; clearChallenge(); publish();
      try {
        const next = await api.beginBiliServiceChallenge();
        if (!complete(current)) return;
        challenge = { ...next };
        phase = 'qr';
      } catch {
        if (!complete(current)) return;
        notice = { kind: 'error', message: '无法创建服务账号登录二维码，请重试' };
      }
      if (complete(current)) publish();
    },

    enterAuthorization(): void {
      if (disposed || !challenge || phase !== 'qr') return;
      phase = 'authorizing';
      notice = undefined;
      publish();
    },

    cancelReplacement(): void {
      if (disposed) return;
      generation++;
      phase = 'idle'; clearChallenge(); notice = undefined; publish();
    },

    async authorizeAndReplace(totp: string): Promise<void> {
      if (!api.authorizeAdminOperation || !api.replaceBiliServiceCredential || !challenge || disposed) return;
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

    dispose(): void { disposed = true; generation++; clearChallenge(); },
  };
}
