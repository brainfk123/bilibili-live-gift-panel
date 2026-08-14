import { describe, expect, it } from 'vitest';
import {
  buildQuickGiftCondition,
  detectQuickGiftCondition,
  GIFT_USER_IDENTITIES,
  isGiftFormulaSystemName,
} from '../src/gift-rule-conditions';

describe('gift rule conditions', () => {
  it('builds the supported quick condition forms', () => {
    expect(buildQuickGiftCondition('any', 2)).toBe('');
    expect(buildQuickGiftCondition('equal', 0)).toBe('用户身份=普通用户');
    expect(buildQuickGiftCondition('equal', 1)).toBe('用户身份=粉丝团');
    expect(buildQuickGiftCondition('atLeast', 0)).toBe('用户身份>=普通用户');
    expect(buildQuickGiftCondition('atLeast', 2)).toBe('用户身份>=舰长');
    expect(buildQuickGiftCondition('advanced', 2)).toBeNull();
  });

  it('detects only trimmed standard condition forms', () => {
    expect(detectQuickGiftCondition('')).toEqual({ mode: 'any', identity: 2 });
    expect(detectQuickGiftCondition(' \t\n ')).toEqual({ mode: 'any', identity: 2 });
    expect(detectQuickGiftCondition(' 用户身份=普通用户 ')).toEqual({ mode: 'equal', identity: 0 });
    expect(detectQuickGiftCondition(' 用户身份=粉丝团 ')).toEqual({ mode: 'equal', identity: 1 });
    expect(detectQuickGiftCondition('\t用户身份>=普通用户\n')).toEqual({ mode: 'atLeast', identity: 0 });
    expect(detectQuickGiftCondition('\t用户身份>=舰长\n')).toEqual({ mode: 'atLeast', identity: 2 });
  });

  it.each([
    '舰长<=用户身份',
    '积分>=用户身份',
    '用户身份==舰长',
    '用户身份>=舰长&&积分>0',
  ])('keeps nonstandard conditions in advanced mode: %s', (condition) => {
    expect(detectQuickGiftCondition(condition)).toEqual({ mode: 'advanced', identity: 2 });
  });

  it('matches backend identity order and system formula names', () => {
    expect(GIFT_USER_IDENTITIES).toEqual([
      { value: 0, name: '普通用户' },
      { value: 1, name: '粉丝团' },
      { value: 2, name: '舰长' },
      { value: 3, name: '提督' },
      { value: 4, name: '总督' },
    ]);
    for (const name of ['用户身份', '普通用户', '粉丝团', '舰长', '提督', '总督']) {
      expect(isGiftFormulaSystemName(name)).toBe(true);
    }
    expect(isGiftFormulaSystemName('用户身份等级')).toBe(false);
    expect(isGiftFormulaSystemName('积分')).toBe(false);
  });
});
