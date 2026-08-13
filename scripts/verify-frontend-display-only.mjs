import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { existsSync } from 'node:fs';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const artifactDirectory = join(root, 'artifacts', 'frontend-display-only', new Date().toISOString().replaceAll(':', '-'));
const runtimeDirectory = await mkdtemp(join(tmpdir(), 'frontend-display-only-e2e-'));
const portFile = join(runtimeDirectory, 'harness-port.json');
const stopFile = join(runtimeDirectory, 'harness-stop');
const forbiddenPorts = new Set(Array.from({ length: 10 }, (_, index) => 12_450 + index));
const startedProcesses = [];
let browserServer;
let browser;
let report;

await mkdir(artifactDirectory, { recursive: true });

try {
  const harness = startProcess('go-harness', 'go', [
    'test', './...', '-run', '^TestFrontendDisplayOnlyHarnessServer$', '-count=1', '-v',
  ], {
    cwd: join(root, 'goserver'),
    env: {
      ...process.env,
      FRONTEND_DISPLAY_E2E_PORT_FILE: portFile,
      FRONTEND_DISPLAY_E2E_STOP_FILE: stopFile,
    },
  });
  const harnessInfo = JSON.parse((await waitForFile(portFile, harness, 45_000)).trim());
  assert.equal(typeof harnessInfo.url, 'string', 'harness port file URL');
  assert.ok(Number.isSafeInteger(harnessInfo.pid) && harnessInfo.pid > 0, 'harness port file PID');
  const harnessURL = new URL(harnessInfo.url).href.replace(/\/$/, '');
  const harnessPort = Number(new URL(harnessURL).port);
  assert.ok(!forbiddenPorts.has(harnessPort), `harness selected reserved user port ${harnessPort}`);

  browserServer = await chromium.launchServer({ headless: true });
  const browserProcess = browserServer.process();
  assert.ok(browserProcess?.pid && browserProcess.pid > 0, 'Playwright Chromium did not provide a safe PID');
  const browserPID = browserProcess.pid;
  browser = await chromium.connect(browserServer.wsEndpoint());

  const global = await verifyGlobalOBS(harnessURL);
  const scoped = await verifyScopedOBS(harnessURL);
  const config = await verifyConfigAndClear(harnessURL);
  const staticAssets = await verifyPackagedAssets(harnessURL);
  const httpFailures = [...global.httpFailures, ...scoped.httpFailures, ...config.httpFailures, ...staticAssets.httpFailures];
  const consoleErrors = [...global.consoleErrors, ...scoped.consoleErrors, ...config.consoleErrors];
  assert.deepEqual(httpFailures, [], `HTTP responses >= 400:\n${httpFailures.join('\n')}`);
  assert.deepEqual(consoleErrors, [], `browser console errors:\n${consoleErrors.join('\n')}`);
  for (const [name, overflow] of Object.entries({ global: global.overflow, scoped: scoped.overflow, config: config.overflow })) {
    assert.deepEqual(overflow, { document: 0, dialog: 0 }, `${name} horizontal overflow`);
  }
  report = {
    harness: { commandPID: harness.pid, serverPID: harnessInfo.pid, port: harnessPort },
    browser: { pid: browserPID },
    screenshots: [...global.screenshots, ...scoped.screenshots, ...config.screenshots],
    staticAssets,
    httpResponsesAtOrAbove400: httpFailures.length,
    consoleErrors: consoleErrors.length,
    overflow: { global: global.overflow, scoped: scoped.overflow, config: config.overflow },
  };
} catch (error) {
  const processLogs = startedProcesses.map(({ name, pid, log }) => `${name} pid=${pid}:\n${log}`).join('\n');
  throw new Error(`${error.message}\n${processLogs}`, { cause: error });
} finally {
  if (browser) await browser.close().catch(() => undefined);
  if (browserServer) await browserServer.close().catch(() => undefined);
  await writeFile(stopFile, 'stop\n').catch(() => undefined);
  const harness = startedProcesses.find(({ name }) => name === 'go-harness');
  if (harness) await waitForExit(harness.child, 15_000).catch(() => terminateExactProcessTree(harness));
  for (const record of [...startedProcesses].reverse()) await terminateExactProcessTree(record);
  const cleanup = {
    goCommandPIDGone: !harness || !isProcessAlive(harness.pid),
    browserPIDGone: !browserServer?.process()?.pid || !isProcessAlive(browserServer.process().pid),
    removedTempRoot: runtimeDirectory,
  };
  if (report) report.cleanup = cleanup;
  assert.ok(cleanup.goCommandPIDGone, `Go harness command PID ${harness?.pid} survived cleanup`);
  assert.ok(cleanup.browserPIDGone, 'Playwright Chromium PID survived cleanup');
  await rm(runtimeDirectory, { recursive: true, force: true });
}

