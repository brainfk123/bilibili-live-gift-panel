import { describe, expect, it } from 'vitest';
import type { GiftReceipt } from '../src/types';
import { createGiftClipExportLayers } from '../src/ui/config/gift-clip-export-layers';

const png = new Blob(['png'], { type: 'image/png' });

function receiptFixture(): GiftReceipt {
  return {
    id: 'receipt-1', time: 1_700_000_000, giftId: 1, giftName: '测试礼物', num: 1,
    price: 100, totalCoin: 100, coinType: 'gold', uname: '测试观众', effects: [],
  };
}

type FakeCanvas = HTMLCanvasElement & { readonly calls: string[]; readonly exportedSizes: Array<[number, number]>; resolveBlob(): void };

function fakeCanvas(blob: Blob | null = png, delay = false): FakeCanvas {
  const calls: string[] = [];
  const exportedSizes: Array<[number, number]> = [];
  let callback: BlobCallback | undefined;
  let canvas: FakeCanvas;
  const context = {
    canvas: {} as HTMLCanvasElement,
    createLinearGradient: () => ({ addColorStop() {} }), createRadialGradient: () => ({ addColorStop() {} }),
    fillRect() { calls.push('fillRect'); }, clearRect() { calls.push('clearRect'); }, drawImage() {},
    save() {}, restore() {}, beginPath() {}, roundRect() {}, arc() {}, clip() {}, fill() {}, stroke() {},
    fillText() { calls.push('fillText'); }, measureText: (text: string) => ({ width: text.length * 8 }),
  } as unknown as CanvasRenderingContext2D;
  canvas = {
    width: 0,
    height: 0,
    calls,
    exportedSizes,
    getContext: () => context,
    toBlob: (next: BlobCallback) => {
      callback = next;
      exportedSizes.push([canvas.width, canvas.height]);
      if (!delay) next(blob);
    },
    resolveBlob: () => callback?.(blob),
  } as unknown as FakeCanvas;
  Object.assign(context.canvas, canvas);
  return canvas;
}

function fakeDocument(canvases: FakeCanvas[]): Document {
  const supplied = [...canvases];
  return { createElement: () => supplied.shift()! } as unknown as Document;
}

describe('gift clip export layers', () => {
  it('exports opaque background and transparent information PNG layers at the requested size', async () => {
    const createdCanvases = [fakeCanvas(), fakeCanvas()];
    await expect(createGiftClipExportLayers({
      width: 960, height: 540, receipt: receiptFixture(), avatar: null, document: fakeDocument(createdCanvases),
    })).resolves.toEqual({ background: expect.any(Blob), overlay: expect.any(Blob) });
    expect(createdCanvases.map(({ exportedSizes }) => exportedSizes)).toEqual([[[960, 540]], [[960, 540]]]);
    expect(createdCanvases.map(({ width, height }) => [width, height])).toEqual([[0, 0], [0, 0]]);
    expect(createdCanvases[0].calls).toContain('fillRect');
    expect(createdCanvases[0].calls).not.toContain('fillText');
    expect(createdCanvases[1].calls).toContain('clearRect');
    expect(createdCanvases[1].calls).toContain('fillText');
  });

  it.each([null, new Blob(['not a png'], { type: 'image/jpeg' }), new Blob([], { type: 'image/png' })])(
    'rejects invalid PNG output with a stable Chinese error',
    async (blob) => {
      await expect(createGiftClipExportLayers({
        width: 960, height: 540, receipt: receiptFixture(), avatar: null,
        document: fakeDocument([fakeCanvas(blob), fakeCanvas()]),
      })).rejects.toThrow('视频图层生成失败，请重试。');
    },
  );

  it('rejects missing 2D contexts with the same Chinese error and releases both canvases', async () => {
    const first = fakeCanvas();
    const second = fakeCanvas();
    (first as unknown as { getContext(): null }).getContext = () => null;
    await expect(createGiftClipExportLayers({
      width: 960, height: 540, receipt: receiptFixture(), avatar: null, document: fakeDocument([first, second]),
    })).rejects.toThrow('视频图层生成失败，请重试。');
    expect([first.width, first.height, second.width, second.height]).toEqual([0, 0, 0, 0]);
  });

  it('waits for both PNG callbacks before rejecting and then releases both canvases', async () => {
    const first = fakeCanvas(null);
    const second = fakeCanvas(png, true);
    const exportPromise = createGiftClipExportLayers({
      width: 960, height: 540, receipt: receiptFixture(), avatar: null, document: fakeDocument([first, second]),
    });
    expect([first.width, first.height, second.width, second.height]).toEqual([960, 540, 960, 540]);
    second.resolveBlob();
    await expect(exportPromise).rejects.toThrow('视频图层生成失败，请重试。');
    expect([first.width, first.height, second.width, second.height]).toEqual([0, 0, 0, 0]);
  });
});
