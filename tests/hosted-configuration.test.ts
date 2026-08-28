import { describe, expect, it, vi } from 'vitest';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { createConfigurationFlow, mountConfigurationView } from '../src/hosted/configuration';
import { GAMEPLAY_TEMPLATES } from '../src/gameplay-templates';

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

  it('strictly validates current and legacy SimplePlay template parameters, slots, and actions', async () => {
    const current = GAMEPLAY_TEMPLATES.find((template) => template.id === 'boss' && template.version === 1); if (!current) throw new Error('boss template missing');
    const make = () => {
      let id = 1; const gifts = Object.fromEntries(current.giftSlots.map((slot) => [slot.id, Array.from({ length: slot.minimum }, () => id++)]));
      return { version: 1, templateId: current.id, templateVersion: current.version, attributeId: 'health', parameters: Object.fromEntries(current.parameters.map((parameter) => [parameter.id, parameter.defaultValue])), gifts, managedFingerprint: 'fingerprint' };
    };
    const accepted = { ...configuration, definition: { ...configuration.definition, gifts: [{ id: 1, name: 'gift', price: 1, coinType: 'gold' }], simplePlay: make() } };
    const connect = async (body: unknown) => HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(body));
    await expect((await connect(accepted)).loadConfiguration()).resolves.toEqual(accepted);
    for (const mutate of [
      (simple: Record<string, unknown>) => { (simple.parameters as Record<string, unknown>).regenEnabled = 'x'; },
      (simple: Record<string, unknown>) => { (simple.parameters as Record<string, unknown>).maxHealth = {}; },
      (simple: Record<string, unknown>) => { delete (simple.parameters as Record<string, unknown>).bossName; },
      (simple: Record<string, unknown>) => { (simple.parameters as Record<string, unknown>).maxHealth = 100000001; },
      (simple: Record<string, unknown>) => { const gifts = simple.gifts as Record<string, number[]>; gifts.heavy = gifts.attack; },
    ]) {
      const malformed = structuredClone(accepted); mutate(malformed.definition.simplePlay as Record<string, unknown>);
      await expect((await connect(malformed)).loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
    }
    const legacy = { ...configuration, definition: { ...configuration.definition, gifts: [{ id: 1, name: 'gift', price: 1, coinType: 'gold' }], simplePlay: { version: 1, templateId: 'overtime', templateVersion: 1, attributeId: 'seconds', parameters: { name: '加班', minutesPerYuan: 60, maxHours: 0, broadcastMessage: '谢谢' }, gifts: { overtime: [1] }, managedFingerprint: 'legacy' } } };
    await expect((await connect(legacy)).loadConfiguration()).resolves.toEqual(legacy);
  });

  it('rejects SimplePlay resource-like text, fractional integer fields, and gifts absent from its catalog', async () => {
    const overtime = GAMEPLAY_TEMPLATES.find((template) => template.id === 'overtime' && template.version === 2); if (!overtime) throw new Error('overtime template missing');
    const current = { ...configuration, definition: { ...configuration.definition, gifts: [{ id: 1, name: 'gift', price: 1, coinType: 'gold' }], simplePlay: { version: 1, templateId: 'overtime', templateVersion: 2, attributeId: 'seconds', parameters: Object.fromEntries(overtime.parameters.map((parameter) => [parameter.id, parameter.defaultValue])), gifts: { overtime: [1] }, overtimeGiftActions: [{ giftId: 1, operation: 'add', seconds: 60 }], managedFingerprint: 'current' } } };
    const connect = async (body: unknown) => HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(body));
    await expect((await connect(current)).loadConfiguration()).resolves.toEqual(current);
    for (const mutate of [
      (simple: Record<string, unknown>) => { (simple.parameters as Record<string, unknown>).maxSeconds = 1.5; },
      (simple: Record<string, unknown>) => { (simple.parameters as Record<string, unknown>).name = 'https://host/path'; },
      (simple: Record<string, unknown>) => { (simple.gifts as Record<string, number[]>).overtime = [0]; },
      (simple: Record<string, unknown>) => { (simple.gifts as Record<string, number[]>).overtime = [99]; },
      (simple: Record<string, unknown>) => { (simple.overtimeGiftActions as Array<Record<string, unknown>>)[0].giftId = 99; },
    ]) {
      const malformed = structuredClone(current); mutate(malformed.definition.simplePlay as Record<string, unknown>);
      await expect((await connect(malformed)).loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
    }
    const legacy = { ...configuration, definition: { ...configuration.definition, gifts: [{ id: 1, name: 'gift', price: 1, coinType: 'gold' }], simplePlay: { version: 1, templateId: 'overtime', templateVersion: 1, attributeId: 'seconds', parameters: { name: '10/20', minutesPerYuan: 60, maxHours: 0, broadcastMessage: 'PK: boss' }, gifts: { overtime: [1] }, managedFingerprint: 'legacy' } } };
    await expect((await connect(legacy)).loadConfiguration()).resolves.toEqual(legacy);
    const mediaPath = structuredClone(legacy); (mediaPath.definition.simplePlay!.parameters as Record<string, unknown>).name = 'avatar.png'; await expect((await connect(mediaPath)).loadConfiguration()).rejects.toMatchObject({ code: 'invalid_response' });
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

  it('confirms dirty in-page navigation and clears the matching beforeunload guard after save', async () => {
    class Element {
      children: Element[] = []; textContent = ''; className = ''; type = ''; disabled = false; value = ''; inputMode = ''; accept = '';
      listeners = new Map<string, () => void>(); attributes = new Map<string, string>();
      constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element; defaultView?: unknown }) {}
      append(...nodes: Element[]) { this.children.push(...nodes); } replaceChildren(...nodes: Element[]) { this.children = nodes; }
      setAttribute(name: string, value: string) { this.attributes.set(name, value); } addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); } focus() {}
    }
    const windowListeners = new Map<string, () => void>(); const view = { confirm: vi.fn(), addEventListener: (name: string, listener: () => void) => windowListeners.set(name, listener), removeEventListener: (name: string) => windowListeners.delete(name) };
    const document: { createElement(tag: string): Element; defaultView: unknown } = { createElement: (tag: string): Element => new Element(tag, document), defaultView: view };
    const root = new Element('div', document) as unknown as HTMLElement; const onMigration = vi.fn();
    const api = { loadConfiguration: vi.fn(async () => configuration), saveConfigurationDefinition: vi.fn(async () => ({ version: 5, revision: 10 })), saveConfigurationRuntime: vi.fn(), suggestRoom: vi.fn() };
    const mounted = mountConfigurationView(root, api, { onMigration, onExit: vi.fn() }); await mounted.ready;
    const panel = (root as unknown as Element).children[0]; const definition = panel.children.find((child) => child.tagName === 'label')?.children[0];
    const save = panel.children.find((child) => child.tagName === 'button' && child.textContent === '保存配置定义'); const migration = panel.children.find((child) => child.tagName === 'button' && child.textContent === '迁移本地配置');
    if (!definition || !save || !migration) throw new Error('configuration controls missing');
    definition.listeners.get('input')?.(); view.confirm.mockReturnValueOnce(false); migration.listeners.get('click')?.(); expect(onMigration).not.toHaveBeenCalled();
    const event = { preventDefault: vi.fn(), returnValue: undefined as unknown }; (windowListeners.get('beforeunload') as unknown as (value: unknown) => void)?.(event); expect(event.preventDefault).toHaveBeenCalledTimes(1);
    save.listeners.get('click')?.(); await vi.waitFor(() => expect(api.saveConfigurationDefinition).toHaveBeenCalledTimes(1)); await Promise.resolve();
    const cleanEvent = { preventDefault: vi.fn(), returnValue: undefined as unknown }; (windowListeners.get('beforeunload') as unknown as (value: unknown) => void)?.(cleanEvent); expect(cleanEvent.preventDefault).not.toHaveBeenCalled();
    view.confirm.mockReturnValueOnce(true); definition.listeners.get('input')?.(); migration.listeners.get('click')?.(); expect(onMigration).toHaveBeenCalledTimes(1); mounted.dispose();
  });

  it('does not clear dirty when typing continues after a save request starts', async () => {
    let release!: (value: { version: number; revision: number }) => void; const saving = new Promise<{ version: number; revision: number }>((resolve) => { release = resolve; });
    class Element { children: Element[] = []; textContent = ''; type = ''; disabled = false; value = ''; inputMode = ''; listeners = new Map<string, () => void>(); constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element; defaultView?: unknown }) {} append(...nodes: Element[]) { this.children.push(...nodes); } replaceChildren(...nodes: Element[]) { this.children = nodes; } setAttribute() {} addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); } focus() {} }
    const listeners = new Map<string, () => void>(); const view = { addEventListener: (name: string, listener: () => void) => listeners.set(name, listener), removeEventListener: vi.fn(), confirm: vi.fn() };
    const document: { createElement(tag: string): Element; defaultView: unknown } = { createElement: (tag: string): Element => new Element(tag, document), defaultView: view }; const root = new Element('div', document) as unknown as HTMLElement;
    const api = { loadConfiguration: vi.fn(async () => configuration), saveConfigurationDefinition: vi.fn(() => saving), saveConfigurationRuntime: vi.fn(), suggestRoom: vi.fn() };
    const mounted = mountConfigurationView(root, api, { onMigration: vi.fn(), onExit: vi.fn() }); await mounted.ready; const panel = (root as unknown as Element).children[0]; const input = panel.children.find((child) => child.tagName === 'label')?.children[0]; const save = panel.children.find((child) => child.tagName === 'button' && child.textContent === '保存配置定义'); if (!input || !save) throw new Error('controls missing');
    input.value = JSON.stringify(configuration.definition); input.listeners.get('input')?.(); save.listeners.get('click')?.(); await vi.waitFor(() => expect(api.saveConfigurationDefinition).toHaveBeenCalledTimes(1)); input.listeners.get('input')?.(); release({ version: 5, revision: 10 }); await Promise.resolve(); await Promise.resolve();
    const event = { preventDefault: vi.fn(), returnValue: undefined as unknown }; (listeners.get('beforeunload') as unknown as (value: unknown) => void)?.(event); expect(event.preventDefault).toHaveBeenCalledTimes(1); mounted.dispose();
  });

  it('keeps a user draft typed before the initial authority response', async () => {
    let release!: (value: typeof configuration) => void; const loaded = new Promise<typeof configuration>((resolve) => { release = resolve; });
    class Element { children: Element[] = []; textContent = ''; type = ''; disabled = false; value = ''; inputMode = ''; listeners = new Map<string, () => void>(); constructor(readonly tagName: string, readonly ownerDocument: { createElement(tag: string): Element; defaultView?: unknown }) {} append(...nodes: Element[]) { this.children.push(...nodes); } replaceChildren(...nodes: Element[]) { this.children = nodes; } setAttribute() {} addEventListener(name: string, listener: () => void) { this.listeners.set(name, listener); } focus() {} }
    const windowListeners = new Map<string, () => void>(); const view = { addEventListener: (name: string, listener: () => void) => windowListeners.set(name, listener), removeEventListener: vi.fn(), confirm: vi.fn() };
    const document: { createElement(tag: string): Element; defaultView: unknown } = { createElement: (tag: string): Element => new Element(tag, document), defaultView: view }; const root = new Element('div', document) as unknown as HTMLElement;
    const mounted = mountConfigurationView(root, { loadConfiguration: vi.fn(() => loaded), saveConfigurationDefinition: vi.fn(), saveConfigurationRuntime: vi.fn(), suggestRoom: vi.fn() }, { onMigration: vi.fn(), onExit: vi.fn() });
    const panel = (root as unknown as Element).children[0]; const definition = panel.children.find((child) => child.tagName === 'label')?.children[0]; if (!definition) throw new Error('definition input missing');
    const draft = JSON.stringify({ ...configuration.definition, attributes: [{ id: 'hp', name: 'local draft', unit: '', format: '', decimals: 0, suffix: '' }] }); definition.value = draft; definition.listeners.get('input')?.(); release(configuration); await mounted.ready;
    expect(definition.value).toBe(draft); const event = { preventDefault: vi.fn(), returnValue: undefined as unknown }; (windowListeners.get('beforeunload') as unknown as (value: unknown) => void)?.(event); expect(event.preventDefault).toHaveBeenCalledTimes(1); mounted.dispose();
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
