import type { GiftReceipt } from '../../types';
import { giftReceiptMediaUrl } from '../../backend';
import { el } from '../common';

const CLIP_SIZE = 480;
const CLIP_FPS = 30;
const DEFAULT_DURATION_MS = 3000;
const MIN_DURATION_MS = 1000;
const MAX_DURATION_MS = 15000;

export interface GiftClipStudioController {
  close: () => void;
}

interface GiftClipStudioOptions {
  host: HTMLElement;
  receipt: GiftReceipt;
  onError?: (message: string) => void;
}

interface RecorderSelection {
  recorder: MediaRecorder;
  mimeType: string;
  extension: 'mp4' | 'webm';
}

export interface GiftEffectLayout {
  videoWidth: number;
  videoHeight: number;
  rgbFrame: [number, number, number, number];
  alphaFrame: [number, number, number, number];
  fps: number;
  frames: number;
}

interface GiftEffectSource {
  video: HTMLVideoElement;
  layout: GiftEffectLayout;
  frame: HTMLCanvasElement;
  color: HTMLCanvasElement;
  alpha: HTMLCanvasElement;
  durationMs: number;
}

interface GiftClipVisual {
  source: CanvasImageSource;
  width: number;
  height: number;
}

const RECORDER_FORMATS = [
  { mimeType: 'video/mp4;codecs=avc1.42E01E', extension: 'mp4' as const },
  { mimeType: 'video/mp4', extension: 'mp4' as const },
  { mimeType: 'video/webm;codecs=vp9', extension: 'webm' as const },
  { mimeType: 'video/webm;codecs=vp8', extension: 'webm' as const },
  { mimeType: 'video/webm', extension: 'webm' as const },
];

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
  if (videoWidth > 8192 || videoHeight > 8192 || fps > 120 || frames > 3600) {
    throw new Error('礼物特效坐标超出允许范围。');
  }
  return { videoWidth, videoHeight, rgbFrame, alphaFrame, fps, frames };
}

