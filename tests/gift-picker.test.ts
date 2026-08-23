import { describe, expect, it } from 'vitest';
import { filterGiftPickerGifts, type GiftPickerCatalog } from '../src/ui/config/gift-picker';
import type { GiftInfo } from '../src/types';

function blindBoxCatalog(): GiftPickerCatalog {
  const gifts: GiftInfo[] = [
    { id: 35788, name: '锦书传意', price: 19000, coinType: 'gold', imgBasic: '', blindBoxParentId: 35786, blindBoxParentName: '七夕鹊匣' },
    { id: 999001, name: '普通礼物', price: 1000, coinType: 'gold', imgBasic: '' },
    { id: 35786, name: '七夕鹊匣', price: 25000, coinType: 'gold', imgBasic: '' },
    { id: 35787, name: '月下牵丝', price: 5000, coinType: 'gold', imgBasic: '', blindBoxParentId: 35786, blindBoxParentName: '七夕鹊匣' },
  ];
  return {
    gifts,
    availabilityById: new Map(gifts.map((gift) => [gift.id, 'listed' as const])),
    hasLiveListingStatus: true,
  };
}

describe('gift picker blind-box search', () => {
  it('expands a parent ID match into the parent followed by every reward', () => {
    expect(filterGiftPickerGifts(blindBoxCatalog(), '35786').map((gift) => gift.id))
      .toEqual([35786, 35788, 35787]);
  });

  it('does not expand siblings when only a reward matches', () => {
    expect(filterGiftPickerGifts(blindBoxCatalog(), '月下牵丝').map((gift) => gift.id))
      .toEqual([35787]);
  });
});
