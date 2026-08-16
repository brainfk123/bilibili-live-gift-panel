import './shell.css';
import { HostedAPI } from './api';
import { mountAdminView } from './admin';
import { mountAuthView } from './auth';
import { mountInvitationView } from './invitations';
import { createHostedViewHost, renderHostedShell, type HostedSession, type HostedView } from './shell';

const root = document.getElementById('hosted-app');

if (!(root instanceof HTMLElement)) {
  throw new Error('Hosted application root is missing.');
}

const viewHost = createHostedViewHost();
const mountShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): HostedView => {
  const showAccount = (): void => { void viewHost.replace(() => mountInvitationView(root, api, undefined, undefined, () => showShell(api, 'ready'))); };
  renderHostedShell(root, {
    serviceStatus,
    onLogin: () => { void viewHost.replace(() => mountAuthView(root, api, {
        onSignedIn: showAccount,
        onRegistrationRequired: (intent) => { void viewHost.replace(() => mountInvitationView(root, api, intent, showAccount)); },
        onExit: () => showShell(api, 'ready'),
      })); },
    onAdmin: () => { void viewHost.replace(() => mountAdminView(root, api)); },
  });
  return { dispose: () => { root.replaceChildren(); } };
};
const showShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => { void viewHost.replace(() => mountShell(api, serviceStatus)); };

renderHostedShell(root, { serviceStatus: 'checking', onLogin: () => undefined });
void HostedAPI.connect().then(async (api) => {
  try { await api.session(); await viewHost.replace(() => mountInvitationView(root, api, undefined, undefined, () => showShell(api, 'ready'))); }
  catch { showShell(api, 'ready'); }
}).catch(() => {
  renderHostedShell(root, { serviceStatus: 'unavailable', onLogin: () => undefined });
});

window.addEventListener('pagehide', () => { void viewHost.dispose(); }, { once: true });
