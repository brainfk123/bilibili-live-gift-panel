import type { AddressInfo } from 'node:net';
import { fileURLToPath } from 'node:url';

import { chromium, type Browser, type Page } from 'playwright';
import { createServer, type ViteDevServer } from 'vite';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

const harnessPage = `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <link rel="stylesheet" href="/src/hosted/shell.css">
    <title>Hosted user workspace harness</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module">
      import { createHostedUserShell, renderHostedUserPageState } from '/src/hosted/user/shell.ts';
      const root = document.querySelector('#root');
      const shell = createHostedUserShell(root, {
        initialPage: 'overview',
        experience: 'simple',
        configurationId: 'browser-config',
        mount(page, host) {
          renderHostedUserPageState(host, {
            kind: page === 'overview' ? 'loading' : 'empty',
            title: page === 'overview' ? '正在加载概览' : '暂无内容',
            description: '此区域保持稳定尺寸。',
          });
          return { dispose() { host.replaceChildren(); } };
        },
      });
      window.__userShell = shell;
      document.documentElement.dataset.ready = 'true';
    </script>
  </body>
</html>`;

declare global {
  interface Window {
    __userShell: { setExperience(value: 'simple' | 'advanced'): void };
  }
}

async function openHarness(browser: Browser, baseURL: string, viewport: { width: number; height: number }) {
  const page = await browser.newPage({ viewport });
  const errors: string[] = [];
  page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()); });
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(`${baseURL}/__hosted-user-layout-test`);
  await page.waitForFunction(() => document.documentElement.dataset.ready === 'true');
  return { page, errors };
}

async function metrics(page: Page) {
  return page.evaluate(() => {
    const app = document.querySelector('.hosted-user-app')!;
    const sidebar = document.querySelector('.hosted-user-sidebar')!;
    const workspace = document.querySelector('.hosted-user-workspace')!;
    const navigation = document.querySelector('.hosted-user-navigation')!;
    const state = document.querySelector('.hosted-user-state')!;
    const visibleButtons = [...document.querySelectorAll<HTMLButtonElement>('.hosted-user-app button')]
      .filter((button) => getComputedStyle(button).display !== 'none')
      .map((button) => {
        const style = getComputedStyle(button);
        const rect = button.getBoundingClientRect();
        return {
          right: rect.right,
          width: rect.width,
          justifyContent: style.justifyContent,
          paddingLeft: Number.parseFloat(style.paddingLeft),
          paddingRight: Number.parseFloat(style.paddingRight),
        };
      });
    return {
      innerWidth: window.innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      appColumns: getComputedStyle(app).gridTemplateColumns.trim().split(/\s+/).length,
      navigationColumns: getComputedStyle(navigation).gridTemplateColumns.trim().split(/\s+/).length,
      mobileSectionGap: workspace.getBoundingClientRect().top - sidebar.getBoundingClientRect().bottom,
      stateHeight: state.getBoundingClientRect().height,
      activeBackground: getComputedStyle(document.querySelector('[aria-current="page"]')!).backgroundColor,
      buttons: visibleButtons,
    };
  });
}

describe('Hosted user workspace layout in real Chromium', () => {
  let browser: Browser;
  let server: ViteDevServer;
  let baseURL: string;

  beforeAll(async () => {
    server = await createServer({
      root: fileURLToPath(new URL('..', import.meta.url)),
      logLevel: 'error',
      server: { host: '127.0.0.1', port: 0, strictPort: false },
      plugins: [{
        name: 'hosted-user-layout-harness',
        configureServer(devServer) {
          devServer.middlewares.use('/__hosted-user-layout-test', (_request, response) => {
            response.statusCode = 200;
            response.setHeader('Content-Type', 'text/html; charset=UTF-8');
            response.end(harnessPage);
          });
        },
      }],
    });
    await server.listen();
    const address = server.httpServer?.address() as AddressInfo;
    baseURL = `http://127.0.0.1:${address.port}`;
    browser = await chromium.launch({ headless: true });
  }, 30_000);

  afterAll(async () => {
    await browser?.close();
    await server?.close();
  });

  it.each([
    { width: 1440, height: 900, appColumns: 2, navigationColumns: 1 },
    { width: 390, height: 844, appColumns: 1, navigationColumns: 3 },
    { width: 320, height: 700, appColumns: 1, navigationColumns: 2 },
  ])('keeps navigation and actions inside a $width px viewport', async (viewport) => {
    const { page, errors } = await openHarness(browser, baseURL, viewport);
    const result = await metrics(page);
    expect(result.documentWidth).toBe(result.innerWidth);
    expect(result.appColumns).toBe(viewport.appColumns);
    expect(result.navigationColumns).toBe(viewport.navigationColumns);
    if (viewport.width <= 760) expect(result.mobileSectionGap).toBeLessThanOrEqual(1);
    expect(result.stateHeight).toBeGreaterThanOrEqual(viewport.width <= 760 ? 240 : 288);
    expect(result.activeBackground).toBe('rgb(43, 104, 234)');
    for (const button of result.buttons) {
      expect(button.right).toBeLessThanOrEqual(viewport.width + 0.5);
      expect(button.width).toBeGreaterThan(0);
      expect(Math.abs(button.paddingLeft - button.paddingRight)).toBeLessThan(1);
    }
    expect(errors).toEqual([]);
    await page.close();
  });

  it('shows active navigation without hover and disables skeleton motion when requested', async () => {
    const { page, errors } = await openHarness(browser, baseURL, { width: 390, height: 844 });
    const attributes = page.getByRole('button', { name: '属性玩法' });
    await attributes.click();
    expect(await attributes.getAttribute('aria-current')).toBe('page');
    expect(await attributes.evaluate((element) => getComputedStyle(element).backgroundColor)).toBe('rgb(43, 104, 234)');
    await page.getByRole('button', { name: '概览' }).click();
    await page.emulateMedia({ reducedMotion: 'reduce' });
    expect(await page.locator('.hosted-user-state-skeleton').evaluate((element) => getComputedStyle(element).animationName)).toBe('none');
    expect(errors).toEqual([]);
    await page.close();
  });
});
