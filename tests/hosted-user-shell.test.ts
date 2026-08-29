import { describe, expect, it, vi } from 'vitest';

import { HOSTED_USER_PAGES, parseHostedUserPage, hostedUserPageSearch } from '../src/hosted/user/routes';
import { createHostedUserShell, renderHostedUserPageState } from '../src/hosted/user/shell';
import { HOSTED_USER_SETTINGS_SECTIONS } from '../src/hosted/user/settings/migration-center';

class Element {
  children: Element[] = [];
  textContent = '';
  className = '';
  type = '';
  hidden = false;
  disabled = false;
  dataset: Record<string, string> = {};
  attributes = new Map<string, string>();
  listeners = new Map<string, (event?: { preventDefault?(): void }) => void>();

  constructor(readonly tagName: string, readonly ownerDocument: DocumentLike) {}
  append(...nodes: Element[]): void { this.children.push(...nodes); }
  replaceChildren(...nodes: Element[]): void { this.children = nodes; }
  setAttribute(name: string, value: string): void { this.attributes.set(name, value); }
  removeAttribute(name: string): void { this.attributes.delete(name); }
  addEventListener(name: string, listener: (event?: { preventDefault?(): void }) => void): void { this.listeners.set(name, listener); }
  removeEventListener(name: string): void { this.listeners.delete(name); }
  focus(): void { this.ownerDocument.activeElement = this; }
}

interface DocumentLike {
  activeElement?: Element;
  createElement(tag: string): Element;
}

function dom() {
  const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
  return { document, root: new Element('div', document) };
}

function descendants(root: Element): Element[] {
  return root.children.flatMap((child) => [child, ...descendants(child)]);
}

describe('Hosted user workspace routes', () => {
  it('uses the six EXE workspace labels and rejects unknown URL state', () => {
    expect(HOSTED_USER_PAGES.map(({ id, label }) => ({ id, label }))).toEqual([
      { id: 'overview', label: '概览' },
      { id: 'attributes', label: '属性玩法' },
      { id: 'activities', label: '活动会话' },
      { id: 'targets', label: '礼物目标' },
      { id: 'obs', label: 'OBS 面板' },
      { id: 'data', label: '数据中心' },
    ]);
    expect(parseHostedUserPage('?workspace=activities')).toBe('activities');
    expect(parseHostedUserPage('?workspace=unknown')).toBe('overview');
    expect(parseHostedUserPage('?workspace=')).toBe('overview');
    expect(hostedUserPageSearch('?source=login&workspace=overview', 'obs')).toBe('?source=login&workspace=obs');
  });

  it('keeps account, devices, retention and migration under settings', () => {
    expect(HOSTED_USER_SETTINGS_SECTIONS.map((section) => section.label)).toEqual([
      '账号', '已登录设备', '数据保留', '迁移中心',
    ]);
  });
});

