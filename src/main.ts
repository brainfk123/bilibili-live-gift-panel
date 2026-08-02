import './ui/display/display.css';
import { mountDisplay } from './ui/display/display';
import { mountConfig } from './ui/config/config';
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
    loadConfigStyles: async () => (await import('./ui/config/config.css?inline')).default,
    mountDisplay,
    mountConfig,
  });
}

void boot();