console.log(JSON.stringify(report, null, 2));

async function verifyGlobalOBS(harnessURL) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 720 } });
  const page = await context.newPage();
  const evidence = observePage(page, 'global OBS');
  const screenshots = [];
  try {
    await page.goto(`${harnessURL}/?mode=display&view=blind-box`, { waitUntil: 'domcontentloaded' });
    await page.locator('.blind-box-panel').waitFor({ timeout: 30_000 });
    await page.locator('.blind-box-row').first().waitFor({ timeout: 30_000 });
    const names = await page.locator('.blind-box-person strong').allTextContents();
    assert.deepEqual(names, ['甲观众', '乙观众'], 'global OBS preserves backend leaderboard order');
    await expectText(page.locator('.blind-box-summary'), '4 个', 'global OBS blind-box summary');
    const path = join(artifactDirectory, 'global-obs.png');
    await page.screenshot({ path });
    screenshots.push(path);
    return { ...evidence, screenshots, overflow: await readOverflow(page) };
  } finally {
    await context.close();
  }
}

async function verifyScopedOBS(harnessURL) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 720 } });
  const page = await context.newPage();
  const evidence = observePage(page, 'scoped OBS');
  const screenshots = [];
  try {
    await page.goto(`${harnessURL}/?mode=display&view=blind-box&blindBox=35800`, { waitUntil: 'domcontentloaded' });
    await page.locator('.blind-box-panel').waitFor({ timeout: 30_000 });
    await page.locator('.blind-box-row').first().waitFor({ timeout: 30_000 });
    await expectText(page.locator('.blind-box-title'), '宝藏盲盒', 'scoped OBS title');
    await expectText(page.locator('.blind-box-summary'), '3 个', 'scoped OBS projected count');
    const profits = await page.locator('.blind-box-row-profit').allTextContents();
    assert.equal(profits.length, 2, 'scoped OBS viewer count');
    assert.notDeepEqual(profits, ['+1.6 元', '-0.4 元'], 'scoped OBS uses projected scope values instead of global totals');
    const path = join(artifactDirectory, 'scoped-obs.png');
    await page.screenshot({ path });
    screenshots.push(path);
    return { ...evidence, screenshots, overflow: await readOverflow(page) };
  } finally {
    await context.close();
  }
}

async function verifyConfigAndClear(harnessURL) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  const evidence = observePage(page, 'config');
  const screenshots = [];
  try {
    await page.goto(`${harnessURL}/?mode=config&page=data`, { waitUntil: 'domcontentloaded' });
    await page.locator('.contribution-section').waitFor({ timeout: 30_000 });
    await page.getByRole('tab', { name: '盲盒盈亏' }).click();
    await page.locator('.blind-box-scope-option[data-gift-id="35800"]').waitFor({ state: 'attached', timeout: 30_000 });
    await expectText(page.locator('.blind-box-scope-summary'), '全部盲盒', 'config global summary');
    const globalPath = join(artifactDirectory, 'config-global.png');
    await page.screenshot({ path: globalPath });
    screenshots.push(globalPath);

    await page.locator('.blind-box-scope-picker summary').click();
    await page.locator('.blind-box-scope-option[data-gift-id="35800"]').waitFor({ state: 'visible', timeout: 30_000 });
    await page.locator('.blind-box-scope-option[data-gift-id="35800"]').click();
    await page.waitForFunction(() => document.querySelector('.blind-box-scope-summary')?.textContent?.includes('宝藏盲盒'));
    await expectText(page.locator('.blind-box-scope-summary'), '3 个', 'config scoped summary');
    const scopedPath = join(artifactDirectory, 'config-scoped.png');
    await page.screenshot({ path: scopedPath });
    screenshots.push(scopedPath);

    page.once('dialog', (dialog) => dialog.accept());
    const clearResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/contributions' && response.request().method() === 'DELETE');
    await page.locator('.contribution-clear').click();
    assert.equal((await clearResponse).status(), 200, 'real contribution DELETE status');
    await page.waitForFunction(() => document.querySelector('.contribution-viewer-count')?.textContent?.includes('0 位观众'));
    const leaderboardResponse = await context.request.get(`${harnessURL}/api/blind-box/leaderboard`);
    assert.equal(leaderboardResponse.status(), 200, 'leaderboard GET after clear');
    const cleared = await leaderboardResponse.json();
    assert.deepEqual(cleared.leaderboard.summary, { viewerCount: 0, blindBoxCount: 0, cost: 0, value: 0, profit: 0, unpricedCount: 0 }, 'real leaderboard is empty after DELETE');
    assert.deepEqual(cleared.leaderboard.viewers, [], 'real leaderboard viewers are empty after DELETE');
    const clearedPath = join(artifactDirectory, 'config-cleared.png');
    await page.screenshot({ path: clearedPath });
    screenshots.push(clearedPath);
    return { ...evidence, screenshots, overflow: await readOverflow(page) };
  } finally {
    await context.close();
  }
}

