import { el } from '../common';
import { CONFIG_PAGES, ConfigPageIcon, ConfigPageId } from './config-route';

export interface ConfigShell {
  element: HTMLElement;
  content: HTMLElement;
  simpleContent: HTMLElement;
  workspace: (page: ConfigPageId) => HTMLElement;
  activate: (page: ConfigPageId) => void;
  setSimpleMode: (simple: boolean) => void;
  clearWorkspaces: () => void;
}

const ICON_PATHS: Record<ConfigPageIcon, string[]> = {
  overview: ['M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z'],
  attributes: ['M4 6h10M18 6h2M4 12h2M10 12h10M4 18h7M15 18h5', 'M14 3v6M6 9v6M11 15v6M18 3v6M15 15v6'],
  activities: ['M5 21V4m0 1h11l-2 4 2 4H5', 'M8 17h10'],
  kpi: ['M12 3a9 9 0 1 0 9 9', 'M12 7a5 5 0 1 0 5 5', 'M12 11a1 1 0 1 0 1 1'],
  obs: ['M3 5h18v12H3z', 'M8 21h8M12 17v4'],
  data: ['M5 20V10M12 20V4M19 20v-7', 'M3 20h18'],
};

function createNavigationIcon(kind: ConfigPageIcon): HTMLElement {
  const namespace = 'http://www.w3.org/2000/svg';
  const createSvgElement = (tag: 'svg' | 'path'): SVGElement => (
    typeof document.createElementNS === 'function'
      ? document.createElementNS(namespace, tag)
      : document.createElement(tag) as unknown as SVGElement
  );
  const svg = createSvgElement('svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  svg.setAttribute('class', 'config-nav-icon');
  for (const data of ICON_PATHS[kind]) {
    const path = createSvgElement('path');
    path.setAttribute('d', data);
    path.setAttribute('fill', 'none');
    path.setAttribute('stroke', 'currentColor');
    path.setAttribute('stroke-width', '1.8');
    path.setAttribute('stroke-linecap', 'round');
    path.setAttribute('stroke-linejoin', 'round');
    svg.append(path);
  }
  return svg as unknown as HTMLElement;
}

export function createConfigShell(
  activePage: ConfigPageId,
  onNavigate: (page: ConfigPageId) => void,
  simpleMode = false,
): ConfigShell {
  const navigation = el('nav', { class: 'config-navigation', ariaLabel: '配置页面' } as any);
  const buttons = new Map<ConfigPageId, HTMLButtonElement>();
  const workspaces = new Map<ConfigPageId, HTMLElement>();
  const content = el('main', { class: 'wizard-content config-page' });
  const simpleContent = el('div', {
    class: 'simple-mode-workspace',
    role: 'region',
    ariaLabel: '简单模式工作区',
  } as any);

  for (const page of CONFIG_PAGES) {
    const button = el('button', { class: 'config-nav-button', type: 'button' }) as HTMLButtonElement;
    button.dataset.configPage = page.id;
    button.title = `${page.label} · ${page.description}`;
    button.append(
      createNavigationIcon(page.icon),
      el('span', { class: 'config-nav-copy' }, [
        el('strong', { text: page.label }),
        el('small', { text: page.description }),
      ]),
    );
    button.onclick = () => onNavigate(page.id);
    buttons.set(page.id, button);
    navigation.append(button);
    const workspace = el('div', {
      class: 'config-page-workspace',
      role: 'region',
      ariaLabel: `${page.label}工作区`,
    } as any);
    workspace.dataset.configPageWorkspace = page.id;
    workspaces.set(page.id, workspace);
    content.append(workspace);
  }
  content.append(simpleContent);

  const activate = (page: ConfigPageId): void => {
    content.dataset.activePage = page;
    for (const [candidate, button] of buttons) {
      const active = candidate === page;
      button.classList.toggle('is-active', active);
      if (active) button.setAttribute('aria-current', 'page');
      else button.removeAttribute('aria-current');
    }
    for (const [candidate, workspace] of workspaces) {
      const active = candidate === page;
      workspace.hidden = !active;
      workspace.setAttribute('aria-hidden', String(!active));
    }
  };
  activate(activePage);

  const sidebar = el('aside', { class: 'config-sidebar' }, [
      navigation,
      el('p', { class: 'config-sidebar-note', text: '配置会自动保存，关闭页面不会中断直播监听。' }),
    ]);
  const element = el('div', { class: 'config-workspace-layout' }, [
    sidebar,
    content,
  ]);

  const setSimpleMode = (simple: boolean): void => {
    element.classList.toggle('is-simple-mode', simple);
    sidebar.hidden = simple;
    sidebar.setAttribute('aria-hidden', String(simple));
    simpleContent.hidden = !simple;
    simpleContent.setAttribute('aria-hidden', String(!simple));
    for (const workspace of workspaces.values()) {
      if (simple) {
        workspace.hidden = true;
        workspace.setAttribute('aria-hidden', 'true');
      }
    }
    if (!simple) activate(activePage);
  };
  setSimpleMode(simpleMode);

  return {
    element,
    content,
    simpleContent,
    workspace: (page) => workspaces.get(page) as HTMLElement,
    activate,
    setSimpleMode,
    clearWorkspaces: () => {
      for (const workspace of workspaces.values()) workspace.replaceChildren();
      simpleContent.replaceChildren();
    },
  };
}
