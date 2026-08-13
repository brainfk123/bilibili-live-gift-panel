import assert from 'node:assert/strict';

export function assertGiftClipExportProbe(kind, probe) {
  assert.equal(probe.codec, 'h264', `${kind} codec`);
  assert.equal(probe.pixelFormat, 'yuv420p', `${kind} pixel format`);
  assert.equal(probe.fps, '30/1', `${kind} fps`);
  assert.equal(probe.frames, 60, `${kind} frames`);
  assert.equal(probe.width, 320, `${kind} width`);
  assert.equal(probe.height, 180, `${kind} height`);
  assert.equal(probe.audioStreams, 0, `${kind} audio streams`);
  assert.ok(Math.abs(probe.duration - 2) <= 0.05, `${kind} duration=${probe.duration}`);
  assert.ok(Number.isFinite(probe.bitrate) && probe.bitrate > 0, `${kind} bitrate=${probe.bitrate}`);
  assert.ok(Number.isFinite(probe.size) && probe.size >= 1024 && probe.size < 1024 * 1024, `${kind} size=${probe.size}`);
  const targetBitrate = 150_000;
  const targetBytes = targetBitrate * probe.duration / 8;
  const actualBytes = probe.bitrate * probe.duration / 8;
  const minimumBytes = targetBytes * 0.65;
  const maximumBytes = targetBytes * 1.35 + 24 * 1024;
  assert.ok(
    actualBytes >= minimumBytes && actualBytes <= maximumBytes,
    `${kind} video bytes=${actualBytes} from ${probe.bitrate}bit/s, want ${minimumBytes}..${maximumBytes} around unchanged ${targetBitrate}bit/s plus bounded 24KiB startup/GOP overhead`,
  );
}
