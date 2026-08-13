import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { existsSync } from 'node:fs';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

if (process.argv.includes('--self-test')) {
  await runVerifierSelfTests();
  process.exit(0);
}

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
let browserRecord;
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
  const packagedAssets = await readPackagedAssets(harnessURL);

  browserServer = await chromium.launchServer({ headless: true });
  const browserProcess = browserServer.process();
  assert.ok(browserProcess?.pid && browserProcess.pid > 0, 'Playwright Chromium did not provide a safe PID');
  const browserPID = browserProcess.pid;
  browserRecord = { name: 'playwright-chromium', child: browserProcess, pid: browserPID, log: '' };
  startedProcesses.push(browserRecord);
  browser = await chromium.connect(browserServer.wsEndpoint());

  const global = await verifyGlobalOBS(harnessURL);
  const scoped = await verifyScopedOBS(harnessURL);
  const config = await verifyConfigAndClear(harnessURL, packagedAssets.configEntryURL);
  const staticAssets = await verifyPackagedAssets(harnessURL, packagedAssets);
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
  const harness = startedProcesses.find(({ name }) => name === 'go-harness');
  const { cleanup, errors } = await cleanupOwnedRuntime({
    closers: [
      { name: 'browser client', close: () => browser?.close() },
      { name: 'browser server', close: () => browserServer?.close() },
    ],
    stopFile,
    processes: startedProcesses,
    runtimeDirectory,
  });
  cleanup.goCommandPIDGone = !harness || !isProcessAlive(harness.pid);
  cleanup.browserPIDGone = !browserRecord || !isProcessAlive(browserRecord.pid);
  if (report) report.cleanup = cleanup;
  assert.ok(cleanup.goCommandPIDGone, `Go harness command PID ${harness?.pid} survived cleanup`);
  assert.ok(cleanup.browserPIDGone, `Playwright Chromium PID ${browserRecord?.pid} survived cleanup`);
  if (errors.length > 0) throw new AggregateError(errors, 'frontend display verifier cleanup failed');
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
    await expectTextParts(page.locator('.blind-box-summary'), ['3 个', '3 元', '3.8 元'], 'scoped OBS projected summary');
    await expectText(page.locator('.blind-box-profit'), '+0.8 元', 'scoped OBS projected profit');
    const names = await page.locator('.blind-box-person strong').allTextContents();
    assert.deepEqual(names, ['甲观众', '乙观众'], 'scoped OBS preserves backend viewer order');
    const profits = await page.locator('.blind-box-row-profit').allTextContents();
    assert.deepEqual(profits, ['+1.6 元', '-0.8 元'], 'scoped OBS uses backend-projected scope profit');
    const path = join(artifactDirectory, 'scoped-obs.png');
    await page.screenshot({ path });
    screenshots.push(path);
    return { ...evidence, screenshots, overflow: await readOverflow(page) };
  } finally {
    await context.close();
  }
}

