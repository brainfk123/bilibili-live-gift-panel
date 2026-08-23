import { describe, expect, it } from 'vitest';

import { runAdminAction } from '../src/hosted/admin/ui/async-action';
import { mountAdminNotice } from '../src/hosted/admin/ui/notice';

class Element {
  children: Element[] = [];
  parent: Element | undefined;
  textContent = '';
  className = '';
  hidden = false;
  disabled = false;
  type = '';
  dataset: Record<string, string> = {};
  attributes = new Map<string, string>();
  listeners = new Map<string, () => void>();
  constructor(readonly tagName: string, readonly ownerDocument: DocumentLike) {}
  append(...nodes: Element[]): void { for (const node of nodes) { node.parent = this; this.children.push(node); } }
  remove(): void { if (this.parent) this.parent.children = this.parent.children.filter((child) => child !== this); }
  replaceChildren(...nodes: Element[]): void { this.children = []; this.append(...nodes); }
  setAttribute(name: string, value: string): void { this.attributes.set(name, value); }
  getAttribute(name: string): string | null { return this.attributes.get(name) ?? null; }
  removeAttribute(name: string): void { this.attributes.delete(name); }
  addEventListener(name: string, listener: () => void): void { this.listeners.set(name, listener); }
  click(): void { this.listeners.get('click')?.(); }
  querySelector<T extends Element>(selector: string): T | null {
    return descendants(this).find((element) => selector === 'button' ? element.tagName === 'button' : false) as T | null;
  }
}

interface DocumentLike { createElement(tag: string): Element; }
function descendants(root: Element): Element[] { return root.children.flatMap((child) => [child, ...descendants(child)]); }

describe('administrator interaction primitives', () => {
  it('closes a success notice without removing neighboring business content', () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const host = document.createElement('section');
    const table = document.createElement('div'); table.dataset.testid = 'inventory';
    host.append(table);
    const notice = mountAdminNotice(host as unknown as HTMLElement);

    notice.show('success', '已创建 1 个邀请码');
    notice.element.querySelector<HTMLButtonElement>('button')!.click();

    expect(host.children.find((element) => element.dataset.testid === 'inventory')).toBe(table);
    expect((notice.element as unknown as Element).hidden).toBe(true);
  });

  it('keeps a button busy until its operation settles', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
    const button = document.createElement('button'); button.textContent = '立即检查';
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => { finish = resolve; });

    const action = runAdminAction(button as unknown as HTMLButtonElement, { idle: '立即检查', busy: '检查中…' }, () => pending);
    expect(button.disabled).toBe(true);
    expect(button.getAttribute('aria-busy')).toBe('true');
    expect(button.textContent).toContain('检查中');
    finish();
    await action;
    expect(button.disabled).toBe(false);
    expect(button.getAttribute('aria-busy')).toBeNull();
  });
});
