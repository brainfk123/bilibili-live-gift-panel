import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { DISPLAY_THEMES } from '../src/display-themes';
import {
  BLIND_BOX_VIEWER_SLOTS_RANGE,
  DISPLAY_FONT_SIZE_RANGE,
  DISPLAY_PANEL_OPACITY_RANGE,
  DISPLAY_SCENE_LAYOUT_IDS,
  GIFT_TARGET_LAYOUT_IDS,
  normalizeBlindBoxDisplayAppearance,
  normalizeDisplayAppearance,
  normalizeDisplaySceneLayout,
  normalizeGiftTargetLayout,
  OUTPUT_CONFIG_SCHEMA_VERSION,
} from '../src/output-config';

const contract = JSON.parse(readFileSync(
  new URL('../testdata/output-config-contract.json', import.meta.url),
  'utf8',
)) as {
  schemaVersion: number;
  displaySceneLayouts: string[];
  giftTargetLayouts: string[];
  displayThemeIds: string[];
  appearance: {
    fontSize: { min: number; max: number; default: number };
    panelOpacity: { min: number; max: number; default: number };
  };
  blindBoxViewerSlots: { min: number; max: number; default: number };
};

const fallback = {
  fontSize: 54,
  accentColor: '#7a4ffb',
  showConnection: true,
  align: 'center' as const,
  panelOpacity: 55,
  defaultDisplayThemeId: 'glass' as const,
};

describe('output configuration contract', () => {
  it('matches the shared TypeScript and Go contract fixture', () => {
    expect(OUTPUT_CONFIG_SCHEMA_VERSION).toBe(contract.schemaVersion);
    expect(DISPLAY_SCENE_LAYOUT_IDS).toEqual(contract.displaySceneLayouts);
    expect(GIFT_TARGET_LAYOUT_IDS).toEqual(contract.giftTargetLayouts);
    expect(DISPLAY_THEMES.map((theme) => theme.id)).toEqual(contract.displayThemeIds);
    expect(DISPLAY_FONT_SIZE_RANGE).toEqual(contract.appearance.fontSize);
    expect(DISPLAY_PANEL_OPACITY_RANGE).toEqual(contract.appearance.panelOpacity);
    expect(BLIND_BOX_VIEWER_SLOTS_RANGE).toEqual(contract.blindBoxViewerSlots);
  });

  it('normalizes layouts and appearances through one interface', () => {
    expect(normalizeDisplaySceneLayout('versus')).toBe('versus');
    expect(normalizeDisplaySceneLayout('unknown')).toBe('stack');
    expect(normalizeGiftTargetLayout('dashboard')).toBe('dashboard');
    expect(normalizeGiftTargetLayout('unknown')).toBe('grid');
    expect(normalizeDisplayAppearance({
      themeId: 'neon', fontSize: 999, accentColor: 'bad', showConnection: false, align: 'right', panelOpacity: 0,
    }, fallback)).toEqual({
      themeId: 'neon', fontSize: 96, accentColor: '#fb7299', showConnection: false, align: 'right', panelOpacity: 10,
    });
    expect(normalizeBlindBoxDisplayAppearance({ viewerSlots: 99 }, fallback)).toEqual(expect.objectContaining({
      viewerSlots: 10,
      themeId: 'glass',
      fontSize: 54,
    }));
  });
});
