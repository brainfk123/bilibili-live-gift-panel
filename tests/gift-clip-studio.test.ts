import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { defaultState } from '../src/storage';
import {
  giftClipAnimationKey,
} from '../src/ui/config/gift-clip-studio';
import { normalizeGiftClipDuration } from '../src/ui/config/gift-clip-media';

describe('gift clip studio', () => {
  it('clamps missing and abnormal animation durations at the media seam', () => {
    expect([undefined, 200, 2200, 60_000].map(normalizeGiftClipDuration))
      .toEqual([3000, 1000, 2200, 15_000]);
  });

  it('keeps a stable crop key for signed versions of the same animation URL', () => {
    expect(giftClipAnimationKey({ giftId: 1, animation: { gif: 'https://i0.hdslb.com/a.gif?token=one', durationMs: 3000 } }))
      .toBe(giftClipAnimationKey({ giftId: 2, animation: { gif: 'https://i0.hdslb.com/a.gif?token=two', durationMs: 5000 } }));
  });

  it('keeps loading copy outside the recorded renderer', () => {
    const source = readFileSync(new URL('../src/ui/config/gift-clip-studio.ts', import.meta.url), 'utf8');
    expect(source).toContain('正在读取礼物动画');
    const renderer = readFileSync(new URL('../src/ui/config/gift-clip-renderer.ts', import.meta.url), 'utf8');
    expect(renderer).not.toContain('正在准备礼物动画');
  });

  it('drops the legacy placement field after the crop cutover', () => {
    const state = defaultState();
    const legacyPlacementSettingsKey = ['giftClip', 'Placements'].join('');
    expect(state.settings.giftClipCrops).toEqual({});
    expect((state.settings as unknown as Record<string, unknown>)[legacyPlacementSettingsKey]).toBeUndefined();
  });
});