describe('Hosted user workspace shell', () => {
  it('hydrates the authoritative mode without publishing a write and enables explicit toggles after loading', () => {
    const { root } = dom();
    const modeChanges = vi.fn();
    const shell = createHostedUserShell(root as unknown as HTMLElement, {
      initialPage: 'overview', experience: 'simple', experiencePending: true, configurationId: 'config-1',
      onExperienceChange: modeChanges,
      mount: () => ({ dispose() {} }),
    });
    const experienceButton = descendants(root).find((element) => element.className === 'hosted-user-experience')!;

    expect(experienceButton.disabled).toBe(true);
    shell.syncExperience('advanced');
    expect(modeChanges).not.toHaveBeenCalled();
    expect(root.children[0]!.dataset.experience).toBe('advanced');
    shell.setExperiencePending(false);
    expect(experienceButton.disabled).toBe(false);
    shell.setExperience('simple');
    expect(modeChanges).toHaveBeenCalledWith('simple');
    void shell.dispose();
  });

  it('synchronizes browser history without publishing a new history entry', async () => {
    const { root } = dom();
    const pageChanges = vi.fn();
    const shell = createHostedUserShell(root as unknown as HTMLElement, {
      initialPage: 'overview', experience: 'simple', configurationId: 'config-1',
      onPageChange: pageChanges,
      mount: () => ({ dispose() {} }),
    });

    await shell.syncPage('data');

    expect(shell.activePage()).toBe('data');
    expect(pageChanges).not.toHaveBeenCalled();
    await shell.dispose();
  });

  it('disposes each workspace exactly once during rapid navigation', async () => {
    const { root } = dom();
    let releaseOverview!: () => void;
    const overviewGate = new Promise<void>((resolve) => { releaseOverview = resolve; });
    let overviewDisposals = 0;
    const mounts: string[] = [];
    const shell = createHostedUserShell(root as unknown as HTMLElement, {
      initialPage: 'overview', experience: 'simple', configurationId: 'config-1',
      mount: (page) => {
        mounts.push(page);
        if (page !== 'overview') return { dispose() {} };
        return { dispose: () => {
          overviewDisposals += 1;
          return overviewGate;
        } };
      },
    });

    const attributes = shell.navigate('attributes');
    const activities = shell.navigate('activities');
    releaseOverview();
    await Promise.all([attributes, activities]);

    expect(overviewDisposals).toBe(1);
    expect(mounts).toEqual(['overview', 'activities']);
    await shell.dispose();
  });

  it('does not dispose the active workspace twice when the shell closes during navigation', async () => {
    const { root } = dom();
    let releaseOverview!: () => void;
    const overviewGate = new Promise<void>((resolve) => { releaseOverview = resolve; });
    let overviewDisposals = 0;
    const shell = createHostedUserShell(root as unknown as HTMLElement, {
      initialPage: 'overview', experience: 'simple', configurationId: 'config-1',
      mount: () => ({ dispose: () => {
        overviewDisposals += 1;
        return overviewGate;
      } }),
    });

    const navigating = shell.navigate('attributes');
    const disposing = shell.dispose();
    releaseOverview();
    await Promise.all([navigating, disposing]);

    expect(overviewDisposals).toBe(1);
  });

  it('does not retry a workspace disposer that rejected', async () => {
    const { root } = dom();
    const failure = new Error('dispose failed');
    let overviewDisposals = 0;
    const mounts: string[] = [];
    const shell = createHostedUserShell(root as unknown as HTMLElement, {
      initialPage: 'overview', experience: 'simple', configurationId: 'config-1',
      mount: (page) => {
        mounts.push(page);
        return page === 'overview'
          ? { dispose: async () => { overviewDisposals += 1; throw failure; } }
          : { dispose() {} };
      },
    });

    await expect(shell.navigate('attributes')).rejects.toBe(failure);
    await expect(shell.navigate('activities')).resolves.toBeUndefined();

    expect(overviewDisposals).toBe(1);
    expect(mounts).toEqual(['overview', 'activities']);
    await shell.dispose();
  });

  it('serializes page changes while simple and advanced modes keep one configuration identity', async () => {
    const { root } = dom();
    const events: string[] = [];
    const pageChanges: string[] = [];
    const shell = createHostedUserShell(root as unknown as HTMLElement, {
      initialPage: 'overview',
      experience: 'simple',
      configurationId: 'config-1',
      onPageChange: (page) => pageChanges.push(page),
      mount: (page, host) => {
        events.push(`mount:${page}`);
        host.textContent = page;
        return { dispose: async () => { events.push(`dispose:${page}`); } };
      },
    });

    const frame = root.children[0]!;
    const buttons = descendants(frame).filter((element) => element.dataset.page !== undefined);
    expect(buttons.map((button) => button.textContent)).toEqual(HOSTED_USER_PAGES.map((page) => page.label));
    expect(buttons[0]!.attributes.get('aria-current')).toBe('page');
    expect(frame.dataset.experience).toBe('simple');
    expect(shell.activeConfigurationId()).toBe('config-1');
    const experienceButton = descendants(frame).find((element) => element.className === 'hosted-user-experience')!;
    expect(experienceButton.textContent).toBe('完整模式');
    expect(experienceButton.attributes.get('aria-label')).toBe('切换到完整模式');

    buttons[1]!.listeners.get('click')?.();
    await vi.waitFor(() => expect(events).toEqual(['mount:overview', 'dispose:overview', 'mount:attributes']));
    expect(pageChanges).toEqual(['attributes']);
    expect(buttons[1]!.attributes.get('aria-current')).toBe('page');

    shell.setExperience('advanced');
    expect(frame.dataset.experience).toBe('advanced');
    expect(shell.activeConfigurationId()).toBe('config-1');
    expect(experienceButton.textContent).toBe('简单模式');
    expect(experienceButton.attributes.get('aria-label')).toBe('切换到简单模式');
    shell.announce('退出失败，请稍后重试');
    expect(descendants(frame).find((element) => element.attributes.get('role') === 'status')?.textContent)
      .toBe('退出失败，请稍后重试');
    await shell.dispose();
    expect(root.children).toHaveLength(0);
  });

  it('keeps account actions in the shell without mixing them into workspace navigation', () => {
    const { root } = dom();
    const onSettings = vi.fn();
    const onInvitations = vi.fn();
    const onLogout = vi.fn();
    const shell = createHostedUserShell(root as unknown as HTMLElement, {
      initialPage: 'overview', experience: 'simple', configurationId: 'config-1',
      onSettings, onInvitations, onLogout, mount: () => ({ dispose() {} }),
    });
    const actions = descendants(root).filter((element) => ['设置', '我的邀请码', '退出登录'].includes(element.textContent));
    expect(actions.map((action) => action.textContent)).toEqual(['我的邀请码', '设置', '退出登录']);
    for (const action of actions) action.listeners.get('click')?.();
    expect(onInvitations).toHaveBeenCalledTimes(1);
    expect(onSettings).toHaveBeenCalledTimes(1);
    expect(onLogout).toHaveBeenCalledTimes(1);
    void shell.dispose();
  });

  it('renders stable loading, empty and failure regions with an explicit retry', () => {
    const { root } = dom();
    renderHostedUserPageState(root as unknown as HTMLElement, { kind: 'loading', title: '正在加载属性玩法' });
    expect(root.children[0]!.className).toContain('is-loading');
    expect(root.children[0]!.attributes.get('aria-busy')).toBe('true');

    renderHostedUserPageState(root as unknown as HTMLElement, { kind: 'empty', title: '暂无礼物目标', description: '创建后会显示在这里。' });
    expect(root.children[0]!.className).toContain('is-empty');
    expect(descendants(root).map((element) => element.textContent)).toContain('创建后会显示在这里。');

    const retry = vi.fn();
    renderHostedUserPageState(root as unknown as HTMLElement, { kind: 'error', title: '数据加载失败', description: '请重试。', onRetry: retry });
    const retryButton = descendants(root).find((element) => element.textContent === '重试');
    retryButton?.listeners.get('click')?.();
    expect(retry).toHaveBeenCalledTimes(1);
  });
});
