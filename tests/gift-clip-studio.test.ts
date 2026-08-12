import { readFileSync } from 'node:fs';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { defaultState } from '../src/storage';
import type { GiftReceipt } from '../src/types';
import type { GiftClipMediaSession } from '../src/ui/config/gift-clip-media';

const studioMocks = vi.hoisted(() => ({
  loadMediaSession: vi.fn(),
  createLayers: vi.fn(),
  createJob: vi.fn(),
  waitForJob: vi.fn(),
  cancelJob: vi.fn(),
  triggerDownload: vi.fn(),
}));

const firstJobID = 'AQIDBAUGBwgJCgsMDQ4PEBES';
const secondJobID = 'ZYXWVUTSRQPONMLKJIHGFEDC';

vi.mock('../src/ui/config/gift-clip-media', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/ui/config/gift-clip-media')>();
  return { ...actual, loadGiftClipMediaSession: studioMocks.loadMediaSession };
});

vi.mock('../src/ui/config/gift-clip-export-api', () => ({
  createGiftClipJob: studioMocks.createJob,
  waitForGiftClipJob: studioMocks.waitForJob,
  cancelGiftClipJob: studioMocks.cancelJob,
  giftClipJobVideoURL: (id: string) => `/api/gift-clips/${id}/video`,
}));

vi.mock('../src/ui/config/gift-clip-export-layers', () => ({
  createGiftClipExportLayers: studioMocks.createLayers,
}));

vi.mock('../src/ui/config/gift-clip-download', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/ui/config/gift-clip-download')>();
  return { ...actual, triggerGiftClipDownload: studioMocks.triggerDownload };
});

import {
  giftClipAnimationKey,
  openGiftClipStudio,
  type GiftClipStudioController,
} from '../src/ui/config/gift-clip-studio';
import { normalizeGiftClipDuration } from '../src/ui/config/gift-clip-media';
import { sanitizeGiftClipFilename } from '../src/ui/config/gift-clip-download';

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

