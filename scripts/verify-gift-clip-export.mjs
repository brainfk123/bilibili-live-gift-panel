import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFile as execFileCallback, spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { createServer as createNetServer } from 'node:net';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { mirrorUiAssets, verifyUiAssetManifest } from './ui-assets.mjs';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');
const execFile = promisify(execFileCallback);
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const artifactDirectory = join(root, 'artifacts', 'gift-clip-export');
const runtimeDirectory = await mkdtemp(join(tmpdir(), 'gift-clip-export-e2e-'));
const portFile = join(runtimeDirectory, 'harness-port.txt');
const stopFile = join(runtimeDirectory, 'harness-stop.txt');
const packagedUiDirectory = join(runtimeDirectory, 'packaged-ui');
const startedProcesses = [];
const forbiddenPorts = new Set(Array.from({ length: 10 }, (_, index) => 12_450 + index));
let browser;

assert.equal(process.env.GIFT_CLIP_FFMPEG_E2E, '1', 'set GIFT_CLIP_FFMPEG_E2E=1 for the real export E2E');
const ffprobe = requireExecutable(process.env.FFPROBE_BIN, 'FFPROBE_BIN');
const fullFFmpeg = findFullFFmpeg(ffprobe);

await rm(artifactDirectory, { recursive: true, force: true });
await mkdir(artifactDirectory, { recursive: true });
const packagedUiManifest = mirrorUiAssets(join(root, 'dist'), packagedUiDirectory);
verifyUiAssetManifest(packagedUiDirectory, packagedUiManifest);
const configEntries = packagedUiManifest.files.filter(({ path }) => /^modules\/ui\/config\/config-entry-[A-Za-z0-9_-]+\.js$/.test(path));
assert.equal(configEntries.length, 1, `packaged UI config entry count=${configEntries.length}`);

try {
  const harness = startProcess('go-harness', 'go', [
    'test', './...', '-run', '^TestGiftClipHarnessServer$', '-count=1', '-v',
  ], {
    cwd: join(root, 'goserver'),
    env: {
      ...process.env,
      GIFT_CLIP_HARNESS_PORT_FILE: portFile,
      GIFT_CLIP_HARNESS_STOP_FILE: stopFile,
      GIFT_CLIP_HARNESS_UI_DIR: packagedUiDirectory,
    },
  });
  const harnessURL = (await waitForFile(portFile, harness, 45_000)).trim();
  const harnessPort = portFromURL(harnessURL);
  assert.ok(!forbiddenPorts.has(harnessPort), `harness selected reserved user port ${harnessPort}`);

  const vitePort = await reserveDynamicPort();
  const vite = startProcess('vite', process.execPath, [
    join(root, 'node_modules', 'vite', 'bin', 'vite.js'),
    '--host', '127.0.0.1', '--port', String(vitePort), '--strictPort', '--clearScreen', 'false',
  ], {
    cwd: root,
    env: { ...process.env, NO_COLOR: '1', VITE_API_PROXY_TARGET: harnessURL },
  });
  const pageURL = `http://127.0.0.1:${vitePort}/tests/fixtures/gift-clip-export.html`;
  await waitForHTTP(pageURL, vite, 30_000);

  browser = await chromium.launch({ headless: true });
  const packagedUi = await verifyPackagedUI(harnessURL, configEntries[0].path, packagedUiManifest.files.length);
  const variants = [];
  for (const kind of ['gif', 'webp', 'effect']) {
    const variantURL = `${pageURL}?kind=${kind}`;
    const baseline = await exportVariant(kind, 'baseline', variantURL, false);
    const stalled = await exportVariant(kind, 'stalled', variantURL, true);
    const baselineFrames = await frameHashes(baseline.output);
    const stalledFrames = await frameHashes(stalled.output);
    assert.deepEqual(stalledFrames, baselineFrames, `${kind}: 180 ms UI stall changed the complete decoded frame sequence`);

    const baselineProbe = await probeExport(baseline.output);
    const stalledProbe = await probeExport(stalled.output);
    assert.deepEqual(stalledProbe, baselineProbe, `${kind}: stall and baseline ffprobe contracts differ`);
    assertExportProbe(kind, baselineProbe);
    variants.push({
      kind,
      outputs: [baseline.output, stalled.output],
      probe: baselineProbe,
      frames: baselineFrames.length,
      frameSequenceSHA256: createHash('sha256').update(JSON.stringify(baselineFrames)).digest('hex'),
      errors: [...baseline.errors, ...stalled.errors],
      overflow: mergeOverflow(baseline.overflow, stalled.overflow),
      screenshots: baseline.screenshots,
    });
  }

  const consoleErrors = variants.flatMap(({ errors }) => errors);
  assert.deepEqual(consoleErrors, [], `browser errors:\n${consoleErrors.join('\n')}`);
  for (const variant of variants) assert.deepEqual(variant.overflow, { document: 0, dialog: 0 }, `${variant.kind} overflow`);
  console.log(JSON.stringify({
    harness: { pid: harness.pid, port: harnessPort },
    vite: { pid: vite.pid, port: vitePort },
    packagedUi,
    variants,
    consoleErrors: consoleErrors.length,
  }, null, 2));
} catch (error) {
  const processLogs = startedProcesses.map(({ name, pid, log }) => `${name} pid=${pid}:\n${log}`).join('\n');
  throw new Error(`${error.message}\n${processLogs}`, { cause: error });
} finally {
  if (browser) await browser.close().catch(() => undefined);
  await writeFile(stopFile, 'stop\n').catch(() => undefined);
  const harness = startedProcesses.find(({ name }) => name === 'go-harness');
  if (harness) await waitForExit(harness.child, 15_000).catch(() => terminateExactProcessTree(harness));
  for (const record of [...startedProcesses].reverse()) await terminateExactProcessTree(record);
  await rm(runtimeDirectory, { recursive: true, force: true });
}

