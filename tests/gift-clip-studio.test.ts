import { describe, expect, it, vi } from 'vitest';
import {
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
