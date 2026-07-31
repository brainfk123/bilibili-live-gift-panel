import { describe, it, expect, beforeEach, vi } from 'vitest';
import { defaultState, loadState, saveState, resetState, pruneLog } from '../src/storage';
import { LogEntry, MAX_LOG } from '../src/types';

const mem = new Map<string, string>();
vi.stubGlobal('localStorage', {
  getItem: (k: string) => mem.get(k) ?? null,
  setItem: (k: string, v: string) => void mem.set(k, v),
  removeItem: (k: string) => void mem.delete(k),
});

beforeEach(() => mem.clear());

describe('storage', () => {
  it('loads default state when empty', () => {
    const s = loadState();
    expect(s.attributes[0].name).toBe('加班时间');
    expect(s.rules).toEqual([]);
  });

  it('round-trips state through save/load', () => {
    const s = defaultState();
    s.roomId = '2145';
    s.attributes[0].value = 3600;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    saveState(s);
    const loaded = loadState();
    expect(loaded.roomId).toBe('2145');
    expect(loaded.attributes[0].value).toBe(3600);
    expect(loaded.rules).toHaveLength(1);
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
