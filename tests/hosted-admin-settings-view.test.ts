import { describe, expect, it, vi } from 'vitest';

import { mountAdminSettingsView } from '../src/hosted/admin/settings';

type Listener = (event?: { preventDefault?(): void }) => void;

class Element {
  children: Element[] = [];
  parent?: Element;
  textContent = '';
  className = '';
  type = '';
  disabled = false;
  hidden = false;
  open = false;
  dataset: Record<string, string> = {};
  style = { width: '' };
  offsetWidth = 100;
  attributes = new Map<string, string>();
  listeners = new Map<string, Listener>();
  classList = {
    add: (...tokens: string[]) => {
      const classes = new Set(this.className.split(/\s+/).filter(Boolean));
      for (const token of tokens) classes.add(token);
      this.className = [...classes].join(' ');
    },
    remove: (...tokens: string[]) => {
      const removed = new Set(tokens);
      this.className = this.className.split(/\s+/).filter((token) => token && !removed.has(token)).join(' ');
    },
    contains: (token: string) => this.className.split(/\s+/).includes(token),
  };

  constructor(readonly tagName: string, readonly ownerDocument: DocumentLike) {}

  append(...nodes: Element[]) {
    for (const node of nodes) {
      node.remove();
      node.parent = this;
      this.children.push(node);
    }
  }

  replaceChildren(...nodes: Element[]) {
    for (const child of this.children) child.parent = undefined;
    this.children = [];
    this.append(...nodes);
  }

  remove() {
    if (!this.parent) return;
    this.parent.children = this.parent.children.filter((child) => child !== this);
    this.parent = undefined;
  }

  setAttribute(name: string, value: string) { this.attributes.set(name, value); }
  removeAttribute(name: string) { this.attributes.delete(name); }
  addEventListener(name: string, listener: Listener) { this.listeners.set(name, listener); }
  removeEventListener(name: string) { this.listeners.delete(name); }
}

interface DocumentLike { createElement(tag: string): Element }

function descendants(root: Element): Element[] {
  return [root, ...root.children.flatMap(descendants)];
}

function text(root: Element): string {
  return descendants(root).map((element) => element.textContent).join('');
}

