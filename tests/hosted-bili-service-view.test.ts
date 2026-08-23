import { describe, expect, it, vi } from 'vitest';

import { createBiliServiceController } from '../src/hosted/admin/bili-service-controller';
import { mountBiliServiceView } from '../src/hosted/admin/bili-service';

class Element {
  children: Element[] = []; parent?: Element; textContent = ''; className = ''; type = ''; disabled = false;
  src = ''; alt = ''; hidden = false; dataset: Record<string, string> = {}; style = { width: '' }; offsetWidth = 100;
  attributes = new Map<string, string>(); listeners = new Map<string, () => void>();
  constructor(readonly tagName: string, readonly ownerDocument: DocumentLike) {}
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
interface DocumentLike { activeElement?: Element; createElement(tag: string): Element; createTextNode(text: string): Element; }
const descendants = (root: Element): Element[] => [root, ...root.children.flatMap(descendants)];
const button = (root: Element, label: string): Element => {
  const found = descendants(root).find((node) => node.tagName === 'button' && node.textContent === label);
  if (!found) throw new Error(`button not found: ${label}`);
  return found;
};
const flush = async (): Promise<void> => { await Promise.resolve(); await Promise.resolve(); };

describe('Bilibili service action state', () => {
  it('publishes checking and a visible success result', async () => {
    const api = { checkBiliService: vi.fn(async () => ({ version: 1, health: 'healthy' as const, maskedUid: '****9588', lastVerifiedAt: '2030-01-01T00:00:00Z' })) };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render);

    await controller.check();

    expect(render.mock.calls.map(([state]) => state.phase)).toContain('checking');
    expect(render.mock.lastCall?.[0].notice).toEqual({ kind: 'success', message: '检查完成，服务账号运行正常' });
  });

  it('fences a stale check after beginning replacement', async () => {
    let resolveCheck!: (value: { version: number; health: 'healthy'; lastVerifiedAt: string }) => void;
    const api = {
      checkBiliService: vi.fn(() => new Promise((resolve) => { resolveCheck = resolve; })),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'new', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
    };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render);
    const stale = controller.check();
    await controller.beginReplacement();
    resolveCheck({ version: 1, health: 'healthy', lastVerifiedAt: '2030-01-01T00:00:00Z' });
    await stale;

    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'qr', challenge: { challengeId: 'new' } });
  });

  it('publishes authorizing only after a QR challenge enters the TOTP step', async () => {
    const api = { beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'replace', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })) };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render);

    controller.enterAuthorization();
    expect(render).not.toHaveBeenCalled();
    await controller.beginReplacement();
    controller.enterAuthorization();

    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'authorizing', challenge: { challengeId: 'replace' } });
  });
});

describe('Bilibili service replacement view', () => {
  it('keeps the continue button below a bounded QR image', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'replace', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1]);
    await flush();
    button(root, '更换服务账号').listeners.get('click')?.();
    await flush();

    const flow = descendants(root).find((node) => node.className === 'hosted-admin-bili-flow');
    expect(flow?.children.map((node) => node.className)).toEqual([
      'hosted-admin-bili-step', 'hosted-admin-bili-qr', 'hosted-admin-bili-flow-actions',
    ]);
    const image = descendants(root).find((node) => node.tagName === 'img');
    expect(image?.getAttribute('width')).toBe('448');
    expect(image?.getAttribute('height')).toBe('448');
    button(root, '二维码确认后继续').listeners.get('click')?.();
    expect(descendants(root).some((node) => node.className.includes('hosted-admin-bili-totp'))).toBe(true);
    await view.dispose();
  });
});
