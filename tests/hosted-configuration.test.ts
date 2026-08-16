import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { createConfigurationFlow } from '../src/hosted/configuration';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const configuration = {
  definition: { attributes: [], displayScenes: [], giftTargetPanels: [], activities: [], rules: [], timerRules: [], formulaPresets: [], gifts: [] },
  runtime: { attributeValues: {}, giftTargetReceived: [], activities: [], ruleLimits: { localDate: '2030-01-01', appliedCounts: {} } },
  version: 4,
  revision: 9,
};

describe('hosted configuration contract', () => {
  it('adds signed-in navigation to configuration and migration views', () => {
    const main = readFileSync(new URL('../src/hosted/main.ts', import.meta.url), 'utf8');
    expect(main).toContain('mountConfigurationView');
    expect(main).toContain('mountMigrationView');
  });

  it('uses exact configuration DTOs and sends optimistic version and revision values', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      if (input === '/api/configuration') return json(configuration);
      if (input === '/api/configuration/definition') return json({ version: 5, revision: 10 });
      return json({ revision: 11 });
    });
    await expect(api.loadConfiguration()).resolves.toEqual(configuration);
    await api.saveConfigurationDefinition(4, configuration.definition);
    await api.saveConfigurationRuntime(10, configuration.runtime);
    expect(requests.slice(2)).toEqual([
      ['/api/configuration/definition', expect.objectContaining({ method: 'PUT', credentials: 'same-origin', body: JSON.stringify({ expectedVersion: 4, definition: configuration.definition }) })],
      ['/api/configuration/state', expect.objectContaining({ method: 'PUT', credentials: 'same-origin', body: JSON.stringify({ expectedRevision: 10, runtime: configuration.runtime }) })],
    ]);
  });

  it('rejects configuration responses with extra account, UID, or internal fields', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ ...configuration, accountId: 37 }));
    await expect(api.loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('rejects extra fields nested inside the explicit configuration DTO', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ ...configuration, definition: { ...configuration.definition, attributes: [{ id: 'hp', name: '生命值', unit: '', format: 'number', decimals: 0, suffix: '', accountId: 37 }] } }));
    await expect(api.loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('preserves an in-memory unsaved draft and refreshes authority after a revision conflict', async () => {
    const render = vi.fn();
    const draft = { ...configuration.definition, attributes: [{ id: 'hp', name: '生命值' }] };
    const authoritative = { ...configuration, version: 5, revision: 10 };
    const api = {
      loadConfiguration: vi.fn(async () => authoritative),
      saveConfigurationDefinition: vi.fn(async () => { throw new HostedAPIError('revision_conflict', 409); }),
      saveConfigurationRuntime: vi.fn(), suggestRoom: vi.fn(),
    };
    const flow = createConfigurationFlow(api, render);
    await flow.load();
    flow.setDefinition(draft);
    await expect(flow.saveDefinition()).rejects.toMatchObject({ code: 'revision_conflict' });
    expect(api.loadConfiguration).toHaveBeenCalledTimes(2);
    expect(render).toHaveBeenLastCalledWith(expect.objectContaining({
      draftDefinition: draft, authoritative, conflict: true,
    }));
    flow.dispose();
    expect(JSON.stringify(render.mock.calls.at(-1))).not.toContain('生命值');
  });

  it('drops a configuration response that completes after disposal', async () => {
    let release!: (value: typeof configuration) => void;
    const loaded = new Promise<typeof configuration>((resolve) => { release = resolve; });
    const api = { loadConfiguration: vi.fn(() => loaded), saveConfigurationDefinition: vi.fn(), saveConfigurationRuntime: vi.fn(), suggestRoom: vi.fn() };
    const flow = createConfigurationFlow(api, vi.fn());
    const loading = flow.load(); flow.dispose(); release(configuration); await loading;
    await expect(flow.saveDefinition()).rejects.toMatchObject({ code: 'invalid_request' });
    expect(api.saveConfigurationDefinition).not.toHaveBeenCalled();
  });
});
