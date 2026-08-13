import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  GIFT_CLIP_TEST_TOOL_PROVENANCE,
  verifyGiftClipTestTools,
} from '../scripts/gift-clip-test-tools.mjs';

const ffmpegBytes = 'fake ffmpeg 9.0';
const ffprobeBytes = 'fake ffprobe 9.0';

function writeToolFixture(root: string) {
  mkdirSync(root, { recursive: true });
  writeFileSync(join(root, 'ffmpeg.exe'), ffmpegBytes);
  writeFileSync(join(root, 'ffprobe.exe'), ffprobeBytes);
  writeFileSync(join(root, 'manifest.json'), `${JSON.stringify({
    schema: 1,
    ...GIFT_CLIP_TEST_TOOL_PROVENANCE,
    binaries: {
      ffmpeg: {
        file: 'ffmpeg.exe',
        size: 15,
        sha256: '82257e60a1c88be55d95cb073e46418fa3159fca6f158242bf1dab9e3f9f94d7',
      },
      ffprobe: {
        file: 'ffprobe.exe',
        size: 16,
        sha256: '9ab1b5dc282ff2c54333e1a662c80441554e2cb640bb0076f79b3f9fafcc5aab',
      },
    },
  }, null, 2)}\n`);
}

describe('gift clip test-tool provenance', () => {
  it('gives pinned release endpoints enough time and retries transient connection failures', () => {
    const script = readFileSync(new URL('../scripts/build-ffmpeg.ps1', import.meta.url), 'utf8');
    expect(script).toMatch(/--connect-timeout\s+60\b/);
    expect(script).toMatch(/--max-time\s+180\b/);
    expect(script).toMatch(/--retry\s+2\b/);
  });

  it('returns absolute verified FFmpeg 9.0 tool paths', () => {
    const root = mkdtempSync(join(tmpdir(), 'gift-clip-test-tools-'));
    try {
      writeToolFixture(root);
      const tools = verifyGiftClipTestTools(root, {
        runVersion: (path: string) => `${path.endsWith('ffprobe.exe') ? 'ffprobe' : 'ffmpeg'} version 9.0 Copyright fixture`,
      });
      expect(tools).toEqual({
        ffmpeg: join(root, 'ffmpeg.exe'),
        ffprobe: join(root, 'ffprobe.exe'),
      });
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('rejects a tampered binary even when its manifest size is unchanged', () => {
    const root = mkdtempSync(join(tmpdir(), 'gift-clip-test-tools-'));
    try {
      writeToolFixture(root);
      writeFileSync(join(root, 'ffmpeg.exe'), 'fake ffmpeg 8.0');
      expect(() => verifyGiftClipTestTools(root, { runVersion: () => 'ffmpeg version 9.0' }))
        .toThrow(/ffmpeg SHA-256/i);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('rejects a manifest that is not tied to the pinned source and toolchain', () => {
    const root = mkdtempSync(join(tmpdir(), 'gift-clip-test-tools-'));
    try {
      writeToolFixture(root);
      const manifestPath = join(root, 'manifest.json');
      const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
      manifest.sourceSha256 = '0'.repeat(64);
      writeFileSync(manifestPath, `${JSON.stringify(manifest)}\n`);
      expect(() => verifyGiftClipTestTools(root, { runVersion: () => 'ffmpeg version 9.0' }))
        .toThrow(/source provenance/i);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
