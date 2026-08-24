import { describe, expect, it, vi } from 'vitest';

import { mountAccountDetail } from '../src/hosted/admin/accounts/detail';

type EventLike = { key?: string; shiftKey?: boolean; target?: Element; preventDefault?(): void };

class Element {
  children: Element[] = [];
  parent?: Element;
  textContent = '';
  className = '';
  type = '';
  value = '';
  inputMode = '';
  disabled = false;
  dataset: Record<string, string> = {};
  style = { width: '' };
  offsetWidth = 100;
  attributes = new Map<string, string>();
  listeners = new Map<string, (event?: EventLike) => void>();

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
  getAttribute(name: string) { return this.attributes.get(name) ?? null; }
  removeAttribute(name: string) { this.attributes.delete(name); }
  addEventListener(name: string, listener: (event?: EventLike) => void) { this.listeners.set(name, listener); }
  removeEventListener(name: string) { this.listeners.delete(name); }
  focus() { this.ownerDocument.activeElement = this; }
  querySelectorAll(): Element[] {
    return descendants(this).filter((element) => (element.tagName === 'button' || element.tagName === 'input') && !element.disabled);
  }
}

interface DocumentLike {
  activeElement?: Element;
  createElement(tag: string): Element;
}

function descendants(root: Element): Element[] {
  return [root, ...root.children.flatMap(descendants)];
}

function button(root: Element, label: string): Element {
  const found = descendants(root).find((element) => element.tagName === 'button' && element.textContent === label);
  if (!found) throw new Error(`button not found: ${label}`);
  return found;
}

function text(root: Element): string {
  return descendants(root).map((element) => element.textContent).join('');
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

const detail = {
  id: 1,
  status: 'active' as const,
  invitationQuota: 3,
  hasObs: false,
  createdAt: '2026-08-23T08:00:00Z',
  updatedAt: '2026-08-23T08:00:00Z',
  recentEvents: [],
};

function fixture(overrides: Record<string, unknown> = {}) {
  return {
    adminAccount: vi.fn(async () => detail),
    updateAdminRoom: vi.fn(async () => ({ ...detail, roomId: '100001' })),
    adjustQuota: vi.fn(async () => detail),
    disableAccount: vi.fn(async () => detail),
    enableAccount: vi.fn(async () => detail),
    issueOBSCredential: vi.fn(async () => ({ ...detail, hasObs: true })),
    ...overrides,
  };
}

describe('administrator account detail drawer', () => {
  it('closes from the top-right button, backdrop and Escape and restores trigger focus', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const trigger = new Element('button', document);
    const host = new Element('aside', document);
    let view = mountAccountDetail(host as unknown as HTMLElement, fixture() as never, 1, vi.fn(), trigger as unknown as HTMLElement);
    await flush();

    const close = descendants(host).find((element) => element.attributes.get('aria-label') === '关闭账号详情');
    if (!close) throw new Error('close button missing');
    close.listeners.get('click')?.();
    expect(host.children).toHaveLength(0);
    expect(document.activeElement).toBe(trigger);

    view = mountAccountDetail(host as unknown as HTMLElement, fixture() as never, 1, vi.fn(), trigger as unknown as HTMLElement);
    await flush();
    host.listeners.get('click')?.({ target: host });
    expect(host.children).toHaveLength(0);

    view = mountAccountDetail(host as unknown as HTMLElement, fixture() as never, 1, vi.fn(), trigger as unknown as HTMLElement);
    await flush();
    host.listeners.get('keydown')?.({ key: 'Escape', preventDefault: vi.fn() });
    expect(host.children).toHaveLength(0);
    await view.dispose();
    expect(host.listeners.has('click')).toBe(false);
    expect(host.listeners.has('keydown')).toBe(false);
  });

  it('serializes drawer writes, keeps it open while pending and preserves success feedback', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const host = new Element('aside', document);
    let resolveRoom!: (value: typeof detail & { roomId: string }) => void;
    const pending = new Promise<typeof detail & { roomId: string }>((resolve) => { resolveRoom = resolve; });
    const api = fixture({ updateAdminRoom: vi.fn(() => pending) });
    const view = mountAccountDetail(host as unknown as HTMLElement, api as never, 1, vi.fn());
    await flush();

    button(host, '保存').listeners.get('click')?.();
    button(host, '创建 OBS 地址').listeners.get('click')?.();
    expect(api.issueOBSCredential).not.toHaveBeenCalled();
    host.listeners.get('keydown')?.({ key: 'Escape', preventDefault: vi.fn() });
    expect(host.children.length).toBeGreaterThan(0);

    resolveRoom({ ...detail, roomId: '100001' });
    await pending;
    await flush();
    expect(text(host)).toContain('直播间已保存');
    expect(descendants(host)).toContain(document.activeElement);
    expect(document.activeElement?.getAttribute('aria-label')).toBe('保存直播间');
    host.listeners.get('keydown')?.({ key: 'Escape', preventDefault: vi.fn() });
    expect(host.children).toHaveLength(0);
    await view.dispose();
  });

  it('cycles Tab focus inside the modal drawer', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const host = new Element('aside', document);
    const view = mountAccountDetail(host as unknown as HTMLElement, fixture() as never, 1, vi.fn());
    await flush();
    const close = descendants(host).find((element) => element.attributes.get('aria-label') === '关闭账号详情');
    const last = button(host, '停用账号');
    if (!close) throw new Error('close button missing');
    last.focus();
    const prevented = vi.fn();
    host.listeners.get('keydown')?.({ key: 'Tab', preventDefault: prevented });
    expect(prevented).toHaveBeenCalledTimes(1);
    expect(document.activeElement).toBe(close);
    await view.dispose();
  });
});
