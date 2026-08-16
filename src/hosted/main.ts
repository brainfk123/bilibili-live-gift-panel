import './shell.css';
import { HostedAPI } from './api';
import { mountAdminView } from './admin';
import { mountAuthView } from './auth';
import { mountConfigurationView } from './configuration';
import { mountInvitationView } from './invitations';
import { mountMigrationView } from './migration';
import { createHostedViewHost, renderHostedShell, type HostedSession, type HostedView } from './shell';

const root = document.getElementById('hosted-app');

if (!(root instanceof HTMLElement)) {
  throw new Error('Hosted application root is missing.');
}

const viewHost = createHostedViewHost();
const mountAccountView = (api: HostedAPI): HostedView => {
  const document = root.ownerDocument;
  const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel';
  const title = document.createElement('h1'); title.textContent = '主播账号';
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const configuration = document.createElement('button'); configuration.type = 'button'; configuration.textContent = '在线配置'; configuration.addEventListener('click', () => showConfiguration(api));
  const migration = document.createElement('button'); migration.type = 'button'; migration.textContent = '迁移本地配置'; migration.addEventListener('click', () => showMigration(api));
  const invitations = document.createElement('button'); invitations.type = 'button'; invitations.textContent = '我的邀请码'; invitations.addEventListener('click', () => { void viewHost.replace(() => mountInvitationView(root, api, undefined, () => showAccount(api), () => showAccount(api))); });
  const logout = document.createElement('button'); logout.type = 'button'; logout.textContent = '退出登录'; logout.addEventListener('click', () => { void api.logout().then(() => showShell(api, 'ready')).catch(() => { status.textContent = '退出失败，请稍后重试'; }); });
  panel.append(title, status, configuration, migration, invitations, logout); root.replaceChildren(panel);
  return { dispose: () => { root.replaceChildren(); } };
};
const showAccount = (api: HostedAPI): void => { void viewHost.replace(() => mountAccountView(api)); };
const showConfiguration = (api: HostedAPI): void => { void viewHost.replace(() => mountConfigurationView(root, api, { onMigration: () => showMigration(api), onExit: () => showAccount(api) })); };
const showMigration = (api: HostedAPI): void => { void viewHost.replace(() => mountMigrationView(root, api, { onConfiguration: () => showConfiguration(api) })); };
const mountShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): HostedView => {
  renderHostedShell(root, {
    serviceStatus,
    onLogin: () => { void viewHost.replace(() => mountAuthView(root, api, {
        onSignedIn: () => showAccount(api),
        onRegistrationRequired: (intent) => { void viewHost.replace(() => mountInvitationView(root, api, intent, () => showAccount(api))); },
        onExit: () => showShell(api, 'ready'),
      })); },
    onAdmin: () => { void viewHost.replace(() => mountAdminView(root, api)); },
  });
  return { dispose: () => { root.replaceChildren(); } };
};
const showShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => { void viewHost.replace(() => mountShell(api, serviceStatus)); };

renderHostedShell(root, { serviceStatus: 'checking', onLogin: () => undefined });
void HostedAPI.connect().then(async (api) => {
  try { await api.session(); showAccount(api); }
  catch { showShell(api, 'ready'); }
}).catch(() => {
  renderHostedShell(root, { serviceStatus: 'unavailable', onLogin: () => undefined });
});

window.addEventListener('pagehide', () => { void viewHost.dispose(); }, { once: true });
