import type { AddressInfo } from 'node:net';
import { fileURLToPath } from 'node:url';

import { expect, test, type Browser, type Page } from 'playwright/test';
import { createServer, type ViteDevServer } from 'vite';

const preview = {
  id: 12, expiresAt: '2030-01-02T00:00:00Z', reused: false,
  counts: { attributes: 4, rules: 3, activities: 1, giftTargetPanels: 1, giftTargetItems: 5 },
  warnings: ['已规范化空白名称'], ignored: ['本地素材路径：Hosted 不支持'], roomSuggestion: '12345',
  source: { appVersion: '0.4.7', configurationSchemaVersion: 5 },
  units: [
    { id: 'attribute:score', kind: 'attribute', name: '积分', attributeIds: ['score'], ruleIds: [], timerRuleIds: [], formulaPresetIds: [], activityIds: [], displaySceneIds: [], giftTargetPanelIds: [], giftIds: [], cropPresetIds: [], compatibility: { status: 'complete', reasonCodes: [] }, selected: true },
    { id: 'attribute:bonus', kind: 'attribute', name: '加成', attributeIds: ['bonus'], ruleIds: [], timerRuleIds: [], formulaPresetIds: [], activityIds: [], displaySceneIds: [], giftTargetPanelIds: [], giftIds: [], cropPresetIds: [], compatibility: { status: 'complete', reasonCodes: [] }, selected: false },
    { id: 'activity:legacy', kind: 'activity', name: '旧活动', attributeIds: [], ruleIds: [], timerRuleIds: [], formulaPresetIds: [], activityIds: ['legacy'], displaySceneIds: ['legacy-scene'], giftTargetPanelIds: [], giftIds: [], cropPresetIds: ['legacy-crop'], compatibility: { status: 'partial', reasonCodes: ['crop_presets_unsupported', 'display_scenes_unsupported'] }, selected: false },
    { id: 'timer:legacy', kind: 'timer', name: '旧定时器', attributeIds: [], ruleIds: [], timerRuleIds: ['legacy-timer'], formulaPresetIds: [], activityIds: [], displaySceneIds: [], giftTargetPanelIds: [], giftIds: [], cropPresetIds: [], compatibility: { status: 'incompatible', reasonCodes: ['timer_rules_unsupported'] }, selected: false },
  ],
  groups: [{ id: 'group:score', unitIds: ['attribute:bonus', 'activity:legacy'], reasons: [{ kind: 'shared-attribute', referenceId: 'bonus' }] }],
  conflicts: [{ id: 'conflict:score', importedUnitIds: ['attribute:score', 'attribute:bonus'], hostedUnitIds: ['attribute:hosted'], suggestedNames: { 'attribute:score': '积分（从 EXE 导入）' } }],
  selection: { unitIds: ['attribute:score'], conflictChoices: {}, includeGeneralSettings: false, includeRoomSuggestion: false },
  generalSettings: { configurationMode: 'simple' },
  generalSettingsCompatibility: { status: 'complete', reasonCodes: [] }, canConfirm: false,
};
const scoreSelector = 'eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiJzY29yZSJ9';

