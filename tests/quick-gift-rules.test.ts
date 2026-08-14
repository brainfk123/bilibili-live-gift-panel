import { describe, expect, it } from 'vitest';
import {
  buildQuickGiftFormula,
  detectQuickGiftRule,
  quickGiftOperationLabel,
  quickGiftOperationSupportsMaximum,
  quickGiftOperationUnit,
  quickGiftOperationUsesAmount,
  quickGiftOperationUsesRange,
  validateQuickGiftRuleDraft,
} from '../src/ui/config/quick-gift-rules';

describe('quick gift rule presets', () => {
  it.each([
    [{ operation: 'add', amount: 60 }, '加班时间+60'],
    [{ operation: 'subtract', amount: 60 }, 'MAX(加班时间-60,0)'],
    [{ operation: 'double', amount: 60 }, '加班时间*2'],
    [{ operation: 'halve', amount: 60 }, 'MAX(FLOOR(加班时间/2),0)'],
    [{ operation: 'price', amount: 60 }, '加班时间+price/1000*60'],
    [{ operation: 'priceSubtract', amount: 60 }, 'MAX(加班时间-price/1000*60,0)'],
    [{ operation: 'set', amount: 120 }, '120'],
    [{ operation: 'reset', amount: 999 }, '0'],
    [{ operation: 'advanced', amount: 60 }, null],
  ] as const)('builds a non-random operation as a backend formula', (draft, formula) => {
    expect(buildQuickGiftFormula(draft, '加班时间')).toBe(formula);
  });

  it.each([
    [{ operation: 'randomRange', rangeMin: -60, rangeMax: 60 }, 'MAX(积分+RANDBETWEEN(-60,60),0)'],
    [{ operation: 'randomRange', rangeMin: -60, rangeMax: -1 }, 'MAX(积分+RANDBETWEEN(-60,-1),0)'],
    [{ operation: 'randomRange', rangeMin: 1, rangeMax: 60 }, 'MAX(积分+RANDBETWEEN(1,60),0)'],
    [{ operation: 'randomRange', rangeMin: 0, rangeMax: 0 }, 'MAX(积分+RANDBETWEEN(0,0),0)'],
    [{ operation: 'randomRange', rangeMin: -60, rangeMax: 60, maximum: 100 }, 'MIN(MAX(积分+RANDBETWEEN(-60,60),0),100)'],
  ] as const)('builds a signed random range %#', (draft, expected) => {
    expect(buildQuickGiftFormula(draft, '积分')).toBe(expected);
  });

  it.each([
    ['加班时间+60', 'add', 60],
    ['MAX(加班时间-60,0)', 'subtract', 60],
    ['加班时间*2', 'double', 2],
    ['MAX(FLOOR(加班时间/2),0)', 'halve', 2],
    ['加班时间+price/1000*60', 'price', 60],
    ['MAX(加班时间-price/1000*60,0)', 'priceSubtract', 60],
    ['120', 'set', 120],
    ['0', 'reset', 0],
    ['IF(加班时间>0,加班时间-1,0)', 'advanced', 60],
  ] as const)('detects %s without changing its meaning', (formula, operation, amount) => {
    expect(detectQuickGiftRule(formula, '加班时间')).toEqual({ operation, amount });
  });

  it.each([
    ['积分+RANDBETWEEN(1,60)', { operation: 'randomRange', rangeMin: 1, rangeMax: 60 }],
    ['MAX(积分-RANDBETWEEN(1,60),0)', { operation: 'randomRange', rangeMin: -60, rangeMax: -1 }],
    ['MAX(积分+RANDBETWEEN(-60,60),0)', { operation: 'randomRange', rangeMin: -60, rangeMax: 60 }],
    ['MIN(MAX(积分+RANDBETWEEN(-60,60),0),100)', {
      operation: 'randomRange', rangeMin: -60, rangeMax: 60, maximum: 100,
    }],
  ] as const)('detects legacy and canonical random formula %s', (formula, expected) => {
    expect(detectQuickGiftRule(formula, '积分')).toEqual(expected);
  });

  it.each([
    [{ operation: 'randomRange', rangeMin: 2.5, rangeMax: 5 }, '随机范围必须使用整数'],
    [{ operation: 'randomRange', rangeMin: 10, rangeMax: -10 }, '随机范围的最小变化不能大于最大变化'],
  ] as const)('rejects invalid random range %#', (draft, message) => {
    expect(validateQuickGiftRuleDraft(draft)).toBe(message);
  });

  it('provides beginner-facing copy and field behavior', () => {
    expect(quickGiftOperationLabel('subtract', '积分')).toBe('让“积分”减少（最低为 0）');
    expect(quickGiftOperationUnit('price', true)).toBe('秒');
    expect(quickGiftOperationUsesAmount('reset')).toBe(false);
    expect(quickGiftOperationUsesAmount('advanced')).toBe(false);
    expect(quickGiftOperationUsesAmount('double')).toBe(false);
    expect(quickGiftOperationUsesAmount('halve')).toBe(false);
    expect(quickGiftOperationUsesAmount('randomRange')).toBe(false);
    expect(quickGiftOperationUsesRange('randomRange')).toBe(true);
    expect(quickGiftOperationSupportsMaximum('add')).toBe(true);
    expect(quickGiftOperationSupportsMaximum('subtract')).toBe(false);
    expect(quickGiftOperationSupportsMaximum('double')).toBe(true);
  });

  it('wraps increasing rules with an optional upper limit', () => {
    const formula = buildQuickGiftFormula({ operation: 'add', amount: 60, maximum: 3600 }, '加班时间');
    expect(formula).toBe('MIN(加班时间+60,3600)');
    expect(detectQuickGiftRule(formula!, '加班时间')).toEqual({
      operation: 'add',
      amount: 60,
      maximum: 3600,
    });
  });

  it('caps doubling but not halving', () => {
    expect(buildQuickGiftFormula({ operation: 'double', amount: 0, maximum: 3600 }, '加班时间')).toBe('MIN(加班时间*2,3600)');
    expect(buildQuickGiftFormula({ operation: 'halve', amount: 0, maximum: 3600 }, '加班时间')).toBe('MAX(FLOOR(加班时间/2),0)');
  });
});
