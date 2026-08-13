import './ui/display/display.css';
import { mountDisplay } from './ui/display/display';
import { installFavicon } from './ui/brand';
import { startApp } from './runtime/bootstrap';
import { hydrateStateFromServer } from './storage';
import { startPagePresence } from './backend';

async function boot(): Promise<void> {
  const mode = new URLSearchParams(location.search).get('mode') === 'config' ? 'config' : 'display';
  startPagePresence(mode);
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
