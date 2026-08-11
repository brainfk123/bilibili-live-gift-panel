import type { GiftClipCrop } from '../../types';

export const MIN_GIFT_CLIP_SOURCE_SIZE = 64;

export type GiftClipCropHandle = 'move' | 'n' | 'ne' | 'e' | 'se' | 's' | 'sw' | 'w' | 'nw';

export interface GiftClipPixelRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export const defaultGiftClipCrop = (): GiftClipCrop => ({ x: 0, y: 0, width: 1, height: 1 });

export function isGiftClipSourceSizeSupported(width: number, height: number): boolean {
  return Number.isInteger(width) && Number.isInteger(height)
    && width >= MIN_GIFT_CLIP_SOURCE_SIZE && height >= MIN_GIFT_CLIP_SOURCE_SIZE;
}

export function normalizeGiftClipCrop(value: unknown): GiftClipCrop {
  if (!value || typeof value !== 'object') return defaultGiftClipCrop();
  const candidate = value as Partial<GiftClipCrop>;
  const numbers = [candidate.x, candidate.y, candidate.width, candidate.height].map(Number);
  if (numbers.some((number) => !Number.isFinite(number)) || numbers[2] <= 0 || numbers[3] <= 0) {
    return defaultGiftClipCrop();
  }
  const width = Math.min(1, numbers[2]);
  const height = Math.min(1, numbers[3]);
  return {
    x: Math.min(1 - width, Math.max(0, numbers[0])),
    y: Math.min(1 - height, Math.max(0, numbers[1])),
    width,
    height,
  };
}

export function constrainGiftClipCrop(crop: GiftClipCrop, sourceWidth: number, sourceHeight: number): GiftClipCrop {
  return giftClipCropFromPixels(giftClipCropToPixels(crop, sourceWidth, sourceHeight), sourceWidth, sourceHeight);
}

export function giftClipCropToPixels(crop: GiftClipCrop, sourceWidth: number, sourceHeight: number): GiftClipPixelRect {
  const normalized = normalizeGiftClipCrop(crop);
  return constrainPixelRect({
    x: Math.round(normalized.x * sourceWidth),
    y: Math.round(normalized.y * sourceHeight),
    width: Math.round(normalized.width * sourceWidth),
    height: Math.round(normalized.height * sourceHeight),
  }, sourceWidth, sourceHeight);
}

export function giftClipCropFromPixels(rect: GiftClipPixelRect, sourceWidth: number, sourceHeight: number): GiftClipCrop {
  const constrained = constrainPixelRect(rect, sourceWidth, sourceHeight);
  return normalizeGiftClipCrop({
    x: constrained.x / sourceWidth,
    y: constrained.y / sourceHeight,
    width: constrained.width / sourceWidth,
    height: constrained.height / sourceHeight,
  });
}

export function giftClipDisplayDeltaToSource(
  deltaX: number,
  deltaY: number,
  displaySize: { readonly width: number; readonly height: number },
  sourceWidth: number,
  sourceHeight: number,
): { x: number; y: number } {
  return {
    x: deltaX * sourceWidth / Math.max(1, displaySize.width),
    y: deltaY * sourceHeight / Math.max(1, displaySize.height),
  };
}

export function updateGiftClipCrop(
  crop: GiftClipCrop,
  handle: GiftClipCropHandle,
  deltaX: number,
  deltaY: number,
  sourceWidth: number,
  sourceHeight: number,
): GiftClipCrop {
  const rect = giftClipCropToPixels(crop, sourceWidth, sourceHeight);
  let left = rect.x;
  let top = rect.y;
  let right = rect.x + rect.width;
  let bottom = rect.y + rect.height;
  const minimumWidth = Math.min(MIN_GIFT_CLIP_SOURCE_SIZE, sourceWidth);
  const minimumHeight = Math.min(MIN_GIFT_CLIP_SOURCE_SIZE, sourceHeight);

  if (handle === 'move') {
    const moveX = clamp(deltaX, -left, sourceWidth - right);
    const moveY = clamp(deltaY, -top, sourceHeight - bottom);
    left += moveX;
    right += moveX;
    top += moveY;
    bottom += moveY;
  } else {
    if (handle === 'n' || handle === 'ne' || handle === 'nw') top = clamp(top + deltaY, 0, bottom - minimumHeight);
    if (handle === 's' || handle === 'se' || handle === 'sw') bottom = clamp(bottom + deltaY, top + minimumHeight, sourceHeight);
    if (handle === 'e' || handle === 'ne' || handle === 'se') right = clamp(right + deltaX, left + minimumWidth, sourceWidth);
    if (handle === 'w' || handle === 'nw' || handle === 'sw') left = clamp(left + deltaX, 0, right - minimumWidth);
  }

  return giftClipCropFromPixels({ x: left, y: top, width: right - left, height: bottom - top }, sourceWidth, sourceHeight);
}

function constrainPixelRect(rect: GiftClipPixelRect, sourceWidth: number, sourceHeight: number): GiftClipPixelRect {
  const width = Math.max(0, Math.round(sourceWidth));
  const height = Math.max(0, Math.round(sourceHeight));
  if (!width || !height) return { x: 0, y: 0, width, height };

  const horizontal = constrainPixelAxis(rect.x, rect.width, width);
  const vertical = constrainPixelAxis(rect.y, rect.height, height);
  return { x: horizontal.origin, y: vertical.origin, width: horizontal.size, height: vertical.size };
}

function constrainPixelAxis(origin: number, size: number, bound: number): { origin: number; size: number } {
  let roundedOrigin = Math.round(Number.isFinite(origin) ? origin : 0);
  let roundedSize = Math.round(Number.isFinite(size) ? size : 0);
  if (roundedSize < 0) {
    roundedOrigin += roundedSize;
    roundedSize = Math.abs(roundedSize);
  }
  const minimum = Math.min(MIN_GIFT_CLIP_SOURCE_SIZE, bound);
  const constrainedSize = clamp(roundedSize, minimum, bound);
  return {
    origin: clamp(roundedOrigin, 0, bound - constrainedSize),
    size: constrainedSize,
  };
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, Number.isFinite(value) ? value : 0));
}
