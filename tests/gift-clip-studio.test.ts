import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  constrainGiftClipPlacement,
  giftClipAnimationKey,
  giftClipCoverRect,
  giftClipPlacedCoverRect,
  normalizeGiftClipDuration,
  normalizeGiftEffectLayout,
  giftEffectDurationMs,
  giftGifFrameIndex,
  sanitizeGiftClipFilename,
  selectGiftClipRecorder,
  stopGiftClipStream,
  triggerGiftClipDownload,
} from '../src/ui/config/gift-clip-studio';

describe('gift clip studio', () => {
  const source = readFileSync(new URL('../src/ui/config/gift-clip-studio.ts', import.meta.url), 'utf8');

  it('clamps missing and abnormal animation durations', () => {
    expect(normalizeGiftClipDuration(undefined)).toBe(3000);
    expect(normalizeGiftClipDuration(200)).toBe(1000);
    expect(normalizeGiftClipDuration(2200)).toBe(2200);
    expect(normalizeGiftClipDuration(60_000)).toBe(15_000);
  });

  it('accepts packed-alpha effect coordinates and derives the real effect duration', () => {
    const layout = normalizeGiftEffectLayout({
      videoWidth: 1088,
      videoHeight: 1280,
      rgbFrame: [0, 0, 720, 1280],
      alphaFrame: [724, 0, 360, 640],
      fps: 30,
      frames: 390,
    });
    expect(layout.rgbFrame).toEqual([0, 0, 720, 1280]);
    expect(layout.alphaFrame).toEqual([724, 0, 360, 640]);
    expect(giftEffectDurationMs(layout)).toBe(13_000);
  });

  it('selects deterministic GIF frames and loops by frame delays', () => {
    const delays = [220, 220, 220];
    expect(giftGifFrameIndex(delays, 0)).toBe(0);
    expect(giftGifFrameIndex(delays, 219)).toBe(0);
    expect(giftGifFrameIndex(delays, 220)).toBe(1);
    expect(giftGifFrameIndex(delays, 500)).toBe(2);
    expect(giftGifFrameIndex(delays, 660)).toBe(0);
  });

  it('uses cover scaling so landscape and portrait animations fill the square canvas', () => {
    expect(giftClipCoverRect(320, 180)).toEqual({ x: -186.66666666666663, y: 0, width: 853.3333333333333, height: 480 });
    expect(giftClipCoverRect(180, 320)).toEqual({ x: 0, y: -186.66666666666663, width: 480, height: 853.3333333333333 });
    expect(giftClipCoverRect(480, 480)).toEqual({ x: 0, y: 0, width: 480, height: 480 });
  });

  it('keeps a stable placement key for the same animation and ignores signed URL queries', () => {
    expect(giftClipAnimationKey({
      giftId: 1,
      animation: { gif: 'https://i0.hdslb.com/gift/heart.gif?token=one', durationMs: 3000 },
    })).toBe(giftClipAnimationKey({
      giftId: 2,
      animation: { gif: 'https://i0.hdslb.com/gift/heart.gif?token=two', durationMs: 5000 },
    }));
    expect(giftClipAnimationKey({
      giftId: 1,
      animation: { durationMs: 3000, effectId: 99 },
    })).toBe('effect:99');
  });

  it('allows useful drag travel even when the animation canvas itself is square', () => {
    expect(constrainGiftClipPlacement(480, 480, { x: 999, y: -999 }))
      .toEqual({ x: 120, y: -120 });
    expect(constrainGiftClipPlacement(180, 320, { x: 100, y: 300 }))
      .toEqual({ x: 100, y: 160 });
  });

  it('adds only enough overscan to keep a shifted animation covering the video', () => {
    expect(giftClipPlacedCoverRect(480, 480, { x: 0, y: 80 }))
      .toEqual({ x: -80, y: 0, width: 640, height: 640 });
    expect(giftClipPlacedCoverRect(480, 480, { x: 0, y: 0 }))
      .toEqual({ x: 0, y: 0, width: 480, height: 480 });
  });

  it('keeps the preparing label in the setup placeholder and out of recorded frames', () => {
    const placeholderStart = source.indexOf('function drawGiftClipPlaceholder');
    const frameStart = source.indexOf('function drawGiftClipFrame');
    const roundedRectStart = source.indexOf('function roundedRect');
    expect(source.slice(placeholderStart, frameStart)).toContain("fillText('正在准备礼物动画'");
    expect(source.slice(frameStart, roundedRectStart)).not.toContain('正在准备礼物动画');
  });

  it('keeps the sender information bar translucent over the animation', () => {
    expect(source).toContain("barGradient.addColorStop(0, 'rgba(87, 39, 101, .76)')");
    expect(source).toContain("barGradient.addColorStop(1, 'rgba(224, 68, 129, .76)')");
  });

  it('rejects packed-alpha coordinates outside the video', () => {
    expect(() => normalizeGiftEffectLayout({
      videoWidth: 1088,
      videoHeight: 1280,
      rgbFrame: [0, 0, 1200, 1280],
      alphaFrame: [724, 0, 360, 640],
      fps: 30,
      frames: 390,
    })).toThrow('礼物特效坐标无效');
  });

  it('sanitizes video filenames without including the sender UID', () => {
    const filename = sanitizeGiftClipFilename({
      giftName: '心动/盲盒:*?', uname: '观众<测试>|', time: 1_700_000_000,
    }, 'mp4');
    expect(filename).toMatch(/^心动-盲盒----观众-测试---\d{8}-\d{6}\.mp4$/);
    expect(filename).not.toContain('UID');
  });

  it('prefers MP4 and falls back to WebM when MP4 construction fails', () => {
    class FakeRecorder {
      static isTypeSupported = vi.fn(() => true);
      mimeType: string;

      constructor(_stream: MediaStream, options?: MediaRecorderOptions) {
        this.mimeType = options?.mimeType ?? '';
        if (this.mimeType.includes('mp4')) throw new Error('MP4 unavailable');
      }
    }
    const selection = selectGiftClipRecorder({} as MediaStream, FakeRecorder as unknown as typeof MediaRecorder);
    expect(selection.extension).toBe('webm');
    expect(selection.mimeType).toContain('video/webm');
    expect(FakeRecorder.isTypeSupported).toHaveBeenCalledWith('video/mp4;codecs=avc1.42E01E');
  });

  it('reports an actionable error when MediaRecorder is unavailable', () => {
    expect(() => selectGiftClipRecorder({} as MediaStream, undefined))
      .toThrow('当前浏览器不支持录制 Canvas，请更新程序后重试。');
  });

  it('stops every canvas capture track during cleanup', () => {
    const tracks = [{ stop: vi.fn() }, { stop: vi.fn() }];
    stopGiftClipStream({ getTracks: () => tracks as unknown as MediaStreamTrack[] });
    expect(tracks.every((track) => track.stop.mock.calls.length === 1)).toBe(true);
  });

  it('downloads through a temporary anchor with a sanitized filename', () => {
    const anchor = { href: '', download: '', click: vi.fn(), remove: vi.fn() };
    const append = vi.fn();
    const targetDocument = {
      createElement: vi.fn(() => anchor),
      body: { append },
    } as unknown as Document;
    triggerGiftClipDownload('blob:fixture', '礼物回放.mp4', targetDocument);
    expect(anchor.href).toBe('blob:fixture');
    expect(anchor.download).toBe('礼物回放.mp4');
    expect(append).toHaveBeenCalledWith(anchor);
    expect(anchor.click).toHaveBeenCalledOnce();
    expect(anchor.remove).toHaveBeenCalledOnce();
  });
});
