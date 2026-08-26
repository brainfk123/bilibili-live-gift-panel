import type { AddressInfo } from 'node:net';
import { fileURLToPath } from 'node:url';

import { chromium, type Browser, type Page } from 'playwright';
import { createServer, type ViteDevServer } from 'vite';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

const qrPixel = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==';
const verificationUrl = 'https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=public-key';

type StateName =
  | 'creating'
  | 'creation-failure'
  | 'pending'
  | 'scanned'
  | 'network'
  | 'temporarily-unavailable'
  | 'rate-limited'
  | 'expired'
  | 'verified'
  | 'registration'
  | 'fatal';

interface LoginState {
  readonly name: StateName;
  readonly kind: 'creating' | 'pending' | 'success' | 'warning' | 'expired' | 'error';
  readonly status: string;
  readonly active: boolean;
  readonly action?: string;
}

const states: readonly LoginState[] = [
  { name: 'creating', kind: 'creating', status: '正在创建二维码', active: false, action: '正在创建' },
  { name: 'creation-failure', kind: 'error', status: '无法创建二维码，请再次尝试', active: false, action: '再次尝试' },
  { name: 'pending', kind: 'pending', status: '请使用 B 站客户端扫码', active: true },
  { name: 'scanned', kind: 'success', status: '已扫码，请在手机确认', active: true },
  { name: 'network', kind: 'warning', status: '网络暂不可用，2 秒后自动重试', active: true, action: '立即重试' },
  { name: 'temporarily-unavailable', kind: 'warning', status: '登录服务暂不可用，2 秒后自动重试', active: true, action: '立即重试' },
  { name: 'rate-limited', kind: 'warning', status: '请求较频繁，稍后自动重试', active: true, action: '立即重试' },
  { name: 'expired', kind: 'expired', status: '二维码已过期', active: false, action: '重新生成' },
  { name: 'verified', kind: 'success', status: '验证成功', active: false },
  { name: 'registration', kind: 'success', status: '请继续完成账号注册', active: false },
  { name: 'fatal', kind: 'error', status: '登录响应无效，请重新生成二维码', active: false, action: '重新生成' },
] as const;

