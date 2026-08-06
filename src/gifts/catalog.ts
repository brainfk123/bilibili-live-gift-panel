import catalog from '../data/gift-catalog.json';
import { GiftEvent } from '../bilibili/messages';
import { AppState, GiftInfo, RecentGift } from '../types';
import { specialEventCatalog } from './special-events';

export const builtinCatalog: GiftInfo[] = [...specialEventCatalog, ...(catalog as GiftInfo[])];

type GiftIdentity = Pick<GiftInfo, 'name' | 'price' | 'coinType' | 'imgBasic'>;

export function loadBuiltinCatalog(): GiftInfo[] {
  return builtinCatalog;
}

export function findGift(state: AppState, giftId: number): GiftInfo | undefined {
  const inCatalog = builtinCatalog.find((g) => g.id === giftId);
  if (inCatalog) return inCatalog;
  const configured = state.giftCatalog.find((g) => g.id === giftId);
  if (configured) return configured;
  return state.recentGifts.find((g) => g.id === giftId);
}

export function giftDisplayKey(gift: GiftIdentity): string {
  return [gift.name.trim(), gift.price, gift.coinType].join('\u0000');
}

export function sameGiftIdentity(left: GiftIdentity, right: GiftIdentity): boolean {
  if (left.name.trim() !== right.name.trim()) return false;
  return left.price === right.price && left.coinType === right.coinType;
}

export function matchesGiftSearch(gift: Pick<GiftInfo, 'id' | 'name'>, rawQuery: string): boolean {
  const query = rawQuery.trim().toLowerCase();
  if (!query) return true;
  if (gift.name.toLowerCase().includes(query)) return true;
  return /^\d+$/.test(query) && String(gift.id) === query;
}

export function sortGiftsByUsage(
  gifts: GiftInfo[],
  configuredGifts: GiftInfo[],
  recentGifts: RecentGift[],
): GiftInfo[] {
  const configuredKeys = new Set(configuredGifts.map(giftDisplayKey));
  const recentUsage = new Map<string, { count: number; lastReceived: number }>();
  for (const gift of recentGifts) {
    const key = giftDisplayKey(gift);
    const usage = recentUsage.get(key);
    if (!usage) {
      recentUsage.set(key, { count: gift.count, lastReceived: gift.lastReceived });
      continue;
    }
    usage.count += gift.count;
    usage.lastReceived = Math.max(usage.lastReceived, gift.lastReceived);
  }

  return gifts
    .map((gift, index) => ({ gift, index, usage: recentUsage.get(giftDisplayKey(gift)) }))
    .sort((left, right) => {
      const configuredDifference = Number(configuredKeys.has(giftDisplayKey(right.gift)))
        - Number(configuredKeys.has(giftDisplayKey(left.gift)));
      if (configuredDifference !== 0) return configuredDifference;
      const countDifference = (right.usage?.count ?? 0) - (left.usage?.count ?? 0);
      if (countDifference !== 0) return countDifference;
      const recencyDifference = (right.usage?.lastReceived ?? 0) - (left.usage?.lastReceived ?? 0);
      if (recencyDifference !== 0) return recencyDifference;
      return left.index - right.index;
    })
    .map(({ gift }) => gift);
}

export function upsertRecentGift(state: AppState, gift: GiftEvent): void {
  const existing = state.recentGifts.find((g) => g.id === gift.giftId);
  if (existing) {
    existing.count += gift.num;
    existing.lastReceived = gift.timestamp;
  } else {
    const recent: RecentGift = {
      id: gift.giftId,
      name: gift.giftName,
      price: gift.price,
      coinType: gift.coinType,
      imgBasic: gift.imgBasic,
      lastReceived: gift.timestamp,
      count: gift.num,
    };
    state.recentGifts.unshift(recent);
    state.recentGifts = state.recentGifts.slice(0, 100);
  }
}
