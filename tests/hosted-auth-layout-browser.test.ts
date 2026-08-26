import { readFileSync } from 'node:fs';

import { chromium, type Browser, type Page } from 'playwright';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

const css = readFileSync(new URL('../src/hosted/shell.css', import.meta.url), 'utf8');
const qrPixel = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==';
const verificationUrl = 'https://passport.bilibili.com/h5-app/passport/login/scan?navhide=1&qrcode_key=public-key';

interface LoginState {
  readonly name: string;
  readonly kind: 'creating' | 'pending' | 'success' | 'warning' | 'expired' | 'error';
  readonly status: string;
  readonly placeholder: string;
  readonly active: boolean;
  readonly busy?: boolean;
  readonly action?: string;
}

const states: readonly LoginState[] = [
  { name: 'creating', kind: 'creating', status: '正在创建二维码', placeholder: '二维码生成中', busy: true, action: '正在创建', active: false },
  { name: 'pending', kind: 'pending', status: '请使用 B 站客户端扫码', placeholder: '', active: true },
  { name: 'scanned', kind: 'success', status: '已扫码，请在手机确认', placeholder: '', active: true },
  { name: 'network', kind: 'warning', status: '网络暂不可用，2 秒后自动重试', placeholder: '', action: '立即重试', active: true },
  { name: 'rate-limited', kind: 'warning', status: '请求较频繁，稍后自动重试', placeholder: '', action: '立即重试', active: true },
  { name: 'expired', kind: 'expired', status: '二维码已过期', placeholder: '二维码已过期', action: '重新生成', active: false },
  { name: 'verified', kind: 'success', status: '验证成功', placeholder: '登录已确认', active: false },
  { name: 'fatal', kind: 'error', status: '登录响应无效，请重新生成二维码', placeholder: '此二维码无法继续使用', action: '重新生成', active: false },
] as const;

function fixture(state: LoginState): string {
  const linkHidden = state.active ? '' : ' hidden';
  const image = state.active
    ? `<img class="hosted-qr" alt="B 站登录二维码" src="${qrPixel}">`
    : '<img class="hosted-qr" alt="B 站登录二维码" hidden>';
  const placeholder = state.active
    ? '<div class="hosted-auth-qr-placeholder" hidden></div>'
    : `<div class="hosted-auth-qr-placeholder">${state.placeholder}${state.busy ? '<span class="hosted-admin-action-spinner hosted-auth-spinner" aria-hidden="true"></span>' : ''}</div>`;
  const action = state.action
    ? `<button data-variant="primary"${state.busy ? ' disabled aria-busy="true"' : ' aria-busy="false"'}>${state.action}${state.busy ? '<span class="hosted-admin-action-spinner" aria-hidden="true"></span>' : ''}</button>`
    : '<button data-variant="primary" hidden></button>';
  return `<!doctype html>
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <style>${css}</style>
    <main class="hosted-auth-page" aria-labelledby="bili-login-title">
      <section class="hosted-auth-card">
        <div class="hosted-auth-copy">
          <p class="hosted-auth-eyebrow">BILIBILI 登录</p>
          <h1 id="bili-login-title">使用 B 站账号登录</h1>
          <p class="hosted-auth-description">扫码确认后即可进入主播工作区。</p>
          <ol class="hosted-auth-steps"><li>打开 B 站客户端扫描二维码</li><li>在手机上确认本次登录</li></ol>
          <p class="hosted-auth-status" data-kind="${state.kind}" role="status" aria-live="polite">${state.status}</p>
          <p class="hosted-auth-expiry"${state.active ? '' : ' hidden'}>二维码有效期至 08:00</p>
        </div>
        <div class="hosted-auth-qr-column">
          <div class="hosted-auth-qr-frame">${image}${placeholder}</div>
          <a class="hosted-auth-mobile-link" href="${state.active ? verificationUrl : ''}"${linkHidden}>在本机打开 B 站确认</a>
          <div class="hosted-auth-actions">${action}<button data-variant="secondary">取消</button></div>
        </div>
      </section>
    </main>`;
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
      columns: cardStyle.gridTemplateColumns === 'none'
        ? 1
        : cardStyle.gridTemplateColumns.trim().split(/\s+/).length,
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

  beforeAll(async () => {
    browser = await chromium.launch({ headless: true });
  }, 20_000);

  afterAll(async () => {
    await browser?.close();
  });

  it('covers every state at desktop and phone widths without overflow or console errors', async () => {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      const page = await browser.newPage({ viewport });
      const consoleErrors: string[] = [];
      page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()); });
      for (const state of states) {
        await page.setContent(fixture(state));
        const metrics = await layoutMetrics(page);
        expect(metrics.statusText, `${state.name} status`).toBe(state.status);
        expect(metrics.documentWidth, `${state.name} overflow at ${viewport.width}px`).toBe(metrics.innerWidth);
        expect(metrics.cardWidth, `${state.name} card width`).toBeLessThanOrEqual(860);
        expect(metrics.columns, `${state.name} columns at ${viewport.width}px`).toBe(viewport.width === 390 ? 1 : 2);
        expect(metrics.bodyBackground).toBe('rgb(243, 245, 248)');
        expect(metrics.actionAlignment).toBe('flex-end');
        for (const button of metrics.buttons) {
          expect(button.justifyContent, `${state.name} button centering`).toBe('center');
          expect(Math.abs(button.paddingLeft - button.paddingRight), `${state.name} symmetric padding`).toBeLessThan(1);
        }
        const mobileLink = page.getByRole('link', { name: '在本机打开 B 站确认' });
        expect(await mobileLink.isVisible(), `${state.name} mobile-link visibility`).toBe(viewport.width === 390 && state.active);
        if (viewport.width === 390) {
          expect(metrics.documentWidth).toBe(390);
          expect(metrics.frameWidth).toBeLessThanOrEqual(390 * 0.72 + 1);
        }
      }
      expect(consoleErrors, `console errors at ${viewport.width}px`).toEqual([]);
      await page.close();
    }
  }, 30_000);

  it('keeps expired QR geometry fixed and gives each state family a static visual treatment', async () => {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      const page = await browser.newPage({ viewport });
      await page.setContent(fixture(states[1]));
      const pending = await layoutMetrics(page);
      await page.setContent(fixture(states[5]));
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
        await page.setContent(fixture(state));
        const background = await page.locator('.hosted-auth-status').evaluate((element) => getComputedStyle(element).backgroundColor);
        expect(background, state.name).toBe(expectedBackground.get(state.kind));
      }
      await page.close();
    }
  }, 30_000);

  it('shows real hover and keyboard focus feedback and disables nonessential reduced-motion animation', async () => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const consoleErrors: string[] = [];
    page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()); });
    await page.setContent(fixture(states[7]));
    const regenerate = page.getByRole('button', { name: '重新生成' });
    const beforeHover = await regenerate.evaluate((element) => getComputedStyle(element).backgroundColor);
    await regenerate.hover();
    const afterHover = await regenerate.evaluate((element) => getComputedStyle(element).backgroundColor);
    expect(afterHover).not.toBe(beforeHover);

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

    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.setContent(fixture(states[0]));
    expect(await page.locator('.hosted-auth-spinner').evaluate((element) => getComputedStyle(element).animationName)).toBe('none');
    expect(await page.locator('.hosted-auth-status').textContent()).toBe(states[0].status);
    expect(await page.locator('.hosted-auth-status').evaluate((element) => getComputedStyle(element).backgroundColor)).toBe('rgb(246, 248, 251)');
    expect(consoleErrors).toEqual([]);
    await page.close();
  });
});
