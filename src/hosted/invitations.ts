import type { GeneratedInvitation, InvitationList, InvitationRecord } from './api';
import type { HostedAPI } from './api';

interface InvitationAPI {
  listInvitations?(): Promise<InvitationList>;
  generateInvitation?(): Promise<GeneratedInvitation>;
  revokeInvitation?(id: number): Promise<void>;
  redeemInvitation?(code: string, registrationIntent: string): Promise<void>;
}

interface ClipboardPort { writeText(value: string): Promise<void>; }

export interface InvitationViewState {
  remainingQuota?: number;
  invitations: InvitationRecord[];
  revealedCode?: string;
  revealedHint?: string;
  generating?: boolean;
}

export function createInvitationFlow(api: InvitationAPI, render: (state: InvitationViewState) => void, clipboard: ClipboardPort, registrationIntent?: string) {
  let fullCode = '';
  let state: InvitationViewState = { invitations: [] };
  let disposed = false;
  let generating = false;
  const publish = (): void => render({ ...state, invitations: [...state.invitations] });
  return Object.freeze({
    async refresh(): Promise<void> {
      if (!api.listInvitations) return;
      const result = await api.listInvitations();
      if (disposed) return;
      state = { remainingQuota: result.remainingQuota, invitations: [...result.invitations] };
      publish();
    },
    async generate(): Promise<void> {
      if (!api.generateInvitation || disposed || generating || fullCode) return;
      generating = true; state = { ...state, generating: true }; publish();
      try {
        const generated = await api.generateInvitation();
        if (disposed) return;
        fullCode = generated.code; generating = false;
        const record: InvitationRecord = {
          id: generated.id, codeHint: generated.codeHint, status: generated.status,
          createdAt: generated.createdAt, expiresAt: generated.expiresAt,
          ...(generated.revokedAt ? { revokedAt: generated.revokedAt } : {}),
          ...(generated.usedAt ? { usedAt: generated.usedAt } : {}),
        };
        state = { ...state, generating: false, invitations: [record, ...state.invitations.filter((item) => item.id !== record.id)], remainingQuota: generated.remainingQuota ?? state.remainingQuota, revealedCode: fullCode, revealedHint: generated.codeHint };
        publish();
      } catch (error) {
        generating = false;
        if (!disposed) { state = { ...state, generating: false }; publish(); }
        throw error;
      }
    },
    async copy(): Promise<void> { if (fullCode) await clipboard.writeText(fullCode); },
    closeReveal(): void {
      fullCode = '';
      const { revealedCode: _revealedCode, ...masked } = state;
      state = { ...masked, revealedHint: state.revealedHint };
      publish();
    },
    async revoke(id: number): Promise<void> { if (api.revokeInvitation) await api.revokeInvitation(id); await this.refresh(); },
    async redeem(code: string): Promise<void> {
      if (!api.redeemInvitation || !registrationIntent) return;
      await api.redeemInvitation(code, registrationIntent);
      registrationIntent = undefined;
    },
    dispose(): void { disposed = true; fullCode = ''; registrationIntent = undefined; state = { invitations: [] }; },
  });
}

export function mountInvitationView(root: HTMLElement, api: HostedAPI, registrationIntent?: string, onRegistered?: () => void, onLogout?: () => void) {
  const document = root.ownerDocument;
  let disposed = false;
  const clipboard: ClipboardPort = {
    writeText: async (value) => {
      if (!globalThis.navigator?.clipboard) throw new Error('Clipboard is unavailable.');
      await globalThis.navigator.clipboard.writeText(value);
    },
  };
  const render = (state: InvitationViewState): void => {
    if (disposed) return;
    const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel';
    const title = document.createElement('h1'); title.textContent = registrationIntent ? '使用邀请码完成注册' : '我的邀请码';
    const live = document.createElement('p'); live.setAttribute('role', 'status'); live.setAttribute('aria-live', 'polite');
    panel.append(title, live);
    let modal: HTMLDialogElement | undefined;
    if (registrationIntent) {
      const label = document.createElement('label'); label.textContent = '邀请码';
      const code = document.createElement('input'); code.type = 'text'; code.autocomplete = 'off'; code.required = true; label.append(code);
      const redeem = document.createElement('button'); redeem.type = 'button'; redeem.textContent = '注册并进入账号';
      redeem.addEventListener('click', () => {
        const value = code.value; code.value = '';
        void flow.redeem(value).then(() => {
          if (disposed) return;
          disposed = true;
          registrationIntent = undefined;
          flow.dispose();
          root.replaceChildren();
          onRegistered?.();
        }).catch(() => { live.textContent = '邀请码无效或已失效'; });
      });
      panel.append(label, redeem);
    } else {
      const quota = document.createElement('p'); quota.textContent = `剩余邀请码额度：${state.remainingQuota ?? '—'}`;
      const generate = document.createElement('button'); generate.type = 'button'; generate.textContent = state.generating ? '正在生成…' : '生成邀请码'; generate.disabled = state.remainingQuota === 0 || state.generating === true || Boolean(state.revealedCode);
      generate.addEventListener('click', () => { void flow.generate().catch(() => { live.textContent = '暂时无法生成邀请码'; }); });
      const list = document.createElement('ul'); list.className = 'hosted-list';
      for (const invitation of state.invitations) {
        const row = document.createElement('li'); row.textContent = `${invitation.codeHint} · ${invitation.status}`;
        if (invitation.status === 'active') {
          const revoke = document.createElement('button'); revoke.type = 'button'; revoke.textContent = '撤销';
          revoke.addEventListener('click', () => { void flow.revoke(invitation.id).catch(() => { live.textContent = '撤销失败'; }); });
          row.append(revoke);
        }
        list.append(row);
      }
      panel.append(quota, generate, list);
      const logout = document.createElement('button'); logout.type = 'button'; logout.textContent = '退出登录';
      logout.addEventListener('click', () => { void api.logout().then(() => { flow.dispose(); onLogout?.(); }).catch(() => { live.textContent = '退出失败，请稍后重试'; }); });
      panel.append(logout);
    }
    if (state.revealedCode) {
      const dialog = document.createElement('dialog'); dialog.setAttribute('aria-labelledby', 'invite-secret-title');
      const heading = document.createElement('h2'); heading.id = 'invite-secret-title'; heading.textContent = '邀请码仅显示一次';
      const secret = document.createElement('code'); secret.textContent = state.revealedCode;
      const copy = document.createElement('button'); copy.type = 'button'; copy.textContent = '复制邀请码'; copy.addEventListener('click', () => { void flow.copy(); });
      const close = document.createElement('button'); close.type = 'button'; close.textContent = '已保存并关闭'; close.addEventListener('click', () => flow.closeReveal());
      dialog.addEventListener('cancel', (event) => { event.preventDefault(); flow.closeReveal(); });
      dialog.append(heading, secret, copy, close); panel.append(dialog); modal = dialog;
    }
    root.replaceChildren(panel);
    if (modal) {
      if (typeof modal.showModal === 'function') modal.showModal();
      else modal.open = true;
    }
  };
  const flow = createInvitationFlow(api, render, clipboard, registrationIntent);
  render({ invitations: [] });
  const ready = registrationIntent ? Promise.resolve() : flow.refresh().catch(() => undefined);
  return Object.freeze({ ready, dispose: () => { disposed = true; registrationIntent = undefined; flow.dispose(); root.replaceChildren(); } });
}
