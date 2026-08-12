import type { GiftReceipt } from '../../types';

export function giftClipAnimationKey(receipt: Pick<GiftReceipt, 'giftId' | 'animation'>): string {
  const effectId = Number(receipt.animation?.effectId);
  if (Number.isInteger(effectId) && effectId > 0) return `effect:${effectId}`;
  const source = receipt.animation?.gif?.trim() || receipt.animation?.webp?.trim();
  if (!source) return `gift:${Math.trunc(Number(receipt.giftId) || 0)}`;
  let stableSource = source.split(/[?#]/, 1)[0];
  try {
    const url = new URL(source, 'https://local.invalid');
    stableSource = `${url.hostname.toLowerCase()}${url.pathname}`;
  } catch {
    // The query-free source remains stable for malformed legacy URLs.
  }
  let hash = 0x811c9dc5;
  for (let index = 0; index < stableSource.length; index += 1) {
    hash ^= stableSource.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `media:${(hash >>> 0).toString(16).padStart(8, '0')}`;
}
