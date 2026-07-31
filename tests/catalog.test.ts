import { describe, it, expect } from 'vitest';
import { upsertRecentGift, findGift, builtinCatalog, loadBuiltinCatalog } from '../src/gifts/catalog';
import { defaultState } from '../src/storage';
import { GiftEvent } from '../src/bilibili/messages';

function makeGift(id: number, name = '礼物'): GiftEvent {
  return { giftId: id, giftName: name, num: 1, price: 10, coinType: 'gold', totalCoin: 10, uname: 'u', uid: 1, timestamp: 1700000000, imgBasic: '', rnd: `x-${id}` };
}

describe('catalog', () => {
  it('upserts new gift to recent', () => {
    const s = defaultState();
    upsertRecentGift(s, makeGift(999));
    expect(s.recentGifts).toHaveLength(1);
    expect(s.recentGifts[0].id).toBe(999);
  });

  it('accumulates count on repeated gift', () => {
    const s = defaultState();
    upsertRecentGift(s, makeGift(999));
    upsertRecentGift(s, makeGift(999));
    expect(s.recentGifts[0].count).toBe(2);
  });

  it('finds gift in recent', () => {
    const s = defaultState();
    upsertRecentGift(s, makeGift(999));
    expect(findGift(s, 999)?.id).toBe(999);
  });

  it('prefers builtin catalog over recent gifts', () => {
    const s = defaultState();
    builtinCatalog.push({ id: 12345, name: '内置礼物', price: 1, coinType: 'gold', imgBasic: '' });
    try {
      upsertRecentGift(s, { ...makeGift(12345), giftName: '最近收到的' });
      expect(findGift(s, 12345)?.name).toBe('内置礼物');
    } finally {
      builtinCatalog.pop();
    }
  });

  it('loadBuiltinCatalog returns the builtin array', () => {
    expect(loadBuiltinCatalog()).toBe(builtinCatalog);
  });
});
