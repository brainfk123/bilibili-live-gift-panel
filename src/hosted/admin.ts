import { HostedAPIError, type BiliServiceStatus, type Challenge, type HostedAPI, type RecoveryPreparation } from './api';
import { mountAdminLogin } from './admin-login';
import { mountAdminShell } from './admin/shell';
import { mountAdminOverview } from './admin/overview';
import { mountAccountList } from './admin/accounts/list';
import { mountAdminInvitationView } from './admin/invitations/view';
import { mountBiliServiceView } from './admin/bili-service';
import { mountAdminSettingsView } from './admin/settings';
import type { AdminSection } from './admin/routes';

interface AdminLoginAPI {
  verifyRecentTOTP(totp: string): Promise<void>;
}

export function createAdminFlow(api: AdminLoginAPI) {
  return Object.freeze({
    async runWithRecentTOTP(totp: string | undefined, action: () => Promise<void>): Promise<void> {
      try { await action(); } catch (error) {
        if (!(error instanceof HostedAPIError) || error.code !== 'recent_totp_required') throw error;
        if (!totp) throw error;
        await api.verifyRecentTOTP(totp);
        await action();
      }
    },
    async dispose(): Promise<void> {},
  });
}

interface BiliServiceAPI {
  beginBiliServiceChallenge(): Promise<Challenge>;
  replaceBiliServiceCredential(challengeId: string): Promise<void>;
  verifyRecentTOTP(totp: string): Promise<void>;
}

export function biliServiceStatusText(status: BiliServiceStatus): string {
  if (status.health === 'healthy') return `版本 ${status.version}；最近验证 ${status.lastVerifiedAt}`;
  return status.health === 'missing' ? '服务账号未配置' : '服务账号状态暂不可用';
}

export function createBiliServiceFlow(api: BiliServiceAPI) {
  let challenge: Challenge | undefined;
  let busy = false;
  let disposed = false;
  let generation = 0;
  let beginOperation: Promise<Challenge> | undefined;
  let replaceOperation: Promise<void> | undefined;
  const unavailable = (): HostedAPIError => new HostedAPIError('operation_failed', 0);
  return Object.freeze({
    begin(): Promise<Challenge> {
      if (disposed) return Promise.reject(unavailable());
      if (beginOperation) return beginOperation;
      if (busy) return Promise.reject(new HostedAPIError('operation_conflict', 409));
      const current = ++generation; busy = true;
      const operation = api.beginBiliServiceChallenge().then((created) => {
        if (disposed || current !== generation) throw unavailable();
        challenge = created; return created;
      }).finally(() => { if (!disposed && current === generation) busy = false; if (beginOperation === operation) beginOperation = undefined; });
      beginOperation = operation;
      return operation;
    },
    replace(totp?: string): Promise<void> {
      if (disposed) return Promise.reject(unavailable());
      if (replaceOperation) return replaceOperation;
      if (busy) return Promise.reject(new HostedAPIError('operation_conflict', 409));
      if (!challenge) return Promise.reject(new HostedAPIError('invalid_request', 400));
      const id = challenge.challengeId; const current = ++generation; busy = true;
      const operation = (async () => {
        try { await api.replaceBiliServiceCredential(id); } catch (error) {
          if (!(error instanceof HostedAPIError) || error.code !== 'recent_totp_required') throw error;
          if (!totp) throw error;
          await api.verifyRecentTOTP(totp);
          if (disposed || current !== generation) throw unavailable();
          await api.replaceBiliServiceCredential(id);
        }
        if (disposed || current !== generation) throw unavailable();
        challenge = undefined;
      })().finally(() => { if (!disposed && current === generation) busy = false; if (replaceOperation === operation) replaceOperation = undefined; });
      replaceOperation = operation;
      return operation;
    },
    state(): { challenge?: Challenge; busy: boolean } { return { busy, ...(challenge ? { challenge: { ...challenge } } : {}) }; },
    dispose(): void { disposed = true; generation += 1; busy = false; challenge = undefined; },
  });
}

interface AdminAccountAPI {
  disableAccount(accountId: number, reason: string): Promise<unknown>;
  enableAccount(accountId: number, reason: string): Promise<unknown>;
  adjustQuota(accountId: number, remainingQuota: number, reason: string): Promise<void>;
}

