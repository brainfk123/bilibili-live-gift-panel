import { describe, expect, it, vi } from 'vitest';

import { mountAdminView } from '../src/hosted/admin';
import { mountAdminOverview } from '../src/hosted/admin/overview';
import { mountAccountList } from '../src/hosted/admin/accounts/list';
import { mountBiliServiceView } from '../src/hosted/admin/bili-service';
import { mountAdminInvitationView } from '../src/hosted/admin/invitations/view';
import { HostedAPIError } from '../src/hosted/api';

class Element {
  children: Element[] = [];
  parent?: Element;
  textContent = '';
  className = '';
  type = '';
  value = '';
  disabled = false;
  inputMode = '';
  autocomplete = '';
  src = '';
  alt = '';
  open = false;
  checked = false;
  hidden = false;
  dataset: Record<string, string> = {};
  style = { width: '' };
  offsetWidth = 100;
  attributes = new Map<string, string>();
  listeners = new Map<string, () => void>();
  constructor(readonly tagName: string, readonly ownerDocument: DocumentLike) {}
  get firstElementChild() { return this.children[0]; }
  append(...nodes: Element[]) { for (const node of nodes) { node.remove(); node.parent = this; this.children.push(node); } }
  replaceChildren(...nodes: Element[]) { for (const child of this.children) child.parent = undefined; this.children = []; this.append(...nodes); }
  remove() { if (this.parent) { this.parent.children = this.parent.children.filter((child) => child !== this); this.parent = undefined; } }
  setAttribute(name: string, value: string) { this.attributes.set(name, value); }
  getAttribute(name: string) { return this.attributes.get(name) ?? null; }
  removeAttribute(name: string) { this.attributes.delete(name); }
  addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
  removeEventListener(name: string) { this.listeners.delete(name); }
  focus() { this.ownerDocument.activeElement = this; }
}

interface DocumentLike {
  activeElement?: Element;
  createElement(tag: string): Element;
  createTextNode(text: string): Element;
}

function descendants(root: Element): Element[] { return [root, ...root.children.flatMap(descendants)]; }
function button(root: Element, text: string): Element {
  const found = descendants(root).find((element) => element.tagName === 'button' && element.textContent === text);
  if (!found) throw new Error(`button not found: ${text}`);
  return found;
}

function control(root: Element, tag: string, label: string): Element {
  const found = descendants(root).find((element) => element.tagName === tag && element.attributes.get('aria-label') === label);
  if (!found) throw new Error(`${tag} not found: ${label}`);
  return found;
}

