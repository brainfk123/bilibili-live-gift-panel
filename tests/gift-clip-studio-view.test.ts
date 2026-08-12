import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { GiftReceipt } from '../src/types';
import { createGiftClipStudioView } from '../src/ui/config/gift-clip-studio-view';

class ViewTestStyle {
  aspectRatio = '';
  private readonly values = new Map<string, string>();

  setProperty(name: string, value: string): void {
    this.values.set(name, value);
  }

  getPropertyValue(name: string): string {
    return this.values.get(name) ?? '';
  }
}

class ViewTestElement {
  className = '';
  textContent = '';
  children: ViewTestElement[] = [];
  parent: ViewTestElement | null = null;
  style = new ViewTestStyle();
  hidden = false;
  disabled = false;
  type = '';
  width = 0;
  height = 0;
  value = 0;
  max = 0;
  role = '';
  ariaModal = '';
  ariaLabel = '';
  ariaHidden = '';
  controls = false;
  loop = false;
  muted = false;
  playsInline = false;
  removeCalls = 0;

  readonly classList = {
    add: (...names: string[]) => this.updateClasses(names, true),
    remove: (...names: string[]) => this.updateClasses(names, false),
  };

  constructor(readonly tagName: string) {}

  append(...nodes: (ViewTestElement | string)[]): void {
    for (const node of nodes) {
      if (typeof node === 'string') continue;
      node.parent = this;
      this.children.push(node);
    }
  }

  remove(): void {
    this.removeCalls += 1;
    if (!this.parent) return;
    this.parent.children = this.parent.children.filter((child) => child !== this);
    this.parent = null;
  }

  querySelector(selector: string): ViewTestElement | null {
    const className = selector.startsWith('.') ? selector.slice(1) : '';
    for (const child of this.children) {
      if (className && child.className.split(/\s+/).includes(className)) return child;
      const nested = child.querySelector(selector);
      if (nested) return nested;
    }
    return null;
  }

  private updateClasses(names: string[], add: boolean): void {
    const classes = new Set(this.className.split(/\s+/).filter(Boolean));
    for (const name of names) {
      if (add) classes.add(name);
      else classes.delete(name);
    }
    this.className = [...classes].join(' ');
  }
}

function receiptFixture(): GiftReceipt {
  return {
    id: 'receipt-1', time: 1_700_000_000, giftId: 1, giftName: '测试礼物', num: 1,
    price: 100, totalCoin: 100, coinType: 'gold', uname: '测试观众',
    animation: { webp: 'animation.webp', durationMs: 2400 }, effects: [],
  };
}

describe('gift clip studio view', () => {
  let documentStub: { createElement: (tagName: string) => ViewTestElement };
  let host: ViewTestElement;

  beforeEach(() => {
    host = new ViewTestElement('host');
    documentStub = { createElement: (tagName) => new ViewTestElement(tagName) };
    vi.stubGlobal('document', documentStub as unknown as Document);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('renders the dialog, square stage, media elements, and four action buttons in the existing order', () => {
    const view = createGiftClipStudioView(host as unknown as HTMLElement, receiptFixture());

    expect(view.overlay.className).toBe('overlay gift-clip-overlay');
    expect(view.overlay.role).toBe('dialog');
    expect(view.stage.className).toBe('gift-clip-stage');
    expect(view.sourceCanvas.className).toBe('gift-clip-canvas');
    expect(view.recordingCanvas.className).toBe('gift-clip-recording-canvas');
    expect(view.preview.className).toBe('gift-clip-video');
    expect(view.overlay.querySelector('.gift-clip-progress')).not.toBeNull();
    expect(view.overlay.querySelector('.gift-clip-status')).not.toBeNull();
    expect(Array.from(view.overlay.querySelector('.gift-clip-actions')!.children).map((child) => child.textContent))
      .toEqual(['恢复完整画面', '确定剪裁并生成', '重新剪裁', '保存视频']);
  });

  it('updates only view state for loading, editing, encoding, ready, and failure', () => {
    const fetchMock = vi.fn();
    const addListener = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('addEventListener', addListener);
    const view = createGiftClipStudioView(host as unknown as HTMLElement, receiptFixture());
    const progress = view.overlay.querySelector('.gift-clip-progress') as HTMLProgressElement;
    const status = view.overlay.querySelector('.gift-clip-status')!;

    view.showLoading();
    expect({ status: status.textContent, progressHidden: progress.hidden, saveDisabled: view.saveButton.disabled })
      .toEqual({ status: '正在读取礼物动画…', progressHidden: true, saveDisabled: true });
    view.showEditing('剪裁 640 × 360 · 成片按原始像素输出');
    expect({ status: status.textContent, resetHidden: view.resetButton.hidden, confirmHidden: view.confirmButton.hidden })
      .toEqual({ status: '剪裁 640 × 360 · 成片按原始像素输出', resetHidden: false, confirmHidden: false });
    view.showEncoding('正在生成视频', 42);
    expect({ status: status.textContent, progressValue: progress.value, progressHidden: progress.hidden })
      .toEqual({ status: '正在生成视频', progressValue: 42, progressHidden: false });
    view.showReady('WEBM 已生成', '保存 WEBM');
    expect({ status: status.textContent, save: view.saveButton.textContent, saveHidden: view.saveButton.hidden, saveDisabled: view.saveButton.disabled })
      .toEqual({ status: 'WEBM 已生成', save: '保存 WEBM', saveHidden: false, saveDisabled: false });
    view.showFailure('视频录制失败，请重试。', '重试');
    expect({ status: status.textContent, error: status.className.includes('is-error'), retry: view.reeditButton.textContent, retryHidden: view.reeditButton.hidden })
      .toEqual({ status: '视频录制失败，请重试。', error: true, retry: '重试', retryHidden: false });
    expect(fetchMock).not.toHaveBeenCalled();
    expect(addListener).not.toHaveBeenCalled();
  });
});
