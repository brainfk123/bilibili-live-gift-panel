import { normalizeDisplayThemeId } from './display-themes';
import type {
  AppState,
  BlindBoxDisplayAppearance,
  DisplayAppearance,
  DisplaySceneLayout,
  DisplayThemeId,
  GiftKpiLayout,
} from './types';

export const OUTPUT_CONFIG_SCHEMA_VERSION = 1;
export const DISPLAY_SCENE_LAYOUT_IDS = ['stack', 'grid', 'focus', 'versus', 'dashboard'] as const satisfies readonly DisplaySceneLayout[];
export const GIFT_TARGET_LAYOUT_IDS = ['stack', 'grid', 'dashboard'] as const satisfies readonly GiftKpiLayout[];
export const DISPLAY_FONT_SIZE_RANGE = { min: 24, max: 96, default: 48 } as const;
export const DISPLAY_PANEL_OPACITY_RANGE = { min: 10, max: 100, default: 55 } as const;
export const BLIND_BOX_VIEWER_SLOTS_RANGE = { min: 1, max: 10, default: 3 } as const;
export const DEFAULT_DISPLAY_ACCENT_COLOR = '#fb7299';

export type DisplayAppearanceFallback = Pick<
  AppState['settings'],
  'fontSize' | 'accentColor' | 'showConnection' | 'align' | 'panelOpacity' | 'defaultDisplayThemeId'
>;

const displaySceneLayoutIds = new Set<string>(DISPLAY_SCENE_LAYOUT_IDS);
const giftTargetLayoutIds = new Set<string>(GIFT_TARGET_LAYOUT_IDS);

export function isDisplaySceneLayout(value: unknown): value is DisplaySceneLayout {
  return typeof value === 'string' && displaySceneLayoutIds.has(value);
}

export function normalizeDisplaySceneLayout(value: unknown): DisplaySceneLayout {
  return isDisplaySceneLayout(value) ? value : 'stack';
}

export function normalizeGiftTargetLayout(value: unknown): GiftKpiLayout {
  return typeof value === 'string' && giftTargetLayoutIds.has(value) ? value as GiftKpiLayout : 'grid';
}

export function normalizeDisplayAppearance(
  appearance: Partial<DisplayAppearance> | undefined,
  fallback: DisplayAppearanceFallback,
  fallbackThemeId: unknown = fallback.defaultDisplayThemeId,
): DisplayAppearance {
  const fontSize = finiteNumber(appearance?.fontSize, fallback.fontSize, DISPLAY_FONT_SIZE_RANGE.default);
  const panelOpacity = finiteNumber(appearance?.panelOpacity, fallback.panelOpacity, DISPLAY_PANEL_OPACITY_RANGE.default);
  const accentCandidate = String(appearance?.accentColor ?? fallback.accentColor ?? DEFAULT_DISPLAY_ACCENT_COLOR);
  const fallbackAlign = fallback.align === 'left' || fallback.align === 'right' ? fallback.align : 'center';
  const align = appearance?.align === 'left' || appearance?.align === 'right' || appearance?.align === 'center'
    ? appearance.align
    : fallbackAlign;
  return {
    themeId: normalizeDisplayThemeId(appearance?.themeId ?? fallbackThemeId) as DisplayThemeId,
    fontSize: clamp(fontSize, DISPLAY_FONT_SIZE_RANGE.min, DISPLAY_FONT_SIZE_RANGE.max),
    accentColor: /^#[0-9a-f]{6}$/i.test(accentCandidate) ? accentCandidate : DEFAULT_DISPLAY_ACCENT_COLOR,
    showConnection: appearance?.showConnection ?? fallback.showConnection,
    align,
    panelOpacity: clamp(panelOpacity, DISPLAY_PANEL_OPACITY_RANGE.min, DISPLAY_PANEL_OPACITY_RANGE.max),
  };
}

export function normalizeBlindBoxDisplayAppearance(
  appearance: Partial<BlindBoxDisplayAppearance> | undefined,
  fallback: DisplayAppearanceFallback,
): BlindBoxDisplayAppearance {
  const viewerSlots = finiteNumber(
    appearance?.viewerSlots,
    BLIND_BOX_VIEWER_SLOTS_RANGE.default,
    BLIND_BOX_VIEWER_SLOTS_RANGE.default,
  );
  return {
    ...normalizeDisplayAppearance(appearance, fallback),
    viewerSlots: clamp(Math.trunc(viewerSlots), BLIND_BOX_VIEWER_SLOTS_RANGE.min, BLIND_BOX_VIEWER_SLOTS_RANGE.max),
  };
}

function finiteNumber(value: unknown, fallback: unknown, finalFallback: number): number {
  const candidate = Number(value ?? fallback);
  if (Number.isFinite(candidate)) return candidate;
  const fallbackCandidate = Number(fallback);
  return Number.isFinite(fallbackCandidate) ? fallbackCandidate : finalFallback;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
