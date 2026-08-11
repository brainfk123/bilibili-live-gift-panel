import type { GiftClipCrop, GiftReceipt } from '../../types';
import {
  constrainGiftClipCrop,
  defaultGiftClipCrop,
  giftClipCropToPixels,
  giftClipDisplayDeltaToSource,
  updateGiftClipCrop,
  type GiftClipCropHandle,
  type GiftClipPixelRect,
} from './gift-clip-crop';
import { GIFT_CLIP_INFO_BAR_DESIGN } from './gift-clip-info-bar';

export interface GiftClipCropEditor {
  readonly element: HTMLElement;
  getCrop(): GiftClipCrop;
  reset(): void;
  destroy(): void;
}

interface GiftClipCropEditorOptions {
  stage: HTMLElement;
  sourceWidth: number;
  sourceHeight: number;
  initialCrop: GiftClipCrop;
  receipt: Pick<GiftReceipt, 'uname' | 'giftName' | 'num'>;
  avatar: HTMLImageElement | null;
  onChange: (crop: GiftClipCrop, pixels: GiftClipPixelRect) => void;
}

interface DragState {
  pointerId: number;
  clientX: number;
  clientY: number;
  handle: GiftClipCropHandle;
  crop: GiftClipCrop;
  displaySize: { width: number; height: number };
}

const handles = [
  ['n', '调整上边'],
  ['ne', '调整右上角'],
  ['e', '调整右边'],
  ['se', '调整右下角'],
  ['s', '调整下边'],
  ['sw', '调整左下角'],
  ['w', '调整左边'],
  ['nw', '调整左上角'],
] as const satisfies ReadonlyArray<readonly [Exclude<GiftClipCropHandle, 'move'>, string]>;

