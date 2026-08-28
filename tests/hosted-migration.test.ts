import { describe, expect, it, vi } from 'vitest';

import {
  HostedAPI,
  type MigrationJob,
  type MigrationPreview,
  type MigrationSelection,
} from '../src/hosted/api';
import { createMigrationFlow, migrationFileLimit, type MigrationViewState } from '../src/hosted/migration';
import {
  dismissMigrationPrompt,
  migrationPromptStorageKey,
  shouldShowMigrationPrompt,
} from '../src/hosted/user/settings/migration-center';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const selection: MigrationSelection = {
  unitIds: [], conflictChoices: {}, includeGeneralSettings: false, includeRoomSuggestion: false,
};

const preview: MigrationPreview = {
  id: 12, expiresAt: '2030-01-02T00:00:00Z', reused: false,
  counts: { attributes: 4, rules: 3, activities: 1, giftTargetPanels: 1, giftTargetItems: 5 },
  warnings: ['已规范化空白名称'], ignored: ['本地素材路径：Hosted 不支持'], roomSuggestion: '12345',
  source: { appVersion: '0.4.7', configurationSchemaVersion: 5 },
  units: [
    {
      id: 'attribute:score', kind: 'attribute', name: '积分', attributeIds: ['score'], ruleIds: ['score-rule'], timerRuleIds: [], formulaPresetIds: [], activityIds: [], displaySceneIds: ['score-card'], giftTargetPanelIds: [], giftIds: [1], cropPresetIds: [],
      compatibility: { status: 'complete', reasonCodes: [] }, selected: false,
    },
    {
      id: 'attribute:bonus', kind: 'attribute', name: '加成', attributeIds: ['bonus'], ruleIds: [], timerRuleIds: [], formulaPresetIds: [], activityIds: [], displaySceneIds: [], giftTargetPanelIds: [], giftIds: [], cropPresetIds: [],
      compatibility: { status: 'complete', reasonCodes: [] }, selected: false,
    },
    {
      id: 'activity:legacy', kind: 'activity', name: '旧活动', attributeIds: [], ruleIds: [], timerRuleIds: [], formulaPresetIds: [], activityIds: ['legacy'], displaySceneIds: ['legacy-scene'], giftTargetPanelIds: [], giftIds: [], cropPresetIds: ['legacy-crop'],
      compatibility: { status: 'partial', reasonCodes: ['crop_presets_unsupported', 'display_scenes_unsupported'] }, selected: false,
    },
    {
      id: 'timer:legacy', kind: 'timer', name: '旧定时器', attributeIds: [], ruleIds: [], timerRuleIds: ['legacy-timer'], formulaPresetIds: [], activityIds: [], displaySceneIds: [], giftTargetPanelIds: [], giftIds: [], cropPresetIds: [],
      compatibility: { status: 'incompatible', reasonCodes: ['timer_rules_unsupported'] }, selected: false,
    },
  ],
  groups: [{ id: 'group:score', unitIds: ['attribute:score', 'attribute:bonus'], reasons: [{ kind: 'shared-attribute', referenceId: 'bonus' }] }],
  conflicts: [], selection, generalSettings: { configurationMode: 'simple' }, canConfirm: true,
};

const selectedPreview: MigrationPreview = {
  ...preview,
  units: preview.units.map((unit) => ({ ...unit, selected: unit.id === 'attribute:score' || unit.id === 'attribute:bonus' })),
  conflicts: [{ id: 'conflict:score', importedUnitIds: ['attribute:score', 'attribute:bonus'], hostedUnitIds: ['attribute:hosted-score'], suggestedNames: { 'attribute:score': '积分（从 EXE 导入）' } }],
  selection: { ...selection, unitIds: ['attribute:score', 'attribute:bonus'] }, canConfirm: false,
};

function collectStates() {
  const states: MigrationViewState[] = [];
  return { states, render: (state: MigrationViewState) => states.push(structuredClone(state)) };
}

