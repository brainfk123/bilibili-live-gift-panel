import { describe, expect, it } from 'vitest';
import {
  defaultGiftClipCrop,
  giftClipCropFromPixels,
  giftClipCropToPixels,
  isGiftClipSourceSizeSupported,
  normalizeGiftClipCrop,
  updateGiftClipCrop,
  type GiftClipCropHandle,
} from '../src/ui/config/gift-clip-crop';

describe('gift clip crop geometry', () => {
  const initial = giftClipCropFromPixels({ x: 100, y: 75, width: 200, height: 150 }, 400, 300);
  const expected: Record<GiftClipCropHandle, { x: number; y: number; width: number; height: number }> = {
    move: { x: 140, y: 105, width: 200, height: 150 },
    n: { x: 100, y: 105, width: 200, height: 120 },
    ne: { x: 100, y: 105, width: 240, height: 120 },
    e: { x: 100, y: 75, width: 240, height: 150 },
    se: { x: 100, y: 75, width: 240, height: 180 },
    s: { x: 100, y: 75, width: 200, height: 180 },
    sw: { x: 140, y: 75, width: 160, height: 180 },
    w: { x: 140, y: 75, width: 160, height: 150 },
    nw: { x: 140, y: 105, width: 160, height: 120 },
  };

  it.each(Object.keys(expected) as GiftClipCropHandle[])('updates %s without changing unrelated edges', (handle) => {
    expect(giftClipCropToPixels(updateGiftClipCrop(initial, handle, 40, 30, 400, 300), 400, 300))
      .toEqual(expected[handle]);
  });

  it('defaults damaged values to the full source and clamps valid values inside it', () => {
    expect(defaultGiftClipCrop()).toEqual({ x: 0, y: 0, width: 1, height: 1 });
    expect(normalizeGiftClipCrop({ x: Number.NaN, y: 0, width: 1, height: 1 })).toEqual(defaultGiftClipCrop());
    expect(giftClipCropToPixels({ x: .9, y: .9, width: .5, height: .5 }, 400, 300))
      .toEqual({ x: 200, y: 150, width: 200, height: 150 });
  });

  it('keeps move and resize operations in bounds with a 64px minimum', () => {
    const tiny = giftClipCropFromPixels({ x: 100, y: 100, width: 80, height: 80 }, 400, 300);
    expect(giftClipCropToPixels(updateGiftClipCrop(tiny, 'se', -999, -999, 400, 300), 400, 300))
      .toEqual({ x: 100, y: 100, width: 64, height: 64 });
    expect(giftClipCropToPixels(updateGiftClipCrop(tiny, 'move', 999, 999, 400, 300), 400, 300))
      .toEqual({ x: 320, y: 220, width: 80, height: 80 });
  });

  it('rejects a source when either original dimension is under 64px', () => {
    expect(isGiftClipSourceSizeSupported(64, 64)).toBe(true);
    expect(isGiftClipSourceSizeSupported(63, 640)).toBe(false);
    expect(isGiftClipSourceSizeSupported(640, 63)).toBe(false);
  });
});
