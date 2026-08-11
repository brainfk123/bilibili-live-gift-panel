import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { GiftClipCrop, GiftReceipt } from '../src/types';
import { createGiftClipCropEditor, type GiftClipCropEditor } from '../src/ui/config/gift-clip-crop-editor';
import {
  defaultGiftClipCrop,
  giftClipCropFromPixels,
  giftClipDisplayDeltaToSource,
  giftClipCropToPixels,
  isGiftClipSourceSizeSupported,
  normalizeGiftClipCrop,
  updateGiftClipCrop,
  type GiftClipCropHandle,
  type GiftClipPixelRect,
} from '../src/ui/config/gift-clip-crop';

describe('gift clip crop geometry', () => {
  type ResizeHandle = Exclude<GiftClipCropHandle, 'move'>;
  interface ResizeBoundaryCase {
    handle: ResizeHandle;
    minimumAt64: readonly [number, number];
    minimumAt63: readonly [number, number];
    minimumExpected: GiftClipPixelRect;
    maximumAt4096: readonly [number, number];
    maximumAt4097: readonly [number, number];
    maximumExpected: GiftClipPixelRect;
    sourceEdgeDelta: readonly [number, number];
    sourceEdgeExpected: GiftClipPixelRect;
  }

  const resizeBoundaryCases: ResizeBoundaryCase[] = [
    {
      handle: 'n',
      minimumAt64: [0, 1], minimumAt63: [0, 2],
      minimumExpected: { x: 1000, y: 1001, width: 65, height: 64 },
      maximumAt4096: [0, -1], maximumAt4097: [0, -2],
      maximumExpected: { x: 3000, y: 2999, width: 4095, height: 4096 },
      sourceEdgeDelta: [0, -9999],
      sourceEdgeExpected: { x: 2000, y: 0, width: 512, height: 2512 },
    },
    {
      handle: 'ne',
      minimumAt64: [-1, 1], minimumAt63: [-2, 2],
      minimumExpected: { x: 1000, y: 1001, width: 64, height: 64 },
      maximumAt4096: [1, -1], maximumAt4097: [2, -2],
      maximumExpected: { x: 3000, y: 2999, width: 4096, height: 4096 },
      sourceEdgeDelta: [9999, -9999],
      sourceEdgeExpected: { x: 2000, y: 0, width: 3000, height: 2512 },
    },
    {
      handle: 'e',
      minimumAt64: [-1, 0], minimumAt63: [-2, 0],
      minimumExpected: { x: 1000, y: 1000, width: 64, height: 65 },
      maximumAt4096: [1, 0], maximumAt4097: [2, 0],
      maximumExpected: { x: 3000, y: 3000, width: 4096, height: 4095 },
      sourceEdgeDelta: [9999, 0],
      sourceEdgeExpected: { x: 2000, y: 2000, width: 3000, height: 512 },
    },
    {
      handle: 'se',
      minimumAt64: [-1, -1], minimumAt63: [-2, -2],
      minimumExpected: { x: 1000, y: 1000, width: 64, height: 64 },
      maximumAt4096: [1, 1], maximumAt4097: [2, 2],
      maximumExpected: { x: 3000, y: 3000, width: 4096, height: 4096 },
      sourceEdgeDelta: [9999, 9999],
      sourceEdgeExpected: { x: 2000, y: 2000, width: 3000, height: 3000 },
    },
    {
      handle: 's',
      minimumAt64: [0, -1], minimumAt63: [0, -2],
      minimumExpected: { x: 1000, y: 1000, width: 65, height: 64 },
      maximumAt4096: [0, 1], maximumAt4097: [0, 2],
      maximumExpected: { x: 3000, y: 3000, width: 4095, height: 4096 },
      sourceEdgeDelta: [0, 9999],
      sourceEdgeExpected: { x: 2000, y: 2000, width: 512, height: 3000 },
    },
    {
      handle: 'sw',
      minimumAt64: [1, -1], minimumAt63: [2, -2],
      minimumExpected: { x: 1001, y: 1000, width: 64, height: 64 },
      maximumAt4096: [-1, 1], maximumAt4097: [-2, 2],
      maximumExpected: { x: 2999, y: 3000, width: 4096, height: 4096 },
      sourceEdgeDelta: [-9999, 9999],
      sourceEdgeExpected: { x: 0, y: 2000, width: 2512, height: 3000 },
    },
    {
      handle: 'w',
      minimumAt64: [1, 0], minimumAt63: [2, 0],
      minimumExpected: { x: 1001, y: 1000, width: 64, height: 65 },
      maximumAt4096: [-1, 0], maximumAt4097: [-2, 0],
      maximumExpected: { x: 2999, y: 3000, width: 4096, height: 4095 },
      sourceEdgeDelta: [-9999, 0],
      sourceEdgeExpected: { x: 0, y: 2000, width: 2512, height: 512 },
    },
    {
      handle: 'nw',
      minimumAt64: [1, 1], minimumAt63: [2, 2],
      minimumExpected: { x: 1001, y: 1001, width: 64, height: 64 },
      maximumAt4096: [-1, -1], maximumAt4097: [-2, -2],
      maximumExpected: { x: 2999, y: 2999, width: 4096, height: 4096 },
      sourceEdgeDelta: [-9999, -9999],
      sourceEdgeExpected: { x: 0, y: 0, width: 2512, height: 2512 },
    },
  ];

  const initial = giftClipCropFromPixels({ x: 100, y: 75, width: 200, height: 150 }, 400, 300);
  const expected: Record<GiftClipCropHandle, { x: number; y: number; width: number; height: number }> = {
    move: { x: 140, y: 105, width: 200, height: 150 },
    n: { x: 100, y: 105, width: 200, height: 120 },
    ne: { x: 100, y: 105, width: 240, height: 120 },
    e: { x: 100, y: 75, width: 240, height: 150 },
    se: { x: 100, y: 75, width: 240, height: 180 },
    s: { x: 100, y: 75, width: 200, height: 180 },
    sw: { x: 140, y: 75, width: 160, height: 180 },
    w: { x: 140, y: 75, width: 160, height: 150 },
    nw: { x: 140, y: 105, width: 160, height: 120 },
  };

  it.each(Object.keys(expected) as GiftClipCropHandle[])('updates %s without changing unrelated edges', (handle) => {
    expect(giftClipCropToPixels(updateGiftClipCrop(initial, handle, 40, 30, 400, 300), 400, 300))
      .toEqual(expected[handle]);
  });

  it('defaults damaged values to the full source and clamps valid values inside it', () => {
    expect(defaultGiftClipCrop()).toEqual({ x: 0, y: 0, width: 1, height: 1 });
    expect(normalizeGiftClipCrop({ x: Number.NaN, y: 0, width: 1, height: 1 })).toEqual(defaultGiftClipCrop());
    expect(giftClipCropToPixels({ x: .9, y: .9, width: .5, height: .5 }, 400, 300))
      .toEqual({ x: 200, y: 150, width: 200, height: 150 });
  });

  it('rounds normalized origins and dimensions independently', () => {
    expect(giftClipCropToPixels({ x: .005, y: .005, width: .65, height: .65 }, 101, 101))
      .toEqual({ x: 1, y: 1, width: 66, height: 66 });
  });

  it('shifts or expands the independently rounded rectangle at source edges', () => {
    expect(giftClipCropToPixels({ x: .5, y: .5, width: .5, height: .5 }, 201, 201))
      .toEqual({ x: 100, y: 100, width: 101, height: 101 });
    expect(giftClipCropToPixels({ x: .99, y: .99, width: .01, height: .01 }, 101, 101))
      .toEqual({ x: 37, y: 37, width: 64, height: 64 });
  });

  it('keeps move operations inside the source without changing crop dimensions', () => {
    const tiny = giftClipCropFromPixels({ x: 100, y: 100, width: 80, height: 80 }, 400, 300);
    expect(giftClipCropToPixels(updateGiftClipCrop(tiny, 'move', 999, 999, 400, 300), 400, 300))
      .toEqual({ x: 320, y: 220, width: 80, height: 80 });
  });

  it.each(resizeBoundaryCases)('$handle clamps an attempted 63px selection at the 64px minimum', ({
    handle, minimumAt64, minimumAt63, minimumExpected,
  }) => {
    const initial = giftClipCropFromPixels({ x: 1000, y: 1000, width: 65, height: 65 }, 4097, 4097);

    expect(giftClipCropToPixels(
      updateGiftClipCrop(initial, handle, ...minimumAt64, 4097, 4097),
      4097,
      4097,
    )).toEqual(minimumExpected);
    expect(giftClipCropToPixels(
      updateGiftClipCrop(initial, handle, ...minimumAt63, 4097, 4097),
      4097,
      4097,
    )).toEqual(minimumExpected);
  });

  it.each(resizeBoundaryCases)('$handle caps an attempted 4097px selection at the 4096px maximum', ({
    handle, maximumAt4096, maximumAt4097, maximumExpected,
  }) => {
    const initial = giftClipCropFromPixels(
      { x: 3000, y: 3000, width: 4095, height: 4095 },
      10_000,
      10_000,
    );

    expect(giftClipCropToPixels(
      updateGiftClipCrop(initial, handle, ...maximumAt4096, 10_000, 10_000),
      10_000,
      10_000,
    )).toEqual(maximumExpected);
    expect(giftClipCropToPixels(
      updateGiftClipCrop(initial, handle, ...maximumAt4097, 10_000, 10_000),
      10_000,
      10_000,
    )).toEqual(maximumExpected);
  });

  it.each(resizeBoundaryCases)('$handle stops at the source edge while preserving the opposite edge', ({
    handle, sourceEdgeDelta, sourceEdgeExpected,
  }) => {
    const initial = giftClipCropFromPixels({ x: 2000, y: 2000, width: 512, height: 512 }, 5000, 5000);

    expect(giftClipCropToPixels(
      updateGiftClipCrop(initial, handle, ...sourceEdgeDelta, 5000, 5000),
      5000,
      5000,
    )).toEqual(sourceEdgeExpected);
  });

  it('rejects a source when either original dimension is under 64px', () => {
    expect(isGiftClipSourceSizeSupported(64, 64)).toBe(true);
    expect(isGiftClipSourceSizeSupported(63, 640)).toBe(false);
    expect(isGiftClipSourceSizeSupported(640, 63)).toBe(false);
  });

  it('converts display pointer deltas to original source pixels', () => {
    expect(giftClipDisplayDeltaToSource(48, 24, { width: 480, height: 270 }, 640, 360))
      .toEqual({ x: 64, y: 32 });
  });

  it('centers the full default crop when either source dimension exceeds 4096px', () => {
    expect(giftClipCropToPixels(defaultGiftClipCrop(), 8192, 6000))
      .toEqual({ x: 2048, y: 952, width: 4096, height: 4096 });
    expect(giftClipCropToPixels(defaultGiftClipCrop(), 4096, 4096))
      .toEqual({ x: 0, y: 0, width: 4096, height: 4096 });
  });

});

