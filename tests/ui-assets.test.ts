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
    mkdirSync(join(target, 'chunks'), { recursive: true });
    writeFileSync(join(source, 'index.html'), '<script type="module" src="./chunks/config-entry-abc.js"></script>');
    writeFileSync(join(source, 'chunks', 'config-entry-abc.js'), 'export const config = true;');
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
      expect(existsSync(join(target, 'stale.js'))).toBe(false);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
