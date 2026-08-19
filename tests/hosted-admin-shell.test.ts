import { describe, expect, it, vi } from 'vitest';

import { mountAdminShell } from '../src/hosted/admin/shell';

class Element {
  children: Element[] = []; textContent = ''; className = ''; type = ''; disabled = false;
  attributes = new Map<string, string>(); listeners = new Map<string, () => void>();
  constructor(readonly tagName: string, readonly ownerDocument: Doc) {}
  append(...nodes: Element[]) { this.children.push(...nodes); }
  replaceChildren(...nodes: Element[]) { this.children = nodes; }
  setAttribute(name: string, value: string) { this.attributes.set(name, value); }
  removeAttribute(name: string) { this.attributes.delete(name); }
  addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
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
    const buttons = sidebar.children.filter((child) => child.tagName === 'button');
    expect(buttons.map((item) => item.textContent)).toEqual(['总览', '账号', '邀请', '服务账号', 'OBS', '安全与恢复']);
    expect(buttons[0].attributes.get('aria-current')).toBe('page');
    buttons[1].listeners.get('click')?.();
    await vi.waitFor(() => expect(events).toEqual(['mount:overview', 'dispose:overview', 'mount:accounts']));
    expect(root.children[0].children[0]).toBe(sidebar); expect(root.children[0].children[1].children[0]).toBe(header);
    expect(buttons[1].attributes.get('aria-current')).toBe('page');
    await shell.dispose(); expect(events.at(-1)).toBe('dispose:accounts');
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
