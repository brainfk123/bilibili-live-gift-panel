import { beforeAll, describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build as viteBuild } from 'vite';

const projectRoot = fileURLToPath(new URL('..', import.meta.url));
const distRoot = join(projectRoot, 'dist');

describe('single-file production output', () => {
  beforeAll(async () => {
    await viteBuild({
      root: projectRoot,
      configFile: join(projectRoot, 'vite.config.ts'),
      logLevel: 'silent',
    });
  });

  it('keeps one index.html with no external CSS or global config style', () => {
    const files = readdirSync(distRoot);
    const html = readFileSync(join(distRoot, 'index.html'), 'utf8');
    const staticStyles = [...html.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((match) => match[1]);

    expect(files.filter((file) => file.endsWith('.html'))).toEqual(['index.html']);
    expect(files.filter((file) => file.endsWith('.css'))).toHaveLength(0);
    expect(staticStyles.some((style) => style.includes('.config-root'))).toBe(false);
    expect(staticStyles.some((style) => style.includes('[data-theme="light"]'))).toBe(false);
  });
});