export function createAdminAccountFlow(api: AdminAccountAPI) {
  return Object.freeze({
    async disable(accountId: number, reason: string): Promise<void> { await api.disableAccount(accountId, reason); },
    async enable(accountId: number, reason: string): Promise<void> { await api.enableAccount(accountId, reason); },
    async adjustQuota(accountId: number, remainingQuota: number, reason: string): Promise<void> { await api.adjustQuota(accountId, remainingQuota, reason); },
    async dispose(): Promise<void> {},
  });
}

interface RecoveryAPI {
  prepareRecovery(recoveryCode: string): Promise<RecoveryPreparation>;
  confirmRecovery(handoffToken: string, totp: string): Promise<void>;
}

export interface RecoveryViewState {
  archiveDelivery: 'email';
  totpUri?: string;
  recoveryPassword?: string;
  canConfirm: boolean;
  acknowledged: Record<'totp' | 'password' | 'archive', boolean>;
  error?: string;
}

export function createAdminRecoveryFlow(api: RecoveryAPI, render: (state: RecoveryViewState) => void) {
  let preparation: RecoveryPreparation | undefined;
  let generation = 0;
  let error: string | undefined;
  let disposed = false;
  let prepareOperation: Promise<void> | undefined;
  let confirmOperation: Promise<void> | undefined;
  const acknowledged = new Set<'totp' | 'password' | 'archive'>();
  const publish = (): void => render({
    archiveDelivery: 'email', totpUri: preparation?.totpUri, recoveryPassword: preparation?.recoveryPassword,
    canConfirm: Boolean(preparation && acknowledged.size === 3),
    acknowledged: { totp: acknowledged.has('totp'), password: acknowledged.has('password'), archive: acknowledged.has('archive') },
    ...(error ? { error } : {}),
  });
  const clear = (): void => { generation += 1; preparation = undefined; error = undefined; acknowledged.clear(); publish(); };
  const unavailable = (): HostedAPIError => new HostedAPIError('operation_failed', 0);
  return Object.freeze({
    prepare(recoveryCode: string): Promise<void> {
      if (disposed) return Promise.reject(unavailable());
      if (prepareOperation) return prepareOperation;
      if (confirmOperation) return Promise.reject(new HostedAPIError('operation_conflict', 409));
      const current = ++generation;
      error = undefined;
      const operation = api.prepareRecovery(recoveryCode).then((result) => {
        if (disposed || current !== generation) return;
        preparation = result;
        acknowledged.clear(); publish();
      }).finally(() => { if (prepareOperation === operation) prepareOperation = undefined; });
      prepareOperation = operation;
      return operation;
    },
    acknowledge(item: 'totp' | 'password' | 'archive', checked = true): void {
      if (preparation) { if (checked) acknowledged.add(item); else acknowledged.delete(item); }
      publish();
    },
    confirm(totp: string): Promise<void> {
      if (disposed) return Promise.reject(unavailable());
      if (confirmOperation) return confirmOperation;
      if (prepareOperation) return Promise.reject(new HostedAPIError('operation_conflict', 409));
      if (!preparation || acknowledged.size !== 3) throw new HostedAPIError('invalid_request', 400);
      const token = preparation.handoffToken;
      const current = ++generation;
      const operation = api.confirmRecovery(token, totp).then(() => {
        if (disposed || current !== generation) return;
        clear();
      }).catch((cause) => {
        if (!disposed && current === generation) { error = '确认失败，请重试'; publish(); }
        throw cause;
      }).finally(() => { if (confirmOperation === operation) confirmOperation = undefined; });
      confirmOperation = operation;
      return operation;
    },
    close: clear,
    dispose(): void { if (!disposed) { disposed = true; clear(); } },
  });
}

export interface AdminOneTimeSecretPresentation {
  title: string;
  copyLabel: string;
  value: string;
}

export interface AdminOneTimeSecretState {
  inFlight: boolean;
  presentation?: AdminOneTimeSecretPresentation;
}