describe('Hosted migration API contract', () => {
  it('uses raw upload only for preview and sends server-owned selection boundaries on selection and apply', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      if (input === '/api/migrations/preview') return json(preview, 201);
      if (input === '/api/migrations/12/selection') return json(selectedPreview);
      return json({ id: 12, status: 'pending', expiresAt: preview.expiresAt });
    });

    await api.previewMigration('{"kind":"gift-panel-online-migration","secret":"RAW"}');
    await api.selectMigration(12, selectedPreview.selection);
    await api.applyMigration(12, 'proof', selectedPreview.selection);

    expect(requests.slice(-3)).toEqual([
      ['/api/migrations/preview', expect.objectContaining({ method: 'POST', credentials: 'same-origin', body: '{"kind":"gift-panel-online-migration","secret":"RAW"}', headers: expect.objectContaining({ 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf' }) })],
      ['/api/migrations/12/selection', expect.objectContaining({ method: 'PUT', body: JSON.stringify(selectedPreview.selection) })],
      ['/api/migrations/12/apply', expect.objectContaining({ method: 'POST', body: JSON.stringify({ challengeId: 'proof', selection: selectedPreview.selection }) })],
    ]);
  });

  it('rejects a preview that invents compatibility, group, conflict, or selection fields', async () => {
    for (const malformed of [
      { ...preview, units: [{ ...preview.units[0], compatibility: { status: 'maybe', reasonCodes: [] } }] },
      { ...preview, groups: [{ ...preview.groups[0], unknown: true }] },
      { ...preview, conflicts: [{ ...selectedPreview.conflicts[0], choice: 'replace' }] },
      { ...preview, selection: { ...selection, rawJSON: 'secret' } },
    ]) {
      const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(malformed, 201));
      await expect(api.previewMigration('{}')).rejects.toMatchObject({ code: 'invalid_response' });
    }
  });

  it('normalizes the Go zero-value selection list without accepting other malformed fields', async () => {
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json({ ...preview, selection: { ...selection, unitIds: null } }, 201));
    await expect(api.previewMigration('{}')).resolves.toEqual(preview);
  });

  it('accepts only HTTPS OBS replacement links returned after apply', async () => {
    const job: MigrationJob = { id: 12, status: 'applied', rollbackExpiresAt: '2030-01-08T00:00:00Z', obsLinks: [{ outputId: 'score-card', name: '积分卡片', url: 'https://host.example/obs/public#token=one-time' }] };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(job));
    await expect(api.getMigration(12)).resolves.toEqual(job);

    const invalid = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json({ ...job, obsLinks: [{ ...job.obsLinks![0], url: 'http://host.example/obs' }] }));
    await expect(invalid.getMigration(12)).rejects.toMatchObject({ code: 'invalid_response' });
  });
});

