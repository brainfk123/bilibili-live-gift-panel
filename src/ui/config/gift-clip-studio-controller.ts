import type { GiftClipCrop, GiftReceipt } from '../../types';
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
import { createGiftClipStudioView } from './gift-clip-studio-view';

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

export function openGiftClipStudio(options: GiftClipStudioOptions): GiftClipStudioController {
  const { host, receipt } = options;
  const view = createGiftClipStudioView(host, receipt);
  const {
    overlay,
    stage,
    sourceCanvas,
    recordingCanvas,
    preview,
    sourceMediaHost,
    closeButton,
    resetButton,
    confirmButton,
    reeditButton,
    saveButton,
  } = view;
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

  const isCurrent = (token: number): boolean => !closed && token === transition;
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
    view.clearPreview();
    preview.load();
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
  const reportFailure = (error: unknown): void => {
    if (closed) return;
    stopEditorPreview();
    destroyEditor();
    clearPreview();
    disposeSession();
    releaseCanvasBackingStores();
    secondaryAction = 'retry';
    const message = error instanceof Error ? error.message : '礼物动画生成失败，请重试。';
    view.showFailure(message, '重试');
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
      view.showEditing('正在读取礼物动画…');
      view.setStageSize(activeSession.width, activeSession.height);
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
          view.showEditing(`剪裁 ${pixels.width} × ${pixels.height} · 成片按原始像素输出`);
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
    view.showLoading();
    sourceCanvas.width = 1;
    sourceCanvas.height = 1;
    view.setStageSize(1, 1);
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
        const message = `动画尺寸过小，无法制作回放（${loaded.width} × ${loaded.height}）`;
        disposeSession();
        releaseCanvasBackingStores();
        secondaryAction = 'retry';
        view.showFailure(message, '重试');
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
    view.setConfirmDisabled(true);
    const crop = normalizeGiftClipCrop(activeEditor.getCrop());
    try {
      options.onCropConfirmed?.({ ...crop });
    } catch (error) {
      view.setConfirmDisabled(false);
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
    secondaryAction = 're-edit';
    const encodingMessage = `正在生成视频 · ${activeSession.sourceLabel} · ${Math.round(activeSession.durationMs / 100) / 10} 秒`;
    view.showEncoding(encodingMessage, 0, '重新剪裁');
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
            if (isCurrent(token)) view.showEncoding(encodingMessage, value * 100, '重新剪裁');
          },
        });
        if (!isCurrent(token)) return;
        activeSession.pause();
        generatedRecording = recording;
        previewURL = URL.createObjectURL(recording.blob);
        view.setStageSize(pixels.width, pixels.height);
        const sizeLabel = formatGiftClipBytes(recording.blob.size);
        view.showReady(
          `${recording.extension.toUpperCase()} 已生成 · ${sizeLabel} · ${pixels.width} × ${pixels.height} · ${activeSession.sourceLabel}`,
          `保存 ${recording.extension.toUpperCase()}`,
          previewURL,
          `${pixels.width} / ${pixels.height}`,
        );
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
    view.destroy();
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
