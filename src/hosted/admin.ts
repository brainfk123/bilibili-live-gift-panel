import { HostedAPIError, type BiliServiceStatus, type Challenge, type HostedAPI, type RecoveryPreparation } from './api';
import { mountAdminLogin } from './admin-login';
import { mountAdminShell } from './admin/shell';
import type { AdminSection } from './admin/routes';

interface AdminLoginAPI {
  verifyRecentTOTP(totp: string): Promise<void>;
}

export function createAdminFlow(api: AdminLoginAPI) {
  return Object.freeze({
    async runWithRecentTOTP(totp: string, action: () => Promise<void>): Promise<void> {
      try { await action(); } catch (error) {
        if (!(error instanceof HostedAPIError) || error.code !== 'recent_totp_required') throw error;
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
    replace(totp: string): Promise<void> {
      if (disposed) return Promise.reject(unavailable());
      if (replaceOperation) return replaceOperation;
      if (busy) return Promise.reject(new HostedAPIError('operation_conflict', 409));
      if (!challenge) return Promise.reject(new HostedAPIError('invalid_request', 400));
      const id = challenge.challengeId; const current = ++generation; busy = true;
      const operation = (async () => {
        try { await api.replaceBiliServiceCredential(id); } catch (error) {
          if (!(error instanceof HostedAPIError) || error.code !== 'recent_totp_required') throw error;
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
  const acknowledged = new Set<'totp' | 'password' | 'archive'>();
  const publish = (): void => render({
    archiveDelivery: 'email', totpUri: preparation?.totpUri, recoveryPassword: preparation?.recoveryPassword,
    canConfirm: Boolean(preparation && acknowledged.size === 3),
    acknowledged: { totp: acknowledged.has('totp'), password: acknowledged.has('password'), archive: acknowledged.has('archive') },
    ...(error ? { error } : {}),
  });
  const clear = (): void => { generation += 1; preparation = undefined; acknowledged.clear(); publish(); };
  return Object.freeze({
    async prepare(recoveryCode: string): Promise<void> {
      const current = ++generation;
      error = undefined;
      const result = await api.prepareRecovery(recoveryCode);
      if (current !== generation) return;
      preparation = result;
      acknowledged.clear(); publish();
    },
    acknowledge(item: 'totp' | 'password' | 'archive', checked = true): void {
      if (preparation) { if (checked) acknowledged.add(item); else acknowledged.delete(item); }
      publish();
    },
    async confirm(totp: string): Promise<void> {
      if (!preparation || acknowledged.size !== 3) throw new HostedAPIError('invalid_request', 400);
      const token = preparation.handoffToken;
      try { await api.confirmRecovery(token, totp); } catch (cause) {
        error = '确认失败，请重试'; publish(); throw cause;
      }
      clear();
    },
    close: clear,
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
  let activeSection: AdminSection = 'overview';
  const secretInputs = new Set<HTMLInputElement>();
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');

  const renderDashboard = (): void => {
    if (disposed) return;
    const previousLogin = loginMount; loginMount = undefined; if (previousLogin) void previousLogin.dispose();
    const previousShell = adminShell; adminShell = undefined; if (previousShell) void previousShell.dispose();
    adminSecretFlow.close();
    adminShell = mountAdminShell(root, {
      initial: activeSection,
      mount: (section, host, navigate) => {
        activeSection = section;
        const localSecrets: HTMLInputElement[] = [];
        const title = document.createElement('h2'); title.className = 'hosted-admin-section-title';
        const intro = document.createElement('p'); intro.className = 'hosted-admin-section-intro';
        const addSecret = (input: HTMLInputElement): HTMLInputElement => { secretInputs.add(input); localSecrets.push(input); return input; };
        const recentControl = (): [HTMLLabelElement, HTMLInputElement] => {
          const [label, input] = labelledInput(document, '当前 TOTP（仅高风险操作需要）', 'password'); addSecret(input); return [label, input];
        };
        const guarded = (recent: HTMLInputElement, action: () => Promise<void>): void => {
          void loginFlow.runWithRecentTOTP(recent.value, action).then(() => { status.textContent = '操作成功'; }).catch(() => { status.textContent = '操作失败，请检查验证码与输入'; }).finally(() => { recent.value = ''; });
        };
        host.append(title, intro, status);

        if (section === 'overview') {
          title.textContent = '运行总览'; intro.textContent = '按业务域进入操作页，避免在同一长列表中误操作。';
          const grid = document.createElement('div'); grid.className = 'hosted-admin-card-grid';
          for (const [target, heading, detail] of [['accounts', '账号管理', '停用、启用、额度与例外换绑'], ['invitations', '邀请管理', '生成一次性管理员邀请码'], ['bili-service', '服务账号', '检查或替换 B 站服务凭据'], ['obs', 'OBS 凭据', '按账号签发新的 OBS 访问地址'], ['security', '安全与恢复', '恢复附件、恢复码与身份重置']] as const) {
            const card = document.createElement('button'); card.type = 'button'; card.className = 'hosted-admin-card hosted-admin-card-link'; card.setAttribute('aria-label', `进入${heading}`); const h3 = document.createElement('h3'); h3.textContent = heading; const p = document.createElement('p'); p.textContent = detail; card.append(h3, p); card.addEventListener('click', () => navigate(target)); grid.append(card);
          }
          host.append(grid);
        }

        if (section === 'accounts') {
          title.textContent = '账号'; intro.textContent = '账号状态与配额操作集中在这里；危险操作必须填写原因。';
          const [recentLabel, recent] = recentControl(); const [accountLabel, account] = labelledInput(document, '账号 ID'); account.inputMode = 'numeric';
          const [reasonLabel, reason] = labelledInput(document, '操作原因'); const [quotaLabel, quota] = labelledInput(document, '剩余额度'); quota.inputMode = 'numeric';
          const danger = document.createElement('section'); danger.className = 'hosted-admin-card hosted-admin-danger'; const dangerTitle = document.createElement('h3'); dangerTitle.textContent = '账号变更';
          const accountID = (): number => Number(account.value);
          danger.append(dangerTitle, accountLabel, reasonLabel, quotaLabel,
            button(document, '停用账号', () => guarded(recent, async () => { await accountFlow.disable(accountID(), reason.value); })),
            button(document, '启用账号', () => guarded(recent, async () => { await accountFlow.enable(accountID(), reason.value); })),
            button(document, '调整邀请码额度', () => guarded(recent, async () => { await accountFlow.adjustQuota(accountID(), Number(quota.value), reason.value); })),
          );
          host.append(recentLabel, danger);
        }

        if (section === 'invitations') {
          title.textContent = '邀请'; intro.textContent = '管理员邀请码只显示一次，请立即保存并通过可信渠道交付。';
          const [recentLabel, recent] = recentControl(); const card = document.createElement('section'); card.className = 'hosted-admin-card';
          card.append(button(document, '生成不限额度邀请码', () => guarded(recent, async () => { await adminSecretFlow.run(async () => { const generated = await api.generateInvitation(true); return { title: '邀请码仅显示一次', value: generated.code, copyLabel: '复制邀请码' }; }); })));
          host.append(recentLabel, card);
        }

        if (section === 'bili-service') {
          title.textContent = 'B 站服务账号'; intro.textContent = '用于直播间连接的独立服务账号，不应与管理员日常账号混用。';
          const [recentLabel, recent] = recentControl(); const card = document.createElement('section'); card.className = 'hosted-admin-card'; const serviceStatus = document.createElement('p'); serviceStatus.textContent = '正在读取服务账号状态…'; card.append(serviceStatus);
          void api.biliServiceStatus().then((value) => { if (!disposed) serviceStatus.textContent = biliServiceStatusText(value); }).catch(() => { if (!disposed) serviceStatus.textContent = '服务账号状态暂不可用'; });
          const serviceState = biliServiceFlow.state(); const begin = button(document, serviceState.busy ? '正在创建验证…' : '创建服务账号验证', () => { const operation = biliServiceFlow.begin(); renderDashboard(); void operation.then(renderDashboard).catch(() => { status.textContent = '无法创建服务账号验证'; renderDashboard(); }); }); begin.disabled = serviceState.busy; card.append(begin);
          if (serviceState.challenge) {
            const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = 'B 站服务账号二维码'; qr.src = serviceState.challenge.qrImage;
            const replace = button(document, serviceState.busy ? '正在替换…' : '确认替换服务账号', () => { const totp = recent.value; recent.value = ''; const operation = biliServiceFlow.replace(totp); renderDashboard(); void operation.then(() => { status.textContent = '服务账号已替换'; renderDashboard(); }).catch(() => { status.textContent = '服务账号替换失败，请检查 TOTP'; renderDashboard(); }); }); replace.disabled = serviceState.busy; card.append(qr, replace);
          }
          host.append(recentLabel, card);
        }

        if (section === 'obs') {
          title.textContent = 'OBS 凭据'; intro.textContent = '重置后旧地址立即失效，新地址只显示一次。';
          const [recentLabel, recent] = recentControl(); const [accountLabel, account] = labelledInput(document, '账号 ID'); account.inputMode = 'numeric'; const card = document.createElement('section'); card.className = 'hosted-admin-card hosted-admin-danger';
          card.append(accountLabel, button(document, '重置 OBS 凭据', () => guarded(recent, async () => { await adminSecretFlow.run(async () => { const issued = await api.issueOBSCredential(Number(account.value)); return { title: 'OBS 地址仅显示一次', value: issued.url, copyLabel: '复制 OBS 地址' }; }); })));
          host.append(recentLabel, card);
        }

        if (section === 'security') {
          title.textContent = '安全与恢复'; intro.textContent = '恢复资料属于最高敏感操作，完成后页面会清除一次性内容。';
          const [recentLabel, recent] = recentControl(); const card = document.createElement('section'); card.className = 'hosted-admin-card hosted-admin-danger';
          card.append(button(document, '发送新的加密恢复附件', () => guarded(recent, async () => { await adminSecretFlow.run(async () => { const result = await api.sendRecoveryArchive(); return { title: '附件已发送到管理员邮箱', value: result.recoveryPassword, copyLabel: '复制解密密码' }; }); })));
          host.append(recentLabel, card);
        }
        return { dispose: () => { for (const input of localSecrets) { input.value = ''; secretInputs.delete(input); } } };
      },
    });
  };

  const showOneTimeSecret = (presentation: AdminOneTimeSecretPresentation): void => {
    const dialog = document.createElement('dialog'); dialog.setAttribute('aria-modal', 'true'); dialog.setAttribute('aria-labelledby', 'admin-one-time-secret-title');
    const title = document.createElement('h2'); title.id = 'admin-one-time-secret-title'; title.textContent = presentation.title;
    const secret = document.createElement('code'); secret.textContent = presentation.value;
    const closeDOM = (): void => { presentation.value = ''; secret.textContent = ''; dialog.remove(); clearTransientSecret = undefined; };
    clearTransientSecret?.(); clearTransientSecret = closeDOM;
    const close = (): void => adminSecretFlow.close();
    dialog.append(title, secret, button(document, presentation.copyLabel, () => { void adminSecretFlow.copy(navigator.clipboard); }), button(document, '已保存并关闭', close));
    dialog.addEventListener('cancel', (event) => { event.preventDefault(); close(); }); root.firstElementChild?.append(dialog);
    if (typeof dialog.showModal === 'function') dialog.showModal(); else dialog.open = true;
  };

  adminSecretFlow = createAdminOneTimeSecretFlow((state) => {
    clearTransientSecret?.(); clearTransientSecret = undefined;
    if (state.presentation) showOneTimeSecret(state.presentation);
  });

  const renderLogin = (): void => {
    adminSecretFlow.close();
    const previousShell = adminShell; adminShell = undefined; if (previousShell) void previousShell.dispose();
    const previous = loginMount; loginMount = undefined; if (previous) void previous.dispose();
    loginMount = mountAdminLogin(root, api, { onSignedIn: renderDashboard });
  };
  renderLogin();
  return Object.freeze({ dispose: async () => {
    disposed = true;
    adminSecretFlow.dispose();
    biliServiceFlow.dispose();
    clearTransientSecret?.(); clearTransientSecret = undefined;
    for (const input of secretInputs) input.value = '';
    secretInputs.clear();
    await loginMount?.dispose(); loginMount = undefined;
    await adminShell?.dispose(); adminShell = undefined;
    await accountFlow.dispose();
    await loginFlow.dispose(); root.replaceChildren();
  } });
}
