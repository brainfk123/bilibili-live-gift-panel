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
    expect(s.settings.panelOpacity).toBe(55);
  });

  it('round-trips state through save/load', () => {
    const s = defaultState();
    s.roomId = '2145';
    s.attributes.push({ name: '加班时间', value: 3600, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    s.settings.tutorialCompletedLessons = ['room', 'attribute'];
    saveState(s);
    const loaded = loadState();
    expect(loaded.roomId).toBe('2145');
    expect(loaded.attributes[0].value).toBe(3600);
    expect(loaded.rules).toHaveLength(1);
    expect(loaded.settings.tutorialVersion).toBe(2);
    expect(loaded.settings.tutorialCompletedLessons).toEqual(['room', 'attribute']);
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
    expect(consumeConfigMigrationRequired()).toBe(true);
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
