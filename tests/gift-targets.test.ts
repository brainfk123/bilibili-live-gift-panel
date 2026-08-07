import { describe, expect, it } from 'vitest';
import {
  applyGiftTargetProgressSnapshot,
  giftTargetPanelConfig,
  giftTargetProgressSignature,
  mergeGiftTargetPanelConfigs,
} from '../src/gift-targets';
import { defaultState } from '../src/storage';

function panel() {
  const state = defaultState();
  return {
    id: 'target-1', name: '本场目标', layout: 'grid' as const,
    items: [{ giftId: 1, giftName: '小花花', imageUrl: 'gift.png', target: 10, received: 7, barStyle: 'progress' as const }],
    appearance: { ...state.blindBoxDisplay },
  };
}

describe('gift target ownership', () => {
  it('projects configuration without backend-owned received counts', () => {
    expect(giftTargetPanelConfig(panel()).items[0]).not.toHaveProperty('received');
  });

  it('preserves progress by panel and gift identity while editing configuration', () => {
    const current = panel();
    const configured = giftTargetPanelConfig(current);
    configured.name = '更新后的目标';
    configured.items[0].target = 30;

    const merged = mergeGiftTargetPanelConfigs([current], [configured]);

    expect(merged[0].name).toBe('更新后的目标');
    expect(merged[0].items[0]).toEqual(expect.objectContaining({ target: 30, received: 7 }));
  });

  it('applies command responses without treating target definitions as changed', () => {
    const current = panel();
    const configBefore = JSON.stringify(giftTargetPanelConfig(current));
    const progressBefore = giftTargetProgressSignature([current]);

    applyGiftTargetProgressSnapshot(current, { panelId: current.id, items: [{ giftId: 1, received: 0 }] });

    expect(JSON.stringify(giftTargetPanelConfig(current))).toBe(configBefore);
    expect(giftTargetProgressSignature([current])).not.toBe(progressBefore);
    expect(current.items[0].received).toBe(0);
  });
});