export function createGiftClipCropEditor(options: GiftClipCropEditorOptions): GiftClipCropEditor {
  const {
    stage,
    sourceWidth,
    sourceHeight,
    onChange,
  } = options;
  let crop = constrainGiftClipCrop(options.initialCrop, sourceWidth, sourceHeight);
  let dragState: DragState | null = null;
  let destroyed = false;

  const layer = document.createElement('div');
  layer.className = 'gift-clip-crop-layer';
  const frame = document.createElement('div');
  frame.className = 'gift-clip-crop-frame';
  frame.tabIndex = 0;
  frame.setAttribute('aria-label', '移动剪裁区域，使用方向键调整');
  const viewport = document.createElement('div');
  viewport.className = 'gift-clip-crop-viewport';
  const infoPreview = createGiftClipInfoPreview(options.receipt, options.avatar);
  infoPreview.style.pointerEvents = 'none';
  infoPreview.inert = true;
  infoPreview.setAttribute('aria-hidden', 'true');
  viewport.append(infoPreview);

  const handleElements = handles.map(([handle, label]) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `gift-clip-crop-handle is-${handle}`;
    button.dataset.handle = handle;
    button.setAttribute('aria-label', label);
    return button;
  });
  frame.append(viewport, ...handleElements);
  layer.append(frame);
  stage.style.setProperty('--gift-clip-source-width', String(sourceWidth));
  stage.style.setProperty('--gift-clip-source-height', String(sourceHeight));
  stage.append(layer);

  const scaleInfoPreview = (): void => {
    const displayWidth = frame.getBoundingClientRect().width;
    if (displayWidth > 0) infoPreview.style.transform = `scale(${displayWidth / 480})`;
  };
  const notify = (): void => {
    onChange({ ...crop }, giftClipCropToPixels(crop, sourceWidth, sourceHeight));
  };
  const render = (shouldNotify = true): void => {
    frame.style.left = `${crop.x * 100}%`;
    frame.style.top = `${crop.y * 100}%`;
    frame.style.width = `${crop.width * 100}%`;
    frame.style.height = `${crop.height * 100}%`;
    scaleInfoPreview();
    if (shouldNotify) notify();
  };
  const resizeObserver = typeof ResizeObserver === 'undefined'
    ? null
    : new ResizeObserver(scaleInfoPreview);
  resizeObserver?.observe(frame);

  const displaySize = (): { width: number; height: number } => {
    const bounds = layer.getBoundingClientRect();
    return {
      width: layer.clientWidth || bounds.width,
      height: layer.clientHeight || bounds.height,
    };
  };

  frame.onpointerdown = (event) => {
    if (destroyed || dragState || event.button !== 0) return;
    const target = event.target as HTMLElement | null;
    const handle = target?.dataset.handle as GiftClipCropHandle | undefined;
    event.preventDefault();
    dragState = {
      pointerId: event.pointerId,
      clientX: event.clientX,
      clientY: event.clientY,
      handle: handle ?? 'move',
      crop: { ...crop },
      displaySize: displaySize(),
    };
    frame.setPointerCapture(event.pointerId);
  };
  frame.onpointermove = (event) => {
    if (!dragState || dragState.pointerId !== event.pointerId) return;
    event.preventDefault();
    const delta = giftClipDisplayDeltaToSource(
      event.clientX - dragState.clientX,
      event.clientY - dragState.clientY,
      dragState.displaySize,
      sourceWidth,
      sourceHeight,
    );
    crop = updateGiftClipCrop(
      dragState.crop,
      dragState.handle,
      delta.x,
      delta.y,
      sourceWidth,
      sourceHeight,
    );
    render();
  };
  const finishDrag = (event: PointerEvent): void => {
    if (!dragState || dragState.pointerId !== event.pointerId) return;
    if (frame.hasPointerCapture(event.pointerId)) frame.releasePointerCapture(event.pointerId);
    dragState = null;
  };
  frame.onpointerup = finishDrag;
  frame.onpointercancel = finishDrag;
  frame.onlostpointercapture = (event) => {
    if (dragState?.pointerId === event.pointerId) dragState = null;
  };
  frame.onkeydown = (event) => {
    const direction = {
      ArrowLeft: { x: -1, y: 0 },
      ArrowRight: { x: 1, y: 0 },
      ArrowUp: { x: 0, y: -1 },
      ArrowDown: { x: 0, y: 1 },
    }[event.key];
    if (!direction) return;
    const target = event.target as HTMLElement | null;
    const handle = (target?.dataset.handle as GiftClipCropHandle | undefined) ?? 'move';
    const adjustsX = handle === 'move' || handle.includes('e') || handle.includes('w');
    const adjustsY = handle === 'move' || handle.includes('n') || handle.includes('s');
    if ((direction.x && !adjustsX) || (direction.y && !adjustsY)) return;
    event.preventDefault();
    const step = event.shiftKey ? 10 : 1;
    crop = updateGiftClipCrop(
      crop,
      handle,
      direction.x * step,
      direction.y * step,
      sourceWidth,
      sourceHeight,
    );
    render();
  };

  render();

  return {
    element: layer,
    getCrop: () => ({ ...crop }),
    reset: () => {
      if (destroyed) return;
      crop = constrainGiftClipCrop(defaultGiftClipCrop(), sourceWidth, sourceHeight);
      render();
    },
    destroy: () => {
      if (destroyed) return;
      destroyed = true;
      if (dragState && frame.hasPointerCapture(dragState.pointerId)) {
        frame.releasePointerCapture(dragState.pointerId);
      }
      dragState = null;
      frame.onpointerdown = null;
      frame.onpointermove = null;
      frame.onpointerup = null;
      frame.onpointercancel = null;
      frame.onlostpointercapture = null;
      frame.onkeydown = null;
      resizeObserver?.disconnect();
      layer.remove();
    },
  };
}