async function exportVariant(kind, name, pageURL, stall) {
  const context = await browser.newContext({
    acceptDownloads: true,
    viewport: { width: 1440, height: 1000 },
    deviceScaleFactor: 1,
  });
  const page = await context.newPage();
  const errors = [];
  const apiResponses = [];
  const apiRequests = [];
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(`console: ${message.text()}`);
  });
  page.on('pageerror', (error) => errors.push(`page: ${error.message}`));
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/gift-clips')) {
      apiRequests.push(`${request.method()} ${request.headers()['content-length'] || 'chunked'} bytes=${request.postDataBuffer()?.length ?? 0} ${request.url()}`);
    }
  });
  page.on('response', (response) => {
    if (new URL(response.url()).pathname.startsWith('/api/gift-clips')) {
      apiResponses.push(`${response.request().method()} ${response.status()} ${response.url()}`);
    }
  });
  const screenshots = [];
  let overflow = { document: 0, dialog: 0 };
  try {
    await page.goto(pageURL, { waitUntil: 'domcontentloaded' });
    await page.locator('.gift-clip-crop-frame').waitFor({ timeout: 30_000 });
    await page.getByRole('button', { name: '确定剪裁并生成' }).waitFor({ state: 'visible' });
    overflow = mergeOverflow(overflow, await readOverflow(page));
    if (!stall) {
      const path = join(artifactDirectory, `${kind}-editing.png`);
      await page.screenshot({ path });
      screenshots.push(path);
    }

    await page.getByRole('button', { name: '确定剪裁并生成' }).click();
    if (stall) await page.evaluate(() => {
      const until = performance.now() + 180;
      while (performance.now() < until) { /* intentional UI stall */ }
    });
    if (!stall) {
      await page.waitForFunction(() => document.querySelector('.gift-clip-status')?.textContent?.includes('正在生成视频'));
      const path = join(artifactDirectory, `${kind}-encoding.png`);
      await page.screenshot({ path });
      screenshots.push(path);
    }
    overflow = mergeOverflow(overflow, await readOverflow(page));

    await page.waitForFunction(
      () => {
        const status = document.querySelector('.gift-clip-status');
        if (status?.classList.contains('is-error')) throw new Error(status.textContent || 'gift clip export failed');
        return status?.textContent?.includes('MP4 已生成');
      },
      undefined,
      { timeout: 90_000 },
    ).catch(async (error) => {
      const status = await page.locator('.gift-clip-status').textContent().catch(() => 'missing status');
      throw new Error(`${error.message}; status=${JSON.stringify(status)}; requests=${JSON.stringify(apiRequests)}; responses=${JSON.stringify(apiResponses)}`);
    });
    if (!stall) {
      const path = join(artifactDirectory, `${kind}-ready.png`);
      await page.screenshot({ path });
      screenshots.push(path);
    }
    overflow = mergeOverflow(overflow, await readOverflow(page));

    const output = join(artifactDirectory, `${kind}-${name}.mp4`);
    const downloadPromise = page.waitForEvent('download', { timeout: 30_000 });
    await page.getByRole('button', { name: '保存 MP4' }).click();
    const download = await downloadPromise;
    const failure = await download.failure();
    assert.equal(failure, null, `${name} download failed: ${failure}`);
    await download.saveAs(output);
    return { output, errors, overflow, screenshots };
  } finally {
    await page.evaluate(() => globalThis.__giftClipExportFixture?.cleanup()).catch(() => undefined);
    await context.close();
  }
}

