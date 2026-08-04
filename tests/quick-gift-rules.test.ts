import { describe, expect, it } from 'vitest';
import {
  buildQuickGiftFormula,
  detectQuickGiftRule,
  quickGiftOperationLabel,
  quickGiftOperationSupportsMaximum,
  quickGiftOperationUnit,
  quickGiftOperationUsesAmount,
} from '../src/ui/config/quick-gift-rules';

describe('quick gift rule presets', () => {
  it.each([
    ['add', 60, '加班时间+60'],
    ['subtract', 60, 'MAX(加班时间-60,0)'],
    ['price', 60, '加班时间+price/1000*60'],
    ['priceSubtract', 60, 'MAX(加班时间-price/1000*60,0)'],
    ['set', 120, '120'],
    ['reset', 999, '0'],
    ['random', 60, '加班时间+RANDBETWEEN(1,60)'],
    ['randomSubtract', 60, 'MAX(加班时间-RANDBETWEEN(1,60),0)'],
    ['advanced', 60, null],
  ] as const)('builds %s as a backend formula', (operation, amount, formula) => {
    expect(buildQuickGiftFormula(operation, '加班时间', amount)).toBe(formula);
  });

  it.each([
    ['加班时间+60', 'add', 60],
    ['MAX(加班时间-60,0)', 'subtract', 60],
    ['加班时间+price/1000*60', 'price', 60],
    ['MAX(加班时间-price/1000*60,0)', 'priceSubtract', 60],
    ['120', 'set', 120],
    ['0', 'reset', 0],
    ['加班时间+RANDBETWEEN(1,60)', 'random', 60],
    ['MAX(加班时间-RANDBETWEEN(1,60),0)', 'randomSubtract', 60],
    ['IF(加班时间>0,加班时间-1,0)', 'advanced', 60],
  ] as const)('detects %s without changing its meaning', (formula, operation, amount) => {
    expect(detectQuickGiftRule(formula, '加班时间')).toEqual({ operation, amount });
  });

  it('provides beginner-facing copy and field behavior', () => {
    expect(quickGiftOperationLabel('subtract', '积分')).toBe('让“积分”减少（最低为 0）');
    expect(quickGiftOperationUnit('price', true)).toBe('秒');
    expect(quickGiftOperationUsesAmount('reset')).toBe(false);
    expect(quickGiftOperationUsesAmount('advanced')).toBe(false);
    expect(quickGiftOperationSupportsMaximum('add')).toBe(true);
    expect(quickGiftOperationSupportsMaximum('subtract')).toBe(false);
  });

  it('wraps increasing rules with an optional upper limit', () => {
    const formula = buildQuickGiftFormula('add', '加班时间', 60, 3600);
    expect(formula).toBe('MIN(加班时间+60,3600)');
    expect(detectQuickGiftRule(formula!, '加班时间')).toEqual({
      operation: 'add',
      amount: 60,
      maximum: 3600,
    });
  });
});