class CropTestStyle {
  [name: string]: string | ((name: string, value: string) => void);

  setProperty(name: string, value: string): void {
    this[name] = value;
  }
}

class CropTestElement {
  className = '';
  textContent = '';
  dataset: Record<string, string> = {};
  children: CropTestElement[] = [];
  parent: CropTestElement | null = null;
  style = new CropTestStyle();
  attributes: Record<string, string> = {};
  type = '';
  tabIndex = -1;
  inert = false;
  alt = '';
  draggable = true;
  clientWidth = 0;
  clientHeight = 0;
  rectWidth: number | null = null;
  rectHeight: number | null = null;
  releasedPointers: number[] = [];
  onpointerdown: ((event: PointerEvent) => unknown) | null = null;
  onpointermove: ((event: PointerEvent) => unknown) | null = null;
  onpointerup: ((event: PointerEvent) => unknown) | null = null;
  onpointercancel: ((event: PointerEvent) => unknown) | null = null;
  onlostpointercapture: ((event: PointerEvent) => unknown) | null = null;
  onkeydown: ((event: KeyboardEvent) => unknown) | null = null;
  onkeyup: ((event: KeyboardEvent) => unknown) | null = null;
  onfocusout: ((event: FocusEvent) => unknown) | null = null;
  private readonly capturedPointers = new Set<number>();
  readonly classList = {
    add: (...names: string[]) => this.updateClasses(names, true),
    remove: (...names: string[]) => this.updateClasses(names, false),
    contains: (name: string) => this.className.split(/\s+/).includes(name),
  };

