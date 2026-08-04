import { describe, expect, it } from 'vitest';
import {
  DISPLAY_THEMES,
  normalizeDisplayThemeId,
  resolveAttributeDisplayTheme,
  resolveAttributeDisplayVariant,
} from '../src/display-themes';
import { defaultState } from '../src/storage';

describe('display themes', () => {
  it('registers six stable theme IDs', () => {
    expect(DISPLAY_THEMES.map((theme) => theme.id)).toEqual([
      'minimal', 'glass', 'rpg', 'pixel', 'neon', 'kawaii',
    ]);
  });

  it('falls back to glass for unknown persisted values', () => {
    expect(normalizeDisplayThemeId('removed-theme')).toBe('glass');
  });

  it('uses an attribute theme before the global default', () => {
    const settings = defaultState().settings;
    settings.defaultDisplayThemeId = 'minimal';
    expect(resolveAttributeDisplayTheme({ display: { variant: 'health', themeId: 'rpg' } }, settings)).toBe('rpg');
    expect(resolveAttributeDisplayTheme({ display: undefined }, settings)).toBe('minimal');
  });

  it('derives a compatible variant for old attributes', () => {
    expect(resolveAttributeDisplayVariant({ format: 'hhmmss' })).toBe('timer');
    expect(resolveAttributeDisplayVariant({ format: 'number' })).toBe('number');
  });
});

