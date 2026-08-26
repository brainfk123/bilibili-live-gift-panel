import { describe, expect, it, vi } from 'vitest';

import { HostedAPIError } from '../src/hosted/api';
import type { BiliChallengeTimerPort } from '../src/hosted/bili-challenge-poller';
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

class ControlledTimers implements BiliChallengeTimerPort {
  private clock = 0;
  private nextID = 1;
  private readonly scheduled = new Map<number, { callback: () => void; dueAt: number }>();

  setTimeout(callback: () => void, milliseconds: number): number {
    const id = this.nextID++;
    this.scheduled.set(id, { callback, dueAt: this.clock + milliseconds });
    return id;
  }

  clearTimeout(id: number): void { this.scheduled.delete(id); }
  now(): number { return this.clock; }
  count(): number { return this.scheduled.size; }
  nextDelay(): number | undefined {
    const next = [...this.scheduled.values()].sort((left, right) => left.dueAt - right.dueAt)[0];
    return next ? next.dueAt - this.clock : undefined;
  }

  async fireNext(): Promise<void> {
    const next = [...this.scheduled.entries()].sort((left, right) => left[1].dueAt - right[1].dueAt)[0];
    if (!next) throw new Error('No controlled timer is scheduled.');
    const [id, task] = next;
    this.scheduled.delete(id);
    this.clock = task.dueAt;
    task.callback();
    for (let turn = 0; turn < 5; turn++) await Promise.resolve();
  }
}

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

    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'qr', challenge: { qrImage: 'data:image/png;base64,qr' } });
  });

  it('publishes authorizing only after a QR challenge enters the TOTP step', async () => {
    const timers = new ControlledTimers();
    const api = {
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'replace', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      pollBiliServiceChallenge: vi.fn(async () => ({ status: 'verified' as const })),
    };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render, timers);

    controller.enterAuthorization();
    expect(render).not.toHaveBeenCalled();
    await controller.beginReplacement();
    controller.enterAuthorization();
    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'qr', challengeStatus: 'pending' });
    await timers.fireNext();
    controller.enterAuthorization();

    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'authorizing', challenge: { qrImage: 'data:image/png;base64,qr' } });
  });

  it('keeps regeneration single-flight until the old poll stops and challenge is cancelled', async () => {
    const timers = new ControlledTimers();
    const events: string[] = [];
    let challengeNumber = 0;
    let releasePoll!: (value: { status: 'pending' }) => void;
    const inFlightPoll = new Promise<{ status: 'pending' }>((resolve) => { releasePoll = resolve; });
    const api = {
      beginBiliServiceChallenge: vi.fn(async () => {
        const id = `challenge-${++challengeNumber}`;
        events.push(`begin:${id}`);
        return { challengeId: id, qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' };
      }),
      pollBiliServiceChallenge: vi.fn(() => inFlightPoll),
      cancelBiliServiceChallenge: vi.fn(async (id: string) => { events.push(`cancel:${id}`); }),
    };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render, timers);
    await controller.beginReplacement();
    await timers.fireNext();

    const first = controller.beginReplacement();
    const second = controller.beginReplacement();
    await flush();

    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'creating' });
    expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(1);
    expect(api.cancelBiliServiceChallenge).not.toHaveBeenCalled();

    releasePoll({ status: 'pending' });
    await Promise.all([first, second]);

    expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(2);
    expect(events).toEqual(['begin:challenge-1', 'cancel:challenge-1', 'begin:challenge-2']);
    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'qr', challenge: { qrImage: 'data:image/png;base64,qr' } });
    await controller.dispose();
  });

  it('cancels a challenge created after cancel invalidates its begin operation', async () => {
    let releaseBegin!: (value: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const creating = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { releaseBegin = resolve; });
    const api = {
      beginBiliServiceChallenge: vi.fn(() => creating),
      cancelBiliServiceChallenge: vi.fn(async () => undefined),
    };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render, new ControlledTimers());
    const starting = controller.beginReplacement();
    await vi.waitFor(() => expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(1));
    const cancelling = controller.cancelReplacement();
    releaseBegin({ challengeId: 'late-after-cancel', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' });

    await Promise.all([starting, cancelling]);

    expect(api.cancelBiliServiceChallenge).toHaveBeenCalledWith('late-after-cancel');
    expect(JSON.stringify(render.mock.calls)).not.toContain('late-after-cancel');
  });

  it('dispose joins begin and cancels its late-created challenge', async () => {
    let releaseBegin!: (value: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const creating = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { releaseBegin = resolve; });
    const api = {
      beginBiliServiceChallenge: vi.fn(() => creating),
      cancelBiliServiceChallenge: vi.fn(async () => undefined),
    };
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], vi.fn(), new ControlledTimers());
    const starting = controller.beginReplacement();
    await vi.waitFor(() => expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(1));
    const disposing = controller.dispose();
    releaseBegin({ challengeId: 'late-after-dispose', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' });

    await Promise.all([starting, disposing]);

    expect(api.cancelBiliServiceChallenge).toHaveBeenCalledWith('late-after-dispose');
  });

  it('blocks regeneration and retains cleanup ownership when DELETE is rejected', async () => {
    let challengeNumber = 0;
    const rawFailure = new Error('RAW DELETE ERROR challenge-1-private');
    const api = {
      beginBiliServiceChallenge: vi.fn(async () => ({
        challengeId: `challenge-${++challengeNumber}-private`,
        qrImage: 'data:image/png;base64,qr',
        expiresAt: '2030-01-01T00:05:00Z',
      })),
      cancelBiliServiceChallenge: vi.fn()
        .mockRejectedValueOnce(rawFailure)
        .mockResolvedValue(undefined),
    };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render, new ControlledTimers());
    await controller.beginReplacement();

    await expect(controller.beginReplacement()).rejects.toMatchObject({ code: 'operation_failed' });

    expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(1);
    expect(api.cancelBiliServiceChallenge).toHaveBeenCalledTimes(1);
    const failed = render.mock.lastCall?.[0];
    expect(failed).toMatchObject({
      phase: 'cleanup_failed',
      notice: { kind: 'error', message: '二维码清理失败，请重试取消或重新生成' },
    });
    expect(JSON.stringify(failed)).not.toContain('challenge-1-private');
    expect(JSON.stringify(failed)).not.toContain(rawFailure.message);

    await controller.beginReplacement();

    expect(api.cancelBiliServiceChallenge).toHaveBeenNthCalledWith(2, 'challenge-1-private');
    expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(2);
    await controller.dispose();
  });

  it('rejects failed cancel and retries the retained challenge ID', async () => {
    const rawFailure = new Error('RAW CANCEL ERROR cancel-private');
    const api = {
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'cancel-private', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      cancelBiliServiceChallenge: vi.fn()
        .mockRejectedValueOnce(rawFailure)
        .mockResolvedValue(undefined),
    };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render, new ControlledTimers());
    await controller.beginReplacement();

    await expect(controller.cancelReplacement()).rejects.toMatchObject({ code: 'operation_failed' });

    const failed = render.mock.lastCall?.[0];
    expect(failed).toMatchObject({ phase: 'cleanup_failed' });
    expect(JSON.stringify(failed)).not.toContain('cancel-private');
    expect(JSON.stringify(failed)).not.toContain(rawFailure.message);

    await controller.cancelReplacement();

    expect(api.cancelBiliServiceChallenge).toHaveBeenNthCalledWith(2, 'cancel-private');
    expect(render.mock.lastCall?.[0]).toMatchObject({ phase: 'idle' });
  });

  it('rejects failed dispose and retries retained cleanup on the next dispose', async () => {
    const api = {
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'dispose-private', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      cancelBiliServiceChallenge: vi.fn()
        .mockRejectedValueOnce(new Error('RAW DISPOSE ERROR dispose-private'))
        .mockResolvedValue(undefined),
    };
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], vi.fn(), new ControlledTimers());
    await controller.beginReplacement();

    await expect(controller.dispose()).rejects.toMatchObject({ code: 'operation_failed' });
    await controller.dispose();

    expect(api.cancelBiliServiceChallenge).toHaveBeenCalledTimes(2);
    expect(api.cancelBiliServiceChallenge).toHaveBeenNthCalledWith(1, 'dispose-private');
    expect(api.cancelBiliServiceChallenge).toHaveBeenNthCalledWith(2, 'dispose-private');
  });

  it('retains a late-created challenge when its invalidation cleanup is rejected', async () => {
    let releaseBegin!: (value: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const creating = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { releaseBegin = resolve; });
    const rawFailure = new Error('RAW LATE ERROR late-private');
    const api = {
      beginBiliServiceChallenge: vi.fn(() => creating),
      cancelBiliServiceChallenge: vi.fn()
        .mockRejectedValueOnce(rawFailure)
        .mockResolvedValue(undefined),
    };
    const render = vi.fn();
    const controller = createBiliServiceController(api as Parameters<typeof createBiliServiceController>[0], render, new ControlledTimers());
    const starting = controller.beginReplacement();
    await vi.waitFor(() => expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(1));
    const cancelling = controller.cancelReplacement();
    releaseBegin({ challengeId: 'late-private', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' });

    const results = await Promise.allSettled([starting, cancelling]);

    expect(results.map((result) => result.status)).toEqual(['rejected', 'rejected']);
    expect(results.every((result) => result.status === 'rejected' && result.reason instanceof HostedAPIError && result.reason.code === 'operation_failed')).toBe(true);
    const failed = render.mock.lastCall?.[0];
    expect(failed).toMatchObject({ phase: 'cleanup_failed' });
    expect(JSON.stringify(failed)).not.toContain('late-private');
    expect(JSON.stringify(failed)).not.toContain(rawFailure.message);

    await controller.cancelReplacement();

    expect(api.cancelBiliServiceChallenge).toHaveBeenNthCalledWith(2, 'late-private');
  });
});

