import { decompressFrames, parseGIF, type ParsedFrame } from 'gifuct-js';
import { giftReceiptMediaUrl } from '../../backend';
import type { GiftReceipt } from '../../types';

const DEFAULT_DURATION_MS = 3000;
const MIN_DURATION_MS = 1000;
const MAX_DURATION_MS = 15000;
const MAX_EFFECT_COMPOSITE_DIMENSION = 4096;

export interface GiftEffectLayout {
  videoWidth: number;
  videoHeight: number;
  rgbFrame: [number, number, number, number];
  alphaFrame: [number, number, number, number];
  fps: number;
  frames: number;
}

export interface GiftClipVisual {
  source: CanvasImageSource;
  width: number;
  height: number;
}

export interface GiftClipMediaSession {
  readonly width: number;
  readonly height: number;
  readonly durationMs: number;
  readonly sourceLabel: '完整特效' | '短动画回退' | '短动画';
  readonly avatar: HTMLImageElement | null;
  visualAt(elapsedMs: number): GiftClipVisual | null;
  restart(): Promise<void>;
  pause(): void;
  dispose(): void;
}

interface GiftClipMediaSource {
  readonly width: number;
  readonly height: number;
  readonly durationMs?: number;
  visualAt(elapsedMs: number): GiftClipVisual | null;
  restart(): Promise<void>;
  pause(): void;
}

interface GiftEffectSource extends GiftClipMediaSource {
  video: HTMLVideoElement;
  layout: GiftEffectLayout;
  frame: HTMLCanvasElement;
  color: HTMLCanvasElement;
  alpha: HTMLCanvasElement;
  durationMs: number;
}

class GiftClipMediaResources {
  private readonly images = new Set<HTMLImageElement>();
  private readonly videos = new Set<HTMLVideoElement>();
  private readonly objectURLs = new Set<string>();
  private readonly canvases = new Set<HTMLCanvasElement>();
  private readonly disposeCallbacks = new Set<() => void>();
  private disposed = false;

  ownImage(image: HTMLImageElement): HTMLImageElement {
    this.images.add(image);
    return image;
  }

  ownVideo(video: HTMLVideoElement): HTMLVideoElement {
    this.videos.add(video);
    return video;
  }

  ownObjectURL(url: string): string {
    this.objectURLs.add(url);
    return url;
  }

  ownCanvas(canvas: HTMLCanvasElement): HTMLCanvasElement {
    this.canvases.add(canvas);
    return canvas;
  }

  onDispose(callback: () => void): void {
    this.disposeCallbacks.add(callback);
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    for (const callback of this.disposeCallbacks) callback();
    this.disposeCallbacks.clear();
    for (const video of this.videos) {
      video.pause();
      video.removeAttribute('src');
      video.load();
      video.remove();
    }
    this.videos.clear();
    for (const image of this.images) {
      image.onload = null;
      image.onerror = null;
      image.removeAttribute('src');
      image.remove();
    }
    this.images.clear();
    for (const canvas of this.canvases) {
      canvas.width = 0;
      canvas.height = 0;
      canvas.remove();
    }
    this.canvases.clear();
    for (const objectURL of this.objectURLs) URL.revokeObjectURL(objectURL);
    this.objectURLs.clear();
  }
}

export function normalizeGiftClipDuration(durationMs: number | undefined): number {
  const normalized = Math.round(Number(durationMs) || DEFAULT_DURATION_MS);
  return Math.min(MAX_DURATION_MS, Math.max(MIN_DURATION_MS, normalized));
}

export function normalizeGiftEffectLayout(value: unknown): GiftEffectLayout {
  if (!value || typeof value !== 'object') throw new Error('礼物特效坐标无效。');
  const candidate = value as Partial<GiftEffectLayout>;
  const videoWidth = positiveInteger(candidate.videoWidth);
  const videoHeight = positiveInteger(candidate.videoHeight);
  const fps = positiveInteger(candidate.fps);
  const frames = positiveInteger(candidate.frames);
  const rgbFrame = normalizeEffectFrame(candidate.rgbFrame, videoWidth, videoHeight);
  const alphaFrame = normalizeEffectFrame(candidate.alphaFrame, videoWidth, videoHeight);
  if (
    videoWidth > 8192 || videoHeight > 8192 || fps > 120 || frames > 3600
    || rgbFrame[2] > MAX_EFFECT_COMPOSITE_DIMENSION
    || rgbFrame[3] > MAX_EFFECT_COMPOSITE_DIMENSION
  ) {
    throw new Error('礼物特效坐标超出允许范围。');
  }
  return { videoWidth, videoHeight, rgbFrame, alphaFrame, fps, frames };
}

