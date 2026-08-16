import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { HostedAPI } from '../src/hosted/api';
import { createMigrationFlow, migrationFileLimit } from '../src/hosted/migration';

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
      getMigration: vi.fn(async () => ({ id: 12, status: 'applied' as const, rollbackExpiresAt: '2030-01-08T00:00:00Z' })),
      cancelMigration: vi.fn(async () => ({ id: 12, status: 'cancelled' as const })),
    };
    const flow = createMigrationFlow(api, (state) => states.push(structuredClone(state)), { now: () => new Date('2030-01-01T00:00:00Z') });
    await flow.refresh(12); await flow.cancel();
    expect(states.at(-2)).toEqual(expect.objectContaining({ rollbackDaysRemaining: 7 }));
    expect(states.at(-1)).toEqual(expect.objectContaining({ job: { id: 12, status: 'cancelled' } }));
    flow.reportFailure(new Error('RAW UPLOADED JSON: secret'));
    expect(JSON.stringify(states.at(-1))).not.toContain('RAW UPLOADED JSON');
  });
});