const harnessPage = `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <link rel="stylesheet" href="/src/hosted/shell.css">
    <title>Hosted auth layout harness</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module">
      import { mountAuthView } from '/src/hosted/auth.ts';
      import { HostedAPIError } from '/src/hosted/api.ts';

      const qrPixel = ${JSON.stringify(qrPixel)};
      const verificationUrl = ${JSON.stringify(verificationUrl)};
      const expiresAt = '2030-01-01T00:00:00Z';
      const root = document.querySelector('#root');
      const flush = async () => { for (let turn = 0; turn < 8; turn += 1) await Promise.resolve(); };

      class ControlledTimers {
        clock = 0;
        nextID = 1;
        scheduled = new Map();
        setTimeout(callback, milliseconds) {
          const id = this.nextID++;
          this.scheduled.set(id, { callback, dueAt: this.clock + milliseconds });
          return id;
        }
        clearTimeout(id) { this.scheduled.delete(id); }
        now() { return this.clock; }
        count() { return this.scheduled.size; }
        nextDelay() {
          const next = [...this.scheduled.values()].sort((left, right) => left.dueAt - right.dueAt)[0];
          return next ? next.dueAt - this.clock : undefined;
        }
        async fireNext() {
          const next = [...this.scheduled.entries()].sort((left, right) => left[1].dueAt - right[1].dueAt)[0];
          if (!next) throw new Error('No controlled browser timer is scheduled.');
          const [id, task] = next;
          this.scheduled.delete(id);
          this.clock = task.dueAt;
          task.callback();
          await flush();
        }
      }

      let mounted;
      let timers;
      let releaseCreating;
      let stats = {};

      const challenge = () => ({ challengeId: 'browser-challenge', qrImage: qrPixel, verificationUrl, expiresAt });
      const pollState = new Set(['scanned', 'network', 'temporarily-unavailable', 'rate-limited', 'expired', 'verified', 'registration', 'fatal']);

      async function cleanup() {
        if (releaseCreating) {
          releaseCreating(challenge());
          releaseCreating = undefined;
          await mounted?.ready;
        }
        await mounted?.dispose();
        mounted = undefined;
      }

      async function mount(state) {
        await cleanup();
        timers = new ControlledTimers();
        stats = { beginCalls: 0, pollCalls: 0, createSessionCalls: 0, signedIn: 0, registrationIntent: undefined };
        const api = {
          async beginLogin() {
            stats.beginCalls += 1;
            if (state === 'creating') return new Promise((resolve) => { releaseCreating = resolve; });
            if (state === 'creation-failure') throw new Error('creation unavailable');
            return challenge();
          },
          async pollLogin() {
            stats.pollCalls += 1;
            if (state === 'scanned') return { status: 'scanned', expiresAt };
            if (state === 'network') {
              if (stats.pollCalls === 1) throw new TypeError('Failed to fetch');
              return { status: 'pending', expiresAt };
            }
            if (state === 'temporarily-unavailable') throw new HostedAPIError('temporarily_unavailable', 503);
            if (state === 'rate-limited') throw new HostedAPIError('rate_limited', 429);
            if (state === 'expired') return { status: 'expired' };
            if (state === 'verified') return { status: 'verified', expiresAt };
            if (state === 'registration') return { status: 'registration_required', registrationIntent: 'browser-registration', expiresAt };
            if (state === 'fatal') throw new HostedAPIError('invalid_response', 200);
            return { status: 'pending', expiresAt };
          },
          async createSession() { stats.createSessionCalls += 1; },
          async cancelLogin() {},
          async logout() {},
        };
        mounted = mountAuthView(root, api, {
          onSignedIn() { stats.signedIn += 1; },
          onRegistrationRequired(intent) { stats.registrationIntent = intent; },
        }, timers);
        if (state === 'creating') { await flush(); return; }
        await mounted.ready;
        if (pollState.has(state)) await timers.fireNext();
      }

      window.__authHarness = {
        mount,
        async fireNext() { await timers.fireNext(); },
        snapshot() { return { ...stats, timerCount: timers?.count(), nextDelay: timers?.nextDelay() }; },
        cleanup,
      };
    </script>
  </body>
</html>`;

interface BrowserHarness {
  mount(state: StateName): Promise<void>;
  fireNext(): Promise<void>;
  snapshot(): {
    beginCalls: number;
    pollCalls: number;
    createSessionCalls: number;
    signedIn: number;
    registrationIntent?: string;
    timerCount: number;
    nextDelay?: number;
  };
  cleanup(): Promise<void>;
}

declare global {
  interface Window { __authHarness: BrowserHarness }
}

async function openHarness(browser: Browser, baseURL: string, viewport: { width: number; height: number }) {
  const page = await browser.newPage({ viewport });
  const consoleErrors: string[] = [];
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()); });
  page.on('pageerror', (error) => { consoleErrors.push(error.message); });
  await page.goto(`${baseURL}/__hosted-auth-layout-test`);
  await page.waitForFunction(() => typeof window.__authHarness?.mount === 'function');
  return { page, consoleErrors };
}

async function mountState(page: Page, state: StateName): Promise<void> {
  await page.evaluate((name) => window.__authHarness.mount(name), state);
}

async function layoutMetrics(page: Page) {
  return page.evaluate(() => {
    const card = document.querySelector('.hosted-auth-card')!;
    const frame = document.querySelector('.hosted-auth-qr-frame')!;
    const cardStyle = getComputedStyle(card);
    const cardRect = card.getBoundingClientRect();
    const frameRect = frame.getBoundingClientRect();
    const buttons = [...document.querySelectorAll<HTMLButtonElement>('.hosted-auth-actions button')]
      .filter((element) => getComputedStyle(element).display !== 'none')
      .map((element) => {
        const style = getComputedStyle(element);
        return {
          justifyContent: style.justifyContent,
          paddingLeft: Number.parseFloat(style.paddingLeft),
          paddingRight: Number.parseFloat(style.paddingRight),
        };
      });
    return {
      innerWidth: window.innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      bodyBackground: getComputedStyle(document.body).backgroundColor,
      cardWidth: cardRect.width,
      columns: cardStyle.gridTemplateColumns === 'none' ? 1 : cardStyle.gridTemplateColumns.trim().split(/\s+/).length,
      frameWidth: frameRect.width,
      frameHeight: frameRect.height,
      actionAlignment: getComputedStyle(document.querySelector('.hosted-auth-actions')!).justifyContent,
      statusText: document.querySelector('.hosted-auth-status')?.textContent?.trim(),
      buttons,
    };
  });
}

