import type { GiftClipCrop, GiftReceipt } from '../../types';
import { el } from '../common';
import {
  giftClipCropToPixels,
  isGiftClipSourceSizeSupported,
  normalizeGiftClipCrop,
} from './gift-clip-crop';
import {
  createGiftClipCropEditor,
  type GiftClipCropEditor,
} from './gift-clip-crop-editor';
import {
  loadGiftClipMediaSession,
  type GiftClipMediaSession,
} from './gift-clip-media';
import {
  drawGiftClipOutputFrame,
  drawGiftClipSourcePreview,
  prepareGiftClipOutputCanvas,
} from './gift-clip-renderer';
import {
  recordGiftClipCanvas,
  sanitizeGiftClipFilename,
  triggerGiftClipDownload,
  type GiftClipRecording,
} from './gift-clip-recorder';

export interface GiftClipStudioController {
  close: () => void;
}

interface GiftClipStudioOptions {
  host: HTMLElement;
  receipt: GiftReceipt;
  initialCrop?: GiftClipCrop;
  onCropConfirmed?: (crop: GiftClipCrop) => void;
  onError?: (message: string) => void;
}

export function giftClipAnimationKey(receipt: Pick<GiftReceipt, 'giftId' | 'animation'>): string {
  const effectId = Number(receipt.animation?.effectId);
  if (Number.isInteger(effectId) && effectId > 0) return `effect:${effectId}`;
  const source = receipt.animation?.gif?.trim() || receipt.animation?.webp?.trim();
  if (!source) return `gift:${Math.trunc(Number(receipt.giftId) || 0)}`;
  let stableSource = source.split(/[?#]/, 1)[0];
  try {
    const url = new URL(source, 'https://local.invalid');
    stableSource = `${url.hostname.toLowerCase()}${url.pathname}`;
  } catch {
    // The query-free source remains stable for malformed legacy URLs.
  }
  let hash = 0x811c9dc5;
  for (let index = 0; index < stableSource.length; index += 1) {
    hash ^= stableSource.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `media:${(hash >>> 0).toString(16).padStart(8, '0')}`;
}

export function openGiftClipStudio(options: GiftClipStudioOptions): GiftClipStudioController {
  const { host, receipt } = options;
  let closed = false;
  let transition = 0;
  let previewFrame = 0;
  let session: GiftClipMediaSession | null = null;
  let editor: GiftClipCropEditor | null = null;
  let loadAbort: AbortController | null = null;
  let recordingAbort: AbortController | null = null;
  let recordingTask: Promise<void> | null = null;
  let previewURL = '';
  let generatedRecording: GiftClipRecording | null = null;
  let confirmedCrop = normalizeGiftClipCrop(options.initialCrop);
  let secondaryAction: 're-edit' | 'retry' = 'retry';

  const overlay = el('div', {
    class: 'overlay gift-clip-overlay',
    role: 'dialog',
    ariaModal: 'true',
    ariaLabel: '制作礼物动画回放',
  });
  const dialog = el('section', { class: 'card gift-clip-dialog' });
  const closeButton = el('button', {
    class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭',
  }) as HTMLButtonElement;
  const stage = el('div', { class: 'gift-clip-stage' });
  const sourceCanvas = el('canvas', {
    class: 'gift-clip-canvas', width: 1, height: 1,
  }) as HTMLCanvasElement;
  const recordingCanvas = el('canvas', {
    class: 'gift-clip-recording-canvas', hidden: true,
  }) as HTMLCanvasElement;
  const preview = el('video', {
    class: 'gift-clip-video', controls: true, loop: true, muted: true, playsInline: true, hidden: true,
  }) as HTMLVideoElement;
  const sourceMediaHost = el('div', {
    class: 'gift-clip-source-media', ariaHidden: 'true',
  });
  const progress = el('progress', {
    class: 'gift-clip-progress', max: 100, value: 0, hidden: true,
  }) as HTMLProgressElement;
  const status = el('p', {
    class: 'gift-clip-status', text: '正在读取礼物动画…',
  });
  const resetButton = el('button', {
    class: 'btn ghost', type: 'button', text: '恢复完整画面', hidden: true,
  }) as HTMLButtonElement;
  const confirmButton = el('button', {
    class: 'btn primary', type: 'button', text: '确定剪裁并生成', hidden: true,
  }) as HTMLButtonElement;
  const reeditButton = el('button', {
    class: 'btn ghost', type: 'button', text: '重新剪裁', hidden: true,
  }) as HTMLButtonElement;
  const saveButton = el('button', {
    class: 'btn primary', type: 'button', text: '保存视频', hidden: true, disabled: true,
  }) as HTMLButtonElement;

  stage.append(sourceCanvas, recordingCanvas, preview, sourceMediaHost);
  dialog.append(
    el('header', { class: 'gift-clip-header' }, [
      el('div', {}, [
        el('span', { class: 'section-kicker', text: '礼物动画回放' }),
        el('h2', { text: `${receipt.giftName || '礼物'} × ${Math.max(1, receipt.num || 1)}` }),
        el('p', { text: '按素材原始像素剪裁；同一动画会记住已确认区域，成片不含 UID。' }),
      ]),
      closeButton,
    ]),
    el('div', { class: 'gift-clip-body' }, [
      stage,
      el('div', { class: 'gift-clip-meta' }, [
        status,
        progress,
        el('div', { class: 'gift-clip-actions' }, [resetButton, confirmButton, reeditButton, saveButton]),
      ]),
    ]),
  );
  overlay.append(dialog);
  host.append(overlay);

  const isCurrent = (token: number): boolean => !closed && token === transition;
  const setStageSize = (width: number, height: number): void => {
    stage.style.setProperty('--gift-clip-source-width', String(width));
    stage.style.setProperty('--gift-clip-source-height', String(height));
  };
  const stopEditorPreview = (): void => {
    if (previewFrame) cancelAnimationFrame(previewFrame);
    previewFrame = 0;
    session?.pause();
  };
  const destroyEditor = (): void => {
    editor?.destroy();
    editor = null;
  };
  const abortRecording = (): void => {
    if (recordingAbort && !recordingAbort.signal.aborted) recordingAbort.abort();
  };
  const abortRecordingTask = (): Promise<void> | null => {
    const activeTask = recordingTask;
    abortRecording();
    return activeTask;
  };
  const abortLoad = (): void => {
    if (loadAbort && !loadAbort.signal.aborted) {
      loadAbort.abort(new DOMException('Gift clip source load cancelled.', 'AbortError'));
    }
    loadAbort = null;
  };
  const clearPreview = (): void => {
    preview.pause();
    if (previewURL) URL.revokeObjectURL(previewURL);
    previewURL = '';
    generatedRecording = null;
    preview.removeAttribute('src');
    preview.load();
    preview.hidden = true;
    preview.style.aspectRatio = '';
  };
  const disposeSession = (): void => {
    session?.dispose();
    session = null;
  };
  const releaseCanvasBackingStores = (): void => {
    sourceCanvas.width = 0;
    sourceCanvas.height = 0;
    recordingCanvas.width = 0;
    recordingCanvas.height = 0;
  };
  const hideActions = (): void => {
    resetButton.hidden = true;
    confirmButton.hidden = true;
    reeditButton.hidden = true;
    saveButton.hidden = true;
    saveButton.disabled = true;
  };
  const reportFailure = (error: unknown): void => {
    if (closed) return;
    stopEditorPreview();
    destroyEditor();
    clearPreview();
    disposeSession();
    releaseCanvasBackingStores();
    progress.value = 0;
    progress.hidden = true;
    resetButton.hidden = true;
    confirmButton.hidden = true;
    saveButton.hidden = true;
    saveButton.disabled = true;
    secondaryAction = 'retry';
    reeditButton.textContent = '重试';
    reeditButton.hidden = false;
    reeditButton.disabled = false;
    const message = error instanceof Error ? error.message : '礼物动画生成失败，请重试。';
    status.textContent = message;
    status.classList.add('is-error');
    options.onError?.(message);
  };

  const startPreviewLoop = (activeSession: GiftClipMediaSession, token: number): void => {
    const context = sourceCanvas.getContext('2d');
    if (!context) throw new Error('礼物动画预览画布初始化失败。');
    const startedAt = performance.now();
    const draw = (now: number): void => {
      previewFrame = 0;
      if (!isCurrent(token) || activeSession !== session) return;
      try {
        drawGiftClipSourcePreview(context, activeSession.visualAt(now - startedAt));
        previewFrame = requestAnimationFrame(draw);
      } catch (error) {
        reportFailure(error);
      }
    };
    drawGiftClipSourcePreview(context, activeSession.visualAt(0));
    previewFrame = requestAnimationFrame(draw);
  };

  const presentEditor = async (
    activeSession: GiftClipMediaSession,
    crop: GiftClipCrop,
    token: number,
  ): Promise<void> => {
    try {
      const activeRecording = abortRecordingTask();
      if (activeRecording) await activeRecording;
      if (!isCurrent(token) || activeSession !== session) return;
      stopEditorPreview();
      destroyEditor();
      clearPreview();
      sourceCanvas.hidden = false;
      progress.value = 0;
      progress.hidden = true;
      status.classList.remove('is-error');
      resetButton.hidden = false;
      confirmButton.hidden = false;
      confirmButton.disabled = false;
      reeditButton.hidden = true;
      saveButton.hidden = true;
      setStageSize(activeSession.width, activeSession.height);
      sourceCanvas.width = activeSession.width;
      sourceCanvas.height = activeSession.height;
      await activeSession.restart();
      if (!isCurrent(token) || activeSession !== session) return;
      startPreviewLoop(activeSession, token);
      editor = createGiftClipCropEditor({
        stage,
        sourceWidth: activeSession.width,
        sourceHeight: activeSession.height,
        initialCrop: crop,
        receipt,
        avatar: activeSession.avatar,
        onChange: (_nextCrop, pixels) => {
          status.textContent = `剪裁 ${pixels.width} × ${pixels.height} · 成片按原始像素输出`;
        },
      });
    } catch (error) {
      if (isCurrent(token)) reportFailure(error);
    }
  };

  const loadSource = async (): Promise<void> => {
    const token = ++transition;
    abortLoad();
    const activeRecording = abortRecordingTask();
    if (activeRecording) await activeRecording;
    if (!isCurrent(token)) return;
    stopEditorPreview();
    destroyEditor();
    clearPreview();
    disposeSession();
    hideActions();
    sourceCanvas.hidden = false;
    sourceCanvas.width = 1;
    sourceCanvas.height = 1;
    setStageSize(1, 1);
    progress.value = 0;
    progress.hidden = true;
    status.textContent = '正在读取礼物动画…';
    status.classList.remove('is-error');
    const controller = new AbortController();
    loadAbort = controller;
    try {
      const loaded = await loadGiftClipMediaSession(receipt, sourceMediaHost, controller.signal);
      if (loadAbort === controller) loadAbort = null;
      if (!isCurrent(token)) {
        loaded.dispose();
        return;
      }
      session = loaded;
      if (!isGiftClipSourceSizeSupported(loaded.width, loaded.height)) {
        status.textContent = `动画尺寸过小，无法制作回放（${loaded.width} × ${loaded.height}）`;
        status.classList.add('is-error');
        disposeSession();
        releaseCanvasBackingStores();
        secondaryAction = 'retry';
        reeditButton.textContent = '重试';
        reeditButton.hidden = false;
        reeditButton.disabled = false;
        return;
      }
      await presentEditor(loaded, confirmedCrop, token);
    } catch (error) {
      if (loadAbort === controller) loadAbort = null;
      if (isCurrent(token) && !controller.signal.aborted) reportFailure(error);
    }
  };

  const confirmAndGenerate = async (): Promise<void> => {
    const activeEditor = editor;
    const activeSession = session;
    if (!activeEditor || !activeSession || closed) return;
    confirmButton.disabled = true;
    const crop = normalizeGiftClipCrop(activeEditor.getCrop());
    try {
      options.onCropConfirmed?.({ ...crop });
    } catch (error) {
      confirmButton.disabled = false;
      reportFailure(error);
      return;
    }
    if (closed) return;
    confirmedCrop = crop;
    const token = ++transition;
    stopEditorPreview();
    destroyEditor();
    const pixels = giftClipCropToPixels(crop, activeSession.width, activeSession.height);
    prepareGiftClipOutputCanvas(recordingCanvas, pixels);
    const context = recordingCanvas.getContext('2d');
    if (!context) {
      reportFailure(new Error('礼物动画录制画布初始化失败。'));
      return;
    }
    resetButton.hidden = true;
    confirmButton.hidden = true;
    saveButton.hidden = true;
    saveButton.disabled = true;
    secondaryAction = 're-edit';
    reeditButton.textContent = '重新剪裁';
    reeditButton.hidden = false;
    reeditButton.disabled = false;
    progress.value = 0;
    progress.hidden = false;
    status.classList.remove('is-error');
    status.textContent = `正在生成视频 · ${activeSession.sourceLabel} · ${Math.round(activeSession.durationMs / 100) / 10} 秒`;
    const controller = new AbortController();
    recordingAbort = controller;
    const recordingRun = (async (): Promise<void> => {
      try {
        await activeSession.restart();
        if (!isCurrent(token)) return;
        const recording = await recordGiftClipCanvas({
          canvas: recordingCanvas,
          durationMs: activeSession.durationMs,
          signal: controller.signal,
          drawFrame: (elapsedMs) => {
            drawGiftClipOutputFrame(context, receipt, activeSession.visualAt(elapsedMs), activeSession.avatar, pixels);
          },
          onProgress: (value) => {
            if (isCurrent(token)) progress.value = Math.min(100, Math.max(0, value * 100));
          },
        });
        if (!isCurrent(token)) return;
        activeSession.pause();
        generatedRecording = recording;
        previewURL = URL.createObjectURL(recording.blob);
        preview.src = previewURL;
        preview.style.aspectRatio = `${pixels.width} / ${pixels.height}`;
        setStageSize(pixels.width, pixels.height);
        sourceCanvas.hidden = true;
        preview.hidden = false;
        progress.value = 100;
        const sizeLabel = formatGiftClipBytes(recording.blob.size);
        status.textContent = `${recording.extension.toUpperCase()} 已生成 · ${sizeLabel} · ${pixels.width} × ${pixels.height} · ${activeSession.sourceLabel}`;
        saveButton.textContent = `保存 ${recording.extension.toUpperCase()}`;
        saveButton.hidden = false;
        saveButton.disabled = false;
        void preview.play().catch(() => undefined);
      } catch (error) {
        if (isCurrent(token) && !controller.signal.aborted) reportFailure(error);
      }
    })();
    const task = recordingRun.finally(() => {
      releaseCanvasBackingStores();
      if (recordingTask === task) recordingTask = null;
      if (recordingAbort === controller) recordingAbort = null;
    });
    recordingTask = task;
    await task;
  };

  const close = (): void => {
    if (closed) return;
    closed = true;
    transition += 1;
    abortLoad();
    void abortRecordingTask();
    stopEditorPreview();
    destroyEditor();
    disposeSession();
    clearPreview();
    releaseCanvasBackingStores();
    globalThis.removeEventListener('keydown', onKeyDown);
    overlay.removeEventListener('click', onOverlayClick);
    closeButton.onclick = null;
    resetButton.onclick = null;
    confirmButton.onclick = null;
    reeditButton.onclick = null;
    saveButton.onclick = null;
    overlay.remove();
  };
  const onKeyDown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') close();
  };
  const onOverlayClick = (event: MouseEvent): void => {
    if (event.target === overlay) close();
  };

  closeButton.onclick = close;
  resetButton.onclick = () => editor?.reset();
  confirmButton.onclick = () => { void confirmAndGenerate(); };
  reeditButton.onclick = () => {
    if (secondaryAction === 'retry' || !session) {
      void loadSource();
      return;
    }
    const token = ++transition;
    void presentEditor(session, confirmedCrop, token);
  };
  saveButton.onclick = () => {
    if (!generatedRecording || !previewURL) return;
    triggerGiftClipDownload(
      previewURL,
      sanitizeGiftClipFilename(receipt, generatedRecording.extension),
    );
  };
  globalThis.addEventListener('keydown', onKeyDown);
  overlay.addEventListener('click', onOverlayClick);

  const initialContext = sourceCanvas.getContext('2d');
  if (initialContext) drawGiftClipSourcePreview(initialContext, null);
  void loadSource();
  closeButton.focus();
  return { close };
}

function formatGiftClipBytes(bytes: number): string {
  return bytes < 1024 * 1024
    ? `${Math.max(1, Math.round(bytes / 1024))} KB`
    : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
