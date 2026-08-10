import { describe, expect, it } from 'vitest';
import {
  giftEffectDurationMs,
  giftEffectVisualSize,
  giftGifFrameIndex,
  normalizeGiftClipDuration,
  normalizeGiftEffectLayout,
} from '../src/ui/config/gift-clip-media';

describe('gift clip media', () => {
  const layout = normalizeGiftEffectLayout({
    videoWidth: 1088,
    videoHeight: 1280,
    rgbFrame: [0, 0, 720, 1280],
    alphaFrame: [724, 0, 360, 640],
    fps: 30,
    frames: 390,
  });

  it('uses the RGB composite dimensions without a 480px pre-scale', () => {
    expect(giftEffectVisualSize(layout)).toEqual({ width: 720, height: 1280 });
    expect(giftEffectDurationMs(layout)).toBe(13_000);
  });

  it('selects deterministic GIF frames across loops', () => {
    expect([0, 219, 220, 500, 660].map((time) => giftGifFrameIndex([220, 220, 220], time)))
      .toEqual([0, 0, 1, 2, 0]);
  });

  it('clamps missing and abnormal durations', () => {
    expect([undefined, 200, 2200, 60_000].map(normalizeGiftClipDuration)).toEqual([3000, 1000, 2200, 15_000]);
  });

  it('rejects packed-alpha coordinates outside the video', () => {
    expect(() => normalizeGiftEffectLayout({ ...layout, rgbFrame: [0, 0, 1200, 1280] }))
      .toThrow('礼物特效坐标无效');
  });
});
