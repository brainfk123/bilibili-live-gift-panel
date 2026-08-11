import type { GiftReceipt } from '../../types';
import type { GiftClipPixelRect } from './gift-clip-crop';
import {
  GIFT_CLIP_INFO_BAR_DESIGN,
  giftClipInfoBarLayout,
} from './gift-clip-info-bar';
import type { GiftClipVisual } from './gift-clip-media';

export { giftClipInfoBarLayout } from './gift-clip-info-bar';

export function prepareGiftClipOutputCanvas(canvas: HTMLCanvasElement, crop: GiftClipPixelRect): void {
  canvas.width = crop.width;
  canvas.height = crop.height;
}

export function drawGiftClipSourcePreview(
  context: CanvasRenderingContext2D,
  visual: GiftClipVisual | null,
): void {
  drawGiftClipBackground(context, context.canvas.width, context.canvas.height);
  if (visual?.width && visual.height) {
    context.drawImage(visual.source, 0, 0, visual.width, visual.height);
  }
}

export function drawGiftClipOutputFrame(
  context: CanvasRenderingContext2D,
  receipt: GiftReceipt,
  visual: GiftClipVisual | null,
  avatar: HTMLImageElement | null,
  crop: GiftClipPixelRect,
): void {
  const outputWidth = context.canvas.width;
  const outputHeight = context.canvas.height;
  drawGiftClipBackground(context, outputWidth, outputHeight);

  if (visual?.width && visual.height) {
    context.drawImage(visual.source, crop.x, crop.y, crop.width, crop.height, 0, 0, crop.width, crop.height);
  }

  const layout = giftClipInfoBarLayout(outputWidth, outputHeight);
  const design = GIFT_CLIP_INFO_BAR_DESIGN;
  const barGradient = context.createLinearGradient(layout.x, layout.y, layout.x + layout.width, layout.y + layout.height);
  barGradient.addColorStop(0, design.gradientStart);
  barGradient.addColorStop(1, design.gradientEnd);
  roundedRect(context, layout.x, layout.y, layout.width, layout.height, layout.radius);
  context.fillStyle = barGradient;
  context.fill();
  context.strokeStyle = design.barBorderColor;
  context.lineWidth = layout.borderWidth;
  context.stroke();

  context.save();
  context.beginPath();
  context.arc(layout.avatarX, layout.avatarY, layout.avatarRadius, 0, Math.PI * 2);
  context.clip();
  if (avatar?.naturalWidth && avatar.naturalHeight) {
    const side = Math.min(avatar.naturalWidth, avatar.naturalHeight);
    const avatarSide = layout.avatarRadius * 2;
    context.drawImage(
      avatar,
      (avatar.naturalWidth - side) / 2,
      (avatar.naturalHeight - side) / 2,
      side,
      side,
      layout.avatarX - layout.avatarRadius,
      layout.avatarY - layout.avatarRadius,
      avatarSide,
      avatarSide,
    );
  } else {
    const avatarSide = layout.avatarRadius * 2;
    context.fillStyle = design.fallbackBackground;
    context.fillRect(layout.avatarX - layout.avatarRadius, layout.avatarY - layout.avatarRadius, avatarSide, avatarSide);
    context.fillStyle = design.fallbackColor;
    context.textAlign = 'center';
    context.textBaseline = 'middle';
    context.font = `700 ${layout.fallbackFontSize}px system-ui, sans-serif`;
    context.fillText((receipt.uname || '观').slice(0, 1), layout.avatarX, layout.avatarY);
  }
  context.restore();
  context.strokeStyle = design.avatarBorderColor;
  context.lineWidth = layout.avatarBorderWidth;
  context.beginPath();
  context.arc(layout.avatarX, layout.avatarY, layout.avatarRadius, 0, Math.PI * 2);
  context.stroke();

  const nameFont = `700 ${layout.nameFontSize}px system-ui, sans-serif`;
  const giftFont = `500 ${layout.giftFontSize}px system-ui, sans-serif`;
  const name = truncateCanvasText(context, receipt.uname?.trim() || '匿名观众', layout.maxTextWidth, nameFont);
  const giftText = truncateCanvasText(context, `赠送 ${receipt.giftName || '礼物'} × ${Math.max(1, receipt.num || 1)}`, layout.maxTextWidth, giftFont);
  context.textAlign = 'left';
  context.textBaseline = 'alphabetic';
  context.fillStyle = '#ffffff';
  context.font = nameFont;
  context.fillText(name, layout.textX, layout.nameY);
  context.fillStyle = design.giftTextColor;
  context.font = giftFont;
  context.fillText(giftText, layout.textX, layout.giftY);
}

function drawGiftClipBackground(context: CanvasRenderingContext2D, width: number, height: number): void {
  const gradient = context.createLinearGradient(0, 0, width, height);
  gradient.addColorStop(0, '#12101d');
  gradient.addColorStop(0.48, '#24152d');
  gradient.addColorStop(1, '#511d45');
  context.fillStyle = gradient;
  context.fillRect(0, 0, width, height);

  const scale = width / 480;
  const glow = context.createRadialGradient(240 * scale, 185 * scale, 20 * scale, 240 * scale, 185 * scale, 250 * scale);
  glow.addColorStop(0, 'rgba(255, 113, 164, .3)');
  glow.addColorStop(1, 'rgba(255, 113, 164, 0)');
  context.fillStyle = glow;
  context.fillRect(0, 0, width, height);
}

function roundedRect(context: CanvasRenderingContext2D, x: number, y: number, width: number, height: number, radius: number): void {
  context.beginPath();
  context.roundRect(x, y, width, height, radius);
}

function truncateCanvasText(context: CanvasRenderingContext2D, text: string, maxWidth: number, font: string): string {
  context.font = font;
  if (context.measureText(text).width <= maxWidth) return text;
  let value = text;
  while (value.length > 1 && context.measureText(`${value}…`).width > maxWidth) value = value.slice(0, -1);
  return `${value}…`;
}