  constructor(readonly tagName: string) {}

  append(...children: CropTestElement[]): void {
    for (const child of children) {
      child.parent = this;
      if (child.className === 'gift-clip-crop-layer') {
        child.clientWidth = this.clientWidth;
        child.clientHeight = this.clientHeight;
      }
      this.children.push(child);
    }
  }

  remove(): void {
    if (!this.parent) return;
    const index = this.parent.children.indexOf(this);
    if (index >= 0) this.parent.children.splice(index, 1);
    this.parent = null;
  }

  setAttribute(name: string, value: string): void {
    this.attributes[name] = value;
  }

  getAttribute(name: string): string | null {
    return this.attributes[name] ?? null;
  }

  querySelector(selector: string): CropTestElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  querySelectorAll(selector: string): CropTestElement[] {
    const className = selector.startsWith('.') ? selector.slice(1) : '';
    const found: CropTestElement[] = [];
    const visit = (element: CropTestElement): void => {
      for (const child of element.children) {
        const matchesClass = className && child.className.split(' ').includes(className);
        if (matchesClass || (!className && child.tagName === selector)) found.push(child);
        visit(child);
      }
    };
    visit(this);
    return found;
  }

  getBoundingClientRect(): DOMRect {
    const percentageWidth = Number.parseFloat(String(this.style.width ?? ''));
    const percentageHeight = Number.parseFloat(String(this.style.height ?? ''));
    const width = this.rectWidth
      ?? (Number.isFinite(percentageWidth) && this.parent ? this.parent.clientWidth * percentageWidth / 100 : this.clientWidth);
    const height = this.rectHeight
      ?? (Number.isFinite(percentageHeight) && this.parent ? this.parent.clientHeight * percentageHeight / 100 : this.clientHeight);
    return { width, height } as DOMRect;
  }

