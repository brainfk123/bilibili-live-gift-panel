import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { defaultState } from '../src/storage';
import type { GiftReceipt } from '../src/types';
import type { GiftClipMediaSession } from '../src/ui/config/gift-clip-media';

const studioMocks = vi.hoisted(() => ({
  loadMediaSession: vi.fn(),
  recordCanvas: vi.fn(),
}));

vi.mock('../src/ui/config/gift-clip-media', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/ui/config/gift-clip-media')>();
  return { ...actual, loadGiftClipMediaSession: studioMocks.loadMediaSession };
});

vi.mock('../src/ui/config/gift-clip-recorder', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/ui/config/gift-clip-recorder')>();
  return { ...actual, recordGiftClipCanvas: studioMocks.recordCanvas };
});

import {
  giftClipAnimationKey,
  openGiftClipStudio,
  type GiftClipStudioController,
} from '../src/ui/config/gift-clip-studio';
import { normalizeGiftClipDuration } from '../src/ui/config/gift-clip-media';

class StudioTestStyle {
  [name: string]: string | ((name: string, value: string) => void);

  cssText = '';
  aspectRatio = '';

  setProperty(name: string, value: string): void {
    this[name] = value;
  }
}

class StudioTestElement {
  static onCropLayerAppended: (() => void) | null = null;

  className = '';
  textContent = '';
  dataset: Record<string, string> = {};
  children: StudioTestElement[] = [];
  parent: StudioTestElement | null = null;
  style = new StudioTestStyle();
  attributes: Record<string, string> = {};
  hidden = false;
  disabled = false;
  type = '';
  tabIndex = -1;
  inert = false;
  width = 0;
  height = 0;
  clientWidth = 480;
  clientHeight = 270;
  value = 0;
  max = 0;
  src = '';
  removeCalls = 0;
  onclick: ((event: MouseEvent) => unknown) | null = null;
  onpointerdown: ((event: PointerEvent) => unknown) | null = null;
  onpointermove: ((event: PointerEvent) => unknown) | null = null;
  onpointerup: ((event: PointerEvent) => unknown) | null = null;
  onpointercancel: ((event: PointerEvent) => unknown) | null = null;
  onlostpointercapture: ((event: PointerEvent) => unknown) | null = null;
  onkeydown: ((event: KeyboardEvent) => unknown) | null = null;
  private readonly listeners = new Map<string, Set<EventListener>>();
  private readonly capturedPointers = new Set<number>();
  readonly classList = {
    add: (...names: string[]) => this.updateClasses(names, true),
    remove: (...names: string[]) => this.updateClasses(names, false),
    contains: (name: string) => this.className.split(/\s+/).includes(name),
  };

  constructor(readonly tagName: string) {}

  append(...children: StudioTestElement[]): void {
    for (const child of children) {
      child.parent = this;
      if (child.className.split(/\s+/).includes('gift-clip-crop-layer')) {
        child.clientWidth = this.clientWidth;
        child.clientHeight = this.clientHeight;
        StudioTestElement.onCropLayerAppended?.();
      }
      this.children.push(child);
    }
  }

  remove(): void {
    this.removeCalls += 1;
    if (!this.parent) return;
    const index = this.parent.children.indexOf(this);
    if (index >= 0) this.parent.children.splice(index, 1);
    this.parent = null;
  }

  setAttribute(name: string, value: string): void {
    this.attributes[name] = value;
    if (name === 'style') this.style.cssText = value;
  }

  removeAttribute(name: string): void {
    delete this.attributes[name];
    if (name === 'src') this.src = '';
  }

  addEventListener(type: string, listener: EventListener): void {
    const listeners = this.listeners.get(type) ?? new Set<EventListener>();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: EventListener): void {
    this.listeners.get(type)?.delete(listener);
  }

  querySelector(selector: string): StudioTestElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  querySelectorAll(selector: string): StudioTestElement[] {
    const className = selector.startsWith('.') ? selector.slice(1) : '';
    const found: StudioTestElement[] = [];
    const visit = (element: StudioTestElement): void => {
      for (const child of element.children) {
        const matches = className
          ? child.className.split(/\s+/).includes(className)
          : child.tagName === selector;
        if (matches) found.push(child);
        visit(child);
      }
    };
    visit(this);
    return found;
  }

  getBoundingClientRect(): DOMRect {
    const widthPercent = Number.parseFloat(String(this.style.width ?? ''));
    const heightPercent = Number.parseFloat(String(this.style.height ?? ''));
    return {
      width: Number.isFinite(widthPercent) && this.parent ? this.parent.clientWidth * widthPercent / 100 : this.clientWidth,
      height: Number.isFinite(heightPercent) && this.parent ? this.parent.clientHeight * heightPercent / 100 : this.clientHeight,
    } as DOMRect;
  }

