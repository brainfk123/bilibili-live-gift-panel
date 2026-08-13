import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { createServer } from 'vite';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');
const port = 12463;
const stallMs = Math.max(0, Number(process.env.GIFT_CLIP_STALL_MS ?? 180));
const forceFormat = process.env.GIFT_CLIP_FORMAT ?? 'auto';
const [canvasWidth, canvasHeight] = String(process.env.GIFT_CLIP_CANVAS_SIZE ?? '320x180')
  .split('x')
  .map((value) => Math.max(1, Number(value)));
const server = await createServer({
  logLevel: 'error',
  server: { host: '127.0.0.1', port, strictPort: true },
});
const browser = await chromium.launch({ headless: true });

try {
  await server.listen();
  const page = await browser.newPage();
  await page.goto(`http://127.0.0.1:${port}/tests/fixtures/gift-clip-stutter.html`);
  const result = await page.evaluate(async ({ stallDurationMs, format, width, height }) => {
    if (format === 'webm') {
      const NativeRecorder = globalThis.MediaRecorder;
      globalThis.MediaRecorder = class extends NativeRecorder {
        static isTypeSupported(mimeType) {
          return !mimeType.includes('mp4') && NativeRecorder.isTypeSupported(mimeType);
        }
      };
    }
    const { recordGiftClipCanvas } = await import('/src/ui/config/gift-clip-recorder.ts');
    const canvas = document.createElement('canvas');
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext('2d', { willReadFrequently: true });
    if (!context) throw new Error('2D canvas unavailable');

    let stalled = false;
    const recording = await recordGiftClipCanvas({
      canvas,
      durationMs: 600,
      drawFrame(elapsedMs) {
        const logicalFrame = Math.round(elapsedMs / (1000 / 30));
        context.fillStyle = `rgb(${Math.min(250, 20 + logicalFrame * 8)}, 0, 0)`;
        context.fillRect(0, 0, canvas.width, canvas.height);
        if (!stalled && stallDurationMs > 0 && elapsedMs >= 100) {
          stalled = true;
          const stallStartedAt = performance.now();
          while (performance.now() - stallStartedAt < stallDurationMs) {
            // Simulate a costly full-resolution animation frame.
          }
        }
      },
      onProgress() {},
      signal: new AbortController().signal,
    });

    const url = URL.createObjectURL(recording.blob);
    const video = document.createElement('video');
    video.muted = true;
    video.preload = 'auto';
    video.src = url;
    document.body.append(video);
    await new Promise((resolve, reject) => {
      video.onloadedmetadata = resolve;
      video.onerror = () => reject(new Error('recorded video could not be decoded'));
    });
    if (!Number.isFinite(video.duration)) {
      await new Promise((resolve) => {
        video.ontimeupdate = () => {
          video.ontimeupdate = null;
          resolve();
        };
        video.currentTime = Number.MAX_SAFE_INTEGER;
      });
    }
    if (!Number.isFinite(video.duration) || video.duration <= 0 || video.duration > 2) {
      throw new Error(`recorded video has invalid duration ${video.duration}`);
    }

    const sampleCanvas = document.createElement('canvas');
    sampleCanvas.width = 1;
    sampleCanvas.height = 1;
    const sampleContext = sampleCanvas.getContext('2d', { willReadFrequently: true });
    if (!sampleContext) throw new Error('sample canvas unavailable');
    const red = [];
    const sampleCount = Math.min(60, Math.max(1, Math.floor(video.duration * 30) - 1));
    for (let index = 1; index <= sampleCount; index += 1) {
      const time = Math.min(video.duration - 0.001, index / 30);
      await new Promise((resolve) => {
        video.onseeked = resolve;
        video.currentTime = time;
      });
      sampleContext.drawImage(video, 0, 0, 1, 1);
      red.push(sampleContext.getImageData(0, 0, 1, 1).data[0]);
    }
    URL.revokeObjectURL(url);
    video.remove();

    const deltas = red.slice(1).map((value, index) => Math.abs(value - red[index]));
    let frozenRun = 0;
    let maxFrozenRun = 0;
    for (const delta of deltas) {
      frozenRun = delta <= 2 ? frozenRun + 1 : 0;
      maxFrozenRun = Math.max(maxFrozenRun, frozenRun);
    }
    return {
      duration: video.duration,
      mimeType: recording.mimeType,
      red,
      deltas,
      maxJump: Math.max(0, ...deltas),
      maxFrozenRun,
    };
  }, { stallDurationMs: stallMs, format: forceFormat, width: canvasWidth, height: canvasHeight });

  console.log(JSON.stringify(result));
  assert.ok(result.maxJump <= 20, `animation jumped ${result.maxJump} color levels between adjacent frames`);
  assert.ok(result.maxFrozenRun <= 2, `animation froze for ${result.maxFrozenRun + 1} adjacent samples`);
} finally {
  await browser.close();
  await server.close();
}
