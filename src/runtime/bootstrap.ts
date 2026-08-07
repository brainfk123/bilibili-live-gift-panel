import { parseObsOutputTarget, type ObsOutputTarget } from '../obs-outputs';

export const CONFIG_STYLE_ID = 'bilibili-config-style';

export interface AppBootstrapOptions {
  document: Document;
  search: string;
  installFavicon: () => void;
  loadConfigStyles: () => Promise<string>;
  mountDisplay: (root: HTMLElement, target?: ObsOutputTarget) => void;
  mountConfig: (root: HTMLElement) => void;
}

export function injectConfigStyles(document: Document, cssText: string): HTMLStyleElement {
  const existing = document.getElementById(CONFIG_STYLE_ID) as HTMLStyleElement | null;
  if (existing) return existing;

  const style = document.createElement('style');
  style.id = CONFIG_STYLE_ID;
  style.dataset.mode = 'config';
  style.textContent = cssText;
  document.head.append(style);
  return style;
}

export async function startApp(options: AppBootstrapOptions): Promise<void> {
  options.installFavicon();

  const root = options.document.getElementById('app');
  if (!root) throw new Error('App root not found');

  const params = new URLSearchParams(options.search);
  const mode = params.get('mode') ?? 'display';
  if (mode === 'config') {
    options.document.body.classList.add('config-mode');
    root.classList.add('config-root');
    const cssText = await options.loadConfigStyles();
    injectConfigStyles(options.document, cssText);
    options.mountConfig(root);
    return;
  }

  options.document.body.classList.add('display-mode');
  root.classList.add('display-root');
  options.mountDisplay(root, parseObsOutputTarget(options.search));
}
