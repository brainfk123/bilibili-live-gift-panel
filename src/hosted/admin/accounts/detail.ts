import type { AdminAccountDetail, HostedAPI } from '../../api';
import type { HostedView } from '../../shell';
import { runAdminAction } from '../ui/async-action';
import { mountAdminNotice } from '../ui/notice';

type AccountDetailAPI = Pick<HostedAPI,
  | 'adminAccount'
  | 'updateAdminRoom'
  | 'adjustQuota'
  | 'disableAccount'
  | 'enableAccount'
  | 'issueOBSCredential'
>;

type FocusTarget = HTMLElement | (() => HTMLElement | undefined);

export function mountAccountDetail(
  host: HTMLElement,
  api: AccountDetailAPI,
  id: number,
  onChanged: () => void | Promise<void>,
  returnFocus?: FocusTarget,
): HostedView {
  const document = host.ownerDocument;
  let disposed = false;
  let closed = false;
  let changed = false;
  let activeRequests = 0;
  let detail: AdminAccountDetail | undefined;
  let firstRender = true;
  const controls = new Set<HTMLButtonElement>();

  host.className = 'hosted-admin-account-detail';
  host.setAttribute('role', 'presentation');
  const panel = document.createElement('section');
  panel.className = 'hosted-admin-account-detail-panel';
  panel.setAttribute('role', 'dialog');
  panel.setAttribute('aria-modal', 'true');
  panel.setAttribute('aria-labelledby', `hosted-admin-account-${id}-title`);
  const feedback = document.createElement('div');
  feedback.className = 'hosted-admin-feedback';
  const notice = mountAdminNotice(feedback);
  const body = document.createElement('div');
  body.className = 'hosted-admin-detail-body';
  panel.append(feedback, body);
  host.replaceChildren(panel);

  const focusTarget = (): HTMLElement | undefined => typeof returnFocus === 'function' ? returnFocus() : returnFocus;
  const restoreFocus = (): void => { focusTarget()?.focus(); };

  const removeHandlers = (): void => {
    host.removeEventListener('click', onHostClick);
    host.removeEventListener('keydown', onHostKeydown);
  };

  const close = (force = false): void => {
    if (closed || (!force && activeRequests > 0)) return;
    closed = true;
    removeHandlers();
    notice.dispose();
    host.replaceChildren();
    if (force || !changed) {
      if (!force) restoreFocus();
      return;
    }
    void Promise.resolve(onChanged()).then(restoreFocus, restoreFocus);
  };

  const setControlsDisabled = (disabled: boolean): void => {
    for (const control of controls) control.disabled = disabled;
  };

  const run = (
    button: HTMLButtonElement,
    labels: { idle: string; busy: string },
    operation: () => Promise<void>,
    success: string,
  ): void => {
    if (disposed || closed || activeRequests > 0) return;
    const focusLabel = button.getAttribute('aria-label');
    const focusText = button.textContent;
    activeRequests = 1;
    panel.setAttribute('aria-busy', 'true');
    setControlsDisabled(true);
    void runAdminAction(button, labels, operation).then((outcome) => {
      activeRequests = 0;
      panel.removeAttribute('aria-busy');
      if (disposed || closed) return;
      if (outcome === 'failure') {
        setControlsDisabled(false);
        notice.show('error', `${labels.idle}失败，请重试`, () => run(button, labels, operation, success));
        return;
      }
      changed = true;
      render();
      const nextFocus = [...controls].find((control) => focusLabel
        ? control.getAttribute('aria-label') === focusLabel
        : control.textContent === focusText);
      (nextFocus ?? [...controls][0])?.focus();
      notice.show('success', success);
    });
  };

  const register = (button: HTMLButtonElement): HTMLButtonElement => {
    controls.add(button);
    return button;
  };

  const render = (): void => {
    if (!detail || disposed || closed) return;
    controls.clear();

    const header = document.createElement('header');
    header.className = 'hosted-admin-detail-header';
    const headingGroup = document.createElement('div');
    const title = document.createElement('h3');
    title.id = `hosted-admin-account-${detail.id}-title`;
    title.textContent = `主播账号 #${detail.id}`;
    const state = document.createElement('p');
    state.textContent = `${detail.status === 'active' ? '启用' : '停用'} · ${detail.roomId ? `直播间 ${detail.roomId}` : '等待设置直播间'}`;
    headingGroup.append(title, state);
    const closeButton = register(document.createElement('button'));
    closeButton.type = 'button';
    closeButton.className = 'hosted-admin-detail-close';
    closeButton.dataset.variant = 'secondary';
    closeButton.setAttribute('aria-label', '关闭账号详情');
    closeButton.textContent = '×';
    closeButton.addEventListener('click', () => close());
    header.append(headingGroup, closeButton);

    const basic = document.createElement('section');
    basic.className = 'hosted-admin-card hosted-admin-detail-card hosted-admin-detail-basic';
    const basicHeading = document.createElement('h4');
    basicHeading.textContent = '基础设置';

    const roomField = document.createElement('label');
    roomField.textContent = '直播间 ID';
    const roomRow = document.createElement('div');
    roomRow.className = 'hosted-admin-detail-action-row';
    const room = document.createElement('input');
    room.value = detail.roomId ?? '';
    room.placeholder = '尚未设置';
    room.inputMode = 'numeric';
    const saveRoom = register(document.createElement('button'));
    saveRoom.type = 'button';
    saveRoom.textContent = '保存';
    saveRoom.setAttribute('aria-label', '保存直播间');
    saveRoom.addEventListener('click', () => {
      run(saveRoom, { idle: '保存', busy: '保存中…' }, async () => {
        detail = await api.updateAdminRoom(id, room.value);
      }, '直播间已保存');
    });
    roomRow.append(room, saveRoom);
    roomField.append(roomRow);

    const quotaField = document.createElement('label');
    quotaField.textContent = '邀请码额度';
    const quotaRow = document.createElement('div');
    quotaRow.className = 'hosted-admin-detail-action-row';
    const quota = document.createElement('input');
    quota.value = String(detail.invitationQuota);
    quota.inputMode = 'numeric';
    const saveQuota = register(document.createElement('button'));
    saveQuota.type = 'button';
    saveQuota.textContent = '保存';
    saveQuota.setAttribute('aria-label', '保存邀请码额度');
    saveQuota.addEventListener('click', () => {
      const remaining = Number(quota.value);
      if (!Number.isSafeInteger(remaining) || remaining < 0) {
        notice.show('warning', '请输入有效的邀请码额度');
        return;
      }
      const reason = globalThis.prompt('请输入调整原因')?.trim();
      if (!reason) return;
      run(saveQuota, { idle: '保存', busy: '保存中…' }, async () => {
        await api.adjustQuota(id, remaining, reason);
        detail = await api.adminAccount(id);
      }, '邀请码额度已保存');
    });
    quotaRow.append(quota, saveQuota);
    quotaField.append(quotaRow);
    basic.append(basicHeading, roomField, quotaField);

    const obs = document.createElement('section');
    obs.className = 'hosted-admin-card hosted-admin-detail-card';
    const obsHeading = document.createElement('h4');
    obsHeading.textContent = 'OBS';
    const obsState = document.createElement('p');
    obsState.textContent = detail.obsUrl ? 'OBS 地址已创建' : '尚未创建 OBS 地址';
    const obsActions = document.createElement('div');
    obsActions.className = 'hosted-admin-detail-card-actions';
    const issue = register(document.createElement('button'));
    issue.type = 'button';
    issue.textContent = detail.obsUrl ? '重发 OBS 地址' : '创建 OBS 地址';
    issue.addEventListener('click', () => {
      if (detail?.obsUrl && !globalThis.confirm('重发后旧地址将立即失效，确认继续？')) return;
      const idle = issue.textContent;
      run(issue, { idle, busy: '处理中…' }, async () => {
        await api.issueOBSCredential(id);
        detail = await api.adminAccount(id);
      }, 'OBS 地址已更新');
    });
    obsActions.append(issue);
    obs.append(obsHeading, obsState, obsActions);

    const accountState = document.createElement('section');
    accountState.className = 'hosted-admin-card hosted-admin-detail-card';
    const stateHeading = document.createElement('h4');
    stateHeading.textContent = '账号状态';
    const stateHelp = document.createElement('p');
    stateHelp.textContent = '停用后主播无法继续兑换邀请码，已有资料仍会保留。';
    const stateActions = document.createElement('div');
    stateActions.className = 'hosted-admin-detail-card-actions';
    const toggle = register(document.createElement('button'));
    toggle.type = 'button';
    toggle.dataset.variant = detail.status === 'active' ? 'danger' : 'primary';
    toggle.textContent = detail.status === 'active' ? '停用账号' : '启用账号';
    toggle.addEventListener('click', () => {
      if (!globalThis.confirm(`确认${toggle.textContent}？`)) return;
      const idle = toggle.textContent;
      const wasActive = detail?.status === 'active';
      run(toggle, { idle, busy: '处理中…' }, async () => {
        if (wasActive) await api.disableAccount(id, 'administrator action');
        else await api.enableAccount(id, 'administrator action');
        detail = await api.adminAccount(id);
      }, wasActive ? '账号已停用' : '账号已启用');
    });
    stateActions.append(toggle);
    accountState.append(stateHeading, stateHelp, stateActions);

    const events = document.createElement('section');
    events.className = 'hosted-admin-detail-events';
    if (detail.recentEvents.length > 0) {
      const eventsHeading = document.createElement('h4');
      eventsHeading.textContent = '最近活动';
      events.append(eventsHeading);
      for (const event of detail.recentEvents) {
        const row = document.createElement('p');
        row.textContent = event.text;
        events.append(row);
      }
    }

    body.replaceChildren(header, basic, obs, accountState, events);
    if (firstRender) {
      firstRender = false;
      closeButton.focus();
    }
  };

  function onHostClick(event: MouseEvent): void {
    if (event.target === host) close();
  }

  function onHostKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== 'Tab') return;
    const tabbable = [...panel.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])')];
    if (tabbable.length === 0) return;
    const first = tabbable[0];
    const last = tabbable.at(-1)!;
    if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    } else if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    }
  }

  host.addEventListener('click', onHostClick);
  host.addEventListener('keydown', onHostKeydown);

  void api.adminAccount(id).then((value) => {
    if (disposed || closed) return;
    detail = value;
    render();
  }).catch(() => {
    if (!disposed && !closed) body.textContent = '账号详情暂不可用';
  });

  return {
    dispose() {
      disposed = true;
      close(true);
    },
  };
}
