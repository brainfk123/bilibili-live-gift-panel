import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build as viteBuild, loadConfigFromFile } from 'vite';
import { describe, expect, it } from 'vitest';

const projectRoot = fileURLToPath(new URL('..', import.meta.url));
const hostedFiles = [
  'hosted.html',
  'obs.html',
  'src/hosted/main.ts',
  'src/hosted/shell.ts',
  'src/hosted/shell.css',
  'src/hosted/obs/main.ts',
  'src/hosted/obs/view.ts',
  'src/hosted/obs/obs.css',
  'vite.hosted.config.ts',
];

function normalizePath(path: string): string {
  return path.replaceAll('\\', '/');
}

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
    expect(hostedFiles.map((file) => existsSync(join(projectRoot, file)))).toEqual(hostedFiles.map(() => true));
  });

  it('adds reproducible hosted build scripts', () => {
    const packageJSON = JSON.parse(readFileSync(join(projectRoot, 'package.json'), 'utf8')) as {
      scripts?: Record<string, string>;
    };

    expect(packageJSON.scripts?.['build:hosted']).toBe('vite build --config vite.hosted.config.ts');
    expect(packageJSON.scripts?.['test:hosted']).toBe('vitest run tests/hosted-build.test.ts');
  });

  it('includes the hosted Vite config in the TypeScript program', () => {
    const tsconfig = JSON.parse(readFileSync(join(projectRoot, 'tsconfig.json'), 'utf8')) as {
      include?: string[];
    };
    const viteConfig = resolve(projectRoot, 'vite.hosted.config.ts').replaceAll('\\', '/');
    const listedFiles = execFileSync(
      process.execPath,
      [join(projectRoot, 'node_modules', 'typescript', 'bin', 'tsc'), '--listFilesOnly'],
      { cwd: projectRoot, encoding: 'utf8' },
    ).replaceAll('\\', '/');

    expect(tsconfig.include).toContain('vite.hosted.config.ts');
    expect(listedFiles).toContain(viteConfig);
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

  it('disposes the current hosted view before mounting its replacement', async () => {
    const module = await import('../src/hosted/shell');
    const events: string[] = [];
    const host = module.createHostedViewHost();
    await host.replace(() => {
      events.push('mount-auth');
      return { dispose: async () => { events.push('dispose-auth'); } };
    });
    await host.replace(() => {
      events.push('mount-registration');
      return { dispose: () => { events.push('dispose-registration'); } };
    });
    await host.dispose();
    expect(events).toEqual(['mount-auth', 'dispose-auth', 'mount-registration', 'dispose-registration']);
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
      const build = await viteBuild({
        root: projectRoot,
        configFile: join(projectRoot, 'vite.hosted.config.ts'),
        logLevel: 'silent',
        build: { outDir },
      });

      const outputs = (Array.isArray(build) ? build : [build]).flatMap((output) => (
        'output' in output ? output.output : []
      ));
      const moduleIDs = outputs.flatMap((output) => (
        output.type === 'chunk' ? Object.keys(output.modules) : []
      ));
      const hostedHTML = normalizePath(join(projectRoot, 'hosted.html'));
      const obsHTML = normalizePath(join(projectRoot, 'obs.html'));
      const hostedSource = `${normalizePath(join(projectRoot, 'src', 'hosted'))}/`;
      const projectSource = `${normalizePath(projectRoot)}/`;
      const allowedSharedModules = new Set([
        normalizePath(join(projectRoot, 'src', 'format.ts')),
        normalizePath(join(projectRoot, 'src', 'display-themes.ts')),
      ]);
      expect(moduleIDs.filter((moduleID) => {
        const normalized = normalizePath(moduleID);
        return normalized.startsWith(projectSource)
          && normalized !== hostedHTML
          && normalized !== obsHTML
          && !normalized.startsWith(hostedSource)
          && !allowedSharedModules.has(normalized);
      })).toEqual([]);

      const obsEntryChunk = outputs.find((output) => output.type === 'chunk'
        && normalizePath(output.facadeModuleId ?? '') === obsHTML);
      expect(obsEntryChunk?.type).toBe('chunk');
      const obsChunkFiles = new Set<string>();
      const visitOBSChunk = (fileName: string): void => {
        if (obsChunkFiles.has(fileName)) return;
        obsChunkFiles.add(fileName);
        const chunk = outputs.find((output) => output.type === 'chunk' && output.fileName === fileName);
        if (!chunk || chunk.type !== 'chunk') return;
        for (const dependency of [...chunk.imports, ...chunk.dynamicImports]) visitOBSChunk(dependency);
      };
      if (obsEntryChunk?.type === 'chunk') visitOBSChunk(obsEntryChunk.fileName);
      const obsModules = outputs.flatMap((output) => (
        output.type === 'chunk' && obsChunkFiles.has(output.fileName) ? Object.keys(output.modules) : []
      ));
      const obsSource = `${normalizePath(join(projectRoot, 'src', 'hosted', 'obs'))}/`;
      expect(obsModules.filter((moduleID) => {
        const normalized = normalizePath(moduleID);
        return normalized.startsWith(projectSource)
          && normalized !== obsHTML
          && !normalized.startsWith(obsSource)
          && !allowedSharedModules.has(normalized);
      })).toEqual([]);
      expect(moduleIDs.filter((moduleID) => (
        moduleID.startsWith('\0') && !moduleID.startsWith('\0vite/')
      ))).toEqual([]);

      const files = listFilesRecursively(outDir);
      const relativeFiles = files.map((file) => relative(outDir, file).replaceAll('\\', '/'));
      const manifestFile = join(outDir, '.vite', 'manifest.json');
      const manifest = JSON.parse(readFileSync(manifestFile, 'utf8')) as Record<string, {
        file: string;
        isEntry?: boolean;
        css?: string[];
        assets?: string[];
        imports?: string[];
        dynamicImports?: string[];
      }>;
      const entry = manifest['hosted.html'];
      const obsEntry = manifest['obs.html'];

      expect(relativeFiles).toContain('hosted.html');
      expect(relativeFiles).toContain('obs.html');
	  const builtOBSHTML = readFileSync(join(outDir, 'obs.html'), 'utf8');
	  const obsAssetReferences = [...builtOBSHTML.matchAll(/(?:src|href)="([^"]+)"/g)]
	    .map((match) => match[1])
	    .filter((reference) => reference.includes('assets/'));
	  const credentialURL = `https://host.example/obs/${'A'.repeat(43)}`;
	  expect(obsAssetReferences.length).toBeGreaterThan(0);
	  expect(obsAssetReferences.map((reference) => new URL(reference, credentialURL).pathname))
	    .toEqual(obsAssetReferences.map((reference) => `/${reference.replace(/^\.?\//, '')}`));
      expect(entry?.isEntry).toBe(true);
      expect(obsEntry?.isEntry).toBe(true);
      expect(entry?.file).toBeTruthy();
      expect(obsEntry?.file).toBeTruthy();
      expect(relativeFiles).toContain(entry.file);
      expect(relativeFiles).toContain(obsEntry.file);
      expect(entry.css?.length).toBeGreaterThan(0);
      expect(obsEntry.css?.length).toBeGreaterThan(0);
      const referencedFiles = new Set<string>(['hosted.html', 'obs.html', '.vite/manifest.json']);
      const visitedEntries = new Set<string>();
      const visitManifestEntry = (entryName: string): void => {
        if (visitedEntries.has(entryName)) return;
        visitedEntries.add(entryName);
        const manifestEntry = manifest[entryName];
        expect(manifestEntry, `missing manifest entry ${entryName}`).toBeDefined();
        if (!manifestEntry) return;
        referencedFiles.add(manifestEntry.file);
        for (const file of manifestEntry.css ?? []) referencedFiles.add(file);
        for (const file of manifestEntry.assets ?? []) referencedFiles.add(file);
        for (const dependency of [
          ...(manifestEntry.imports ?? []),
          ...(manifestEntry.dynamicImports ?? []),
        ]) visitManifestEntry(dependency);
      };
      visitManifestEntry('hosted.html');
      visitManifestEntry('obs.html');

      expect(new Set(relativeFiles)).toEqual(referencedFiles);
      expect(relativeFiles.some((file) => /ffmpeg|gift-clip|update|dpapi|\.exe$/i.test(file))).toBe(false);
      expect(files.map((file) => readFileSync(file, 'utf8')).join('\n')).not.toMatch(
        /ffmpeg|gift-clip|autoUpdate|electron|DPAPI/i,
      );
    } finally {
      rmSync(outDir, { recursive: true, force: true });
    }
  });
});
