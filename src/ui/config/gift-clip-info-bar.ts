export interface GiftClipInfoBarDesign {
  readonly baselineWidth: number;
  readonly containerHeight: number;
  readonly horizontalInset: number;
  readonly bottomInset: number;
  readonly barWidth: number;
  readonly barHeight: number;
  readonly barRadius: number;
  readonly barPaddingX: number;
  readonly barPaddingY: number;
  readonly barGap: number;
  readonly barBorderWidth: number;
  readonly avatarCenterX: number;
  readonly avatarCenterFromBottom: number;
  readonly avatarDiameter: number;
  readonly avatarBorderWidth: number;
  readonly fallbackFontSize: number;
  readonly textX: number;
  readonly nameBaselineFromBottom: number;
  readonly giftBaselineFromBottom: number;
  readonly maxTextWidth: number;
  readonly nameFontSize: number;
  readonly giftFontSize: number;
  readonly gradientStart: string;
  readonly gradientEnd: string;
  readonly barBorderColor: string;
  readonly avatarBorderColor: string;
  readonly fallbackBackground: string;
  readonly fallbackColor: string;
  readonly giftTextColor: string;
}

export const GIFT_CLIP_INFO_BAR_DESIGN = {
  baselineWidth: 480,
  containerHeight: 110,
  horizontalInset: 18,
  bottomInset: 20,
  barWidth: 444,
  barHeight: 90,
  barRadius: 22,
  barPaddingX: 18,
  barPaddingY: 13,
  barGap: 16,
  barBorderWidth: 1.5,
  avatarCenterX: 67,
  avatarCenterFromBottom: 65,
  avatarDiameter: 60,
  avatarBorderWidth: 2,
  fallbackFontSize: 24,
  textX: 114,
  nameBaselineFromBottom: 71,
  giftBaselineFromBottom: 44,
  maxTextWidth: 302,
  nameFontSize: 20,
  giftFontSize: 17,
  gradientStart: 'rgba(87, 39, 101, .76)',
  gradientEnd: 'rgba(224, 68, 129, .76)',
  barBorderColor: 'rgba(255,255,255,.24)',
  avatarBorderColor: 'rgba(255,255,255,.78)',
  fallbackBackground: '#2a2132',
  fallbackColor: '#ff85b1',
  giftTextColor: 'rgba(255,255,255,.82)',
} as const satisfies GiftClipInfoBarDesign;

export interface GiftClipInfoBarLayout {
  readonly scale: number;
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
  readonly radius: number;
  readonly borderWidth: number;
  readonly avatarX: number;
  readonly avatarY: number;
  readonly avatarRadius: number;
  readonly avatarBorderWidth: number;
  readonly fallbackFontSize: number;
  readonly textX: number;
  readonly nameY: number;
  readonly giftY: number;
  readonly maxTextWidth: number;
  readonly nameFontSize: number;
  readonly giftFontSize: number;
}

export function giftClipInfoBarLayout(outputWidth: number, outputHeight: number): GiftClipInfoBarLayout {
  const design = GIFT_CLIP_INFO_BAR_DESIGN;
  const scale = outputWidth / design.baselineWidth;
  return {
    scale,
    x: design.horizontalInset * scale,
    y: outputHeight - design.containerHeight * scale,
    width: design.barWidth * scale,
    height: design.barHeight * scale,
    radius: design.barRadius * scale,
    borderWidth: design.barBorderWidth * scale,
    avatarX: design.avatarCenterX * scale,
    avatarY: outputHeight - design.avatarCenterFromBottom * scale,
    avatarRadius: design.avatarDiameter / 2 * scale,
    avatarBorderWidth: design.avatarBorderWidth * scale,
    fallbackFontSize: design.fallbackFontSize * scale,
    textX: design.textX * scale,
    nameY: outputHeight - design.nameBaselineFromBottom * scale,
    giftY: outputHeight - design.giftBaselineFromBottom * scale,
    maxTextWidth: design.maxTextWidth * scale,
    nameFontSize: design.nameFontSize * scale,
    giftFontSize: design.giftFontSize * scale,
  };
}