async function verifyConfigAndClear(harnessURL, configEntryURL) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  const evidence = observePage(page, 'config');
  const screenshots = [];
  try {
    const configEntryResponse = page.waitForResponse((response) => response.url() === configEntryURL);
    await page.goto(`${harnessURL}/?mode=config&page=data`, { waitUntil: 'domcontentloaded' });
    assert.equal((await configEntryResponse).status(), 200, `packaged config dynamic chunk ${configEntryURL}`);
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
    await expectTextParts(page.locator('.blind-box-scope-summary'), ['宝藏盲盒', '2 位观众', '3 个', '投入 3 元', '开出 3.8 元', '净盈亏 +0.8 元'], 'config scoped summary');
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

async function readPackagedAssets(harnessURL) {
  const manifestResponse = await fetch(`${harnessURL}/ui-assets.json`, { cache: 'no-store' });
  assert.equal(manifestResponse.status, 200, 'packaged UI manifest route');
  const manifest = await manifestResponse.json();
  assert.equal(manifest.version, 1, 'packaged manifest version');
  assert.ok(Array.isArray(manifest.files) && manifest.files.length > 0, 'packaged manifest files');
  const configEntries = manifest.files.filter(({ path }) => /^modules\/ui\/config\/config-entry-[A-Za-z0-9_-]+\.js$/.test(path));
  assert.equal(configEntries.length, 1, 'packaged config dynamic chunk count');
  return { manifest, configEntry: configEntries[0].path, configEntryURL: new URL(configEntries[0].path, `${harnessURL}/`).href };
}

async function verifyPackagedAssets(harnessURL, packagedAssets) {
  const { manifest, configEntry } = packagedAssets;
  const httpFailures = [];
  for (const { path } of manifest.files) {
    const response = await fetch(new URL(path, `${harnessURL}/`).href, { cache: 'no-store' });
    if (response.status >= 400) httpFailures.push(`${response.status} ${path}`);
  }
  return { manifestFiles: manifest.files.length, configEntry, httpFailures };
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

async function expectTextParts(locator, texts, label) {
  await locator.waitFor({ state: 'visible', timeout: 30_000 });
  const actual = await locator.textContent();
  for (const text of texts) assert.ok(actual?.includes(text), `${label}: expected ${JSON.stringify(text)} in ${JSON.stringify(actual)}`);
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
  if (!record || !isProcessAlive(record.pid)) return;
  assert.ok(Number.isSafeInteger(record.pid) && record.pid > 0 && record.pid !== process.pid, `unsafe ${record.name} PID`);
  if (process.platform === 'win32') {
    spawnSync('taskkill.exe', ['/pid', String(record.pid), '/t', '/f'], { windowsHide: true, stdio: 'ignore' });
  } else {
    record.child?.kill('SIGTERM');
  }
  if (record.child) await waitForExit(record.child, 5_000).catch(() => undefined);
  await waitForPIDGone(record.pid, 5_000);
}

async function stopOwnedProcess(record) {
  if (!record || !isProcessAlive(record.pid)) return;
  await terminateExactProcessTree(record);
  await waitForPIDGone(record.pid, 5_000);
}

async function waitForPIDGone(pid, timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (!isProcessAlive(pid)) return;
    await delay(50);
  }
  throw new Error(`owned process PID ${pid} did not exit within ${timeout}ms`);
}

async function cleanupOwnedRuntime({
  closers,
  stopFile,
  processes,
  runtimeDirectory,
  writeStopFile = (path) => writeFile(path, 'stop\n'),
  stopProcess = stopOwnedProcess,
  isAlive = isProcessAlive,
  removeRuntime = (path) => rm(path, { recursive: true, force: true }),
}) {
  const errors = [];
  const attempt = async (label, action) => {
    try {
      await action();
    } catch (error) {
      errors.push(new Error(`${label}: ${error instanceof Error ? error.message : String(error)}`, { cause: error }));
    }
  };
  for (const { name, close } of closers) await attempt(`close ${name}`, close);
  await attempt('write private stop file', () => writeStopFile(stopFile));
  for (const record of [...processes].reverse()) await attempt(`stop owned ${record.name} PID ${record.pid}`, () => stopProcess(record));
  const gone = {};
  for (const record of processes) {
    gone[record.name] = !isAlive(record.pid);
    if (!gone[record.name]) errors.push(new Error(`owned ${record.name} PID ${record.pid} survived cleanup`));
  }
  await attempt('remove private runtime directory', () => removeRuntime(runtimeDirectory));
  return { cleanup: { ownedPIDsGone: gone, removedTempRoot: runtimeDirectory }, errors };
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

async function runVerifierSelfTests() {
  for (const stopFails of [false, true]) {
    const owned = { name: 'recorded-browser', pid: 901, child: undefined };
    const calls = [];
    const live = new Set([901]);
    const result = await cleanupOwnedRuntime({
      closers: [{ name: 'failing-browser-close', close: async () => { throw new Error('close failed'); } }],
      stopFile: 'private-stop',
      processes: [owned],
      runtimeDirectory: 'private-runtime',
      writeStopFile: async (path) => calls.push(`stop-file:${path}`),
      stopProcess: async (record) => {
        calls.push(`stop:${record.pid}`);
        if (stopFails) throw new Error('stop failed');
        live.delete(record.pid);
      },
      isAlive: (pid) => live.has(pid),
      removeRuntime: async (path) => calls.push(`rm:${path}`),
    });
    assert.deepEqual(calls, ['stop-file:private-stop', 'stop:901', 'rm:private-runtime'], `cleanup call order stopFails=${stopFails}`);
    assert.ok(result.errors.length >= (stopFails ? 3 : 1), `cleanup errors stopFails=${stopFails}`);
    assert.equal(calls.some((call) => call.includes('902')), false, 'cleanup never touches unrecorded PID');
    assert.equal(calls.at(-1), 'rm:private-runtime', 'cleanup removes the private runtime directory after close/stop failures');
  }
  console.log('frontend display verifier self-test: PASS');
}