const harnessPage = `<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/hosted/shell.css"></head><body><div id="root"></div><script type="module">
  import { mountMigrationView } from '/src/hosted/migration.ts';
  const preview = ${JSON.stringify(preview)};
  const root = document.querySelector('#root');
  let mounted; let releaseUpload;
  async function cleanup() { if (releaseUpload) { releaseUpload(preview); releaseUpload = undefined; } await mounted?.dispose(); mounted = undefined; }
  async function mount(state) {
    await cleanup();
    const api = {
      previewMigration() { return new Promise((resolve) => { releaseUpload = resolve; }); },
      selectMigration(_id, selection) { return Promise.resolve({ ...preview, selection }); },
      beginLogin() { return Promise.resolve({ challengeId: 'proof', qrImage: 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', expiresAt: preview.expiresAt }); },
      pollLogin() { return Promise.resolve({ status: 'verified', expiresAt: preview.expiresAt }); },
      cancelLogin() { return Promise.resolve(); },
      applyMigration() { return Promise.resolve({ id: 12, status: 'applied', rollbackExpiresAt: '2030-01-08T00:00:00Z', ...(state === 'applied-without-links' ? { obsReissueAvailable: true, obsReissueRequired: true } : state === 'applied-no-outputs' ? {} : { obsReissueAvailable: true, obsLinks: [{ outputId: '${scoreSelector}', name: '积分卡片', url: 'https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${scoreSelector}#token=one-time' }] }) }); },
    };
    mounted = mountMigrationView(root, api, { onConfiguration() {} });
    if (state === 'preview') mounted.flow.acceptPreview(preview);
    if (state === 'unsupported-settings') mounted.flow.acceptPreview({ ...preview, blindBoxDisplayCompatibility: { status: 'partial', reasonCodes: ['blind_box_display_unsupported'] } });
    if (state === 'busy') void mounted.flow.preview({ name: 'private.json', size: 2, text: async () => '{}' });
    if (state === 'error') { mounted.flow.acceptPreview(preview); mounted.flow.reportFailure(new TypeError('Failed to fetch RAW')); }
    if (state === 'applied' || state === 'applied-without-links' || state === 'applied-no-outputs') { const ready = { ...preview, conflicts: [], canConfirm: true }; mounted.flow.acceptPreview(ready); mounted.flow.confirmReplacement(true); await mounted.flow.apply(); }
  }
  window.__migrationHarness = { mount, cleanup };
</script></body></html>`;

const settingsRacePage = `<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/hosted/shell.css"></head><body><div id="root"></div><script type="module">
  import { mountMigrationSettingsView } from '/src/hosted/user/settings/migration-center.ts';
  const root = document.querySelector('#root');
  const historyCalls = [];
  let mounted;
  let beforeUnloadGuards = 0;
  const add = window.addEventListener.bind(window);
  const remove = window.removeEventListener.bind(window);
  window.addEventListener = (type, listener, options) => { if (type === 'beforeunload') beforeUnloadGuards += 1; add(type, listener, options); };
  window.removeEventListener = (type, listener, options) => { if (type === 'beforeunload') beforeUnloadGuards -= 1; remove(type, listener, options); };
  const api = {
    migrationHistory(signal) { return new Promise((resolve) => historyCalls.push({ resolve, signal })); },
    getMigration(id) { return Promise.resolve({ id, status: 'rolled_back' }); },
  };
  window.__settingsRaceHarness = {
    mount() { mounted = mountMigrationSettingsView(root, api, { onExit() {} }); },
    historyCount() { return historyCalls.length; },
    historyAborted(index) { return historyCalls[index]?.signal.aborted ?? false; },
    resolveHistory(index, jobs) { historyCalls[index]?.resolve(jobs); },
    guardCount() { return beforeUnloadGuards; },
    async cleanup() { await mounted?.dispose(); mounted = undefined; },
  };
</script></body></html>`;

declare global {
  interface Window {
    __migrationHarness: { mount(state: 'preview' | 'unsupported-settings' | 'busy' | 'error' | 'applied' | 'applied-without-links' | 'applied-no-outputs'): Promise<void>; cleanup(): Promise<void> };
    __settingsRaceHarness: { mount(): void; historyCount(): number; historyAborted(index: number): boolean; resolveHistory(index: number, jobs: unknown[]): void; guardCount(): number; cleanup(): Promise<void> };
  }
}

let server: ViteDevServer;
let baseURL: string;

