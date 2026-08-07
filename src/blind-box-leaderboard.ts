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

export interface BlindBoxLeaderboardScope {
  giftId: number;
  giftName: string;
  count: number;
  lastGiftAt: number;
}

/**
 * Builds the canonical blind-box leaderboard from the backend-owned contribution ledger.
 * Positive profit means the viewer opened more gift value than the blind boxes cost.
 */
export function buildBlindBoxLeaderboard(
  ledger: ContributionLedger,
  limit = Number.POSITIVE_INFINITY,
  blindBoxGiftId?: number,
): BlindBoxLeaderboard {
  const eligible = ledger.viewers.flatMap((viewer) => {
    if (!blindBoxGiftId) return finite(viewer.blindBoxCount) > 0 ? [viewer] : [];
    const breakdown = viewer.blindBoxes?.find((candidate) => candidate.giftId === blindBoxGiftId);
    if (!breakdown || finite(breakdown.count) <= 0) return [];
    return [{
      ...viewer,
      blindBoxCount: finite(breakdown.count),
      blindBoxCost: finite(breakdown.cost),
      blindBoxValue: finite(breakdown.value),
      blindBoxProfit: finite(breakdown.value) - finite(breakdown.cost),
      unpricedBlindBoxCount: finite(breakdown.unpricedCount),
      lastGiftAt: finite(breakdown.lastGiftAt),
    }];
  });
  const viewers = [...eligible].sort((left, right) => (
    finite(right.blindBoxProfit) - finite(left.blindBoxProfit)
    || finite(right.blindBoxValue) - finite(left.blindBoxValue)
    || finite(right.blindBoxCount) - finite(left.blindBoxCount)
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

export function listBlindBoxLeaderboardScopes(ledger: ContributionLedger): BlindBoxLeaderboardScope[] {
  const scopes = new Map<number, BlindBoxLeaderboardScope>();
  for (const viewer of ledger.viewers) {
    for (const breakdown of viewer.blindBoxes ?? []) {
      const giftId = Math.trunc(finite(breakdown.giftId));
      const count = finite(breakdown.count);
      if (giftId <= 0 || count <= 0) continue;
      const giftName = String(breakdown.giftName ?? '').trim() || `盲盒 ${giftId}`;
      const lastGiftAt = finite(breakdown.lastGiftAt);
      const current = scopes.get(giftId);
      if (current) {
        current.count += count;
        if (lastGiftAt >= current.lastGiftAt) {
          current.giftName = giftName;
          current.lastGiftAt = lastGiftAt;
        }
      } else {
        scopes.set(giftId, { giftId, giftName, count, lastGiftAt });
      }
    }
  }
  return Array.from(scopes.values()).sort((left, right) => (
    right.count - left.count
    || right.lastGiftAt - left.lastGiftAt
    || left.giftName.localeCompare(right.giftName, 'zh-CN')
  ));
}

function finite(value: number | undefined): number {
  const normalized = Number(value ?? 0);
  return Number.isFinite(normalized) ? normalized : 0;
}
