import { describe, it, expect, beforeEach, vi } from 'vitest';
import { clearRoomScopedRecords, consumeConfigMigrationRequired, createConfigBackup, defaultState, hydrateStateFromServer, loadState, mergeConfigBackup, saveState, resetState, pruneLog, refreshStateFromServer } from '../src/storage';
import { LogEntry, MAX_LOG } from '../src/types';

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })));
  resetState();
});

describe('storage', () => {
  it('loads default state when empty', () => {
    const s = loadState();
    expect(s.attributes).toEqual([]);
    expect(s.rules).toEqual([]);
    expect(s.contributions).toEqual({ viewers: [] });
    expect(s.giftKpiPanels).toEqual([]);
    expect(s.blindBoxDisplay).toEqual(expect.objectContaining({ themeId: 'glass', fontSize: 48, panelOpacity: 55, viewerSlots: 3 }));
    expect(s.settings.panelOpacity).toBe(55);
    expect(s.settings.trainingCompletedTopics).toEqual([]);
    expect(s.settings.lastSeenChangelogVersion).toBe('');
    expect(s.settings.tutorialReplayMode).toBe(false);
    expect(s.settings.configExperience).toBe('simple');
  });

  it('treats persisted settings without configExperience as advanced', async () => {
    const legacy = defaultState();
    const settings = { ...legacy.settings } as Partial<typeof legacy.settings>;
    delete settings.configExperience;
    legacy.settings = settings as typeof legacy.settings;
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(legacy)));

    await hydrateStateFromServer();

    expect(loadState().settings.configExperience).toBe('advanced');
    expect(consumeConfigMigrationRequired()).toBe(true);
  });

  it('clears only room-scoped records when switching rooms', () => {
    const state = defaultState();
    state.roomId = '100';
    state.attributes = [{ name: '积分', value: 7, unit: 'none', format: 'number', decimals: 0, suffix: '' }];
    state.rules = [{ id: 'r1', giftId: 1, attributeName: '积分', formula: '积分+1' }];
    state.giftCatalog = [{ id: 1, name: '测试礼物', price: 100, coinType: 'gold', imgBasic: '' }];
    state.recentGifts = [{ ...state.giftCatalog[0], lastReceived: 1, count: 2 }];
    state.stats = { today: { date: 'today', giftTotals: { 1: 2 }, ruleTriggers: { r1: 2 } } };
    state.log = [{ time: 1, giftId: 1, giftName: '测试礼物', num: 1, uname: '观众', attributeName: '积分', delta: 1, valueAfter: 7, ruleId: 'r1' }];
    state.contributions = { updatedAt: 1, viewers: [{ key: 'uid:1', uid: 1, uname: '观众', giftCount: 2, goldValue: 200, silverValue: 0, ruleTriggers: 2, attributeDeltas: { 积分: 2 }, blindBoxCount: 0, blindBoxCost: 0, blindBoxValue: 0, blindBoxProfit: 0, lastGiftAt: 1 }] };
    state.giftKpiPanels = [{
      id: 'kpi-1', name: '本场礼物目标', layout: 'grid',
      items: [{ giftId: 1, giftName: '测试礼物', imageUrl: '', target: 10, received: 4, barStyle: 'progress' }],
      appearance: { ...state.blindBoxDisplay },
    }];

    const cleared = clearRoomScopedRecords(state);

    expect(cleared.recentGifts).toEqual([]);
    expect(cleared.stats).toEqual({});
    expect(cleared.log).toEqual([]);
    expect(cleared.contributions.viewers).toEqual([]);
    expect(cleared.contributions.updatedAt).toBeGreaterThan(1);
    expect(cleared.attributes).toEqual(state.attributes);
    expect(cleared.rules).toEqual(state.rules);
    expect(cleared.giftCatalog).toEqual(state.giftCatalog);
    expect(cleared.giftKpiPanels[0].items[0].received).toBe(0);
    expect(cleared.giftKpiPanels[0].items[0].target).toBe(10);
    expect(state.log).toHaveLength(1);
  });

  it('round-trips state through save/load', async () => {
    const s = defaultState();
    s.roomId = '2145';
    s.attributes.push({ name: '加班时间', value: 3600, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    s.settings.tutorialCompletedLessons = ['room', 'attribute'];
    s.settings.tutorialReplayMode = true;
    s.settings.trainingCompletedTopics = ['blind-box', 'obs-no-change'];
    s.settings.lastSeenChangelogVersion = '0.2.0';
    await saveState(s);
    const loaded = loadState();
    expect(loaded.roomId).toBe('2145');
    expect(loaded.attributes[0].value).toBe(3600);
    expect(loaded.rules).toHaveLength(1);
    expect(loaded.settings.tutorialVersion).toBe(3);
    expect(loaded.settings.tutorialCompletedLessons).toEqual(['room', 'attribute']);
    expect(loaded.settings.tutorialReplayMode).toBe(true);
    expect(loaded.settings.trainingCompletedTopics).toEqual(['blind-box', 'obs-no-change']);
    expect(loaded.settings.lastSeenChangelogVersion).toBe('0.2.0');
  });

  it('saves only changed settings when history is larger than the keepalive limit', async () => {
    const serverState = defaultState();
    serverState.log = Array.from({ length: MAX_LOG }, (_, index) => ({
      time: index,
      giftId: index,
      giftName: '粉丝团灯牌',
      num: 1,
      uname: `观众-${index}-${'长昵称'.repeat(80)}`,
      attributeName: '加班时间',
      delta: 60,
      valueAfter: 60,
      ruleId: `rule-${index}`,
    }));
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, request?: RequestInit) => (
      request?.method === 'PATCH'
        ? new Response(null, { status: 200 })
        : new Response(JSON.stringify(serverState), { status: 200, headers: { 'Content-Type': 'application/json' } })
    ));
    vi.stubGlobal('fetch', fetchMock);
    await hydrateStateFromServer();
    const state = { ...loadState(), settings: { ...loadState().settings, theme: 'light' as const } };

    await saveState(state);

    const [, request] = fetchMock.mock.calls.at(-1) ?? [];
    expect(request?.method).toBe('PATCH');
    expect(JSON.parse(String(request?.body))).toEqual({ settings: state.settings });
    expect(String(request?.body).length).toBeLessThan(64 * 1024);
    expect(request?.keepalive).toBe(true);
  });

  it('clears a persisted simplePlay shard with null', async () => {
    const serverState = defaultState();
    serverState.simplePlay = {
      version: 1,
      templateId: 'counter',
      templateVersion: 1,
      attributeId: 'attribute-counter',
      parameters: { name: '积分' },
      gifts: { count: [1] },
      managedFingerprint: 'simple-play-v1-test',
    };
    serverState.attributes = [{
      id: 'attribute-counter', name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '',
    }];
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, request?: RequestInit) => (
      request?.method === 'PATCH'
        ? new Response(null, { status: 200 })
        : Response.json(serverState)
    ));
    vi.stubGlobal('fetch', fetchMock);
    await hydrateStateFromServer();
    const next = { ...loadState(), simplePlay: undefined };

    await saveState(next);

    const [, request] = fetchMock.mock.calls.at(-1) ?? [];
    expect(JSON.parse(String(request?.body))).toEqual({ simplePlay: null });
  });

  it('omits keepalive when the changed shard itself exceeds the browser limit', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _request?: RequestInit) => new Response(null, { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const state = defaultState();
    state.log = Array.from({ length: MAX_LOG }, (_, index) => ({
      time: index, giftId: index, giftName: '礼物', num: 1,
      uname: `观众-${'长昵称'.repeat(100)}`, attributeName: '积分', delta: 1, valueAfter: 1, ruleId: `r-${index}`,
    }));

    await saveState(state);

    const [, request] = fetchMock.mock.calls.at(-1) ?? [];
    expect(request?.method).toBe('PATCH');
    expect(String(request?.body).length).toBeGreaterThan(64 * 1024);
    expect(request).not.toHaveProperty('keepalive');
  });

  it('loads and normalizes the disk configuration from the server', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      roomId: '31567150', attributes: [], rules: [], settings: { fontSize: 48, accentColor: '#fb7299', showStats: true, showConnection: true, align: 'center' },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    await hydrateStateFromServer();
    const loaded = loadState();
    expect(loaded.roomId).toBe('31567150');
    expect(loaded.settings.theme).toBe('dark');
    expect(loaded.settings.giftView).toBe('list');
    expect(loaded.settings.panelOpacity).toBe(55);
    expect(loaded.blindBoxDisplay).toEqual(expect.objectContaining({
      themeId: 'glass', fontSize: 48, accentColor: '#fb7299', align: 'center', panelOpacity: 55,
    }));
    expect(loaded.settings.showTutorial).toBe(true);
    expect(loaded.settings.tutorialVersion).toBe(3);
    expect(loaded.settings.tutorialCompletedLessons).toEqual([]);
    expect(loaded.settings.tutorialReplayMode).toBe(false);
    expect(loaded.settings.trainingCompletedTopics).toEqual([]);
    expect(consumeConfigMigrationRequired()).toBe(true);
  });

  it('migrates legacy global appearance values into each OBS panel', async () => {
    const legacy = defaultState();
    legacy.settings = {
      ...legacy.settings,
      fontSize: 66,
      accentColor: '#123456',
      showConnection: false,
      align: 'right',
      panelOpacity: 72,
      defaultDisplayThemeId: 'neon',
    };
    legacy.attributes = [{
      name: '积分', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '',
      display: { variant: 'number', themeId: 'pixel' },
    }];
    legacy.displayScenes = [{
      id: 'scene-1', name: '积分面板', attributeNames: ['积分'], layout: 'stack', themeId: 'kawaii',
    }];
    delete (legacy as Partial<typeof legacy>).blindBoxDisplay;
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(legacy)));

    await hydrateStateFromServer();

    const loaded = loadState();
    expect(loaded.attributes[0].display?.appearance).toEqual({
      themeId: 'pixel', fontSize: 66, accentColor: '#123456', showConnection: false, align: 'right', panelOpacity: 72,
    });
    expect(loaded.displayScenes[0].appearance).toEqual({
      themeId: 'kawaii', fontSize: 66, accentColor: '#123456', showConnection: false, align: 'right', panelOpacity: 72,
    });
    expect(loaded.blindBoxDisplay).toEqual({
      themeId: 'neon', fontSize: 66, accentColor: '#123456', showConnection: false, align: 'right', panelOpacity: 72, viewerSlots: 3,
    });
    expect(consumeConfigMigrationRequired()).toBe(true);
  });

  it('clamps blind-box viewer slots to the supported 1–10 range', async () => {
    const serverState = defaultState();
    serverState.blindBoxDisplay.viewerSlots = 99;
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(serverState)));

    await hydrateStateFromServer();
    expect(loadState().blindBoxDisplay.viewerSlots).toBe(10);

    serverState.blindBoxDisplay.viewerSlots = -4;
    await refreshStateFromServer();
    expect(loadState().blindBoxDisplay.viewerSlots).toBe(1);
  });

  it('infers replay mode for a complete legacy configuration with reset tutorial progress', async () => {
    const legacy = defaultState();
    legacy.roomId = '31567150';
    legacy.attributes.push({ name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' });
    legacy.rules.push({ id: 'r1', giftId: 1, attributeName: '积分', formula: '积分+1' });
    const settings = { ...legacy.settings } as Partial<typeof legacy.settings>;
    delete settings.tutorialReplayMode;
    legacy.settings = settings as typeof legacy.settings;
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(legacy)));

    await hydrateStateFromServer();

    expect(loadState().settings.tutorialReplayMode).toBe(true);
    expect(consumeConfigMigrationRequired()).toBe(true);
  });

  it('normalizes enum value mappings and drops invalid or duplicate entries', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ...defaultState(),
      attributes: [{
        name: '结果', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '',
        display: {
          variant: 'enum', themeId: 'glass', valueMappings: [
            { value: 1, label: ' 红队胜 ', color: '#ff3366', imageUrl: 'https://example.com/red.png' },
            { value: 1, label: '重复' },
            { value: 2, label: '' },
            { value: 3, label: '蓝队胜', color: 'red', imageUrl: 'javascript:bad' },
          ],
        },
      }],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })));

    await hydrateStateFromServer();

    expect(loadState().attributes[0].display?.valueMappings).toEqual([
      { value: 1, label: '红队胜', color: '#ff3366', imageUrl: 'https://example.com/red.png' },
      { value: 3, label: '蓝队胜' },
    ]);
  });

  it('normalizes persisted contribution rankings and recomputes blind-box profit', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ...defaultState(),
      contributions: {
        updatedAt: 200,
        viewers: [
          {
            key: 'uid:1', uid: 1, uname: ' 观众 ', giftCount: 3.8, goldValue: 5000, silverValue: -1,
            ruleTriggers: 2, attributeDeltas: { 积分: 4, '': 99 }, blindBoxCount: 1,
            blindBoxCost: 9000, blindBoxValue: 12000, blindBoxProfit: -999, lastGiftAt: 100,
            blindBoxes: [
              { giftId: 35800, giftName: ' 心动盲盒 ', count: 1, cost: 9000, value: 12000, profit: -999, lastGiftAt: 100 },
              { giftId: 35800, giftName: '心动盲盒', count: 2, cost: 18000, value: 8000, profit: 999, unpricedCount: 1, lastGiftAt: 200 },
              { giftId: 0, giftName: '无效盲盒', count: 1, cost: 1, value: 1, profit: 0, lastGiftAt: 300 },
            ],
          },
          { key: 'uid:1', uid: 1, uname: '重复', giftCount: 99 },
        ],
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })));

    await hydrateStateFromServer();

    expect(loadState().contributions).toEqual({
      updatedAt: 200,
      viewers: [{
        key: 'uid:1', uid: 1, uname: '观众', giftCount: 3, goldValue: 5000, silverValue: 0,
        ruleTriggers: 2, attributeDeltas: { 积分: 4 }, blindBoxCount: 1,
        blindBoxCost: 9000, blindBoxValue: 12000, blindBoxProfit: 3000, lastGiftAt: 100,
        blindBoxes: [{
          giftId: 35800, giftName: '心动盲盒', count: 3, cost: 27000, value: 20000,
          profit: -7000, unpricedCount: 1, lastGiftAt: 200,
        }],
      }],
    });
  });

  it('hides the tutorial for a completed legacy config and marks the field for persistence', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      roomId: '31567150',
      attributes: [{ name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' }],
      rules: [{ id: 'r1', giftId: 1, attributeName: '积分', formula: '积分+1' }],
      settings: {},
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })));

    await hydrateStateFromServer();
    expect(loadState().settings.showTutorial).toBe(false);
    expect(consumeConfigMigrationRequired()).toBe(true);
  });

  it('does not apply a server refresh rejected as stale', async () => {
    const local = defaultState();
    local.roomId = 'new-room';
    await saveState(local);
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ...defaultState(),
      roomId: 'stale-room',
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })));

    await refreshStateFromServer(() => false);

    expect(loadState().roomId).toBe('new-room');
  });

  it('exports only configuration and referenced gift metadata without runtime progress', () => {
    const state = defaultState();
    state.rules = [{ id: 'r1', giftId: 1, matchGiftIds: [2], attributeName: '积分', formula: '积分+1' }];
    state.giftCatalog = [
      { id: 1, name: '父礼物', price: 100, coinType: 'gold', imgBasic: '1.png' },
      { id: 2, name: '子礼物', price: 200, coinType: 'gold', imgBasic: '2.png' },
      { id: 3, name: '无关礼物', price: 300, coinType: 'gold', imgBasic: '3.png' },
    ];
    state.recentGifts = [{ id: 4, name: '历史礼物', price: 400, coinType: 'gold', imgBasic: '4.png', lastReceived: 1, count: 1 }];
    state.log = [{ time: 1, giftId: 1, giftName: '父礼物', num: 1, uname: '观众', attributeName: '积分', delta: 1, valueAfter: 1, ruleId: 'r1' }];
    state.stats = { today: { date: 'today', giftTotals: { 1: 1 }, ruleTriggers: { r1: 1 } } };
    state.contributions = { viewers: [{ key: 'uid:1', uid: 1, uname: '观众', giftCount: 1, goldValue: 100, silverValue: 0, ruleTriggers: 1, attributeDeltas: { 积分: 1 }, blindBoxCount: 0, blindBoxCost: 0, blindBoxValue: 0, blindBoxProfit: 0, lastGiftAt: 1 }] };
    state.giftKpiPanels = [{
      id: 'target-1', name: '本场目标', layout: 'grid',
      items: [{ giftId: 2, giftName: '子礼物', imageUrl: '2.png', target: 10, received: 7, barStyle: 'progress' }],
      appearance: { ...state.blindBoxDisplay },
    }];
    state.simplePlay = {
      version: 1,
      templateId: 'counter',
      templateVersion: 1,
      attributeId: 'attribute-counter',
      parameters: { name: '积分', amount: 1 },
      gifts: { count: [1, 2] },
      managedFingerprint: 'simple-play-v1-test',
    };

    const backup = createConfigBackup(state);

    expect(backup.schemaVersion).toBe(5);
    expect(backup.giftCatalog.map((gift) => gift.id)).toEqual([1, 2]);
    expect(backup.giftKpiPanels[0].items[0]).not.toHaveProperty('received');
    expect(backup.simplePlay).toEqual(state.simplePlay);
    expect(backup).not.toHaveProperty('recentGifts');
    expect(backup).not.toHaveProperty('stats');
    expect(backup).not.toHaveProperty('log');
    expect(backup).not.toHaveProperty('contributions');
  });

  it('imports gift target definitions without replacing local progress', () => {
    const current = defaultState();
    current.giftKpiPanels = [{
      id: 'target-1', name: '旧名称', layout: 'grid',
      items: [{ giftId: 1, giftName: '小花花', imageUrl: '', target: 10, received: 7, barStyle: 'progress' }],
      appearance: { ...current.blindBoxDisplay },
    }];

    const merged = mergeConfigBackup(current, {
      schemaVersion: 5,
      giftKpiPanels: [{
        id: 'target-1', name: '新名称', layout: 'stack',
        items: [{ giftId: 1, giftName: '小花花', imageUrl: '', target: 30, barStyle: 'health' }],
        appearance: { ...current.blindBoxDisplay },
      }],
    });

    expect(merged.giftKpiPanels[0].name).toBe('新名称');
    expect(merged.giftKpiPanels[0].items[0]).toEqual(expect.objectContaining({ target: 30, received: 7 }));
  });

  it('imports legacy full backups without replacing local history', () => {
    const current = defaultState();
    current.log = [{ time: 9, giftId: 9, giftName: '本地礼物', num: 1, uname: '本地观众', attributeName: '积分', delta: 1, valueAfter: 1, ruleId: 'local' }];
    current.stats = { local: { date: 'local', giftTotals: {}, ruleTriggers: {} } };
    current.recentGifts = [{ id: 9, name: '本地礼物', price: 100, coinType: 'gold', imgBasic: 'local.png', lastReceived: 9, count: 1 }];
    current.contributions = { viewers: [], updatedAt: 9 };
    const imported = {
      roomId: '31567150',
      attributes: [{ name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' }],
      rules: [],
      log: [],
      stats: {},
      recentGifts: [],
      contributions: { viewers: [], updatedAt: 1 },
    };

    const merged = mergeConfigBackup(current, imported);

    expect(merged.roomId).toBe('31567150');
    expect(merged.attributes[0].name).toBe('积分');
    expect(merged.log).toEqual(current.log);
    expect(merged.stats).toEqual(current.stats);
    expect(merged.recentGifts).toEqual(current.recentGifts);
    expect(merged.contributions).toEqual(current.contributions);
  });

  it('imports pre-simple-mode backups as advanced and clears stale simple play metadata', () => {
    const current = defaultState();
    current.attributes = [{ id: 'old-simple', name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' }];
    current.simplePlay = {
      version: 1,
      templateId: 'overtime',
      templateVersion: 2,
      attributeId: 'old-simple',
      parameters: { name: '加班时间', maxSeconds: 0 },
      gifts: { overtime: [1] },
      managedFingerprint: 'simple-play-v1-old',
    };

    const merged = mergeConfigBackup(current, {
      schemaVersion: 4,
      settings: { theme: 'dark' },
      attributes: [{ id: 'legacy-score', name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' }],
      rules: [],
    });

    expect(merged.settings.configExperience).toBe('advanced');
    expect(merged.simplePlay).toBeUndefined();
    expect(merged.attributes[0].id).toBe('legacy-score');
  });

  it('drops simple play metadata when its managed attribute no longer exists', () => {
    const current = defaultState();
    const merged = mergeConfigBackup(current, {
      schemaVersion: 5,
      settings: { ...current.settings, configExperience: 'simple' },
      attributes: [],
      simplePlay: {
        version: 1,
        templateId: 'counter',
        templateVersion: 1,
        attributeId: 'missing',
        parameters: { name: '积分' },
        gifts: { count: [1] },
        managedFingerprint: 'simple-play-v1-missing',
      },
    });

    expect(merged.simplePlay).toBeUndefined();
  });

  it('rejects backups created by a newer schema', () => {
    expect(() => mergeConfigBackup(defaultState(), { schemaVersion: 999 })).toThrow('请先更新程序');
  });

  it('resetState removes stored state', () => {
    saveState(defaultState());
    resetState();
    expect(loadState().roomId).toBe('');
  });

  it('pruneLog keeps the first MAX_LOG entries, preserving input order', () => {
    const entry = (time: number): LogEntry => ({
      time,
      giftId: 0,
      giftName: '',
      num: 0,
      uname: '',
      attributeName: '',
      delta: 0,
      valueAfter: 0,
      ruleId: '',
    });
    const log = Array.from({ length: MAX_LOG + 5 }, (_, i) => entry(i));
    const pruned = pruneLog(log);
    expect(pruned).toHaveLength(MAX_LOG);
    expect(pruned[0].time).toBe(0);
    expect(pruned[pruned.length - 1].time).toBe(MAX_LOG - 1);
  });
});