  setPointerCapture(pointerId: number): void {
    this.capturedPointers.add(pointerId);
  }

  hasPointerCapture(pointerId: number): boolean {
    return this.capturedPointers.has(pointerId);
  }

  releasePointerCapture(pointerId: number): void {
    if (!this.capturedPointers.delete(pointerId)) return;
    this.releasedPointers.push(pointerId);
  }

  dispatchPointer(
    type: 'pointerdown' | 'pointermove' | 'pointerup' | 'pointercancel' | 'lostpointercapture',
    init: { pointerId: number; clientX?: number; clientY?: number; button?: number; target?: CropTestElement },
  ): boolean {
    let defaultPrevented = false;
    const event = {
      pointerId: init.pointerId,
      clientX: init.clientX ?? 0,
      clientY: init.clientY ?? 0,
      button: init.button ?? 0,
      target: init.target ?? this,
      currentTarget: this,
      preventDefault: () => { defaultPrevented = true; },
    } as unknown as PointerEvent;
    const handlers = {
      pointerdown: this.onpointerdown,
      pointermove: this.onpointermove,
      pointerup: this.onpointerup,
      pointercancel: this.onpointercancel,
      lostpointercapture: this.onlostpointercapture,
    };
    handlers[type]?.(event);
    return defaultPrevented;
  }

  dispatchKey(key: string, target: CropTestElement = this, shiftKey = false): boolean {
    let defaultPrevented = false;
    this.onkeydown?.({
      key,
      shiftKey,
      target,
      currentTarget: this,
      preventDefault: () => { defaultPrevented = true; },
    } as unknown as KeyboardEvent);
    return defaultPrevented;
  }

  dispatchKeyUp(key: string, target: CropTestElement = this): void {
    this.onkeyup?.({ key, target, currentTarget: this } as unknown as KeyboardEvent);
  }

  dispatchFocusOut(target: CropTestElement = this): void {
    this.onfocusout?.({ target, currentTarget: this } as unknown as FocusEvent);
  }

  private updateClasses(names: string[], add: boolean): void {
    const classes = new Set(this.className.split(/\s+/).filter(Boolean));
    for (const name of names) {
      if (add) classes.add(name);
      else classes.delete(name);
    }
    this.className = [...classes].join(' ');
  }
}

class CropTestResizeObserver {
  static instances: CropTestResizeObserver[] = [];
  disconnected = false;
  observed: Element | null = null;

