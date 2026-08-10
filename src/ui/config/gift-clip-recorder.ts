import type { GiftReceipt } from '../../types';

const GIFT_CLIP_FPS = 30;

const RECORDER_FORMATS = [
  { mimeType: 'video/mp4;codecs=avc1.42E01E', extension: 'mp4' as const },
  { mimeType: 'video/mp4', extension: 'mp4' as const },
  { mimeType: 'video/webm;codecs=vp9', extension: 'webm' as const },
  { mimeType: 'video/webm;codecs=vp8', extension: 'webm' as const },
  { mimeType: 'video/webm', extension: 'webm' as const },
];

export interface GiftClipRecording {
  blob: Blob;
  mimeType: string;
  extension: 'mp4' | 'webm';
}

interface GiftClipRecorderSelection {
  recorder: MediaRecorder;
  mimeType: string;
  extension: 'mp4' | 'webm';
}

export function sanitizeGiftClipFilename(
  receipt: Pick<GiftReceipt, 'giftName' | 'uname' | 'time'>,
  extension: 'mp4' | 'webm',
): string {
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
): GiftClipRecorderSelection {
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

export function triggerGiftClipDownload(
  url: string,
  filename: string,
  targetDocument: Document = document,
): void {
  const link = targetDocument.createElement('a');
  link.href = url;
  link.download = filename;
  targetDocument.body.append(link);
  link.click();
  link.remove();
}

export async function recordGiftClipCanvas(options: {
  canvas: HTMLCanvasElement;
  durationMs: number;
  drawFrame: (elapsedMs: number) => void;
  onProgress: (progress: number) => void;
  signal: AbortSignal;
}): Promise<GiftClipRecording> {
  const { canvas, drawFrame, onProgress, signal } = options;
  if (typeof canvas.captureStream !== 'function') throw new Error('当前浏览器不能录制画布。');

  let stream: MediaStream | null = null;
  let recorder: MediaRecorder | null = null;
  let animationFrame: number | null = null;
  const abort = (): void => {
    if (animationFrame !== null) cancelAnimationFrame(animationFrame);
    animationFrame = null;
    if (recorder?.state !== 'inactive') recorder?.stop();
  };

  try {
    stream = canvas.captureStream(GIFT_CLIP_FPS);
    const selection = selectGiftClipRecorder(stream);
    recorder = selection.recorder;
    const chunks: BlobPart[] = [];
    recorder.ondataavailable = (event) => {
      if (event.data.size > 0) chunks.push(event.data);
    };
    const recorderStopped = new Promise<'stopped'>((resolve, reject) => {
      if (!recorder) return;
      recorder.onerror = () => reject(new Error('视频录制失败，请重试。'));
      recorder.onstop = () => resolve('stopped');
    });

    signal.addEventListener('abort', abort, { once: true });
    throwIfAborted(signal);
    drawFrame(0);
    throwIfAborted(signal);
    onProgress(0);
    throwIfAborted(signal);
    recorder.start(250);

    const durationMs = Math.max(0, Number(options.durationMs) || 0);
    const startedAt = performance.now();
    const framesFinished = new Promise<'frames'>((resolve, reject) => {
      const draw = (now: number): void => {
        animationFrame = null;
        if (signal.aborted) {
          reject(abortReason(signal));
          return;
        }
        try {
          const elapsedMs = Math.max(0, now - startedAt);
          drawFrame(elapsedMs);
          onProgress(durationMs === 0 ? 1 : Math.min(1, elapsedMs / durationMs));
          if (elapsedMs >= durationMs) {
            resolve('frames');
            return;
          }
          animationFrame = requestAnimationFrame(draw);
        } catch (error) {
          reject(error);
        }
      };
      animationFrame = requestAnimationFrame(draw);
    });

    const outcome = await Promise.race([framesFinished, recorderStopped]);
    throwIfAborted(signal);
    if (outcome === 'frames' && recorder.state !== 'inactive') recorder.stop();
    await recorderStopped;
    throwIfAborted(signal);

    const mimeType = recorder.mimeType || selection.mimeType;
    const blob = new Blob(chunks, { type: mimeType });
    if (blob.size === 0) throw new Error('视频没有生成有效内容，请重试。');
    return { blob, mimeType, extension: selection.extension };
  } finally {
    signal.removeEventListener('abort', abort);
    if (animationFrame !== null) cancelAnimationFrame(animationFrame);
    animationFrame = null;
    try {
      if (recorder?.state !== 'inactive') recorder?.stop();
    } catch {
      // Stream tracks still need to be released when recorder shutdown fails.
    }
    stopGiftClipStream(stream);
  }
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw abortReason(signal);
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException('The operation was aborted.', 'AbortError');
}
