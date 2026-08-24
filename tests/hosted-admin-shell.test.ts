import { describe, expect, it, vi } from 'vitest';

import { mountAdminShell } from '../src/hosted/admin/shell';

class Element {
  children: Element[] = []; textContent = ''; className = ''; type = ''; disabled = false; hidden = false;
  attributes = new Map<string, string>(); listeners = new Map<string, (event?: { key?: string; shiftKey?: boolean; preventDefault?(): void }) => void>();
  constructor(readonly tagName: string, readonly ownerDocument: Doc) {}
  append(...nodes: Element[]) { this.children.push(...nodes); }
  replaceChildren(...nodes: Element[]) { this.children = nodes; }
  setAttribute(name: string, value: string) { this.attributes.set(name, value); }
  removeAttribute(name: string) { this.attributes.delete(name); }
  addEventListener(name: string, listener: (event?: { key?: string; shiftKey?: boolean; preventDefault?(): void }) => void) { this.listeners.set(name, listener); }
  removeEventListener(name: string) { this.listeners.delete(name); }
  focus() { this.ownerDocument.activeElement = this; }
}
interface Doc { activeElement?: Element; createElement(tag: string): Element }

describe('A3 administrator shell', () => {
  it('keeps one stable sidebar and disposes the old section before mounting the next', async () => {
    const document: Doc = { createElement: (tag) => new Element(tag, document) }; const root = new Element('div', document);
    const events: string[] = [];
    const shell = mountAdminShell(root as unknown as HTMLElement, {
      initial: 'overview', onLogout: vi.fn(), mount: (section, host) => {
        events.push(`mount:${section}`); host.textContent = section;
        return { dispose: async () => { events.push(`dispose:${section}`); } };
      },
    });
    const frame = root.children[0]; const sidebar = frame.children[0]; const header = frame.children[1].children[0];
    const buttons = sidebar.children.filter((child) => child.tagName === 'button' && child.className !== 'hosted-admin-sidebar-close');
    expect(buttons.map((item) => item.textContent)).toEqual(['运营总览', '主播账号', '邀请码', 'B站服务账号', '系统设置']);
    expect(buttons[0].attributes.get('aria-current')).toBe('page');
    buttons[1].listeners.get('click')?.();
    await vi.waitFor(() => expect(events).toEqual(['mount:overview', 'dispose:overview', 'mount:accounts']));
    expect(root.children[0].children[0]).toBe(sidebar); expect(root.children[0].children[1].children[0]).toBe(header);
    expect(buttons[1].attributes.get('aria-current')).toBe('page');
    await shell.dispose(); expect(events.at(-1)).toBe('dispose:accounts');
  });

  it('opens and closes the mobile navigation with focus restoration', async () => {
    const document: Doc = { createElement: (tag) => new Element(tag, document) }; const root = new Element('div', document);
    const shell = mountAdminShell(root as unknown as HTMLElement, { initial: 'overview', mount: () => ({ dispose() {} }) });
    const frame = root.children[0]; const sidebar = frame.children[0]; const workspace = frame.children[1]; const backdrop = frame.children[2]; const header = workspace.children[0];
    const trigger = header.children.find((child) => child.className === 'hosted-admin-menu-trigger');
    const close = sidebar.children.find((child) => child.className === 'hosted-admin-sidebar-close');
    if (!trigger) throw new Error('mobile menu trigger missing');
    if (!close) throw new Error('mobile menu close missing');
    trigger.listeners.get('click')?.();
    expect(sidebar.attributes.get('data-open')).toBe('true');
    expect(backdrop.hidden).toBe(false);
    expect(document.activeElement).toBe(sidebar.children[1]);
    close.listeners.get('click')?.();
    expect(sidebar.attributes.get('data-open')).toBe('false');
    expect(document.activeElement).toBe(trigger);
    trigger.listeners.get('click')?.();
    backdrop.listeners.get('click')?.();
    expect(sidebar.attributes.get('data-open')).toBe('false');
    expect(document.activeElement).toBe(trigger);
    trigger.listeners.get('click')?.();
    frame.listeners.get('keydown')?.({ key: 'Escape', preventDefault: vi.fn() });
    expect(sidebar.attributes.get('data-open')).toBe('false');
    expect(document.activeElement).toBe(trigger);
    trigger.listeners.get('click')?.();
    sidebar.children[2].listeners.get('click')?.();
    await vi.waitFor(() => expect(sidebar.attributes.get('data-open')).toBe('false'));
    await shell.dispose();
  });

  it('lets overview content navigate through the same serialized section transition', async () => {
    const document: Doc = { createElement: (tag) => new Element(tag, document) }; const root = new Element('div', document);
    const events: string[] = [];
    const shell = mountAdminShell(root as unknown as HTMLElement, {
      initial: 'overview', mount: (section, host, navigate) => {
        events.push(`mount:${section}`);
        if (section === 'overview') {
          const open = document.createElement('button'); open.textContent = '进入账号'; open.addEventListener('click', () => navigate('accounts')); (host as unknown as Element).append(open);
        }
        return { dispose: () => { events.push(`dispose:${section}`); } };
      },
    });
    const content = root.children[0].children[1].children[1];
    content.children[0].listeners.get('click')?.();
    await vi.waitFor(() => expect(events).toEqual(['mount:overview', 'dispose:overview', 'mount:accounts']));
    await shell.dispose();
  });
});
