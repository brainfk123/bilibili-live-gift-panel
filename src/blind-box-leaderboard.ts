import type { ContributionLedger, ViewerContribution } from './types';

export interface BlindBoxLeaderboardSummary {
  viewerCount: number;
  blindBoxCount: number;
  cost: number;
  value: number;
  profit: number;
  unpricedCount: number;
}

export interface BlindBoxLeaderboard {
  summary: BlindBoxLeaderboardSummary;
  viewers: ViewerContribution[];
}

/**
 * Builds the canonical blind-box leaderboard from the backend-owned contribution ledger.
 * Positive profit means the viewer opened more gift value than the blind boxes cost.
 */
export function buildBlindBoxLeaderboard(
  ledger: ContributionLedger,
  limit = Number.POSITIVE_INFINITY,
): BlindBoxLeaderboard {
  const eligible = ledger.viewers.filter((viewer) => finite(viewer.blindBoxCount) > 0);
  const viewers = [...eligible].sort((left, right) => (
    finite(right.blindBoxProfit) - finite(left.blindBoxProfit)
    || finite(right.blindBoxValue) - finite(left.blindBoxValue)
    || finite(right.giftCount) - finite(left.giftCount)
    || finite(right.lastGiftAt) - finite(left.lastGiftAt)
  ));
  const safeLimit = Number.isFinite(limit) ? Math.max(0, Math.floor(limit)) : viewers.length;
  const summary = eligible.reduce<BlindBoxLeaderboardSummary>((result, viewer) => ({
    viewerCount: result.viewerCount + 1,
    blindBoxCount: result.blindBoxCount + finite(viewer.blindBoxCount),
    cost: result.cost + finite(viewer.blindBoxCost),
    value: result.value + finite(viewer.blindBoxValue),
    profit: result.profit + finite(viewer.blindBoxProfit),
    unpricedCount: result.unpricedCount + finite(viewer.unpricedBlindBoxCount),
  }), {
    viewerCount: 0,
    blindBoxCount: 0,
    cost: 0,
    value: 0,
    profit: 0,
    unpricedCount: 0,
  });
  return { summary, viewers: viewers.slice(0, safeLimit) };
}

function finite(value: number | undefined): number {
  const normalized = Number(value ?? 0);
  return Number.isFinite(normalized) ? normalized : 0;
}
