import { describe, expect, it } from 'vitest';
import {
  blindBoxDisplayUrl,
  buildObsOutputCatalog,
  displaySceneUrl,
  giftKpiDisplayUrl,
  obsOutputCount,
  obsOutputUrl,
  parseObsOutputTarget,
} from '../src/obs-outputs';
import { defaultState } from '../src/storage';

describe('OBS output catalog', () => {
  it('owns parsing and encoded URL generation for every output kind', () => {
    expect(obsOutputUrl('http://localhost:12450')).toBe('http://localhost:12450/?mode=display');
    expect(obsOutputUrl('http://localhost:12450', { kind: 'attribute', attributeName: '加班 时间' }))
      .toBe('http://localhost:12450/?mode=display&attribute=%E5%8A%A0%E7%8F%AD%20%E6%97%B6%E9%97%B4');
    expect(displaySceneUrl('http://localhost:12450', 'scene 1')).toBe('http://localhost:12450/?mode=display&scene=scene%201');
    expect(blindBoxDisplayUrl('http://localhost:12450', 35800)).toBe('http://localhost:12450/?mode=display&view=blind-box&blindBox=35800');
    expect(giftKpiDisplayUrl('http://localhost:12450', 'target 1')).toBe('http://localhost:12450/?mode=display&view=gift-kpi&panel=target%201');
    expect(parseObsOutputTarget('?mode=display&view=blind-box&blindBox=35800')).toEqual({ kind: 'blind-box', blindBoxGiftId: 35800 });
    expect(parseObsOutputTarget('?mode=display&view=gift-kpi&panel=target-1')).toEqual({ kind: 'gift-target', panelId: 'target-1' });
    expect(parseObsOutputTarget('?mode=display&scene=scene-1')).toEqual({ kind: 'scene', sceneId: 'scene-1' });
    expect(parseObsOutputTarget('?mode=display&attribute=生命值')).toEqual({ kind: 'attribute', attributeName: '生命值' });
  });

  it('lists every configured output, including combination scenes', () => {
    const state = defaultState();
    state.attributes = [{ name: '生命值', value: 80, unit: 'none', format: 'number', decimals: 0, suffix: '' }];
    state.displayScenes = [{ id: 'scene-1', name: 'Boss 面板', attributeNames: ['生命值'], layout: 'focus', themeId: 'rpg' }];
    state.giftKpiPanels = [{
      id: 'target-1', name: '本场目标', layout: 'grid',
      items: [{ giftId: 1, giftName: '小花花', imageUrl: 'gift.png', target: 10, received: 3, barStyle: 'progress' }],
      appearance: { ...state.blindBoxDisplay },
    }];

    const catalog = buildObsOutputCatalog(state, { blindBoxLoginEnabled: false });

    expect(catalog.map((group) => group.id)).toEqual(['attributes', 'scenes', 'gift-targets', 'leaderboards']);
    expect(obsOutputCount(catalog)).toBe(4);
    expect(catalog.find((group) => group.id === 'scenes')?.items[0]).toEqual(expect.objectContaining({
      title: 'Boss 面板', target: { kind: 'scene', sceneId: 'scene-1' },
    }));
    expect(catalog.find((group) => group.id === 'gift-targets')?.items[0].imageUrl).toBe('gift.png');
    expect(catalog.find((group) => group.id === 'leaderboards')?.items[0].meta).toContain('依赖登录');
  });
});
