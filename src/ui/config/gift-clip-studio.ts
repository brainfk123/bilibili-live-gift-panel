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
  const loadedImages = new Set<HTMLImageElement>();

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
    for (const image of loadedImages) image.src = '';
    loadedImages.clear();
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
      const animation = await loadImage(`${giftReceiptMediaUrl(receipt.id, 'animation')}&v=${Date.now()}`, loadedImages);
      if (disposed) return;

      const duration = normalizeGiftClipDuration(receipt.animation?.durationMs);
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
      drawGiftClipFrame(context, receipt, animation, avatar);
      selection.recorder.start(250);
      status.textContent = `正在生成 ${selection.extension.toUpperCase()} · ${Math.round(duration / 100) / 10} 秒`;
      const startedAt = performance.now();
      await new Promise<void>((resolve) => {
        const draw = (now: number): void => {
          if (disposed) {
            resolve();
            return;
          }
          const elapsed = now - startedAt;
          drawGiftClipFrame(context, receipt, animation, avatar);
          progress.value = Math.min(100, (elapsed / duration) * 100);
          if (elapsed >= duration) {
            resolve();
            return;
          }
          animationFrame = requestAnimationFrame(draw);
        };
        animationFrame = requestAnimationFrame(draw);
      });
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
      status.textContent = `${generatedExtension.toUpperCase()} 已生成 · ${sizeLabel}`;
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
  animation: HTMLImageElement | null,
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

  if (animation?.naturalWidth && animation.naturalHeight) {
    const maxWidth = 400;
    const maxHeight = 340;
    const scale = Math.min(maxWidth / animation.naturalWidth, maxHeight / animation.naturalHeight);
    const width = animation.naturalWidth * scale;
    const height = animation.naturalHeight * scale;
    context.save();
    context.shadowColor = 'rgba(0, 0, 0, .32)';
    context.shadowBlur = 20;
    context.drawImage(animation, (CLIP_SIZE - width) / 2, 18 + (340 - height) / 2, width, height);
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