describe('Bilibili service replacement view', () => {
  it('clears secret DOM while surfacing dispose cleanup failure and permits retry', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'dispose-view-private', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      pollBiliServiceChallenge: vi.fn(async () => ({ status: 'pending' as const })),
      cancelBiliServiceChallenge: vi.fn()
        .mockRejectedValueOnce(new Error('RAW VIEW DISPOSE ERROR'))
        .mockResolvedValue(undefined),
      replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1], new ControlledTimers());
    await flush();
    button(root, '更换服务账号').listeners.get('click')?.();
    await flush();
    expect(descendants(root).some((node) => node.tagName === 'img')).toBe(true);

    await expect(view.dispose()).rejects.toMatchObject({ code: 'operation_failed' });

    expect(descendants(root).some((node) => node.tagName === 'img')).toBe(false);
    expect(descendants(root).some((node) => node.className.includes('hosted-admin-bili-totp'))).toBe(false);
    await view.dispose();
    expect(api.cancelBiliServiceChallenge).toHaveBeenNthCalledWith(2, 'dispose-view-private');
  });

  it('renders only safe retry guidance when verified challenge cleanup fails', async () => {
    const timers = new ControlledTimers();
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const rawFailure = new Error('RAW UI CLEANUP ERROR ui-private');
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'ui-private', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      pollBiliServiceChallenge: vi.fn(async () => ({ status: 'verified' as const })),
      cancelBiliServiceChallenge: vi.fn()
        .mockRejectedValueOnce(rawFailure)
        .mockResolvedValue(undefined),
      replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1], timers);
    await flush();
    button(root, '更换服务账号').listeners.get('click')?.();
    await flush();
    await timers.fireNext();
    expect(button(root, '二维码确认后继续').disabled).toBe(false);

    button(root, '取消').listeners.get('click')?.();
    await vi.waitFor(() => expect(descendants(root).some((node) => node.textContent === '二维码清理失败，请重试取消或重新生成')).toBe(true));

    expect(descendants(root).some((node) => node.textContent === '二维码已确认，可以继续')).toBe(false);
    expect(button(root, '二维码确认后继续').disabled).toBe(true);
    expect(button(root, '重新生成').disabled).toBe(false);
    expect(button(root, '取消').disabled).toBe(false);
    const rendered = descendants(root).flatMap((node) => [node.textContent, node.src, node.alt, ...node.attributes.values()]).join('|');
    expect(rendered).not.toContain('ui-private');
    expect(rendered).not.toContain(rawFailure.message);
    await view.dispose();
  });

  it('never renders masked UID material in visible text or DOM attributes', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 1, health: 'healthy' as const, maskedUid: '****UID-PRIVATE-9588', lastVerifiedAt: '2030-01-01T00:00:00Z' })),
      checkBiliService: vi.fn(), beginBiliServiceChallenge: vi.fn(), pollBiliServiceChallenge: vi.fn(), cancelBiliServiceChallenge: vi.fn(), replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1]);
    await flush();

    const rendered = descendants(root).flatMap((node) => [
      node.textContent,
      node.src,
      node.alt,
      ...node.attributes.values(),
      ...Object.values(node.dataset),
    ]).join('|');
    expect(rendered).not.toContain('UID-PRIVATE-9588');
    expect(rendered).not.toMatch(/\bUID\b/i);
    await view.dispose();
  });

  it('shows a loading state and withholds actions until the initial status resolves', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = { biliServiceStatus: vi.fn(() => new Promise(() => undefined)), checkBiliService: vi.fn(), beginBiliServiceChallenge: vi.fn(), replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn() };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as never);

    expect(descendants(root).some((node) => node.className.includes('hosted-admin-state'))).toBe(true);
    expect(descendants(root).some((node) => node.textContent.includes('正在加载服务账号状态'))).toBe(true);
    expect(button(root, '立即检查').hidden).toBe(true);
    expect(button(root, '更换服务账号').hidden).toBe(true);
    await view.dispose();
  });

  it('replaces the loading state with the error notice when status loading fails', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = { biliServiceStatus: vi.fn(async () => { throw new Error('offline'); }), checkBiliService: vi.fn(), beginBiliServiceChallenge: vi.fn(), replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn() };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as never);
    await flush();

    expect(descendants(root).some((node) => node.className.includes('hosted-admin-state'))).toBe(false);
    expect(descendants(root).some((node) => node.textContent.includes('服务账号状态暂不可用'))).toBe(true);
    await view.dispose();
  });

  it('keeps the currently rendered check button visibly busy until a deferred check settles', async () => {
    let finish!: (status: { version: number; health: 'healthy'; lastVerifiedAt: string }) => void;
    const pending = new Promise<{ version: number; health: 'healthy'; lastVerifiedAt: string }>((resolve) => { finish = resolve; });
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(() => pending), beginBiliServiceChallenge: vi.fn(), replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1]);
    await flush();
    button(root, '立即检查').listeners.get('click')?.();

    const visible = button(root, '检查中…');
    expect(visible.disabled).toBe(true);
    expect(visible.getAttribute('aria-busy')).toBe('true');
    expect(descendants(visible).some((node) => node.className === 'hosted-admin-action-spinner')).toBe(true);
    finish({ version: 1, health: 'healthy', lastVerifiedAt: '2030-01-01T00:00:00Z' });
    await flush();

    expect(button(root, '立即检查').disabled).toBe(false);
    expect(descendants(root).some((node) => node.textContent === '检查完成，服务账号运行正常')).toBe(true);
    await view.dispose();
  });

  it('keeps the continue button below a bounded QR image while confirmation is pending', async () => {
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'replace', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      cancelBiliServiceChallenge: vi.fn(async () => undefined),
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
    expect(button(root, '二维码确认后继续').disabled).toBe(true);
    button(root, '二维码确认后继续').listeners.get('click')?.();
    expect(descendants(root).some((node) => node.className.includes('hosted-admin-bili-totp'))).toBe(false);
    await view.dispose();
  });

  it('shows scanned then enables continue only after verified', async () => {
    const timers = new ControlledTimers();
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'service-challenge-secret', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      pollBiliServiceChallenge: vi.fn()
        .mockResolvedValueOnce({ status: 'pending' as const })
        .mockResolvedValueOnce({ status: 'scanned' as const })
        .mockResolvedValueOnce({ status: 'verified' as const }),
      cancelBiliServiceChallenge: vi.fn(async () => undefined),
      replaceBiliServiceCredential: vi.fn(),
      authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1], timers);
    await flush();
    button(root, '更换服务账号').listeners.get('click')?.();
    await flush();

    expect(button(root, '二维码确认后继续').disabled).toBe(true);
    expect(timers.count()).toBe(1);
    expect(timers.nextDelay()).toBe(6_000);
    await timers.fireNext();
    expect(button(root, '二维码确认后继续').disabled).toBe(true);
    expect(timers.nextDelay()).toBe(6_000);
    await timers.fireNext();
    expect(button(root, '二维码确认后继续').disabled).toBe(true);
    expect(descendants(root).some((node) => node.textContent === '已扫码，请在手机确认')).toBe(true);
    expect(timers.nextDelay()).toBe(6_000);
    await timers.fireNext();

    expect(button(root, '二维码确认后继续').disabled).toBe(false);
    expect(timers.count()).toBe(0);
    expect(descendants(root).flatMap((node) => [node.textContent, node.src]).join('|')).not.toContain('service-challenge-secret');
    button(root, '二维码确认后继续').listeners.get('click')?.();
    expect(descendants(root).some((node) => node.className.includes('hosted-admin-bili-totp'))).toBe(true);
    await view.dispose();
  });

  it('retains the same QR through temporary unavailability and resumes polling successfully', async () => {
    const timers = new ControlledTimers();
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const cancelBiliServiceChallenge = vi.fn(async () => undefined);
    const pollBiliServiceChallenge = vi.fn()
      .mockRejectedValueOnce(new HostedAPIError('temporarily_unavailable', 503))
      .mockResolvedValueOnce({ status: 'scanned' as const });
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({
        challengeId: 'admin-private-challenge',
        qrImage: 'data:image/png;base64,retained-qr',
        expiresAt: '2030-01-01T00:05:00Z',
      })),
      pollBiliServiceChallenge,
      cancelBiliServiceChallenge,
      replaceBiliServiceCredential: vi.fn(),
      authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1], timers);
    await flush();
    button(root, '更换服务账号').listeners.get('click')?.();
    await flush();

    await timers.fireNext();

    expect(descendants(root).some((node) => node.textContent === '登录服务暂不可用，稍后将自动重试')).toBe(true);
    expect(descendants(root).find((node) => node.tagName === 'img')?.src).toBe('data:image/png;base64,retained-qr');
    expect(descendants(root).flatMap((node) => [node.textContent, node.src, ...node.attributes.values()]).join('|')).not.toContain('admin-private-challenge');
    expect(cancelBiliServiceChallenge).not.toHaveBeenCalled();
    expect(timers.nextDelay()).toBe(2_000);

    await timers.fireNext();
    await timers.fireNext();

    expect(pollBiliServiceChallenge).toHaveBeenCalledTimes(2);
    expect(descendants(root).some((node) => node.textContent === '已扫码，请在手机确认')).toBe(true);
    expect(descendants(root).find((node) => node.tagName === 'img')?.src).toBe('data:image/png;base64,retained-qr');
    expect(cancelBiliServiceChallenge).not.toHaveBeenCalled();
    expect(timers.nextDelay()).toBe(6_000);
    await view.dispose();
  });

  it('clears service challenge polling on cancel and dispose', async () => {
    const mount = async () => {
      const timers = new ControlledTimers();
      const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
      const root = new Element('div', document);
      const api = {
        biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
        checkBiliService: vi.fn(),
        beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'replace', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
        pollBiliServiceChallenge: vi.fn(async () => ({ status: 'pending' as const })),
        cancelBiliServiceChallenge: vi.fn(async () => undefined),
        replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn(),
      };
      const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1], timers);
      await flush();
      button(root, '更换服务账号').listeners.get('click')?.();
      await flush();
      return { timers, root, view };
    };

    const cancelled = await mount();
    expect(cancelled.timers.count()).toBe(1);
    button(cancelled.root, '取消').listeners.get('click')?.();
    expect(cancelled.timers.count()).toBe(0);
    await cancelled.view.dispose();

    const disposed = await mount();
    expect(disposed.timers.count()).toBe(1);
    await disposed.view.dispose();
    expect(disposed.timers.count()).toBe(0);
  });

  it.each([
    ['expired', new HostedAPIError('expired', 410), '二维码已过期，请重新生成'],
    ['fatal', new HostedAPIError('invalid_response', 200), '登录响应无效，请重新生成二维码'],
  ])('never enters TOTP after an %s service challenge poll', async (_kind, pollError, guidance) => {
    const timers = new ControlledTimers();
    const document: DocumentLike = { createElement: (tag) => new Element(tag, document), createTextNode: (text) => { const node = new Element('#text', document); node.textContent = text; return node; } };
    const root = new Element('div', document);
    const api = {
      biliServiceStatus: vi.fn(async () => ({ version: 0 as const, health: 'missing' as const })),
      checkBiliService: vi.fn(),
      beginBiliServiceChallenge: vi.fn(async () => ({ challengeId: 'replace', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:05:00Z' })),
      pollBiliServiceChallenge: vi.fn(async () => { throw pollError; }),
      replaceBiliServiceCredential: vi.fn(), authorizeAdminOperation: vi.fn(),
    };
    const view = mountBiliServiceView(root as unknown as HTMLElement, api as unknown as Parameters<typeof mountBiliServiceView>[1], timers);
    await flush();
    button(root, '更换服务账号').listeners.get('click')?.();
    await flush();
    await timers.fireNext();

    expect(timers.count()).toBe(0);
    expect(descendants(root).some((node) => node.className.includes('hosted-admin-bili-totp'))).toBe(false);
    expect(descendants(root).some((node) => node.textContent === guidance)).toBe(true);
    await view.dispose();
  });
});
