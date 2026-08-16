import { HostedAPIError, type Challenge, type HostedAPI, type RecoveryPreparation } from './api';

interface AdminLoginAPI {
  beginAdminProof(): Promise<Challenge>;
  cancelAdminProof(id: string): Promise<void>;
  adminLogin(challengeId: string, totp: string): Promise<void>;
  verifyRecentTOTP(totp: string): Promise<void>;
}

export function createAdminFlow(api: AdminLoginAPI) {
  let proof: Challenge | undefined;
  let generation = 0;
  let disposed = false;
  return Object.freeze({
    async beginProof(): Promise<Challenge> {
      if (proof) await api.cancelAdminProof(proof.challengeId);
      proof = undefined;
      const current = ++generation;
      const created = await api.beginAdminProof();
      if (disposed || current !== generation) { await api.cancelAdminProof(created.challengeId); return created; }
      proof = created; return proof;
    },
    async login(totp: string): Promise<void> {
      if (!proof) throw new HostedAPIError('invalid_request', 400);
      const id = proof.challengeId;
      try { await api.adminLogin(id, totp); proof = undefined; } catch (error) {
        if (!(error instanceof HostedAPIError && error.code === 'verification_pending')) proof = undefined;
        throw error;
      }
    },
    async runWithRecentTOTP(totp: string, action: () => Promise<void>): Promise<void> {
      try { await action(); } catch (error) {
        if (!(error instanceof HostedAPIError) || error.code !== 'recent_totp_required') throw error;
        await api.verifyRecentTOTP(totp);
        await action();
      }
    },
    async dispose(): Promise<void> { disposed = true; generation += 1; const current = proof; proof = undefined; if (current) await api.cancelAdminProof(current.challengeId); },
  });
}

interface AdminAccountAPI {
  beginAdminProof(): Promise<Challenge>;
  cancelAdminProof(id: string): Promise<void>;
  disableAccount(accountId: number, reason: string): Promise<unknown>;
  enableAccount(accountId: number, reason: string): Promise<unknown>;
  adjustQuota(accountId: number, remainingQuota: number, reason: string): Promise<void>;
  rebindAccount(accountId: number, challengeId: string, reason: string): Promise<unknown>;
}

export function createAdminAccountFlow(api: AdminAccountAPI) {
  let rebindProof: Challenge | undefined;
  let generation = 0;
  let disposed = false;
  return Object.freeze({
    async disable(accountId: number, reason: string): Promise<void> { await api.disableAccount(accountId, reason); },
    async enable(accountId: number, reason: string): Promise<void> { await api.enableAccount(accountId, reason); },
    async adjustQuota(accountId: number, remainingQuota: number, reason: string): Promise<void> { await api.adjustQuota(accountId, remainingQuota, reason); },
    async beginRebind(): Promise<Challenge> {
      if (rebindProof) await api.cancelAdminProof(rebindProof.challengeId);
      rebindProof = undefined;
      const current = ++generation;
      const created = await api.beginAdminProof();
      if (disposed || current !== generation) { await api.cancelAdminProof(created.challengeId); return created; }
      rebindProof = created; return rebindProof;
    },
    async rebind(accountId: number, reason: string): Promise<void> {
      if (!rebindProof || !reason.trim()) throw new HostedAPIError('invalid_request', 400);
      await api.rebindAccount(accountId, rebindProof.challengeId, reason);
      rebindProof = undefined;
    },
    async dispose(): Promise<void> { disposed = true; generation += 1; const current = rebindProof; rebindProof = undefined; if (current) await api.cancelAdminProof(current.challengeId); },
  });
}

interface RecoveryAPI {
  prepareRecovery(challengeId: string, recoveryCode: string): Promise<RecoveryPreparation>;
  confirmRecovery(handoffToken: string, totp: string): Promise<void>;
}

export interface RecoveryViewState {
  archiveDelivery: 'email';
  totpUri?: string;
  recoveryPassword?: string;
  canConfirm: boolean;
  acknowledged: Record<'totp' | 'password' | 'archive', boolean>;
}

