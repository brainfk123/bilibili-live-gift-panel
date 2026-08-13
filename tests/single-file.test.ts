import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, relative, resolve } from 'node:path';
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

function emittedImportTargets(file: string): string[] {
  const source = readFileSync(file, 'utf8');
  const specifiers = [...source.matchAll(/(?:from\s*|import\s*)["']([^"']+)["']/g)].map((match) => match[1]);
  return specifiers
    .filter((specifier) => specifier.startsWith('.'))
    .map((specifier) => resolve(dirname(file), specifier));
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
    expect(built).toContain('index.html');
    expect(built.some((file) => file.includes('config-entry-') && file.endsWith('.js'))).toBe(true);
    expect(snapshotFiles(listFilesRecursively(distRoot))).toEqual(distBefore);
  });

  it('inlines every asset: the HTML has no external stylesheet link', () => {
    expect(html).not.toMatch(/<link\b[^>]*\brel=["']stylesheet["']/i);
  });

  it('keeps configuration CSS out of the static styles', () => {
    expect(staticStyles.some((style) => style.includes('.config-root'))).toBe(false);
    expect(staticStyles.some((style) => /data-theme\s*=\s*"?light"?/.test(style))).toBe(false);
  });

  it('keeps configuration-only ingestion warnings out of the OBS entry artifact', () => {
    expect(html).not.toContain('gift-ingestion-warning');
    expect(html).not.toContain('连接中断期间可能漏礼物');
    const chunks = listFilesRecursively(outDir).filter((file) => file.endsWith('.js'));
    expect(chunks.some((file) => readFileSync(file, 'utf8').includes('gift-ingestion-warning'))).toBe(true);
  });

  it('keeps every configuration chunk dependency executable in the emitted graph', async () => {
    const configEntry = listFilesRecursively(outDir).find((file) => (
      file.includes('config-entry-') && file.endsWith('.js')
    ));
    if (!configEntry) throw new Error('build produced no configuration entry chunk');
    const seen = new Set<string>();
    const visit = (file: string): void => {
      if (seen.has(file)) return;
      seen.add(file);
      expect(existsSync(file), `missing emitted import ${relative(outDir, file)}`).toBe(true);
      for (const target of emittedImportTargets(file)) visit(target);
    };
    visit(configEntry);

    // The project deliberately runs Vitest in node and ships no jsdom. Supply
    // the small browser surface needed for module evaluation so this exercises
    // the emitted entry rather than only inspecting its source.
    const previousDocument = globalThis.document;
    const element = () => ({
      style: {}, dataset: {}, children: [], append() {}, appendChild() {},
      setAttribute() {}, getAttribute() { return null; }, classList: { add() {}, remove() {}, toggle() { return false; } },
      content: { firstElementChild: null },
    });
    Object.defineProperty(globalThis, 'document', {
      configurable: true,
      value: { createElement: element, createElementNS: element, getElementById: () => null, querySelectorAll: () => [], head: element(), body: element() },
    });
    const previousMutationObserver = globalThis.MutationObserver;
    Object.defineProperty(globalThis, 'MutationObserver', {
      configurable: true,
      value: class { observe() {} disconnect() {} },
    });
    try {
      // Vitest expects a filesystem id here. pathToFileURL encodes `~` in
      // Windows 8.3 temp paths as `%7E`, which Vite does not decode.
      const config = await import(configEntry.replaceAll('\\', '/'));
      expect(config.configStyles).toContain('.config-root');
      expect(config.mountConfigEntry).toBeTypeOf('function');
    } finally {
      Object.defineProperty(globalThis, 'document', { configurable: true, value: previousDocument });
      Object.defineProperty(globalThis, 'MutationObserver', { configurable: true, value: previousMutationObserver });
    }
  });
});
