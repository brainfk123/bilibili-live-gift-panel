import { describe, it, expect } from 'vitest';
import { addWbiSign, extractWbiKey, WBI_KEY_INDEX_TABLE } from '../server/wbi';

describe('wbi', () => {
  it('extracts wbi key from img_url/sub_url', () => {
    const key = extractWbiKey(
      'https://i0.hdslb.com/bfs/wbi/aaaabbbbccccddddeeeeffff00001111.png',
      'https://i0.hdslb.com/bfs/wbi/22223333444455556666777788889999.png',
    );
    expect(key).toHaveLength(32);
  });

  it('adds wbi signature with wts and w_rid', () => {
    const signed = addWbiSign({ id: 2145, type: 0 }, 'testkey1234567890abcdefghijklmnop');
    expect(signed.id).toBe(2145);
    expect(signed.type).toBe(0);
    expect(typeof signed.wts).toBe('string');
    expect(signed.w_rid).toMatch(/^[0-9a-f]{32}$/);
  });

  it('sorts params and filters special chars like blivedm', () => {
    const key = 'k'.repeat(32);
    const a = addWbiSign({ b: 'x!y', a: 1 }, key);
    const b = addWbiSign({ a: 1, b: 'x!y' }, key);
    expect(a.w_rid).toBe(b.w_rid);
  });

  it('wbi key index table has 32 entries in range', () => {
    expect(WBI_KEY_INDEX_TABLE).toHaveLength(32);
    for (const i of WBI_KEY_INDEX_TABLE) {
      expect(i).toBeGreaterThanOrEqual(0);
      expect(i).toBeLessThan(64);
    }
  });
});
