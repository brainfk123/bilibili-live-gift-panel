import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build as viteBuild } from 'vite';

const projectRoot = fileURLToPath(new URL('..', import.meta.url));
const distRoot = join(projectRoot, 'dist');

/** Recursively list every file under dir (empty array when dir is missing). */
function listFilesRecursively(dir: string): string[] {
  if (!existsSync(dir)) return [];
  const files: string[] = [];
  const walk = (current: string): void => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const full = join(current, entry.name);
      if (entry.isDirectory()) walk(full);
      else files.push(full);
    }
  };
  walk(dir);
  return files;
}

/** Snapshot of files (path, size, mtime) used to prove the real dist is never written. */
type FileSnapshot = [path: string, size: number, mtimeMs: number];

function snapshotFiles(files: string[]): FileSnapshot[] {
  return files.map((file) => {
    const stat = statSync(file);
    return [file, stat.size, stat.mtimeMs];
  });
}

describe('single-file production output', () => {
  let outDir: string;
  let html: string;
  let staticStyles: string[];
  const distBefore: FileSnapshot[] = [];

  beforeAll(async () => {
    distBefore.push(...snapshotFiles(listFilesRecursively(distRoot)));
    outDir = mkdtempSync(join(tmpdir(), 'bilibili-singlefile-'));
    await viteBuild({
      root: projectRoot,
      configFile: join(projectRoot, 'vite.config.ts'),
      logLevel: 'silent',
      build: { outDir },
    });
    const indexFile = listFilesRecursively(outDir).find((file) => file.endsWith('index.html'));
    if (!indexFile) throw new Error('build produced no index.html');
    html = readFileSync(indexFile, 'utf8');
    staticStyles = [...html.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((match) => match[1]);
  });

  afterAll(() => {
    rmSync(outDir, { recursive: true, force: true });
  });

  it('builds into a temp dir and produces only index.html, leaving the real dist untouched', () => {
    const built = listFilesRecursively(outDir).map((file) => relative(outDir, file).replaceAll('\\', '/'));
    expect(built).toEqual(['index.html']);
    expect(snapshotFiles(listFilesRecursively(distRoot))).toEqual(distBefore);
  });

  it('inlines every asset: the HTML has no external stylesheet link', () => {
    expect(html).not.toMatch(/<link\b[^>]*\brel=["']stylesheet["']/i);
  });

  it('keeps configuration CSS out of the static styles', () => {
    expect(staticStyles.some((style) => style.includes('.config-root'))).toBe(false);
    expect(staticStyles.some((style) => /data-theme\s*=\s*"?light"?/.test(style))).toBe(false);
  });

  it('still ships configuration CSS through the runtime text-injection path', () => {
    // Config CSS is loaded as a text module (?inline) in config mode only. The
    // minified `[data-theme=light]` selector is unique to config.css, so its
    // presence anywhere in the bundle proves the CSS text still travels as a
    // runtime-injected string rather than being dropped.
    expect(html).toMatch(/data-theme\s*=\s*"?light"?/);
    expect(html).toContain('.config-root');
  });
});