function createGiftClipInfoPreview(
  receipt: Pick<GiftReceipt, 'uname' | 'giftName' | 'num'>,
  avatar: HTMLImageElement | null,
): HTMLElement {
  const design = GIFT_CLIP_INFO_BAR_DESIGN;
  const preview = document.createElement('div');
  preview.className = 'gift-clip-crop-info-preview';
  preview.style.boxSizing = 'border-box';
  preview.style.width = `${design.baselineWidth}px`;
  preview.style.height = `${design.containerHeight}px`;
  preview.style.padding = `0 ${design.horizontalInset}px ${design.bottomInset}px`;

  const bar = document.createElement('div');
  bar.className = 'gift-clip-info-bar';
  bar.style.display = 'flex';
  bar.style.alignItems = 'center';
  bar.style.gap = `${design.barGap}px`;
  bar.style.boxSizing = 'border-box';
  bar.style.width = `${design.barWidth}px`;
  bar.style.height = `${design.barHeight}px`;
  bar.style.border = `${design.barBorderWidth}px solid ${design.barBorderColor}`;
  bar.style.borderRadius = `${design.barRadius}px`;
  bar.style.padding = `${design.barPaddingY}px ${design.barPaddingX}px`;
  bar.style.background = `linear-gradient(135deg,${design.gradientStart},${design.gradientEnd})`;
  bar.style.color = '#fff';
  bar.style.fontFamily = 'system-ui, sans-serif';

  const avatarFrame = document.createElement('span');
  avatarFrame.className = 'gift-clip-info-avatar';
  avatarFrame.style.display = 'block';
  avatarFrame.style.flex = `0 0 ${design.avatarDiameter}px`;
  avatarFrame.style.width = `${design.avatarDiameter}px`;
  avatarFrame.style.height = `${design.avatarDiameter}px`;
  avatarFrame.style.overflow = 'hidden';
  avatarFrame.style.border = `${design.avatarBorderWidth}px solid ${design.avatarBorderColor}`;
  avatarFrame.style.borderRadius = '50%';
  avatarFrame.style.boxSizing = 'border-box';
  if (avatar) {
    avatar.alt = '';
    avatar.draggable = false;
    avatar.style.display = 'block';
    avatar.style.width = `${design.avatarDiameter}px`;
    avatar.style.height = `${design.avatarDiameter}px`;
    avatar.style.objectFit = 'cover';
    avatarFrame.append(avatar);
  } else {
    const fallback = document.createElement('span');
    fallback.className = 'gift-clip-info-avatar-fallback';
    fallback.textContent = (receipt.uname?.trim() || '观').slice(0, 1);
    fallback.style.display = 'grid';
    fallback.style.width = `${design.avatarDiameter}px`;
    fallback.style.height = `${design.avatarDiameter}px`;
    fallback.style.placeItems = 'center';
    fallback.style.background = design.fallbackBackground;
    fallback.style.color = design.fallbackColor;
    fallback.style.font = `700 ${design.fallbackFontSize}px system-ui, sans-serif`;
    avatarFrame.append(fallback);
  }

  const text = document.createElement('span');
  text.className = 'gift-clip-info-text';
  text.style.display = 'grid';
  text.style.minWidth = '0';
  text.style.gap = '4px';
  text.style.textAlign = 'left';
  const name = document.createElement('strong');
  name.className = 'gift-clip-info-name';
  name.textContent = receipt.uname?.trim() || '匿名观众';
  name.style.overflow = 'hidden';
  name.style.fontSize = `${design.nameFontSize}px`;
  name.style.lineHeight = '1.2';
  name.style.textOverflow = 'ellipsis';
  name.style.whiteSpace = 'nowrap';
  const gift = document.createElement('span');
  gift.className = 'gift-clip-info-gift';
  gift.textContent = `赠送 ${receipt.giftName || '礼物'} × ${Math.max(1, receipt.num || 1)}`;
  gift.style.overflow = 'hidden';
  gift.style.color = design.giftTextColor;
  gift.style.fontSize = `${design.giftFontSize}px`;
  gift.style.lineHeight = '1.2';
  gift.style.textOverflow = 'ellipsis';
  gift.style.whiteSpace = 'nowrap';
  text.append(name, gift);
  bar.append(avatarFrame, text);
  preview.append(bar);
  return preview;
}
