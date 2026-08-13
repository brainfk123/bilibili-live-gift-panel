import type { GiftReceipt } from '../../types';
import { el } from '../common';

export interface GiftClipStudioView {
  readonly overlay: HTMLElement;
  readonly stage: HTMLElement;
  readonly sourceCanvas: HTMLCanvasElement;
  readonly preview: HTMLVideoElement;
  readonly sourceMediaHost: HTMLElement;
  readonly closeButton: HTMLButtonElement;
  readonly resetButton: HTMLButtonElement;
  readonly confirmButton: HTMLButtonElement;
  readonly reeditButton: HTMLButtonElement;
  readonly saveButton: HTMLButtonElement;
  setStageSize(width: number, height: number): void;
  setConfirmDisabled(disabled: boolean): void;
  clearPreview(): void;
  showLoading(): void;
  showEditing(message: string): void;
  showEncoding(message: string, progress: number, secondaryLabel: string): void;
  showReady(message: string, saveLabel: string, previewSource: string, aspectRatio: string): void;
  showFailure(message: string, retryLabel: string): void;
  destroy(): void;
}

export function createGiftClipStudioView(host: HTMLElement, receipt: GiftReceipt): GiftClipStudioView {
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

  stage.append(sourceCanvas, preview, sourceMediaHost);
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

  const hideActions = (): void => {
    resetButton.hidden = true;
    confirmButton.hidden = true;
    reeditButton.hidden = true;
    saveButton.hidden = true;
    saveButton.disabled = true;
  };
  const clearError = (): void => status.classList.remove('is-error');

  return {
    overlay,
    stage,
    sourceCanvas,
    preview,
    sourceMediaHost,
    closeButton,
    resetButton,
    confirmButton,
    reeditButton,
    saveButton,
    setStageSize: (width, height) => {
      stage.style.setProperty('--gift-clip-source-width', String(width));
      stage.style.setProperty('--gift-clip-source-height', String(height));
    },
    setConfirmDisabled: (disabled) => {
      confirmButton.disabled = disabled;
    },
    clearPreview: () => {
      preview.removeAttribute('src');
      preview.hidden = true;
      preview.style.aspectRatio = '';
    },
    showLoading: () => {
      hideActions();
      sourceCanvas.hidden = false;
      progress.value = 0;
      progress.hidden = true;
      status.textContent = '正在读取礼物动画…';
      clearError();
    },
    showEditing: (message) => {
      sourceCanvas.hidden = false;
      progress.value = 0;
      progress.hidden = true;
      status.textContent = message;
      clearError();
      resetButton.hidden = false;
      confirmButton.hidden = false;
      confirmButton.disabled = false;
      reeditButton.hidden = true;
      saveButton.hidden = true;
      saveButton.disabled = true;
    },
    showEncoding: (message, value, secondaryLabel) => {
      resetButton.hidden = true;
      confirmButton.hidden = true;
      saveButton.hidden = true;
      saveButton.disabled = true;
      reeditButton.hidden = false;
      reeditButton.disabled = false;
      reeditButton.textContent = secondaryLabel;
      progress.value = Math.min(100, Math.max(0, value));
      progress.hidden = false;
      status.textContent = message;
      clearError();
    },
    showReady: (message, saveLabel, previewSource, aspectRatio) => {
      preview.src = previewSource;
      preview.style.aspectRatio = aspectRatio;
      sourceCanvas.hidden = true;
      preview.hidden = false;
      progress.value = 100;
      progress.hidden = false;
      status.textContent = message;
      clearError();
      saveButton.textContent = saveLabel;
      saveButton.hidden = false;
      saveButton.disabled = false;
    },
    showFailure: (message, retryLabel) => {
      progress.value = 0;
      progress.hidden = true;
      resetButton.hidden = true;
      confirmButton.hidden = true;
      saveButton.hidden = true;
      saveButton.disabled = true;
      reeditButton.textContent = retryLabel;
      reeditButton.hidden = false;
      reeditButton.disabled = false;
      status.textContent = message;
      status.classList.add('is-error');
    },
    destroy: () => overlay.remove(),
  };
}
