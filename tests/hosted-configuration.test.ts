import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { createConfigurationFlow, mountConfigurationView } from '../src/hosted/configuration';

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

  it('accepts real Go empty values and empty optional structs without widening response keys', async () => {
    const realShape = {
      ...configuration,
      definition: {
        ...configuration.definition,
        attributes: [{ id: 'hp', name: '生命值', unit: '', format: '', decimals: 0, suffix: '' }],
        displayScenes: [{ id: 'scene', name: '', attributeIds: [], layout: '', themeId: '' }],
        giftTargetPanels: [{ id: 'panel', name: '', layout: '', items: [{ giftId: 0, target: 0, barStyle: '' }] }],
        activities: [{ id: 'a', name: '', attributeIds: [], resultMode: '', gateRules: false, initialValues: {}, milestones: [] }],
        rules: [{ id: 'rule', giftId: 0, attributeId: '', formula: '' }],
        timerRules: [{ id: 'timer', attributeId: '', formulaName: '', intervalSeconds: 0, formula: '', enabled: false }],
        formulaPresets: [{ id: 'preset', name: '', context: '', formula: '', attributeId: '' }], gifts: [{ id: 0, name: '', price: 0, coinType: '' }],
      },
      runtime: { ...configuration.runtime, ruleLimits: { localDate: '', appliedCounts: {} }, activities: [{ id: 'a', status: '', milestones: [], giftTimeout: {} }] },
    };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(realShape));
    await expect(api.loadConfiguration()).resolves.toEqual(realShape);
  });

  it('rejects an impossible empty definition gift timeout object', async () => {
    const malformed = { ...configuration, definition: { ...configuration.definition, activities: [{ id: 'a', name: '', attributeIds: [], resultMode: '', gateRules: false, initialValues: {}, milestones: [], giftTimeout: {} }] } };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(malformed));
    await expect(api.loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('fails closed when a SimplePlay parameter is null rather than an exact scalar value', async () => {
    const malformed = {
      ...configuration,
      definition: {
        ...configuration.definition,
        simplePlay: {
          version: 1, templateId: 'overtime', templateVersion: 1, attributeId: 'seconds',
          parameters: { name: null }, gifts: { overtime: [] }, managedFingerprint: 'fingerprint',
        },
      },
    };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(malformed));
    await expect(api.loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('rejects SimplePlay overtime actions outside the versioned action allowlist', async () => {
    const malformed = {
      ...configuration,
      definition: {
        ...configuration.definition,
        simplePlay: {
          version: 1, templateId: 'overtime', templateVersion: 1, attributeId: 'seconds',
          parameters: { name: '加班' }, gifts: { overtime: [] }, managedFingerprint: 'fingerprint',
          overtimeGiftActions: [{ giftId: 1, operation: 'shell' }],
        },
      },
    };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(malformed));
    await expect(api.loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('preserves an in-memory unsaved draft and refreshes authority after a revision conflict', async () => {
    const render = vi.fn();
    const draft = { ...configuration.definition, attributes: [{ id: 'hp', name: '生命值', unit: '', format: '', decimals: 0, suffix: '' }] };
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

  it('rejects a JSON object that is not the exact configuration definition before saving it', async () => {
    const saveConfigurationDefinition = vi.fn(async () => ({ version: 5, revision: 10 }));
    const flow = createConfigurationFlow({ loadConfiguration: vi.fn(async () => configuration), saveConfigurationDefinition, saveConfigurationRuntime: vi.fn(), suggestRoom: vi.fn() }, vi.fn());
    await flow.load();
    expect(() => flow.setDefinition({ unexpected: true } as unknown as typeof configuration.definition)).toThrow(expect.objectContaining({ code: 'invalid_request' }));
    expect(saveConfigurationDefinition).not.toHaveBeenCalled();
  });

  it('keeps the same focused draft textarea during a revision-conflict authority refresh', async () => {
    class Element {
      children: Element[] = []; textContent = ''; className = ''; type = ''; disabled = false; value = ''; inputMode = '';
      listeners = new Map<string, () => void>(); attributes = new Map<string, string>();
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element; activeElement?: Element }) {}
      append(...nodes: Element[]) { this.children.push(...nodes); }
      replaceChildren(...nodes: Element[]) { this.children = nodes; }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); }
      addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); }
      focus() { this.ownerDocument.activeElement = this; }
    }
    const document: { createElement(tag: string): Element; activeElement?: Element } = { createElement: (tag: string): Element => new Element(tag, document) };
    const root = new Element('div', document) as unknown as HTMLElement;
    const authoritative = { ...configuration, version: 5, revision: 10 };
    const api = { loadConfiguration: vi.fn().mockResolvedValueOnce(configuration).mockResolvedValueOnce(authoritative), saveConfigurationDefinition: vi.fn(async () => { throw new HostedAPIError('revision_conflict', 409); }), saveConfigurationRuntime: vi.fn(), suggestRoom: vi.fn() };
    const mounted = mountConfigurationView(root, api, { onMigration: vi.fn(), onExit: vi.fn() }); await mounted.ready;
    const panel = (root as unknown as Element).children[0];
    const textarea = panel.children.find((child) => child.tagName === 'label')?.children[0];
    const save = panel.children.find((child) => child.tagName === 'button' && child.textContent === '保存配置定义');
    if (!textarea || !save) throw new Error('configuration controls missing');
    textarea.value = JSON.stringify({ ...configuration.definition, attributes: [{ id: 'hp', name: '草稿', unit: '', format: '', decimals: 0, suffix: '' }] }); textarea.focus(); save.listeners.get('click')?.();
    await vi.waitFor(() => expect(api.loadConfiguration).toHaveBeenCalledTimes(2));
    expect((root as unknown as Element).children[0]).toBe(panel);
    expect(textarea.value).toContain('草稿'); expect(document.activeElement).toBe(textarea);
    expect(panel.children.find((child) => child.tagName === 'pre')?.textContent).toContain('服务器权威版本 5');
    mounted.dispose();
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