export function giftEffectVisualSize(layout: GiftEffectLayout): { width: number; height: number } {
  return { width: layout.rgbFrame[2], height: layout.rgbFrame[3] };
}

export function giftEffectDurationMs(layout: GiftEffectLayout): number {
  return normalizeGiftClipDuration(Math.round((layout.frames / layout.fps) * 1000));
}

export function giftGifFrameIndex(delays: readonly number[], elapsedMs: number): number {
  if (delays.length === 0) return -1;
  const normalizedDelays = delays.map((delay) => Math.max(10, Math.round(delay) || 100));
  const cycleMs = normalizedDelays.reduce((total, delay) => total + delay, 0);
  let position = Math.max(0, elapsedMs) % cycleMs;
  for (let index = 0; index < normalizedDelays.length; index += 1) {
    if (position < normalizedDelays[index]) return index;
    position -= normalizedDelays[index];
  }
  return normalizedDelays.length - 1;
}

export async function loadGiftClipMediaSession(
  receipt: GiftReceipt,
  sourceMediaHost: HTMLElement,
): Promise<GiftClipMediaSession> {
  const resources: GiftClipMediaResources[] = [];
  const avatarResources = new GiftClipMediaResources();
  resources.push(avatarResources);
  const disposeResources = (): void => {
    for (const owner of resources) owner.dispose();
  };

  try {
    const avatar = await loadOptionalImage(giftReceiptMediaUrl(receipt.id, 'avatar'), avatarResources);
    let source: GiftClipMediaSource | null = null;
    let sourceLabel: GiftClipMediaSession['sourceLabel'] = '短动画';

    if (receipt.animation?.mp4 && receipt.animation.mp4Json) {
      const effectResources = new GiftClipMediaResources();
      resources.push(effectResources);
      try {
        source = await loadGiftEffect(receipt.id, effectResources, sourceMediaHost);
        sourceLabel = '完整特效';
      } catch (effectError) {
        effectResources.dispose();
        if (!receipt.animation.gif && !receipt.animation.webp) throw effectError;
        sourceLabel = '短动画回退';
      }
    }

    if (!source) {
      const animationResources = new GiftClipMediaResources();
      resources.push(animationResources);
      source = await loadGiftAnimation(
        `${giftReceiptMediaUrl(receipt.id, 'animation')}&v=${Date.now()}`,
        animationResources,
        sourceMediaHost,
      );
    }

    const durationMs = sourceLabel === '完整特效'
      ? normalizeGiftClipDuration(source.durationMs)
      : normalizeGiftClipDuration(receipt.animation?.durationMs || source.durationMs);
    let disposed = false;
    return {
      width: source.width,
      height: source.height,
      durationMs,
      sourceLabel,
      avatar,
      visualAt(elapsedMs) {
        return disposed ? null : source.visualAt(elapsedMs);
      },
      restart() {
        return disposed ? Promise.resolve() : source.restart();
      },
      pause() {
        if (!disposed) source.pause();
      },
      dispose() {
        if (disposed) return;
        disposed = true;
        disposeResources();
      },
    };
  } catch (error) {
    disposeResources();
    throw error;
  }
}

function positiveInteger(value: unknown): number {
  const result = Number(value);
  if (!Number.isInteger(result) || result <= 0) throw new Error('礼物特效坐标无效。');
  return result;
}

function normalizeEffectFrame(value: unknown, videoWidth: number, videoHeight: number): [number, number, number, number] {
  if (!Array.isArray(value) || value.length !== 4) throw new Error('礼物特效坐标无效。');
  const frame = value.map(Number) as [number, number, number, number];
  if (!frame.every(Number.isInteger)) throw new Error('礼物特效坐标无效。');
  const [x, y, width, height] = frame;
  if (x < 0 || y < 0 || width <= 0 || height <= 0 || x + width > videoWidth || y + height > videoHeight) {
    throw new Error('礼物特效坐标无效。');
  }
  return frame;
}

