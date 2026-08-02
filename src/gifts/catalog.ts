import catalog from '../data/gift-catalog.json';
import { GiftEvent } from '../bilibili/messages';
import { AppState, GiftInfo, RecentGift } from '../types';

export const builtinCatalog: GiftInfo[] = catalog as GiftInfo[];

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
  return [gift.name.trim(), gift.price, gift.coinType, gift.imgBasic.trim()].join('\u0000');
}

export function sameGiftIdentity(left: GiftIdentity, right: GiftIdentity): boolean {
  if (left.name.trim() !== right.name.trim()) return false;
  if (left.price !== right.price || left.coinType !== right.coinType) return false;
  return !left.imgBasic || !right.imgBasic || left.imgBasic === right.imgBasic;
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
