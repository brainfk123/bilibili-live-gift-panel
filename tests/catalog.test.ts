import { describe, it, expect } from 'vitest';
import { upsertRecentGift, findGift, builtinCatalog, giftDisplayKey, loadBuiltinCatalog, matchesGiftSearch, sameGiftIdentity, sortGiftsByUsage } from '../src/gifts/catalog';
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

  it('treats icon-only revisions as the same display gift and runtime alias', () => {
    const oldGift = { id: 1, name: '情书', price: 5200, coinType: 'gold' as const, imgBasic: 'old.png' };
    const currentGift = { id: 2, name: '情书', price: 5200, coinType: 'gold' as const, imgBasic: 'current.png' };
    expect(giftDisplayKey(oldGift)).toBe(giftDisplayKey(currentGift));
    expect(sameGiftIdentity(oldGift, currentGift)).toBe(true);
  });

  it('matches gift names partially but numeric gift IDs only exactly', () => {
    const gift = { id: 33345, name: '这个好诶' };
    expect(matchesGiftSearch(gift, '好诶')).toBe(true);
    expect(matchesGiftSearch(gift, '33')).toBe(false);
    expect(matchesGiftSearch(gift, '33345')).toBe(true);
    expect(matchesGiftSearch({ id: 1, name: '33周年礼物' }, '33')).toBe(true);
  });

  it('sorts configured and frequently received gifts before the room panel order', () => {
    const panel = [
      { id: 1, name: '普通礼物', price: 100, coinType: 'gold' as const, imgBasic: '' },
      { id: 2, name: '常收礼物', price: 200, coinType: 'gold' as const, imgBasic: '' },
      { id: 3, name: '已配置礼物', price: 300, coinType: 'gold' as const, imgBasic: '' },
      { id: 4, name: '最近礼物', price: 400, coinType: 'gold' as const, imgBasic: '' },
    ];
    const recent = [
      { ...panel[1], count: 8, lastReceived: 10 },
      { ...panel[3], count: 2, lastReceived: 20 },
    ];

    expect(sortGiftsByUsage(panel, [panel[2]], recent).map((gift) => gift.id)).toEqual([3, 2, 4, 1]);
  });
});
