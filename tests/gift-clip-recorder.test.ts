import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  recordGiftClipCanvas,
  sanitizeGiftClipFilename,
  selectGiftClipRecorder,
  stopGiftClipStream,
  triggerGiftClipDownload,
} from '../src/ui/config/gift-clip-recorder';

afterEach(() => vi.unstubAllGlobals());

describe('gift clip recorder', () => {
  it('sanitizes video filenames without including the sender UID', () => {
    const timestamp = new Date(2024, 0, 2, 3, 4, 5).getTime();
    const filename = sanitizeGiftClipFilename({
      giftName: '心动/盲盒:*?',
      uname: '观众<测试>|',
      time: timestamp,
    }, 'mp4');

    expect(filename).toBe('心动-盲盒----观众-测试---20240102-030405.mp4');
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

  it('stops the capture stream when drawing throws', async () => {
    const stop = vi.fn();
    class FakeRecorder {
      static isTypeSupported = vi.fn(() => true);
      state: RecordingState = 'inactive';
      mimeType = 'video/webm';
      ondataavailable: ((event: BlobEvent) => void) | null = null;
      onerror: (() => void) | null = null;
      onstop: (() => void) | null = null;

      start(): void { this.state = 'recording'; }
      stop(): void { this.state = 'inactive'; this.onstop?.(); }
    }
    vi.stubGlobal('MediaRecorder', FakeRecorder);
    const canvas = {
      captureStream: () => ({ getTracks: () => [{ stop }] }),
    } as unknown as HTMLCanvasElement;

    await expect(recordGiftClipCanvas({
      canvas,
      durationMs: 1000,
      drawFrame: () => { throw new Error('draw failed'); },
      onProgress: vi.fn(),
      signal: new AbortController().signal,
    })).rejects.toThrow('draw failed');
    expect(stop).toHaveBeenCalledOnce();
  });

  it('cancels RAF handle zero and releases the stream when recording is aborted', async () => {
    const stopTrack = vi.fn();
    const stopRecorder = vi.fn();
    class FakeRecorder {
      static isTypeSupported = vi.fn(() => true);
      state: RecordingState = 'inactive';
      mimeType = 'video/webm';
      ondataavailable: ((event: BlobEvent) => void) | null = null;
      onerror: (() => void) | null = null;
      onstop: (() => void) | null = null;

      start(): void { this.state = 'recording'; }
      stop(): void {
        stopRecorder();
        this.state = 'inactive';
        this.onstop?.();
      }
    }
    const cancelAnimationFrame = vi.fn();
    vi.stubGlobal('MediaRecorder', FakeRecorder);
    vi.stubGlobal('requestAnimationFrame', vi.fn(() => 0));
    vi.stubGlobal('cancelAnimationFrame', cancelAnimationFrame);
    const controller = new AbortController();
    const canvas = {
      captureStream: vi.fn(() => ({ getTracks: () => [{ stop: stopTrack }] })),
    } as unknown as HTMLCanvasElement;

    const recording = recordGiftClipCanvas({
      canvas,
      durationMs: 1000,
      drawFrame: vi.fn(),
      onProgress: vi.fn(),
      signal: controller.signal,
    });
    controller.abort();

    await expect(recording).rejects.toMatchObject({ name: 'AbortError' });
    expect(cancelAnimationFrame).toHaveBeenCalledWith(0);
    expect(stopRecorder).toHaveBeenCalledOnce();
    expect(stopTrack).toHaveBeenCalledOnce();
  });
});
