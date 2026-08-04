import { mkdir } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const baseURL = process.env.TUTORIAL_UI_URL ?? 'http://127.0.0.1:12461/?mode=config';
const artifactDir = resolve(process.cwd(), 'artifacts');
await mkdir(artifactDir, { recursive: true });

let config = {
  roomId: '31567150',
  attributes: [],
  rules: [],
  timerRules: [],
  formulaPresets: [],
  settings: {
    fontSize: 48,
    accentColor: '#fb7299',
    showStats: true,
    showConnection: true,
    align: 'center',
    theme: 'dark',
    giftView: 'list',
    panelOpacity: 55,
    showTutorial: true,
    tutorialVersion: 2,
    tutorialCompletedLessons: ['room'],
    autoUpdate: true,
  },
  giftCatalog: [],
  recentGifts: [],
  stats: {},
  log: [],
};

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1 });
const errors = [];
page.on('console', (message) => {
  if (message.type() === 'error') errors.push(`console: ${message.text()}`);
});
page.on('pageerror', (error) => errors.push(`page: ${error.message}`));
await page.addInitScript(() => {
  class QuietEventSource {
    close() {}
  }
  Object.defineProperty(window, 'EventSource', { value: QuietEventSource });
});
await page.route('**/api/**', async (route) => {
  const request = route.request();
  const url = new URL(request.url());
  const json = async (body, status = 200) => route.fulfill({
    status,
    contentType: 'application/json; charset=utf-8',
    body: JSON.stringify(body),
  });
  if (url.pathname === '/api/config') {
    if (request.method() === 'GET') return json(config);
    if (request.method() === 'PUT') {
      config = request.postDataJSON();
      return json({ code: 0 });
    }
  }
  if (url.pathname === '/api/runtime') {
    return json({ code: 0, runtime: { state: 'connected', roomId: '31567150' } });
  }
  if (url.pathname === '/api/auth/status') return json({ code: 0, auth: { state: 'anonymous' } });
  if (url.pathname === '/api/gifts') return json({ code: 0, gifts: [] });
  if (url.pathname === '/api/update') {
    return json({
      code: 0,
      update: {
        state: 'up-to-date', currentVersion: '0.1.4', latestVersion: '0.1.4', message: '当前已经是最新版本。',
        autoUpdate: true, restartRequired: false,
      },
    });
  }
  if (url.pathname === '/api/formula/preview') {
    const payload = request.postDataJSON();
    const value = Number(payload.attributeValue) || 0;
    return json({ code: 0, result: payload.context === 'timer' ? Math.max(value - 60, 0) : value + 60 });
  }
  if (url.pathname === '/api/blind-box') return json({ code: 0, blindBox: null, requiresLogin: false });
  return json({ code: 0 });
});

await page.goto(baseURL, { waitUntil: 'networkidle' });
await page.locator('.guide-attribute-add').click({ force: true });
await page.locator('.attribute-workbench').waitFor();
await page.screenshot({ path: resolve(artifactDir, 'tutorial-workbench-overview.png'), fullPage: false });

const followDelta = await page.evaluate(async () => {
  const panel = document.querySelector('.attribute-workbench-panel:not([hidden])');
  const target = document.querySelector('.guide-overtime-template');
  const focus = document.querySelector('.tour-focus');
  if (!(panel instanceof HTMLElement) || !(target instanceof HTMLElement) || !(focus instanceof HTMLElement)) return 999;
  panel.style.paddingBottom = '900px';
  panel.scrollTop = 120;
  panel.dispatchEvent(new Event('scroll', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 260));
  const targetTop = target.getBoundingClientRect().top;
  const focusTop = focus.getBoundingClientRect().top;
  panel.scrollTop = 0;
  panel.style.paddingBottom = '';
  panel.dispatchEvent(new Event('scroll', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 260));
  return Math.abs((targetTop - 8) - focusTop);
});
if (followDelta > 3) throw new Error(`Tutorial focus did not follow scrolled content: ${followDelta}px`);

