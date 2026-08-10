import type { GiftReceipt } from '../../types';
import type { GiftClipPixelRect } from './gift-clip-crop';
import type { GiftClipVisual } from './gift-clip-media';

export function prepareGiftClipOutputCanvas(canvas: HTMLCanvasElement, crop: GiftClipPixelRect): void {
  canvas.width = crop.width;
  canvas.height = crop.height;
}

export function giftClipInfoBarLayout(outputWidth: number, outputHeight: number) {
  const scale = outputWidth / 480;
  return {
    scale,
    x: 18 * scale,
    y: outputHeight - 110 * scale,
    width: 444 * scale,
    height: 90 * scale,
    radius: 22 * scale,
    avatarX: 67 * scale,
    avatarY: outputHeight - 65 * scale,
    avatarRadius: 30 * scale,
    textX: 114 * scale,
    nameY: outputHeight - 71 * scale,
    giftY: outputHeight - 44 * scale,
  };
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
  const barGradient = context.createLinearGradient(layout.x, layout.y, layout.x + layout.width, layout.y + layout.height);
  barGradient.addColorStop(0, 'rgba(87, 39, 101, .76)');
  barGradient.addColorStop(1, 'rgba(224, 68, 129, .76)');
  roundedRect(context, layout.x, layout.y, layout.width, layout.height, layout.radius);
  context.fillStyle = barGradient;
  context.fill();
  context.strokeStyle = 'rgba(255,255,255,.24)';
  context.lineWidth = 1.5 * layout.scale;
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
    context.fillStyle = '#2a2132';
    context.fillRect(layout.avatarX - layout.avatarRadius, layout.avatarY - layout.avatarRadius, avatarSide, avatarSide);
    context.fillStyle = '#ff85b1';
    context.textAlign = 'center';
    context.textBaseline = 'middle';
    context.font = `700 ${24 * layout.scale}px system-ui, sans-serif`;
    context.fillText((receipt.uname || '观').slice(0, 1), layout.avatarX, layout.avatarY);
  }
  context.restore();
  context.strokeStyle = 'rgba(255,255,255,.78)';
  context.lineWidth = 2 * layout.scale;
  context.beginPath();
  context.arc(layout.avatarX, layout.avatarY, layout.avatarRadius, 0, Math.PI * 2);
  context.stroke();

  const maxTextWidth = 302 * layout.scale;
  const nameFont = `700 ${20 * layout.scale}px system-ui, sans-serif`;
  const giftFont = `500 ${17 * layout.scale}px system-ui, sans-serif`;
  const name = truncateCanvasText(context, receipt.uname?.trim() || '匿名观众', maxTextWidth, nameFont);
  const giftText = truncateCanvasText(context, `赠送 ${receipt.giftName || '礼物'} × ${Math.max(1, receipt.num || 1)}`, maxTextWidth, giftFont);
  context.textAlign = 'left';
  context.textBaseline = 'alphabetic';
  context.fillStyle = '#ffffff';
  context.font = nameFont;
  context.fillText(name, layout.textX, layout.nameY);
  context.fillStyle = 'rgba(255,255,255,.82)';
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
