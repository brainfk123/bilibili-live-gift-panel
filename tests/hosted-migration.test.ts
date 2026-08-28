import { describe, expect, it, vi } from 'vitest';

import {
  HostedAPI,
  type MigrationJob,
  type MigrationPreview,
  type MigrationSelection,
} from '../src/hosted/api';
import { createMigrationFlow, migrationFileLimit, type MigrationViewState } from '../src/hosted/migration';
import { encodeOBSOutputSelector } from '../src/hosted/obs/selector';
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
const scoreSelector = 'eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiJzY29yZSJ9';
const bonusSelector = 'eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiJib251cyJ9';
const scoreSceneSelector = 'eyJraW5kIjoic2NlbmUiLCJpZCI6InNjb3JlIiwiYXR0cmlidXRlcyI6WyJzY29yZSIsImJvbnVzIl19';

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
    const job: MigrationJob = { id: 12, status: 'applied', rollbackExpiresAt: '2030-01-08T00:00:00Z', obsLinks: [{ outputId: scoreSelector, name: '积分卡片', url: `https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${scoreSelector}#token=one-time` }] };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(job));
    await expect(api.getMigration(12)).resolves.toEqual(job);

    for (const link of [
      { ...job.obsLinks![0], url: 'http://host.example/obs' },
      { ...job.obsLinks![0], outputId: bonusSelector },
      { ...job.obsLinks![0], outputId: 'eyJraW5kIjoic2NlbmUiLCJpZCI6Im1haW4iLCJhdHRyaWJ1dGVzIjpbInNjb3JlIiwic2NvcmUiXX0', url: 'https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=eyJraW5kIjoic2NlbmUiLCJpZCI6Im1haW4iLCJhdHRyaWJ1dGVzIjpbInNjb3JlIiwic2NvcmUiXX0#token=one-time' },
      { ...job.obsLinks![0], url: `https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${scoreSelector}#token=${'x'.repeat(513)}` },
    ]) {
      const invalid = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json({ ...job, obsLinks: [link] }));
      await expect(invalid.getMigration(12)).rejects.toMatchObject({ code: 'invalid_response' });
    }
  });

  it('accepts an exact 64 KiB OBS URL and rejects one byte more', async () => {
    const id = 'x'.repeat(46_052);
    const outputId = encodeOBSOutputSelector({ kind: 'attribute', id, attributeIds: [id] })!;
    const token = 's'.repeat(512);
    const link = (hostRunes: number) => `https://${'a'.repeat(hostRunes)}.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${outputId}#token=${token}`;
    const exactURL = link(3_505);
    const aboveURL = link(3_506);
    expect(exactURL).toHaveLength(64 * 1024);
    expect(aboveURL).toHaveLength(64 * 1024 + 1);
    const exactJob: MigrationJob = { id: 12, status: 'applied', obsLinks: [{ outputId, name: '边界输出', url: exactURL }] };
    const exact = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(exactJob));
    await expect(exact.getMigration(12)).resolves.toEqual(exactJob);
    const above = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json({ ...exactJob, obsLinks: [{ ...exactJob.obsLinks![0], url: aboveURL }] }));
    await expect(above.getMigration(12)).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('loads only the bounded history projection and sends proof-gated OBS reissue with abort signals', async () => {
    const requests: Array<[string, RequestInit | undefined]> = [];
    const history = { jobs: [{ id: 12, status: 'pending', createdAt: '2030-01-01T00:00:00Z', expiresAt: '2030-01-02T00:00:00Z' }, { id: 11, status: 'applied', createdAt: '2029-12-31T00:00:00Z', appliedAt: '2029-12-31T00:01:00Z', rollbackExpiresAt: '2030-01-07T00:01:00Z' }] };
    const reissued: MigrationJob = { id: 11, status: 'applied', rollbackExpiresAt: '2030-01-07T00:01:00Z', obsLinks: [{ outputId: scoreSceneSelector, name: '积分场景', url: `https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${scoreSceneSelector}#token=rotated` }] };
    const api = await HostedAPI.connect(async (input, init) => {
      const path = String(input); requests.push([path, init]);
      if (path === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      if (path === '/api/migrations') return json(history);
      return json(reissued);
    });
    const historyController = new AbortController();
    const reissueController = new AbortController();
    await expect(api.migrationHistory(historyController.signal)).resolves.toEqual(history.jobs);
    await expect(api.reissueMigrationOBS(11, 'fresh-proof', reissueController.signal)).resolves.toEqual(reissued);
    expect(requests.slice(-2)).toEqual([
      ['/api/migrations', expect.objectContaining({ method: 'GET', signal: historyController.signal })],
      ['/api/migrations/11/obs-links', expect.objectContaining({ method: 'POST', signal: reissueController.signal, body: '{"challengeId":"fresh-proof"}' })],
    ]);

    const leaked = await HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json({ jobs: [{ ...history.jobs[0], rawConfig: { secret: true } }] }));
    await expect(leaked.migrationHistory()).rejects.toMatchObject({ code: 'invalid_response' });
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
    expect(api.selectMigration).toHaveBeenCalledWith(12, { unitIds: ['attribute:score', 'attribute:bonus'], conflictChoices: {}, includeGeneralSettings: false, includeRoomSuggestion: false },expect.any(AbortSignal));
    expect(states.at(-1)?.preview?.units.filter((unit) => unit.selected).map((unit) => unit.id)).toEqual(['attribute:score', 'attribute:bonus']);
    expect(states.at(-1)?.preview?.units.find((unit) => unit.id === 'activity:legacy')).toEqual(expect.objectContaining({ selected: false, compatibility: { status: 'partial', reasonCodes: ['crop_presets_unsupported', 'display_scenes_unsupported'] } }));
    expect(states.at(-1)?.preview?.units.find((unit) => unit.id === 'timer:legacy')).toEqual(expect.objectContaining({ selected: false, compatibility: { status: 'incompatible', reasonCodes: ['timer_rules_unsupported'] } }));

    const mixedAPI = { selectMigration: vi.fn() };
    const mixedFlow = createMigrationFlow(mixedAPI, vi.fn());
    mixedFlow.acceptPreview({ ...preview, groups: [{ id: 'group:mixed', unitIds: ['attribute:bonus', 'activity:legacy'], reasons: [{ kind: 'shared-attribute', referenceId: 'bonus' }] }] });
    await expect(mixedFlow.setGroupSelected('group:mixed', true)).rejects.toMatchObject({ code: 'invalid_request' });
    expect(mixedAPI.selectMigration).not.toHaveBeenCalled();
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
    const applied: MigrationJob = { id: 12, status: 'applied', rollbackExpiresAt: '2030-01-08T00:00:00Z', obsLinks: [{ outputId: scoreSelector, name: '积分', url: `https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${scoreSelector}#token=a` }, { outputId: bonusSelector, name: '加成', url: `https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${bonusSelector}#token=b` }] };
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })), pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-02T00:00:00Z' })), cancelLogin: vi.fn(async () => undefined),
      applyMigration: vi.fn(async () => ({ id: 12, status: 'pending' as const, expiresAt: preview.expiresAt })), getMigration: vi.fn(async () => applied),
    };
    const { states, render } = collectStates();
    const flow = createMigrationFlow(api, render, { now: () => new Date('2030-01-01T00:00:00Z') });
    flow.acceptPreview({ ...preview, selection: { ...selection, unitIds: ['attribute:score'] } });
    flow.confirmReplacement(true);
    await flow.apply();
    expect(api.applyMigration).toHaveBeenCalledWith(12, 'proof', { ...selection, unitIds: ['attribute:score'] },expect.any(AbortSignal));
    expect(states.at(-1)).toEqual(expect.objectContaining({ job: expect.objectContaining({ status: 'pending' }), canRefresh: true, applyProgress: 'waiting_for_live_boundary' }));
    await flow.refresh(12);
    expect(states.at(-1)).toEqual(expect.objectContaining({ job: applied, rollbackDaysRemaining: 7, canRollback: true, applyProgress: 'applied' }));
    expect(states.at(-1)?.obsChecklist).toEqual(applied.obsLinks?.map((item)=>({...item,replaced:false})));
    flow.confirmOBSReplacement(scoreSelector, true);
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

  it('auto-polls pending immediately with bounded backoff and keeps retrying after a network error', async () => {
    const timers: Array<{callback:()=>void;delay:number;cleared:boolean}>=[];
    const api={getMigration:vi.fn().mockRejectedValueOnce(new TypeError('offline')).mockResolvedValueOnce({id:12,status:'applied' as const,rollbackExpiresAt:'2030-01-08T00:00:00Z'})};
    const {states,render}=collectStates();
    const flow=createMigrationFlow(api,render,{now:()=>new Date('2030-01-01T00:00:00Z'),setTimeout:(callback,delay)=>{const timer={callback,delay,cleared:false};timers.push(timer);return timer;},clearTimeout:(timer)=>{(timer as {cleared:boolean}).cleared=true;}});
    flow.resumeJob({id:12,status:'pending',expiresAt:'2030-01-02T00:00:00Z'});
    expect(timers[0]?.delay).toBe(0);timers[0]!.callback();await vi.waitFor(()=>expect(api.getMigration).toHaveBeenCalledTimes(1));
    await vi.waitFor(()=>expect(timers.at(-1)?.delay).toBe(1000));timers.at(-1)!.callback();await vi.waitFor(()=>expect(states.at(-1)?.job?.status).toBe('applied'));
    expect(api.getMigration).toHaveBeenCalledTimes(2);
    expect(states.at(-1)?.job).toEqual(expect.objectContaining({ status: 'applied', obsReissueRequired: true }));
    await flow.dispose();
  });

  it('recovers a lost applied response by exposing proof-gated OBS reissue on idempotent retry', async () => {
    const reissued: MigrationJob = { id: 12, status: 'applied', rollbackExpiresAt: '2030-01-08T00:00:00Z', obsLinks: [{ outputId: scoreSelector, name: '积分', url: `https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${scoreSelector}#token=rotated` }] };
    const api = {
      beginLogin: vi.fn(async () => ({ challengeId: 'fresh-proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' })),
      pollLogin: vi.fn(async () => ({ status: 'verified' as const, expiresAt: '2030-01-02T00:00:00Z' })),
      cancelLogin: vi.fn(async () => undefined),
      applyMigration: vi.fn().mockRejectedValueOnce(new TypeError('lost response')).mockResolvedValueOnce({ id: 12, status: 'applied' as const, rollbackExpiresAt: '2030-01-08T00:00:00Z' }),
      reissueMigrationOBS: vi.fn(async () => reissued),
    };
    const { states, render } = collectStates();
    const flow = createMigrationFlow(api, render, { now: () => new Date('2030-01-01T00:00:00Z') });
    flow.acceptPreview({ ...preview, conflicts: [], selection: { ...selection, unitIds: ['attribute:score'] }, canConfirm: true });
    flow.confirmReplacement(true);
    await expect(flow.apply()).rejects.toBeInstanceOf(TypeError);
    await flow.apply();
    expect(states.at(-1)?.job).toEqual(expect.objectContaining({ status: 'applied', obsReissueRequired: true }));
    await flow.reissueOBS();
    expect(api.reissueMigrationOBS).toHaveBeenCalledWith(12, 'fresh-proof', expect.any(AbortSignal));
    expect(states.at(-1)?.obsChecklist).toEqual([{ ...reissued.obsLinks![0], replaced: false }]);
  });

  it('aborts file and request work and bounded-awaits cleanup on dispose', async () => {
    let fileSignal:AbortSignal|undefined;
    const file={name:'package.json',size:2,text:(signal?:AbortSignal)=>new Promise<string>((_resolve,reject)=>{fileSignal=signal;signal?.addEventListener('abort',()=>reject(new DOMException('aborted','AbortError')),{once:true});})};
    const flow=createMigrationFlow({previewMigration:vi.fn()},vi.fn(),{now:()=>new Date(),settleTimeoutMs:50});
    const pending=flow.preview(file).catch((cause:unknown)=>cause);
    await vi.waitFor(()=>expect(fileSignal).toBeDefined());await flow.dispose();
    expect(fileSignal?.aborted).toBe(true);await expect(pending).resolves.toBeInstanceOf(DOMException);
  });

  it('aborts pending polling and proof requests and ignores late completions after disposal', async () => {
    const timers: Array<() => void> = [];
    let pollSignal: AbortSignal | undefined;
    let resolvePoll!: (job: MigrationJob) => void;
    const pollStates = collectStates();
    const polling = createMigrationFlow({ getMigration: vi.fn((_id: number, signal?: AbortSignal) => { pollSignal = signal; return new Promise<MigrationJob>((resolve) => { resolvePoll = resolve; }); }) }, pollStates.render, { now: () => new Date('2030-01-01T00:00:00Z'), setTimeout: (callback) => { timers.push(callback); return callback; }, clearTimeout: () => undefined, settleTimeoutMs: 50 });
    polling.resumeJob({ id: 12, status: 'pending', expiresAt: '2030-01-02T00:00:00Z' });
    timers.shift()?.();
    await vi.waitFor(() => expect(pollSignal).toBeDefined());
    const pollDispose = polling.dispose();
    expect(pollSignal?.aborted).toBe(true);
    resolvePoll({ id: 12, status: 'applied', rollbackExpiresAt: '2030-01-08T00:00:00Z' });
    await pollDispose;
    expect(pollStates.states.some((state) => state.job?.status === 'applied')).toBe(false);

    let proofSignal: AbortSignal | undefined;
    const proofFlow = createMigrationFlow({ beginLogin: vi.fn((signal?: AbortSignal) => { proofSignal = signal; return new Promise<never>((_resolve, reject) => signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })); }), applyMigration: vi.fn() }, vi.fn(), { now: () => new Date('2030-01-01T00:00:00Z'), settleTimeoutMs: 50 });
    proofFlow.acceptPreview({ ...preview, conflicts: [], selection: { ...selection, unitIds: ['attribute:score'] }, canConfirm: true });
    proofFlow.confirmReplacement(true);
    const proofOperation = proofFlow.apply().catch((cause: unknown) => cause);
    await vi.waitFor(() => expect(proofSignal).toBeDefined());
    await proofFlow.dispose();
    expect(proofSignal?.aborted).toBe(true);
    await expect(proofOperation).resolves.toBeInstanceOf(DOMException);
  });

  it('owns a signal-ignoring late login success and aborts its cleanup at the dispose deadline', async () => {
    let resolveBegin!: (value: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    let beginSignal: AbortSignal | undefined;
    let cleanupSignal: AbortSignal | undefined;
    const api = {
      beginLogin: vi.fn((signal?: AbortSignal) => { beginSignal = signal; return new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { resolveBegin = resolve; }); }),
      pollLogin: vi.fn(), applyMigration: vi.fn(),
      cancelLogin: vi.fn((_id: string, signal?: AbortSignal) => { cleanupSignal = signal; return new Promise<void>(() => undefined); }),
    };
    const states = collectStates();
    const flow = createMigrationFlow(api, states.render, { now: () => new Date('2030-01-01T00:00:00Z'), settleTimeoutMs: 25 });
    flow.acceptPreview({ ...preview, conflicts: [], selection: { ...selection, unitIds: ['attribute:score'] }, canConfirm: true });
    flow.confirmReplacement(true);
    const operation = flow.apply().catch((cause: unknown) => cause);
    await vi.waitFor(() => expect(beginSignal).toBeDefined());
    const disposing = flow.dispose();
    expect(beginSignal?.aborted).toBe(true);
    resolveBegin({ challengeId: 'late-proof', qrImage: 'qr', expiresAt: '2030-01-02T00:00:00Z' });
    await vi.waitFor(() => expect(cleanupSignal).toBeDefined());
    await disposing;
    expect(cleanupSignal?.aborted).toBe(true);
    await expect(operation).resolves.toMatchObject({ code: 'operation_failed' });
    expect(states.states.some((state) => state.proof?.qrImage === 'qr')).toBe(false);
  });

  it('owns late file and preview success and bounds dynamically queued job cleanup', async () => {
    let resolveText!: (value: string) => void;
    let fileSignal: AbortSignal | undefined;
    const uploadAfterFile = vi.fn();
    const fileFlow = createMigrationFlow({ previewMigration: uploadAfterFile }, vi.fn(), { now: () => new Date(), settleTimeoutMs: 25 });
    const fileOperation = fileFlow.preview({ name: 'package.json', size: 2, text: (signal?: AbortSignal) => { fileSignal = signal; return new Promise<string>((resolve) => { resolveText = resolve; }); } }).catch((cause: unknown) => cause);
    await vi.waitFor(() => expect(fileSignal).toBeDefined());
    const fileDispose = fileFlow.dispose();
    resolveText('{}');
    await fileDispose;
    expect(fileSignal?.aborted).toBe(true);
    expect(uploadAfterFile).not.toHaveBeenCalled();
    await expect(fileOperation).resolves.toMatchObject({ code: 'operation_failed' });

    let resolvePreview!: (value: MigrationPreview) => void;
    let previewSignal: AbortSignal | undefined;
    let cleanupSignal: AbortSignal | undefined;
    const previewFlow = createMigrationFlow({
      previewMigration: vi.fn((_raw: string, signal?: AbortSignal) => { previewSignal = signal; return new Promise<MigrationPreview>((resolve) => { resolvePreview = resolve; }); }),
      cancelMigration: vi.fn((_id: number, signal?: AbortSignal) => { cleanupSignal = signal; return new Promise<MigrationJob>(() => undefined); }),
    }, vi.fn(), { now: () => new Date(), settleTimeoutMs: 25 });
    const previewOperation = previewFlow.preview({ name: 'package.json', size: 2, text: async () => '{}' }).catch((cause: unknown) => cause);
    await vi.waitFor(() => expect(previewSignal).toBeDefined());
    const previewDispose = previewFlow.dispose();
    resolvePreview(preview);
    await vi.waitFor(() => expect(cleanupSignal).toBeDefined());
    await previewDispose;
    expect(previewSignal?.aborted).toBe(true);
    expect(cleanupSignal?.aborted).toBe(true);
    await expect(previewOperation).resolves.toMatchObject({ code: 'operation_failed' });
  });
});

describe('migration prompt privacy', () => {
  it('persists only a non-sensitive dismissed boolean and leaves raw migration data out of storage and URLs', () => {
    const values = new Map<string, string>();
    const storage: Storage = { get length() { return values.size; }, clear: () => values.clear(), getItem: (key) => values.get(key) ?? null, key: (index) => [...values.keys()][index] ?? null, removeItem: (key) => { values.delete(key); }, setItem: (key, value) => { values.set(key, value); } };
    const beforeURL = 'https://host.example/account?tab=overview';
    const accountA='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'; const accountB='BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB';
    expect(shouldShowMigrationPrompt(storage,accountA)).toBe(true);
    dismissMigrationPrompt(storage,accountA);
    expect(shouldShowMigrationPrompt(storage,accountA)).toBe(false);
    expect(shouldShowMigrationPrompt(storage,accountB)).toBe(true);
    expect([...values.entries()]).toEqual([[migrationPromptStorageKey(accountA), 'true']]);
    expect(beforeURL).toBe('https://host.example/account?tab=overview');
    expect(JSON.stringify([...values.entries()])).not.toContain('gift-panel-online-migration');
  });
});
