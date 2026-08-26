import type { HostedView } from '../shell';
import { adminSections, type AdminSection } from './routes';

export function mountAdminShell(
  root: HTMLElement,
  options: { initial: AdminSection; mount(section: AdminSection, host: HTMLElement, navigate: (next: AdminSection) => void): HostedView; onLogout?(): void },
): HostedView {
  const document = root.ownerDocument;
  const frame = document.createElement('div'); frame.className = 'hosted-admin-frame';
  const sidebar = document.createElement('nav'); sidebar.className = 'hosted-admin-sidebar'; sidebar.setAttribute('aria-label', '管理员功能');
  const sidebarClose = document.createElement('button'); sidebarClose.type = 'button'; sidebarClose.className = 'hosted-admin-sidebar-close'; sidebarClose.setAttribute('aria-label', '关闭菜单'); sidebarClose.textContent = '×';
  const workspace = document.createElement('div'); workspace.className = 'hosted-admin-workspace';
  const backdrop = document.createElement('div'); backdrop.className = 'hosted-admin-sidebar-backdrop'; backdrop.hidden = true;
  const header = document.createElement('header'); header.className = 'hosted-admin-header';
  const menuTrigger = document.createElement('button'); menuTrigger.type = 'button'; menuTrigger.className = 'hosted-admin-menu-trigger'; menuTrigger.textContent = '菜单'; menuTrigger.setAttribute('aria-expanded', 'false');
  const title = document.createElement('h1'); title.textContent = '管理员控制台';
  header.append(menuTrigger, title);
  if (options.onLogout) {
    const logout = document.createElement('button'); logout.type = 'button'; logout.textContent = '退出登录'; logout.addEventListener('click', options.onLogout); header.append(logout);
  }
  const content = document.createElement('main'); content.className = 'hosted-admin-content';
  sidebar.append(sidebarClose);
  workspace.append(header, content); frame.append(sidebar, workspace, backdrop); root.replaceChildren(frame);

  const buttons = new Map<AdminSection, HTMLButtonElement>();
  let active = options.initial;
  let current: HostedView = { dispose() {} };
  let disposed = false;
  let transition: Promise<void> = Promise.resolve();
  let menuOpen = false;
  let backdropPressStarted = false;
  const setMenuOpen = (open: boolean, restoreFocus = false): void => {
    backdropPressStarted = false;
    menuOpen = open;
    sidebar.setAttribute('data-open', String(open));
    menuTrigger.setAttribute('aria-expanded', String(open));
    backdrop.hidden = !open;
    if (open) buttons.values().next().value?.focus();
    else if (restoreFocus) menuTrigger.focus();
  };
  const refreshNavigation = (): void => {
    for (const [section, button] of buttons) {
      if (section === active) button.setAttribute('aria-current', 'page'); else button.removeAttribute('aria-current');
    }
  };
  const navigate = (section: AdminSection): void => {
    if (menuOpen) setMenuOpen(false);
    if (disposed || section === active) return;
    transition = transition.then(async () => {
      if (disposed || section === active) return;
      const old = current; current = { dispose() {} };
      await old.dispose();
      if (disposed) return;
      active = section; refreshNavigation(); content.replaceChildren(); current = options.mount(section, content, navigate);
    });
  };
  for (const item of adminSections) {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = item.label; button.addEventListener('click', () => navigate(item.id));
    buttons.set(item.id, button); sidebar.append(button);
  }
  sidebarClose.addEventListener('click', () => setMenuOpen(false, true));
  backdrop.addEventListener('pointerdown', (event) => { backdropPressStarted = event.target === backdrop; });
  backdrop.addEventListener('pointercancel', () => { backdropPressStarted = false; });
  backdrop.addEventListener('click', (event) => {
    const shouldClose = backdropPressStarted && event.target === backdrop;
    backdropPressStarted = false;
    if (shouldClose) setMenuOpen(false, true);
  });
  menuTrigger.addEventListener('click', () => setMenuOpen(!menuOpen));
  const onKeyDown = (event: KeyboardEvent): void => {
    if (!menuOpen) return;
    if (event.key === 'Escape') { event.preventDefault(); setMenuOpen(false, true); return; }
    if (event.key !== 'Tab') return;
    const items = [sidebarClose, ...buttons.values()];
    if (items.length === 0) return;
    if (!event.shiftKey && document.activeElement === items.at(-1)) { event.preventDefault(); items[0].focus(); }
    if (event.shiftKey && document.activeElement === items[0]) { event.preventDefault(); items.at(-1)?.focus(); }
  };
  frame.addEventListener('keydown', onKeyDown);
  refreshNavigation();
  current = options.mount(active, content, navigate);

  return Object.freeze({ dispose: async (): Promise<void> => {
    if (disposed) return; disposed = true; frame.removeEventListener('keydown', onKeyDown);
    await transition; await current.dispose();
  } });
}
