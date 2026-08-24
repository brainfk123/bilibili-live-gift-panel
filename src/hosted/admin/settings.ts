import type {
  AdminDeviceSession,
  AdminLoginEvent,
  AdminSettings,
  HostedAPI,
} from '../api';
import type { HostedView } from '../shell';
import { runAdminAction } from './ui/async-action';
import { mountAdminNotice } from './ui/notice';

type SettingsAPI = Pick<HostedAPI,
  | 'adminSettings'
  | 'adminSessions'
  | 'revokeAdminSession'
  | 'revokeOtherAdminSessions'
  | 'adminLoginEvents'
  | 'adminEvents'
  | 'adminDiagnostics'
  | 'adminLogout'
>;

function localTime(value: string): string {
  return new Date(value).toLocaleString('zh-CN', { hour12: false });
}

export function mountAdminSettingsView(
  host: HTMLElement,
  api: SettingsAPI,
  onLogout: () => void,
): HostedView {
  const document = host.ownerDocument;
  let disposed = false;
  let settings: AdminSettings | undefined;
  let sessions: AdminDeviceSession[] = [];
  let loginEvents: AdminLoginEvent[] = [];

  host.className = 'hosted-admin-settings';

  const feedback = document.createElement('div');
  feedback.className = 'hosted-admin-feedback';
  const notice = mountAdminNotice(feedback);

  const profile = document.createElement('section');
  profile.className = 'hosted-admin-card hosted-admin-settings-profile';

  const devices = document.createElement('section');
  devices.className = 'hosted-admin-card hosted-admin-device-card';

  const logins = document.createElement('section');
  logins.className = 'hosted-admin-card hosted-admin-login-card';

  const advanced = document.createElement('details');
  advanced.className = 'hosted-admin-settings-advanced';
  const advancedLabel = document.createElement('summary');
  advancedLabel.textContent = '高级信息';
  const advancedFeedback = document.createElement('div');
  advancedFeedback.className = 'hosted-admin-feedback';
  const advancedNotice = mountAdminNotice(advancedFeedback);
  const advancedContent = document.createElement('div');
  advanced.append(advancedLabel, advancedFeedback, advancedContent);

  const footer = document.createElement('div');
  footer.className = 'hosted-admin-settings-footer';
  const logout = document.createElement('button');
  logout.type = 'button';
  logout.dataset.variant = 'danger';
  logout.textContent = '退出管理员登录';
  logout.addEventListener('click', () => {
    void runAdminAction(logout, { idle: '退出管理员登录', busy: '正在退出…' }, async () => {
      await api.adminLogout();
      onLogout();
    }).then((outcome) => {
      if (outcome === 'failure') notice.show('error', '退出登录失败，请重试');
    });
  });
  footer.append(logout);

  host.append(feedback, profile, devices, logins, advanced, footer);

  const renderProfile = (): void => {
    if (!settings) return;
    const heading = document.createElement('h3');
    heading.textContent = '管理员账号';
    const email = document.createElement('p');
    email.textContent = `邮箱 · ${settings.maskedEmail}`;
    const session = document.createElement('p');
    session.textContent = `当前设备保持登录至 ${localTime(settings.sessionExpiresAt)} · 最长 30 天`;
    const security = document.createElement('p');
    security.textContent = `高风险操作 TOTP · ${settings.totpEnabled ? '已启用' : '未启用'}`;
    profile.replaceChildren(heading, email, session, security);
  };

  const revokeOne = (button: HTMLButtonElement, id: string): void => {
    void runAdminAction(button, { idle: '退出此设备', busy: '正在退出…' }, async () => {
      await api.revokeAdminSession(id);
    }).then((outcome) => {
      if (disposed) return;
      if (outcome === 'failure') {
        notice.show('error', '设备退出失败，请重试', () => revokeOne(button, id));
        return;
      }
      sessions = sessions.filter((session) => session.id !== id);
      notice.show('success', '设备已退出');
      renderDevices();
    });
  };

  const revokeOthers = (button: HTMLButtonElement): void => {
    const count = sessions.filter((session) => !session.current).length;
    if (count === 0 || !globalThis.confirm(`确认退出其他 ${count} 台设备？`)) return;
    void runAdminAction(button, { idle: '退出其他设备', busy: '正在退出…' }, async () => {
      await api.revokeOtherAdminSessions();
    }).then((outcome) => {
      if (disposed) return;
      if (outcome === 'failure') {
        notice.show('error', '退出其他设备失败，请重试', () => revokeOthers(button));
        return;
      }
      sessions = sessions.filter((session) => session.current);
      notice.show('success', `已退出其他 ${count} 台设备`);
      renderDevices();
    });
  };

  const renderDevices = (): void => {
    const headingRow = document.createElement('div');
    headingRow.className = 'hosted-admin-card-heading';
    const heading = document.createElement('h3');
    heading.textContent = '已授权设备';
    const count = document.createElement('span');
    count.textContent = `${sessions.length} 台`;
    headingRow.append(heading, count);

    const list = document.createElement('div');
    list.className = 'hosted-admin-device-list';
    for (const session of sessions) {
      const row = document.createElement('article');
      row.className = `hosted-admin-device-row${session.current ? ' is-current' : ''}`;
      const summary = document.createElement('div');
      const label = document.createElement('strong');
      label.textContent = session.deviceLabel;
      const meta = document.createElement('span');
      meta.textContent = `${session.clientNetwork} · 首次登录 ${localTime(session.createdAt)} · 最近活动 ${localTime(session.lastSeenAt)} · 会话到期 ${localTime(session.expiresAt)}`;
      summary.append(label, meta);
      row.append(summary);
      if (session.current) {
        const badge = document.createElement('span');
        badge.className = 'hosted-admin-current-badge';
        badge.textContent = '当前';
        row.append(badge);
      } else {
        const revoke = document.createElement('button');
        revoke.type = 'button';
        revoke.dataset.variant = 'danger-outline';
        revoke.textContent = '退出此设备';
        revoke.addEventListener('click', () => revokeOne(revoke, session.id));
        row.append(revoke);
      }
      list.append(row);
    }

    const actions = document.createElement('div');
    actions.className = 'hosted-admin-device-actions';
    if (sessions.some((session) => !session.current)) {
      const revoke = document.createElement('button');
      revoke.type = 'button';
      revoke.dataset.variant = 'danger-outline';
      revoke.textContent = '退出其他设备';
      revoke.addEventListener('click', () => revokeOthers(revoke));
      actions.append(revoke);
    }
    devices.replaceChildren(headingRow, list, actions);
  };

  const renderLogins = (): void => {
    const heading = document.createElement('h3');
    heading.textContent = '最近登录记录';
    const list = document.createElement('div');
    list.className = 'hosted-admin-login-list';
    for (const event of loginEvents) {
      const row = document.createElement('article');
      row.className = `hosted-admin-login-row is-${event.result}`;
      const result = document.createElement('strong');
      result.textContent = event.result === 'success' ? '登录成功' : '登录失败';
      const meta = document.createElement('span');
      meta.textContent = `${localTime(event.occurredAt)} · ${event.deviceLabel} · ${event.clientNetwork}`;
      row.append(result, meta);
      list.append(row);
    }
    logins.replaceChildren(heading, list);
  };

  let advancedLoaded = false;
  let advancedLoading = false;
  const loadAdvanced = (): void => {
    if (disposed || advancedLoaded || advancedLoading) return;
    advancedLoading = true;
    advancedNotice.clear();
    advancedContent.textContent = '正在读取高级信息…';
    void Promise.all([api.adminEvents(), api.adminDiagnostics()]).then(([events, diagnostics]) => {
      if (disposed) return;
      advancedLoaded = true;
      advancedNotice.clear();
      advancedContent.replaceChildren();
      const diagnostic = document.createElement('p');
      diagnostic.textContent = `数据库：${diagnostics.database} · B站服务：${diagnostics.biliService} · 检查于 ${localTime(diagnostics.checkedAt)}`;
      advancedContent.append(diagnostic);
      for (const event of events) {
        const row = document.createElement('p');
        row.textContent = event.text;
        advancedContent.append(row);
      }
    }).catch(() => {
      if (!disposed) {
        advancedContent.textContent = '高级信息暂不可用';
        advancedNotice.show('error', '高级信息加载失败，请重试', loadAdvanced);
      }
    }).finally(() => {
      advancedLoading = false;
    });
  };
  advanced.addEventListener('toggle', () => {
    if (advanced.open) loadAdvanced();
  });

  let loadGeneration = 0;
  const load = (): void => {
    const current = ++loadGeneration;
    notice.clear();
    void Promise.all([api.adminSettings(), api.adminSessions(), api.adminLoginEvents()]).then((values) => {
      if (disposed || current !== loadGeneration) return;
      [settings, sessions, loginEvents] = values;
      renderProfile();
      renderDevices();
      renderLogins();
    }).catch(() => {
      if (!disposed && current === loadGeneration) notice.show('error', '系统设置加载失败，请重试', load);
    });
  };
  load();

  return {
    dispose() {
      disposed = true;
      loadGeneration += 1;
      notice.dispose();
      advancedNotice.dispose();
      advancedContent.replaceChildren();
    },
  };
}