export function giftEffectDurationMs(layout: GiftEffectLayout): number {
  return normalizeGiftClipDuration(Math.round((layout.frames / layout.fps) * 1000));
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

export function sanitizeGiftClipFilename(receipt: Pick<GiftReceipt, 'giftName' | 'uname' | 'time'>, extension: 'mp4' | 'webm'): string {
  const timestamp = new Date(receipt.time < 1_000_000_000_000 ? receipt.time * 1000 : receipt.time);
  const date = Number.isNaN(timestamp.getTime())
    ? 'unknown-time'
    : [
      timestamp.getFullYear(),
      String(timestamp.getMonth() + 1).padStart(2, '0'),
      String(timestamp.getDate()).padStart(2, '0'),
      '-',
      String(timestamp.getHours()).padStart(2, '0'),
      String(timestamp.getMinutes()).padStart(2, '0'),
      String(timestamp.getSeconds()).padStart(2, '0'),
    ].join('');
  const safe = `${receipt.giftName || '礼物'}-${receipt.uname || '观众'}`
    .replace(/[\\/:*?"<>|\u0000-\u001f]/g, '-')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 72) || '礼物回放';
  return `${safe}-${date}.${extension}`;
}

export function selectGiftClipRecorder(
  stream: MediaStream,
  Recorder: typeof MediaRecorder | undefined = globalThis.MediaRecorder,
): RecorderSelection {
  if (typeof Recorder !== 'function') {
    throw new Error('当前浏览器不支持录制 Canvas，请更新程序后重试。');
  }
  for (const format of RECORDER_FORMATS) {
    if (typeof Recorder.isTypeSupported === 'function' && !Recorder.isTypeSupported(format.mimeType)) continue;
    try {
      return {
        recorder: new Recorder(stream, { mimeType: format.mimeType, videoBitsPerSecond: 4_000_000 }),
        mimeType: format.mimeType,
        extension: format.extension,
      };
    } catch {
      // Some Chromium builds report MP4 support but still reject construction.
    }
  }
  try {
    const recorder = new Recorder(stream, { videoBitsPerSecond: 4_000_000 });
    const mimeType = recorder.mimeType || 'video/webm';
    return { recorder, mimeType, extension: mimeType.includes('mp4') ? 'mp4' : 'webm' };
  } catch {
    throw new Error('当前浏览器不支持录制 Canvas，请更新程序后重试。');
  }
}

export function stopGiftClipStream(stream: Pick<MediaStream, 'getTracks'> | null): void {
  stream?.getTracks().forEach((track) => track.stop());
}

export function triggerGiftClipDownload(url: string, filename: string, targetDocument: Document = document): void {
  const link = targetDocument.createElement('a');
  link.href = url;
  link.download = filename;
  targetDocument.body.append(link);
  link.click();
  link.remove();
}

export function openGiftClipStudio(options: GiftClipStudioOptions): GiftClipStudioController {
  const { host, receipt } = options;
  let disposed = false;
  let animationFrame = 0;
  let previewURL = '';
  let activeStream: MediaStream | null = null;
  let generatedBlob: Blob | null = null;
  let generatedExtension: 'mp4' | 'webm' = 'webm';
  let activeEffect: GiftEffectSource | null = null;
  const loadedImages = new Set<HTMLImageElement>();
  const sourceURLs = new Set<string>();

  const overlay = el('div', { class: 'overlay gift-clip-overlay', role: 'dialog', ariaModal: 'true', ariaLabel: '制作礼物动画回放' });
  const dialog = el('section', { class: 'card gift-clip-dialog' });
  const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭' }) as HTMLButtonElement;
  const canvas = el('canvas', { class: 'gift-clip-canvas', width: CLIP_SIZE, height: CLIP_SIZE }) as HTMLCanvasElement;
  const preview = el('video', { class: 'gift-clip-video', controls: true, loop: true, muted: true, playsInline: true }) as HTMLVideoElement;
  preview.hidden = true;
  const progress = el('progress', { class: 'gift-clip-progress', max: 100, value: 0 }) as HTMLProgressElement;
  const status = el('p', { class: 'gift-clip-status', text: '正在准备礼物动画…' });
  const retryButton = el('button', { class: 'btn ghost', type: 'button', text: '重新生成' }) as HTMLButtonElement;
  const saveButton = el('button', { class: 'btn primary', type: 'button', text: '保存视频' }) as HTMLButtonElement;
  retryButton.disabled = true;
  saveButton.disabled = true;

  const disposeMedia = (): void => {
    if (animationFrame) cancelAnimationFrame(animationFrame);
    animationFrame = 0;
    stopGiftClipStream(activeStream);
    activeStream = null;
    if (activeEffect) {
      activeEffect.video.pause();
      activeEffect.video.removeAttribute('src');
      activeEffect.video.load();
      activeEffect = null;
    }
    for (const image of loadedImages) image.src = '';
    loadedImages.clear();
    for (const sourceURL of sourceURLs) URL.revokeObjectURL(sourceURL);
    sourceURLs.clear();
    if (previewURL) URL.revokeObjectURL(previewURL);
    previewURL = '';
    generatedBlob = null;
    preview.removeAttribute('src');
    preview.load();
  };

  const close = (): void => {
    if (disposed) return;
    disposed = true;
    disposeMedia();
    globalThis.removeEventListener('keydown', onKeyDown);
    overlay.remove();
  };
  const onKeyDown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') close();
  };
  closeButton.onclick = close;
  overlay.onclick = (event) => {
    if (event.target === overlay) close();
  };
  globalThis.addEventListener('keydown', onKeyDown);

  dialog.append(
    el('header', { class: 'gift-clip-header' }, [
      el('div', {}, [
        el('span', { class: 'section-kicker', text: '礼物动画回放' }),
        el('h2', { text: `${receipt.giftName || '礼物'} × ${Math.max(1, receipt.num || 1)}` }),
        el('p', { text: '480 × 480 · 成片仅显示昵称，不包含 UID；关闭后不会保留视频。' }),
      ]),
      closeButton,
    ]),
    el('div', { class: 'gift-clip-body' }, [
      el('div', { class: 'gift-clip-stage' }, [canvas, preview]),
      el('div', { class: 'gift-clip-meta' }, [
        status,
        progress,
        el('div', { class: 'gift-clip-actions' }, [retryButton, saveButton]),
      ]),
    ]),
  );
  overlay.append(dialog);
  host.append(overlay);

  const generate = async (): Promise<void> => {
    disposeMedia();
    if (disposed) return;
    preview.hidden = true;
    canvas.hidden = false;
    retryButton.disabled = true;
    saveButton.disabled = true;
    progress.value = 0;
    status.textContent = '正在读取礼物动画…';
    try {
      const context = canvas.getContext('2d');
      if (!context || typeof canvas.captureStream !== 'function') throw new Error('当前浏览器不能录制画布。');
      const avatar = await loadOptionalImage(giftReceiptMediaUrl(receipt.id, 'avatar'), loadedImages);
      if (disposed) return;
      let animation: HTMLImageElement | null = null;
      let usingShortAnimationFallback = false;
      if (receipt.animation?.mp4 && receipt.animation.mp4Json) {
        try {
          activeEffect = await loadGiftEffect(receipt.id, sourceURLs);
        } catch (effectError) {
          if (!receipt.animation.gif && !receipt.animation.webp) throw effectError;
          usingShortAnimationFallback = true;
        }
      }
      if (!activeEffect) {
        animation = await loadImage(`${giftReceiptMediaUrl(receipt.id, 'animation')}&v=${Date.now()}`, loadedImages);
      }
      if (disposed) return;

      const duration = activeEffect?.durationMs ?? normalizeGiftClipDuration(receipt.animation?.durationMs);
      activeStream = canvas.captureStream(CLIP_FPS);
      const selection = selectGiftClipRecorder(activeStream);
      generatedExtension = selection.extension;
      const chunks: BlobPart[] = [];
      selection.recorder.ondataavailable = (event) => {
        if (event.data.size > 0) chunks.push(event.data);
      };
      const stopped = new Promise<void>((resolve, reject) => {
        selection.recorder.onerror = () => reject(new Error('视频录制失败，请重试。'));
        selection.recorder.onstop = () => resolve();
      });
      const drawCurrentFrame = (): void => {
        const visual = activeEffect
          ? renderGiftEffectFrame(activeEffect)
          : imageGiftClipVisual(animation);
        drawGiftClipFrame(context, receipt, visual, avatar);
      };
      drawCurrentFrame();
      selection.recorder.start(250);
      const sourceLabel = activeEffect ? '完整特效' : usingShortAnimationFallback ? '短动画回退' : '短动画';
      status.textContent = `正在生成 ${selection.extension.toUpperCase()} · ${sourceLabel} · ${Math.round(duration / 100) / 10} 秒`;
      if (activeEffect) {
        activeEffect.video.currentTime = 0;
        await activeEffect.video.play();
      }
      const startedAt = performance.now();
      await new Promise<void>((resolve) => {
        const draw = (now: number): void => {
          if (disposed) {
            resolve();
            return;
          }
          const elapsed = now - startedAt;
          drawCurrentFrame();
          progress.value = Math.min(100, (elapsed / duration) * 100);
          if (elapsed >= duration) {
            resolve();
            return;
          }
          animationFrame = requestAnimationFrame(draw);
        };
        animationFrame = requestAnimationFrame(draw);
      });
      activeEffect?.video.pause();
      if (selection.recorder.state !== 'inactive') selection.recorder.stop();
      await stopped;
      if (disposed) return;
      stopGiftClipStream(activeStream);
      activeStream = null;
      generatedBlob = new Blob(chunks, { type: selection.recorder.mimeType || selection.mimeType });
      if (generatedBlob.size === 0) throw new Error('视频没有生成有效内容，请重试。');
      previewURL = URL.createObjectURL(generatedBlob);
      preview.src = previewURL;
      preview.hidden = false;
      canvas.hidden = true;
      progress.value = 100;
      const sizeLabel = generatedBlob.size < 1024 * 1024
        ? `${Math.max(1, Math.round(generatedBlob.size / 1024))} KB`
        : `${(generatedBlob.size / 1024 / 1024).toFixed(1)} MB`;
      status.textContent = `${generatedExtension.toUpperCase()} 已生成 · ${sizeLabel} · ${sourceLabel}`;
      saveButton.textContent = `保存 ${generatedExtension.toUpperCase()}`;
      saveButton.disabled = false;
      retryButton.disabled = false;
      void preview.play().catch(() => undefined);
    } catch (error) {
      if (disposed) return;
      stopGiftClipStream(activeStream);
      activeStream = null;
      progress.value = 0;
      const message = error instanceof Error ? error.message : '礼物动画生成失败，请重试。';
      status.textContent = message;
      status.classList.add('is-error');
      retryButton.disabled = false;
      options.onError?.(message);
    }
  };

  retryButton.onclick = () => {
    status.classList.remove('is-error');
    void generate();
  };
  saveButton.onclick = () => {
    if (!generatedBlob || !previewURL) return;
    triggerGiftClipDownload(previewURL, sanitizeGiftClipFilename(receipt, generatedExtension));
  };

  drawGiftClipPlaceholder(canvas, receipt);
  void generate();
  closeButton.focus();
  return { close };
}

async function loadGiftEffect(receiptId: string, sourceURLs: Set<string>): Promise<GiftEffectSource> {
  const cacheBuster = `&v=${Date.now()}`;
  const [layoutResponse, videoResponse] = await Promise.all([
    fetch(`${giftReceiptMediaUrl(receiptId, 'effect-layout')}${cacheBuster}`, { cache: 'no-store' }),
    fetch(`${giftReceiptMediaUrl(receiptId, 'effect-video')}${cacheBuster}`, { cache: 'no-store' }),
  ]);
  if (!layoutResponse.ok || !videoResponse.ok) throw new Error('完整礼物特效读取失败。');
  const layout = normalizeGiftEffectLayout(await layoutResponse.json());
  const videoBlob = await videoResponse.blob();
  if (!videoBlob.size) throw new Error('完整礼物特效没有有效视频。');
  const sourceURL = URL.createObjectURL(videoBlob);
  sourceURLs.add(sourceURL);
  const video = document.createElement('video');
  video.muted = true;
  video.playsInline = true;
  video.preload = 'auto';
  video.src = sourceURL;
  await waitForVideo(video);
  if (video.videoWidth !== layout.videoWidth || video.videoHeight !== layout.videoHeight) {
    throw new Error('礼物特效视频尺寸与坐标不一致。');
  }
  const [width, height] = fitGiftEffectFrame(layout.rgbFrame[2], layout.rgbFrame[3]);
  return {
    video,
    layout,
    frame: createEffectCanvas(width, height),
    color: createEffectCanvas(width, height),
    alpha: createEffectCanvas(width, height),
    durationMs: giftEffectDurationMs(layout),
  };
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

function createEffectCanvas(width: number, height: number): HTMLCanvasElement {
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  return canvas;
}

function fitGiftEffectFrame(width: number, height: number): [number, number] {
  const scale = Math.min(400 / width, 340 / height);
  return [Math.max(1, Math.round(width * scale)), Math.max(1, Math.round(height * scale))];
}

function renderGiftEffectFrame(effect: GiftEffectSource): GiftClipVisual | null {
  if (effect.video.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) return null;
  const colorContext = effect.color.getContext('2d', { willReadFrequently: true });
  const alphaContext = effect.alpha.getContext('2d', { willReadFrequently: true });
  const frameContext = effect.frame.getContext('2d');
  if (!colorContext || !alphaContext || !frameContext) throw new Error('礼物特效画布初始化失败。');
  const [rgbX, rgbY, rgbWidth, rgbHeight] = effect.layout.rgbFrame;
  const [alphaX, alphaY, alphaWidth, alphaHeight] = effect.layout.alphaFrame;
  const width = effect.frame.width;
  const height = effect.frame.height;
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

function imageGiftClipVisual(animation: HTMLImageElement | null): GiftClipVisual | null {
  if (!animation?.naturalWidth || !animation.naturalHeight) return null;
  return { source: animation, width: animation.naturalWidth, height: animation.naturalHeight };
}

function loadImage(src: string, registry: Set<HTMLImageElement>): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    registry.add(image);
    image.decoding = 'async';
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error('礼物动画素材读取失败，请稍后重试。'));
    image.src = src;
  });
}

