import './shell.css';
import { HostedAPI } from './api';
import { mountAdminView } from './admin';
import { mountAuthView } from './auth';
import { mountConfigurationView } from './configuration';
import { mountInvitationView } from './invitations';
import { mountRoomControls } from './room';
import { createHostedApplicationLifecycle, createHostedRuntimePresence, type HostedRuntimePresence } from './runtime';
import { createHostedViewHost, isAdminEntryHash, renderHostedShell, type HostedSession, type HostedView } from './shell';
import { hostedUserPageSearch, parseHostedUserPage, type HostedUserPage } from './user/routes';
import { createHostedUserShell, renderHostedUserPageState } from './user/shell';
import { mountMigrationPrompt, mountMigrationSettingsView } from './user/settings/migration-center';

const root = document.getElementById('hosted-app');

if (!(root instanceof HTMLElement)) {
  throw new Error('Hosted application root is missing.');
}

const viewHost = createHostedViewHost();
const applicationLifecycle = createHostedApplicationLifecycle();
const observeViewOperation = (operation: Promise<void>): void => { void operation.catch(() => undefined); };
let runtimePresence: HostedRuntimePresence | undefined;
const ensureRuntimePresence = (): HostedRuntimePresence => {
  runtimePresence ??= createHostedRuntimePresence({
    createEventSource: (path) => new EventSource(path),
    setTimer: (callback, delay) => window.setTimeout(callback, delay),
    clearTimer: (timer) => window.clearTimeout(timer as number),
    random: Math.random,
  });
  return runtimePresence;
};
const disposeRuntimePresence = (): void => { runtimePresence?.dispose(); runtimePresence = undefined; };
const workspaceCard = (host: HTMLElement, titleText: string, descriptionText: string, actionText: string, action: () => void): HostedView => {
  const document = host.ownerDocument;
  const card = document.createElement('section'); card.className = 'hosted-user-card';
  const title = document.createElement('h2'); title.textContent = titleText;
  const description = document.createElement('p'); description.textContent = descriptionText;
  const button = document.createElement('button'); button.type = 'button'; button.textContent = actionText; button.addEventListener('click', action);
  card.append(title, description, button); host.replaceChildren(card);
  return { dispose: () => { host.replaceChildren(); } };
};
const mountAccountWorkspace = (page: HostedUserPage, host: HTMLElement, api: HostedAPI, accountScope: string): HostedView => {
  if (page === 'overview') {
    const document = host.ownerDocument;
    const prompt = document.createElement('div');
    let storage: Storage | undefined;
    try { storage = document.defaultView?.localStorage; } catch { storage = undefined; }
    const promptView = mountMigrationPrompt(prompt, storage, accountScope, { onOpen: () => showSettings(api, accountScope) });
    const room = document.createElement('section'); room.className = 'hosted-user-card hosted-panel';
    const roomView = mountRoomControls(room, api, ensureRuntimePresence());
    host.append(prompt, room);
    return { dispose: () => { promptView.dispose(); roomView.dispose(); host.replaceChildren(); } };
  }
  if (page === 'attributes') {
    return workspaceCard(host, '属性玩法', '使用与 EXE 一致的权威配置管理属性、礼物规则与定时规则。', '打开在线配置', () => showConfiguration(api, accountScope));
  }
  const copy: Record<Exclude<HostedUserPage, 'overview' | 'attributes'>, readonly [string, string]> = {
    activities: ['暂无活动会话', '后续阶段会在这里接入活动运行状态与阶段结果。'],
    targets: ['暂无礼物目标', '后续阶段会在这里接入目标面板与完成进度。'],
    obs: ['OBS 面板尚未接入', '后续阶段会在这里管理在线输出定义与浏览器源。'],
    data: ['数据中心尚未接入', '后续阶段会在这里展示场次、趋势和观众贡献。'],
  };
  const [title, description] = copy[page];
  renderHostedUserPageState(host, { kind: 'empty', title, description });
  return { dispose: () => { host.replaceChildren(); } };
};
const mountAccountView = (api: HostedAPI, accountScope: string, initialPage: HostedUserPage): HostedView => {
  let shell!: ReturnType<typeof createHostedUserShell>;
  shell = createHostedUserShell(root, {
    initialPage,
    experience: 'simple',
    configurationId: accountScope,
    onPageChange: (page) => {
      const search = hostedUserPageSearch(window.location.search, page);
      window.history.pushState(null, '', `${window.location.pathname}${search}${window.location.hash}`);
    },
    onSettings: () => showSettings(api, accountScope),
    onInvitations: () => { observeViewOperation(viewHost.replace(() => mountInvitationView(root, api, undefined, () => returnToAccount(api), () => returnToSignedOut(api)))); },
    onLogout: () => { void api.logout().then(() => returnToSignedOut(api)).catch(() => applicationLifecycle.run(() => { shell.announce('退出失败，请稍后重试'); })); },
    mount: (page, host) => mountAccountWorkspace(page, host, api, accountScope),
  });
  return shell;
};
const showAccount = (api: HostedAPI, accountScope: string): void => { observeViewOperation(viewHost.replace(() => mountAccountView(api, accountScope, parseHostedUserPage(window.location.search)))); };
const showConfiguration = (api: HostedAPI, accountScope: string): void => { observeViewOperation(viewHost.replace(() => mountConfigurationView(root, api, { onMigration: () => showSettings(api, accountScope), onExit: () => showAccount(api, accountScope) }))); };
const showSettings = (api: HostedAPI, accountScope: string): void => { observeViewOperation(viewHost.replace(() => mountMigrationSettingsView(root, api, { onExit: () => showAccount(api, accountScope) }))); };
const mountShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): HostedView => {
  renderHostedShell(root, {
    serviceStatus,
    onLogin: () => { observeViewOperation(viewHost.replace(() => mountAuthView(root, api, {
        onSignedIn: () => returnToAccount(api),
        onRegistrationRequired: (intent) => { applicationLifecycle.run(() => { observeViewOperation(viewHost.replace(() => mountInvitationView(root, api, intent, () => returnToAccount(api)))); }); },
        onExit: () => returnToSignedOut(api),
      }))); },
  });
  return { dispose: () => { root.replaceChildren(); } };
};
const showAdmin = (api: HostedAPI): void => { disposeRuntimePresence(); observeViewOperation(viewHost.replace(() => mountAdminView(root, api))); };
const showSignedOut = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => {
  if (isAdminEntryHash(window.location.hash)) showAdmin(api);
  else showShell(api, serviceStatus);
};
const showShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => { disposeRuntimePresence(); observeViewOperation(viewHost.replace(() => mountShell(api, serviceStatus))); };
const returnToAccount = (api: HostedAPI): void => { void api.session().then(({ accountScope }) => applicationLifecycle.run(() => showAccount(api, accountScope))).catch(() => returnToSignedOut(api)); };
const returnToSignedOut = (api: HostedAPI): void => { applicationLifecycle.run(() => showSignedOut(api, 'ready')); };

renderHostedShell(root, { serviceStatus: 'checking', onLogin: () => undefined });
void HostedAPI.connect().then(async (api) => {
  if (!applicationLifecycle.active()) return;
  window.addEventListener('hashchange', () => {
    if (!applicationLifecycle.active()) return;
    applicationLifecycle.run(() => showSignedOut(api, 'ready'));
  });
  window.addEventListener('popstate', () => {
    if (!applicationLifecycle.active()) return;
    if (isAdminEntryHash(window.location.hash)) applicationLifecycle.run(() => showAdmin(api));
    else returnToAccount(api);
  });
  try { const { accountScope } = await api.session(); applicationLifecycle.run(() => showAccount(api, accountScope)); }
  catch { applicationLifecycle.run(() => showSignedOut(api, 'ready')); }
}).catch(() => {
  applicationLifecycle.run(() => renderHostedShell(root, { serviceStatus: 'unavailable', onLogin: () => undefined }));
});

window.addEventListener('pagehide', (event) => {
  if (event.persisted) return;
  applicationLifecycle.dispose();
  disposeRuntimePresence();
  observeViewOperation(viewHost.dispose());
});
