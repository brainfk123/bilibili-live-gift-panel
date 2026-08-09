import type { GiftInfo } from '../types';
import { formatYuanFromGoldSeeds } from '../currency';

export const SPECIAL_EVENT_GIFT_IDS = {
  guardCaptain: 1_900_000_001,
  guardAdmiral: 1_900_000_002,
  guardGovernor: 1_900_000_003,
  superChat: 1_900_000_004,
} as const;

function eventIcon(label: string, color: string): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop stop-color="${color}"/><stop offset="1" stop-color="#1b1830"/></linearGradient></defs><rect width="96" height="96" rx="24" fill="url(#g)"/><rect x="7" y="7" width="82" height="82" rx="20" fill="none" stroke="#fff" stroke-opacity=".28" stroke-width="2"/><text x="48" y="59" text-anchor="middle" font-family="system-ui,sans-serif" font-size="34" font-weight="800" fill="#fff">${label}</text></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

export const specialEventCatalog: GiftInfo[] = [
  {
    id: SPECIAL_EVENT_GIFT_IDS.guardCaptain,
    name: '大航海·舰长',
    price: 198_000,
    coinType: 'gold',
    imgBasic: eventIcon('舰', '#3e9cff'),
    specialEvent: 'guard-captain',
  },
  {
    id: SPECIAL_EVENT_GIFT_IDS.guardAdmiral,
    name: '大航海·提督',
    price: 1_998_000,
    coinType: 'gold',
    imgBasic: eventIcon('提', '#9f6cff'),
    specialEvent: 'guard-admiral',
  },
  {
    id: SPECIAL_EVENT_GIFT_IDS.guardGovernor,
    name: '大航海·总督',
    price: 19_998_000,
    coinType: 'gold',
    imgBasic: eventIcon('总', '#ff9d38'),
    specialEvent: 'guard-governor',
  },
  {
    id: SPECIAL_EVENT_GIFT_IDS.superChat,
    name: 'Super Chat',
    price: 30_000,
    coinType: 'gold',
    imgBasic: eventIcon('SC', '#ff5f91'),
    specialEvent: 'super-chat',
  },
];

export function isSpecialEventGift(gift: Pick<GiftInfo, 'specialEvent'>): boolean {
  return gift.specialEvent !== undefined;
}

export function giftPriceDescription(gift: GiftInfo): string {
  if (gift.specialEvent) return '按实际支付金额';
  return gift.coinType === 'gold'
    ? formatYuanFromGoldSeeds(gift.price)
    : `${gift.price} 银瓜子`;
}
