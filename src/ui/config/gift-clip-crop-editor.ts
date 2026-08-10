import type { GiftClipCrop } from '../../types';
import {
  constrainGiftClipCrop,
  defaultGiftClipCrop,
  giftClipCropToPixels,
  giftClipDisplayDeltaToSource,
  updateGiftClipCrop,
  type GiftClipCropHandle,
  type GiftClipPixelRect,
} from './gift-clip-crop';

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
  infoPreview: HTMLElement;
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
    infoPreview,
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
  infoPreview.classList.add('gift-clip-crop-info-preview');
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
      crop = defaultGiftClipCrop();
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