test.beforeAll(async () => {
  server = await createServer({
    root: fileURLToPath(new URL('..', import.meta.url)), logLevel: 'error', server: { host: '127.0.0.1', port: 0, strictPort: false },
    plugins: [{
      name: 'hosted-migration-layout-harness',
      configureServer(devServer) {
        devServer.middlewares.use((request, response, next) => {
          if (request.url === '/__hosted-migration-layout-test') { response.statusCode = 200; response.setHeader('Content-Type', 'text/html; charset=UTF-8'); response.end(harnessPage); return; }
          if (request.url === '/__hosted-settings-race-test') { response.statusCode = 200; response.setHeader('Content-Type', 'text/html; charset=UTF-8'); response.end(settingsRacePage); return; }
          if (request.url === '/__hosted-account-test') { response.statusCode = 200; response.setHeader('Content-Type', 'text/html; charset=UTF-8'); response.end('<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="hosted-app"></div><script>window.EventSource=class { addEventListener(){} close(){} };</script><script type="module" src="/src/hosted/main.ts"></script></body></html>'); return; }
          if (request.url === '/api/bootstrap') { response.statusCode = 200; response.setHeader('Content-Type', 'application/json'); response.end('{"csrfToken":"csrf"}'); return; }
          if (request.url === '/api/auth/session') { response.statusCode = 200; response.setHeader('Content-Type', 'application/json'); response.end('{"authenticated":true,"accountScope":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}'); return; }
          if (request.url === '/api/migrations') { response.statusCode = 200; response.setHeader('Content-Type', 'application/json'); response.end('{"jobs":[{"id":7,"status":"applied","createdAt":"2026-08-29T00:00:00Z","appliedAt":"2026-08-29T00:01:00Z","rollbackExpiresAt":"2030-01-08T00:00:00Z","obsReissueAvailable":true}]}'); return; }
          next();
        });
      },
    }],
  });
  await server.listen();
  const address = server.httpServer?.address() as AddressInfo;
  baseURL = `http://127.0.0.1:${address.port}`;
});

test.afterAll(async () => { await server?.close(); });

async function openHarness(browser: Browser, viewport: { width: number; height: number }): Promise<Page> {
  const page = await browser.newPage({ viewport });
  await page.goto(`${baseURL}/__hosted-migration-layout-test`);
  await page.waitForFunction(() => typeof window.__migrationHarness?.mount === 'function');
  return page;
}

test('first-login prompt can be skipped while Settings remains a real route to the migration center', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.goto(`${baseURL}/__hosted-account-test`);
  await expect(page.getByLabel('本地配置迁移提示')).toBeVisible();
  await page.getByRole('button', { name: '暂时跳过' }).click();
  await expect(page.getByLabel('本地配置迁移提示')).toHaveCount(0);
  await page.reload();
  await expect(page.getByLabel('本地配置迁移提示')).toHaveCount(0);
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await expect(page.getByRole('heading', { name: '设置', exact: true })).toBeVisible();
  await expect(page.getByText('可回滚的迁移')).toBeVisible();
  await page.getByRole('button', { name: '查看并回滚' }).click();
  await expect(page.getByRole('button', { name: '扫码重新签发 OBS 链接' })).toBeVisible();
  await expect(page.getByText(/轮换并撤销之前的 OBS 链接/)).toBeVisible();
  await page.getByRole('button', { name: '返回设置' }).click();
  await expect(page.getByRole('heading', { name: '设置', exact: true })).toBeVisible();
  const settingsInteractive = await page.locator('.hosted-migration-settings button,.hosted-migration-settings input,.hosted-migration-settings select,.hosted-migration-settings a').evaluateAll((elements) => elements.map((element) => { const rect = element.getBoundingClientRect(); return { left: rect.left, right: rect.right }; }));
  for (const item of settingsInteractive) { expect(item.left).toBeGreaterThanOrEqual(0); expect(item.right).toBeLessThanOrEqual(390); }
  await page.getByRole('button', { name: '打开迁移中心' }).click();
  await expect(page.getByRole('heading', { name: '从本地 EXE 迁移' })).toBeVisible();
  await expect(page.getByLabel('选择迁移 JSON 文件')).toBeVisible();
  expect(await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: innerWidth, storage: { ...localStorage }, url: location.href }))).toEqual(expect.objectContaining({ width: 390, viewport: 390, storage: { 'hosted.migration.prompt.dismissed.v1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA': 'true' }, url: `${baseURL}/__hosted-account-test` }));
  await page.close();
});