describe('Hosted migration flow', () => {
  it('rejects non-JSON and files over 2 MiB before reading or uploading them', async () => {
    const api = { previewMigration: vi.fn() };
    const flow = createMigrationFlow(api, vi.fn());
    const wrongType = { name: 'package.txt', size: 2, text: vi.fn() };
    const tooLarge = { name: 'package.json', size: migrationFileLimit + 1, text: vi.fn() };
    await expect(flow.preview(wrongType)).rejects.toMatchObject({ code: 'invalid_request' });
    await expect(flow.preview(tooLarge)).rejects.toMatchObject({ code: 'invalid_request' });
    expect(wrongType.text).not.toHaveBeenCalled();
    expect(tooLarge.text).not.toHaveBeenCalled();
    expect(api.previewMigration).not.toHaveBeenCalled();
  });

  it('shows upload progress and a safe network error while retaining the last server preview for retry', async () => {
    let release!: (value: MigrationPreview) => void;
    const firstUpload = new Promise<MigrationPreview>((resolve) => { release = resolve; });
    const api = { previewMigration: vi.fn().mockImplementationOnce(() => firstUpload).mockRejectedValueOnce(new TypeError('Failed to fetch RAW-CONTENT')) };
    const { states, render } = collectStates();
    const flow = createMigrationFlow(api, render);
    const first = flow.preview({ name: 'first.json', size: 2, text: async () => '{}' });
    await vi.waitFor(() => expect(states.at(-1)).toEqual(expect.objectContaining({ operationInFlight: true, rawFileActive: true })));
    release(preview);
    await first;
    await expect(flow.preview({ name: 'secret-filename.json', size: 20, text: async () => '{"raw":"PRIVATE"}' })).rejects.toBeInstanceOf(TypeError);
    expect(states.at(-1)).toEqual(expect.objectContaining({ preview, operationInFlight: false, rawFileActive: false, error: '网络连接失败，请检查网络后重试' }));
    expect(JSON.stringify(states)).not.toContain('PRIVATE');
    expect(JSON.stringify(states)).not.toContain('secret-filename');
    expect(JSON.stringify(states)).not.toContain('RAW-CONTENT');
  });

  it('selects a dependency group as one server round-trip and never selects partial or incompatible units by default', async () => {
    const api = { selectMigration: vi.fn(async (_id: number, next: MigrationSelection) => ({ ...selectedPreview, selection: next })) };
    const { states, render } = collectStates();
    const flow = createMigrationFlow(api, render);
    flow.acceptPreview(preview);
    await flow.setGroupSelected('group:score', true);
    expect(api.selectMigration).toHaveBeenCalledWith(12, { unitIds: ['attribute:score', 'attribute:bonus'], conflictChoices: {}, includeGeneralSettings: false, includeRoomSuggestion: false });
    expect(states.at(-1)?.preview?.units.filter((unit) => unit.selected).map((unit) => unit.id)).toEqual(['attribute:score', 'attribute:bonus']);
    expect(states.at(-1)?.preview?.units.find((unit) => unit.id === 'activity:legacy')).toEqual(expect.objectContaining({ selected: false, compatibility: { status: 'partial', reasonCodes: ['crop_presets_unsupported', 'display_scenes_unsupported'] } }));
    expect(states.at(-1)?.preview?.units.find((unit) => unit.id === 'timer:legacy')).toEqual(expect.objectContaining({ selected: false, compatibility: { status: 'incompatible', reasonCodes: ['timer_rules_unsupported'] } }));
  });

  it('keeps general settings and room suggestion independent from gameplay and requires every same-name conflict choice', async () => {
    const resolvedPreview: MigrationPreview = { ...selectedPreview, selection: { ...selectedPreview.selection, conflictChoices: { 'conflict:score': 'keep_both' }, includeGeneralSettings: true, includeRoomSuggestion: false }, canConfirm: true };
    const api = { selectMigration: vi.fn(async (_id: number, next: MigrationSelection) => next.conflictChoices['conflict:score'] ? resolvedPreview : { ...selectedPreview, selection: next }) };
    const { states, render } = collectStates();
    const flow = createMigrationFlow(api, render);
    flow.acceptPreview(selectedPreview);
    expect(states.at(-1)).toEqual(expect.objectContaining({ canApply: false }));
    await flow.setGeneralSettingsIncluded(true);
    await flow.setRoomSuggestionIncluded(false);
    await flow.setConflictChoice('conflict:score', 'keep_both');
    flow.confirmReplacement(true);
    expect(states.at(-1)).toEqual(expect.objectContaining({ canApply: true }));
    expect(states.at(-1)?.preview?.selection).toEqual(expect.objectContaining({ unitIds: ['attribute:score', 'attribute:bonus'], conflictChoices: { 'conflict:score': 'keep_both' }, includeGeneralSettings: true, includeRoomSuggestion: false }));
  });

  it('applies the exact server-confirmed selection, reports live progress, and exposes the seven-day rollback window', async () => {
    const applied: MigrationJob = { id: 12, status: 'applied', rollbackExpiresAt: '2030-01-08T00:00:00Z', obsLinks: [{ outputId: 'score', name: '积分', url: 'https://host.example/obs/score#token=a' }, { outputId: 'bonus', name: '加成', url: 'https://host.example/obs/bonus#token=b' }] };
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })), pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-02T00:00:00Z' })), cancelLogin: vi.fn(async () => undefined),
      applyMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const, expiresAt: preview.expiresAt })), getMigration: vi.fn(async () => applied),
    };
    const { states, render } = collectStates();
    const flow = createMigrationFlow(api, render, { now: () => new Date('2030-01-01T00:00:00Z') });
    flow.acceptPreview({ ...preview, selection: { ...selection, unitIds: ['attribute:score'] } });
    flow.confirmReplacement(true);
    await flow.apply();
    expect(api.applyMigration).toHaveBeenCalledWith(12, 'proof', { ...selection, unitIds: ['attribute:score'] });
    expect(states.at(-1)).toEqual(expect.objectContaining({ job: expect.objectContaining({ status: 'pending' }), canRefresh: true, applyProgress: 'waiting_for_live_boundary' }));
    await flow.refresh(12);
    expect(states.at(-1)).toEqual(expect.objectContaining({ job: applied, rollbackDaysRemaining: 7, canRollback: true, applyProgress: 'applied' }));
    expect(states.at(-1)?.obsChecklist).toEqual([{ outputId: 'score', name: '积分', url: 'https://host.example/obs/score#token=a', replaced: false }, { outputId: 'bonus', name: '加成', url: 'https://host.example/obs/bonus#token=b', replaced: false }]);
    flow.confirmOBSReplacement('score', true);
    expect(states.at(-1)?.obsChecklist?.[0]).toEqual(expect.objectContaining({ replaced: true }));
  });

  it('recognizes a duplicate package, preserves the authoritative pending preview, and expires preview actions after 24 hours', async () => {
    const duplicate = { ...preview, reused: true };
    const pending = { id: 12, status: 'pending' as const, expiresAt: preview.expiresAt };
    const api = { previewMigration: vi.fn(async () => duplicate), getMigration: vi.fn(async () => pending) };
    const { states, render } = collectStates();
    const clock = { now: () => new Date('2030-01-01T00:00:00Z') };
    const flow = createMigrationFlow(api, render, clock);
    await flow.preview({ name: 'package.json', size: 2, text: async () => '{}' });
    expect(states.at(-1)).toEqual(expect.objectContaining({ duplicatePackage: true, job: pending, preview: duplicate, canApply: false, canRefresh: true, previewHoursRemaining: 24 }));
    clock.now = () => new Date('2030-01-02T00:00:01Z');
    flow.refreshTime();
    expect(states.at(-1)).toEqual(expect.objectContaining({ previewHoursRemaining: 0, canApply: false, canCancel: false, canRefresh: false }));
  });
});

describe('migration prompt privacy', () => {
  it('persists only a non-sensitive dismissed boolean and leaves raw migration data out of storage and URLs', () => {
    const values = new Map<string, string>();
    const storage: Storage = { get length() { return values.size; }, clear: () => values.clear(), getItem: (key) => values.get(key) ?? null, key: (index) => [...values.keys()][index] ?? null, removeItem: (key) => { values.delete(key); }, setItem: (key, value) => { values.set(key, value); } };
    const beforeURL = 'https://host.example/account?tab=overview';
    expect(shouldShowMigrationPrompt(storage)).toBe(true);
    dismissMigrationPrompt(storage);
    expect(shouldShowMigrationPrompt(storage)).toBe(false);
    expect([...values.entries()]).toEqual([[migrationPromptStorageKey, 'true']]);
    expect(beforeURL).toBe('https://host.example/account?tab=overview');
    expect(JSON.stringify([...values.entries()])).not.toContain('gift-panel-online-migration');
  });
});
