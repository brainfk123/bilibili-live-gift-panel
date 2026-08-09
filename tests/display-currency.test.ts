import { describe, expect, it } from 'vitest';
import {
  formatCompactYuanFromGoldSeeds,
  formatSignedYuanFromGoldSeeds,
  formatYuanFromGoldSeeds,
  goldSeedsFromYuan,
} from '../src/currency';

describe('OBS currency formatting', () => {
  it('converts gold seeds to yuan without losing sub-yuan values', () => {
    expect(formatYuanFromGoldSeeds(54_000)).toBe('54 元');
    expect(formatYuanFromGoldSeeds(100)).toBe('0.1 元');
    expect(formatYuanFromGoldSeeds(1)).toBe('0.001 元');
  });

  it('places the sign before the yuan amount', () => {
    expect(formatSignedYuanFromGoldSeeds(6_000)).toBe('+6 元');
    expect(formatSignedYuanFromGoldSeeds(-18_000)).toBe('-18 元');
    expect(formatSignedYuanFromGoldSeeds(0)).toBe('0 元');
  });

  it('compacts only large yuan amounts', () => {
    expect(formatCompactYuanFromGoldSeeds(9_000)).toBe('9 元');
    expect(formatCompactYuanFromGoldSeeds(54_000_000)).toBe('5.4万元');
  });

  it('converts yuan input back to the internal gold-seed value', () => {
    expect(goldSeedsFromYuan(0.1)).toBe(100);
    expect(goldSeedsFromYuan(54)).toBe(54_000);
  });
});