test('renders grouped compatibility and conflict controls without desktop or phone overflow', async ({ browser }) => {
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    const page = await openHarness(browser, viewport);
    await page.evaluate(() => window.__migrationHarness.mount('preview'));
    await expect(page.getByText('关联玩法组')).toBeVisible();
    await expect(page.getByText('剪裁预设不受支持（crop_presets_unsupported）')).toBeVisible();
    await expect(page.getByText('定时规则不受支持（timer_rules_unsupported）')).toBeVisible();
    await expect(page.getByLabel('解决冲突 conflict:score')).toBeVisible();
    expect(await page.locator('.hosted-migration-unit[data-compatibility="partial"] input').isDisabled()).toBe(true);
    expect(await page.locator('.hosted-migration-unit[data-compatibility="incompatible"] input').isDisabled()).toBe(true);
    expect(await page.locator('.hosted-migration-group .hosted-migration-unit[data-compatibility="complete"] input').isDisabled()).toBe(true);
    const metrics = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: innerWidth, actionAlignment: getComputedStyle(document.querySelector('.hosted-migration-actions')!).justifyContent, selectAppearance: getComputedStyle(document.querySelector('.hosted-migration-conflict select')!).backgroundColor, buttons: [...document.querySelectorAll<HTMLButtonElement>('.hosted-migration-center button')].map((button) => { const style = getComputedStyle(button); return { justify: style.justifyContent, left: Number.parseFloat(style.paddingLeft), right: Number.parseFloat(style.paddingRight) }; }), interactive:[...document.querySelectorAll<HTMLElement>('.hosted-migration-center button,.hosted-migration-center input,.hosted-migration-center select,.hosted-migration-center a')].map((element)=>{const rect=element.getBoundingClientRect();return{left:rect.left,right:rect.right}}) }));
    expect(metrics.width).toBe(metrics.viewport);
    expect(metrics.actionAlignment).toBe('flex-end');
    expect(metrics.selectAppearance).not.toBe('rgba(0, 0, 0, 0)');
    for (const item of metrics.buttons) { expect(item.justify).toBe('center'); expect(Math.abs(item.left - item.right)).toBeLessThan(1); }
    for(const item of metrics.interactive){expect(item.left).toBeGreaterThanOrEqual(0);expect(item.right).toBeLessThanOrEqual(viewport.width);}
    await page.close();
  }
});

test('renders unsupported blind-box settings as an explicit disabled choice', async ({ browser }) => {
  const page = await openHarness(browser, { width: 390, height: 844 });
  await page.evaluate(() => window.__migrationHarness.mount('unsupported-settings'));
  await expect(page.getByText('盲盒排行榜数据暂不受 Hosted 支持（blind_box_display_unsupported）')).toBeVisible();
  await expect(page.getByRole('checkbox', { name: /导入通用设置/ })).toBeEnabled();
  await expect(page.getByRole('checkbox', { name: /导入盲盒排行榜样式/ })).toBeDisabled();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await page.close();
});