  constructor(private readonly callback: ResizeObserverCallback) {
    CropTestResizeObserver.instances.push(this);
  }

  observe(target: Element): void {
    this.observed = target;
  }

  disconnect(): void {
    this.disconnected = true;
  }

  trigger(): void {
    if (!this.disconnected) this.callback([], this as unknown as ResizeObserver);
  }
}

interface CropEditorHarness {
  editor: GiftClipCropEditor;
  stage: CropTestElement;
  layer: CropTestElement;
  frame: CropTestElement;
  infoPreview: CropTestElement;
  changes: Array<{ crop: GiftClipCrop; pixels: GiftClipPixelRect }>;
}

function cropReceiptFixture(): GiftReceipt {
  return {
    id: 'receipt-1',
    time: 1_700_000_000,
    giftId: 1,
    giftName: '测试礼物',
    num: 2,
    price: 100,
    totalCoin: 200,
    coinType: 'gold',
    uname: '测试观众',
    effects: [],
  };
}

function createCropEditorHarness(
  initialCrop: GiftClipCrop = defaultGiftClipCrop(),
  sourceWidth = 640,
  sourceHeight = 360,
): CropEditorHarness {
  const stage = new CropTestElement('div');
  stage.clientWidth = 480;
  stage.clientHeight = 270;
  stage.rectWidth = 960;
  stage.rectHeight = 540;
  const changes: Array<{ crop: GiftClipCrop; pixels: GiftClipPixelRect }> = [];
  const editor = createGiftClipCropEditor({
    stage: stage as unknown as HTMLElement,
    sourceWidth,
    sourceHeight,
    initialCrop,
    receipt: cropReceiptFixture(),
    avatar: null,
    onChange: (crop, pixels) => { changes.push({ crop, pixels }); },
  });
  const layer = editor.element as unknown as CropTestElement;
  const frame = layer.querySelector('.gift-clip-crop-frame');
  if (!frame) throw new Error('crop frame was not created');
  const infoPreview = layer.querySelector('.gift-clip-crop-info-preview');
  if (!infoPreview) throw new Error('information preview was not created');
  return { editor, stage, layer, frame, infoPreview, changes };
}