  getContext(): CanvasRenderingContext2D | null {
    if (this.tagName !== 'canvas') return null;
    const gradient = { addColorStop: vi.fn() };
    return {
      canvas: this,
      createLinearGradient: vi.fn(() => gradient),
      createRadialGradient: vi.fn(() => gradient),
      fillRect: vi.fn(),
      drawImage: vi.fn(),
    } as unknown as CanvasRenderingContext2D;
  }

  setPointerCapture(pointerId: number): void {
    this.capturedPointers.add(pointerId);
  }

  hasPointerCapture(pointerId: number): boolean {
    return this.capturedPointers.has(pointerId);
  }

  releasePointerCapture(pointerId: number): void {
    this.capturedPointers.delete(pointerId);
  }

  focus(): void {}
  pause(): void {}
  load(): void {}
  play(): Promise<void> { return Promise.resolve(); }

  private updateClasses(names: string[], add: boolean): void {
    const classes = new Set(this.className.split(/\s+/).filter(Boolean));
    for (const name of names) {
      if (add) classes.add(name);
      else classes.delete(name);
    }
    this.className = [...classes].join(' ');
  }
}

class StudioTestResizeObserver {
  observe(): void {}
  disconnect(): void {}
}

function receiptFixture(): GiftReceipt {
  return {
    id: 'receipt-1',
    time: 1_700_000_000,
    giftId: 1,
    giftName: '测试礼物',
    num: 1,
    price: 100,
    totalCoin: 100,
    coinType: 'gold',
    uname: '测试观众',
    animation: { webp: 'animation.webp', durationMs: 2400 },
    effects: [],
  };
}

function mediaSessionFixture(width = 640, height = 360): GiftClipMediaSession {
  return {
    width,
    height,
    durationMs: 2400,
    sourceLabel: '短动画',
    avatar: null,
    visualAt: vi.fn(() => null),
    restart: vi.fn(async () => undefined),
    pause: vi.fn(),
    dispose: vi.fn(),
  };
}

function button(root: StudioTestElement, text: string): StudioTestElement {
  const match = root.querySelectorAll('button').find((candidate) => candidate.textContent === text);
  if (!match) throw new Error(`button not found: ${text}`);
  return match;
}

