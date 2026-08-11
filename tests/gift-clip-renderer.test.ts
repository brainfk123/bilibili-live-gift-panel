import { describe, expect, it, vi } from 'vitest';
import type { GiftReceipt } from '../src/types';
import {
  drawGiftClipOutputFrame,
  giftClipInfoBarLayout,
  prepareGiftClipOutputCanvas,
} from '../src/ui/config/gift-clip-renderer';

function createGiftClipContextStub(options: { width: number; height: number; drawImage: ReturnType<typeof vi.fn> }): CanvasRenderingContext2D {
  const gradient = { addColorStop: vi.fn() };
  return {
    canvas: { width: options.width, height: options.height },
    createLinearGradient: vi.fn(() => gradient),
    createRadialGradient: vi.fn(() => gradient),
    fillRect: vi.fn(),
    clearRect: vi.fn(),
    drawImage: options.drawImage,
    save: vi.fn(),
    restore: vi.fn(),
    beginPath: vi.fn(),
    roundRect: vi.fn(),
    arc: vi.fn(),
    clip: vi.fn(),
    fill: vi.fn(),
    stroke: vi.fn(),
    fillText: vi.fn(),
    measureText: vi.fn((text: string) => ({ width: text.length * 8 })),
  } as unknown as CanvasRenderingContext2D;
}

const receiptFixture = (): GiftReceipt => ({
  id: 'receipt-1', time: 1_700_000_000, giftId: 1, giftName: '测试礼物', num: 1,
  price: 100, totalCoin: 100, coinType: 'gold', uname: '测试观众', effects: [],
});

describe('gift clip renderer', () => {
  it('sizes the recording canvas to the original crop pixels', () => {
    const canvas = { width: 480, height: 480 } as HTMLCanvasElement;
    prepareGiftClipOutputCanvas(canvas, { x: 64, y: 32, width: 512, height: 256 });
    expect({ width: canvas.width, height: canvas.height }).toEqual({ width: 512, height: 256 });
  });

  it('scales the information bar from output width and ignores height', () => {
    expect(giftClipInfoBarLayout(960, 240)).toEqual({
      scale: 2,
      x: 36,
      y: 20,
      width: 888,
      height: 180,
      radius: 44,
      borderWidth: 3,
      avatarX: 134,
      avatarY: 110,
      avatarRadius: 60,
      avatarBorderWidth: 4,
      fallbackFontSize: 48,
      textX: 228,
      nameY: 98,
      giftY: 152,
      maxTextWidth: 604,
      nameFontSize: 40,
      giftFontSize: 34,
    });
    expect(giftClipInfoBarLayout(960, 960)).toEqual(expect.objectContaining({
      scale: 2,
      y: 740,
      height: 180,
      avatarY: 830,
      nameY: 818,
      giftY: 872,
    }));
  });

  it('draws the selected source pixels to matching output pixels', () => {
    const drawImage = vi.fn();
    const context = createGiftClipContextStub({ width: 320, height: 180, drawImage });
    const visual = { source: {} as CanvasImageSource, width: 640, height: 360 };
    drawGiftClipOutputFrame(context, receiptFixture(), visual, null, { x: 80, y: 40, width: 320, height: 180 });
    expect(drawImage).toHaveBeenCalledWith(visual.source, 80, 40, 320, 180, 0, 0, 320, 180);
  });
});
