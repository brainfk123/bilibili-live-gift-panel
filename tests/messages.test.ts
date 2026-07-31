import { describe, it, expect } from 'vitest';
import { parseGift, parseSc } from '../src/bilibili/messages';
import { formatValue, formatSeconds, formatNumber } from '../src/format';
import { Attribute } from '../src/types';

describe('parseGift', () => {
  it('extracts core fields', () => {
    const ev = parseGift({
      giftId: 30607,
      giftName: '辣条',
      num: 2,
      price: 20,
      coin_type: 'gold',
      total_coin: 40,
      uname: 'user',
      uid: 123,
      timestamp: 1700000000,
      gift_info: { img_basic: 'https://img' },
      rnd: 'x1',
    });
    expect(ev.giftId).toBe(30607);
    expect(ev.num).toBe(2);
    expect(ev.price).toBe(20);
    expect(ev.coinType).toBe('gold');
    expect(ev.imgBasic).toBe('https://img');
    expect(ev.rnd).toBe('x1');
  });

  it('handles missing optional fields', () => {
    const ev = parseGift({ giftId: 1, uname: 'u' });
    expect(ev.num).toBe(1);
    expect(ev.imgBasic).toBe('');
    expect(ev.rnd.length).toBeGreaterThan(0);
  });
});

describe('parseSc', () => {
  it('extracts fields', () => {
    const ev = parseSc({
      id: 42,
      price: 30,
      message: '你好',
      uid: 9,
      user_info: { uname: 'rich' },
      gift: { gift_id: 1223, gift_name: '醒目留言' },
    });
    expect(ev.id).toBe(42);
    expect(ev.message).toBe('你好');
    expect(ev.uname).toBe('rich');
    expect(ev.giftId).toBe(1223);
  });
});

describe('formatValue', () => {
  const hhmmss: Attribute = { name: 't', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' };
  const num: Attribute = { name: 'n', value: 0, unit: 'none', format: 'number', decimals: 2, suffix: '' };
  const suffix: Attribute = { name: 's', value: 0, unit: 'none', format: 'suffix', decimals: 0, suffix: '次' };

  it('formats hhmmss', () => expect(formatValue(90305, hhmmss)).toBe('1天 01:05:05'));
  it('formats hhmmss with days', () => expect(formatValue(3600 * 24 + 3600 + 2, hhmmss)).toBe('1天 01:00:02'));
  it('formats number with decimals', () => expect(formatValue(1234.5, num)).toBe('1,234.50'));
  it('formats suffix', () => expect(formatValue(125, suffix)).toBe('125 次'));
  it('formatSeconds pads zeros', () => expect(formatSeconds(65)).toBe('00:01:05'));
  it('formatNumber thousands', () => expect(formatNumber(1234567, 0)).toBe('1,234,567'));
});
