import type { Attribute, DisplayThemeId, DisplayVariant, Settings } from './types';

export interface DisplayThemeDefinition {
  id: DisplayThemeId;
  name: string;
  description: string;
  recommendedFor: string;
  previewClass: string;
  accent: string;
  surface: string;
}

export const DISPLAY_THEMES: readonly DisplayThemeDefinition[] = [
  {
    id: 'minimal',
    name: '极简透明',
    description: '弱化背景和装饰，让游戏画面保持清晰。',
    recommendedFor: '计数器、倒计时',
    previewClass: 'is-minimal',
    accent: '#f8fafc',
    surface: '#111318',
  },
  {
    id: 'glass',
    name: '玻璃卡片',
    description: '半透明磨砂和柔和高光，适合大多数直播画面。',
    recommendedFor: '加班机、目标进度',
    previewClass: 'is-glass',
    accent: '#fb7299',
    surface: '#15171f',
  },
  {
    id: 'rpg',
    name: 'RPG Boss',
    description: '暗色金属、金色描边和厚重血条。',
    recommendedFor: 'Boss 挑战',
    previewClass: 'is-rpg',
    accent: '#f5c86b',
    surface: '#211913',
  },
  {
    id: 'pixel',
    name: '像素街机',
    description: '硬边框、分段进度和街机数字效果。',
    recommendedFor: '挑战次数、复活次数',
    previewClass: 'is-pixel',
    accent: '#ffe45e',
    surface: '#17152c',
  },
  {
    id: 'neon',
    name: '霓虹科技',
    description: '深色底、双色发光和扫描线氛围。',
    recommendedFor: '资源条、双向拉扯',
    previewClass: 'is-neon',
    accent: '#63f3ff',
    surface: '#090d1b',
  },
  {
    id: 'kawaii',
    name: '软萌应援',
    description: '明亮柔和的卡片与轻量弹性动画。',
    recommendedFor: '礼物目标、应援进度',
    previewClass: 'is-kawaii',
    accent: '#ff6fae',
    surface: '#fff2f7',
  },
] as const;

const themeIds = new Set<DisplayThemeId>(DISPLAY_THEMES.map((theme) => theme.id));

export function normalizeDisplayThemeId(value: unknown): DisplayThemeId {
  return typeof value === 'string' && themeIds.has(value as DisplayThemeId)
    ? value as DisplayThemeId
    : 'glass';
}

export function getDisplayTheme(id: unknown): DisplayThemeDefinition {
  const normalized = normalizeDisplayThemeId(id);
  return DISPLAY_THEMES.find((theme) => theme.id === normalized) ?? DISPLAY_THEMES[1];
}

export function resolveAttributeDisplayTheme(
  attribute: Pick<Attribute, 'display'>,
  settings: Pick<Settings, 'defaultDisplayThemeId'>,
): DisplayThemeId {
  return normalizeDisplayThemeId(attribute.display?.themeId ?? settings.defaultDisplayThemeId);
}

export function resolveAttributeDisplayVariant(attribute: Pick<Attribute, 'display' | 'format'>): DisplayVariant {
  return attribute.display?.variant ?? (attribute.format === 'hhmmss' ? 'timer' : 'number');
}

