import type { HostedView } from '../shell';
import { adminSections, type AdminSection } from './routes';

export function mountAdminShell(
  root: HTMLElement,
  options: { initial: AdminSection; mount(section: AdminSection, host: HTMLElement): HostedView; onLogout?(): void },
): HostedView {
  const document = root.ownerDocument;
  const frame = document.createElement('div'); frame.className = 'hosted-admin-frame';
  const sidebar = document.createElement('nav'); sidebar.className = 'hosted-admin-sidebar'; sidebar.setAttribute('aria-label', '管理员功能');
  const workspace = document.createElement('div'); workspace.className = 'hosted-admin-workspace';
  const header = document.createElement('header'); header.className = 'hosted-admin-header';
  const title = document.createElement('h1'); title.textContent = '管理员控制台';
  header.append(title);
  if (options.onLogout) {
    const logout = document.createElement('button'); logout.type = 'button'; logout.textContent = '退出登录'; logout.addEventListener('click', options.onLogout); header.append(logout);
  }
  const content = document.createElement('main'); content.className = 'hosted-admin-content';
  workspace.append(header, content); frame.append(sidebar, workspace); root.replaceChildren(frame);

  const buttons = new Map<AdminSection, HTMLButtonElement>();
  let active = options.initial;
  let current = options.mount(active, content);
  let disposed = false;
  let transition: Promise<void> = Promise.resolve();
  const refreshNavigation = (): void => {
    for (const [section, button] of buttons) {
      if (section === active) button.setAttribute('aria-current', 'page'); else button.removeAttribute('aria-current');
    }
  };
  const navigate = (section: AdminSection): void => {
    if (disposed || section === active) return;
    transition = transition.then(async () => {
      if (disposed || section === active) return;
      const old = current; current = { dispose() {} };
      await old.dispose();
      if (disposed) return;
      active = section; refreshNavigation(); content.replaceChildren(); current = options.mount(section, content);
    });
  };
  for (const item of adminSections) {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = item.label; button.addEventListener('click', () => navigate(item.id));
    buttons.set(item.id, button); sidebar.append(button);
  }
  refreshNavigation();

  return Object.freeze({ dispose: async (): Promise<void> => {
    if (disposed) return; disposed = true;
    await transition; await current.dispose();
  } });
}
