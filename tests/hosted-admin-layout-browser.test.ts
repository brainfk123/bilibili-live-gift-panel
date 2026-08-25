import { readFileSync } from 'node:fs';

import { chromium, type Browser } from 'playwright';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

const css = readFileSync(new URL('../src/hosted/shell.css', import.meta.url), 'utf8');

describe('administrator layout in a real browser', () => {
  let browser: Browser;

  beforeAll(async () => {
    browser = await chromium.launch({ headless: true });
  }, 20_000);

  afterAll(async () => {
    await browser?.close();
  });

  it('keeps every resource card neutral until pointer hover', async () => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.setContent(`
      <style>${css}</style>
      <main class="hosted-admin-content">
        <section class="hosted-admin-resource-row">
          <button class="hosted-admin-resource-card"><strong>主播账号</strong><span>管理直播间</span><span>→</span></button>
          <button class="hosted-admin-resource-card"><strong>邀请码</strong><span>创建邀请码</span><span>→</span></button>
        </section>
      </main>
    `);

    const first = page.locator('.hosted-admin-resource-card').first();
    expect(await first.evaluate((element) => getComputedStyle(element).backgroundColor)).toBe('rgb(255, 255, 255)');
    expect(await first.evaluate((element) => getComputedStyle(element).color)).toBe('rgb(23, 32, 51)');

    await first.hover();
    await page.waitForTimeout(200);
    expect(await first.evaluate((element) => getComputedStyle(element).backgroundColor)).toBe('rgb(43, 104, 234)');
    expect(await first.evaluate((element) => getComputedStyle(element).color)).toBe('rgb(255, 255, 255)');
    await page.close();
  });

  it('aligns invitation toolbar and creation controls on one baseline with equal heights', async () => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.setContent(`
      <style>${css}</style>
      <main class="hosted-admin-content">
        <div class="hosted-admin-invitation-actions">
          <input aria-label="搜索" />
          <label class="hosted-admin-field">状态<select><option>全部状态</option></select></label>
          <button>创建邀请码</button>
        </div>
        <section class="hosted-admin-invitation-create">
          <label class="hosted-admin-field">数量<select><option>1 个</option></select></label>
          <label class="hosted-admin-field">有效期<select><option>7 天</option></select></label>
          <button data-variant="primary">创建</button>
          <button data-variant="quiet">取消</button>
        </section>
      </main>
    `);

    const metrics = await page.evaluate(() => {
      const rect = (element: Element) => {
        const value = element.getBoundingClientRect();
        return { bottom: value.bottom, height: value.height };
      };
      const toolbar = document.querySelector('.hosted-admin-invitation-actions')!;
      const panel = document.querySelector('.hosted-admin-invitation-create')!;
      return {
        toolbar: [toolbar.querySelector('input')!, toolbar.querySelector('select')!, toolbar.querySelector('button')!].map(rect),
        panel: [...panel.querySelectorAll('select, button')].map(rect),
      };
    });

    for (const group of [metrics.toolbar, metrics.panel]) {
      expect(Math.max(...group.map((item) => item.bottom)) - Math.min(...group.map((item) => item.bottom))).toBeLessThan(1);
      expect(Math.max(...group.map((item) => item.height)) - Math.min(...group.map((item) => item.height))).toBeLessThan(1);
    }
    await page.close();
  });

  it('aligns account search and filter controls on the same top and bottom edges', async () => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.setContent(`
      <style>${css}</style>
      <main class="hosted-admin-content">
        <div class="hosted-admin-account-controls">
          <input placeholder="搜索账号 ID 或直播间" />
          <label class="hosted-admin-field"><span>账号状态</span><select><option>全部状态</option></select></label>
          <label class="hosted-admin-field"><span>关注事项</span><select><option>全部账号</option></select></label>
        </div>
      </main>
    `);

    const controls = await page.locator('.hosted-admin-account-controls input, .hosted-admin-account-controls select').evaluateAll((elements) => elements.map((element) => {
      const rect = element.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom };
    }));
    expect(Math.max(...controls.map((item) => item.top)) - Math.min(...controls.map((item) => item.top))).toBeLessThan(1);
    expect(Math.max(...controls.map((item) => item.bottom)) - Math.min(...controls.map((item) => item.bottom))).toBeLessThan(1);
    await page.close();
  });

  it('keeps state panels and action rows hidden when the hidden attribute is present', async () => {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    await page.setContent(`
      <style>${css}</style>
      <main class="hosted-admin-content">
        <section class="hosted-admin-state" hidden>正在加载…</section>
        <div class="hosted-admin-bili-actions" hidden><button>立即检查</button></div>
      </main>
    `);

    for (const selector of ['.hosted-admin-state', '.hosted-admin-bili-actions']) {
      expect(await page.locator(selector).evaluate((element) => getComputedStyle(element).display)).toBe('none');
    }
    await page.close();
  });
});