function jobSnapshot(overrides: Record<string, unknown> = {}) {
  return {
    id: firstJobID,
    state: 'queued',
    progress: 0,
    output: { width: 640, height: 360, fps: 30 },
    ...overrides,
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
    studioMocks.createLayers.mockReset();
    studioMocks.createJob.mockReset();
    studioMocks.waitForJob.mockReset();
    studioMocks.cancelJob.mockReset();
    studioMocks.triggerDownload.mockReset();
    studioMocks.createLayers.mockResolvedValue({
      background: new Blob(['background'], { type: 'image/png' }),
      overlay: new Blob(['overlay'], { type: 'image/png' }),
    });
    studioMocks.cancelJob.mockResolvedValue(undefined);
    vi.stubGlobal('document', {
      createElement: (tagName: string) => new StudioTestElement(tagName),
      body: new StudioTestElement('body'),
    } as unknown as Document);
    vi.stubGlobal('ResizeObserver', StudioTestResizeObserver as unknown as typeof ResizeObserver);
    vi.stubGlobal('requestAnimationFrame', requestFrame);
    vi.stubGlobal('cancelAnimationFrame', vi.fn());
    vi.stubGlobal('addEventListener', vi.fn());
    vi.stubGlobal('removeEventListener', removeGlobalListener);
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

  it('keeps the public studio module as a small stable facade', () => {
    const studioSource = readFileSync(new URL('../src/ui/config/gift-clip-studio.ts', import.meta.url), 'utf8');

    expect(studioSource.split(/\r?\n/).length).toBeLessThanOrEqual(20);
    expect(studioSource).toContain("from './gift-clip-animation-key'");
    expect(studioSource).toContain("from './gift-clip-studio-controller'");
  });

  it('keeps DOM rendering writes behind the studio view boundary', () => {
    const controllerSource = readFileSync(
      new URL('../src/ui/config/gift-clip-studio-controller.ts', import.meta.url),
      'utf8',
    );

    expect(controllerSource).not.toMatch(/\.(?:hidden|disabled|textContent|src|style\.aspectRatio)\s*=/);
    expect(controllerSource).not.toContain("removeAttribute('src')");
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

  it('shows the exact small-source gate, disposes it immediately, and retries with a fresh session', async () => {
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
    expect(first.dispose).toHaveBeenCalledOnce();

    retry.onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(second.dispose).toHaveBeenCalledOnce());

    expect(first.dispose).toHaveBeenCalledOnce();
    controller.close();
    expect(second.dispose).toHaveBeenCalledOnce();
  });

  it('disposes a session whose editor restart rejects and retries with a fresh session', async () => {
    const first = mediaSessionFixture();
    first.restart = vi.fn(async () => { throw new Error('礼物动画素材读取失败，请稍后重试。'); });
    const second = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValueOnce(first).mockResolvedValueOnce(second);
    const host = new StudioTestElement('host');
    const controller = openStudio({
      host: host as unknown as HTMLElement,
      receipt: receiptFixture(),
    });
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-status')?.textContent)
      .toBe('礼物动画素材读取失败，请稍后重试。'));

    const sourceCanvas = host.querySelector('.gift-clip-canvas');
    expect(first.dispose).toHaveBeenCalledOnce();
    expect({ width: sourceCanvas?.width, height: sourceCanvas?.height }).toEqual({ width: 0, height: 0 });

    button(host, '重试').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-crop-layer')).not.toBeNull());
    expect(studioMocks.loadMediaSession).toHaveBeenCalledTimes(2);
    expect(second.dispose).not.toHaveBeenCalled();
    expect({ width: sourceCanvas?.width, height: sourceCanvas?.height }).toEqual({ width: 640, height: 360 });

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

  it('creates static layers, follows job snapshots, and previews the HTTP MP4', async () => {
    const session = mediaSessionFixture();
    const receipt = receiptFixture();
    const events: string[] = [];
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockImplementation(async (_id: string, options: { onSnapshot: (value: unknown) => void }) => {
      for (const state of ['queued', 'encoding', 'retrying', 'ready'] as const) {
        options.onSnapshot(jobSnapshot({ state, progress: state === 'encoding' ? .5 : state === 'ready' ? 1 : 0 }));
      }
      return jobSnapshot({ state: 'ready', progress: 1 });
    });
    const host = new StudioTestElement('host');
    const controller = openStudio({
      host: host as unknown as HTMLElement,
      receipt,
      onCropConfirmed: () => { events.push('confirm'); },
    });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));

    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '保存 MP4').hidden).toBe(false));

    expect(events).toEqual(['confirm']);
    expect(studioMocks.createLayers).toHaveBeenCalledWith(expect.objectContaining({
      width: 640, height: 360, receipt, avatar: null, document: expect.anything(),
    }));
    expect(studioMocks.createJob).toHaveBeenCalledWith(expect.objectContaining({
      receiptId: 'receipt-1', crop: { x: 0, y: 0, width: 1, height: 1 },
      background: expect.any(Blob), overlay: expect.any(Blob),
    }), expect.any(AbortSignal));
    expect(host.querySelector('.gift-clip-video')).toEqual(expect.objectContaining({
      src: `/api/gift-clips/${firstJobID}/video`, hidden: false,
    }));
    expect(host.querySelector('.gift-clip-video')?.style.aspectRatio).toBe('640 / 360');
    controller.close();
  });

  it('shows the compatibility retry message and silently cancels an in-flight job on close', async () => {
    const session = mediaSessionFixture();
    let signal: AbortSignal | undefined;
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockImplementation((_id: string, options: { signal: AbortSignal; onSnapshot: (value: unknown) => void }) => {
      signal = options.signal;
      options.onSnapshot(jobSnapshot({ state: 'retrying', message: '已切换兼容编码模式。' }));
      return new Promise(() => undefined);
    });
    const host = new StudioTestElement('host');
    const onError = vi.fn();
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture(), onError });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-status')?.textContent).toBe('已切换兼容编码模式。'));

    controller.close();
    await vi.waitFor(() => expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID));
    expect(signal?.aborted).toBe(true);
    expect(onError).not.toHaveBeenCalled();
  });

  it('tears down synchronously on close while DELETE never settles and ignores later snapshots', async () => {
    const session = mediaSessionFixture();
    let signal: AbortSignal | undefined;
    let reportSnapshot: ((snapshot: ReturnType<typeof jobSnapshot>) => void) | undefined;
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockImplementation((_id: string, options: {
      signal: AbortSignal;
      onSnapshot: (snapshot: ReturnType<typeof jobSnapshot>) => void;
    }) => {
      signal = options.signal;
      reportSnapshot = options.onSnapshot;
      return new Promise(() => undefined);
    });
    studioMocks.cancelJob.mockReturnValue(new Promise<void>(() => undefined));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(studioMocks.waitForJob).toHaveBeenCalledOnce());
    const overlay = host.children[0];
    const status = host.querySelector('.gift-clip-status')!;
    const textAtClose = status.textContent;

    controller.close();

    expect(signal?.aborted).toBe(true);
    expect(host.children).toEqual([]);
    expect(overlay.removeCalls).toBe(1);
    expect(studioMocks.cancelJob).toHaveBeenCalledTimes(1);
    expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID);
    reportSnapshot?.(jobSnapshot({ state: 'ready', progress: 1 }));
    await Promise.resolve();
    expect(status.textContent).toBe(textAtClose);
    expect(host.children).toEqual([]);
  });

  it('restores the editor immediately on re-edit while DELETE never settles', async () => {
    const session = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockResolvedValue(jobSnapshot({ state: 'ready', progress: 1 }));
    studioMocks.cancelJob.mockReturnValue(new Promise<void>(() => undefined));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '保存 MP4').hidden).toBe(false));

    button(host, '重新剪裁').onclick?.({} as MouseEvent);

    await vi.waitFor(() => expect(host.querySelector('.gift-clip-crop-layer')).not.toBeNull());
    expect(studioMocks.cancelJob).toHaveBeenCalledTimes(1);
    expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID);
    expect(studioMocks.loadMediaSession).toHaveBeenCalledOnce();
    expect(session.dispose).not.toHaveBeenCalled();
    controller.close();
  });

  it('shows a polling failure immediately even while its best-effort DELETE never settles', async () => {
    const session = mediaSessionFixture();
    const deleteNeverSettles = new Promise<void>(() => undefined);
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockRejectedValue(new Error('视频导出失败，请重试。'));
    studioMocks.cancelJob.mockReturnValue(deleteNeverSettles);
    const host = new StudioTestElement('host');
    const onError = vi.fn();
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture(), onError });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);

    await vi.waitFor(() => expect(host.querySelector('.gift-clip-status')?.textContent).toBe('视频导出失败，请重试。'));
    expect(button(host, '重试').hidden).toBe(false);
    expect(onError).toHaveBeenCalledWith('视频导出失败，请重试。');
    expect(studioMocks.cancelJob).toHaveBeenCalledTimes(1);
    expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID);
    controller.close();
  });

  it('does not DELETE a failed job again when the failed studio closes', async () => {
    const session = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockRejectedValue(new Error('视频导出失败，请重试。'));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-status')?.textContent).toBe('视频导出失败，请重试。'));
    expect(studioMocks.cancelJob).toHaveBeenCalledTimes(1);
    expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID);

    controller.close();
    await Promise.resolve();

    expect(studioMocks.cancelJob).toHaveBeenCalledTimes(1);
  });

  it('owns and swallows a rejected best-effort DELETE', async () => {
    const session = mediaSessionFixture();
    const unhandled = vi.fn();
    process.on('unhandledRejection', unhandled);
    try {
      studioMocks.loadMediaSession.mockResolvedValue(session);
      studioMocks.createJob.mockResolvedValue(jobSnapshot());
      studioMocks.waitForJob.mockResolvedValue(jobSnapshot({ state: 'ready', progress: 1 }));
      studioMocks.cancelJob.mockRejectedValue(new Error('DELETE failed'));
      const host = new StudioTestElement('host');
      const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
      await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
      button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
      await vi.waitFor(() => expect(button(host, '保存 MP4').hidden).toBe(false));

      controller.close();
      await Promise.resolve();
      await Promise.resolve();
      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(studioMocks.cancelJob).toHaveBeenCalledTimes(1);
      expect(unhandled).not.toHaveBeenCalled();
      expect(host.children).toEqual([]);
    } finally {
      process.off('unhandledRejection', unhandled);
    }
  });

  it('shows a create failure and retry without waiting for unrelated cleanup', async () => {
    const session = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockRejectedValue(new Error('视频导出创建失败，请重试。'));
    const host = new StudioTestElement('host');
    const onError = vi.fn();
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture(), onError });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);

    await vi.waitFor(() => expect(host.querySelector('.gift-clip-status')?.textContent).toBe('视频导出创建失败，请重试。'));
    expect(button(host, '重试').hidden).toBe(false);
    expect(onError).toHaveBeenCalledWith('视频导出创建失败，请重试。');
    expect(studioMocks.cancelJob).not.toHaveBeenCalled();
    controller.close();
  });

  it('uses the create response ID for preview, download, and cancellation when wait resolves another ID', async () => {
    const session = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot({ id: firstJobID }));
    studioMocks.waitForJob.mockResolvedValue(jobSnapshot({ id: secondJobID, state: 'ready', progress: 1 }));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '保存 MP4').hidden).toBe(false));

    expect(host.querySelector('.gift-clip-video')?.src).toBe(`/api/gift-clips/${firstJobID}/video`);
    button(host, '保存 MP4').onclick?.({} as MouseEvent);
    expect(studioMocks.triggerDownload).toHaveBeenCalledWith(
      `/api/gift-clips/${firstJobID}/video`,
      sanitizeGiftClipFilename(receiptFixture()),
    );
    expect(studioMocks.triggerDownload.mock.calls[0][1]).toMatch(/^测试礼物-测试观众-.+\.mp4$/);
    controller.close();
    await vi.waitFor(() => expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID));
    expect(studioMocks.cancelJob).toHaveBeenCalledTimes(1);
  });

  it('aborts an unresolved create and deletes the exact job returned after close', async () => {
    const session = mediaSessionFixture();
    let signal: AbortSignal | undefined;
    let resolveCreate: ((value: unknown) => void) | undefined;
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockImplementation((_input: unknown, activeSignal: AbortSignal) => {
      signal = activeSignal;
      return new Promise((resolve) => { resolveCreate = resolve; });
    });
    const host = new StudioTestElement('host');
    const onError = vi.fn();
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture(), onError });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(studioMocks.createJob).toHaveBeenCalledOnce());

    controller.close();
    expect(signal?.aborted).toBe(true);
    resolveCreate?.(jobSnapshot());
    await vi.waitFor(() => expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID));
    expect(onError).not.toHaveBeenCalled();
  });

  it('deletes the ready job before re-editing without reloading its media session', async () => {
    const session = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockResolvedValue(jobSnapshot({ state: 'ready', progress: 1 }));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '保存 MP4').hidden).toBe(false));

    button(host, '重新剪裁').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID));
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-crop-layer')).not.toBeNull());
    expect(studioMocks.loadMediaSession).toHaveBeenCalledOnce();
    expect(session.dispose).not.toHaveBeenCalled();
    controller.close();
  });

  it('calls onCropConfirmed exactly once for each explicit confirmation', async () => {
    const session = mediaSessionFixture();
    const onCropConfirmed = vi.fn();
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockResolvedValue(jobSnapshot({ state: 'ready', progress: 1 }));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture(), onCropConfirmed });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '保存 MP4').hidden).toBe(false));
    button(host, '重新剪裁').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(onCropConfirmed).toHaveBeenCalledTimes(2));
    expect(onCropConfirmed.mock.calls).toEqual([
      [{ x: 0, y: 0, width: 1, height: 1 }],
      [{ x: 0, y: 0, width: 1, height: 1 }],
    ]);
    controller.close();
  });

  it('retries a failed job with a fresh source load and reports its stable error', async () => {
    const first = mediaSessionFixture();
    const second = mediaSessionFixture();
    studioMocks.loadMediaSession.mockResolvedValueOnce(first).mockResolvedValueOnce(second);
    studioMocks.createJob.mockResolvedValue(jobSnapshot());
    studioMocks.waitForJob.mockRejectedValueOnce(new Error('视频导出失败，请重试。'));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-status')?.textContent).toBe('视频导出失败，请重试。'));

    button(host, '重试').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-crop-layer')).not.toBeNull());
    expect(first.dispose).toHaveBeenCalledOnce();
    expect(studioMocks.loadMediaSession).toHaveBeenCalledTimes(2);
    controller.close();
  });

  it('deletes a late-created stale job once without disturbing the newer ready export', async () => {
    const session = mediaSessionFixture();
    let finishOldCreate: ((value: ReturnType<typeof jobSnapshot>) => void) | undefined;
    studioMocks.loadMediaSession.mockResolvedValue(session);
    studioMocks.createJob
      .mockImplementationOnce(() => new Promise((resolve) => { finishOldCreate = resolve; }))
      .mockResolvedValueOnce(jobSnapshot({ id: secondJobID }));
    studioMocks.waitForJob.mockResolvedValue(jobSnapshot({ id: secondJobID, state: 'ready', progress: 1 }));
    const host = new StudioTestElement('host');
    const controller = openStudio({ host: host as unknown as HTMLElement, receipt: receiptFixture() });
    await vi.waitFor(() => expect(button(host, '确定剪裁并生成').hidden).toBe(false));
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(studioMocks.createJob).toHaveBeenCalledOnce());
    button(host, '重新剪裁').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(host.querySelector('.gift-clip-crop-layer')).not.toBeNull());
    button(host, '确定剪裁并生成').onclick?.({} as MouseEvent);
    await vi.waitFor(() => expect(button(host, '保存 MP4').hidden).toBe(false));

    finishOldCreate?.(jobSnapshot({ id: firstJobID }));
    await vi.waitFor(() => expect(studioMocks.cancelJob).toHaveBeenCalledWith(firstJobID));
    expect(host.querySelector('.gift-clip-video')?.src).toBe(`/api/gift-clips/${secondJobID}/video`);
    button(host, '保存 MP4').onclick?.({} as MouseEvent);
    expect(studioMocks.triggerDownload).toHaveBeenCalledWith(
      `/api/gift-clips/${secondJobID}/video`,
      sanitizeGiftClipFilename(receiptFixture()),
    );
    expect(studioMocks.cancelJob.mock.calls.filter(([id]) => id === firstJobID)).toHaveLength(1);

    controller.close();
    await vi.waitFor(() => expect(studioMocks.cancelJob).toHaveBeenCalledWith(secondJobID));
    expect(studioMocks.cancelJob.mock.calls.filter(([id]) => id === firstJobID)).toHaveLength(1);
    expect(studioMocks.cancelJob.mock.calls.filter(([id]) => id === secondJobID)).toHaveLength(1);
  });

  it('contains no recorder surface while retaining editor RAFs', () => {
    expect(document.createElement('canvas').className).not.toContain('gift-clip-recording-canvas');
    for (const path of ['gift-clip-studio-controller.ts', 'gift-clip-studio-view.ts']) {
      const source = readFileSync(new URL(`../src/ui/config/${path}`, import.meta.url), 'utf8');
      expect(source).not.toMatch(/MediaRecorder|captureStream|requestAnimationFrame\(draw.*record/i);
    }
  });

  it('drops the legacy placement field after the crop cutover', () => {
    const state = defaultState();
    const legacyPlacementSettingsKey = ['giftClip', 'Placements'].join('');
    expect(state.settings.giftClipCrops).toEqual({});
    expect((state.settings as unknown as Record<string, unknown>)[legacyPlacementSettingsKey]).toBeUndefined();
  });
});