async function loadGiftEffect(
  receiptId: string,
  resources: GiftClipMediaResources,
  sourceMediaHost: HTMLElement,
): Promise<GiftEffectSource> {
  const cacheBuster = `&v=${Date.now()}`;
  const [layoutResponse, videoResponse] = await Promise.all([
    fetch(`${giftReceiptMediaUrl(receiptId, 'effect-layout')}${cacheBuster}`, { cache: 'no-store' }),
    fetch(`${giftReceiptMediaUrl(receiptId, 'effect-video')}${cacheBuster}`, { cache: 'no-store' }),
  ]);
  if (!layoutResponse.ok || !videoResponse.ok) throw new Error('完整礼物特效读取失败。');
  const layout = normalizeGiftEffectLayout(await layoutResponse.json());
  const videoBlob = await videoResponse.blob();
  if (!videoBlob.size) throw new Error('完整礼物特效没有有效视频。');
  const sourceURL = resources.ownObjectURL(URL.createObjectURL(videoBlob));
  const video = resources.ownVideo(document.createElement('video'));
  video.muted = true;
  video.playsInline = true;
  video.preload = 'auto';
  sourceMediaHost.append(video);
  video.src = sourceURL;
  await waitForVideo(video);
  if (video.videoWidth !== layout.videoWidth || video.videoHeight !== layout.videoHeight) {
    throw new Error('礼物特效视频尺寸与坐标不一致。');
  }
  const { width, height } = giftEffectVisualSize(layout);
  const effect: GiftEffectSource = {
    video,
    layout,
    frame: createEffectCanvas(width, height, resources),
    color: createEffectCanvas(width, height, resources),
    alpha: createEffectCanvas(width, height, resources),
    width,
    height,
    durationMs: giftEffectDurationMs(layout),
    visualAt: () => renderGiftEffectFrame(effect),
    async restart() {
      video.currentTime = 0;
      await video.play();
    },
    pause() {
      video.pause();
    },
  };
  return effect;
}

function waitForVideo(video: HTMLVideoElement): Promise<void> {
  return new Promise((resolve, reject) => {
    const cleanup = (): void => {
      video.removeEventListener('loadedmetadata', onLoaded);
      video.removeEventListener('error', onError);
    };
    const onLoaded = (): void => {
      cleanup();
      resolve();
    };
    const onError = (): void => {
      cleanup();
      reject(new Error('完整礼物特效视频无法解码。'));
    };
    video.addEventListener('loadedmetadata', onLoaded);
    video.addEventListener('error', onError);
    video.load();
  });
}

function createEffectCanvas(
  width: number,
  height: number,
  resources: GiftClipMediaResources,
): HTMLCanvasElement {
  const canvas = resources.ownCanvas(document.createElement('canvas'));
  canvas.width = width;
  canvas.height = height;
  return canvas;
}

function renderGiftEffectFrame(effect: GiftEffectSource): GiftClipVisual | null {
  if (effect.video.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) return null;
  const colorContext = effect.color.getContext('2d', { willReadFrequently: true });
  const alphaContext = effect.alpha.getContext('2d', { willReadFrequently: true });
  const frameContext = effect.frame.getContext('2d');
  if (!colorContext || !alphaContext || !frameContext) throw new Error('礼物特效画布初始化失败。');
  const [rgbX, rgbY, rgbWidth, rgbHeight] = effect.layout.rgbFrame;
  const [alphaX, alphaY, alphaWidth, alphaHeight] = effect.layout.alphaFrame;
  const { width, height } = effect;
  colorContext.clearRect(0, 0, width, height);
  alphaContext.clearRect(0, 0, width, height);
  colorContext.drawImage(effect.video, rgbX, rgbY, rgbWidth, rgbHeight, 0, 0, width, height);
  alphaContext.drawImage(effect.video, alphaX, alphaY, alphaWidth, alphaHeight, 0, 0, width, height);
  const colorPixels = colorContext.getImageData(0, 0, width, height);
  const alphaPixels = alphaContext.getImageData(0, 0, width, height);
  for (let index = 0; index < colorPixels.data.length; index += 4) {
    colorPixels.data[index + 3] = alphaPixels.data[index];
  }
  frameContext.clearRect(0, 0, width, height);
  frameContext.putImageData(colorPixels, 0, 0);
  return { source: effect.frame, width, height };
}