function buttons(root: Element, label: string): Element[] {
  return descendants(root).filter((element) => element.tagName === 'button' && element.textContent === label);
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

const sessions = [
  {
    id: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', deviceLabel: 'iPhone · Safari', clientNetwork: '203.0.113.*',
    createdAt: '2026-08-23T08:00:00Z', lastSeenAt: '2026-08-23T08:10:00Z', expiresAt: '2026-09-22T08:00:00Z', current: true,
  },
  {
    id: 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', deviceLabel: 'Windows · Edge', clientNetwork: '198.51.100.*',
    createdAt: '2026-08-22T08:00:00Z', lastSeenAt: '2026-08-23T07:00:00Z', expiresAt: '2026-09-21T08:00:00Z', current: false,
  },
  {
    id: 'cccccccccccccccccccccccccccccccc', deviceLabel: 'Android · Chrome', clientNetwork: '2001:db8:abcd:1234::*',
    createdAt: '2026-08-21T08:00:00Z', lastSeenAt: '2026-08-22T07:00:00Z', expiresAt: '2026-09-20T08:00:00Z', current: false,
  },
];

function fixture(overrides: Record<string, unknown> = {}) {
  return {
    adminSettings: vi.fn(async () => ({
      maskedEmail: 'o***@example.com', sessionExpiresAt: sessions[0].expiresAt,
      totpEnabled: true, recoveryGeneratedAt: null, serviceHealth: 'healthy',
    })),
    adminSessions: vi.fn(async () => sessions.map((session) => ({ ...session }))),
    revokeAdminSession: vi.fn(async () => undefined),
    revokeOtherAdminSessions: vi.fn(async () => undefined),
    adminLoginEvents: vi.fn(async () => [
      { result: 'success', deviceLabel: 'iPhone · Safari', clientNetwork: '203.0.113.*', occurredAt: '2026-08-23T08:10:00Z' },
      { result: 'failure', deviceLabel: 'Windows · Edge', clientNetwork: '198.51.100.*', occurredAt: '2026-08-22T08:10:00Z' },
    ]),
    adminEvents: vi.fn(async () => []),
    adminDiagnostics: vi.fn(async () => ({ database: 'ok', biliService: 'healthy', checkedAt: '2026-08-23T08:10:00Z' })),
    adminLogout: vi.fn(async () => undefined),
    ...overrides,
  };
}

describe('administrator settings view', () => {
  it('preserves the shared content container class for layout and removes only its page class on dispose', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const root = new Element('main', document);
    root.className = 'hosted-admin-content';

    const view = mountAdminSettingsView(root as unknown as HTMLElement, fixture() as never, vi.fn());
    await flush();

    expect(root.classList.contains('hosted-admin-content')).toBe(true);
    expect(root.classList.contains('hosted-admin-settings')).toBe(true);
    await view.dispose();
    expect(root.className).toBe('hosted-admin-content');
  });

  it('lists the current device, revocable devices and recent login outcomes', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const root = new Element('div', document);
    const api = fixture();

    const view = mountAdminSettingsView(root as unknown as HTMLElement, api as never, vi.fn());
    await flush();

    expect(text(root)).toContain('管理员账号');
    expect(text(root)).toContain('iPhone · Safari');
    expect(text(root)).toContain('当前');
    expect(text(root)).toContain('首次登录');
    expect(text(root)).toContain('最近活动');
    expect(text(root)).toContain('会话到期');
    expect(buttons(root, '退出此设备')).toHaveLength(2);
    expect(text(root)).toContain('登录成功');
    expect(text(root)).toContain('登录失败');
    await view.dispose();
  });

  it('offers only per-device revocation and no bulk exit action', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const root = new Element('div', document);
    const api = fixture();
    const view = mountAdminSettingsView(root as unknown as HTMLElement, api as never, vi.fn());
    await flush();

    expect(buttons(root, '退出其他设备')).toHaveLength(0);
    expect(buttons(root, '退出此设备')).toHaveLength(2);
    await view.dispose();
  });

  it('removes one successfully revoked device but retains a failed row', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const root = new Element('div', document);
    const revokeAdminSession = vi.fn()
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('offline'));
    const api = fixture({ revokeAdminSession });
    const view = mountAdminSettingsView(root as unknown as HTMLElement, api as never, vi.fn());
    await flush();

    buttons(root, '退出此设备')[0].listeners.get('click')?.();
    await flush();
    const deviceCard = descendants(root).find((element) => element.className.includes('hosted-admin-device-card'));
    if (!deviceCard) throw new Error('device card missing');
    expect(text(deviceCard)).not.toContain('Windows · Edge');
    expect(text(deviceCard)).toContain('Android · Chrome');

    buttons(root, '退出此设备')[0].listeners.get('click')?.();
    await flush();
    expect(text(deviceCard)).toContain('Android · Chrome');
    expect(text(root)).toContain('设备退出失败，请重试');
    await view.dispose();
  });

  it('offers a working retry after the main settings load fails', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const root = new Element('div', document);
    const adminSettings = vi.fn()
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({
        maskedEmail: 'o***@example.com', sessionExpiresAt: sessions[0].expiresAt,
        totpEnabled: true, recoveryGeneratedAt: null, serviceHealth: 'healthy',
      });
    const api = fixture({ adminSettings });
    const view = mountAdminSettingsView(root as unknown as HTMLElement, api as never, vi.fn());
    await flush();

    expect(text(root)).toContain('系统设置加载失败，请重试');
    expect(descendants(root).filter((element) => element.className.includes('hosted-admin-card') && !element.hidden)).toHaveLength(0);
    buttons(root, '重试')[0].listeners.get('click')?.();
    await flush();

    expect(adminSettings).toHaveBeenCalledTimes(2);
    expect(text(root)).toContain('管理员账号');
    await view.dispose();
  });

  it('shows a loading state before settings resolve and explains an empty login history', async () => {
    let resolveSettings!: (value: { maskedEmail: string; sessionExpiresAt: string; totpEnabled: boolean; recoveryGeneratedAt: null; serviceHealth: string }) => void;
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const root = new Element('div', document);
    const api = fixture({
      adminSettings: vi.fn(() => new Promise((resolve) => { resolveSettings = resolve; })),
      adminLoginEvents: vi.fn(async () => []),
    });
    const view = mountAdminSettingsView(root as unknown as HTMLElement, api as never, vi.fn());

    expect(text(root)).toContain('正在加载系统设置…');
    expect(descendants(root).filter((element) => element.className.includes('hosted-admin-card') && !element.hidden)).toHaveLength(0);
    resolveSettings({ maskedEmail: 'o***@example.com', sessionExpiresAt: sessions[0].expiresAt, totpEnabled: true, recoveryGeneratedAt: null, serviceHealth: 'healthy' });
    await flush();

    expect(text(root)).toContain('暂无登录记录');
    await view.dispose();
  });

  it('lets advanced diagnostics retry after a transient failure', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const root = new Element('div', document);
    const adminDiagnostics = vi.fn()
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({ database: 'ok', biliService: 'healthy', checkedAt: '2026-08-23T08:10:00Z' });
    const api = fixture({ adminDiagnostics });
    const view = mountAdminSettingsView(root as unknown as HTMLElement, api as never, vi.fn());
    await flush();
    const advanced = descendants(root).find((element) => element.tagName === 'details');
    if (!advanced) throw new Error('advanced details missing');
    advanced.open = true;
    advanced.listeners.get('toggle')?.();
    await flush();

    buttons(root, '重试')[0].listeners.get('click')?.();
    await flush();

    expect(adminDiagnostics).toHaveBeenCalledTimes(2);
    expect(text(root)).toContain('数据库：ok');
    expect(text(root)).not.toContain('高级信息加载失败，请重试');
    expect(buttons(root, '重试')).toHaveLength(0);
    await view.dispose();
  });
});
