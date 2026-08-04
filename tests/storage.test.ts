import { describe, it, expect, beforeEach, vi } from 'vitest';
import { consumeConfigMigrationRequired, defaultState, hydrateStateFromServer, loadState, saveState, resetState, pruneLog, refreshStateFromServer } from '../src/storage';
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

  it('round-trips state through save/load', () => {
    const s = defaultState();
    s.roomId = '2145';
    s.attributes.push({ name: '加班时间', value: 3600, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    s.settings.tutorialCompletedLessons = ['room', 'attribute'];
    s.settings.trainingCompletedTopics = ['blind-box', 'obs-no-change'];
    s.settings.lastSeenChangelogVersion = '0.2.0';
    saveState(s);
    const loaded = loadState();
    expect(loaded.roomId).toBe('2145');
    expect(loaded.attributes[0].value).toBe(3600);
    expect(loaded.rules).toHaveLength(1);
    expect(loaded.settings.tutorialVersion).toBe(2);
    expect(loaded.settings.tutorialCompletedLessons).toEqual(['room', 'attribute']);
    expect(loaded.settings.trainingCompletedTopics).toEqual(['blind-box', 'obs-no-change']);
    expect(loaded.settings.lastSeenChangelogVersion).toBe('0.2.0');
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
