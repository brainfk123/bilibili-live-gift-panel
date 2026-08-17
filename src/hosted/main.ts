import './shell.css';
import { HostedAPI } from './api';
import { mountAdminView } from './admin';
import { mountAuthView } from './auth';
import { mountConfigurationView } from './configuration';
import { mountInvitationView } from './invitations';
import { mountMigrationView } from './migration';
import { mountRoomControls } from './room';
import { createHostedApplicationLifecycle, createHostedRuntimePresence, type HostedRuntimePresence } from './runtime';
import { createHostedViewHost, renderHostedShell, type HostedSession, type HostedView } from './shell';

const root = document.getElementById('hosted-app');

if (!(root instanceof HTMLElement)) {
  throw new Error('Hosted application root is missing.');
}

const viewHost = createHostedViewHost();
const applicationLifecycle = createHostedApplicationLifecycle();
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
const mountAccountView = (api: HostedAPI): HostedView => {
  const document = root.ownerDocument;
  const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel';
  const title = document.createElement('h1'); title.textContent = '主播账号';
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const configuration = document.createElement('button'); configuration.type = 'button'; configuration.textContent = '在线配置'; configuration.addEventListener('click', () => showConfiguration(api));
  const migration = document.createElement('button'); migration.type = 'button'; migration.textContent = '迁移本地配置'; migration.addEventListener('click', () => showMigration(api));
  const invitations = document.createElement('button'); invitations.type = 'button'; invitations.textContent = '我的邀请码'; invitations.addEventListener('click', () => { void viewHost.replace(() => mountInvitationView(root, api, undefined, () => returnToAccount(api), () => returnToSignedOut(api))); });
  const room = document.createElement('div');
  const roomView = mountRoomControls(room, api, ensureRuntimePresence());
  const logout = document.createElement('button'); logout.type = 'button'; logout.textContent = '退出登录'; logout.addEventListener('click', () => { void api.logout().then(() => returnToSignedOut(api)).catch(() => applicationLifecycle.run(() => { status.textContent = '退出失败，请稍后重试'; })); });
  panel.append(title, status, room, configuration, migration, invitations, logout); root.replaceChildren(panel);
  return { dispose: () => { roomView.dispose(); root.replaceChildren(); } };
};
const showAccount = (api: HostedAPI): void => { void viewHost.replace(() => mountAccountView(api)); };
const showConfiguration = (api: HostedAPI): void => { void viewHost.replace(() => mountConfigurationView(root, api, { onMigration: () => showMigration(api), onExit: () => showAccount(api) })); };
const showMigration = (api: HostedAPI): void => { void viewHost.replace(() => mountMigrationView(root, api, { onConfiguration: () => showConfiguration(api) })); };
const mountShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): HostedView => {
  renderHostedShell(root, {
    serviceStatus,
    onLogin: () => { void viewHost.replace(() => mountAuthView(root, api, {
        onSignedIn: () => returnToAccount(api),
        onRegistrationRequired: (intent) => { applicationLifecycle.run(() => { void viewHost.replace(() => mountInvitationView(root, api, intent, () => returnToAccount(api))); }); },
        onExit: () => returnToSignedOut(api),
      })); },
    onAdmin: () => { void viewHost.replace(() => mountAdminView(root, api)); },
  });
  return { dispose: () => { root.replaceChildren(); } };
};
const showShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => { disposeRuntimePresence(); void viewHost.replace(() => mountShell(api, serviceStatus)); };
const returnToAccount = (api: HostedAPI): void => { applicationLifecycle.run(() => showAccount(api)); };
const returnToSignedOut = (api: HostedAPI): void => { applicationLifecycle.run(() => showShell(api, 'ready')); };

renderHostedShell(root, { serviceStatus: 'checking', onLogin: () => undefined });
void HostedAPI.connect().then(async (api) => {
  if (!applicationLifecycle.active()) return;
  try { await api.session(); applicationLifecycle.run(() => showAccount(api)); }
  catch { applicationLifecycle.run(() => showShell(api, 'ready')); }
}).catch(() => {
  applicationLifecycle.run(() => renderHostedShell(root, { serviceStatus: 'unavailable', onLogin: () => undefined }));
});

window.addEventListener('pagehide', () => { applicationLifecycle.dispose(); disposeRuntimePresence(); void viewHost.dispose(); }, { once: true });
