import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

describe('embedded UI asset mirroring', () => {
  it('copies nested chunks and emits a verified manifest without stale files', () => {
    const root = mkdtempSync(join(tmpdir(), 'gift-panel-ui-assets-'));
    const source = join(root, 'source');
    const target = join(root, 'target');
    mkdirSync(join(source, 'chunks'), { recursive: true });
    mkdirSync(join(source, 'ffmpeg'), { recursive: true });
    mkdirSync(join(source, 'gift-clip-test-tools'), { recursive: true });
    mkdirSync(join(source, 'msys2-toolchain-root', 'ucrt64', 'bin'), { recursive: true });
    mkdirSync(join(target, 'chunks'), { recursive: true });
    writeFileSync(join(source, 'index.html'), '<script type="module" src="./chunks/config-entry-abc.js"></script>');
    writeFileSync(join(source, 'chunks', 'config-entry-abc.js'), 'export const config = true;');
    writeFileSync(join(source, 'ffmpeg', 'ffmpeg.exe'), 'must not be embedded');
    writeFileSync(join(source, 'gift-clip-test-tools', 'ffprobe.exe'), 'test tool must not be embedded');
    writeFileSync(join(source, 'msys2-toolchain-root', 'ucrt64', 'bin', 'c++.exe'), 'build tool must not be embedded');
    writeFileSync(join(source, 'gift-panel.exe'), 'stale executable');
    writeFileSync(join(target, 'stale.js'), 'must be removed');

    try {
      const moduleURL = new URL('../scripts/ui-assets.mjs', import.meta.url).href;
      const script = `
        import { mirrorUiAssets } from ${JSON.stringify(moduleURL)};
        const manifest = mirrorUiAssets(${JSON.stringify(source)}, ${JSON.stringify(target)});
        process.stdout.write(JSON.stringify(manifest));
      `;
      const manifest = JSON.parse(execFileSync(process.execPath, ['--input-type=module', '-e', script], { encoding: 'utf8' }));
      expect(manifest).toEqual({
        version: 1,
        files: [
          { path: 'chunks/config-entry-abc.js', size: 27, sha256: expect.any(String) },
          { path: 'index.html', size: 66, sha256: expect.any(String) },
        ],
      });
      expect(readFileSync(join(target, 'chunks', 'config-entry-abc.js'), 'utf8')).toBe('export const config = true;');
      expect(readFileSync(join(target, 'ui-assets.json'), 'utf8')).toContain('config-entry-abc.js');
      expect(existsSync(join(target, 'ffmpeg'))).toBe(false);
      expect(existsSync(join(target, 'gift-clip-test-tools'))).toBe(false);
      expect(existsSync(join(target, 'msys2-toolchain-root'))).toBe(false);
      expect(existsSync(join(target, 'gift-panel.exe'))).toBe(false);
      expect(existsSync(join(target, 'stale.js'))).toBe(false);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('excludes release staging artifacts from the embedded UI manifest', () => {
    const root = mkdtempSync(join(tmpdir(), 'gift-panel-ui-assets-release-'));
    const source = join(root, 'source');
    const target = join(root, 'target');
    mkdirSync(join(source, 'ffmpeg-component-download'), { recursive: true });
    mkdirSync(join(source, 'ffmpeg-component-published'), { recursive: true });
    mkdirSync(join(source, 'ffmpeg-component-publish'), { recursive: true });
    mkdirSync(join(source, 'ffmpeg-component'), { recursive: true });
    mkdirSync(join(source, 'release-ffmpeg-sealed'), { recursive: true });
    mkdirSync(join(source, 'standalone-ffmpeg'), { recursive: true });
    writeFileSync(join(source, 'index.html'), '<main>gift panel</main>');
    writeFileSync(join(source, 'ffmpeg-component-download', 'ffmpeg.zip'), 'signed component');
    writeFileSync(join(source, 'ffmpeg-component-published', 'ffmpeg.zip'), 'new component');
    writeFileSync(join(source, 'ffmpeg-component-publish', 'ffmpeg.zip'), 'component staging');
    writeFileSync(join(source, 'ffmpeg-component', 'ffmpeg-9.0.tar.xz'), 'reviewed component source');
    writeFileSync(join(source, 'release-ffmpeg-sealed', 'ffmpeg.zip'), 'sealed runtime payload');
    writeFileSync(join(source, 'standalone-ffmpeg', 'ffmpeg.exe'), 'standalone executable');
    writeFileSync(join(source, 'ffmpeg-component-release.json'), '{}');
    writeFileSync(join(source, 'ffmpeg-windows-x64.exe'), 'release executable');
    writeFileSync(join(source, 'ffmpeg-windows-x64.exe.sha256'), 'release checksum');
    writeFileSync(join(source, 'gift-clip-test-tools.zip'), 'test tools archive');
    writeFileSync(join(source, 'standalone-component-manifest.json'), '{}');

    try {
      const moduleURL = new URL('../scripts/ui-assets.mjs', import.meta.url).href;
      const script = `
        import { mirrorUiAssets } from ${JSON.stringify(moduleURL)};
        const manifest = mirrorUiAssets(${JSON.stringify(source)}, ${JSON.stringify(target)});
        process.stdout.write(JSON.stringify(manifest));
      `;
      const manifest = JSON.parse(execFileSync(process.execPath, ['--input-type=module', '-e', script], { encoding: 'utf8' }));

      expect(manifest).toEqual({
        version: 1,
        files: [
          { path: 'index.html', size: 23, sha256: expect.any(String) },
        ],
      });
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
