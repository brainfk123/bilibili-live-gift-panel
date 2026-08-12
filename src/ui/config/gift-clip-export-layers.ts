import type { GiftReceipt } from '../../types';
import { drawGiftClipBackground, drawGiftClipInfoOverlay } from './gift-clip-renderer';

const LAYER_ERROR = '视频图层生成失败，请重试。';

export interface GiftClipExportLayerOptions {
  readonly width: number;
  readonly height: number;
  readonly receipt: GiftReceipt;
  readonly avatar: HTMLImageElement | null;
  readonly document: Document;
}

function requireContext(canvas: HTMLCanvasElement): CanvasRenderingContext2D {
  const context = canvas.getContext('2d');
  if (!context) throw new Error(LAYER_ERROR);
  return context;
}

function canvasPNG(canvas: HTMLCanvasElement): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => {
      if (blob?.type === 'image/png' && blob.size > 0) resolve(blob);
      else reject(new Error(LAYER_ERROR));
    }, 'image/png');
  });
}

export async function createGiftClipExportLayers(options: GiftClipExportLayerOptions): Promise<{ background: Blob; overlay: Blob }> {
  const backgroundCanvas = options.document.createElement('canvas');
  const overlayCanvas = options.document.createElement('canvas');
  try {
    for (const canvas of [backgroundCanvas, overlayCanvas]) {
      canvas.width = options.width;
      canvas.height = options.height;
    }
    drawGiftClipBackground(requireContext(backgroundCanvas), options.width, options.height);
    drawGiftClipInfoOverlay(requireContext(overlayCanvas), options.receipt, options.avatar, options.width, options.height);

    const results = await Promise.allSettled([canvasPNG(backgroundCanvas), canvasPNG(overlayCanvas)]);
    const failure = results.find((result): result is PromiseRejectedResult => result.status === 'rejected');
    if (failure) throw failure.reason;
    if (results[0].status !== 'fulfilled' || results[1].status !== 'fulfilled') throw new Error(LAYER_ERROR);
    return { background: results[0].value, overlay: results[1].value };
  } finally {
    backgroundCanvas.width = 0;
    backgroundCanvas.height = 0;
    overlayCanvas.width = 0;
    overlayCanvas.height = 0;
  }
}