async function loadOptionalImage(src: string, registry: Set<HTMLImageElement>): Promise<HTMLImageElement | null> {
  try {
    return await loadImage(src, registry);
  } catch {
    return null;
  }
}

function drawGiftClipPlaceholder(canvas: HTMLCanvasElement, receipt: GiftReceipt): void {
  const context = canvas.getContext('2d');
  if (!context) return;
  drawGiftClipFrame(context, receipt, null, null);
}

function drawGiftClipFrame(
  context: CanvasRenderingContext2D,
  receipt: GiftReceipt,
  animation: GiftClipVisual | null,
  avatar: HTMLImageElement | null,
): void {
  const gradient = context.createLinearGradient(0, 0, CLIP_SIZE, CLIP_SIZE);
  gradient.addColorStop(0, '#12101d');
  gradient.addColorStop(0.48, '#24152d');
  gradient.addColorStop(1, '#511d45');
  context.fillStyle = gradient;
  context.fillRect(0, 0, CLIP_SIZE, CLIP_SIZE);

  const glow = context.createRadialGradient(240, 185, 20, 240, 185, 250);
  glow.addColorStop(0, 'rgba(255, 113, 164, .3)');
  glow.addColorStop(1, 'rgba(255, 113, 164, 0)');
  context.fillStyle = glow;
  context.fillRect(0, 0, CLIP_SIZE, 370);

  if (animation?.width && animation.height) {
    const maxWidth = 400;
    const maxHeight = 340;
    const scale = Math.min(maxWidth / animation.width, maxHeight / animation.height);
    const width = animation.width * scale;
    const height = animation.height * scale;
    context.save();
    context.shadowColor = 'rgba(0, 0, 0, .32)';
    context.shadowBlur = 20;
    context.drawImage(animation.source, (CLIP_SIZE - width) / 2, 18 + (340 - height) / 2, width, height);
    context.restore();
  } else {
    context.fillStyle = 'rgba(255,255,255,.68)';
    context.textAlign = 'center';
    context.font = '600 24px system-ui, sans-serif';
    context.fillText('正在准备礼物动画', CLIP_SIZE / 2, 190);
  }

  const barGradient = context.createLinearGradient(18, 370, 462, 458);
  barGradient.addColorStop(0, 'rgba(87, 39, 101, .94)');
  barGradient.addColorStop(1, 'rgba(224, 68, 129, .94)');
  roundedRect(context, 18, 370, 444, 90, 22);
  context.fillStyle = barGradient;
  context.fill();
  context.strokeStyle = 'rgba(255,255,255,.24)';
  context.lineWidth = 1.5;
  context.stroke();

  context.save();
  context.beginPath();
  context.arc(67, 415, 30, 0, Math.PI * 2);
  context.clip();
  if (avatar?.naturalWidth && avatar.naturalHeight) {
    const side = Math.min(avatar.naturalWidth, avatar.naturalHeight);
    context.drawImage(avatar, (avatar.naturalWidth - side) / 2, (avatar.naturalHeight - side) / 2, side, side, 37, 385, 60, 60);
  } else {
    context.fillStyle = '#2a2132';
    context.fillRect(37, 385, 60, 60);
    context.fillStyle = '#ff85b1';
    context.textAlign = 'center';
    context.textBaseline = 'middle';
    context.font = '700 24px system-ui, sans-serif';
    context.fillText((receipt.uname || '观').slice(0, 1), 67, 415);
  }
  context.restore();
  context.strokeStyle = 'rgba(255,255,255,.78)';
  context.lineWidth = 2;
  context.beginPath();
  context.arc(67, 415, 30, 0, Math.PI * 2);
  context.stroke();

  const name = truncateCanvasText(context, receipt.uname?.trim() || '匿名观众', 302, '700 20px system-ui, sans-serif');
  const giftText = truncateCanvasText(context, `赠送 ${receipt.giftName || '礼物'} × ${Math.max(1, receipt.num || 1)}`, 302, '500 17px system-ui, sans-serif');
  context.textAlign = 'left';
  context.textBaseline = 'alphabetic';
  context.fillStyle = '#ffffff';
  context.font = '700 20px system-ui, sans-serif';
  context.fillText(name, 114, 409);
  context.fillStyle = 'rgba(255,255,255,.82)';
  context.font = '500 17px system-ui, sans-serif';
  context.fillText(giftText, 114, 436);
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