async function readOverflow(page) {
  return page.evaluate(() => {
    const dialog = document.querySelector('.gift-clip-dialog');
    if (!(dialog instanceof HTMLElement)) throw new Error('gift clip dialog is missing');
    return {
      document: Math.max(0, document.documentElement.scrollWidth - document.documentElement.clientWidth),
      dialog: Math.max(0, dialog.scrollWidth - dialog.clientWidth),
    };
  });
}

async function verifyPackagedUI(harnessURL, configEntryPath, manifestFileCount) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 720 } });
  const errors = [];
  const failedResponses = [];
  const configEntryURL = new URL(configEntryPath, `${harnessURL}/`).href;
  try {
    const manifestResponse = await context.request.get(`${harnessURL}/ui-assets.json`);
    assert.equal(manifestResponse.status(), 200, 'packaged UI manifest route');
    const servedManifest = await manifestResponse.json();
    assert.equal(servedManifest.version, 1);
    assert.equal(servedManifest.files.length, manifestFileCount);

    const obs = await context.newPage();
    const obsConfigRequests = [];
    obs.on('console', (message) => { if (message.type() === 'error') errors.push(`packaged OBS console: ${message.text()}`); });
    obs.on('pageerror', (error) => errors.push(`packaged OBS page: ${error.message}`));
    obs.on('response', (response) => { if (response.status() >= 400) failedResponses.push(`OBS ${response.status()} ${response.url()}`); });
    obs.on('request', (request) => { if (request.url() === configEntryURL) obsConfigRequests.push(request.url()); });
    await obs.goto(`${harnessURL}/?mode=display`, { waitUntil: 'domcontentloaded' });
    await obs.waitForFunction(() => document.body.classList.contains('display-mode'));
    await delay(250);
    assert.deepEqual(obsConfigRequests, [], 'OBS route loaded the configuration-only dynamic chunk');

    const config = await context.newPage();
    config.on('console', (message) => { if (message.type() === 'error') errors.push(`packaged config console: ${message.text()}`); });
    config.on('pageerror', (error) => errors.push(`packaged config page: ${error.message}`));
    config.on('response', (response) => { if (response.status() >= 400) failedResponses.push(`config ${response.status()} ${response.url()}`); });
    const configEntryResponse = config.waitForResponse((response) => response.url() === configEntryURL && response.status() === 200);
    await config.goto(`${harnessURL}/?mode=config`, { waitUntil: 'domcontentloaded' });
    await configEntryResponse;
    await config.waitForFunction(() => document.body.classList.contains('config-mode'));
    assert.deepEqual(failedResponses, [], `packaged UI failed responses:\n${failedResponses.join('\n')}`);
    assert.deepEqual(errors, [], `packaged UI browser errors:\n${errors.join('\n')}\nfailed responses:\n${failedResponses.join('\n')}`);
    return { manifestFiles: manifestFileCount, configEntry: configEntryPath, obsConfigRequests: 0, consoleErrors: errors.length };
  } finally {
    await context.close();
  }
}

function mergeOverflow(left, right) {
  return { document: Math.max(left.document, right.document), dialog: Math.max(left.dialog, right.dialog) };
}

async function frameHashes(path) {
  const { stdout } = await execFile(fullFFmpeg, ['-v', 'error', '-i', path, '-f', 'framemd5', '-'], {
    encoding: 'utf8', maxBuffer: 8 * 1024 * 1024,
  });
  const lines = stdout.replaceAll('\r\n', '\n').split('\n');
  const timebase = lines.find((line) => line.startsWith('#tb 0: '))?.slice('#tb 0: '.length).trim();
  assert.equal(timebase, '1/30');
  const frames = lines.filter((line) => /^0,/.test(line)).map((line) => {
    const fields = line.split(',').map((value) => value.trim());
    assert.equal(fields.length, 6, `unexpected framemd5 row: ${line}`);
    return { pts: Number(fields[2]), duration: Number(fields[3]), size: Number(fields[4]), hash: fields[5] };
  });
  assert.equal(frames.length, 60, `${path} decoded frame count`);
  for (const [index, frame] of frames.entries()) {
    assert.deepEqual(
      { pts: frame.pts, duration: frame.duration },
      { pts: index, duration: 1 },
      `${path} frame ${index} timestamp`,
    );
    assert.match(frame.hash, /^[0-9a-f]{32}$/i, `${path} frame ${index} hash`);
  }
  return frames;
}