function text(root: Element): string {
  return descendants(root).map((element) => element.textContent).join('');
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe('administrator section lifetime fence', () => {
  it('renders actionable loading, empty and error states instead of blank account tables', async () => {
    let resolveAccounts!: (value: { items: never[] }) => void;
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; } };
    const root = new Element('div', document);
    const api = { adminAccounts: vi.fn()
      .mockImplementationOnce(() => new Promise((resolve) => { resolveAccounts = resolve; }))
      .mockRejectedValueOnce(new Error('offline')) };
    const view = mountAccountList(root as unknown as HTMLElement, api as never);

    expect(text(root)).toContain('正在加载主播账号…');
    resolveAccounts({ items: [] });
    await flush();
    expect(text(root)).toContain('还没有主播账号');

    control(root, 'select', '账号状态').listeners.get('change')?.();
    await flush();
    expect(text(root)).toContain('账号列表加载失败，请重试');
    expect(button(root, '重试')).toBeDefined();
    await view.dispose();
  });

  it('uses a visible loading card while invitations are pending', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; } };
    const root = new Element('div', document);
    const view = mountAdminInvitationView(root as unknown as HTMLElement, { adminInvitations: vi.fn(() => new Promise(() => undefined)) } as never);

    expect(text(root)).toContain('正在加载邀请码…');
    expect(descendants(root).some((element) => element.className.includes('hosted-admin-state'))).toBe(true);
    await view.dispose();
  });

  it('gives overview failures a retry action', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; } };
    const root = new Element('div', document);
    const api = { adminOverview: vi.fn().mockRejectedValue(new Error('offline')) };
    const view = mountAdminOverview(root as unknown as HTMLElement, api, vi.fn());
    await flush();

    expect(text(root)).toContain('运营数据暂不可用');
    expect(button(root, '重试')).toBeDefined();
    await view.dispose();
  });

  it('labels both account filters and the current-page checkbox', async () => {
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; },
    };
    const root = new Element('div', document);
    const api = { adminAccounts: vi.fn(async () => ({ items: [{ id: 1, status: 'active', roomId: null, invitationQuota: 0, hasObs: false, createdAt: '', updatedAt: '' }] })) };
    const view = mountAccountList(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAccountList>[1]);
    await flush();

    expect(control(root, 'select', '账号状态').getAttribute('aria-label')).toBe('账号状态');
    expect(control(root, 'select', '关注事项').getAttribute('aria-label')).toBe('关注事项');
    expect(control(root, 'input', '全选当前页').getAttribute('aria-label')).toBe('全选当前页');
    await view.dispose();
  });

  it('renders resource destinations as descriptive cards', async () => {
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; },
    };
    const root = new Element('div', document);
    mountAdminOverview(root as unknown as HTMLElement, { adminOverview: vi.fn(async () => ({ totalAccounts: 0, activeAccounts: 0, disabledAccounts: 0, missingRooms: 0, missingObs: 0, attention: [], recentEvents: [] })) }, () => undefined);
    await flush();

    expect(text(root)).toContain('管理直播间、邀请额度与 OBS');
    expect(text(root)).toContain('创建、分享与作废邀请码');
    expect(root.children.map((element) => element.className)).toEqual([
      'hosted-admin-metrics',
      'hosted-admin-resource-row',
      'hosted-admin-overview-bottom',
    ]);
  });

  it('keeps failed batch accounts selected and reports exact result counts', async () => {
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; },
    };
    const root = new Element('div', document);
    const api = {
      adminAccounts: vi.fn(async () => ({ items: [
        { id: 41, status: 'active', roomId: '123', invitationQuota: 8, hasObs: true, createdAt: '', updatedAt: '' },
        { id: 52, status: 'active', roomId: '456', invitationQuota: 8, hasObs: true, createdAt: '', updatedAt: '' },
      ] })),
      adminBatch: vi.fn(async () => [{ accountId: 41, status: 'succeeded' as const }, { accountId: 52, status: 'failed' as const }]),
    };
    const prompt = vi.fn(() => 'maintenance');
    vi.stubGlobal('prompt', prompt);
    const view = mountAccountList(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAccountList>[1]);
    await flush();
    const currentPage = control(root, 'input', '全选当前页');
    currentPage.checked = true;
    currentPage.listeners.get('change')?.();
    button(root, '停用').listeners.get('click')?.();
    await flush();

    expect(text(root)).toContain('1 个成功，1 个失败');
    expect(control(root, 'input', '选择主播账号 #52').checked).toBe(true);
    expect(control(root, 'input', '选择主播账号 #41').checked).toBe(false);
    vi.unstubAllGlobals();
    await view.dispose();
  });

  it('clears every selected account after an all-success batch and reports the exact count', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; } };
    const root = new Element('div', document);
    const api = { adminAccounts: vi.fn(async () => ({ items: [
      { id: 41, status: 'active', roomId: '123', invitationQuota: 8, hasObs: true, createdAt: '', updatedAt: '' },
      { id: 52, status: 'active', roomId: '456', invitationQuota: 8, hasObs: true, createdAt: '', updatedAt: '' },
    ] })), adminBatch: vi.fn(async () => [{ accountId: 41, status: 'succeeded' as const }, { accountId: 52, status: 'succeeded' as const }]) };
    vi.stubGlobal('prompt', vi.fn(() => 'maintenance'));
    const view = mountAccountList(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAccountList>[1]);
    await flush();
    const currentPage = control(root, 'input', '全选当前页'); currentPage.checked = true; currentPage.listeners.get('change')?.();
    button(root, '停用').listeners.get('click')?.();
    await flush();

    expect(text(root)).toContain('2 个账号操作成功');
    expect(control(root, 'input', '选择主播账号 #41').checked).toBe(false);
    expect(control(root, 'input', '选择主播账号 #52').checked).toBe(false);
    vi.unstubAllGlobals();
    await view.dispose();
  });

  it('shows only the safe rejected-request reason after a batch rejection', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; } };
    const root = new Element('div', document);
    const api = { adminAccounts: vi.fn(async () => ({ items: [{ id: 41, status: 'active', roomId: '123', invitationQuota: 8, hasObs: true, createdAt: '', updatedAt: '' }] })), adminBatch: vi.fn(async () => { throw new HostedAPIError('request_rejected', 400); }) };
    vi.stubGlobal('prompt', vi.fn(() => 'maintenance'));
    const view = mountAccountList(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAccountList>[1]);
    await flush();
    const currentPage = control(root, 'input', '全选当前页'); currentPage.checked = true; currentPage.listeners.get('change')?.();
    button(root, '停用').listeners.get('click')?.();
    await flush();

    expect(text(root)).toContain('批量操作失败：请求被拒绝，请重试');
    expect(text(root)).not.toContain('Hosted request failed');
    vi.unstubAllGlobals();
    await view.dispose();
  });

  it('retries the exact captured batch request without clearing its failed selection', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; } };
    const root = new Element('div', document);
    const api = { adminAccounts: vi.fn(async () => ({ items: [{ id: 41, status: 'active', roomId: '123', invitationQuota: 8, hasObs: true, createdAt: '', updatedAt: '' }] })), adminBatch: vi.fn(async () => { throw new HostedAPIError('temporarily_unavailable', 503); }) };
    vi.stubGlobal('prompt', vi.fn().mockReturnValueOnce('maintenance').mockReturnValueOnce('9'));
    const view = mountAccountList(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAccountList>[1]);
    await flush();
    const currentPage = control(root, 'input', '全选当前页'); currentPage.checked = true; currentPage.listeners.get('change')?.();
    button(root, '调整邀请额度').listeners.get('click')?.();
    await flush();
    button(root, '重试').listeners.get('click')?.();
    await flush();

    expect(api.adminBatch).toHaveBeenNthCalledWith(1, [41], 'set_invitation_quota', 'maintenance', 9);
    expect(api.adminBatch).toHaveBeenNthCalledWith(2, [41], 'set_invitation_quota', 'maintenance', 9);
    expect(control(root, 'input', '选择主播账号 #41').checked).toBe(true);
    vi.unstubAllGlobals();
    await view.dispose();
  });

  it('opens compact invitation creation under the inventory title', async () => {
    const api = {
      adminSession: vi.fn(async () => undefined),
      adminOverview: vi.fn(async () => ({totalAccounts:0,activeAccounts:0,disabledAccounts:0,missingRooms:0,missingObs:0,attention:[],recentEvents:[]})),
      adminInvitations: vi.fn(async () => ({invitations:[]})),
      createAdminInvitations: vi.fn(async () => []),
      revokeAdminInvitation: vi.fn(async () => undefined),
    };
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; },
    };
    const root = new Element('div', document);
    const mounted = mountAdminView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAdminView>[1]);

    await vi.waitFor(() => expect(button(root, '邀请码')).toBeDefined());
    button(root, '邀请码').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '创建邀请码')).toBeDefined());
    button(root, '创建邀请码').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '创建')).toBeDefined());
    expect(descendants(root).filter((element)=>element.className==='hosted-admin-invitation-create')).toHaveLength(1);
    await mounted.dispose();
  });

  it('does not let a late account-list response overwrite the next section', async () => {
    let resolveAccounts!: (value: {items: never[]}) => void;
    const accountsPending = new Promise<{items: never[]}>((resolve) => { resolveAccounts = resolve; });
    const api = {
      adminSession: vi.fn(async () => undefined),
      adminOverview: vi.fn(async () => ({totalAccounts:0,activeAccounts:0,disabledAccounts:0,missingRooms:0,missingObs:0,attention:[],recentEvents:[]})),
      adminAccounts: vi.fn(() => accountsPending),
      biliServiceStatus: vi.fn(async () => ({ version: 0, health: 'missing' as const })),
    };
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; },
    };
    const root = new Element('div', document);
    const mounted = mountAdminView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAdminView>[1]);

    await vi.waitFor(() => expect(button(root, '主播账号')).toBeDefined());
    button(root, '主播账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(api.adminAccounts).toHaveBeenCalledTimes(1));

    button(root, 'B站服务账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '更换服务账号')).toBeDefined());
    resolveAccounts({items:[]});
    await accountsPending;
    await Promise.resolve();
    await Promise.resolve();

    const status = descendants(root).find((element) => element.attributes.get('role') === 'status');
    expect(status?.textContent).toBe('');
    await mounted.dispose();
  });

  it('does not mount a routine TOTP prompt in the account workspace', async () => {
    const api = {
      adminSession: vi.fn(async () => undefined),
      adminOverview: vi.fn(async () => ({totalAccounts:0,activeAccounts:0,disabledAccounts:0,missingRooms:0,missingObs:0,attention:[],recentEvents:[]})),
      adminAccounts: vi.fn(async () => ({items:[]})),
      biliServiceStatus: vi.fn(async () => ({ version: 0, health: 'missing' as const })),
    };
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; },
    };
    const root = new Element('div', document);
    const mounted = mountAdminView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAdminView>[1]);

    await vi.waitFor(() => expect(button(root, '主播账号')).toBeDefined());
    button(root, '主播账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(api.adminAccounts).toHaveBeenCalledTimes(1));
    expect(descendants(root).some((element) => element.className === 'hosted-code-control')).toBe(false);
    await mounted.dispose();
  });

  it('keeps the Bilibili TOTP cells in a dedicated horizontal control', async () => {
    let poll!: () => void;
    const timers = {
      setTimeout(callback: () => void): number { poll = callback; return 1; },
      clearTimeout: vi.fn(),
      now: () => 0,
    };
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; },
    };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 4, health: 'healthy' as const, lastVerifiedAt: '2026-08-23T08:00:00Z' })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'challenge', qrImage: 'data:image/png;base64,AA==', expiresAt: '2026-08-23T08:10:00Z' })),
      pollBiliServiceChallenge: vi.fn(async () => ({ status: 'verified' as const })),
      cancelBiliServiceChallenge: vi.fn(async () => undefined),
      authorizeAdminOperation: vi.fn(),
      replaceBiliServiceCredential: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as never, timers);
    await flush();
    button(root, '更换服务账号').listeners.get('click')?.();
    await flush();
    poll();
    await flush();
    button(root, '二维码确认后继续').listeners.get('click')?.();
    await flush();

    const totp = descendants(root).find((element) => element.className.split(/\s+/).includes('hosted-admin-bili-totp'));
    const control = descendants(root).find((element) => element.className.split(/\s+/).includes('hosted-code-control'));
    expect(totp).toBeDefined();
    expect(control).toBeDefined();
    expect(control).not.toBe(totp);
    expect(control?.parent).toBe(totp);
    expect(control?.children.filter((element) => element.className === 'hosted-code-cell')).toHaveLength(6);
    await view.dispose();
  });
});