test('styles loading, network failure, applied progress and OBS checklist with reduced motion', async ({ browser }) => {
  const page = await openHarness(browser, { width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await page.evaluate(() => window.__migrationHarness.mount('busy'));
  await expect(page.getByRole('alert')).toContainText('正在与服务器同步');
  expect(await page.locator('.hosted-migration-spinner').first().evaluate((element) => getComputedStyle(element).animationName)).not.toBe('none');
  await page.emulateMedia({ reducedMotion: 'reduce' });
  expect(await page.locator('.hosted-migration-spinner').first().evaluate((element) => getComputedStyle(element).animationName)).toBe('none');
  await page.evaluate(() => window.__migrationHarness.mount('error'));
  await expect(page.getByRole('alert')).toHaveText('网络连接失败，请检查网络后重试');
  await page.evaluate(() => window.__migrationHarness.mount('applied'));
  await expect(page.getByText('迁移已应用', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: '逐项替换 OBS 链接' })).toBeVisible();
  await expect(page.getByRole('link', { name: /https:\/\/host\.example\/obs\/A/ })).toHaveAttribute('href', `https://host.example/obs/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA?output=${scoreSelector}#token=one-time`);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await page.close();
});

test('applied jobs without in-memory OBS links expose proof-gated rotation recovery', async ({ browser }) => {
  const page = await openHarness(browser, { width: 390, height: 844 });
  await page.evaluate(() => window.__migrationHarness.mount('applied-without-links'));
  await expect(page.getByText('迁移已应用', { exact: true })).toBeVisible();
  await expect(page.getByText(/OBS 链接需重新签发/)).toBeVisible();
  await expect(page.getByRole('button', { name: '扫码重新签发 OBS 链接' })).toBeVisible();
  await expect(page.getByText(/轮换并撤销旧链接/)).toBeVisible();
  await page.close();
});

test('applied jobs without OBS outputs do not expose an impossible reissue action', async ({ browser }) => {
  const page = await openHarness(browser, { width: 390, height: 844 });
  await page.evaluate(() => window.__migrationHarness.mount('applied-no-outputs'));
  await expect(page.getByText('此配置没有可签发的 OBS 输出。')).toBeVisible();
  await expect(page.getByRole('button', { name: '扫码重新签发 OBS 链接' })).toHaveCount(0);
  await page.close();
});

test('opening migration invalidates a slow parent history response before it can replace the child', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.goto(`${baseURL}/__hosted-settings-race-test`);
  await page.waitForFunction(() => typeof window.__settingsRaceHarness?.mount === 'function');
  await page.evaluate(() => window.__settingsRaceHarness.mount());
  await expect.poll(() => page.evaluate(() => window.__settingsRaceHarness.historyCount())).toBe(1);
  await page.getByRole('button', { name: '打开迁移中心' }).click();
  await expect(page.getByRole('heading', { name: '从本地 EXE 迁移' })).toBeVisible();
  await expect.poll(() => page.evaluate(() => window.__settingsRaceHarness.historyAborted(0))).toBe(true);
  await page.evaluate(() => window.__settingsRaceHarness.resolveHistory(0, [{ id: 7, status: 'pending', createdAt: '2026-08-29T00:00:00Z', expiresAt: '2030-01-02T00:00:00Z' }]));
  await expect(page.getByRole('heading', { name: '从本地 EXE 迁移' })).toBeVisible();
  expect(await page.evaluate(() => window.__settingsRaceHarness.guardCount())).toBe(1);
  await page.evaluate(() => window.__settingsRaceHarness.cleanup());
  await page.close();
});

test('returning from a terminal resumed job disposes the child and refetches history', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.goto(`${baseURL}/__hosted-settings-race-test`);
  await page.waitForFunction(() => typeof window.__settingsRaceHarness?.mount === 'function');
  await page.evaluate(() => window.__settingsRaceHarness.mount());
  await expect.poll(() => page.evaluate(() => window.__settingsRaceHarness.historyCount())).toBe(1);
  await page.evaluate(() => window.__settingsRaceHarness.resolveHistory(0, [{ id: 7, status: 'pending', createdAt: '2026-08-29T00:00:00Z', expiresAt: '2030-01-02T00:00:00Z' }]));
  await expect(page.getByRole('button', { name: '继续查看' })).toBeVisible();
  await page.getByRole('button', { name: '继续查看' }).click();
  await expect(page.getByText('迁移状态：rolled_back')).toBeVisible();
  await page.getByRole('button', { name: '返回设置' }).click();
  await expect.poll(() => page.evaluate(() => window.__settingsRaceHarness.historyCount())).toBe(2);
  await page.evaluate(() => window.__settingsRaceHarness.resolveHistory(1, []));
  await expect(page.getByRole('heading', { name: '设置', exact: true })).toBeVisible();
  await expect(page.getByText('等待应用')).toHaveCount(0);
  expect(await page.evaluate(() => window.__settingsRaceHarness.guardCount())).toBe(0);
  await page.close();
});

test('settings defensively hides pending history whose preview has expired', async ({ browser }) => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.goto(`${baseURL}/__hosted-settings-race-test`);
  await page.waitForFunction(() => typeof window.__settingsRaceHarness?.mount === 'function');
  await page.evaluate(() => window.__settingsRaceHarness.mount());
  await expect.poll(() => page.evaluate(() => window.__settingsRaceHarness.historyCount())).toBe(1);
  await page.evaluate(() => window.__settingsRaceHarness.resolveHistory(0, [{ id: 7, status: 'pending', createdAt: '2000-01-01T00:00:00Z', expiresAt: '2000-01-02T00:00:00Z' }]));
  await expect(page.getByRole('heading', { name: '设置', exact: true })).toBeVisible();
  await expect(page.getByText('等待应用')).toHaveCount(0);
  await page.close();
});