export function createAdminOneTimeSecretFlow(render: (state: AdminOneTimeSecretState) => void) {
  let presentation: AdminOneTimeSecretPresentation | undefined;
  let inFlight = false;
  let disposed = false;
  let generation = 0;
  const clearPresentation = (): void => {
    if (presentation) presentation.value = '';
    presentation = undefined;
  };
  const publish = (): void => render({ inFlight, ...(presentation ? { presentation: { ...presentation } } : {}) });
  return Object.freeze({
    async run(load: () => Promise<AdminOneTimeSecretPresentation>): Promise<void> {
      if (disposed || inFlight) return;
      clearPresentation(); inFlight = true; const current = ++generation; publish();
      let result: AdminOneTimeSecretPresentation;
      try { result = await load(); } catch (error) {
        if (current === generation) { inFlight = false; publish(); }
        throw error;
      }
      if (disposed || current !== generation) { result.value = ''; return; }
      inFlight = false; presentation = result; publish();
    },
    async copy(clipboard: { writeText(value: string): Promise<void> }): Promise<void> {
      if (presentation?.value) await clipboard.writeText(presentation.value);
    },
    close(): void { generation += 1; inFlight = false; clearPresentation(); publish(); },
    dispose(): void { disposed = true; generation += 1; inFlight = false; clearPresentation(); publish(); },
  });
}

export function mountAdminView(root: HTMLElement, api: HostedAPI) {
  const document = root.ownerDocument;
  let disposed = false;
  let loginMount: { dispose(): Promise<void> } | undefined;
  let adminShell: { dispose(): void | Promise<void> } | undefined;
  let activeSection: AdminSection = 'overview';
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const renderDashboard = (): void => {
    if (disposed) return;
    const previousLogin = loginMount; loginMount = undefined; if (previousLogin) void previousLogin.dispose();
    const previousShell = adminShell; adminShell = undefined; if (previousShell) void previousShell.dispose();
    adminShell = mountAdminShell(root, {
      initial: activeSection,
      mount: (section, host, navigate) => {
        activeSection = section;
        const localDisposers: Array<() => void> = [];
        const title = document.createElement('h2'); title.className = 'hosted-admin-section-title';
        const intro = document.createElement('p'); intro.className = 'hosted-admin-section-intro';
        host.append(title, intro, status);

        if (section === 'overview') {
          title.textContent = '运营总览'; intro.textContent = '先看需要处理的事项，再进入对应资源。';
          const view=mountAdminOverview(host,api,navigate);localDisposers.push(()=>{void view.dispose()});
        }

        if (section === 'accounts') {
          title.textContent = '主播账号'; intro.textContent = '搜索、筛选和批量管理账号；OBS 设置位于账号详情。';
          const view=mountAccountList(host,api);localDisposers.push(()=>{void view.dispose()});
        }

        if (section === 'invitations') {
          title.textContent = '邀请码'; intro.textContent = '搜索、排序、复制、分享或作废邀请码；创建设置按需展开。';
          const view=mountAdminInvitationView(host,api);localDisposers.push(()=>{void view.dispose()});
        }

        if (section === 'bili-service') {
          title.textContent = 'B站服务账号'; intro.textContent = '查看健康状态、主动检查或按三步流程安全替换。';
          const view=mountBiliServiceView(host,api);localDisposers.push(()=>{void view.dispose()});
        }

        if (section === 'settings') {
          title.textContent = '系统设置'; intro.textContent = '查看登录状态和恢复资料；诊断信息默认收起。';
          const view=mountAdminSettingsView(host,api,renderLogin);localDisposers.push(()=>{void view.dispose()});
        }
        return { dispose: () => { for (const dispose of localDisposers) dispose(); } };
      },
    });
  };

  const renderLogin = (): void => {
    const previousShell = adminShell; adminShell = undefined; if (previousShell) void previousShell.dispose();
    const previous = loginMount; loginMount = undefined; if (previous) void previous.dispose();
    loginMount = mountAdminLogin(root, api, { onSignedIn: renderDashboard });
  };
  renderLogin();
  return Object.freeze({ dispose: async () => {
    disposed = true;
    await loginMount?.dispose(); loginMount = undefined;
    await adminShell?.dispose(); adminShell = undefined;
    root.replaceChildren();
  } });
}
