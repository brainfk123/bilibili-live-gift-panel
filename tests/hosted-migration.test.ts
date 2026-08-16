import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { createMigrationFlow, migrationFileLimit, mountMigrationView } from '../src/hosted/migration';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const preview = {
  id: 12, expiresAt: '2030-01-02T00:00:00Z', reused: false,
  counts: { attributes: 2, rules: 3, activities: 4, giftTargetPanels: 1, giftTargetItems: 5 },
  warnings: ['已规范化空白名称'], ignored: ['/payload/cache'], roomSuggestion: '12345',
  source: { appVersion: '0.4.4', configurationSchemaVersion: 5 },
};

describe('hosted migration contract', () => {
  it('offers a pending migration refresh action to obtain its final status', () => {
    const source = readFileSync(new URL('../src/hosted/migration.ts', import.meta.url), 'utf8');
    expect(source).toContain('刷新迁移状态');
  });

  it('sends preview upload bytes as the raw JSON request body with same-origin CSRF protection', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      return json(preview, 201);
    });
    await api.previewMigration('{"kind":"gift-panel-online-migration"}');
    expect(requests.at(-1)).toEqual(['/api/migrations/preview', expect.objectContaining({
      method: 'POST', credentials: 'same-origin', body: '{"kind":"gift-panel-online-migration"}',
      headers: expect.objectContaining({ 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf' }),
    })]);
  });

  it('rejects non-json and oversized files before they are read or uploaded', async () => {
    const api = { previewMigration: vi.fn() };
    const flow = createMigrationFlow(api, vi.fn(), { now: () => new Date('2030-01-01T00:00:00Z') });
    await expect(flow.preview({ name: 'package.txt', size: 2, text: vi.fn() })).rejects.toMatchObject({ code: 'invalid_request' });
    await expect(flow.preview({ name: 'package.json', size: migrationFileLimit + 1, text: vi.fn() })).rejects.toMatchObject({ code: 'invalid_request' });
    expect(api.previewMigration).not.toHaveBeenCalled();
  });

  it('renders only server preview groups and clears uploaded text after success or failure', async () => {
    const rendered: unknown[] = [];
    const raw = '{"private":"UPLOAD-MUST-NOT-PERSIST"}';
    const api = { previewMigration: vi.fn(async () => preview) };
    const flow = createMigrationFlow(api, (state) => rendered.push(structuredClone(state)), { now: () => new Date('2030-01-01T00:00:00Z') });
    await flow.preview({ name: 'package.json', size: raw.length, text: vi.fn(async () => raw) });
    expect(rendered.at(-1)).toEqual(expect.objectContaining({ preview, rawFileActive: false }));
    expect(JSON.stringify(rendered)).not.toContain('UPLOAD-MUST-NOT-PERSIST');
  });

  it('cancels a preview accepted by the server after a page-leave disposal barrier', async () => {
    let release!: (value: typeof preview) => void; const submitted = new Promise<typeof preview>((resolve) => { release = resolve; });
    const api = { previewMigration: vi.fn(() => submitted), cancelMigration: vi.fn(async () => ({ id: 12, status: 'cancelled' as const })) };
    const flow = createMigrationFlow(api, vi.fn()); const pending = flow.preview({ name: 'package.json', size: 2, text: async () => '{}' });
    await vi.waitFor(() => expect(api.previewMigration).toHaveBeenCalledTimes(1)); await flow.dispose(); release(preview); await expect(pending).rejects.toMatchObject({ code: 'operation_failed' });
    await vi.waitFor(() => expect(api.cancelMigration).toHaveBeenCalledWith(12));
  });

  it('never cancels a reused pending migration after disposal', async () => {
    let release!: (value: typeof preview) => void; const submitted = new Promise<typeof preview>((resolve) => { release = resolve; });
    const api = { previewMigration: vi.fn(() => submitted), cancelMigration: vi.fn(async () => ({ id: 12, status: 'cancelled' as const })) };
    const flow = createMigrationFlow(api, vi.fn()); const pending = flow.preview({ name: 'package.json', size: 2, text: async () => '{}' });
    await vi.waitFor(() => expect(api.previewMigration).toHaveBeenCalledTimes(1)); await flow.dispose(); release({ ...preview, reused: true }); await expect(pending).rejects.toMatchObject({ code: 'operation_failed' });
    expect(api.cancelMigration).not.toHaveBeenCalled();
  });

  it('uses the server job state rather than assuming a reused preview is previewed', async () => {
    const rendered: unknown[] = [];
    const api = { previewMigration: vi.fn(async () => ({ ...preview, reused: true })), getMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const, expiresAt: preview.expiresAt })) };
    const flow = createMigrationFlow(api, (state) => rendered.push(structuredClone(state)));
    await flow.preview({ name: 'package.json', size: 2, text: vi.fn(async () => '{}') });
    expect(api.getMigration).toHaveBeenCalledWith(12);
    expect(rendered.at(-1)).toEqual(expect.objectContaining({ job: expect.objectContaining({ id: 12, status: 'pending' }), canApply: false, canRefresh: true, canCancel: true }));
  });

  it('keeps migration confirmation controls mounted and focused across incremental updates', async () => {
    class Element {
      children: Element[] = []; textContent = ''; className = ''; type = ''; disabled = false; hidden = false; checked = false; value = ''; accept = ''; src = ''; alt = '';
      listeners = new Map<string, () => void>(); attributes = new Map<string, string>(); files?: { item(index: number): unknown };
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element; createTextNode(text: string): Element; activeElement?: Element; defaultView?: unknown }) {}
      get firstChild() { return this.children[0]; } append(...nodes: Element[]) { this.children.push(...nodes); } replaceChildren(...nodes: Element[]) { this.children = nodes; } removeChild(node: Element) { this.children = this.children.filter((child) => child !== node); }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); } removeAttribute(name: string) { this.attributes.delete(name); } addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); } focus() { this.ownerDocument.activeElement = this; }
    }
    const view = { addEventListener: vi.fn(), removeEventListener: vi.fn() };
    const document: { createElement(tag: string): Element; createTextNode(text: string): Element; activeElement?: Element; defaultView: unknown } = { createElement: (tag: string): Element => new Element(tag, document), createTextNode: (text: string): Element => { const node = new Element('#text', document); node.textContent = text; return node; }, defaultView: view };
    const root = new Element('div', document) as unknown as HTMLElement;
    const mounted = mountMigrationView(root, { previewMigration: vi.fn(async () => preview) }, { onConfiguration: vi.fn() });
    const panel = (root as unknown as Element).children[0]; const file = panel.children[2]; const previewButton = panel.children[3];
    file.files = { item: () => ({ name: 'package.json', size: 2, text: async () => '{}' }) }; file.listeners.get('change')?.(); previewButton.listeners.get('click')?.();
    await vi.waitFor(() => expect(panel.children[5].hidden).toBe(false));
    const confirmation = panel.children[5].children[0]; confirmation.focus(); confirmation.checked = true; confirmation.listeners.get('change')?.();
    expect((root as unknown as Element).children[0]).toBe(panel); expect(panel.children[5].children[0]).toBe(confirmation); expect(document.activeElement).toBe(confirmation); expect(panel.children[7].hidden).toBe(false);
    await mounted.dispose();
  });

  it('requires an explicit unchecked room suggestion and polls the reusable proof without creating a site session', async () => {
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })),
      pollLogin: vi.fn().mockResolvedValueOnce({ status: 'pending', expiresAt: '2030-01-02T00:00:00Z' }).mockResolvedValueOnce({ status: 'verified', expiresAt: '2030-01-02T00:00:00Z' }),
      cancelLogin: vi.fn(async () => undefined), applyMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const, expiresAt: '2030-01-02T00:00:00Z' })),
      createSession: vi.fn(),
    };
    const flow = createMigrationFlow(api, vi.fn(), { now: () => new Date('2030-01-01T00:00:00Z') });
    flow.acceptPreview(preview);
    await expect(flow.apply()).rejects.toMatchObject({ code: 'invalid_request' });
    flow.confirmReplacement(true); flow.setKeepRoomSuggestion(false);
    await expect(flow.apply()).rejects.toMatchObject({ code: 'verification_pending' });
    await flow.apply();
    expect(api.applyMigration).toHaveBeenCalledWith(12, 'proof', false);
    expect(api.createSession).not.toHaveBeenCalled();
  });

  it('rejects a duplicate proof operation while keeping a transient verification failure retryable', async () => {
    let release!: (value: { status: 'verified'; expiresAt: string }) => void;
    const polling = new Promise<{ status: 'verified'; expiresAt: string }>((resolve) => { release = resolve; });
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })),
      pollLogin: vi.fn(() => polling), cancelLogin: vi.fn(async () => undefined), applyMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const })),
    };
    const flow = createMigrationFlow(api, vi.fn()); flow.acceptPreview(preview); flow.confirmReplacement(true);
    const first = flow.apply(); const second = flow.apply().catch((error: unknown) => error);
    await vi.waitFor(() => { expect(api.beginLogin).toHaveBeenCalledTimes(1); expect(api.pollLogin).toHaveBeenCalledTimes(1); });
    await expect(second).resolves.toMatchObject({ code: 'operation_conflict' });
    release({ status: 'verified', expiresAt: '2030-01-02T00:00:00Z' }); await first;
    expect(api.applyMigration).toHaveBeenCalledTimes(1);
  });

  it('freezes migration A while its proof is pending so it cannot apply to B', async () => {
    let release!: (value: { status: 'verified'; expiresAt: string }) => void;
    const pending = new Promise<{ status: 'verified'; expiresAt: string }>((resolve) => { release = resolve; });
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'proof-a', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })),
      pollLogin: vi.fn(() => pending), cancelLogin: vi.fn(async () => undefined),
      applyMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const })), cancelMigration: vi.fn(), previewMigration: vi.fn(),
    };
    const flow = createMigrationFlow(api, vi.fn()); flow.acceptPreview(preview); flow.confirmReplacement(true); flow.setKeepRoomSuggestion(false);
    const applying = flow.apply(); await vi.waitFor(() => expect(api.pollLogin).toHaveBeenCalledWith('proof-a'));
    flow.setKeepRoomSuggestion(true);
    await expect(flow.preview({ name: 'b.json', size: 2, text: vi.fn(async () => '{}') })).rejects.toMatchObject({ code: 'operation_conflict' });
    await expect(flow.cancel()).rejects.toMatchObject({ code: 'operation_conflict' });
    await expect(flow.apply()).rejects.toMatchObject({ code: 'operation_conflict' });
    release({ status: 'verified', expiresAt: '2030-01-02T00:00:00Z' }); await applying;
    expect(api.applyMigration).toHaveBeenCalledWith(12, 'proof-a', false); expect(api.previewMigration).not.toHaveBeenCalled(); expect(api.cancelMigration).not.toHaveBeenCalled();
  });

  it('reuses an unexpired proof after a temporary verification outage', async () => {
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })),
      pollLogin: vi.fn().mockRejectedValueOnce(new HostedAPIError('temporarily_unavailable', 503)).mockResolvedValueOnce({ status: 'verified', expiresAt: '2030-01-02T00:00:00Z' }),
      cancelLogin: vi.fn(async () => undefined), applyMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const })),
    };
    const flow = createMigrationFlow(api, vi.fn()); flow.acceptPreview(preview); flow.confirmReplacement(true);
    await expect(flow.apply()).rejects.toMatchObject({ code: 'temporarily_unavailable' });
    await flow.apply();
    expect(api.beginLogin).toHaveBeenCalledTimes(1); expect(api.pollLogin).toHaveBeenCalledTimes(2); expect(api.applyMigration).toHaveBeenCalledTimes(1);
  });

  it('does not mutate state after disposal and exposes only valid lifecycle actions', async () => {
    let release!: (value: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const begin = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { release = resolve; });
    const states: Array<Record<string, unknown>> = [];
    const api = { beginLogin: vi.fn(() => begin), pollLogin: vi.fn(), cancelLogin: vi.fn(async () => undefined), applyMigration: vi.fn() };
    const flow = createMigrationFlow(api, (state) => states.push(structuredClone(state) as unknown as Record<string, unknown>)); flow.acceptPreview(preview); flow.confirmReplacement(true);
    const applying = flow.apply(); await flow.dispose(); release({ challengeId: 'late-proof', qrImage: 'secret-qr', expiresAt: '2030-01-02T00:00:00Z' }); await expect(applying).rejects.toMatchObject({ code: 'operation_failed' });
    expect(api.cancelLogin).toHaveBeenCalledWith('late-proof'); expect(JSON.stringify(states)).not.toContain('secret-qr');
  });

  it('cleans up a terminal rejected proof while keeping a pending proof retryable', async () => {
    const api = {
      previewMigration: vi.fn(), beginLogin: vi.fn(async () => ({ challengeId: 'terminal-proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'registration_required' as const, expiresAt: '2030-01-02T00:00:00Z' })),
      cancelLogin: vi.fn(async () => undefined), applyMigration: vi.fn(),
    };
    const flow = createMigrationFlow(api, vi.fn());
    flow.acceptPreview(preview); flow.confirmReplacement(true);
    await expect(flow.apply()).rejects.toMatchObject({ code: 'proof_rejected' });
    expect(api.cancelLogin).toHaveBeenCalledWith('terminal-proof');
  });

  it('shows pending, supports cancellation, and exposes the seven-day rollback countdown without raw errors', async () => {
    const states: unknown[] = [];
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-02T00:00:00Z' })),
      cancelLogin: vi.fn(async () => undefined),
      applyMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const, expiresAt: '2030-01-02T00:00:00Z' })),
      getMigration: vi.fn(async () => ({ id: 12, status: 'applied' as const, rollbackExpiresAt: '2030-01-08T00:00:00Z' })),
      cancelMigration: vi.fn(async () => ({ id: 12, status: 'cancelled' as const })),
    };
    const flow = createMigrationFlow(api, (state) => states.push(structuredClone(state)), { now: () => new Date('2030-01-01T00:00:00Z') });
    flow.acceptPreview(preview); flow.confirmReplacement(true); await flow.apply();
    await flow.refresh(12);
    expect(states.at(-1)).toEqual(expect.objectContaining({ rollbackDaysRemaining: 7, canRollback: true, canCancel: false, canRefresh: false }));
    await expect(flow.cancel()).rejects.toMatchObject({ code: 'invalid_request' });
    flow.reportFailure(new Error('RAW UPLOADED JSON: secret'));
    expect(JSON.stringify(states.at(-1))).not.toContain('RAW UPLOADED JSON');
  });
});