describe('gift clip crop DOM editor', () => {
  beforeEach(() => {
    CropTestResizeObserver.instances = [];
    vi.stubGlobal('document', {
      createElement: (tagName: string) => new CropTestElement(tagName),
    } as unknown as Document);
    vi.stubGlobal('ResizeObserver', CropTestResizeObserver as unknown as typeof ResizeObserver);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('owns the detailed information preview behind the editor interface', () => {
    const { layer, frame, infoPreview } = createCropEditorHarness(
      giftClipCropFromPixels({ x: 160, y: 90, width: 320, height: 180 }, 640, 360),
    );
    const handleElements = layer.querySelectorAll('.gift-clip-crop-handle');
    const infoBar = infoPreview.querySelector('.gift-clip-info-bar');
    const avatarFallback = infoPreview.querySelector('.gift-clip-info-avatar-fallback');
    const name = infoPreview.querySelector('.gift-clip-info-name');
    const gift = infoPreview.querySelector('.gift-clip-info-gift');

    expect(handleElements.map((handle) => handle.dataset.handle)).toEqual(['n', 'ne', 'e', 'se', 's', 'sw', 'w', 'nw']);
    expect(handleElements.every((handle) => handle.tagName === 'button' && handle.type === 'button')).toBe(true);
    expect(handleElements.every((handle) => Boolean(handle.getAttribute('aria-label')))).toBe(true);
    expect(infoPreview.parent?.className).toBe('gift-clip-crop-viewport');
    expect(infoPreview.style.pointerEvents).toBe('none');
    expect(infoPreview.inert).toBe(true);
    expect(infoPreview.style.transform).toBe('scale(0.5)');
    expect(infoPreview.style.width).toBe('480px');
    expect(infoPreview.style.height).toBe('110px');
    expect(infoBar?.style.width).toBe('444px');
    expect(infoBar?.style.height).toBe('90px');
    expect(infoBar?.style.borderRadius).toBe('22px');
    expect(avatarFallback?.textContent).toBe('测');
    expect(name?.textContent).toBe('测试观众');
    expect(gift?.textContent).toBe('赠送 测试礼物 × 2');
    expect(frame.style.left).toBe('25%');
  });

  it('uses the crop layer client area for pointer movement and releases capture on pointer up', () => {
    const initial = giftClipCropFromPixels({ x: 128, y: 72, width: 384, height: 216 }, 640, 360);
    const { editor, frame, layer } = createCropEditorHarness(initial);
    const westHandle = layer.querySelector('.is-w');
    if (!westHandle) throw new Error('west handle was not created');

    expect(frame.dispatchPointer('pointerdown', { pointerId: 7, clientX: 100, target: westHandle })).toBe(true);
    expect(frame.hasPointerCapture(7)).toBe(true);
    frame.dispatchPointer('pointermove', { pointerId: 7, clientX: 148 });

    expect(giftClipCropToPixels(editor.getCrop(), 640, 360))
      .toEqual({ x: 192, y: 72, width: 320, height: 216 });
    frame.dispatchPointer('pointerup', { pointerId: 7 });
    expect(frame.hasPointerCapture(7)).toBe(false);
    expect(frame.releasedPointers).toEqual([7]);
  });

  it('stops an active drag when pointer capture is lost', () => {
    const initial = giftClipCropFromPixels({ x: 64, y: 64, width: 320, height: 180 }, 640, 360);
    const { editor, frame } = createCropEditorHarness(initial);
    frame.dispatchPointer('pointerdown', { pointerId: 4, clientX: 20 });
    frame.dispatchPointer('pointermove', { pointerId: 4, clientX: 30 });
    const cropAfterFirstMove = editor.getCrop();

    frame.dispatchPointer('lostpointercapture', { pointerId: 4 });
    frame.dispatchPointer('pointermove', { pointerId: 4, clientX: 120 });

    expect(editor.getCrop()).toEqual(cropAfterFirstMove);
  });

  it('releases capture and stops movement when a pointer is cancelled', () => {
    const initial = giftClipCropFromPixels({ x: 64, y: 64, width: 320, height: 180 }, 640, 360);
    const { editor, frame } = createCropEditorHarness(initial);
    frame.dispatchPointer('pointerdown', { pointerId: 5, clientX: 20 });

    frame.dispatchPointer('pointercancel', { pointerId: 5, clientX: 20 });
    frame.dispatchPointer('pointermove', { pointerId: 5, clientX: 120 });

    expect(frame.hasPointerCapture(5)).toBe(false);
    expect(frame.releasedPointers).toEqual([5]);
    expect(editor.getCrop()).toEqual(initial);
  });

  it('makes the move surface keyboard reachable with precise and accelerated arrow movement', () => {
    const initial = giftClipCropFromPixels({ x: 64, y: 64, width: 320, height: 180 }, 640, 360);
    const { editor, frame } = createCropEditorHarness(initial);

    expect(frame.tabIndex).toBe(0);
    expect(frame.getAttribute('aria-label')).toContain('方向键');
    expect(frame.dispatchKey('ArrowRight')).toBe(true);
    expect(frame.dispatchKey('ArrowDown', frame, true)).toBe(true);
    expect(giftClipCropToPixels(editor.getCrop(), 640, 360))
      .toEqual({ x: 65, y: 74, width: 320, height: 180 });
  });

  it('resizes a focused handle with arrow keys without changing unrelated edges', () => {
    const initial = giftClipCropFromPixels({ x: 64, y: 64, width: 320, height: 180 }, 640, 360);
    const { editor, frame, layer, changes } = createCropEditorHarness(initial);
    const eastHandle = layer.querySelector('.is-e');
    if (!eastHandle) throw new Error('east handle was not created');

    expect(frame.dispatchKey('ArrowLeft', eastHandle, true)).toBe(true);
    expect(frame.dispatchKey('ArrowUp', eastHandle)).toBe(false);
    expect(giftClipCropToPixels(editor.getCrop(), 640, 360))
      .toEqual({ x: 64, y: 64, width: 310, height: 180 });
    expect(changes).toHaveLength(2);
  });

  it('exposes an inert rule-of-thirds guide only while pointer adjustment is active', () => {
    const { frame, layer } = createCropEditorHarness();
    const guides = layer.querySelector('.gift-clip-crop-guides');
    const eastHandle = layer.querySelector('.is-e');
    if (!guides || !eastHandle) throw new Error('crop guide or east handle missing');

    expect(guides.getAttribute('aria-hidden')).toBe('true');
    expect(frame.classList.contains('is-adjusting')).toBe(false);
    frame.dispatchPointer('pointerdown', { pointerId: 11, target: frame });
    expect(frame.classList.contains('is-adjusting')).toBe(true);
    expect(frame.classList.contains('is-moving')).toBe(true);
    frame.dispatchPointer('pointerup', { pointerId: 11 });
    expect(frame.classList.contains('is-adjusting')).toBe(false);

    frame.dispatchPointer('pointerdown', { pointerId: 12, target: eastHandle });
    expect(frame.classList.contains('is-adjusting')).toBe(true);
    expect(frame.classList.contains('is-moving')).toBe(false);
    frame.dispatchPointer('pointercancel', { pointerId: 12 });
    expect(frame.classList.contains('is-adjusting')).toBe(false);
  });

  it('shows keyboard adjustment state until keyup or focusout', () => {
    const { frame, layer } = createCropEditorHarness();
    const eastHandle = layer.querySelector('.is-e');
    if (!eastHandle) throw new Error('east handle missing');

    frame.dispatchKey('ArrowLeft', eastHandle);
    expect(frame.classList.contains('is-adjusting')).toBe(true);
    frame.dispatchKeyUp('ArrowLeft', eastHandle);
    expect(frame.classList.contains('is-adjusting')).toBe(false);

    frame.dispatchKey('ArrowRight');
    frame.dispatchFocusOut();
    expect(frame.classList.contains('is-adjusting')).toBe(false);
  });

  it('notifies with the full source crop when reset', () => {
    const initial = giftClipCropFromPixels({ x: 64, y: 64, width: 320, height: 180 }, 640, 360);
    const { editor, changes } = createCropEditorHarness(initial);

    editor.reset();

    expect(editor.getCrop()).toEqual({ x: 0, y: 0, width: 1, height: 1 });
    expect(changes.at(-1)).toEqual({
      crop: { x: 0, y: 0, width: 1, height: 1 },
      pixels: { x: 0, y: 0, width: 640, height: 360 },
    });
  });

  it('resets an oversized source to its centered 4096px selection', () => {
    const initial = giftClipCropFromPixels({ x: 3000, y: 2000, width: 1000, height: 1000 }, 8192, 6000);
    const { editor, changes } = createCropEditorHarness(initial, 8192, 6000);

    editor.reset();

    expect(giftClipCropToPixels(editor.getCrop(), 8192, 6000))
      .toEqual({ x: 2048, y: 952, width: 4096, height: 4096 });
    expect(changes.at(-1)?.pixels)
      .toEqual({ x: 2048, y: 952, width: 4096, height: 4096 });
  });

  it('releases an active drag and stops preview resizing when destroyed', () => {
    const { editor, frame, layer, infoPreview } = createCropEditorHarness();
    const resizeObserver = CropTestResizeObserver.instances[0];
    frame.dispatchPointer('pointerdown', { pointerId: 9 });
    layer.clientWidth = 360;
    resizeObserver.trigger();
    expect(infoPreview.style.transform).toBe('scale(0.75)');

    editor.destroy();
    layer.clientWidth = 240;
    resizeObserver.trigger();

    expect(frame.releasedPointers).toEqual([9]);
    expect(frame.onpointerdown).toBeNull();
    expect(frame.onpointermove).toBeNull();
    expect(frame.onpointerup).toBeNull();
    expect(frame.onpointercancel).toBeNull();
    expect(frame.onlostpointercapture).toBeNull();
    expect(frame.onkeydown).toBeNull();
    expect(frame.onkeyup).toBeNull();
    expect(frame.onfocusout).toBeNull();
    expect(frame.classList.contains('is-adjusting')).toBe(false);
    expect(frame.classList.contains('is-moving')).toBe(false);
    expect(layer.parent).toBeNull();
    expect(resizeObserver.disconnected).toBe(true);
    expect(infoPreview.style.transform).toBe('scale(0.75)');
  });
});
