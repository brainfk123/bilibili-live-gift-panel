import { describe, it, expect, beforeEach, vi } from 'vitest';
import { defaultState, loadState, saveState, resetState } from '../src/storage';

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
});
