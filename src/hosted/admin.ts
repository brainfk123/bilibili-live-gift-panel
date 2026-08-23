import { HostedAPIError, type BiliServiceStatus, type Challenge, type HostedAPI, type RecoveryPreparation } from './api';
import { mountAdminLogin } from './admin-login';
import { mountAdminShell } from './admin/shell';
import { mountAdminOverview } from './admin/overview';
import { mountAccountList } from './admin/accounts/list';
import { mountAdminInvitationView } from './admin/invitations/view';
import { createOneTimeSecretDialog } from './one-time-secret';
import type { AdminSection } from './admin/routes';
import { mountVerificationCode, type VerificationCodeControl } from './verification-code';

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

function button(document: Document, label: string, action: () => void): HTMLButtonElement {
  const result = document.createElement('button'); result.type = 'button'; result.textContent = label; result.addEventListener('click', action); return result;
}

function labelledInput(document: Document, labelText: string, type = 'text'): [HTMLLabelElement, HTMLInputElement] {
  const label = document.createElement('label'); label.textContent = labelText;
  const input = document.createElement('input'); input.type = type; input.autocomplete = 'off'; label.append(input); return [label, input];
}

export function mountAdminView(root: HTMLElement, api: HostedAPI) {
  const document = root.ownerDocument;
  const loginFlow = createAdminFlow(api);
  const biliServiceFlow = createBiliServiceFlow(api);
  const accountFlow = createAdminAccountFlow(api);
  let disposed = false;
  let adminSecretFlow: ReturnType<typeof createAdminOneTimeSecretFlow>;
  let clearTransientSecret: (() => void) | undefined;
  let loginMount: { dispose(): Promise<void> } | undefined;
  let adminShell: { dispose(): void | Promise<void> } | undefined;
  let recoveryFlow: ReturnType<typeof createAdminRecoveryFlow> | undefined;
  let recoveryCodeControl: VerificationCodeControl | undefined;
  let recoveryCodeInput: HTMLInputElement | undefined;
  let activeSection: AdminSection = 'overview';
  let sectionGeneration = 0;
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const clearRecoveryPanel = (): void => {
    recoveryCodeControl?.dispose(); recoveryCodeControl = undefined;
    if (recoveryCodeInput) recoveryCodeInput.value = '';
    recoveryCodeInput = undefined;
  };

  const renderDashboard = (): void => {
    if (disposed) return;
    const previousRecovery = recoveryFlow; recoveryFlow = undefined; clearRecoveryPanel(); previousRecovery?.dispose();
    const previousLogin = loginMount; loginMount = undefined; if (previousLogin) void previousLogin.dispose();
    const previousShell = adminShell; adminShell = undefined; if (previousShell) void previousShell.dispose();
    adminSecretFlow.close();
    adminShell = mountAdminShell(root, {
      initial: activeSection,
      mount: (section, host, navigate) => {
        activeSection = section;
        const currentGeneration = ++sectionGeneration;
        let sectionDisposed = false;
        const isCurrentSection = (): boolean => !disposed && !sectionDisposed && currentGeneration === sectionGeneration;
        const unavailable = (): HostedAPIError => new HostedAPIError('operation_failed', 0);
        const localDisposers: Array<() => void> = [];
        const title = document.createElement('h2'); title.className = 'hosted-admin-section-title';
        const intro = document.createElement('p'); intro.className = 'hosted-admin-section-intro';
        const needsRecentTOTP = (error: unknown): boolean => error instanceof HostedAPIError && error.code === 'recent_totp_required';
        const recentControl = (): { element: HTMLElement; guarded(action: () => Promise<void>, retry?: (totp: string) => Promise<void>, onSuccess?: () => void): void } => {
          const element = document.createElement('section'); element.className = 'hosted-admin-recent-totp';
          const hint = document.createElement('p'); hint.textContent = '高风险操作仅在服务端要求时才需要当前 TOTP。';
          const codeHost = document.createElement('div'); element.append(hint, codeHost);
          let control: VerificationCodeControl | undefined;
          let busy = false;
          const clearPrompt = (): void => { control?.dispose(); control = undefined; codeHost.replaceChildren(); };
          const prompt = (retry: (totp: string) => Promise<void>): void => {
            if (!isCurrentSection()) return;
            clearPrompt();
            const mounted = mountVerificationCode(codeHost, {
              label: '当前六位 TOTP（仅高风险操作需要）',
              onComplete: (totp) => { if (isCurrentSection()) { mounted.setBusy(true); run(() => retry(totp), retry, mounted); } },
            });
            control = mounted; mounted.focus();
          };
          const run = (operation: () => Promise<void>, retry: (totp: string) => Promise<void>, attempted?: VerificationCodeControl, onSuccess?: () => void): void => {
            if (!isCurrentSection() || busy) return;
            busy = true;
            void operation().then(() => {
              if (!isCurrentSection()) return;
              clearPrompt(); status.textContent = '操作成功'; onSuccess?.();
            }).catch((error) => {
              if (!isCurrentSection()) return;
              if (needsRecentTOTP(error)) prompt(retry); else status.textContent = '操作失败，请检查验证码与输入';
            }).finally(() => {
              if (!isCurrentSection()) return;
              busy = false; attempted?.clear(); attempted?.setBusy(false);
            });
          };
          localDisposers.push(clearPrompt);
          return {
            element,
            guarded: (action, retry = (totp) => loginFlow.runWithRecentTOTP(totp, action), onSuccess) => run(action, retry, undefined, onSuccess),
          };
        };
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
          title.textContent = 'B 站服务账号'; intro.textContent = '用于直播间连接的独立服务账号，不应与管理员日常账号混用。';
          const recent = recentControl(); const card = document.createElement('section'); card.className = 'hosted-admin-card'; const serviceStatus = document.createElement('p'); serviceStatus.textContent = '正在读取服务账号状态…'; card.append(serviceStatus);
          void api.biliServiceStatus().then((value) => { if (isCurrentSection()) serviceStatus.textContent = biliServiceStatusText(value); }).catch(() => { if (isCurrentSection()) serviceStatus.textContent = '服务账号状态暂不可用'; });
          const serviceState = biliServiceFlow.state(); const begin = button(document, serviceState.busy ? '正在创建验证…' : '创建服务账号验证', () => {
            if (!isCurrentSection()) return;
            begin.disabled = true;
            const operation = biliServiceFlow.begin();
            void operation.then(() => { if (isCurrentSection()) renderDashboard(); }).catch(() => { if (isCurrentSection()) { status.textContent = '无法创建服务账号验证'; renderDashboard(); } });
          }); begin.disabled = serviceState.busy; card.append(begin);
          if (serviceState.challenge) {
            const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = 'B 站服务账号二维码'; qr.src = serviceState.challenge.qrImage;
            const replace = button(document, serviceState.busy ? '正在替换…' : '确认替换服务账号', () => recent.guarded(
              () => biliServiceFlow.replace(),
              (totp) => biliServiceFlow.replace(totp),
              renderDashboard,
            )); replace.disabled = serviceState.busy; card.append(qr, replace);
          }
          host.append(recent.element, card);
        }

        if (section === 'settings') {
          title.textContent = '系统设置'; intro.textContent = '管理管理员登录、恢复资料与诊断。';
          const recent = recentControl(); const card = document.createElement('section'); card.className = 'hosted-admin-card hosted-admin-danger';
          card.append(
            button(document, '发送新的加密恢复附件', () => recent.guarded(async () => { await adminSecretFlow.run(async () => { const result = await api.sendRecoveryArchive(); if (!isCurrentSection()) throw unavailable(); return { title: '附件已发送到管理员邮箱', value: result.recoveryPassword, copyLabel: '复制解密密码' }; }); })),
            button(document, '使用恢复码重置管理员', () => openRecovery()),
          );
          host.append(recent.element, card);
        }
        return { dispose: () => { sectionDisposed = true; for (const dispose of localDisposers) dispose(); } };
      },
    });
  };

  const closeRecovery = (): void => {
    const current = recoveryFlow; recoveryFlow = undefined; clearRecoveryPanel(); current?.close();
    activeSection = 'settings'; renderDashboard();
  };

  const renderRecovery = (state: RecoveryViewState): void => {
    const flow = recoveryFlow;
    if (!flow || disposed) return;
    clearRecoveryPanel();
    const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel';
    const title = document.createElement('h1'); title.textContent = '管理员恢复';
    const recoveryStatus = document.createElement('p'); recoveryStatus.setAttribute('role', 'alert'); recoveryStatus.setAttribute('aria-live', 'assertive'); recoveryStatus.textContent = state.error ?? '';
    panel.append(title, recoveryStatus);
    if (!state.totpUri || !state.recoveryPassword) {
      const note = document.createElement('p'); note.textContent = '输入恢复码后，系统会生成新的 TOTP 与恢复资料。此流程不使用日常邮箱登录。';
      const [codeLabel, code] = labelledInput(document, '恢复码', 'password'); recoveryCodeInput = code;
      const prepare = button(document, '准备恢复', () => {
        const recoveryCode = code.value; code.value = ''; prepare.disabled = true;
        void flow.prepare(recoveryCode).catch(() => { recoveryStatus.textContent = '无法准备恢复，请检查恢复码'; }).finally(() => { code.value = ''; prepare.disabled = false; });
      });
      panel.append(note, codeLabel, prepare, button(document, '关闭并清除页面秘密', closeRecovery)); root.replaceChildren(panel);
      return;
    }
    const note = document.createElement('p'); note.textContent = '加密恢复附件已发送到管理员邮箱；请保存下面的新 TOTP、解密密码并确认已收到附件。';
    const uri = document.createElement('code'); uri.textContent = state.totpUri;
    const password = document.createElement('code'); password.textContent = state.recoveryPassword;
    panel.append(note, uri, password);
    for (const item of ['totp', 'password', 'archive'] as const) {
      const label = document.createElement('label'); const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.checked = state.acknowledged[item];
      label.append(checkbox, document.createTextNode(item === 'totp' ? '我已保存新 TOTP' : item === 'password' ? '我已保存解密密码' : '我已收到邮件附件'));
      checkbox.addEventListener('change', () => flow.acknowledge(item, checkbox.checked)); panel.append(label);
    }
    if (state.canConfirm) {
      const codeHost = document.createElement('div'); panel.append(codeHost);
      const control = mountVerificationCode(codeHost, {
        label: '新六位 TOTP',
        onComplete: (totp) => {
          control.setBusy(true);
          void flow.confirm(totp).then(() => {
            if (recoveryFlow === flow) { status.textContent = '恢复已确认'; closeRecovery(); }
          }).catch(() => undefined).finally(() => { control.clear(); control.setBusy(false); });
        },
      });
      recoveryCodeControl = control; control.focus();
    } else {
      const hint = document.createElement('p'); hint.textContent = '完成三项确认后可输入新 TOTP。'; panel.append(hint);
    }
    panel.append(button(document, '关闭并清除页面秘密', closeRecovery)); root.replaceChildren(panel);
  };

  const openRecovery = (): void => {
    const previousShell = adminShell; adminShell = undefined; if (previousShell) void previousShell.dispose();
    adminSecretFlow.close(); clearRecoveryPanel();
    const previous = recoveryFlow; recoveryFlow = undefined; previous?.dispose();
    recoveryFlow = createAdminRecoveryFlow(api, renderRecovery);
    renderRecovery({ archiveDelivery: 'email', canConfirm: false, acknowledged: { totp: false, password: false, archive: false } });
  };

  const showOneTimeSecret = (presentation: AdminOneTimeSecretPresentation): void => {
    const close = (): void => adminSecretFlow.close();
    const dialog = createOneTimeSecretDialog(document, {
      titleID: 'admin-one-time-secret-title', title: presentation.title, value: presentation.value, copyLabel: presentation.copyLabel,
      onCopy: () => { void adminSecretFlow.copy(navigator.clipboard); }, onClose: close,
    });
    const secret = dialog.children[1] as HTMLElement;
    const closeDOM = (): void => { presentation.value = ''; secret.textContent = ''; dialog.remove(); clearTransientSecret = undefined; };
    clearTransientSecret?.(); clearTransientSecret = closeDOM;
    root.firstElementChild?.append(dialog);
    if (typeof dialog.showModal === 'function') dialog.showModal(); else dialog.open = true;
  };

  adminSecretFlow = createAdminOneTimeSecretFlow((state) => {
    clearTransientSecret?.(); clearTransientSecret = undefined;
    if (state.presentation) showOneTimeSecret(state.presentation);
  });

  const renderLogin = (): void => {
    const previousRecovery = recoveryFlow; recoveryFlow = undefined; clearRecoveryPanel(); previousRecovery?.dispose();
    adminSecretFlow.close();
    const previousShell = adminShell; adminShell = undefined; if (previousShell) void previousShell.dispose();
    const previous = loginMount; loginMount = undefined; if (previous) void previous.dispose();
    loginMount = mountAdminLogin(root, api, { onSignedIn: renderDashboard });
  };
  renderLogin();
  return Object.freeze({ dispose: async () => {
    disposed = true;
    const previousRecovery = recoveryFlow; recoveryFlow = undefined; clearRecoveryPanel(); previousRecovery?.dispose();
    adminSecretFlow.dispose();
    biliServiceFlow.dispose();
    clearTransientSecret?.(); clearTransientSecret = undefined;
    await loginMount?.dispose(); loginMount = undefined;
    await adminShell?.dispose(); adminShell = undefined;
    await accountFlow.dispose();
    await loginFlow.dispose(); root.replaceChildren();
  } });
}
