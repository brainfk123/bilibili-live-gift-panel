import { describe, expect, it } from 'vitest';
import { buildBlindBoxLeaderboard, listBlindBoxLeaderboardScopes } from '../src/blind-box-leaderboard';
import type { ContributionLedger } from '../src/types';

const ledger: ContributionLedger = {
  viewers: [
    {
      key: 'uid:1', uid: 1, uname: '盈利观众', giftCount: 4, goldValue: 20_000, silverValue: 0,
      ruleTriggers: 0, attributeDeltas: {}, blindBoxCount: 2, blindBoxCost: 18_000,
      blindBoxValue: 25_000, blindBoxProfit: 7_000, lastGiftAt: 300,
      blindBoxes: [
        { giftId: 35800, giftName: '小熊虫盲盒', count: 1, cost: 9_000, value: 12_000, profit: 3_000, lastGiftAt: 300 },
        { giftId: 35900, giftName: '星愿盲盒', count: 1, cost: 9_000, value: 13_000, profit: 4_000, lastGiftAt: 290 },
      ],
    },
    {
      key: 'uid:2', uid: 2, uname: '亏损观众', giftCount: 3, goldValue: 10_000, silverValue: 0,
      ruleTriggers: 0, attributeDeltas: {}, blindBoxCount: 1, blindBoxCost: 9_000,
      blindBoxValue: 4_000, blindBoxProfit: -5_000, unpricedBlindBoxCount: 1, lastGiftAt: 200,
      blindBoxes: [
        { giftId: 35800, giftName: '小熊虫盲盒', count: 1, cost: 9_000, value: 4_000, profit: -5_000, unpricedCount: 1, lastGiftAt: 200 },
      ],
    },
    {
      key: 'uid:3', uid: 3, uname: '普通观众', giftCount: 9, goldValue: 90_000, silverValue: 0,
      ruleTriggers: 0, attributeDeltas: {}, blindBoxCount: 0, blindBoxCost: 0,
      blindBoxValue: 0, blindBoxProfit: 0, lastGiftAt: 400,
    },
  ],
};

describe('blind-box leaderboard', () => {
  it('summarizes backend-owned blind-box values and excludes viewers without boxes', () => {
    const result = buildBlindBoxLeaderboard(ledger);

    expect(result.summary).toEqual({
      viewerCount: 2,
      blindBoxCount: 3,
      cost: 27_000,
      value: 29_000,
      profit: 2_000,
      unpricedCount: 1,
    });
    expect(result.viewers.map((viewer) => viewer.uname)).toEqual(['盈利观众', '亏损观众']);
  });

  it('limits only the displayed rows without changing the full summary', () => {
    const result = buildBlindBoxLeaderboard(ledger, 1);

    expect(result.viewers).toHaveLength(1);
    expect(result.summary.viewerCount).toBe(2);
    expect(result.summary.profit).toBe(2_000);
  });

  it('builds an independent leaderboard for one blind-box type', () => {
    const result = buildBlindBoxLeaderboard(ledger, Number.POSITIVE_INFINITY, 35800);

    expect(result.summary).toEqual({
      viewerCount: 2,
      blindBoxCount: 2,
      cost: 18_000,
      value: 16_000,
      profit: -2_000,
      unpricedCount: 1,
    });
    expect(result.viewers.map((viewer) => [viewer.uname, viewer.blindBoxProfit]))
      .toEqual([['盈利观众', 3_000], ['亏损观众', -5_000]]);
  });

  it('lists available blind-box scopes by total opened count', () => {
    expect(listBlindBoxLeaderboardScopes(ledger)).toEqual([
      { giftId: 35800, giftName: '小熊虫盲盒', count: 2, lastGiftAt: 300 },
      { giftId: 35900, giftName: '星愿盲盒', count: 1, lastGiftAt: 290 },
    ]);
  });
});
