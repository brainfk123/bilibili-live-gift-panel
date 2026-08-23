import { describe, expect, it, vi } from 'vitest';

import { mountAdminView } from '../src/hosted/admin';
import { mountAdminOverview } from '../src/hosted/admin/overview';
import { mountAccountList } from '../src/hosted/admin/accounts/list';
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
  it('labels both account filters and the current-page checkbox', async () => {
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (value) => { const node = new Element('#text', document); node.textContent = value; return node; },
    };
    const root = new Element('div', document);
    const api = { adminAccounts: vi.fn(async () => ({ items: [] })) };
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
});