describe('ordinary Hosted Bilibili login layout in real Chromium', () => {
  let browser: Browser;
  let server: ViteDevServer;
  let baseURL: string;

  beforeAll(async () => {
    server = await createServer({
      root: fileURLToPath(new URL('..', import.meta.url)),
      logLevel: 'error',
      server: { host: '127.0.0.1', port: 0, strictPort: false },
      plugins: [{
        name: 'hosted-auth-layout-browser-harness',
        configureServer(devServer) {
          devServer.middlewares.use('/__hosted-auth-layout-test', (_request, response) => {
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

  it('mounts production DOM for every state at desktop and phone widths without overflow or console errors', async () => {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      const { page, consoleErrors } = await openHarness(browser, baseURL, viewport);
      for (const state of states) {
        await mountState(page, state.name);
        const metrics = await layoutMetrics(page);
        expect(metrics.statusText, `${state.name} status`).toBe(state.status);
        for (const className of ['hosted-auth-page', 'hosted-auth-card', 'hosted-auth-copy', 'hosted-auth-qr-column', 'hosted-auth-status', 'hosted-auth-actions', 'hosted-auth-mobile-link']) {
          expect(await page.locator(`.${className}`).count(), `${state.name} .${className}`).toBe(1);
        }
        expect(metrics.documentWidth, `${state.name} overflow at ${viewport.width}px`).toBe(metrics.innerWidth);
        expect(metrics.cardWidth, `${state.name} card width`).toBeLessThanOrEqual(860);
        expect(metrics.columns, `${state.name} columns at ${viewport.width}px`).toBe(viewport.width === 390 ? 1 : 2);
        expect(metrics.bodyBackground).toBe('rgb(243, 245, 248)');
        expect(metrics.actionAlignment).toBe('flex-end');
        for (const button of metrics.buttons) {
          expect(button.justifyContent, `${state.name} button centering`).toBe('center');
          expect(Math.abs(button.paddingLeft - button.paddingRight), `${state.name} symmetric padding`).toBeLessThan(1);
        }

        const qr = page.locator('.hosted-qr');
        const mobileLink = page.locator('.hosted-auth-mobile-link');
        expect(await qr.getAttribute('src'), `${state.name} QR lifecycle`).toBe(state.active ? qrPixel : null);
        expect(await mobileLink.getAttribute('href'), `${state.name} link lifecycle`).toBe(state.active ? verificationUrl : null);
        expect(await mobileLink.isVisible(), `${state.name} mobile-link visibility`).toBe(viewport.width === 390 && state.active);
        if (state.action) {
          const action = page.getByRole('button', { name: state.action });
          expect(await action.count()).toBe(1);
          if (state.name === 'creating') {
            expect(await action.isDisabled()).toBe(true);
            expect(await action.getAttribute('aria-busy')).toBe('true');
          }
        }
        if (state.name === 'verified') expect(await page.evaluate(() => window.__authHarness.snapshot().signedIn)).toBe(1);
        if (state.name === 'registration') expect(await page.evaluate(() => window.__authHarness.snapshot().registrationIntent)).toBe('browser-registration');
        if (viewport.width === 390) {
          expect(metrics.documentWidth).toBe(390);
          expect(metrics.frameWidth).toBeLessThanOrEqual(390 * 0.72 + 1);
        }
      }
      expect(consoleErrors, `console errors at ${viewport.width}px`).toEqual([]);
      await page.evaluate(() => window.__authHarness.cleanup());
      await page.close();
    }
  }, 40_000);

  it('keeps expired QR geometry fixed and gives each production state family a static visual treatment', async () => {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      const { page } = await openHarness(browser, baseURL, viewport);
      await mountState(page, 'pending');
      const pending = await layoutMetrics(page);
      await mountState(page, 'expired');
      const expired = await layoutMetrics(page);
      expect(Math.abs(pending.frameWidth - expired.frameWidth)).toBeLessThan(1);
      expect(Math.abs(pending.frameHeight - expired.frameHeight)).toBeLessThan(1);

      const expectedBackground = new Map([
        ['creating', 'rgb(246, 248, 251)'],
        ['pending', 'rgb(237, 244, 255)'],
        ['success', 'rgb(239, 250, 243)'],
        ['warning', 'rgb(255, 248, 223)'],
        ['expired', 'rgb(255, 241, 239)'],
        ['error', 'rgb(255, 241, 239)'],
      ]);
      for (const state of states) {
        await mountState(page, state.name);
        const background = await page.locator('.hosted-auth-status').evaluate((element) => getComputedStyle(element).backgroundColor);
        expect(background, state.name).toBe(expectedBackground.get(state.kind));
      }
      await page.evaluate(() => window.__authHarness.cleanup());
      await page.close();
    }
  }, 40_000);

  it('publishes manual retry enablement from the production poller after its cooldown', async () => {
    const { page } = await openHarness(browser, baseURL, { width: 390, height: 844 });
    await mountState(page, 'network');
    const retry = page.getByRole('button', { name: '立即重试' });
    expect(await retry.isDisabled()).toBe(true);

    await page.evaluate(() => window.__authHarness.fireNext());

    expect(await retry.isVisible()).toBe(true);
    expect(await retry.isDisabled()).toBe(false);
    await retry.click();
    await page.waitForFunction(() => window.__authHarness.snapshot().pollCalls === 2);
    expect(await page.evaluate(() => window.__authHarness.snapshot().timerCount)).toBe(1);
    await page.evaluate(() => window.__authHarness.cleanup());
    await page.close();
  });

  it('shows real button and same-device-link interaction feedback and preserves reduced-motion state', async () => {
    const { page, consoleErrors } = await openHarness(browser, baseURL, { width: 390, height: 844 });
    await mountState(page, 'fatal');
    const regenerate = page.getByRole('button', { name: '重新生成' });
    const beforeHover = await regenerate.evaluate((element) => getComputedStyle(element).backgroundColor);
    await regenerate.hover();
    expect(await regenerate.evaluate((element) => getComputedStyle(element).backgroundColor)).not.toBe(beforeHover);

    await page.mouse.move(0, 0);
    await page.keyboard.press('Tab');
    expect(await regenerate.evaluate((element) => element === document.activeElement)).toBe(true);
    const focus = await regenerate.evaluate((element) => {
      const style = getComputedStyle(element);
      return { style: style.outlineStyle, width: Number.parseFloat(style.outlineWidth), color: style.outlineColor };
    });
    expect(focus.style).not.toBe('none');
    expect(focus.width).toBeGreaterThanOrEqual(3);
    expect(focus.color).not.toBe('rgba(0, 0, 0, 0)');

    await mountState(page, 'pending');
    const mobileLink = page.getByRole('link', { name: '在本机打开 B 站确认' });
    const linkBase = await mobileLink.evaluate((element) => getComputedStyle(element).backgroundColor);
    await mobileLink.hover();
    const linkHover = await mobileLink.evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(linkHover).not.toBe(linkBase);
    const box = await mobileLink.boundingBox();
    if (!box) throw new Error('Mobile confirmation link has no layout box.');
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    const active = await mobileLink.evaluate((element) => ({
      background: getComputedStyle(element).backgroundColor,
      transform: getComputedStyle(element).transform,
    }));
    expect(active.background).not.toBe(linkHover);
    expect(active.transform).not.toBe('none');
    await page.mouse.move(0, 0);
    await page.mouse.up();

    await page.emulateMedia({ reducedMotion: 'reduce' });
    await mountState(page, 'creating');
    expect(await page.locator('.hosted-auth-spinner').evaluate((element) => getComputedStyle(element).animationName)).toBe('none');
    expect(await page.locator('.hosted-auth-status').textContent()).toBe('正在创建二维码');
    expect(await page.locator('.hosted-auth-status').evaluate((element) => getComputedStyle(element).backgroundColor)).toBe('rgb(246, 248, 251)');
    expect(consoleErrors).toEqual([]);
    await page.evaluate(() => window.__authHarness.cleanup());
    await page.close();
  }, 30_000);
});
