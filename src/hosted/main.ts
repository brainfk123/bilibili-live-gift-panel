import './shell.css';
import { HostedAPI } from './api';
import { mountAdminView } from './admin';
import { mountAuthView } from './auth';
import { mountInvitationView } from './invitations';
import { renderHostedShell, type HostedSession } from './shell';

const root = document.getElementById('hosted-app');

if (!(root instanceof HTMLElement)) {
  throw new Error('Hosted application root is missing.');
}

let disposeCurrent: (() => void | Promise<void>) | undefined;
const showShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => {
  disposeCurrent = undefined;
  const showAccount = (): void => {
    disposeCurrent = mountInvitationView(root, api, undefined, undefined, () => showShell(api, 'ready')).dispose;
  };
  renderHostedShell(root, {
    serviceStatus,
    onLogin: () => {
      const mounted = mountAuthView(root, api, {
        onSignedIn: showAccount,
        onRegistrationRequired: (intent) => { disposeCurrent = mountInvitationView(root, api, intent, showAccount).dispose; },
        onExit: () => showShell(api, 'ready'),
      });
      disposeCurrent = mounted.dispose;
    },
    onAdmin: () => { disposeCurrent = mountAdminView(root, api).dispose; },
  });
};

renderHostedShell(root, { serviceStatus: 'checking', onLogin: () => undefined });
void HostedAPI.connect().then(async (api) => {
  try { await api.session(); disposeCurrent = mountInvitationView(root, api, undefined, undefined, () => showShell(api, 'ready')).dispose; }
  catch { showShell(api, 'ready'); }
}).catch(() => {
  renderHostedShell(root, { serviceStatus: 'unavailable', onLogin: () => undefined });
});

window.addEventListener('pagehide', () => { void disposeCurrent?.(); disposeCurrent = undefined; }, { once: true });
