import './shell.css';

import type { HostedView } from '../shell';
import { HOSTED_USER_PAGES, hostedUserPageDefinition, type HostedUserPage } from './routes';

export type HostedUserExperience = 'simple' | 'advanced';
export type HostedUserPageStateKind = 'loading' | 'empty' | 'error';

export interface HostedUserShellOptions {
  readonly initialPage: HostedUserPage;
  readonly experience: HostedUserExperience;
  readonly experiencePending?: boolean;
  readonly configurationId: string;
  readonly mount: (page: HostedUserPage, host: HTMLElement, navigate: (page: HostedUserPage) => void) => HostedView;
  readonly onPageChange?: (page: HostedUserPage) => void;
  readonly onExperienceChange?: (experience: HostedUserExperience) => void;
  readonly onSettings?: () => void;
  readonly onInvitations?: () => void;
  readonly onLogout?: () => void;
}

export interface HostedUserPageState {
  readonly kind: HostedUserPageStateKind;
  readonly title: string;
  readonly description?: string;
  readonly onRetry?: () => void;
}

function actionButton(document: Document, label: string, action?: () => void): HTMLButtonElement {
  const button = document.createElement('button');
  button.type = 'button';
  button.textContent = label;
  if (action) button.addEventListener('click', action);
  return button;
}

export function createHostedUserShell(root: HTMLElement, options: HostedUserShellOptions) {
  const document = root.ownerDocument;
  const frame = document.createElement('div');
  frame.className = 'hosted-user-app';
  frame.dataset.experience = options.experience;

  const sidebar = document.createElement('aside');
  sidebar.className = 'hosted-user-sidebar';
  const brand = document.createElement('div');
  brand.className = 'hosted-user-brand';
  const brandMark = document.createElement('span'); brandMark.textContent = '礼'; brandMark.setAttribute('aria-hidden', 'true');
  const brandCopy = document.createElement('span'); brandCopy.textContent = '礼物互动工坊';
  brand.append(brandMark, brandCopy);
  const navigation = document.createElement('nav');
  navigation.className = 'hosted-user-navigation';
  navigation.setAttribute('aria-label', '主播工作区');

  const workspace = document.createElement('section');
  workspace.className = 'hosted-user-workspace';
  const header = document.createElement('header');
  header.className = 'hosted-user-header';
  const heading = document.createElement('div');
  heading.className = 'hosted-user-heading';
  const eyebrow = document.createElement('span'); eyebrow.textContent = '主播工作区';
  const title = document.createElement('h1');
  const description = document.createElement('p');
  heading.append(eyebrow, title, description);
  const headerActions = document.createElement('div');
  headerActions.className = 'hosted-user-header-actions';
  const experience = actionButton(document, '', () => {
    setExperience(frame.dataset.experience === 'advanced' ? 'simple' : 'advanced');
  });
  experience.className = 'hosted-user-experience';
  headerActions.append(
    experience,
    actionButton(document, '我的邀请码', options.onInvitations),
    actionButton(document, '设置', options.onSettings),
    actionButton(document, '退出登录', options.onLogout),
  );
  const notice = document.createElement('p');
  notice.className = 'hosted-user-notice';
  notice.setAttribute('role', 'status');
  notice.setAttribute('aria-live', 'polite');
  const content = document.createElement('main');
  content.className = 'hosted-user-content';
  content.id = 'hosted-user-content';
  header.append(heading, headerActions);
  workspace.append(header, notice, content);
  sidebar.append(brand, navigation);
  frame.append(sidebar, workspace);
  root.replaceChildren(frame);

  const buttons = new Map<HostedUserPage, HTMLButtonElement>();
  let currentPage = options.initialPage;
  let currentView: HostedView | undefined;
  let transition: Promise<void> = Promise.resolve();
  let disposed = false;

  const updatePage = (page: HostedUserPage): void => {
    const definition = hostedUserPageDefinition(page);
    title.textContent = definition.label;
    description.textContent = definition.description;
    for (const [candidate, button] of buttons) {
      if (candidate === page) button.setAttribute('aria-current', 'page');
      else button.removeAttribute('aria-current');
    }
  };

  const mountPage = (page: HostedUserPage): void => {
    content.replaceChildren();
    currentView = options.mount(page, content, (target) => { void navigate(target); });
  };

  const disposeCurrent = async (): Promise<void> => {
    const previous = currentView;
    if (!previous) return;
    currentView = undefined;
    await previous.dispose();
  };

  const navigateTo = (page: HostedUserPage, publish: boolean): Promise<void> => {
    if (disposed || page === currentPage) return transition;
    currentPage = page;
    updatePage(page);
    if (publish) options.onPageChange?.(page);
    const operation = async (): Promise<void> => {
      await disposeCurrent();
      if (!disposed && currentPage === page) mountPage(page);
    };
    const scheduled = transition.then(operation, operation);
    transition = scheduled.catch(() => undefined);
    return scheduled;
  };
  const navigate = (page: HostedUserPage): Promise<void> => navigateTo(page, true);
  const syncPage = (page: HostedUserPage): Promise<void> => navigateTo(page, false);

  for (const page of HOSTED_USER_PAGES) {
    const button = actionButton(document, page.label, () => { void navigate(page.id); });
    button.className = 'hosted-user-navigation-item';
    button.dataset.page = page.id;
    button.setAttribute('aria-controls', content.id);
    navigation.append(button);
    buttons.set(page.id, button);
  }

  const applyExperience = (next: HostedUserExperience, publish: boolean): void => {
    frame.dataset.experience = next;
    experience.textContent = next === 'simple' ? '完整模式' : '简单模式';
    experience.setAttribute('aria-label', next === 'simple' ? '切换到完整模式' : '切换到简单模式');
    experience.setAttribute('aria-pressed', next === 'advanced' ? 'true' : 'false');
    if (publish) options.onExperienceChange?.(next);
  };
  const setExperience = (next: HostedUserExperience): void => { applyExperience(next, true); };
  const syncExperience = (next: HostedUserExperience): void => { applyExperience(next, false); };
  const setExperiencePending = (pending: boolean): void => {
    experience.disabled = pending;
    experience.setAttribute('aria-busy', pending ? 'true' : 'false');
  };

  syncExperience(options.experience);
  setExperiencePending(options.experiencePending ?? false);
  updatePage(currentPage);
  mountPage(currentPage);

  return Object.freeze({
    navigate,
    syncPage,
    setExperience,
    syncExperience,
    setExperiencePending,
    activeConfigurationId: () => options.configurationId,
    activePage: () => currentPage,
    announce(message: string): void { notice.textContent = message; },
    async dispose(): Promise<void> {
      disposed = true;
      await transition;
      await disposeCurrent();
      root.replaceChildren();
    },
  });
}

export function renderHostedUserPageState(host: HTMLElement, state: HostedUserPageState): void {
  const document = host.ownerDocument;
  const section = document.createElement('section');
  section.className = `hosted-user-state is-${state.kind}`;
  section.setAttribute('role', 'status');
  if (state.kind === 'loading') section.setAttribute('aria-busy', 'true');
  const title = document.createElement('h2'); title.textContent = state.title;
  section.append(title);
  if (state.description) {
    const description = document.createElement('p'); description.textContent = state.description;
    section.append(description);
  }
  if (state.kind === 'loading') {
    const skeleton = document.createElement('div'); skeleton.className = 'hosted-user-state-skeleton'; skeleton.setAttribute('aria-hidden', 'true');
    section.append(skeleton);
  }
  if (state.kind === 'error' && state.onRetry) section.append(actionButton(document, '重试', state.onRetry));
  host.replaceChildren(section);
}
