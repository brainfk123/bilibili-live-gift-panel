import './ui/config/config.css';
import './ui/display/display.css';
import { mountDisplay } from './ui/display/display';
import { mountConfig } from './ui/config/config';

const root = document.getElementById('app')!;
const params = new URLSearchParams(location.search);
const mode = params.get('mode') ?? 'display';
if (mode === 'config') {
  root.classList.add('config-root');
  mountConfig(root);
} else {
  document.body.classList.add('display-mode');
  root.classList.add('display-root');
  mountDisplay(root);
}
