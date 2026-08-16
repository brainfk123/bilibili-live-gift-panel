import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build as viteBuild, loadConfigFromFile } from 'vite';
import { describe, expect, it } from 'vitest';

const projectRoot = fileURLToPath(new URL('..', import.meta.url));
const hostedFiles = [
  'hosted.html',
  'src/hosted/main.ts',
  'src/hosted/shell.ts',
  'src/hosted/shell.css',
  'vite.hosted.config.ts',
];

function listFilesRecursively(directory: string): string[] {
  const files: string[] = [];
  const walk = (current: string): void => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const file = join(current, entry.name);
      if (entry.isDirectory()) walk(file);
      else files.push(file);
    }
  };
  walk(directory);
  return files;
}

function hostedSourceExists(): boolean {
  return hostedFiles.every((file) => existsSync(join(projectRoot, file)));
}

describe('hosted web build contract', () => {
  it('declares an independent hosted entry, shell, and Vite config', () => {
    expect(hostedFiles.map((file) => existsSync(join(projectRoot, file)))).toEqual([
      true,
      true,
      true,
      true,
      true,
    ]);
  });

  it('adds reproducible hosted build scripts', () => {
    const packageJSON = JSON.parse(readFileSync(join(projectRoot, 'package.json'), 'utf8')) as {
      scripts?: Record<string, string>;
    };

    expect(packageJSON.scripts?.['build:hosted']).toBe('vite build --config vite.hosted.config.ts');
    expect(packageJSON.scripts?.['test:hosted']).toBe('vitest run tests/hosted-build.test.ts');
  });

  it('keeps hosted UI source free of desktop-only imports and browser persistence', () => {
    if (!hostedSourceExists()) {
      expect(hostedSourceExists()).toBe(true);
      return;
    }

    const source = ['src/hosted/main.ts', 'src/hosted/shell.ts']
      .map((file) => readFileSync(join(projectRoot, file), 'utf8'))
      .join('\n');

    expect(source).not.toMatch(/gift-clip|autoUpdate|electron|ffmpeg/i);
    expect(source).not.toMatch(/\bfetch\s*\(|document\.cookie|localStorage\b/i);
  });

  it('renders a signed-out, callback-only shell with an accessible login action', async () => {
    const module = await import('../src/hosted/shell').catch(() => undefined);
    expect(module).toBeDefined();
    if (!module) return;

    class FakeElement {
      readonly children: FakeElement[] = [];
      readonly attributes = new Map<string, string>();
      textContent = '';
      type = '';
      onclick: (() => void) | null = null;

      constructor(readonly tagName: string) {}

      append(...children: FakeElement[]): void {
        this.children.push(...children);
      }

      replaceChildren(...children: FakeElement[]): void {
        this.children.length = 0;
        this.children.push(...children);
      }

      setAttribute(name: string, value: string): void {
        this.attributes.set(name, value);
      }

      addEventListener(type: string, listener: () => void): void {
        if (type === 'click') this.onclick = listener;
      }
    }

    const textContent = (element: FakeElement): string => [
      element.textContent,
      ...element.children.map(textContent),
    ].join(' ');

    const document = {
      createElement: (tagName: string) => new FakeElement(tagName),
    };
    const root = new FakeElement('div') as unknown as HTMLElement;
    Object.defineProperty(root, 'ownerDocument', { value: document });
    let loginCalls = 0;

    module.renderHostedShell(root, {
      serviceStatus: 'ready',
      onLogin: () => { loginCalls += 1; },
    });

    const shell = (root as unknown as FakeElement).children[0];
    const button = shell.children.find((child) => child.tagName === 'button');
    expect(shell.tagName).toBe('main');
    expect(textContent(shell)).toContain('礼物互动工坊');
    expect(textContent(shell)).toContain('仅在登录授权后处理账号信息');
    expect(button?.textContent).toBe('使用 B 站账号登录');
    expect(button?.type).toBe('button');
    expect(button?.attributes.get('aria-label')).toBe('使用 B 站账号登录');
    button?.onclick?.();
    expect(loginCalls).toBe(1);
  });

  it('builds a multi-file hosted asset graph with a manifest and no desktop artifacts', async () => {
    if (!hostedSourceExists()) {
      expect(hostedSourceExists()).toBe(true);
      return;
    }

    const config = await loadConfigFromFile(
      { command: 'build', mode: 'production' },
      join(projectRoot, 'vite.hosted.config.ts'),
    );
    expect(config?.config.build?.outDir).toBe(resolve(projectRoot, 'goserver/cmd/hosted/dist'));
    expect(config?.config.build?.manifest).toBe(true);

    const outDir = mkdtempSync(join(tmpdir(), 'gift-panel-hosted-build-'));
    try {
      await viteBuild({
        root: projectRoot,
        configFile: join(projectRoot, 'vite.hosted.config.ts'),
        logLevel: 'silent',
        build: { outDir },
      });

      const files = listFilesRecursively(outDir);
      const relativeFiles = files.map((file) => relative(outDir, file).replaceAll('\\', '/'));
      const manifestFile = join(outDir, '.vite', 'manifest.json');
      const manifest = JSON.parse(readFileSync(manifestFile, 'utf8')) as Record<string, {
        file: string;
        isEntry?: boolean;
        css?: string[];
      }>;
      const entry = manifest['hosted.html'];

      expect(relativeFiles).toContain('hosted.html');
      expect(entry?.isEntry).toBe(true);
      expect(entry?.file).toBeTruthy();
      expect(relativeFiles).toContain(entry.file);
      expect(entry.css?.length).toBeGreaterThan(0);
      for (const cssFile of entry.css ?? []) expect(relativeFiles).toContain(cssFile);
      expect(relativeFiles.some((file) => /ffmpeg|gift-clip|update|dpapi|\.exe$/i.test(file))).toBe(false);
      expect(files.map((file) => readFileSync(file, 'utf8')).join('\n')).not.toMatch(
        /ffmpeg|gift-clip|autoUpdate|electron|DPAPI/i,
      );
    } finally {
      rmSync(outDir, { recursive: true, force: true });
    }
  });
});
