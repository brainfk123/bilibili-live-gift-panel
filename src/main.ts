import './ui/display/display.css';
import { mountDisplay } from './ui/display/display';
import { installFavicon } from './ui/brand';
import { startApp } from './runtime/bootstrap';
import { hydrateStateFromServer } from './storage';
import { startPagePresence } from './backend';
import {
  createServerContinuityController,
  plannedUpdateRestartExpected,
  setPlannedUpdateRestart,
} from './server-continuity';

async function boot(): Promise<void> {
  const mode = new URLSearchParams(location.search).get('mode') === 'config' ? 'config' : 'display';
  const serverNotice = document.createElement('div');
  serverNotice.className = 'server-continuity-notice';
  serverNotice.setAttribute('role', 'status');
  serverNotice.setAttribute('aria-live', 'polite');
  serverNotice.hidden = true;
  if (mode === 'config') document.body.append(serverNotice);
  const continuity = createServerContinuityController({
    show: (message) => {
      serverNotice.textContent = message;
      serverNotice.hidden = false;
    },
    hide: () => { serverNotice.hidden = true; },
    reload: () => location.reload(),
  });
  startPagePresence(mode, {
    onUnavailable: () => continuity.unavailable(plannedUpdateRestartExpected()),
    onReady: (version) => {
      continuity.ready(version);
      setPlannedUpdateRestart(false);
    },
  });
  await hydrateStateFromServer();
  await startApp({
    document,
    search: location.search,
    installFavicon,
    loadConfig: async () => import('./ui/config/config-entry'),
    mountDisplay,
  });
}

void boot();