async function loadGiftAnimation(
  src: string,
  resources: GiftClipMediaResources,
  sourceMediaHost: HTMLElement,
): Promise<GiftClipMediaSource> {
  const response = await fetch(src, { cache: 'no-store' });
  if (!response.ok) throw new Error('礼物动画素材读取失败，请稍后重试。');
  const blob = await response.blob();
  if (!blob.size) throw new Error('礼物动画素材没有有效内容。');
  const data = await blob.arrayBuffer();
  if (isGIFData(data)) return createGIFAnimationSource(data, resources);

  const sourceURL = resources.ownObjectURL(URL.createObjectURL(blob));
  const image = await loadAnimatedImage(sourceURL, resources, sourceMediaHost);
  return createAnimatedImageSource(image, sourceURL, resources);
}

function isGIFData(data: ArrayBuffer): boolean {
  if (data.byteLength < 6) return false;
  const signature = String.fromCharCode(...new Uint8Array(data, 0, 6));
  return signature === 'GIF87a' || signature === 'GIF89a';
}

function createGIFAnimationSource(
  data: ArrayBuffer,
  resources: GiftClipMediaResources,
): GiftClipMediaSource {
  const parsed = parseGIF(data);
  const width = parsed.lsd.width;
  const height = parsed.lsd.height;
  const frameDimensions = parsed.frames.flatMap((frame) => (
    'image' in frame ? [frame.image.descriptor] : []
  ));
  if (
    width <= 0 || height <= 0 || width > 2048 || height > 2048
    || width * height > 4_194_304 || frameDimensions.length === 0 || frameDimensions.length > 1200
  ) {
    throw new Error('礼物 GIF 动画尺寸或帧数超出允许范围。');
  }
  let totalFramePixels = 0;
  for (const dimensions of frameDimensions) {
    validateGIFFrameDimensions(dimensions, width, height);
    totalFramePixels += dimensions.width * dimensions.height;
    if (totalFramePixels > 33_554_432) throw new Error('礼物 GIF 动画解码后体积过大。');
  }
  const frames = decompressFrames(parsed, true);
  for (const frame of frames) validateGIFFramePatch(frame);

  const canvas = createEffectCanvas(width, height, resources);
  const context = canvas.getContext('2d');
  const patchCanvas = createEffectCanvas(1, 1, resources);
  const patchContext = patchCanvas.getContext('2d');
  if (!context || !patchContext) throw new Error('礼物 GIF 动画画布初始化失败。');
  const delays = frames.map((frame) => Math.max(10, Math.round(frame.delay) || 100));
  const cycleMs = delays.reduce((total, delay) => total + delay, 0);
  let currentFrame = -1;
  let currentCycle = -1;
  let restoreSnapshot: ImageData | null = null;

  const reset = (): void => {
    context.clearRect(0, 0, width, height);
    currentFrame = -1;
    currentCycle = -1;
    restoreSnapshot = null;
  };
  const drawFrame = (frameIndex: number): void => {
    if (currentFrame >= 0) {
      const previous = frames[currentFrame];
      if (previous.disposalType === 2) {
        context.clearRect(previous.dims.left, previous.dims.top, previous.dims.width, previous.dims.height);
      } else if (previous.disposalType === 3 && restoreSnapshot) {
        context.putImageData(restoreSnapshot, 0, 0);
      }
      restoreSnapshot = null;
    }

    const frame = frames[frameIndex];
    if (frame.disposalType === 3) restoreSnapshot = context.getImageData(0, 0, width, height);
    patchCanvas.width = frame.dims.width;
    patchCanvas.height = frame.dims.height;
    const patchPixels = new Uint8ClampedArray(frame.patch.length);
    patchPixels.set(frame.patch);
    const patch = new ImageData(patchPixels, frame.dims.width, frame.dims.height);
    patchContext.putImageData(patch, 0, 0);
    context.drawImage(patchCanvas, frame.dims.left, frame.dims.top);
    currentFrame = frameIndex;
  };

  return {
    width,
    height,
    durationMs: cycleMs,
    visualAt: (elapsedMs) => {
      const normalizedElapsed = Math.max(0, elapsedMs);
      const cycle = Math.floor(normalizedElapsed / cycleMs);
      const targetFrame = giftGifFrameIndex(delays, normalizedElapsed);
      if (cycle !== currentCycle || targetFrame < currentFrame) {
        reset();
        currentCycle = cycle;
      }
      for (let index = currentFrame + 1; index <= targetFrame; index += 1) drawFrame(index);
      return { source: canvas, width, height };
    },
    restart: async () => reset(),
    pause: () => undefined,
  };
}

