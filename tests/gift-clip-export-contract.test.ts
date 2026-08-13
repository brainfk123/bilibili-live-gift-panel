import { describe, expect, it } from 'vitest';
import { assertGiftClipExportProbe } from '../scripts/gift-clip-export-contract.mjs';

const validProbe = {
  codec: 'h264',
  pixelFormat: 'yuv420p',
  fps: '30/1',
  frames: 60,
  duration: 2,
  width: 320,
  height: 180,
  audioStreams: 0,
  bitrate: 180_000,
  size: 46_000,
};

describe('gift clip browser export contract', () => {
  it('rejects a decoded output that is not yuv420p', () => {
    expect(() => assertGiftClipExportProbe('fixture', { ...validProbe, pixelFormat: 'yuv444p' }))
      .toThrow(/pixel format/i);
  });

  it('accepts the unchanged 150 kbit\/s profile with bounded short-clip overhead', () => {
    expect(() => assertGiftClipExportProbe('fixture', validProbe)).not.toThrow();
  });
});
