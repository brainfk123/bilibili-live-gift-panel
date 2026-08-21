import { describe, expect, it, vi } from 'vitest';

import { HostedAPIError } from '../src/hosted/api';
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
  it('does not let a late account mutation overwrite the next section status', async () => {
    let resolveDisable!: () => void;
    const disablePending = new Promise<void>((resolve) => { resolveDisable = resolve; });
    const api = {
      adminSession: vi.fn(async () => undefined),
      disableAccount: vi.fn(() => disablePending),
      biliServiceStatus: vi.fn(async () => ({ version: 0, health: 'missing' as const })),
    };
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; },
    };
    const root = new Element('div', document);
    const mounted = mountAdminView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAdminView>[1]);

    await vi.waitFor(() => expect(button(root, '账号')).toBeDefined());
    button(root, '账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '停用账号')).toBeDefined());
    button(root, '停用账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(api.disableAccount).toHaveBeenCalledTimes(1));

    button(root, '服务账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '创建服务账号验证')).toBeDefined());
    resolveDisable();
    await disablePending;
    await Promise.resolve();
    await Promise.resolve();

    const status = descendants(root).find((element) => element.attributes.get('role') === 'status');
    expect(status?.textContent).toBe('');
    await mounted.dispose();
  });

  it('does not mount a TOTP prompt after its account section is disposed', async () => {
    let rejectDisable!: (error: Error) => void;
    const disablePending = new Promise<void>((_resolve, reject) => { rejectDisable = reject; });
    const api = {
      adminSession: vi.fn(async () => undefined),
      disableAccount: vi.fn(() => disablePending),
      biliServiceStatus: vi.fn(async () => ({ version: 0, health: 'missing' as const })),
    };
    const document: DocumentLike = {
      createElement: (tag) => new Element(tag, document),
      createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; },
    };
    const root = new Element('div', document);
    const mounted = mountAdminView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountAdminView>[1]);

    await vi.waitFor(() => expect(button(root, '账号')).toBeDefined());
    button(root, '账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '停用账号')).toBeDefined());
    button(root, '停用账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(api.disableAccount).toHaveBeenCalledTimes(1));
    button(root, '服务账号').listeners.get('click')?.();
    await vi.waitFor(() => expect(button(root, '创建服务账号验证')).toBeDefined());
    rejectDisable(new HostedAPIError('recent_totp_required', 403));
    await disablePending.catch(() => undefined);
    await Promise.resolve();
    await Promise.resolve();

    expect(document.activeElement).toBeUndefined();
    expect(descendants(root).some((element) => element.className === 'hosted-code-control')).toBe(false);
    await mounted.dispose();
  });
});