async function verifyPackagedAssets(harnessURL) {
  const manifestResponse = await fetch(`${harnessURL}/ui-assets.json`, { cache: 'no-store' });
  assert.equal(manifestResponse.status, 200, 'packaged UI manifest route');
  const manifest = await manifestResponse.json();
  assert.equal(manifest.version, 1, 'packaged manifest version');
  assert.ok(Array.isArray(manifest.files) && manifest.files.length > 0, 'packaged manifest files');
  const httpFailures = [];
  const configEntries = manifest.files.filter(({ path }) => /^modules\/ui\/config\/config-entry-[A-Za-z0-9_-]+\.js$/.test(path));
  assert.equal(configEntries.length, 1, 'packaged config dynamic chunk count');
  for (const { path } of manifest.files) {
    const response = await fetch(new URL(path, `${harnessURL}/`).href, { cache: 'no-store' });
    if (response.status >= 400) httpFailures.push(`${response.status} ${path}`);
  }
  return { manifestFiles: manifest.files.length, configEntry: configEntries[0].path, httpFailures };
}

function observePage(page, name) {
  const consoleErrors = [];
  const httpFailures = [];
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(`${name}: ${message.text()}`); });
  page.on('pageerror', (error) => consoleErrors.push(`${name}: ${error.message}`));
  page.on('response', (response) => { if (response.status() >= 400) httpFailures.push(`${name}: ${response.status()} ${response.url()}`); });
  return { consoleErrors, httpFailures };
}

async function expectText(locator, text, label) {
  await locator.waitFor({ state: 'visible', timeout: 30_000 });
  const actual = await locator.textContent();
  assert.ok(actual?.includes(text), `${label}: expected ${JSON.stringify(text)} in ${JSON.stringify(actual)}`);
}

async function readOverflow(page) {
  return page.evaluate(() => {
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]');
    return {
      document: Math.max(0, document.documentElement.scrollWidth - document.documentElement.clientWidth),
      dialog: dialog ? Math.max(0, dialog.scrollWidth - dialog.clientWidth) : 0,
    };
  });
}

function startProcess(name, command, args, options) {
  const child = spawn(command, args, { ...options, windowsHide: true, shell: false, stdio: ['ignore', 'pipe', 'pipe'] });
  assert.ok(Number.isSafeInteger(child.pid) && child.pid > 0 && child.pid !== process.pid, `${name} did not start with a safe PID`);
  const record = { name, child, pid: child.pid, log: '' };
  startedProcesses.push(record);
  for (const stream of [child.stdout, child.stderr]) stream.on('data', (chunk) => { record.log = `${record.log}${chunk}`.slice(-64 * 1024); });
  return record;
}

async function waitForFile(path, record, timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (existsSync(path)) return readFile(path, 'utf8');
    if (record.child.exitCode !== null) throw new Error(`${record.name} exited ${record.child.exitCode}:\n${record.log}`);
    await delay(50);
  }
  throw new Error(`timed out waiting for ${path}:\n${record.log}`);
}

async function waitForExit(child, timeout) {
  if (child.exitCode !== null) return child.exitCode;
  return Promise.race([
    new Promise((resolveExit) => child.once('exit', (code) => resolveExit(code))),
    delay(timeout).then(() => { throw new Error('process exit timeout'); }),
  ]);
}

async function terminateExactProcessTree(record) {
  if (!record || record.child.exitCode !== null) return;
  assert.ok(Number.isSafeInteger(record.pid) && record.pid > 0 && record.pid !== process.pid, `unsafe ${record.name} PID`);
  if (process.platform === 'win32') {
    spawnSync('taskkill.exe', ['/pid', String(record.pid), '/t', '/f'], { windowsHide: true, stdio: 'ignore' });
  } else {
    record.child.kill('SIGTERM');
  }
  await waitForExit(record.child, 5_000).catch(() => undefined);
}

function isProcessAlive(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error?.code !== 'ESRCH';
  }
}

function delay(milliseconds) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds));
}
