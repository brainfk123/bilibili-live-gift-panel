import './shell.css';
import { renderHostedShell, type HostedSession } from './shell';

const root = document.getElementById('hosted-app');

if (!(root instanceof HTMLElement)) {
  throw new Error('Hosted application root is missing.');
}

const signedOutSession: HostedSession = {
  serviceStatus: 'checking',
  // Authentication is intentionally composed by a later identity boundary.
  // This shell owns presentation only and never performs a network request.
  onLogin: () => undefined,
};

renderHostedShell(root, signedOutSession);