async function probeExport(path) {
  const { stdout } = await execFile(ffprobe, ['-v', 'error', '-show_streams', '-show_format', '-of', 'json', path], {
    encoding: 'utf8', maxBuffer: 2 * 1024 * 1024,
  });
  const payload = JSON.parse(stdout);
  const video = payload.streams.filter(({ codec_type: type }) => type === 'video');
  const audio = payload.streams.filter(({ codec_type: type }) => type === 'audio');
  assert.equal(video.length, 1, `video streams=${video.length}`);
  return {
    codec: video[0].codec_name,
    fps: video[0].avg_frame_rate,
    frames: Number(video[0].nb_frames),
    duration: Number(video[0].duration ?? payload.format.duration),
    width: video[0].width,
    height: video[0].height,
    audioStreams: audio.length,
    bitrate: Number(video[0].bit_rate ?? payload.format.bit_rate),
    size: Number(payload.format.size),
  };
}

function assertExportProbe(kind, probe) {
  assert.equal(probe.codec, 'h264', `${kind} codec`);
  assert.equal(probe.fps, '30/1', `${kind} fps`);
  assert.equal(probe.frames, 60, `${kind} frames`);
  assert.equal(probe.width, 320, `${kind} width`);
  assert.equal(probe.height, 180, `${kind} height`);
  assert.equal(probe.audioStreams, 0, `${kind} audio streams`);
  assert.ok(Math.abs(probe.duration - 2) <= 0.05, `${kind} duration=${probe.duration}`);
  assert.ok(Number.isFinite(probe.bitrate) && probe.bitrate > 0, `${kind} bitrate=${probe.bitrate}`);
  assert.ok(Number.isFinite(probe.size) && probe.size >= 1024 && probe.size < 1024 * 1024, `${kind} size=${probe.size}`);
  const targetBitrate = 150_000;
  const targetBytes = targetBitrate * probe.duration / 8;
  const actualBytes = probe.bitrate * probe.duration / 8;
  const minimumBytes = targetBytes * 0.65;
  const maximumBytes = targetBytes * 1.35 + 24 * 1024;
  assert.ok(
    actualBytes >= minimumBytes && actualBytes <= maximumBytes,
    `${kind} video bytes=${actualBytes} from ${probe.bitrate}bit/s, want ${minimumBytes}..${maximumBytes} around unchanged ${targetBitrate}bit/s plus bounded 24KiB startup/GOP overhead`,
  );
}

function startProcess(name, command, args, options) {
  const child = spawn(command, args, {
    ...options,
    windowsHide: true,
    shell: false,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  assert.ok(Number.isSafeInteger(child.pid) && child.pid > 0 && child.pid !== process.pid, `${name} did not start with a safe PID`);
  const record = { name, child, pid: child.pid, log: '' };
  startedProcesses.push(record);
  for (const stream of [child.stdout, child.stderr]) stream.on('data', (chunk) => {
    record.log = `${record.log}${chunk}`.slice(-64 * 1024);
  });
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

async function waitForHTTP(url, record, timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (record.child.exitCode !== null) throw new Error(`${record.name} exited ${record.child.exitCode}:\n${record.log}`);
    try {
      const response = await fetch(url, { cache: 'no-store' });
      if (response.ok) return;
    } catch { /* server is not ready yet */ }
    await delay(100);
  }
  throw new Error(`timed out waiting for ${url}:\n${record.log}`);
}

async function reserveDynamicPort() {
  for (let attempt = 0; attempt < 32; attempt += 1) {
    const port = await new Promise((resolvePort, reject) => {
      const server = createNetServer();
      server.unref();
      server.once('error', reject);
      server.listen(0, '127.0.0.1', () => {
        const address = server.address();
        server.close((error) => error ? reject(error) : resolvePort(address.port));
      });
    });
    if (!forbiddenPorts.has(port)) return port;
  }
  throw new Error('could not reserve a dynamic Vite port outside 12450-12459');
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
    await waitForExit(record.child, 5_000).catch(() => record.child.kill('SIGKILL'));
  }
  await waitForExit(record.child, 5_000).catch(() => undefined);
}

function requireExecutable(value, name) {
  const path = value?.trim();
  assert.ok(path && existsSync(path), `${name} must point to an existing executable`);
  return resolve(path);
}

function findFullFFmpeg(ffprobePath) {
  const candidates = [
    process.env.FFMPEG_FULL_BIN?.trim(),
    join(dirname(ffprobePath), process.platform === 'win32' ? 'ffmpeg.exe' : 'ffmpeg'),
    String.raw`D:\Program Files\ffmpeg\bin\ffmpeg.exe`,
  ].filter(Boolean);
  const path = candidates.find(existsSync);
  assert.ok(path, 'FFMPEG_FULL_BIN or ffmpeg beside FFPROBE_BIN is required');
  return resolve(path);
}

function portFromURL(value) {
  const url = new URL(value);
  assert.equal(url.hostname, '127.0.0.1');
  const port = Number(url.port);
  assert.ok(Number.isSafeInteger(port) && port > 0);
  return port;
}

function delay(milliseconds) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds));
}