describe('gift clip studio', () => {
  let opened: GiftClipStudioController[];
  let removeGlobalListener: ReturnType<typeof vi.fn>;
  let requestFrame: ReturnType<typeof vi.fn>;
  let cropLayerRafCounts: number[];

  beforeEach(() => {
    opened = [];
    cropLayerRafCounts = [];
    let frameId = 0;
    requestFrame = vi.fn(() => ++frameId);
    removeGlobalListener = vi.fn();
    StudioTestElement.onCropLayerAppended = () => {
      cropLayerRafCounts.push(requestFrame.mock.calls.length);
    };
    studioMocks.loadMediaSession.mockReset();
    studioMocks.recordCanvas.mockReset();
    vi.stubGlobal('document', {
      createElement: (tagName: string) => new StudioTestElement(tagName),
      body: new StudioTestElement('body'),
    } as unknown as Document);
    vi.stubGlobal('ResizeObserver', StudioTestResizeObserver as unknown as typeof ResizeObserver);
    vi.stubGlobal('requestAnimationFrame', requestFrame);
    vi.stubGlobal('cancelAnimationFrame', vi.fn());
    vi.stubGlobal('addEventListener', vi.fn());
    vi.stubGlobal('removeEventListener', removeGlobalListener);
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:recording');
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined);
  });

  afterEach(() => {
    for (const controller of opened) controller.close();
    StudioTestElement.onCropLayerAppended = null;
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const openStudio = (options: Parameters<typeof openGiftClipStudio>[0]) => {
    const controller = openGiftClipStudio(options);
    opened.push(controller);
    return controller;
  };

  it('clamps missing and abnormal animation durations at the media seam', () => {
    expect([undefined, 200, 2200, 60_000].map(normalizeGiftClipDuration))
      .toEqual([3000, 1000, 2200, 15_000]);
  });

  it('keeps a stable crop key for signed versions of the same animation URL', () => {
    expect(giftClipAnimationKey({ giftId: 1, animation: { gif: 'https://i0.hdslb.com/a.gif?token=one', durationMs: 3000 } }))
      .toBe(giftClipAnimationKey({ giftId: 2, animation: { gif: 'https://i0.hdslb.com/a.gif?token=two', durationMs: 5000 } }));
  });

  it('cancels a pending source load on idempotent close without surfacing an error', async () => {
    let loadSignal: AbortSignal | undefined;
    studioMocks.loadMediaSession.mockImplementation((
      _receipt: GiftReceipt,
      _host: HTMLElement,
      signal?: AbortSignal,
    ) => {
      loadSignal = signal;
      return new Promise((_resolve, reject) => {
        signal?.addEventListener('abort', () => reject(signal.reason), { once: true });
      });
    });
    const host = new StudioTestElement('host');
    const onError = vi.fn();
    const controller = openStudio({
      host: host as unknown as HTMLElement,
      receipt: receiptFixture(),
      onError,
    });
    const overlay = host.children[0];
    expect(host.querySelector('.gift-clip-status')?.textContent).toBe('正在读取礼物动画…');

    controller.close();
    controller.close();
    await Promise.resolve();

    expect(loadSignal).toBeInstanceOf(AbortSignal);
    expect(loadSignal?.aborted).toBe(true);
    expect(onError).not.toHaveBeenCalled();
    expect(host.children).toEqual([]);
    expect(overlay.removeCalls).toBe(1);
    expect(removeGlobalListener).toHaveBeenCalledOnce();
  });

  it('shows the exact small-source gate and retries by replacing the media session', async () => {
    const first = mediaSessionFixture(63, 120);
    const second = mediaSessionFixture(63, 120);
    studioMocks.loadMediaSession.mockResolvedValueOnce(first).mockResolvedValueOnce(second);
    const host = new StudioTestElement('host');
    const controller = openStudio({
      host: host as unknown as HTMLElement,
      receipt: receiptFixture(),
    });
    await vi.waitFor(() => {
      expect(host.querySelector('.gift-clip-status')?.textContent)
        .toBe('动画尺寸过小，无法制作回放（63 × 120）');
    });

    expect(button(host, '恢复完整画面').hidden).toBe(true);
    expect(button(host, '确定剪裁并生成').hidden).toBe(true);
    expect(button(host, '保存视频').hidden).toBe(true);
    const retry = button(host, '重试');
    expect(retry.hidden).toBe(false);

    retry.onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(studioMocks.loadMediaSession).toHaveBeenCalledTimes(2));

    expect(first.dispose).toHaveBeenCalledOnce();
    expect(second.dispose).not.toHaveBeenCalled();
    controller.close();
    expect(second.dispose).toHaveBeenCalledOnce();
  });

  it('starts the source preview RAF before mounting the crop editor', async () => {
    const session = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    const host = new StudioTestElement('host');
    const controller = openStudio({
      host: host as unknown as HTMLElement,
      receipt: receiptFixture(),
    });
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-crop-layer')).not.toBeNull());

    expect(cropLayerRafCounts).toEqual([1]);
    controller.close();
  });

  it('notifies the confirmed crop before recording starts', async () => {
    const events: string[] = [];
    const session = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.recordCanvas.mockImplementation(async () => {
      events.push('record');
      return { blob: new Blob(['clip']), mimeType: 'video/webm', extension: 'webm' as const };
    });
    const host = new StudioTestElement('host');
    const controller = openStudio({
      host: host as unknown as HTMLElement,
      receipt: receiptFixture(),
      onCropConfirmed: () => { events.push('confirm'); },
    });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));

    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-status')?.textContent)
      .toBe('WEBM 已生成 · 1 KB · 640 × 360 · 短动画'));

    expect(events).toEqual(['confirm', 'record']);
    controller.close();
  });

  it('re-edits the confirmed crop without reloading media or saving an unconfirmed change', async () => {
    const session = mediaSessionFixture();
    const onCropConfirmed = vi.fn();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.recordCanvas.mockResolvedValue({
      blob: new Blob(['clip']), mimeType: 'video/webm', extension: 'webm',
    });
    const host = new StudioTestElement('host');
    const controller = openStudio({
      host: host as unknown as HTMLElement,
      receipt: receiptFixture(),
      onCropConfirmed,
    });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '保存 WEBM').hidden).toBe(false));

    button(host, '重新剪裁').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-crop-layer')).not.toBeNull());

    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:recording');
    expect(studioMocks.loadMediaSession).toHaveBeenCalledOnce();
    expect(session.restart).toHaveBeenCalledTimes(3);
    expect(session.dispose).not.toHaveBeenCalled();
    expect(onCropConfirmed).toHaveBeenCalledOnce();
    expect(host.querySelector('.gift-clip-status')?.textContent)
      .toBe('剪裁 640 × 360 · 成片按原始像素输出');

    controller.close();
    controller.close();
    expect(session.dispose).toHaveBeenCalledOnce();
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(removeGlobalListener).toHaveBeenCalledOnce();
  });

  it('drops the legacy placement field after the crop cutover', () => {
    const state = defaultState();
    const legacyPlacementSettingsKey = ['giftClip', 'Placements'].join('');
    expect(state.settings.giftClipCrops).toEqual({});
    expect((state.settings as unknown as Record<string, unknown>)[legacyPlacementSettingsKey]).toBeUndefined();
  });
});
