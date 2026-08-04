import { describe, it, expect, beforeEach, vi } from 'vitest';
import { consumeConfigMigrationRequired, createConfigBackup, defaultState, hydrateStateFromServer, loadState, mergeConfigBackup, saveState, resetState, pruneLog, refreshStateFromServer } from '../src/storage';
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
    expect(s.settings.panelOpacity).toBe(55);
    expect(s.settings.trainingCompletedTopics).toEqual([]);
    expect(s.settings.lastSeenChangelogVersion).toBe('');
  });

  it('round-trips state through save/load', async () => {
    const s = defaultState();
    s.roomId = '2145';
    s.attributes.push({ name: '加班时间', value: 3600, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    s.settings.tutorialCompletedLessons = ['room', 'attribute'];
    s.settings.trainingCompletedTopics = ['blind-box', 'obs-no-change'];
    s.settings.lastSeenChangelogVersion = '0.2.0';
    await saveState(s);
    const loaded = loadState();
    expect(loaded.roomId).toBe('2145');
    expect(loaded.attributes[0].value).toBe(3600);
    expect(loaded.rules).toHaveLength(1);
    expect(loaded.settings.tutorialVersion).toBe(2);
    expect(loaded.settings.tutorialCompletedLessons).toEqual(['room', 'attribute']);
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
    expect(loaded.settings.showTutorial).toBe(true);
    expect(loaded.settings.tutorialVersion).toBe(2);
    expect(loaded.settings.tutorialCompletedLessons).toEqual([]);
    expect(loaded.settings.trainingCompletedTopics).toEqual([]);
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

  it('exports only configuration and gift metadata referenced by rules', () => {
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

    const backup = createConfigBackup(state);

    expect(backup.schemaVersion).toBe(1);
    expect(backup.giftCatalog.map((gift) => gift.id)).toEqual([1, 2]);
    expect(backup).not.toHaveProperty('recentGifts');
    expect(backup).not.toHaveProperty('stats');
    expect(backup).not.toHaveProperty('log');
    expect(backup).not.toHaveProperty('contributions');
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
