import './shell.css';
import { HostedAPI } from './api';
import { mountAdminView } from './admin';
import { mountAuthView } from './auth';
import { mountConfigurationView } from './configuration';
import { mountInvitationView } from './invitations';
import { mountRoomControls } from './room';
import { createHostedApplicationLifecycle, createHostedRuntimePresence, type HostedRuntimePresence } from './runtime';
import { createHostedViewHost, isAdminEntryHash, renderHostedShell, type HostedSession, type HostedView } from './shell';
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
const mountAccountView = (api: HostedAPI, accountScope: string): HostedView => {
  const document = root.ownerDocument;
  const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel';
  const title = document.createElement('h1'); title.textContent = '主播账号';
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const prompt = document.createElement('div'); let storage: Storage | undefined; try { storage = document.defaultView?.localStorage; } catch { storage = undefined; }
  const promptView = mountMigrationPrompt(prompt, storage, accountScope, { onOpen: () => showSettings(api, accountScope) });
  const configuration = document.createElement('button'); configuration.type = 'button'; configuration.textContent = '在线配置'; configuration.addEventListener('click', () => showConfiguration(api, accountScope));
  const settings = document.createElement('button'); settings.type = 'button'; settings.textContent = '设置'; settings.addEventListener('click', () => showSettings(api, accountScope));
  const invitations = document.createElement('button'); invitations.type = 'button'; invitations.textContent = '我的邀请码'; invitations.addEventListener('click', () => { observeViewOperation(viewHost.replace(() => mountInvitationView(root, api, undefined, () => returnToAccount(api), () => returnToSignedOut(api)))); });
  const room = document.createElement('div');
  const roomView = mountRoomControls(room, api, ensureRuntimePresence());
  const logout = document.createElement('button'); logout.type = 'button'; logout.textContent = '退出登录'; logout.addEventListener('click', () => { void api.logout().then(() => returnToSignedOut(api)).catch(() => applicationLifecycle.run(() => { status.textContent = '退出失败，请稍后重试'; })); });
  panel.append(title, status, prompt, room, configuration, settings, invitations, logout); root.replaceChildren(panel);
  return { dispose: () => { promptView.dispose(); roomView.dispose(); root.replaceChildren(); } };
};
const showAccount = (api: HostedAPI, accountScope: string): void => { observeViewOperation(viewHost.replace(() => mountAccountView(api, accountScope))); };
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
  try { const { accountScope } = await api.session(); applicationLifecycle.run(() => showAccount(api, accountScope)); }
  catch { applicationLifecycle.run(() => showSignedOut(api, 'ready')); }
}).catch(() => {
  applicationLifecycle.run(() => renderHostedShell(root, { serviceStatus: 'unavailable', onLogin: () => undefined }));
});

window.addEventListener('pagehide', () => { applicationLifecycle.dispose(); disposeRuntimePresence(); observeViewOperation(viewHost.dispose()); }, { once: true });
