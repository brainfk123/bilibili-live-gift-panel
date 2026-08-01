import './ui/display/display.css';
import { mountDisplay } from './ui/display/display';
import { mountConfig } from './ui/config/config';
import { installFavicon } from './ui/brand';
import { startApp } from './runtime/bootstrap';

void startApp({
  document,
  search: location.search,
  installFavicon,
  loadConfigStyles: async () => (await import('./ui/config/config.css?inline')).default,
  mountDisplay,
  mountConfig,
});