await page.locator('.guide-overtime-template').click({ force: true });
if (await page.locator('.workbench-lesson-card').first().isVisible()) {
  throw new Error('Completed lesson card should not remain visible');
}
await page.locator('.guide-add-gift').click({ force: true });
await page.locator('.gift-picker-drawer:not([hidden])').waitFor();
if (await page.locator('.attribute-workbench-actions').isVisible()) {
  throw new Error('Attribute save footer should be hidden while choosing gifts');
}
await page.locator('.gift-choice').filter({ hasText: '666' }).first().click({ force: true });
await page.getByRole('button', { name: '确认选择（1）' }).waitFor();
await page.screenshot({ path: resolve(artifactDir, 'tutorial-gift-picker.png'), fullPage: false });
await page.getByRole('button', { name: '确认选择（1）' }).click({ force: true });
await page.locator('.gift-picker-drawer').waitFor({ state: 'hidden' });
if (!await page.locator('.attribute-workbench-actions').isVisible()) {
  throw new Error('Attribute save footer should return after confirming gifts');
}
await page.locator('.quick-rule-builder').first().waitFor();
const beginnerOperations = await page.locator('.quick-rule-operation option').evaluateAll((options) => (
  options.map((option) => ({ value: option.value, text: option.textContent?.trim() }))
));
for (const expected of ['add', 'subtract', 'set', 'reset', 'price', 'priceSubtract', 'random', 'randomSubtract', 'advanced']) {
  if (!beginnerOperations.some((option) => option.value === expected)) {
    throw new Error(`Missing beginner rule operation: ${expected}`);
  }
}
await page.locator('.quick-rule-operation').first().selectOption('add');
await page.locator('.quick-rule-limit .setting-switch-input').first().check({ force: true });
await page.locator('input[data-field-label="最高不超过"]').first().fill('3600');
await page.getByRole('button', { name: '退出训练' }).click({ force: true });
await page.screenshot({ path: resolve(artifactDir, 'tutorial-workbench-rules.png'), fullPage: false });
await page.locator('.rule-advanced-settings > summary').first().click();
await page.screenshot({ path: resolve(artifactDir, 'tutorial-workbench-rule-advanced.png'), fullPage: false });

await page.getByRole('tab', { name: /定时器/ }).click();
await page.locator('.guide-add-timer').click();
await page.screenshot({ path: resolve(artifactDir, 'tutorial-workbench-timer.png'), fullPage: false });

await page.getByRole('tab', { name: '输出与预览' }).click();
await page.screenshot({ path: resolve(artifactDir, 'tutorial-workbench-output.png'), fullPage: false });

await page.setViewportSize({ width: 760, height: 1000 });
await page.getByRole('tab', { name: '概览' }).click();
await page.screenshot({ path: resolve(artifactDir, 'tutorial-workbench-narrow.png'), fullPage: false });

const checks = await page.evaluate(() => ({
  pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
  modalOverflow: document.querySelector('.attribute-workbench')?.scrollWidth
    - document.querySelector('.attribute-workbench')?.clientWidth,
  visiblePanels: Array.from(document.querySelectorAll('.attribute-workbench-panel'))
    .filter((panel) => !panel.hidden).length,
  tabs: Array.from(document.querySelectorAll('.attribute-workbench-tab')).map((tab) => tab.textContent?.trim()),
  presetInsideAdvanced: Boolean(document.querySelector('.rule-advanced-settings .guide-save-preset')),
}));

await page.setViewportSize({ width: 1440, height: 1000 });
await page.getByRole('button', { name: '关闭', exact: true }).click();
await page.locator('.training-toggle').click();
await page.locator('.training-center').waitFor();
await page.screenshot({ path: resolve(artifactDir, 'tutorial-training-center.png'), fullPage: false });
const lessonCount = await page.locator('.training-center-lesson').count();

await browser.close();
if (errors.length > 0) throw new Error(`Browser errors:\n${errors.join('\n')}`);
if (checks.pageOverflow > 1 || checks.modalOverflow > 1) throw new Error(`Horizontal overflow: ${JSON.stringify(checks)}`);
if (checks.visiblePanels !== 1) throw new Error(`Expected one visible panel: ${JSON.stringify(checks)}`);
if (!checks.presetInsideAdvanced) throw new Error('Save preset button must stay inside advanced rules');
if (checks.tabs.length !== 4 || lessonCount !== 10) throw new Error(`Workbench structure mismatch: ${JSON.stringify({ ...checks, lessonCount })}`);
console.log(JSON.stringify({ ...checks, lessonCount, followDelta, screenshots: 8 }, null, 2));
