import { describe, expect, it, vi } from 'vitest';

import { mountAdminView } from '../src/hosted/admin';

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
  attributes = new Map<string, string>();
  listeners = new Map<string, () => void>();
  constructor(readonly tagName: string, readonly ownerDocument: DocumentLike) {}
  get firstElementChild() { return this.children[0]; }
  append(...nodes: Element[]) { for (const node of nodes) { node.remove(); node.parent = this; this.children.push(node); } }
  replaceChildren(...nodes: Element[]) { for (const child of this.children) child.parent = undefined; this.children = []; this.append(...nodes); }
  remove() { if (this.parent) { this.parent.children = this.parent.children.filter((child) => child !== this); this.parent = undefined; } }
  setAttribute(name: string, value: string) { this.attributes.set(name, value); }
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

describe('administrator section lifetime fence', () => {
  it('presents a generated invitation in the styled one-time-secret dialog', async () => {
    const api = {
      adminSession: vi.fn(async () => undefined),
      generateInvitation: vi.fn(async () => ({ code: 'AB12CD34' })),
    };
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; },
    };
    const root = new Element('div', document);
    const mounted = mountAdminView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAdminView>[1]);

    await vi.waitFor(() => expect(button(root, '邀请码')).toBeDefined());
    button(root, '邀请码').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '生成不限额度邀请码')).toBeDefined());
    button(root, '生成不限额度邀请码').listeners.get('click')?.();
    await vi.waitFor(() => expect(descendants(root).some((element) => element.tagName === 'dialog')).toBe(true));

    const dialog = descendants(root).find((element) => element.tagName === 'dialog');
    expect(dialog?.className).toBe('hosted-secret-dialog');
    expect(dialog?.children.map((element) => element.className)).toEqual([
      'hosted-secret-dialog-header',
      'hosted-secret-dialog-value',
      'hosted-secret-dialog-actions',
    ]);
    expect(descendants(dialog as Element).some((element) => element.textContent === '只显示一次，请先复制并妥善保存。')).toBe(true);
    expect(descendants(dialog as Element).find((element) => element.textContent === '复制邀请码')?.className).toBe('hosted-secret-dialog-primary');
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
    await vi.waitFor(() => expect(button(root, '创建服务账号验证')).toBeDefined());
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
