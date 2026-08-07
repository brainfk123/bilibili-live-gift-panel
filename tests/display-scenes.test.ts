import { describe, expect, it, vi } from 'vitest';
import { blindBoxDisplayUrl, createDisplaySceneId, DISPLAY_SCENE_LAYOUTS, displaySceneLayoutName, displaySceneUrl, giftKpiDisplayUrl, normalizeDisplayScenes, resolveDisplayTarget } from '../src/display-scenes';
import { defaultState } from '../src/storage';

const attributes = [
  { name: '生命值', value: 100, unit: 'none', format: 'number', decimals: 0, suffix: '' },
  { name: '能量', value: 50, unit: 'none', format: 'number', decimals: 0, suffix: '' },
] as const;

describe('display scene model', () => {
  it('normalizes layout, theme, duplicates, and missing attributes', () => {
    const scenes = normalizeDisplayScenes([{
      id: ' scene-1 ', name: ' Boss 战 ', layout: 'grid', themeId: 'neon',
      attributeNames: ['生命值', '生命值', '不存在', '能量'],
    }], attributes as any, 'glass');

    expect(scenes).toEqual([{
      id: 'scene-1', name: 'Boss 战', layout: 'grid', themeId: 'neon', attributeNames: ['生命值', '能量'],
    }]);
  });

  it('resolves scene attributes in the saved order', () => {
    const state = defaultState();
    state.attributes = attributes.map((attribute) => ({ ...attribute })) as any;
    state.displayScenes = [{ id: 'scene-1', name: '状态总览', layout: 'stack', themeId: 'glass', attributeNames: ['能量', '生命值'] }];

    const resolved = resolveDisplayTarget(state, { sceneId: 'scene-1' });
    expect(resolved.scene?.name).toBe('状态总览');
    expect(resolved.attributes.map((attribute) => attribute.name)).toEqual(['能量', '生命值']);
  });

  it('supports gameplay-specific layouts and falls back safely', () => {
    expect(DISPLAY_SCENE_LAYOUTS.map((layout) => layout.id)).toEqual(['stack', 'grid', 'focus', 'versus', 'dashboard']);
    expect(displaySceneLayoutName('versus')).toBe('双方对抗');
    const scenes = normalizeDisplayScenes([
      { id: 'focus', name: 'Boss', layout: 'focus', themeId: 'rpg', attributeNames: ['生命值', '能量'] },
      { id: 'old', name: '旧布局', layout: 'unknown' as any, themeId: 'glass', attributeNames: ['生命值'] },
    ], attributes as any, 'glass');
    expect(scenes.map((scene) => scene.layout)).toEqual(['focus', 'stack']);
  });

  it('builds encoded links and stable prefixed IDs', () => {
    vi.stubGlobal('crypto', { randomUUID: () => 'scene-uuid' });
    expect(createDisplaySceneId()).toBe('scene-scene-uuid');
    expect(displaySceneUrl('http://localhost:12450', 'scene 1')).toBe('http://localhost:12450/?mode=display&scene=scene%201');
    expect(blindBoxDisplayUrl('http://localhost:12450')).toBe('http://localhost:12450/?mode=display&view=blind-box');
    expect(giftKpiDisplayUrl('http://localhost:12450', 'kpi 1')).toBe('http://localhost:12450/?mode=display&view=gift-kpi&panel=kpi%201');
    vi.unstubAllGlobals();
  });
});
