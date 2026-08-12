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
import { sanitizeGiftClipFilename, triggerGiftClipDownload } from './gift-clip-download';
import {
  createGiftClipJob,
  cancelGiftClipJob,
  giftClipJobVideoURL,
  waitForGiftClipJob,
  type GiftClipJobSnapshot,
} from './gift-clip-export-api';
import { createGiftClipExportLayers } from './gift-clip-export-layers';
import {
  loadGiftClipMediaSession,
  type GiftClipMediaSession,
} from './gift-clip-media';
import { drawGiftClipSourcePreview } from './gift-clip-renderer';
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
    overlay, stage, sourceCanvas, preview, sourceMediaHost, closeButton,
    resetButton, confirmButton, reeditButton, saveButton,
  } = view;
  let closed = false;
  let transition = 0;
  let previewFrame = 0;
  let session: GiftClipMediaSession | null = null;
  let editor: GiftClipCropEditor | null = null;
  let loadAbort: AbortController | null = null;
  let exportAbort: AbortController | null = null;
  let exportJobId = '';
  let exportTask: Promise<void> | null = null;
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
  const abortLoad = (): void => {
    if (loadAbort && !loadAbort.signal.aborted) loadAbort.abort();
    loadAbort = null;
  };
  const cancelExport = async (): Promise<void> => {
    exportAbort?.abort();
    exportAbort = null;
    const id = exportJobId;
    exportJobId = '';
    if (id) await cancelGiftClipJob(id).catch(() => undefined);
  };
  const clearPreview = (): void => {
    preview.pause();
    view.clearPreview();
    preview.load();
  };
  const disposeSession = (): void => {
    session?.dispose();
    session = null;
  };
  const releaseCanvasBackingStore = (): void => {
    sourceCanvas.width = 0;
    sourceCanvas.height = 0;
  };
  const reportFailure = (error: unknown): void => {
    if (closed) return;
    stopEditorPreview();
    destroyEditor();
    clearPreview();
    disposeSession();
    releaseCanvasBackingStore();
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
    void cancelExport();
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
        releaseCanvasBackingStore();
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

  const snapshotMessage = (snapshot: GiftClipJobSnapshot, activeSession: GiftClipMediaSession): string => {
    if (snapshot.message) return snapshot.message;
    if (snapshot.state === 'queued') return '正在排队导出…';
    if (snapshot.state === 'retrying') return '已切换兼容编码模式。';
    if (snapshot.state === 'ready') return '视频已生成。';
    return `正在生成视频 · ${activeSession.sourceLabel}`;
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
    void cancelExport();
    stopEditorPreview();
    destroyEditor();
    const pixels = giftClipCropToPixels(crop, activeSession.width, activeSession.height);
    secondaryAction = 're-edit';
    view.showEncoding(`正在生成视频 · ${activeSession.sourceLabel}`, 0, '重新剪裁');
    const controller = new AbortController();
    exportAbort = controller;
    let task!: Promise<void>;
    task = (async (): Promise<void> => {
      try {
        const layers = await createGiftClipExportLayers({
          width: pixels.width,
          height: pixels.height,
          receipt,
          avatar: activeSession.avatar,
          document,
        });
        if (!isCurrent(token) || controller.signal.aborted) return;
        const created = await createGiftClipJob({
          receiptId: receipt.id,
          crop,
          ...layers,
        }, controller.signal);
        if (!isCurrent(token) || controller.signal.aborted) {
          await cancelGiftClipJob(created.id).catch(() => undefined);
          return;
        }
        exportJobId = created.id;
        const ready = await waitForGiftClipJob(created.id, {
          signal: controller.signal,
          onSnapshot: (snapshot) => {
            if (!isCurrent(token)) return;
            view.showEncoding(snapshotMessage(snapshot, activeSession), snapshot.progress * 100, '重新剪裁');
          },
        });
        if (!isCurrent(token) || controller.signal.aborted) return;
        view.setStageSize(ready.output.width, ready.output.height);
        view.showReady(
          `MP4 已生成 · ${ready.output.width} × ${ready.output.height} · ${activeSession.sourceLabel}`,
          '保存 MP4',
          giftClipJobVideoURL(ready.id),
          `${ready.output.width} / ${ready.output.height}`,
        );
        void preview.play().catch(() => undefined);
      } catch (error) {
        if (!isCurrent(token) || controller.signal.aborted) return;
        await cancelExport();
        if (isCurrent(token)) reportFailure(error);
      } finally {
        if (exportAbort === controller) exportAbort = null;
        if (exportTask === task) exportTask = null;
      }
    })();
    exportTask = task;
    await task;
  };

  const close = (): void => {
    if (closed) return;
    closed = true;
    transition += 1;
    abortLoad();
    void cancelExport();
    stopEditorPreview();
    destroyEditor();
    disposeSession();
    clearPreview();
    releaseCanvasBackingStore();
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
    void cancelExport();
    void presentEditor(session, confirmedCrop, token);
  };
  saveButton.onclick = () => {
    if (!exportJobId) return;
    triggerGiftClipDownload(giftClipJobVideoURL(exportJobId), sanitizeGiftClipFilename(receipt));
  };
  globalThis.addEventListener('keydown', onKeyDown);
  overlay.addEventListener('click', onOverlayClick);

  const initialContext = sourceCanvas.getContext('2d');
  if (initialContext) drawGiftClipSourcePreview(initialContext, null);
  void loadSource();
  closeButton.focus();
  return { close };
}