export function createAdminRecoveryFlow(api: RecoveryAPI, render: (state: RecoveryViewState) => void) {
  let preparation: RecoveryPreparation | undefined;
  let generation = 0;
  const acknowledged = new Set<'totp' | 'password' | 'archive'>();
  const publish = (): void => render({
    archiveDelivery: 'email', totpUri: preparation?.totpUri, recoveryPassword: preparation?.recoveryPassword,
    canConfirm: Boolean(preparation && acknowledged.size === 3),
    acknowledged: { totp: acknowledged.has('totp'), password: acknowledged.has('password'), archive: acknowledged.has('archive') },
  });
  const clear = (): void => { generation += 1; preparation = undefined; acknowledged.clear(); publish(); };
  return Object.freeze({
    async prepare(challengeId: string, recoveryCode: string): Promise<void> {
      const current = ++generation;
      const result = await api.prepareRecovery(challengeId, recoveryCode);
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
      await api.confirmRecovery(token, totp);
      clear();
    },
    close: clear,
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
  const accountFlow = createAdminAccountFlow(api);
  let disposed = false;
  let recoveryProof: Challenge | undefined;
  let recoveryProofGeneration = 0;
  let rebindProof: Challenge | undefined;
  let recoveryFlow: ReturnType<typeof createAdminRecoveryFlow> | undefined;
  let clearTransientSecret: (() => void) | undefined;
  const secretInputs = new Set<HTMLInputElement>();
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');

  const renderDashboard = (): void => {
    if (disposed) return;
    clearTransientSecret?.(); clearTransientSecret = undefined;
    const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel';
    const title = document.createElement('h1'); title.textContent = '管理员控制台'; panel.append(title, status);
    const [recentLabel, recent] = labelledInput(document, '当前 TOTP（高风险操作需要）', 'password'); secretInputs.add(recent); panel.append(recentLabel);
    const [accountLabel, account] = labelledInput(document, '账号 ID'); account.inputMode = 'numeric';
    const [reasonLabel, reason] = labelledInput(document, '操作原因');
    const [quotaLabel, quota] = labelledInput(document, '剩余额度'); quota.inputMode = 'numeric';
    const accountID = (): number => Number(account.value);
    const guarded = (action: () => Promise<void>): void => {
      void loginFlow.runWithRecentTOTP(recent.value, action).then(() => { recent.value = ''; status.textContent = '操作成功'; }).catch(() => { status.textContent = '操作失败，请检查 TOTP 与输入'; });
    };
    panel.append(accountLabel, reasonLabel,
      button(document, '停用账号', () => guarded(async () => { await accountFlow.disable(accountID(), reason.value); })),
      button(document, '启用账号', () => guarded(async () => { await accountFlow.enable(accountID(), reason.value); })),
      quotaLabel, button(document, '调整邀请码额度', () => guarded(async () => { await accountFlow.adjustQuota(accountID(), Number(quota.value), reason.value); })),
    );
    const rebindGroup = document.createElement('section'); const rebindTitle = document.createElement('h2'); rebindTitle.textContent = '例外换绑'; rebindGroup.append(rebindTitle);
    rebindGroup.append(button(document, '创建新的 B 站身份验证', () => {
      void (async () => {
        const created = await accountFlow.beginRebind();
        if (disposed) return;
        rebindProof = created; renderDashboard();
      })().catch(() => { status.textContent = '无法创建验证'; });
    }));
    if (rebindProof) {
      const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = '换绑身份验证二维码'; qr.src = rebindProof.qrImage;
      rebindGroup.append(qr, button(document, '确认换绑', () => guarded(async () => {
        if (!rebindProof || !reason.value.trim()) throw new HostedAPIError('invalid_request', 400);
        await accountFlow.rebind(accountID(), reason.value); rebindProof = undefined;
      })));
    }
    panel.append(rebindGroup);

    const invitation = document.createElement('section'); const invitationTitle = document.createElement('h2'); invitationTitle.textContent = '管理员邀请码'; invitation.append(invitationTitle);
    invitation.append(button(document, '生成不限额度邀请码', () => guarded(async () => {
      const generated = await api.generateInvitation(true); showOneTimeSecret('邀请码仅显示一次', generated.code, '复制邀请码');
    }))); panel.append(invitation);

    const recovery = document.createElement('section'); const recoveryTitle = document.createElement('h2'); recoveryTitle.textContent = '管理员恢复'; recovery.append(recoveryTitle);
    recovery.append(button(document, '发送新的加密恢复附件', () => guarded(async () => {
      const result = await api.sendRecoveryArchive(); showOneTimeSecret('附件已发送到管理员邮箱', result.recoveryPassword, '复制解密密码');
    })));
    const [codeLabel, oldCode] = labelledInput(document, '旧恢复码', 'password'); secretInputs.add(oldCode); recovery.append(codeLabel);
    recovery.append(button(document, '创建恢复用 B 站验证', () => {
      void (async () => {
        if (recoveryProof) await api.cancelAdminProof(recoveryProof.challengeId);
        recoveryProof = undefined;
        const generation = ++recoveryProofGeneration;
        const created = await api.beginAdminProof();
        if (disposed || generation !== recoveryProofGeneration) { await api.cancelAdminProof(created.challengeId); return; }
        recoveryProof = created; renderDashboard();
      })().catch(() => { status.textContent = '无法创建验证'; });
    }));
    if (recoveryProof) {
      const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = '管理员恢复二维码'; qr.src = recoveryProof.qrImage; recovery.append(qr);
      recovery.append(button(document, '准备恢复或重试取回交接', () => {
        const proof = recoveryProof; if (!proof) return;
        recoveryFlow ??= createAdminRecoveryFlow(api, renderRecovery);
        void recoveryFlow.prepare(proof.challengeId, oldCode.value).then(() => { recoveryProof = undefined; }).catch(() => { status.textContent = '验证待确认或恢复失败，可用同一恢复码和新二维码重试'; });
        oldCode.value = '';
      }));
    }
    panel.append(recovery); root.replaceChildren(panel);
  };

  const showOneTimeSecret = (titleText: string, value: string, copyLabel: string): void => {
    const dialog = document.createElement('dialog'); dialog.setAttribute('aria-modal', 'true');
    const title = document.createElement('h2'); title.textContent = titleText; const secret = document.createElement('code'); secret.textContent = value;
    const close = (): void => { value = ''; secret.textContent = ''; dialog.remove(); clearTransientSecret = undefined; };
    clearTransientSecret = close;
    dialog.append(title, secret, button(document, copyLabel, () => { void navigator.clipboard.writeText(value); }), button(document, '已保存并关闭', close));
    dialog.addEventListener('cancel', (event) => { event.preventDefault(); close(); }); root.firstElementChild?.append(dialog);
    if (typeof dialog.showModal === 'function') dialog.showModal(); else dialog.open = true;
  };

  const renderRecovery = (state: RecoveryViewState): void => {
    clearTransientSecret?.(); clearTransientSecret = undefined;
    if (!state.totpUri || !state.recoveryPassword) { renderDashboard(); return; }
    const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel';
    const title = document.createElement('h1'); title.textContent = '保存新的管理员恢复资料';
    const note = document.createElement('p'); note.textContent = '加密恢复附件已发送到管理员邮箱，网页只显示独立解密密码。';
    const uri = document.createElement('code'); uri.textContent = state.totpUri; const password = document.createElement('code'); password.textContent = state.recoveryPassword;
    const acknowledgements = ['totp', 'password', 'archive'] as const;
    panel.append(title, note, uri, password);
    for (const item of acknowledgements) {
      const label = document.createElement('label'); const checkbox = document.createElement('input'); checkbox.type = 'checkbox';
      checkbox.checked = state.acknowledged[item];
      label.append(checkbox, document.createTextNode(item === 'totp' ? '我已保存新 TOTP' : item === 'password' ? '我已保存解密密码' : '我已收到邮件附件'));
      checkbox.addEventListener('change', () => recoveryFlow?.acknowledge(item, checkbox.checked)); panel.append(label);
    }
    const [totpLabel, totp] = labelledInput(document, '新 TOTP', 'password'); secretInputs.add(totp); const confirm = button(document, '确认恢复', () => {
      const value = totp.value; totp.value = '';
      void recoveryFlow?.confirm(value).then(() => { recoveryFlow = undefined; renderLogin(); }).catch(() => { status.textContent = '确认失败'; });
    }); confirm.disabled = !state.canConfirm;
    panel.append(totpLabel, confirm, button(document, '关闭并清除页面秘密', () => { recoveryFlow?.close(); recoveryFlow = undefined; renderLogin(); })); root.replaceChildren(panel);
  };

  const renderLogin = (): void => {
    clearTransientSecret?.(); clearTransientSecret = undefined;
    const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel'; const title = document.createElement('h1'); title.textContent = '管理员登录';
    const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = '管理员 B 站登录二维码'; const [totpLabel, totp] = labelledInput(document, 'TOTP', 'password'); secretInputs.add(totp);
    panel.append(title, status, qr, button(document, '创建 B 站验证二维码', () => { void loginFlow.beginProof().then((proof) => { qr.src = proof.qrImage; status.textContent = '扫码后输入 TOTP'; }); }), totpLabel,
      button(document, '登录管理员控制台', () => {
        const value = totp.value; totp.value = '';
        void loginFlow.login(value).then(renderDashboard).catch((error) => {
          if (error instanceof HostedAPIError && error.code === 'verification_pending') status.textContent = '等待 B 站扫码确认，请稍后再提交';
          else { status.textContent = '登录失败'; renderLogin(); }
        });
      }));
    root.replaceChildren(panel);
  };
  renderLogin();
  return Object.freeze({ dispose: async () => {
    disposed = true; recoveryProofGeneration += 1; recoveryFlow?.close(); recoveryFlow = undefined;
    clearTransientSecret?.(); clearTransientSecret = undefined;
    for (const input of secretInputs) input.value = '';
    secretInputs.clear();
    if (recoveryProof) await api.cancelAdminProof(recoveryProof.challengeId);
    await accountFlow.dispose();
    recoveryProof = undefined; rebindProof = undefined; await loginFlow.dispose(); root.replaceChildren();
  } });
}