function validateGIFFrameDimensions(
  dimensions: { left: number; top: number; width: number; height: number },
  width: number,
  height: number,
): void {
  const { left, top, width: frameWidth, height: frameHeight } = dimensions;
  if (
    left < 0 || top < 0 || frameWidth <= 0 || frameHeight <= 0
    || left + frameWidth > width || top + frameHeight > height
  ) {
    throw new Error('礼物 GIF 动画帧无效。');
  }
}

function validateGIFFramePatch(frame: ParsedFrame): void {
  if (frame.patch.length !== frame.dims.width * frame.dims.height * 4) {
    throw new Error('礼物 GIF 动画帧无效。');
  }
}

function imageGiftClipVisual(animation: HTMLImageElement | null): GiftClipVisual | null {
  if (!animation?.naturalWidth || !animation.naturalHeight) return null;
  return { source: animation, width: animation.naturalWidth, height: animation.naturalHeight };
}

function loadImage(src: string, resources: GiftClipMediaResources): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = resources.ownImage(new Image());
    image.decoding = 'async';
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error('礼物动画素材读取失败，请稍后重试。'));
    image.src = src;
  });
}

function loadAnimatedImage(
  src: string,
  resources: GiftClipMediaResources,
  sourceMediaHost: HTMLElement,
): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = resources.ownImage(new Image());
    image.className = 'gift-clip-source-animation';
    image.decoding = 'auto';
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error('礼物动画素材读取失败，请稍后重试。'));
    // Chromium advances animated GIF/WebP only while the image remains rendered.
    sourceMediaHost.append(image);
    image.src = src;
  });
}

function createAnimatedImageSource(
  image: HTMLImageElement,
  src: string,
  resources: GiftClipMediaResources,
): GiftClipMediaSource {
  let restartPromise: Promise<void> | null = null;
  let rejectRestart: ((reason: Error) => void) | null = null;

  resources.onDispose(() => {
    if (!rejectRestart) return;
    const reject = rejectRestart;
    restartPromise = null;
    rejectRestart = null;
    image.onload = null;
    image.onerror = null;
    reject(new Error('礼物动画会话已释放。'));
  });

  const restart = (): Promise<void> => {
    if (restartPromise) return restartPromise;
    let resolvePending!: () => void;
    let rejectPending!: (reason: Error) => void;
    const pending = new Promise<void>((resolve, reject) => {
      resolvePending = resolve;
      rejectPending = reject;
    });
    restartPromise = pending;
    rejectRestart = rejectPending;

    const settle = (error?: Error): void => {
      if (restartPromise !== pending) return;
      restartPromise = null;
      rejectRestart = null;
      image.onload = null;
      image.onerror = null;
      if (error) rejectPending(error);
      else resolvePending();
    };
    image.onload = () => settle();
    image.onerror = () => settle(new Error('礼物动画素材读取失败，请稍后重试。'));
    image.removeAttribute('src');
    image.src = src;
    return pending;
  };

  return {
    width: image.naturalWidth,
    height: image.naturalHeight,
    visualAt: () => imageGiftClipVisual(image),
    restart,
    pause: () => undefined,
  };
}

async function loadOptionalImage(
  src: string,
  resources: GiftClipMediaResources,
): Promise<HTMLImageElement | null> {
  try {
    return await loadImage(src, resources);
  } catch {
    return null;
  }
}
